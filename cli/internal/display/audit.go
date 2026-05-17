package display

import (
	"fmt"
	"io"
	"time"
)

// AuditLogger records command invocations and state changes to an audit trail.
type AuditLogger struct {
	w io.Writer
}

// NewAuditLogger creates an AuditLogger writing to w.
func NewAuditLogger(w io.Writer) *AuditLogger {
	return &AuditLogger{w: w}
}

// WriteEntry writes a tagged audit log entry with a timestamp.
func (a *AuditLogger) WriteEntry(tag, message string) {
	ts := time.Now().Format("2006-01-02 15:04:05")
	_, _ = fmt.Fprintf(a.w, "[%s] [%s] %s\n", ts, tag, message)
}
