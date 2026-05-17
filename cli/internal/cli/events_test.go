package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jdwlabs/platform/internal/display"
)

func TestEmitter_JSON(t *testing.T) {
	var buf bytes.Buffer
	e := NewEmitter(&buf, true)
	e.Emit(Event{Phase: "verify", Name: "argocd-ready", Status: "ok", Message: "deploy/argocd-server Available"})
	e.Emit(Event{Phase: "verify", Name: "vault", Status: "broken", Message: "not unsealed"})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	var first Event
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if first.Status != "ok" || first.Phase != "verify" {
		t.Fatalf("unexpected event: %+v", first)
	}
}

func TestEmitter_Text_fallback(t *testing.T) {
	var buf bytes.Buffer
	e := NewEmitter(&buf, false)
	e.Emit(Event{Phase: "heal", Name: "stuck-finalizer", Status: "ok", Message: "stripped"})
	out := buf.String()
	if !strings.Contains(out, "heal/stuck-finalizer") || !strings.Contains(out, "stripped") {
		t.Fatalf("missing fields in human output: %q", out)
	}
}

func TestEmitter_session_delegates_to_zap(t *testing.T) {
	dir := t.TempDir()
	sess, err := display.NewRunSession(display.Config{LogDir: dir, Command: "test", NoColor: true})
	if err != nil {
		t.Fatalf("NewRunSession: %v", err)
	}
	defer sess.Close(nil)

	var buf bytes.Buffer
	e := NewEmitter(&buf, false)
	e.SetSession(sess)

	// Emit should not write to buf (fallback writer) when session is set
	e.Emit(Event{Phase: "bootstrap", Name: "argocd-install", Status: "ok", Message: "applied"})

	if strings.Contains(buf.String(), "argocd-install") {
		t.Errorf("expected session path (not fallback) but fallback writer was used: %s", buf.String())
	}
}

func TestEmitter_JSON_path_unaffected_by_session(t *testing.T) {
	dir := t.TempDir()
	sess, _ := display.NewRunSession(display.Config{LogDir: dir, Command: "test", NoColor: true})
	defer sess.Close(nil)

	var buf bytes.Buffer
	e := NewEmitter(&buf, true) // --json mode
	e.SetSession(sess)

	e.Emit(Event{Phase: "bootstrap", Name: "argocd-install", Status: "ok"})

	// JSON path must still write to buf regardless of session
	if buf.Len() == 0 {
		t.Error("JSON path should write to out writer even when session is set")
	}
	var ev Event
	if err := json.Unmarshal(buf.Bytes(), &ev); err != nil {
		t.Errorf("expected valid JSON output, got: %q", buf.String())
	}
}
