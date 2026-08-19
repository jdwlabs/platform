package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jdwlabs/platform/internal/prompt"
)

func TestSeedValueSource_ReadsStdinWithNoTerminal(t *testing.T) {
	noTerminal(t)
	src := &SeedValueSource{Path: "-", Field: "api_key", Stdin: strings.NewReader("2-abcdef\n")}
	got, err := src.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got != "2-abcdef" {
		t.Fatalf("got %q, want %q", got, "2-abcdef")
	}
}

func TestSeedValueSource_ReadsFile(t *testing.T) {
	path := writeTemp(t, "hunter2")
	src := &SeedValueSource{Path: path, Field: "api_key"}
	got, err := src.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got != "hunter2" {
		t.Fatalf("got %q", got)
	}
}

// A value that begins and ends with a quote is stored with its quotes. The
// alternative — stripping them — corrupts every secret that legitimately
// contains one, and cannot tell the two cases apart.
func TestSeedValueSource_PreservesEmbeddedAndSurroundingQuotes(t *testing.T) {
	for name, value := range map[string]string{
		"surrounding double": `"2-abcdef"`,
		"surrounding single": `'2-abcdef'`,
		"embedded double":    `pa"ss`,
		"embedded single":    `pa'ss`,
		"both":               `a"b'c`,
		"leading whitespace": "  padded",
		"inner whitespace":   "two words",
	} {
		t.Run(name, func(t *testing.T) {
			src := &SeedValueSource{Path: writeTemp(t, value), Field: "api_key"}
			got, err := src.Read()
			if err != nil {
				t.Fatal(err)
			}
			if got != value {
				t.Fatalf("got %q, want %q", got, value)
			}
		})
	}
}

func TestSeedValueSource_TrailingNewline(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		keep bool
		want string
	}{
		{"one newline dropped", "secret\n", false, "secret"},
		{"crlf dropped", "secret\r\n", false, "secret"},
		{"only the last newline dropped", "line1\nline2\n", false, "line1\nline2"},
		{"blank line kept", "secret\n\n", false, "secret\n"},
		{"kept on request", "secret\n", true, "secret\n"},
		{"absent newline is not invented", "secret", false, "secret"},
		{"lone carriage return is not a terminator", "secret\r", false, "secret\r"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := &SeedValueSource{Path: writeTemp(t, c.raw), Field: "api_key", KeepTrailingNewline: c.keep}
			got, err := src.Read()
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestSeedValueSource_ReadIsIdempotentForStdin(t *testing.T) {
	src := &SeedValueSource{Path: "-", Field: "api_key", Stdin: strings.NewReader("once")}
	first, err := src.Read()
	if err != nil {
		t.Fatal(err)
	}
	second, err := src.Read()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("second read returned %q, want %q", second, first)
	}
}

func TestSeedValueSource_EmptyInputIsAnError(t *testing.T) {
	src := &SeedValueSource{Path: "-", Field: "api_key", Stdin: strings.NewReader("\n")}
	_, err := src.Read()
	if err == nil {
		t.Fatal("an empty value must fail rather than write an empty credential")
	}
	if !strings.Contains(err.Error(), "empty value") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSeedValueSource_MissingFileNamesThePath(t *testing.T) {
	src := &SeedValueSource{Path: "/nonexistent/api-key", Field: "api_key"}
	_, err := src.Read()
	if err == nil {
		t.Fatal("expected an error for a missing file")
	}
	if !strings.Contains(err.Error(), "/nonexistent/api-key") {
		t.Fatalf("error should name the path: %v", err)
	}
}

func TestNewSeedValueSource_NilWithoutTheFlag(t *testing.T) {
	src, err := NewSeedValueSource("", false, nil, []string{"truenas-csi"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if src != nil {
		t.Fatal("no --from-file must leave the interactive path in place")
	}
}

func TestNewSeedValueSource_InfersTheOnlyFieldOfASingleFieldSpec(t *testing.T) {
	src, err := NewSeedValueSource("-", false, nil, []string{"truenas-csi"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if src.Field != "api_key" {
		t.Fatalf("got field %q, want api_key", src.Field)
	}
}

func TestNewSeedValueSource_RejectsAmbiguousAndUnknownSelections(t *testing.T) {
	cases := []struct {
		name     string
		selected []string
		fields   []string
		wants    string
	}{
		{"no spec", nil, nil, "exactly one spec"},
		{"two specs", []string{"truenas-csi", "porkbun"}, nil, "exactly one spec"},
		{"two fields", []string{"holmes"}, []string{"jira_url", "github_token"}, "one field"},
		{"multi-field spec with no field", []string{"holmes"}, nil, "needs --field"},
		{"unknown field", []string{"holmes"}, []string{"webhook-token"}, "webhook_token"},
		{"unknown spec", []string{"grafana-git-sync"}, nil, "unknown seed spec"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewSeedValueSource("-", false, nil, c.selected, c.fields)
			if err == nil {
				t.Fatal("expected a rejection")
			}
			if !strings.Contains(err.Error(), c.wants) {
				t.Fatalf("error %q should mention %q", err, c.wants)
			}
		})
	}
}

// The seed must refuse, not block, when it has no value and no terminal to ask
// for one. A hang here is the worst outcome: it consumes the caller's whole
// timeout budget and reports nothing.
func TestVaultSeedPhase_NoValueAndNoTerminalFailsWithTheFix(t *testing.T) {
	noTerminal(t)
	os.Unsetenv("PLATFORMCTL_TRUENAS_CSI_API_KEY")
	p := NewVaultSeedPhase(nil, false, "kv", nil, []string{"truenas-csi"})
	_, err := p.fieldValue(seedField{"api_key", "PLATFORMCTL_TRUENAS_CSI_API_KEY", true, false}, "truenas-csi")
	if err == nil {
		t.Fatal("expected a refusal, not a prompt")
	}
	for _, want := range []string{"no terminal available", "--from-file", "PLATFORMCTL_TRUENAS_CSI_API_KEY"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q should name %q", err, want)
		}
	}
}

func TestVaultSeedPhase_ValueSourceBypassesThePrompt(t *testing.T) {
	noTerminal(t)
	p := NewVaultSeedPhase(nil, false, "kv", nil, []string{"truenas-csi"})
	p.SetValueSource(&SeedValueSource{Path: "-", Field: "api_key", Stdin: strings.NewReader("2-abcdef\n")})
	got, err := p.fieldValue(seedField{"api_key", "PLATFORMCTL_TRUENAS_CSI_API_KEY", true, false}, "truenas-csi")
	if err != nil {
		t.Fatal(err)
	}
	if got != "2-abcdef" {
		t.Fatalf("got %q", got)
	}
}

func noTerminal(t *testing.T) {
	t.Helper()
	restore := prompt.HasTerminal
	prompt.HasTerminal = func() bool { return false }
	t.Cleanup(func() { prompt.HasTerminal = restore })
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "value")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
