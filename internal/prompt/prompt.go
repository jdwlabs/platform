package prompt

import (
	"fmt"
	"os"

	"github.com/charmbracelet/huh"
)

// String asks for a value. When nonInteractive is true, reads envVar instead
// of prompting. Returns an error if envVar is unset in non-interactive mode.
func String(label, envVar string, nonInteractive bool) (string, error) {
	if nonInteractive {
		v, ok := os.LookupEnv(envVar)
		if !ok || v == "" {
			return "", fmt.Errorf("non-interactive mode requires %s to be set", envVar)
		}
		return v, nil
	}
	var v string
	if err := huh.NewInput().Title(label).Value(&v).Run(); err != nil {
		return "", err
	}
	return v, nil
}

// Secret is like String but masks input in interactive mode.
func Secret(label, envVar string, nonInteractive bool) (string, error) {
	if nonInteractive {
		return String(label, envVar, true)
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
