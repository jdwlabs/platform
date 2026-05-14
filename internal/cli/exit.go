package cli

import (
	"errors"
	"os"
)

const (
	ExitOK          = 0
	ExitHardFail    = 1
	ExitProgressing = 2
	ExitBroken      = 3
	ExitUserAbort   = 4
)

type ProgressingError struct{ Detail string }

func (e *ProgressingError) Error() string { return "progressing: " + e.Detail }

type BrokenStateError struct{ Detail string }

func (e *BrokenStateError) Error() string { return "broken: " + e.Detail }

type UserAbortError struct{}

func (e *UserAbortError) Error() string { return "user abort" }

// ExitCode maps an error returned from any cobra RunE into the exit code
// contract defined in the spec (§5.3).
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var progressing *ProgressingError
	if errors.As(err, &progressing) {
		return ExitProgressing
	}
	var broken *BrokenStateError
	if errors.As(err, &broken) {
		return ExitBroken
	}
	var abort *UserAbortError
	if errors.As(err, &abort) {
		return ExitUserAbort
	}
	return ExitHardFail
}

// Exit terminates the process. Wraps os.Exit so tests can hot-swap it.
var Exit = func(code int) { os.Exit(code) }
