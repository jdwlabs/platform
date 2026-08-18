package cli

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jdwlabs/platform/internal/display"
	"github.com/jdwlabs/platform/internal/longhorn"
	"github.com/jdwlabs/platform/internal/truenas"
)

// Field names are the command's contract with agents, so they are listed once
// and both the default and --full sets are derived from that single list.
var truenasFields = []string{
	"name", "kind", "class", "size",
	"storageClass", "dataset", "target", "extent", "share",
	"sessions", "claimedBy", "pv", "pvPhase", "objects", "reason",
}

var (
	defaultTruenasFields = []string{"name", "kind", "class", "size"}
	truenasReclaimFields = []string{"name", "kind", "class", "size", "objects"}
)

// truenasGlobals are the connection settings both subcommands share.
type truenasGlobals struct {
	StorageClass string
	CAFile       string
	SkipVerify   bool
}

func newTrueNASVolumesCmd(g *Globals) *cobra.Command {
	shared := &truenasGlobals{StorageClass: "all"}

	cmd := &cobra.Command{
		Use:   "truenas",
		Short: "List TrueNAS-backed volumes and reclaim orphaned ones",
		Long: `Report the objects the democratic-csi drivers leave on TrueNAS, classified
against live claims, and reclaim the ones nothing references any more.

Both TrueNAS storage classes use reclaimPolicy: Retain, so deleting a PVC
deletes nothing on the NAS. One truenas-iscsi PVC leaves a zvol, an iSCSI
extent, an iSCSI target and the target-to-extent mapping behind; one truenas-nfs
PVC leaves a dataset and its NFS export. All of it is invisible to kubectl.

Classification never trusts a NAS object's own name. Provisioned objects are
named after the PVC UID they were created for, and that name outlives the PV,
the PVC and the workload. Two things prove storage is still live, and both are
read from the other side of the relationship: a PersistentVolume whose CSI
volume handle, volume attributes, NFS path or iSCSI IQN names it, and an open
iSCSI session on a target that exports it.

The iSCSI objects are joined by numeric ID rather than by name, so a target
named for one volume can be mapped to an extent that exports another. Every
linkage here is resolved through those IDs, and a candidate whose target also
exports something else is refused rather than deleted.

Run with no subcommand to list everything.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTrueNASList(cmd, g, shared, &truenasListOptions{Class: "all"})
		},
	}
	cmd.PersistentFlags().StringVar(&shared.StorageClass, "storage-class", "all",
		"limit to one storage class: all, "+strings.Join(truenas.Classes(), ", "))
	cmd.PersistentFlags().StringVar(&shared.CAFile, "truenas-ca-file", "",
		"PEM bundle the TrueNAS certificate must chain to")
	cmd.PersistentFlags().BoolVar(&shared.SkipVerify, "truenas-insecure-skip-tls-verify", false,
		"accept the NAS certificate without verifying it (a stock TrueNAS ships a self-signed one)")

	cmd.AddCommand(newTrueNASListCmd(g, shared))
	cmd.AddCommand(newTrueNASReclaimCmd(g, shared))
	return cmd
}

type truenasListOptions struct {
	Class  string
	Fields string
	Full   bool
}

func newTrueNASListCmd(g *Globals, shared *truenasGlobals) *cobra.Command {
	opts := &truenasListOptions{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List TrueNAS-backed volumes with their reclaim classification",
		Example: `  platformctl cluster volumes truenas list
  platformctl cluster volumes truenas list --class orphaned
  platformctl cluster volumes truenas list --storage-class truenas-iscsi --full`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTrueNASList(cmd, g, shared, opts)
		},
	}
	cmd.Flags().StringVar(&opts.Class, "class", "all",
		"filter by classification: all, "+strings.Join(truenas.Verdicts(), ", "))
	cmd.Flags().StringVar(&opts.Fields, "fields", "",
		"comma-separated fields to report instead of the default set")
	cmd.Flags().BoolVar(&opts.Full, "full", false, "report every field")
	return cmd
}

type truenasReclaimOptions struct {
	Names       []string
	AllOrphaned bool
	Confirm     bool
}

func newTrueNASReclaimCmd(g *Globals, shared *truenasGlobals) *cobra.Command {
	opts := &truenasReclaimOptions{}
	cmd := &cobra.Command{
		Use:   "reclaim",
		Short: "Delete orphaned TrueNAS objects and their Released PVs (requires --confirm)",
		Long: `Delete the TrueNAS objects nothing references any more. Destructive, so it
refuses every path that is not explicit: --confirm deletes, --dry-run previews,
and neither is assumed. Nothing is ever read from stdin.

A candidate is only ever deleted when live state proves it unreferenced: no
PersistentVolume names it, no PersistentVolumeClaim claims such a volume, no
PersistentVolume is Bound, and — for the iSCSI class — the NAS reports no open
session on any target that exports it. If the session list cannot be read at
all, every zvol is refused: unknown liveness is not idle.

--name targets are checked against the same rules and refused if they fail,
which is why "delete everything unmatched" is not offered.

Objects are deleted in dependency order — target-extent mapping, extent, target,
NFS export, dataset, then the Released PersistentVolume — and each one is
re-read and matched against its exact name immediately before the delete.`,
		Example: `  platformctl cluster volumes truenas reclaim --all-orphaned --dry-run
  platformctl cluster volumes truenas reclaim --all-orphaned --confirm
  platformctl cluster volumes truenas reclaim --name pvc-ae4ffd70-... --confirm`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTrueNASReclaim(cmd, g, shared, opts)
		},
	}
	cmd.Flags().StringArrayVar(&opts.Names, "name", nil,
		"volume to reclaim, repeatable; each is still verified against live references")
	cmd.Flags().BoolVar(&opts.AllOrphaned, "all-orphaned", false,
		"select every candidate classified orphaned")
	cmd.Flags().BoolVar(&opts.Confirm, "confirm", false, "actually delete the selected objects")
	bindDryRun(cmd, g)
	return cmd
}

func runTrueNASList(cmd *cobra.Command, g *Globals, shared *truenasGlobals, opts *truenasListOptions) error {
	out := cmd.OutOrStdout()

	fields, err := resolveTrueNASFields(opts.Fields, opts.Full)
	if err != nil {
		return reportCLIError(out, err, "Run `platformctl cluster volumes truenas list --help` for the field list")
	}
	if !validTrueNASVerdict(opts.Class) {
		err := fmt.Errorf("unknown --class %s; valid: all, %s", opts.Class, strings.Join(truenas.Verdicts(), ", "))
		return reportCLIError(out, err,
			"Run `platformctl cluster volumes truenas list --class orphaned` to see reclaim candidates")
	}

	cands, err := loadTrueNASCandidates(cmd.Context(), shared)
	if err != nil {
		return reportCLIError(out, err, truenasConnectHelp(shared))
	}

	shown := cands
	if opts.Class != "all" {
		shown = filterTrueNASClass(cands, truenas.Class(opts.Class))
	}

	if g.JSON {
		return emitTrueNASEvents(out, g, "list", shown, fields, truenasCountLine(cands))
	}

	if err := display.ToonScalar(out, "count", truenasCountLine(cands)); err != nil {
		return err
	}
	if err := display.ToonTable(out, "volumes", fields, truenasRows(shown, fields)); err != nil {
		return err
	}
	if len(shown) == 0 {
		return display.ToonScalar(out, "result",
			fmt.Sprintf("0 candidates classed %s of %d total", opts.Class, len(cands)))
	}
	return display.ToonList(out, "help", []string{
		"Run `platformctl cluster volumes truenas list --full` for every field",
		"Run `platformctl cluster volumes truenas reclaim --all-orphaned --dry-run` to preview a reclaim",
	})
}

func runTrueNASReclaim(cmd *cobra.Command, g *Globals, shared *truenasGlobals, opts *truenasReclaimOptions) error {
	out := cmd.OutOrStdout()

	// Every flag conflict is settled before a single read, so a malformed
	// invocation can never half-execute.
	if opts.Confirm && g.DryRun {
		return reportCLIError(out,
			fmt.Errorf("--confirm and --dry-run are mutually exclusive"),
			"Run with --dry-run to preview, then re-run with --confirm to delete")
	}
	if !opts.Confirm && !g.DryRun {
		return reportCLIError(out,
			fmt.Errorf("reclaim needs --confirm to delete or --dry-run to preview"),
			"Run `platformctl cluster volumes truenas reclaim --all-orphaned --dry-run` first, then re-run with --confirm")
	}
	if opts.AllOrphaned && len(opts.Names) > 0 {
		return reportCLIError(out,
			fmt.Errorf("--all-orphaned and --name are mutually exclusive"),
			"Run with --name for a specific volume, or --all-orphaned for every unreferenced one")
	}
	if !opts.AllOrphaned && len(opts.Names) == 0 {
		return reportCLIError(out,
			fmt.Errorf("reclaim needs --all-orphaned or at least one --name"),
			"Run `platformctl cluster volumes truenas list --class orphaned` to see what is reclaimable")
	}

	sessions, err := openTrueNASSessions(cmd.Context(), shared)
	if err != nil {
		return reportCLIError(out, err, truenasConnectHelp(shared))
	}
	defer sessions.Close()

	cands, err := sessions.Classify(cmd.Context())
	if err != nil {
		return reportCLIError(out, err, truenasConnectHelp(shared))
	}

	selected, refused := selectTrueNASTargets(cands, opts)

	mode := "delete"
	if g.DryRun {
		mode = "dry-run"
	}

	var deleted []truenas.Candidate
	var deleteErr error
	if opts.Confirm {
		deleted, deleteErr = sessions.Reclaim(cmd.Context(), selected)
	}

	orphanTotal := len(filterTrueNASClass(cands, truenas.ClassOrphaned))
	summary := truenasResultLine(mode, selected, deleted, refused, len(cands), orphanTotal)

	if g.JSON {
		reported, table := selected, "reclaim"
		if opts.Confirm {
			reported, table = deleted, "deleted"
		}
		if err := emitTrueNASEvents(out, g, table, reported, truenasReclaimFields, summary); err != nil {
			return err
		}
	} else if err := writeTrueNASReclaimReport(out, mode, opts, selected, deleted, refused, summary); err != nil {
		return err
	}

	if deleteErr != nil {
		return deleteErr
	}
	if len(refused) > 0 {
		return fmt.Errorf("%d named candidate(s) are not reclaimable", len(refused))
	}
	return nil
}

func writeTrueNASReclaimReport(out io.Writer, mode string, opts *truenasReclaimOptions,
	selected, deleted, refused []truenas.Candidate, summary string) error {
	if err := display.ToonScalar(out, "mode", mode); err != nil {
		return err
	}
	table, rows := "reclaim", selected
	if opts.Confirm {
		table, rows = "deleted", deleted
	}
	if err := display.ToonTable(out, table, truenasReclaimFields, truenasRows(rows, truenasReclaimFields)); err != nil {
		return err
	}
	// The per-object plan is the part an operator has to check: it is the
	// difference between deleting one PVC's leftovers and deleting a target
	// that something else is still exporting.
	if err := display.ToonTable(out, "objects", []string{"volume", "step", "kind", "name"},
		truenasObjectRows(rows)); err != nil {
		return err
	}
	if len(refused) > 0 {
		if err := display.ToonTable(out, "refused", []string{"name", "class", "reason"},
			truenasRows(refused, []string{"name", "class", "reason"})); err != nil {
			return err
		}
	}
	if err := display.ToonScalar(out, "result", summary); err != nil {
		return err
	}
	if mode == "dry-run" && len(selected) > 0 {
		return display.ToonList(out, "help", []string{
			fmt.Sprintf("Re-run with --confirm to delete these %d candidate(s)", len(selected)),
		})
	}
	if len(refused) > 0 {
		return display.ToonList(out, "help", []string{
			"Run `platformctl cluster volumes truenas list --fields name,class,reason` to see what holds them",
		})
	}
	return nil
}

// selectTrueNASTargets splits the classified candidates into what will be
// deleted and what was asked for but must be refused. There is no skipped set:
// unlike Longhorn, a NAS object records no last-use timestamp, so there is
// nothing to apply a minimum age to and no --min-age flag pretending otherwise.
func selectTrueNASTargets(cands []truenas.Candidate, opts *truenasReclaimOptions) (selected, refused []truenas.Candidate) {
	if len(opts.Names) > 0 {
		byName := map[string]truenas.Candidate{}
		for _, c := range cands {
			byName[c.Name] = c
		}
		for _, name := range opts.Names {
			c, ok := byName[name]
			if !ok {
				refused = append(refused, truenas.Candidate{
					Name:   name,
					Class:  truenas.ClassOther,
					Reason: "no such object under either driver's dataset parent",
				})
				continue
			}
			if c.Class != truenas.ClassOrphaned {
				refused = append(refused, c)
				continue
			}
			selected = append(selected, c)
		}
		return selected, refused
	}
	return filterTrueNASClass(cands, truenas.ClassOrphaned), nil
}

func truenasResultLine(mode string, selected, deleted, refused []truenas.Candidate, total, orphanTotal int) string {
	if len(selected) == 0 && len(refused) == 0 {
		return fmt.Sprintf("0 candidates — %d orphaned of %d total", orphanTotal, total)
	}
	if mode == "dry-run" {
		if len(selected) == 0 {
			return fmt.Sprintf("0 candidates — %d orphaned of %d total", orphanTotal, total)
		}
		return fmt.Sprintf("would delete %d object(s) across %d candidate(s) reclaiming %s — nothing was mutated",
			countTrueNASObjects(selected), len(selected), longhorn.FormatBytes(sumTrueNASUsed(selected)))
	}
	if len(deleted) == 0 {
		return fmt.Sprintf("0 deleted and %d refused", len(refused))
	}
	return fmt.Sprintf("deleted %d object(s) across %d candidate(s) reclaiming %s with %d refused",
		countTrueNASObjects(deleted), len(deleted), longhorn.FormatBytes(sumTrueNASUsed(deleted)), len(refused))
}

func sumTrueNASUsed(cands []truenas.Candidate) int64 {
	var total int64
	for _, c := range cands {
		total += c.Used
	}
	return total
}

func countTrueNASObjects(cands []truenas.Candidate) int {
	var n int
	for _, c := range cands {
		n += len(c.Objects)
	}
	return n
}

func resolveTrueNASFields(csv string, full bool) ([]string, error) {
	if full && csv != "" {
		return nil, fmt.Errorf("--fields and --full are mutually exclusive")
	}
	if full {
		return truenasFields, nil
	}
	if csv == "" {
		return defaultTruenasFields, nil
	}
	known := map[string]bool{}
	for _, f := range truenasFields {
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
			return nil, fmt.Errorf("unknown field %s; valid: %s", f, strings.Join(truenasFields, ", "))
		}
		fields = append(fields, f)
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("--fields is empty; valid: %s", strings.Join(truenasFields, ", "))
	}
	return fields, nil
}

func validTrueNASVerdict(class string) bool {
	return class == "all" || containsString(truenas.Verdicts(), class)
}

func filterTrueNASClass(cands []truenas.Candidate, class truenas.Class) []truenas.Candidate {
	out := make([]truenas.Candidate, 0, len(cands))
	for _, c := range cands {
		if c.Class == class {
			out = append(out, c)
		}
	}
	return out
}

func truenasCountLine(cands []truenas.Candidate) string {
	counts := map[truenas.Class]int{}
	for _, c := range cands {
		counts[c.Class]++
	}
	// Slash-separated rather than comma-separated: a comma is the TOON
	// delimiter, so a scalar containing one has to be quoted.
	return fmt.Sprintf("%d total (%d orphaned / %d claimed / %d attached / %d other)",
		len(cands), counts[truenas.ClassOrphaned], counts[truenas.ClassClaimed],
		counts[truenas.ClassAttached], counts[truenas.ClassOther])
}

func truenasRows(cands []truenas.Candidate, fields []string) [][]string {
	rows := make([][]string, 0, len(cands))
	for _, c := range cands {
		row := make([]string, 0, len(fields))
		for _, f := range fields {
			row = append(row, truenasFieldValue(c, f))
		}
		rows = append(rows, row)
	}
	return rows
}

func truenasObjectRows(cands []truenas.Candidate) [][]string {
	var rows [][]string
	for _, c := range cands {
		for i, o := range c.Objects {
			rows = append(rows, []string{c.Name, strconv.Itoa(i + 1), string(o.Kind), o.Name})
		}
	}
	return rows
}

func truenasFieldValue(c truenas.Candidate, field string) string {
	switch field {
	case "name":
		return c.Name
	case "kind":
		return string(c.Kind)
	case "class":
		return string(c.Class)
	case "size":
		return longhorn.FormatBytes(c.Used)
	case "storageClass":
		return c.StorageClass
	case "dataset":
		return c.DatasetID
	case "target":
		return c.Target
	case "extent":
		return c.Extent
	case "share":
		return c.Share
	case "sessions":
		return strconv.Itoa(c.Sessions)
	case "claimedBy":
		return c.ClaimedBy
	case "pv":
		return c.PV
	case "pvPhase":
		return c.PVPhase
	case "objects":
		names := make([]string, 0, len(c.Objects))
		for _, o := range c.Objects {
			names = append(names, o.String())
		}
		return strings.Join(names, " ")
	case "reason":
		return c.Reason
	default:
		return ""
	}
}

// emitTrueNASEvents mirrors the TOON report onto the newline-delimited event
// stream the repo's --json contract defines, so an agent parsing events sees the
// same rows and the same aggregate line.
func emitTrueNASEvents(out io.Writer, g *Globals, phaseName string,
	cands []truenas.Candidate, fields []string, summary string) error {
	em := NewEmitter(out, g.JSON)
	if g.Session != nil {
		em.SetSession(g.Session)
	}
	for _, c := range cands {
		detail := map[string]string{}
		for _, f := range fields {
			detail[f] = truenasFieldValue(c, f)
		}
		status := "info"
		if c.Class == truenas.ClassOrphaned {
			status = "broken"
		}
		em.Emit(Event{
			Phase:   "truenas-volumes",
			Name:    c.Name,
			Status:  status,
			Message: fmt.Sprintf("class=%s kind=%s storageClass=%s", c.Class, c.Kind, c.StorageClass),
			Detail:  detail,
		})
	}
	em.Emit(Event{Phase: "truenas-volumes", Name: phaseName, Status: "ok", Message: summary})
	return nil
}

func loadTrueNASCandidates(ctx context.Context, shared *truenasGlobals) ([]truenas.Candidate, error) {
	sessions, err := openTrueNASSessions(ctx, shared)
	if err != nil {
		return nil, err
	}
	defer sessions.Close()
	return sessions.Classify(ctx)
}

func truenasConnectHelp(shared *truenasGlobals) string {
	if shared.CAFile == "" && !shared.SkipVerify {
		return "A stock TrueNAS presents a self-signed certificate: pass --truenas-ca-file, " +
			"or --truenas-insecure-skip-tls-verify on a trusted LAN"
	}
	return "Check KUBECONFIG, and that the democratic-csi driver-config secrets have synced from Vault"
}

func sortTrueNASCandidates(cands []truenas.Candidate) {
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].StorageClass != cands[j].StorageClass {
			return cands[i].StorageClass < cands[j].StorageClass
		}
		return cands[i].Name < cands[j].Name
	})
}
