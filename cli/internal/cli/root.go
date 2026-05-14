package cli

import (
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/spf13/cobra"
)

// Globals holds persistent flags wired by NewRoot.
type Globals struct {
	Branch         string
	DryRun         bool
	NonInteractive bool
	JSON           bool
}

// NewRoot returns the configured root command.
func NewRoot() *cobra.Command {
	g := &Globals{}
	cmd := &cobra.Command{
		Use:           "platformctl",
		Short:         "Operate the jdwlabs platform: bootstrap, verify, heal, tenants.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	cmd.PersistentFlags().StringVar(&g.Branch, "branch", "", "override targetRevision in bootstrap/root-app.yaml (consumed by `bootstrap apply` in a future plan)")
	cmd.PersistentFlags().BoolVar(&g.DryRun, "dry-run", false, "log intended actions but do not execute mutating operations")
	cmd.PersistentFlags().BoolVar(&g.NonInteractive, "non-interactive", false, "consume PLATFORMCTL_* env vars; never prompt")
	cmd.PersistentFlags().BoolVar(&g.JSON, "json", false, "emit newline-delimited JSON events instead of human-readable text")

	cmd.AddCommand(newTenantsCmd(g))
	cmd.AddCommand(newBootstrapCmd(g))
	return cmd
}

// NewRootForTest returns a root command whose subcommands use the provided
// k8s.Interface and dynamic.Interface rather than building real clients.
func NewRootForTest(k kubernetes.Interface, d dynamic.Interface) *cobra.Command {
	root := NewRoot()
	testKubeClient = k
	testDynamicClient = d
	return root
}

// Package-level overrides used by RunE bodies when set (test injection seam).
var (
	testKubeClient    kubernetes.Interface
	testDynamicClient dynamic.Interface
)
