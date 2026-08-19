package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/jdwlabs/platform/internal/bootstrap"
	"github.com/jdwlabs/platform/internal/k8s"
	"github.com/jdwlabs/platform/internal/prompt"
)

// runSeed drives `bootstrap seed` with no reachable Vault. Every case here must
// fail during validation, which happens before any client is built — if one ever
// reached the Vault resolver instead, this test would hang rather than fail
// quietly, which is the outcome worth noticing.
func runSeed(t *testing.T, args ...string) (string, error) {
	t.Helper()
	kc := k8s.NewFake()
	dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}: "ApplicationList",
		},
	)
	root := NewRootForTest(kc, dc)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func TestSeed_HasFieldFlag(t *testing.T) {
	root, _ := NewRoot("test")
	for _, c := range root.Commands() {
		if c.Name() != "bootstrap" {
			continue
		}
		for _, sub := range c.Commands() {
			if sub.Name() == "seed" {
				if sub.Flags().Lookup("field") == nil {
					t.Fatal("bootstrap seed is missing --field")
				}
				return
			}
		}
	}
	t.Fatal("bootstrap seed command not found")
}

func TestSeed_UnknownSpecKeyIsRejectedBeforeAnyVaultCall(t *testing.T) {
	_, err := runSeed(t, "bootstrap", "seed", "grafana-git-sync")
	if err == nil {
		t.Fatal("a mistyped spec key must fail rather than write nothing and succeed")
	}
	if !strings.Contains(err.Error(), "unknown seed spec") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "grafana-gitsync") {
		t.Fatalf("error should list the valid keys: %v", err)
	}
}

func TestSeed_UnknownFieldIsRejected(t *testing.T) {
	_, err := runSeed(t, "bootstrap", "seed", "holmes", "--field", "webhook-token")
	if err == nil {
		t.Fatal("a misspelled field must be rejected")
	}
	if !strings.Contains(err.Error(), "webhook_token") {
		t.Fatalf("error should list the spec's fields: %v", err)
	}
}

func TestSeed_FieldWithoutASingleSpecIsRejected(t *testing.T) {
	_, err := runSeed(t, "bootstrap", "seed", "--field", "webhook_token")
	if err == nil {
		t.Fatal("--field with no spec must be rejected")
	}
	if !strings.Contains(err.Error(), "exactly one spec") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSeed_HasNonInteractiveInputFlags(t *testing.T) {
	seed := findSeedCmd(t)
	for _, name := range []string{"from-file", "keep-trailing-newline"} {
		if seed.Flags().Lookup(name) == nil {
			t.Fatalf("bootstrap seed is missing --%s", name)
		}
	}
}

// A --value flag would put the credential in argv, where every process on the
// host can read it and the shell history keeps it. --from-file is the only
// sanctioned way in.
func TestSeed_HasNoValueFlag(t *testing.T) {
	if findSeedCmd(t).Flags().Lookup("value") != nil {
		t.Fatal("bootstrap seed must not accept a secret through argv")
	}
}

func TestSeed_FromFileRejectsAnAmbiguousTargetBeforeAnyVaultCall(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		wants string
	}{
		{"no spec", []string{"bootstrap", "seed", "--from-file", "-"}, "exactly one spec"},
		{"two specs", []string{"bootstrap", "seed", "truenas-csi", "porkbun", "--from-file", "-"}, "exactly one spec"},
		{"two fields", []string{"bootstrap", "seed", "holmes", "--field", "jira_url", "--field", "github_token", "--from-file", "-"}, "one field"},
		{"multi-field spec, no field", []string{"bootstrap", "seed", "holmes", "--from-file", "-"}, "needs --field"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := runSeed(t, c.args...)
			if err == nil {
				t.Fatal("expected a rejection rather than a Vault connection")
			}
			if !strings.Contains(err.Error(), c.wants) {
				t.Fatalf("error %q should mention %q", err, c.wants)
			}
		})
	}
}

