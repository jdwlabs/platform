package cli

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jdwlabs/platform/internal/display"
	"github.com/jdwlabs/platform/internal/drain"
)

// Field names are the command's contract with agents, so they are listed once
// and the default and --full sets are derived from that single list.
var drainFields = []string{
	"node", "verdict", "movable", "movableMem", "blockers",
	"role", "allocatable", "requested", "free", "pinnedMem", "tightestAfter", "pdbAtZero",
	"used", "underDeclared",
}

var defaultDrainFields = []string{"node", "verdict", "movable", "movableMem", "blockers"}

type drainCheckOptions struct {
	Node   string
	Fields string
	Full   bool
	Plan   bool
	Pods   bool
	Usage  bool
}

func newClusterDrainCheckCmd(g *Globals) *cobra.Command {
	opts := &drainCheckOptions{}
	cmd := &cobra.Command{
		Use:   "drain-check",
		Short: "Simulate draining every node and report which drains could not complete",
		Long: `Answer, without touching the cluster, whether each node could be drained right now.

A node drain is the mechanism behind Talos upgrades, Kubernetes upgrades,
hardware maintenance and incident response. It only completes if every pod it
evicts can be re-placed somewhere else. Nothing in Kubernetes reports that
ahead of time, so a cluster can be un-maintainable for weeks and look healthy
throughout — the discovery happens mid-upgrade, with a node already cordoned.

For each node this takes the pods a drain would evict, drops that node from the
set of targets, and packs them onto the rest under the predicates that decide
real placements: readiness and cordon state, taints and tolerations,
nodeSelector and required node affinity, required pod affinity and
anti-affinity, PersistentVolume node affinity, and memory/CPU/pod-count
capacity. DaemonSet and static pods are skipped, as kubectl drain skips them.

Verdicts:
  drainable  an assignment for every evicted pod was found
  blocked    at least one evicted pod had nowhere to go
  empty      the drain evicts nothing
  skipped    the node is already cordoned or not ready

A blocked node reports a reason class. "hard" means no surviving node satisfies
that pod's own constraints at all, which no packing order can change — a proof.
"capacity" means eligible nodes existed but were full once the rest of the
evacuation was packed, which is order-dependent and so a strong signal rather
than a proof. "unmanaged" means the pod has no controller to recreate it.

Preferred affinity and topology spread constraints are not evaluated: they never
make a placement impossible. Any workload carrying a hard constraint this
simulation cannot evaluate is named in the unmodelled report instead of being
silently assumed satisfied.

Read-only. Exits non-zero when any node is blocked.`,
		Example: `  platformctl cluster drain-check
  platformctl cluster drain-check --node talos-lx0-6a4 --plan
  platformctl cluster drain-check --full
  platformctl cluster drain-check --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDrainCheck(cmd, g, opts)
		},
	}
	cmd.Flags().StringVar(&opts.Node, "node", "", "check only this node")
	cmd.Flags().StringVar(&opts.Fields, "fields", "",
		"comma-separated fields to report instead of the default set")
	cmd.Flags().BoolVar(&opts.Full, "full", false, "report every field")
	cmd.Flags().BoolVar(&opts.Plan, "plan", false,
		"also print the pod-to-node assignment the simulation found")
	cmd.Flags().BoolVar(&opts.Pods, "pods", false,
		"also print every pod on each reported node with its drain classification")
	cmd.Flags().BoolVar(&opts.Usage, "usage", false,
		"read metrics-server and report observed memory beside declared requests")
	return cmd
}

func runDrainCheck(cmd *cobra.Command, g *Globals, opts *drainCheckOptions) error {
	out := cmd.OutOrStdout()

	fields, err := resolveDrainFields(opts.Fields, opts.Full)
	if err != nil {
		return reportCLIError(out, err, "Run `platformctl cluster drain-check --help` for the field list")
	}

	kc, err := volumeKubeClient()
	if err != nil {
		return reportCLIError(out, err, "Check KUBECONFIG points at the cluster")
	}

	cluster, err := drain.Load(cmd.Context(), kc)
	if err != nil {
		return reportCLIError(out, err, "Run `platformctl cluster status` to check the cluster is reachable")
	}

	var usageNote string
	if opts.Usage {
		dc, err := volumeDynamicClient()
		if err != nil {
			return reportCLIError(out, err, "Check KUBECONFIG points at the cluster")
		}
		usage, err := drain.LoadUsage(cmd.Context(), dc)
		if err != nil {
			// Usage never decides a verdict, so an absent metrics-server
			// downgrades the report rather than failing it — but it says so,
			// because a silently empty column reads as "nothing is oversized".
			usageNote = "observed memory unavailable: " + err.Error()
		} else {
			drain.AttachUsage(&cluster, usage)
		}
	}

	if opts.Node != "" && !clusterHasNode(cluster, opts.Node) {
		err := fmt.Errorf("no node named %s; known: %s", opts.Node, strings.Join(nodeNames(cluster), " "))
		return reportCLIError(out, err, "Run `platformctl cluster drain-check` for every node")
	}

	result := drain.Simulate(cluster)
	if opts.Node != "" {
		result = filterDrainNode(result, opts.Node)
	}

	if g.JSON {
		if err := emitDrainEvents(out, g, result, fields, usageNote); err != nil {
			return err
		}
	} else if err := writeDrainReport(out, result, fields, opts, usageNote); err != nil {
		return err
	}

	if blocked := result.Blocked(); len(blocked) > 0 {
		return fmt.Errorf("%d node(s) cannot be drained", len(blocked))
	}
	return nil
}

func writeDrainReport(out io.Writer, result drain.Result, fields []string, opts *drainCheckOptions, usageNote string) error {
	if err := display.ToonScalar(out, "count", drainCountLine(result)); err != nil {
		return err
	}
	if usageNote != "" {
		if err := display.ToonScalar(out, "warning", usageNote); err != nil {
			return err
		}
	}
	if err := display.ToonTable(out, "nodes", fields, drainRows(result.Nodes, fields)); err != nil {
		return err
	}

	blocked := result.Blocked()
	if len(blocked) > 0 {
		blockerFields := []string{"node", "pod", "request", "class", "reason"}
		if err := display.ToonTable(out, "blockers", blockerFields, drainBlockerRows(blocked)); err != nil {
			return err
		}
	}

	if opts.Plan {
		planFields := []string{"node", "pod", "request", "target"}
		if err := display.ToonTable(out, "plan", planFields, drainPlanRows(result.Nodes)); err != nil {
			return err
		}
	}

	if opts.Pods {
		podFields := []string{"node", "pod", "class", "request", "pdb", "allowed"}
		if opts.Usage {
			podFields = []string{"node", "pod", "class", "request", "used", "pdb", "allowed"}
		}
		if err := display.ToonTable(out, "pods", podFields, drainPodRows(result.Nodes, opts.Usage)); err != nil {
			return err
		}
	}

	if len(result.Colocations) > 0 {
		coFields := []string{"workload", "node", "replicas", "rule"}
		if err := display.ToonTable(out, "colocated", coFields, drainColocationRows(result)); err != nil {
			return err
		}
	}

	if len(result.UnmodelledConstraints) > 0 {
		if err := display.ToonList(out, "unmodelled", result.UnmodelledConstraints); err != nil {
			return err
		}
	}

	if len(result.Nodes) == 0 {
		return display.ToonScalar(out, "result", "no nodes matched")
	}
	if err := display.ToonScalar(out, "result", drainResultLine(result)); err != nil {
		return err
	}
	return display.ToonList(out, "help", drainHelp(result, opts))
}

func drainHelp(result drain.Result, opts *drainCheckOptions) []string {
	help := []string{}
	if blocked := result.Blocked(); len(blocked) > 0 && !opts.Plan {
		help = append(help, fmt.Sprintf(
			"Run `platformctl cluster drain-check --node %s --plan` to see where the rest would go",
			blocked[0].Node.Name))
	}
	if !opts.Full {
		help = append(help, "Run `platformctl cluster drain-check --full` for per-node commitment figures")
	}
	if !opts.Pods {
		help = append(help, "Run `platformctl cluster drain-check --pods` to see what each node carries")
	}
	if !opts.Usage {
		help = append(help, "Run `platformctl cluster drain-check --usage` to compare declared requests against observed memory")
	}
	return append(help, "Nothing was cordoned, evicted or applied — this command only reads")
}

func drainCountLine(result drain.Result) string {
	var blocked, drainable, empty, skipped int
	for _, n := range result.Nodes {
		switch n.Verdict {
		case drain.VerdictBlocked:
			blocked++
		case drain.VerdictDrainable:
			drainable++
		case drain.VerdictEmpty:
			empty++
		case drain.VerdictSkipped:
			skipped++
		}
	}
	// Slash-separated rather than comma-separated: a comma is the TOON
	// delimiter, so a scalar containing one has to be quoted and the line
	// gets noisier.
	return fmt.Sprintf("%d nodes (%d blocked / %d drainable / %d empty / %d skipped)",
		len(result.Nodes), blocked, drainable, empty, skipped)
}

func drainResultLine(result drain.Result) string {
	blocked := result.Blocked()
	if len(blocked) == 0 {
		return fmt.Sprintf("every one of %d node(s) can be drained", len(result.Nodes))
	}
	names := make([]string, 0, len(blocked))
	for _, n := range blocked {
		names = append(names, n.Node.Name)
	}
	return fmt.Sprintf("%d of %d node(s) cannot be drained: %s",
		len(blocked), len(result.Nodes), strings.Join(names, " "))
}

func drainRows(nodes []drain.NodeResult, fields []string) [][]string {
	rows := make([][]string, 0, len(nodes))
	for _, n := range nodes {
		row := make([]string, 0, len(fields))
		for _, f := range fields {
			row = append(row, drainFieldValue(n, f))
		}
		rows = append(rows, row)
	}
	return rows
}

func drainFieldValue(n drain.NodeResult, field string) string {
	switch field {
	case "node":
		return n.Node.Name
	case "verdict":
		return string(n.Verdict)
	case "movable":
		return strconv.Itoa(n.Movable)
	case "movableMem":
		return drain.FormatMemory(n.MovableMemory)
	case "blockers":
		return strconv.Itoa(len(n.Blockers))
	case "role":
		if n.Node.ControlPlane {
			return "control-plane"
		}
		return "worker"
	case "allocatable":
		return drain.FormatMemory(n.Node.AllocatableMemory)
	case "requested":
		return drain.FormatMemory(n.RequestedMemory)
	case "free":
		return drain.FormatMemory(n.FreeMemory)
	case "pinnedMem":
		return drain.FormatMemory(n.PinnedMemory)
	case "tightestAfter":
		return drain.FormatMemory(n.TightestAfter)
	case "pdbAtZero":
		return strconv.Itoa(n.PDBAtZero)
	case "used":
		if !n.UsageKnown {
			return "-"
		}
		return drain.FormatMemory(n.UsedMemory)
	case "underDeclared":
		if !n.UsageKnown {
			return "-"
		}
		return drain.FormatMemory(n.UnderDeclared)
	default:
		return ""
	}
}

func drainBlockerRows(blocked []drain.NodeResult) [][]string {
	var rows [][]string
	for _, n := range blocked {
		for _, b := range n.Blockers {
			rows = append(rows, []string{
				n.Node.Name,
				b.Pod.Ref(),
				drain.FormatMemory(b.Pod.Memory),
				string(b.Class),
				b.Reason,
			})
		}
	}
	return rows
}

func drainPlanRows(nodes []drain.NodeResult) [][]string {
	var rows [][]string
	for _, n := range nodes {
		for _, p := range n.Placements {
			rows = append(rows, []string{
				n.Node.Name,
				p.Pod.Ref(),
				drain.FormatMemory(p.Pod.Memory),
				p.Target,
			})
		}
	}
	return rows
}

func drainPodRows(nodes []drain.NodeResult, withUsage bool) [][]string {
	var rows [][]string
	for _, n := range nodes {
		for _, p := range n.Pods {
			pdb, allowed := "-", "-"
			if p.HasPDB {
				pdb = p.PDB
				allowed = strconv.Itoa(int(p.DisruptionsAllowed))
			}
			used := "-"
			if p.UsageKnown {
				used = drain.FormatMemory(p.UsedMemory)
			}
			row := []string{n.Node.Name, p.Ref(), string(p.Class), drain.FormatMemory(p.Memory)}
			if withUsage {
				row = append(row, used)
			}
			rows = append(rows, append(row, pdb, allowed))
		}
	}
	return rows
}

func drainColocationRows(result drain.Result) [][]string {
	rows := make([][]string, 0, len(result.Colocations))
	for _, c := range result.Colocations {
		rows = append(rows, []string{c.Workload, c.Node, strconv.Itoa(c.Replicas), c.Rule})
	}
	return rows
}

// emitDrainEvents mirrors the TOON report onto the newline-delimited event
// stream the repo's --json contract defines, so an agent parsing events sees
// the same verdicts and the same aggregate line.
func emitDrainEvents(out io.Writer, g *Globals, result drain.Result, fields []string, usageNote string) error {
	em := NewEmitter(out, g.JSON)
	if g.Session != nil {
		em.SetSession(g.Session)
	}
	for _, n := range result.Nodes {
		detail := map[string]string{}
		for _, f := range fields {
			detail[f] = drainFieldValue(n, f)
		}
		status := "info"
		message := fmt.Sprintf("verdict=%s movable=%d", n.Verdict, n.Movable)
		if n.Verdict == drain.VerdictBlocked {
			status = "broken"
			message = fmt.Sprintf("verdict=blocked %s", n.Blockers[0].Reason)
			detail["firstBlockedPod"] = n.Blockers[0].Pod.Ref()
			detail["reasonClass"] = string(n.Blockers[0].Class)
		}
		em.Emit(Event{Phase: "drain-check", Name: n.Node.Name, Status: status, Message: message, Detail: detail})
	}
	if usageNote != "" {
		em.Emit(Event{Phase: "drain-check", Name: "usage", Status: "broken", Message: usageNote})
	}
	for _, c := range result.Colocations {
		em.Emit(Event{Phase: "drain-check", Name: c.Workload, Status: "broken",
			Message: fmt.Sprintf("%d replicas share %s against %s anti-affinity", c.Replicas, c.Node, c.Rule)})
	}
	for _, u := range result.UnmodelledConstraints {
		em.Emit(Event{Phase: "drain-check", Name: "unmodelled", Status: "info", Message: u})
	}
	em.Emit(Event{Phase: "drain-check", Name: "summary", Status: "ok", Message: drainResultLine(result)})
	return nil
}

func resolveDrainFields(csv string, full bool) ([]string, error) {
	if full && csv != "" {
		return nil, fmt.Errorf("--fields and --full are mutually exclusive")
	}
	if full {
		return drainFields, nil
	}
	if csv == "" {
		return defaultDrainFields, nil
	}
	known := map[string]bool{}
	for _, f := range drainFields {
		known[f] = true
	}
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		f := strings.TrimSpace(p)
		if f == "" {
			continue
		}
		if !known[f] {
			return nil, fmt.Errorf("unknown field %s; valid: %s", f, strings.Join(drainFields, ", "))
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--fields is empty; valid: %s", strings.Join(drainFields, ", "))
	}
	return out, nil
}

func clusterHasNode(c drain.Cluster, name string) bool {
	for _, n := range c.Nodes {
		if n.Name == name {
			return true
		}
	}
	return false
}

func nodeNames(c drain.Cluster) []string {
	out := make([]string, 0, len(c.Nodes))
	for _, n := range c.Nodes {
		out = append(out, n.Name)
	}
	return out
}

func filterDrainNode(result drain.Result, name string) drain.Result {
	out := drain.Result{
		UnmodelledConstraints: result.UnmodelledConstraints,
		Colocations:           result.Colocations,
	}
	for _, n := range result.Nodes {
		if n.Node.Name == name {
			out.Nodes = append(out.Nodes, n)
		}
	}
	return out
}
