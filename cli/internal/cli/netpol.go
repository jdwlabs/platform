package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jdwlabs/platform/internal/cilium"
	"github.com/jdwlabs/platform/internal/display"
)

var coverageAxes = []string{"namespace", "node"}

func newClusterNetpolCmd(g *Globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "netpol",
		Short: "Report NetworkPolicy enforcement coverage",
	}
	cmd.AddCommand(newNetpolCoverageCmd(g))
	return cmd
}

type coverageOptions struct {
	By          string
	Namespace   string
	Unmanaged   bool
	MinCoverage int
}

func newNetpolCoverageCmd(g *Globals) *cobra.Command {
	opts := &coverageOptions{}
	cmd := &cobra.Command{
		Use:   "coverage",
		Short: "Report how many pods Cilium holds an endpoint for (requires --min-coverage to pass)",
		Long: `Report the managed/unmanaged split of the pods NetworkPolicy is expected to
cover, by joining the API server's pod list against Cilium's CiliumEndpoints.

A pod that predates the cilium-agent on its node never received a cilium-cni
ADD, so it has no endpoint, no security identity, and every policy selecting it
resolves against nothing — while the namespace still reports policies applied.
Cilium exports metrics for the endpoints it manages and cannot export a count
of pods it never learned about, so this join is the only place the gap is
visible.

Host-network pods and pods that are not Running are excluded: neither can ever
carry an endpoint, so counting them would report a gap no remediation closes.

Exits non-zero when coverage is below --min-coverage, which is what makes this
usable as a regression gate rather than only as a report.`,
		Example: `  platformctl cluster netpol coverage
  platformctl cluster netpol coverage --by node
  platformctl cluster netpol coverage --namespace jdwlabs-prd --unmanaged
  platformctl cluster netpol coverage --min-coverage 0`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNetpolCoverage(cmd, g, opts)
		},
	}
	cmd.Flags().StringVar(&opts.By, "by", "namespace",
		"aggregate by: "+strings.Join(coverageAxes, ", "))
	cmd.Flags().StringVarP(&opts.Namespace, "namespace", "n", "",
		"restrict to one namespace instead of the whole cluster")
	cmd.Flags().BoolVar(&opts.Unmanaged, "unmanaged", false,
		"also list every unmanaged pod, which is the restart worklist")
	cmd.Flags().IntVar(&opts.MinCoverage, "min-coverage", 100,
		"exit non-zero below this coverage percentage; 0 reports without failing")
	return cmd
}

func runNetpolCoverage(cmd *cobra.Command, g *Globals, opts *coverageOptions) error {
	out := cmd.OutOrStdout()

	if !validCoverageAxis(opts.By) {
		return reportCLIError(out,
			fmt.Errorf("unknown --by %s; valid: %s", opts.By, strings.Join(coverageAxes, ", ")),
			"Run `platformctl cluster netpol coverage --by namespace` for the per-namespace split")
	}
	if opts.MinCoverage < 0 || opts.MinCoverage > 100 {
		return reportCLIError(out,
			fmt.Errorf("--min-coverage %d is outside 0-100", opts.MinCoverage),
			"Pass --min-coverage 0 to report without failing, or 100 to require full coverage")
	}

	kc, err := volumeKubeClient()
	if err != nil {
		return reportCLIError(out, err, "Check KUBECONFIG points at the cluster")
	}
	dc, err := volumeDynamicClient()
	if err != nil {
		return reportCLIError(out, err, "Check KUBECONFIG points at the cluster")
	}

	pods, err := cilium.ListPolicyPods(cmd.Context(), kc, opts.Namespace)
	if err != nil {
		return reportCLIError(out, err, "Run `platformctl cluster status` to check the API server")
	}
	endpoints, err := cilium.ListEndpoints(cmd.Context(), dc, opts.Namespace)
	if err != nil {
		return reportCLIError(out, err,
			"Run `platformctl cluster status` — no CiliumEndpoint CRD means the policy engine is not installed")
	}

	pods = cilium.Join(pods, endpoints)
	total := cilium.Total(pods)
	groups := cilium.GroupByNamespace(pods)
	if opts.By == "node" {
		groups = cilium.GroupByNode(pods)
	}
	summary := coverageSummary(total, opts.Namespace)

	if g.JSON {
		if err := emitCoverageEvents(out, g, opts.By, groups, total, summary); err != nil {
			return err
		}
	} else if err := writeCoverageReport(out, opts, total, groups, cilium.Unmanaged(pods), summary); err != nil {
		return err
	}

	if total.Coverage() < opts.MinCoverage {
		return reportCLIError(out,
			fmt.Errorf("%s / below --min-coverage %d", summary, opts.MinCoverage),
			"Enroll the unmanaged pods by restarting their workloads — docs/OPERATIONS.md, closing the Cilium managed-endpoint gap; pass --min-coverage 0 to report without failing")
	}
	return nil
}

