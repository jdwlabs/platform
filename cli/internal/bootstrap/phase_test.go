package bootstrap

import (
	"testing"
)

func TestStateString(t *testing.T) {
	tests := map[State]string{
		StateAlreadyDone: "already_done",
		StateInProgress:  "in_progress",
		StateNotStarted:  "not_started",
		StateBroken:      "broken",
	}
	for s, want := range tests {
		if s.String() != want {
			t.Errorf("State(%d).String()=%s want %s", s, s.String(), want)
		}
	}
}
