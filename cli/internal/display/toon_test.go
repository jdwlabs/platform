package display

import (
	"bytes"
	"strings"
	"testing"
)

func TestToonQuote(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "pvc-abc123", "pvc-abc123"},
		{"empty", "", `""`},
		{"leading space", " x", `" x"`},
		{"bool literal", "true", `"true"`},
		{"null literal", "null", `"null"`},
		{"integer", "42", `"42"`},
		{"leading zero", "05", `"05"`},
		{"exponent", "1e-6", `"1e-6"`},
		{"size suffix is not numeric", "4.2Gi", "4.2Gi"},
		{"timestamp has colons", "2026-07-30T01:02:03Z", `"2026-07-30T01:02:03Z"`},
		{"delimiter", "a,b", `"a,b"`},
		{"braces", "{x}", `"{x}"`},
		{"brackets", "[x]", `"[x]"`},
		{"hyphen prefix", "-flag", `"-flag"`},
		{"hash prefix", "#1", `"#1"`},
		{"embedded quote", `say "hi"`, `"say \"hi\""`},
		{"backslash", `a\b`, `"a\\b"`},
		{"newline", "a\nb", `"a\nb"`},
		// Both sides are built by concatenation: a literal control byte cannot be
		// typed safely into source, and its expected escape form is easier to read
		// as an explicit backslash than as an escaped escape.
		{"control char", "a" + string(rune(1)) + "b", `"a` + string(rune(92)) + `u0001b"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ToonQuote(tt.in); got != tt.want {
				t.Fatalf("ToonQuote(%q) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

func TestToonTable(t *testing.T) {
	var buf bytes.Buffer
	err := ToonTable(&buf, "volumes", []string{"name", "state", "orphan"}, [][]string{
		{"pvc-1", "detached", "true"},
		{"pvc-2", "attached", "false"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "volumes[2]{name,state,orphan}:\n" +
		"  pvc-1,detached,\"true\"\n" +
		"  pvc-2,attached,\"false\"\n"
	if buf.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestToonTable_EmptyRowsStillDeclaresCount(t *testing.T) {
	var buf bytes.Buffer
	if err := ToonTable(&buf, "volumes", []string{"name"}, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf.String() != "volumes[0]{name}:\n" {
		t.Fatalf("got %q", buf.String())
	}
}

func TestToonTable_RaggedRowIsAnError(t *testing.T) {
	var buf bytes.Buffer
	err := ToonTable(&buf, "volumes", []string{"name", "state"}, [][]string{{"pvc-1"}})
	if err == nil {
		t.Fatal("expected an error for a row with fewer cells than fields")
	}
	if !strings.Contains(err.Error(), "want 2") {
		t.Fatalf("error should name the expected cell count, got: %v", err)
	}
}

func TestToonListAndScalar(t *testing.T) {
	var buf bytes.Buffer
	if err := ToonScalar(&buf, "count", "2 total, 1 orphaned"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := ToonList(&buf, "help", []string{"Run `platformctl cluster volumes list --full`"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "count: \"2 total, 1 orphaned\"\n" +
		"help[1]:\n" +
		"  - Run `platformctl cluster volumes list --full`\n"
	if buf.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", buf.String(), want)
	}
}
