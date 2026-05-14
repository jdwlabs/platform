package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
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

func TestEmitter_Text(t *testing.T) {
	var buf bytes.Buffer
	e := NewEmitter(&buf, false)
	e.Emit(Event{Phase: "heal", Name: "stuck-finalizer", Status: "ok", Message: "stripped"})
	out := buf.String()
	if !strings.Contains(out, "heal/stuck-finalizer") || !strings.Contains(out, "stripped") {
		t.Fatalf("missing fields in human output: %q", out)
	}
}
