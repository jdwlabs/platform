package cli

import (
	"fmt"
	"io"
	"os"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/spf13/cobra"

	"github.com/jdwlabs/platform/internal/display"
	"github.com/jdwlabs/platform/internal/vault"
)

// Globals holds flags shared across commands. Every field but DryRun is a
// persistent root flag; DryRun is bound per-command by bindDryRun.
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
		Version:       version,
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

	// --dry-run is deliberately absent here: see bindDryRun.
	cmd.PersistentFlags().StringVar(&g.Branch, "branch", "", "override targetRevision in bootstrap/root-app.yaml")
	cmd.PersistentFlags().BoolVar(&g.NonInteractive, "non-interactive", false, "consume PLATFORMCTL_* env vars; never prompt")
	cmd.PersistentFlags().BoolVar(&g.JSON, "json", false, "emit newline-delimited JSON events instead of human-readable text")
	cmd.PersistentFlags().BoolVar(&g.NoColor, "no-color", false, "disable ANSI color output")

	// SilenceErrors keeps cobra quiet and main prints nothing on failure, so
	// without this a rejected flag is an empty response with a non-zero exit —
	// indistinguishable from a command that ran and found nothing. Subcommands
	// inherit this unless they set their own.
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return reportCLIError(c.OutOrStdout(), err,
			fmt.Sprintf("Valid flags for `%s`: %s", c.CommandPath(), localFlagNames(c)))
	})

	cmd.AddCommand(newTenantsCmd(g))
	cmd.AddCommand(newBootstrapCmd(g))
	cmd.AddCommand(newClusterCmd(g))
	cmd.AddCommand(newGitSyncCmd(g))
	return cmd, cleanup
}

// bindDryRun declares --dry-run as a local flag on a command that honours it.
// It is not a persistent root flag: every other command silently accepted the
// flag and mutated for real anyway, so a preview that was never a preview
// reported success. A command that cannot preview must reject the flag.
func bindDryRun(cmd *cobra.Command, g *Globals) {
	cmd.Flags().BoolVar(&g.DryRun, "dry-run", false,
		"log intended actions but do not execute mutating operations")
}

// reportCLIError writes the error and its fix as data on stdout, so an agent
// reading stdout sees why the command refused and what to run instead.
func reportCLIError(out io.Writer, err error, help string) error {
	if serr := display.ToonScalar(out, "error", err.Error()); serr != nil {
		return serr
	}
	if serr := display.ToonList(out, "help", []string{help}); serr != nil {
		return serr
	}
	return err
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
	testVaultClient   *vault.Client
)