func findSeedCmd(t *testing.T) *cobra.Command {
	t.Helper()
	root, _ := NewRoot("test")
	for _, c := range root.Commands() {
		if c.Name() != "bootstrap" {
			continue
		}
		for _, sub := range c.Commands() {
			if sub.Name() == "seed" {
				return sub
			}
		}
	}
	t.Fatal("bootstrap seed command not found")
	return nil
}

// The failure the ticket is about: with no terminal and no value, the command
// must refuse before it connects to anything. runSeed has no reachable Vault,
// so a regression fails here — by blocking on a prompt where a terminal exists,
// or on the next call out where one does not. Either way the assertion on the
// refusal's own text is what proves the refusal came first.
func TestSeed_NoTerminalAndNoValueRefusesBeforeAnyVaultCall(t *testing.T) {
	withoutTerminal(t)
	t.Setenv("PLATFORMCTL_TRUENAS_CSI_API_KEY", "")
	out, err := runSeed(t, "bootstrap", "seed", "truenas-csi")
	if err == nil {
		t.Fatal("expected a refusal, not a prompt or a Vault connection")
	}
	if code := ExitCode(err); code == ExitOK {
		t.Fatalf("refusal must exit non-zero, got %d", code)
	}
	if !strings.Contains(err.Error(), "no value source") {
		t.Fatalf("unexpected error: %v", err)
	}
	// The fix reaches stdout as the help line, where an agent reads it.
	for _, want := range []string{
		"--from-file <path>",
		"--from-file -",
		"PLATFORMCTL_TRUENAS_CSI_API_KEY",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output should name %q, got:\n%s", want, out)
		}
	}
	// Naming the variable is the fix; writing `VAR=<secret>` would teach the
	// caller to put the credential in its own shell history.
	if strings.Contains(out, "PLATFORMCTL_TRUENAS_CSI_API_KEY=") {
		t.Fatalf("help must not spell an inline assignment: %s", out)
	}
}

// An env var still answers for the value with no terminal attached, so an
// existing non-interactive caller keeps working.
func TestSeed_NoTerminalWithEnvPassesPreflight(t *testing.T) {
	withoutTerminal(t)
	t.Setenv("PLATFORMCTL_TRUENAS_CSI_API_KEY", "2-abcdef")
	if err := bootstrap.PreflightSeedInput(nil, []string{"truenas-csi"}, nil, nil, false); err != nil {
		t.Fatalf("an env-supplied value must satisfy preflight: %v", err)
	}
}

// --from-file is the value source, so preflight must not demand an env var it
// was never going to read. The file has to exist and hold bytes, because
// preflight reads it rather than merely noting that a source was named.
func TestSeed_FromFileSatisfiesPreflight(t *testing.T) {
	withoutTerminal(t)
	t.Setenv("PLATFORMCTL_TRUENAS_CSI_API_KEY", "")
	path := filepath.Join(t.TempDir(), "api-key")
	if err := os.WriteFile(path, []byte("2-abcdef\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := bootstrap.NewSeedValueSource(path, false, nil, []string{"truenas-csi"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.PreflightSeedInput(nil, []string{"truenas-csi"}, []string{src.Field}, src, false); err != nil {
		t.Fatalf("--from-file must satisfy preflight: %v", err)
	}
}

func withoutTerminal(t *testing.T) {
	t.Helper()
	restore := prompt.HasTerminal
	prompt.HasTerminal = func() bool { return false }
	t.Cleanup(func() { prompt.HasTerminal = restore })
}

// A flag that cannot act is worse than a rejected one: the caller believes the
// newline was kept and has nothing to read that says otherwise.
func TestSeed_KeepTrailingNewlineWithoutFromFileIsRejected(t *testing.T) {
	_, err := runSeed(t, "bootstrap", "seed", "truenas-csi", "--keep-trailing-newline")
	if err == nil {
		t.Fatal("--keep-trailing-newline must not be accepted where it does nothing")
	}
	if !strings.Contains(err.Error(), "--from-file") {
		t.Fatalf("unexpected error: %v", err)
	}
}
