package display

import (
	"bytes"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestNewKVCore_writesKeyValue(t *testing.T) {
	var buf bytes.Buffer
	core := NewKVCore(zapcore.InfoLevel, true, &buf)
	logger := zap.New(core)
	logger.Info("argocd-install", zap.String("phase", "bootstrap"), zap.String("state", "applying"))

	out := buf.String()
	if !strings.Contains(out, "phase=bootstrap") {
		t.Errorf("expected phase=bootstrap in output, got: %s", out)
	}
	if !strings.Contains(out, "state=applying") {
		t.Errorf("expected state=applying in output, got: %s", out)
	}
	if !strings.Contains(out, "argocd-install") {
		t.Errorf("expected message in output, got: %s", out)
	}
}

func TestNewKVCore_belowLevel_suppressed(t *testing.T) {
	var buf bytes.Buffer
	core := NewKVCore(zapcore.WarnLevel, true, &buf)
	logger := zap.New(core)
	logger.Info("should-not-appear")
	if buf.Len() > 0 {
		t.Errorf("expected no output for Info below WarnLevel, got: %s", buf.String())
	}
}

func TestKVEncoder_clone_independent(t *testing.T) {
	var buf1, buf2 bytes.Buffer
	core := NewKVCore(zapcore.InfoLevel, true, &buf1)
	logger1 := zap.New(core).With(zap.String("env", "test"))

	core2 := NewKVCore(zapcore.InfoLevel, true, &buf2)
	logger2 := zap.New(core2)

	logger1.Info("msg1")
	logger2.Info("msg2")

	if !strings.Contains(buf1.String(), "env=test") {
		t.Errorf("cloned encoder should carry fields: %s", buf1.String())
	}
	if strings.Contains(buf2.String(), "env=test") {
		t.Errorf("independent encoder should not have cloned fields: %s", buf2.String())
	}
}

func TestColorLevelEncoder_noColor(t *testing.T) {
	var buf bytes.Buffer
	core := NewKVCore(zapcore.DebugLevel, true, &buf)
	logger := zap.New(core)
	logger.Debug("test")
	out := buf.String()
	if strings.Contains(out, "\033[") {
		t.Errorf("noColor=true should produce no ANSI codes, got: %s", out)
	}
}
