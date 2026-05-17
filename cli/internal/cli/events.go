package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/jdwlabs/platform/internal/display"
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
	out     io.Writer
	json    bool
	session *display.RunSession
}

func NewEmitter(out io.Writer, asJSON bool) *Emitter {
	return &Emitter{out: out, json: asJSON}
}

// SetSession wires the emitter to a RunSession so Emit delegates to zap.
func (e *Emitter) SetSession(sess *display.RunSession) {
	e.session = sess
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
	if e.session != nil {
		lvl := statusZapLevel(ev.Status)
		e.session.Logger.Log(lvl, ev.Name,
			zap.String("phase", ev.Phase),
			zap.String("state", ev.Status),
			zap.String("msg", ev.Message),
		)
		return
	}
	// Fallback: plain text (used in tests and when no session is present)
	fmt.Fprintf(e.out, "[%s] %s/%s %s — %s\n", ev.Status, ev.Phase, ev.Name, statusGlyph(ev.Status), ev.Message)
}

func statusZapLevel(s string) zapcore.Level {
	switch s {
	case "ok":
		return zapcore.InfoLevel
	case "progressing":
		return zapcore.WarnLevel
	case "broken", "failed":
		return zapcore.ErrorLevel
	default:
		return zapcore.InfoLevel
	}
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
