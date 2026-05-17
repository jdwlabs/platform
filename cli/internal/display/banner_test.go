package display

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintBanner_containsVersion(t *testing.T) {
	var buf bytes.Buffer
	PrintBanner(&buf, "v0.1.0", true)
	out := buf.String()
	if !strings.Contains(out, "v0.1.0") {
		t.Errorf("expected version in banner output, got: %s", out)
	}
	if !strings.Contains(out, "Platform Bootstrap Tool") {
		t.Errorf("expected 'Platform Bootstrap Tool' in banner output, got: %s", out)
	}
}

func TestPrintBanner_noColor_noANSI(t *testing.T) {
	var buf bytes.Buffer
	PrintBanner(&buf, "v0.1.0", true)
	if strings.Contains(buf.String(), "\033[") {
		t.Errorf("noColor=true should produce no ANSI codes")
	}
}

func TestPrintBanner_color_hasANSI(t *testing.T) {
	var buf bytes.Buffer
	PrintBanner(&buf, "v0.1.0", false)
	if !strings.Contains(buf.String(), "\033[") {
		t.Errorf("noColor=false should produce ANSI codes")
	}
}
