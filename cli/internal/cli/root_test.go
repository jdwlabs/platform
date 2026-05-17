package cli

import (
	"bytes"
	"testing"
)

func TestRoot_HasGlobalFlags(t *testing.T) {
	cmd, _ := NewRoot("test")
	for _, name := range []string{"branch", "dry-run", "non-interactive", "json", "no-color"} {
		if cmd.PersistentFlags().Lookup(name) == nil {
			t.Errorf("missing global flag --%s", name)
		}
	}
}

func TestRoot_HelpRuns(t *testing.T) {
	cmd, _ := NewRoot("test")
	cmd.SetArgs([]string{"--help"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("help failed: %v", err)
	}
	if out.Len() == 0 {
		t.Fatalf("no help output")
	}
}