func writeCoverageReport(out io.Writer, opts *coverageOptions,
	total cilium.Group, groups []cilium.Group, unmanaged []cilium.Pod, summary string) error {
	if err := display.ToonScalar(out, "coverage", summary); err != nil {
		return err
	}
	if err := display.ToonTable(out, "by-"+opts.By,
		[]string{opts.By, "total", "managed", "unmanaged", "coverage"},
		coverageRows(groups)); err != nil {
		return err
	}
	if opts.Unmanaged {
		rows := make([][]string, 0, len(unmanaged))
		for _, p := range unmanaged {
			rows = append(rows, []string{p.Namespace, p.Name, p.Node})
		}
		if err := display.ToonTable(out, "unmanaged", []string{"namespace", "pod", "node"}, rows); err != nil {
			return err
		}
	}
	if total.Unmanaged == 0 {
		return display.ToonScalar(out, "result", "every pod-network pod has a CiliumEndpoint")
	}
	help := []string{
		"A pod is enrolled only by being recreated — see docs/OPERATIONS.md, closing the Cilium managed-endpoint gap",
	}
	if !opts.Unmanaged {
		help = append([]string{"Run with `--unmanaged` for the pod-level restart worklist"}, help...)
	}
	return display.ToonList(out, "help", help)
}

func coverageRows(groups []cilium.Group) [][]string {
	rows := make([][]string, 0, len(groups))
	for _, g := range groups {
		rows = append(rows, []string{
			g.Key,
			fmt.Sprintf("%d", g.Total),
			fmt.Sprintf("%d", g.Managed),
			fmt.Sprintf("%d", g.Unmanaged),
			fmt.Sprintf("%d%%", g.Coverage()),
		})
	}
	return rows
}

func coverageSummary(total cilium.Group, namespace string) string {
	scope := "cluster-wide"
	if namespace != "" {
		scope = "namespace " + namespace
	}
	// Slash-separated rather than comma-separated: a comma is the TOON
	// delimiter, so a scalar containing one has to be quoted.
	return fmt.Sprintf("%d/%d pods managed (%d%%) / %d unmanaged, %s",
		total.Managed, total.Total, total.Coverage(), total.Unmanaged, scope)
}

func validCoverageAxis(axis string) bool {
	for _, a := range coverageAxes {
		if a == axis {
			return true
		}
	}
	return false
}

// emitCoverageEvents mirrors the TOON report onto the newline-delimited event
// stream the repo's --json contract defines. A group with unmanaged pods is
// emitted as `broken` so an agent filtering on status sees the gap without
// re-deriving it from the counts.
func emitCoverageEvents(out io.Writer, g *Globals, axis string, groups []cilium.Group, total cilium.Group, summary string) error {
	em := NewEmitter(out, g.JSON)
	if g.Session != nil {
		em.SetSession(g.Session)
	}
	for _, grp := range groups {
		status := "ok"
		if grp.Unmanaged > 0 {
			status = "broken"
		}
		em.Emit(Event{
			Phase:   "netpol-coverage",
			Name:    grp.Key,
			Status:  status,
			Message: fmt.Sprintf("%d/%d managed (%d%%)", grp.Managed, grp.Total, grp.Coverage()),
			Detail: map[string]string{
				axis:        grp.Key,
				"total":     fmt.Sprintf("%d", grp.Total),
				"managed":   fmt.Sprintf("%d", grp.Managed),
				"unmanaged": fmt.Sprintf("%d", grp.Unmanaged),
			},
		})
	}
	summaryStatus := "ok"
	if total.Unmanaged > 0 {
		summaryStatus = "broken"
	}
	em.Emit(Event{Phase: "netpol-coverage", Name: "summary", Status: summaryStatus, Message: summary})
	return nil
}
