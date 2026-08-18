package cli

import (
	"bytes"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRoot_HasGlobalFlags(t *testing.T) {
	cmd, _ := NewRoot("test")
	for _, name := range []string{"branch", "non-interactive", "json", "no-color"} {
		if cmd.PersistentFlags().Lookup(name) == nil {
			t.Errorf("missing global flag --%s", name)
		}
	}
}

func TestRoot_HelpRuns(t *testing.T) {
	cmd, _ := NewRoot("test")
	cmd.SetArgs([]string{"--help"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("help failed: %v", err)
	}
	if out.Len() == 0 {
		t.Fatalf("no help output")
	}
}

// dryRunContract is the whole command tree and, for each command, whether it
// honours --dry-run. Only the three listed as true read Globals.DryRun; every
// other command mutates for real, so accepting the flag would report a preview
// that never happened. TestDryRun_ContractCoversEveryCommand fails when a new
// command is added without an entry, which is what stops one inheriting the
// flag by accident.
var dryRunContract = map[string]bool{
	"platformctl":                         false,
	"platformctl bootstrap":               false,
	"platformctl bootstrap heal":          false,
	"platformctl bootstrap phase":         false,
	"platformctl bootstrap seed":          false,
	"platformctl bootstrap verify":        false,
	"platformctl cluster":                 false,
	"platformctl cluster drain-check":     false,
	"platformctl cluster status":          false,
	"platformctl cluster volumes":         false,
	"platformctl cluster volumes list":    false,
	"platformctl cluster volumes reclaim": true,
	"platformctl gitsync":                 false,
	"platformctl gitsync delete":          true,
	"platformctl gitsync recreate":        true,
	"platformctl gitsync status":          false,
	"platformctl tenants":                 false,
	"platformctl tenants validate":        false,
	"platformctl tenants verify-secrets":  false,
}

func TestDryRun_ScopedToImplementingCommands(t *testing.T) {
	for path, wantAccept := range dryRunContract {
		t.Run(path, func(t *testing.T) {
			root, _, g := newRootWithGlobals("test")
			cmd := findCommand(t, root, path)

			// ParseFlags resolves inherited persistent flags exactly as an
			// invocation would, without running the command.
			err := cmd.ParseFlags([]string{"--dry-run"})
			switch {
			case wantAccept && err != nil:
				t.Fatalf("`%s` must accept --dry-run: %v", path, err)
			case !wantAccept && err == nil:
				t.Fatalf("`%s` accepts --dry-run but does not implement it", path)
			case !wantAccept && !strings.Contains(err.Error(), "unknown flag"):
				t.Fatalf("`%s` must reject --dry-run as an unknown flag, got: %v", path, err)
			}
			if !wantAccept {
				return
			}

			// Accepting the flag is not the contract. A command that binds
			// --dry-run to anything but the Globals its RunE body reads
			// passes every acceptance check and still mutates for real, so
			// assert the flag writes through to that exact field.
			flag := cmd.Flags().Lookup("dry-run")
			if flag == nil {
				t.Fatalf("`%s` parsed --dry-run but declares no such flag", path)
			}
			g.DryRun = false
			if err := flag.Value.Set("true"); err != nil {
				t.Fatalf("`%s` --dry-run is not settable: %v", path, err)
			}
			if !g.DryRun {
				t.Fatalf("`%s` binds --dry-run to something other than Globals.DryRun", path)
			}
		})
	}
}

func TestDryRun_ContractCoversEveryCommand(t *testing.T) {
	root, _ := NewRoot("test")

	var found []string
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		found = append(found, c.CommandPath())
		// Either setting makes ParseFlags return nil for an unknown flag, so
		// the contract test above would read a rejecting command as accepting
		// and steer its author to mark the row true — at which point the
		// command swallows --dry-run and mutates for real.
		if c.DisableFlagParsing {
			t.Errorf("`%s` sets DisableFlagParsing, which defeats the --dry-run contract test", c.CommandPath())
		}
		if c.FParseErrWhitelist.UnknownFlags {
			t.Errorf("`%s` whitelists unknown flags, so it would swallow --dry-run instead of rejecting it", c.CommandPath())
		}
		for _, sub := range c.Commands() {
			if sub.Name() == "help" || sub.Name() == "completion" {
				continue
			}
			walk(sub)
		}
	}
	walk(root)

	for _, path := range found {
		if _, ok := dryRunContract[path]; !ok {
			t.Errorf("`%s` is not in dryRunContract; decide whether it implements --dry-run", path)
		}
	}
	if len(found) != len(dryRunContract) {
		sort.Strings(found)
		t.Errorf("dryRunContract lists %d commands, tree has %d: %v",
			len(dryRunContract), len(found), found)
	}
}

// TestDryRun_UnsupportedCommandReportsTheRejection covers what ParseFlags
// cannot: a rejected flag must reach stdout, because the process silences
// cobra's own errors and would otherwise exit non-zero having printed nothing.
func TestDryRun_UnsupportedCommandReportsTheRejection(t *testing.T) {
	root, _ := NewRoot("test")
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := Execute(root, []string{"bootstrap", "seed", "grafana", "--dry-run"})
	if err == nil {
		t.Fatalf("bootstrap seed must fail on --dry-run\n%s", out.String())
	}
	if ExitCode(err) == ExitOK {
		t.Fatalf("bootstrap seed --dry-run must exit non-zero, got %d", ExitCode(err))
	}
	if !strings.Contains(out.String(), "unknown flag: --dry-run") {
		t.Errorf("rejection must name the flag on stdout:\n%s", out.String())
	}
	// FlagErrorFunc already reported this one; Execute must not repeat it.
	if n := strings.Count(out.String(), "unknown flag: --dry-run"); n != 1 {
		t.Errorf("rejection reported %d times, want 1:\n%s", n, out.String())
	}
}

// TestDryRun_RootPositionReportsTheRejection is the sibling of the above for
// the documented `platformctl --dry-run <subcommand>` form. Cobra resolves the
// subcommand before any command's flags exist, so this failure never reaches
// FlagErrorFunc and is the one ordering that can exit non-zero in silence.
func TestDryRun_RootPositionReportsTheRejection(t *testing.T) {
	root, _ := NewRoot("test")
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)

	err := Execute(root, []string{"--dry-run", "bootstrap", "seed", "grafana"})
	if err == nil {
		t.Fatalf("bootstrap seed must fail on --dry-run\n%s", out.String())
	}
	if ExitCode(err) == ExitOK {
		t.Fatalf("--dry-run bootstrap seed must exit non-zero, got %d", ExitCode(err))
	}
	if !strings.Contains(out.String(), "--dry-run") {
		t.Errorf("rejection must name the flag on stdout:\n%s", out.String())
	}
}

func findCommand(t *testing.T, root *cobra.Command, path string) *cobra.Command {
	t.Helper()
	args := strings.Fields(strings.TrimPrefix(path, "platformctl"))
	cmd, _, err := root.Find(args)
	if err != nil {
		t.Fatalf("find %q: %v", path, err)
	}
	if cmd.CommandPath() != path {
		t.Fatalf("find %q resolved to %q", path, cmd.CommandPath())
	}
	return cmd
}
