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
			root, _ := NewRoot("test")
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
		})
	}
}

func TestDryRun_ContractCoversEveryCommand(t *testing.T) {
	root, _ := NewRoot("test")

	var found []string
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		found = append(found, c.CommandPath())
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
	root.SetArgs([]string{"bootstrap", "seed", "grafana", "--dry-run"})

	err := root.Execute()
	if err == nil {
		t.Fatalf("bootstrap seed must fail on --dry-run\n%s", out.String())
	}
	if ExitCode(err) == ExitOK {
		t.Fatalf("bootstrap seed --dry-run must exit non-zero, got %d", ExitCode(err))
	}
	if !strings.Contains(out.String(), "unknown flag: --dry-run") {
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
