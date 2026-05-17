package display

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBox_headerFooter(t *testing.T) {
	var buf bytes.Buffer
	b := NewBox(&buf, true)
	b.Header("BOOTSTRAP SUMMARY")
	b.Footer()

	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// Header = top border + title + separator (3 lines); Footer = bottom border (1 line)
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines (top, title, sep, bottom), got %d:\n%s", len(lines), out)
	}

	// Each line should be exactly boxWidth runes wide (no ANSI in noColor mode)
	for i, line := range lines {
		got := utf8.RuneCountInString(line)
		if got != boxWidth {
			t.Errorf("line %d: expected %d runes, got %d: %q", i, boxWidth, got, line)
		}
	}
}

func TestBox_row_padding(t *testing.T) {
	var buf bytes.Buffer
	b := NewBox(&buf, true)
	b.Header("X")
	buf.Reset()

	b.Row("Status", "SUCCESS")
	line := strings.TrimRight(buf.String(), "\n")
	got := utf8.RuneCountInString(line)
	if got != boxWidth {
		t.Errorf("Row line: expected %d runes, got %d: %q", boxWidth, got, line)
	}
}

func TestBox_item_check(t *testing.T) {
	var buf bytes.Buffer
	b := NewBox(&buf, true)
	b.Item(MarkerCheck, "phase 1  argocd-install")
	out := buf.String()
	if !strings.Contains(out, MarkerCheck) {
		t.Errorf("expected check marker in output: %s", out)
	}
}

func TestStripANSI(t *testing.T) {
	input := "\033[32mHello\033[0m World"
	got := StripANSI(input)
	want := "Hello World"
	if got != want {
		t.Errorf("StripANSI(%q) = %q, want %q", input, got, want)
	}
}

func TestBox_wrap_longLine(t *testing.T) {
	var buf bytes.Buffer
	b := NewBox(&buf, true)
	// 80-char content that exceeds inner width (61)
	long := strings.Repeat("x", 80)
	b.writeLine(long)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) < 2 {
		t.Errorf("expected wrap onto 2+ lines for 80-char content")
	}
	for i, line := range lines {
		got := utf8.RuneCountInString(line)
		if got != boxWidth {
			t.Errorf("wrapped line %d: expected %d runes, got %d: %q", i, boxWidth, got, line)
		}
	}
}
