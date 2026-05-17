package cli

import (
	"fmt"
	"os"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/spf13/cobra"

	"github.com/jdwlabs/platform/internal/display"
)

// Globals holds persistent flags wired by NewRoot.
type Globals struct {
	Branch         string
	DryRun         bool
	NonInteractive bool
	JSON           bool
	NoColor        bool
	Session        *display.RunSession // nil when --json
}

// NewRoot returns the configured root command and a cleanup function.
// The caller MUST invoke cleanup(err) after root.Execute() regardless of error,
// because cobra does not call PersistentPostRunE when RunE returns an error.
func NewRoot(version string) (*cobra.Command, func(error)) {
	g := &Globals{}
	var cleanup func(error)

	cmd := &cobra.Command{
		Use:           "platformctl",
		Short:         "Operate the jdwlabs platform: bootstrap, verify, heal, tenants.",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if g.JSON {
				return nil
			}
			sess, err := display.NewRunSession(display.Config{
				Command: cmd.Name(),
				NoColor: g.NoColor,
			})
			if err != nil {
				// Non-fatal: proceed without session
				fmt.Fprintf(os.Stderr, "warning: could not create log session: %v\n", err)
				return nil
			}
			g.Session = sess
			display.PrintBanner(os.Stderr, version, g.NoColor)
			sess.AuditLog.WriteEntry("INVOKE", fmt.Sprintf("platformctl %s", cmd.CommandPath()))
			return nil
		},
	}

	cleanup = func(exitErr error) {
		if g.Session == nil {
			return
		}
		g.Session.PrintSummaryBox(exitErr)
		g.Session.Close(exitErr)
	}

	cmd.PersistentFlags().StringVar(&g.Branch, "branch", "", "override targetRevision in bootstrap/root-app.yaml")
	cmd.PersistentFlags().BoolVar(&g.DryRun, "dry-run", false, "log intended actions but do not execute mutating operations")
	cmd.PersistentFlags().BoolVar(&g.NonInteractive, "non-interactive", false, "consume PLATFORMCTL_* env vars; never prompt")
	cmd.PersistentFlags().BoolVar(&g.JSON, "json", false, "emit newline-delimited JSON events instead of human-readable text")
	cmd.PersistentFlags().BoolVar(&g.NoColor, "no-color", false, "disable ANSI color output")

	cmd.AddCommand(newTenantsCmd(g))
	cmd.AddCommand(newBootstrapCmd(g))
	return cmd, cleanup
}

// NewRootForTest returns a root command whose subcommands use the provided
// k8s.Interface and dynamic.Interface rather than building real clients.
func NewRootForTest(k kubernetes.Interface, d dynamic.Interface) *cobra.Command {
	root, _ := NewRoot("test")
	testKubeClient = k
	testDynamicClient = d
	return root
}

// Package-level overrides used by RunE bodies when set (test injection seam).
var (
	testKubeClient    kubernetes.Interface
	testDynamicClient dynamic.Interface
)
