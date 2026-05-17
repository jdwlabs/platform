package display

import (
	"bytes"
	"strings"
	"testing"
)

func TestAuditLogger_writeEntry(t *testing.T) {
	var buf bytes.Buffer
	a := NewAuditLogger(&buf)
	a.WriteEntry("CMD", "platformctl bootstrap")

	out := buf.String()
	if !strings.Contains(out, "[CMD]") {
		t.Errorf("expected [CMD] tag in output, got: %s", out)
	}
	if !strings.Contains(out, "platformctl bootstrap") {
		t.Errorf("expected message in output, got: %s", out)
	}
	if !strings.HasSuffix(strings.TrimRight(out, "\n"), "platformctl bootstrap") {
		t.Errorf("expected message at end of line, got: %s", out)
	}
}

func TestAuditLogger_multipleEntries(t *testing.T) {
	var buf bytes.Buffer
	a := NewAuditLogger(&buf)
	a.WriteEntry("START", "begin")
	a.WriteEntry("END", "done")

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 lines, got %d: %q", len(lines), buf.String())
	}
}
