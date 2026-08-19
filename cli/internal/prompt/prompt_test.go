package prompt

import (
	"os"
	"strings"
	"testing"
)

func TestString_NonInteractive_UsesEnv(t *testing.T) {
	t.Setenv("PLATFORMCTL_PORKBUN_API_KEY", "from-env")
	got, err := String("porkbun api key", "PLATFORMCTL_PORKBUN_API_KEY", true)
	if err != nil {
		t.Fatal(err)
	}
	if got != "from-env" {
		t.Fatalf("got %q", got)
	}
}

func TestString_NonInteractive_MissingEnv(t *testing.T) {
	os.Unsetenv("PLATFORMCTL_TEST_MISSING")
	_, err := String("label", "PLATFORMCTL_TEST_MISSING", true)
	if err == nil {
		t.Fatal("expected error for missing env var")
	}
}

func TestSecret_NonInteractive_UsesEnv(t *testing.T) {
	t.Setenv("PLATFORMCTL_SECRET_VAL", "mysecret")
	got, err := Secret("secret label", "PLATFORMCTL_SECRET_VAL", true)
	if err != nil {
		t.Fatal(err)
	}
	if got != "mysecret" {
		t.Fatalf("got %q", got)
	}
}

func TestConfirm_NonInteractive_ReturnsDefault(t *testing.T) {
	got, err := Confirm("proceed?", true, true)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("expected default true")
	}
}

func withoutTerminal(t *testing.T) {
	t.Helper()
	restore := HasTerminal
	HasTerminal = func() bool { return false }
	t.Cleanup(func() { HasTerminal = restore })
}

// Without this the form library is entered anyway and fails naming /dev/tty,
// which tells the caller nothing about the flag that would have worked.
func TestSecret_NoTerminalFailsWithTheFixInsteadOfPrompting(t *testing.T) {
	withoutTerminal(t)
	os.Unsetenv("PLATFORMCTL_TEST_NO_TTY")
	_, err := Secret("label", "PLATFORMCTL_TEST_NO_TTY", false)
	if err == nil {
		t.Fatal("expected a refusal with no terminal attached")
	}
	for _, want := range []string{"no terminal available", "--from-file", "PLATFORMCTL_TEST_NO_TTY"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q should name %q", err, want)
		}
	}
}

func TestString_NoTerminalFailsWithTheFixInsteadOfPrompting(t *testing.T) {
	withoutTerminal(t)
	os.Unsetenv("PLATFORMCTL_TEST_NO_TTY")
	_, err := String("label", "PLATFORMCTL_TEST_NO_TTY", false)
	if err == nil {
		t.Fatal("expected a refusal with no terminal attached")
	}
	if !strings.Contains(err.Error(), "no terminal available") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// The env var is a value, not a mode: it answers the question whether or not a
// terminal is attached.
func TestSecret_NoTerminalStillUsesEnv(t *testing.T) {
	withoutTerminal(t)
	t.Setenv("PLATFORMCTL_TEST_NO_TTY", "from-env")
	got, err := Secret("label", "PLATFORMCTL_TEST_NO_TTY", false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "from-env" {
		t.Fatalf("got %q", got)
	}
}

func TestConfirm_NoTerminalReturnsDefault(t *testing.T) {
	withoutTerminal(t)
	got, err := Confirm("proceed?", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatal("expected the default with no terminal attached")
	}
}
