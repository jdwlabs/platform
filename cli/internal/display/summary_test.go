package display

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteSummary_success(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SUMMARY.txt")

	start := time.Date(2026, 5, 17, 2, 6, 37, 0, time.UTC)
	data := &SummaryData{
		StartTime: start,
		Duration:  4*time.Minute + 32*time.Second,
		Status:    "SUCCESS",
		Command:   "bootstrap",
		RunDir:    dir,
		PhaseResults: []PhaseResult{
			{Name: "argocd-install", Status: "ok"},
			{Name: "root-apply", Status: "ok"},
		},
	}

	if err := WriteSummary(path, data); err != nil {
		t.Fatalf("WriteSummary: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read SUMMARY.txt: %v", err)
	}
	out := string(content)

	for _, want := range []string{"SUCCESS", "bootstrap", "4m32s", "argocd-install", "root-apply"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in SUMMARY.txt, got:\n%s", want, out)
		}
	}
}

func TestWriteSummary_failure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SUMMARY.txt")

	data := &SummaryData{
		StartTime: time.Now(),
		Duration:  10 * time.Second,
		Status:    "FAILED",
		Command:   "bootstrap phase 1",
		RunDir:    dir,
		ExitError: fmt.Errorf("argocd-server not Available"),
		PhaseResults: []PhaseResult{
			{Name: "argocd-install", Status: "failed", Message: "argocd-server not Available"},
		},
	}

	if err := WriteSummary(path, data); err != nil {
		t.Fatalf("WriteSummary: %v", err)
	}

	content, _ := os.ReadFile(path)
	out := string(content)
	if !strings.Contains(out, MarkerCross) {
		t.Errorf("expected cross marker for failed phase, got:\n%s", out)
	}
	if !strings.Contains(out, "argocd-server not Available") {
		t.Errorf("expected error message in summary, got:\n%s", out)
	}
}

func TestCountFailed(t *testing.T) {
	phases := []PhaseResult{
		{Status: "ok"},
		{Status: "failed"},
		{Status: "broken"},
		{Status: "ok"},
	}
	if got := countFailed(phases); got != 2 {
		t.Errorf("countFailed = %d, want 2", got)
	}
}
