package prompt

import (
	"fmt"
	"os"

	"github.com/charmbracelet/huh"
)

// String asks for a value. Env var is always checked first regardless of mode;
// only prompts when unset. Returns an error if envVar is unset in non-interactive mode.
func String(label, envVar string, nonInteractive bool) (string, error) {
	if v := os.Getenv(envVar); v != "" {
		return v, nil
	}
	if nonInteractive {
		return "", fmt.Errorf("non-interactive mode requires %s to be set", envVar)
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
	var v string
	if err := huh.NewInput().Title(label).EchoMode(huh.EchoModePassword).Value(&v).Run(); err != nil {
		return "", err
	}
	return v, nil
}

// Confirm asks a yes/no question. In non-interactive mode returns the default.
func Confirm(label string, defaultYes, nonInteractive bool) (bool, error) {
	if nonInteractive {
		return defaultYes, nil
	}
	v := defaultYes
	if err := huh.NewConfirm().Title(label).Affirmative("yes").Negative("no").Value(&v).Run(); err != nil {
		return false, err
	}
	return v, nil
}
