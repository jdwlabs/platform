package cli

import (
	"errors"
	"testing"
)

func TestExitCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"nil error returns 0", nil, ExitOK},
		{"generic error returns 1", errors.New("boom"), ExitHardFail},
		{"in-progress wraps to 2", &ProgressingError{Detail: "waiting"}, ExitProgressing},
		{"broken-state error wraps to 3", &BrokenStateError{Detail: "stuck"}, ExitBroken},
		{"user abort wraps to 4", &UserAbortError{}, ExitUserAbort},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExitCode(tc.err)
			if got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
		})
	}
}
