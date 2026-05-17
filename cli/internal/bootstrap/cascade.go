package bootstrap

import (
	"context"
	"fmt"
	"time"
)

// EventFunc is called by RunCascade to report phase transitions.
// The CLI layer wraps this with its Emitter.
type EventFunc func(phase, name, status, message string)

// CascadeOptions configures RunCascade behaviour.
type CascadeOptions struct {
	OnEvent           EventFunc
	InProgressTimeout time.Duration // default 5 minutes
}

func (o CascadeOptions) emit(phase, name, status, message string) {
	if o.OnEvent != nil {
		o.OnEvent(phase, name, status, message)
	}
}

// RunCascade runs phases in order, dispatching on Detect state per spec §5.3:
//   - already_done → skip Apply, run Verify, continue
//   - not_started  → run Apply, run Verify, continue
//   - in_progress  → wait up to InProgressTimeout, error if still pending
//   - broken       → stop immediately, return BrokenStateError
func RunCascade(ctx context.Context, phases []Phase, opts CascadeOptions) error {
	if opts.InProgressTimeout == 0 {
		opts.InProgressTimeout = 5 * time.Minute
	}

	for _, p := range phases {
		st, stErr := p.Detect(ctx)
		opts.emit("bootstrap", p.Name(), "info", "state="+st.String())

		switch st {
		case StateBroken:
			detail := fmt.Sprintf("phase %d (%s): %s", p.Number(), p.Name(), errStr(stErr))
			opts.emit("bootstrap", p.Name(), "broken", detail)
			return &BrokenError{Detail: detail}

		case StateAlreadyDone:
			if err := p.Verify(ctx); err != nil {
				opts.emit("bootstrap", p.Name(), "failed", err.Error())
				return err
			}
			opts.emit("bootstrap", p.Name(), "ok", "already done")

		case StateNotStarted:
			opts.emit("bootstrap", p.Name(), "progressing", "applying")
			if err := p.Apply(ctx); err != nil {
				opts.emit("bootstrap", p.Name(), "failed", err.Error())
				return err
			}
			if err := p.Verify(ctx); err != nil {
				opts.emit("bootstrap", p.Name(), "failed", err.Error())
				return err
			}
			opts.emit("bootstrap", p.Name(), "ok", "applied and verified")

		case StateInProgress:
			waitCtx, cancel := context.WithTimeout(ctx, opts.InProgressTimeout)
			err := waitUntilDone(waitCtx, p)
			cancel()
			if err != nil {
				msg := fmt.Sprintf("phase %d (%s) still progressing after %s", p.Number(), p.Name(), opts.InProgressTimeout)
				opts.emit("bootstrap", p.Name(), "progressing", msg)
				return &ProgressingError{Detail: msg}
			}
			opts.emit("bootstrap", p.Name(), "ok", "converged")

		case StateUnknown:
			msg := fmt.Sprintf("cannot determine state: %s", errStr(stErr))
			opts.emit("bootstrap", p.Name(), "failed", msg)
			return fmt.Errorf("phase %d (%s): %s", p.Number(), p.Name(), msg)

		default:
			return fmt.Errorf("phase %d returned unhandled state %d", p.Number(), st)
		}
	}
	return nil
}

func waitUntilDone(ctx context.Context, p Phase) error {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		st, _ := p.Detect(ctx)
		if st == StateAlreadyDone {
			return p.Verify(ctx)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// BrokenError and ProgressingError are returned by RunCascade so cli.ExitCode
// can map them to exit codes 3 and 2 respectively (spec §5.3).
type BrokenError struct{ Detail string }

func (e *BrokenError) Error() string { return "broken: " + e.Detail }

type ProgressingError struct{ Detail string }

func (e *ProgressingError) Error() string { return "progressing: " + e.Detail }

func errStr(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}
