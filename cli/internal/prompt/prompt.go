package prompt

import (
	"fmt"
	"os"
	"runtime"

	"charm.land/huh/v2"
)

// HasTerminal reports whether an interactive form can actually reach a
// terminal.
//
// A variable, and exported, so a test can drive both branches: the real check
// depends on the process's controlling terminal, which a test cannot grant or
// take away from itself.
var HasTerminal = func() bool {
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	// The form library opens the controlling terminal directly rather than
	// reading stdin, so a character-device stdin is necessary but not
	// sufficient — under a detached session /dev/tty is absent and the form
	// fails deep inside itself, naming /dev/tty rather than the flag that
	// would have worked.
	f, err := os.Open("/dev/tty")
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// noTTY names the input the caller can supply instead of a terminal. The
// message must carry the fix: this error is what an automated caller sees
// where a human would have seen a prompt, and it is its only chance to learn
// that a non-interactive path exists.
func noTTY(envVar string) error {
	return fmt.Errorf(
		"no terminal available and %s is unset; supply the value with `--field <name> --from-file <path>` "+
			"(`--from-file -` reads stdin), or export %s", envVar, envVar)
}

// String asks for a value. Env var is always checked first regardless of mode;
// only prompts when unset. Returns an error if envVar is unset in
// non-interactive mode or when no terminal is attached.
func String(label, envVar string, nonInteractive bool) (string, error) {
	if v := os.Getenv(envVar); v != "" {
		return v, nil
	}
	if nonInteractive {
		return "", fmt.Errorf("non-interactive mode requires %s to be set", envVar)
	}
	if !HasTerminal() {
		return "", noTTY(envVar)
	}
	var v string
	if err := huh.NewInput().Title(label).Value(&v).Run(); err != nil {
		return "", err
	}
	return v, nil
}

// Secret is like String but masks input in interactive mode.
func Secret(label, envVar string, nonInteractive bool) (string, error) {
	if v := os.Getenv(envVar); v != "" {
		return v, nil
	}
	if nonInteractive {
		return "", fmt.Errorf("non-interactive mode requires %s to be set", envVar)
	}
	if !HasTerminal() {
		return "", noTTY(envVar)
	}
	var v string
	if err := huh.NewInput().Title(label).EchoMode(huh.EchoModePassword).Value(&v).Run(); err != nil {
		return "", err
	}
	return v, nil
}

// Confirm asks a yes/no question. In non-interactive mode, or with no terminal
// attached, returns the default rather than blocking on a form that cannot be
// answered.
func Confirm(label string, defaultYes, nonInteractive bool) (bool, error) {
	if nonInteractive || !HasTerminal() {
		return defaultYes, nil
	}
	v := defaultYes
	if err := huh.NewConfirm().Title(label).Affirmative("yes").Negative("no").Value(&v).Run(); err != nil {
		return false, err
	}
	return v, nil
}
