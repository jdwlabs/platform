package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Event is one structured progress record. Stable JSON schema — external AI
// agents key off these field names.
type Event struct {
	Timestamp string            `json:"ts,omitempty"`
	Phase     string            `json:"phase"`
	Name      string            `json:"name"`
	Status    string            `json:"status"`
	Message   string            `json:"message,omitempty"`
	Detail    map[string]string `json:"detail,omitempty"`
}

type Emitter struct {
	out  io.Writer
	json bool
}

func NewEmitter(out io.Writer, asJSON bool) *Emitter {
	return &Emitter{out: out, json: asJSON}
}

func (e *Emitter) Emit(ev Event) {
	if ev.Timestamp == "" {
		ev.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	if e.json {
		b, _ := json.Marshal(ev)
		fmt.Fprintln(e.out, string(b))
		return
	}
	fmt.Fprintf(e.out, "[%s] %s/%s %s — %s\n", ev.Status, ev.Phase, ev.Name, statusGlyph(ev.Status), ev.Message)
}

func statusGlyph(s string) string {
	switch s {
	case "ok":
		return "OK"
	case "progressing":
		return "WAIT"
	case "broken":
		return "BROKEN"
	case "failed":
		return "FAIL"
	default:
		return "INFO"
	}
}
