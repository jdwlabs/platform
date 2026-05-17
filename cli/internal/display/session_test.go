package display

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewRunSession_createsLogFiles(t *testing.T) {
	dir := t.TempDir()
	sess, err := NewRunSession(Config{
		LogDir:  dir,
		Command: "bootstrap",
		NoColor: true,
	})
	if err != nil {
		t.Fatalf("NewRunSession: %v", err)
	}
	defer sess.Close(nil)

	for _, name := range []string{"console.log", "structured.log", "audit.log"} {
		path := filepath.Join(sess.RunDir, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected %s to exist", path)
		}
	}
}

func TestNewRunSession_registersRunsLog(t *testing.T) {
	dir := t.TempDir()
	sess, err := NewRunSession(Config{LogDir: dir, Command: "bootstrap", NoColor: true})
	if err != nil {
		t.Fatalf("NewRunSession: %v", err)
	}
	defer sess.Close(nil)

	runsLog := filepath.Join(dir, "runs.log")
	data, err := os.ReadFile(runsLog)
	if err != nil {
		t.Fatalf("runs.log not created: %v", err)
	}
	if !strings.Contains(string(data), "bootstrap") {
		t.Errorf("runs.log should contain command, got: %s", string(data))
	}
}

func TestRunSession_close_writesStatusToRunsLog(t *testing.T) {
	dir := t.TempDir()
	sess, _ := NewRunSession(Config{LogDir: dir, Command: "test", NoColor: true})
	sess.Close(fmt.Errorf("something broke"))

	runsLog := filepath.Join(dir, "runs.log")
	data, _ := os.ReadFile(runsLog)
	if !strings.Contains(string(data), "failed") {
		t.Errorf("runs.log should contain 'failed', got: %s", string(data))
	}
}

func TestRunSession_recordPhase_counters(t *testing.T) {
	dir := t.TempDir()
	sess, _ := NewRunSession(Config{LogDir: dir, Command: "bootstrap", NoColor: true})
	defer sess.Close(nil)

	sess.RecordPhase("argocd-install", "ok", "")
	sess.RecordPhase("root-apply", "failed", "timeout")
	sess.RecordPhase("vault-init", "ok", "")

	if sess.PhasesRun != 3 {
		t.Errorf("PhasesRun = %d, want 3", sess.PhasesRun)
	}
	if sess.PhasesFailed != 1 {
		t.Errorf("PhasesFailed = %d, want 1", sess.PhasesFailed)
	}
}

func TestRunSession_close_writesSummaryTxt(t *testing.T) {
	dir := t.TempDir()
	sess, _ := NewRunSession(Config{LogDir: dir, Command: "bootstrap", NoColor: true})
	sess.RecordPhase("argocd-install", "ok", "")
	sess.Close(nil)

	summaryPath := filepath.Join(sess.RunDir, "SUMMARY.txt")
	data, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("SUMMARY.txt not created: %v", err)
	}
	if !strings.Contains(string(data), "SUCCESS") {
		t.Errorf("SUMMARY.txt should contain SUCCESS, got: %s", string(data))
	}
}

func TestNewRunSession_writesLatestTxt(t *testing.T) {
	dir := t.TempDir()
	sess, _ := NewRunSession(Config{LogDir: dir, Command: "bootstrap", NoColor: true})
	defer sess.Close(nil)

	latestPath := filepath.Join(dir, "latest.txt")
	data, err := os.ReadFile(latestPath)
	if err != nil {
		t.Fatalf("latest.txt not created: %v", err)
	}
	if !strings.Contains(string(data), sess.RunDir) {
		t.Errorf("latest.txt should contain run dir, got: %s", string(data))
	}
}
