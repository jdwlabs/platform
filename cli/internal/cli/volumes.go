package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/jdwlabs/platform/internal/display"
	"github.com/jdwlabs/platform/internal/k8s"
	"github.com/jdwlabs/platform/internal/longhorn"
)

// Field names are the command's contract with agents, so they are listed once
// and both the default and --full sets are derived from that single list.
var volumeFields = []string{
	"name", "state", "class", "size", "robustness",
	"lastPodRefAt", "claimedBy", "pvPhase", "recordedPvc", "recordedNs",
}

var (
	defaultVolumeFields = []string{"name", "state", "class", "size"}
	reclaimFields       = []string{"name", "class", "size", "lastPodRefAt"}
	volumeClasses       = []string{"orphaned", "claimed", "attached", "other"}
)

func newClusterVolumesCmd(g *Globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "volumes",
		Short: "List Longhorn volumes and reclaim orphaned ones",
		Long: `Report Longhorn volume state, classified against live claims, and reclaim
volumes that nothing claims any more.

Classification never trusts status.kubernetesStatus.pvcName: that field records
the claim a volume was created for and survives on later generations, so
several volumes of one StatefulSet carry the same name. A claim is resolved from
the PersistentVolumeClaim's spec.volumeName instead.

Run with no subcommand to list every volume.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVolumeList(cmd, g, &volumeListOptions{Class: "all"})
		},
	}
	cmd.AddCommand(newVolumesListCmd(g))
	cmd.AddCommand(newVolumesReclaimCmd(g))
	return cmd
}

type volumeListOptions struct {
	Class  string
	Fields string
	Full   bool
}

func newVolumesListCmd(g *Globals) *cobra.Command {
	opts := &volumeListOptions{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Longhorn volumes with their reclaim classification",
		Example: `  platformctl cluster volumes list
  platformctl cluster volumes list --class orphaned
  platformctl cluster volumes list --fields name,claimedBy,lastPodRefAt`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVolumeList(cmd, g, opts)
		},
	}
	cmd.Flags().StringVar(&opts.Class, "class", "all",
		"filter by classification: all, "+strings.Join(volumeClasses, ", "))
	cmd.Flags().StringVar(&opts.Fields, "fields", "",
		"comma-separated fields to report instead of the default set")
	cmd.Flags().BoolVar(&opts.Full, "full", false, "report every field")
	return cmd
}

type volumeReclaimOptions struct {
	Names       []string
	AllOrphaned bool
	Confirm     bool
	MinAge      time.Duration
}

func newVolumesReclaimCmd(g *Globals) *cobra.Command {
	opts := &volumeReclaimOptions{}
	cmd := &cobra.Command{
		Use:   "reclaim",
		Short: "Delete orphaned Longhorn volumes (requires --confirm)",
		Long: `Delete Longhorn volumes that nothing claims. Destructive, so it refuses
every path that is not explicit: --confirm deletes, --dry-run previews, and
neither is assumed. Nothing is ever read from stdin.

A volume is only ever deleted when live state proves it unclaimed: detached, no
PersistentVolumeClaim whose spec.volumeName names it, and no Bound
PersistentVolume. --name targets are checked against the same rules and refused
if they fail, which is why "delete everything unmatched" is not offered.

--min-age is measured from status.kubernetesStatus.lastPodRefAt and applies to
--all-orphaned only. A volume with no recorded reference has an unknown age and
is skipped unless --min-age is 0 or it is named explicitly.`,
		Example: `  platformctl cluster volumes reclaim --all-orphaned --dry-run
  platformctl cluster volumes reclaim --all-orphaned --confirm
  platformctl cluster volumes reclaim --name pvc-ae4ffd70-... --confirm`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVolumeReclaim(cmd, g, opts)
		},
	}
	cmd.Flags().StringArrayVar(&opts.Names, "name", nil,
		"volume to reclaim, repeatable; each is still verified against live claims")
	cmd.Flags().BoolVar(&opts.AllOrphaned, "all-orphaned", false,
		"select every volume classified orphaned")
	cmd.Flags().BoolVar(&opts.Confirm, "confirm", false, "actually delete the selected volumes")
	cmd.Flags().DurationVar(&opts.MinAge, "min-age", time.Hour,
		"with --all-orphaned, skip volumes referenced by a pod more recently than this")
	bindDryRun(cmd, g)
	return cmd
}

func runVolumeList(cmd *cobra.Command, g *Globals, opts *volumeListOptions) error {
	out := cmd.OutOrStdout()

	fields, err := resolveVolumeFields(opts.Fields, opts.Full)
	if err != nil {
		return reportCLIError(out, err, "Run `platformctl cluster volumes list --help` for the field list")
	}
	if !validVolumeClass(opts.Class) {
		err := fmt.Errorf("unknown --class %s; valid: all, %s", opts.Class, strings.Join(volumeClasses, ", "))
		return reportCLIError(out, err, "Run `platformctl cluster volumes list --class orphaned` to see reclaim candidates")
	}

	cands, err := loadVolumeCandidates(cmd.Context())
	if err != nil {
		return reportCLIError(out, err, "Run `platformctl cluster status` to check Longhorn itself")
	}

	shown := cands
	if opts.Class != "all" {
		shown = filterVolumeClass(cands, longhorn.Class(opts.Class))
	}

	if g.JSON {
		return emitVolumeEvents(out, g, "list", shown, fields, volumeCountLine(cands))
	}

	if err := display.ToonScalar(out, "count", volumeCountLine(cands)); err != nil {
		return err
	}
	if err := display.ToonTable(out, "volumes", fields, volumeRows(shown, fields)); err != nil {
		return err
	}
	if len(shown) == 0 {
		return display.ToonScalar(out, "result",
			fmt.Sprintf("0 volumes classed %s of %d total", opts.Class, len(cands)))
	}
	return display.ToonList(out, "help", []string{
		"Run `platformctl cluster volumes list --full` for every field",
		"Run `platformctl cluster volumes reclaim --all-orphaned --dry-run` to preview a reclaim",
	})
}

func runVolumeReclaim(cmd *cobra.Command, g *Globals, opts *volumeReclaimOptions) error {
	out := cmd.OutOrStdout()

	// Every flag conflict is settled before a single cluster read, so a
	// malformed invocation can never half-execute.
	if opts.Confirm && g.DryRun {
		return reportCLIError(out,
			fmt.Errorf("--confirm and --dry-run are mutually exclusive"),
			"Run with --dry-run to preview, then re-run with --confirm to delete")
	}
	if !opts.Confirm && !g.DryRun {
		return reportCLIError(out,
			fmt.Errorf("reclaim needs --confirm to delete or --dry-run to preview"),
			"Run `platformctl cluster volumes reclaim --all-orphaned --dry-run` first, then re-run with --confirm")
	}
	if opts.AllOrphaned && len(opts.Names) > 0 {
		return reportCLIError(out,
			fmt.Errorf("--all-orphaned and --name are mutually exclusive"),
			"Run with --name for a specific volume, or --all-orphaned for every unclaimed one")
	}
	if !opts.AllOrphaned && len(opts.Names) == 0 {
		return reportCLIError(out,
			fmt.Errorf("reclaim needs --all-orphaned or at least one --name"),
			"Run `platformctl cluster volumes list --class orphaned` to see what is reclaimable")
	}

	cands, err := loadVolumeCandidates(cmd.Context())
	if err != nil {
		return reportCLIError(out, err, "Run `platformctl cluster status` to check Longhorn itself")
	}

	selected, skipped, refused := selectReclaimTargets(cands, opts)

	mode := "delete"
	if g.DryRun {
		mode = "dry-run"
	}

	var deleted []longhorn.Candidate
	var deleteErr error
	if opts.Confirm {
		dc, err := volumeDynamicClient()
		if err != nil {
			return reportCLIError(out, err, "Check KUBECONFIG points at the cluster")
		}
		for _, c := range selected {
			if err := longhorn.DeleteVolume(cmd.Context(), dc, c.Name); err != nil {
				deleteErr = err
				break
			}
			deleted = append(deleted, c)
		}
	}

	orphanTotal := len(filterVolumeClass(cands, longhorn.ClassOrphaned))
	summary := reclaimResultLine(mode, selected, deleted, refused, len(cands), orphanTotal)

	if g.JSON {
		reported := selected
		table := "reclaim"
		if opts.Confirm {
			reported = deleted
			table = "deleted"
		}
		if err := emitVolumeEvents(out, g, table, reported, reclaimFields, summary); err != nil {
			return err
		}
	} else {
		if err := writeReclaimReport(out, mode, opts, selected, deleted, skipped, refused, summary); err != nil {
			return err
		}
	}

	if deleteErr != nil {
		return deleteErr
	}
	if len(refused) > 0 {
		return fmt.Errorf("%d named volume(s) are not reclaimable", len(refused))
	}
	return nil
}

func writeReclaimReport(out io.Writer, mode string, opts *volumeReclaimOptions,
	selected, deleted, skipped, refused []longhorn.Candidate, summary string) error {
	if err := display.ToonScalar(out, "mode", mode); err != nil {
		return err
	}
	table, rows := "reclaim", selected
	if opts.Confirm {
		table, rows = "deleted", deleted
	}
	if err := display.ToonTable(out, table, reclaimFields, volumeRows(rows, reclaimFields)); err != nil {
		return err
	}
	if len(skipped) > 0 {
		if err := display.ToonTable(out, "skipped", []string{"name", "reason"},
			volumeRows(skipped, []string{"name", "reason"})); err != nil {
			return err
		}
	}
	if len(refused) > 0 {
		if err := display.ToonTable(out, "refused", []string{"name", "class", "reason"},
			volumeRows(refused, []string{"name", "class", "reason"})); err != nil {
			return err
		}
	}
	if err := display.ToonScalar(out, "result", summary); err != nil {
		return err
	}
	if mode == "dry-run" && len(selected) > 0 {
		return display.ToonList(out, "help", []string{
			fmt.Sprintf("Re-run with --confirm to delete these %d volume(s)", len(selected)),
		})
	}
	if len(refused) > 0 {
		return display.ToonList(out, "help", []string{
			"Run `platformctl cluster volumes list --fields name,class,claimedBy` to see what claims them",
		})
	}
	return nil
}

// selectReclaimTargets splits the classified volumes into what will be deleted,
// what was excluded by age, and what was asked for but must be refused.
func selectReclaimTargets(cands []longhorn.Candidate, opts *volumeReclaimOptions) (selected, skipped, refused []longhorn.Candidate) {
	byName := map[string]longhorn.Candidate{}
	for _, c := range cands {
		byName[c.Name] = c
	}

	if len(opts.Names) > 0 {
		for _, name := range opts.Names {
			c, ok := byName[name]
			if !ok {
				refused = append(refused, longhorn.Candidate{
					Volume: longhorn.Volume{Name: name},
					Class:  longhorn.ClassOther,
					Reason: "no such volume in " + longhorn.SystemNamespace,
				})
				continue
			}
			if c.Class != longhorn.ClassOrphaned {
				refused = append(refused, c)
				continue
			}
			selected = append(selected, c)
		}
		return selected, skipped, refused
	}

	now := time.Now()
	for _, c := range filterVolumeClass(cands, longhorn.ClassOrphaned) {
		if opts.MinAge <= 0 {
			selected = append(selected, c)
			continue
		}
		ref, err := time.Parse(time.RFC3339, c.LastPodRefAt)
		if err != nil {
			c.Reason = "lastPodRefAt is unset — age unknown; name it explicitly or pass --min-age 0"
			skipped = append(skipped, c)
			continue
		}
		if age := now.Sub(ref); age < opts.MinAge {
			c.Reason = fmt.Sprintf("last pod reference %s ago is newer than --min-age %s",
				age.Round(time.Minute), opts.MinAge)
			skipped = append(skipped, c)
			continue
		}
		selected = append(selected, c)
	}
	return selected, skipped, refused
}

func reclaimResultLine(mode string,
	selected, deleted, refused []longhorn.Candidate, total, orphanTotal int) string {
	if len(selected) == 0 && len(refused) == 0 {
		return fmt.Sprintf("0 candidates — %d orphaned volumes of %d total", orphanTotal, total)
	}
	if mode == "dry-run" {
		if len(selected) == 0 {
			return fmt.Sprintf("0 candidates — %d orphaned volumes of %d total", orphanTotal, total)
		}
		return fmt.Sprintf("would delete %d volume(s) reclaiming %s — nothing was mutated",
			len(selected), longhorn.FormatBytes(sumActualSize(selected)))
	}
	if len(deleted) == 0 {
		return fmt.Sprintf("0 deleted and %d refused", len(refused))
	}
	return fmt.Sprintf("deleted %d volume(s) reclaiming %s with %d refused",
		len(deleted), longhorn.FormatBytes(sumActualSize(deleted)), len(refused))
}

func sumActualSize(cands []longhorn.Candidate) int64 {
	var total int64
	for _, c := range cands {
		total += c.ActualSize
	}
	return total
}

func loadVolumeCandidates(ctx context.Context) ([]longhorn.Candidate, error) {
	kc, err := volumeKubeClient()
	if err != nil {
		return nil, err
	}
	dc, err := volumeDynamicClient()
	if err != nil {
		return nil, err
	}
	vols, err := longhorn.ListVolumes(ctx, dc)
	if err != nil {
		return nil, err
	}
	bindings, err := longhorn.ResolveBindings(ctx, kc)
	if err != nil {
		return nil, err
	}
	cands := longhorn.Classify(vols, bindings)
	sort.Slice(cands, func(i, j int) bool { return cands[i].Name < cands[j].Name })
	return cands, nil
}

func volumeKubeClient() (kubernetes.Interface, error) {
	if testKubeClient != nil {
		return testKubeClient, nil
	}
	kc, err := k8s.NewClient()
	if err != nil {
		return nil, fmt.Errorf("kube client: %w", err)
	}
	return kc, nil
}

func volumeDynamicClient() (dynamic.Interface, error) {
	if testDynamicClient != nil {
		return testDynamicClient, nil
	}
	dc, err := k8s.NewDynamic()
	if err != nil {
		return nil, fmt.Errorf("dynamic client: %w", err)
	}
	return dc, nil
}

func resolveVolumeFields(csv string, full bool) ([]string, error) {
	if full && csv != "" {
		return nil, fmt.Errorf("--fields and --full are mutually exclusive")
	}
	if full {
		return volumeFields, nil
	}
	if csv == "" {
		return defaultVolumeFields, nil
	}
	known := map[string]bool{}
	for _, f := range volumeFields {
		known[f] = true
	}
	parts := strings.Split(csv, ",")
	fields := make([]string, 0, len(parts))
	for _, p := range parts {
		f := strings.TrimSpace(p)
		if f == "" {
			continue
		}
		if !known[f] {
			return nil, fmt.Errorf("unknown field %s; valid: %s", f, strings.Join(volumeFields, ", "))
		}
		fields = append(fields, f)
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("--fields is empty; valid: %s", strings.Join(volumeFields, ", "))
	}
	return fields, nil
}

func validVolumeClass(class string) bool {
	if class == "all" {
		return true
	}
	for _, c := range volumeClasses {
		if c == class {
			return true
		}
	}
	return false
}

func filterVolumeClass(cands []longhorn.Candidate, class longhorn.Class) []longhorn.Candidate {
	out := make([]longhorn.Candidate, 0, len(cands))
	for _, c := range cands {
		if c.Class == class {
			out = append(out, c)
		}
	}
	return out
}

func volumeCountLine(cands []longhorn.Candidate) string {
	counts := map[longhorn.Class]int{}
	for _, c := range cands {
		counts[c.Class]++
	}
	// Slash-separated rather than comma-separated: a comma is the TOON delimiter,
	// so a scalar containing one has to be quoted and the line gets noisier.
	return fmt.Sprintf("%d total (%d orphaned / %d claimed / %d attached / %d other)",
		len(cands), counts[longhorn.ClassOrphaned], counts[longhorn.ClassClaimed],
		counts[longhorn.ClassAttached], counts[longhorn.ClassOther])
}

func volumeRows(cands []longhorn.Candidate, fields []string) [][]string {
	rows := make([][]string, 0, len(cands))
	for _, c := range cands {
		row := make([]string, 0, len(fields))
		for _, f := range fields {
			row = append(row, volumeFieldValue(c, f))
		}
		rows = append(rows, row)
	}
	return rows
}

func volumeFieldValue(c longhorn.Candidate, field string) string {
	switch field {
	case "name":
		return c.Name
	case "state":
		return c.State
	case "class":
		return string(c.Class)
	case "size":
		return longhorn.FormatBytes(c.ActualSize)
	case "robustness":
		return c.Robustness
	case "lastPodRefAt":
		return c.LastPodRefAt
	case "claimedBy":
		return c.ClaimedBy
	case "pvPhase":
		return c.PVPhase
	case "recordedPvc":
		return c.RecordedPVC
	case "recordedNs":
		return c.RecordedNS
	case "reason":
		return c.Reason
	default:
		return ""
	}
}

// emitVolumeEvents mirrors the TOON report onto the newline-delimited event
// stream the repo's --json contract defines, so an agent parsing events sees the
// same rows and the same aggregate line.
func emitVolumeEvents(out io.Writer, g *Globals, phaseName string, cands []longhorn.Candidate, fields []string, summary string) error {
	em := NewEmitter(out, g.JSON)
	if g.Session != nil {
		em.SetSession(g.Session)
	}
	for _, c := range cands {
		detail := map[string]string{}
		for _, f := range fields {
			detail[f] = volumeFieldValue(c, f)
		}
		status := "info"
		if c.Class == longhorn.ClassOrphaned {
			status = "broken"
		}
		em.Emit(Event{
			Phase:   "volumes",
			Name:    c.Name,
			Status:  status,
			Message: fmt.Sprintf("class=%s state=%s size=%s", c.Class, c.State, longhorn.FormatBytes(c.ActualSize)),
			Detail:  detail,
		})
	}
	em.Emit(Event{Phase: "volumes", Name: phaseName, Status: "ok", Message: summary})
	return nil
}
