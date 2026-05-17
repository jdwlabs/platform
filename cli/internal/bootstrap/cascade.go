package bootstrap

import (
	"context"
	"fmt"
	"time"
)

// EventFunc is called by RunCascade to report phase transitions.
type EventFunc func(phase, name, status, message string)

// CascadeOptions configures RunCascade behaviour.
type CascadeOptions struct {
	OnEvent           EventFunc
	InProgressTimeout time.Duration // default 10 minutes
}

func (o CascadeOptions) emit(phase, name, status, message string) {
	if o.OnEvent != nil {
		o.OnEvent(phase, name, status, message)
	}
}

// RunCascade runs phases in order. Per phase, it polls Detect until the phase
// is no longer InProgress, then applies if NotStarted, then verifies.
func RunCascade(ctx context.Context, phases []Phase, opts CascadeOptions) error {
	if opts.InProgressTimeout == 0 {
		opts.InProgressTimeout = 45 * time.Minute
	}

	for _, p := range phases {
		if err := runPhase(ctx, p, opts); err != nil {
			return err
		}
	}
	return nil
}

func runPhase(ctx context.Context, p Phase, opts CascadeOptions) error {
	deadline := time.Now().Add(opts.InProgressTimeout)

	for {
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
			return nil

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
			return nil

		case StateInProgress:
			if time.Now().After(deadline) {
				msg := fmt.Sprintf("still in progress after %s", opts.InProgressTimeout)
				opts.emit("bootstrap", p.Name(), "progressing", msg)
				return &ProgressingError{Detail: fmt.Sprintf("phase %d (%s): %s", p.Number(), p.Name(), msg)}
			}
			elapsed := time.Until(deadline.Add(-opts.InProgressTimeout)).Abs()
			detail := fmt.Sprintf("waiting (%s elapsed)", elapsed.Round(time.Second))
			if pm, ok := p.(ProgressMessenger); ok {
				if msg := pm.ProgressMessage(ctx); msg != "" {
					detail = fmt.Sprintf("waiting (%s elapsed): %s", elapsed.Round(time.Second), msg)
				}
			}
			opts.emit("bootstrap", p.Name(), "progressing", detail)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(15 * time.Second):
				// re-detect on next iteration
			}

		case StateUnknown:
			msg := fmt.Sprintf("cannot determine state: %s", errStr(stErr))
			opts.emit("bootstrap", p.Name(), "failed", msg)
			return fmt.Errorf("phase %d (%s): %s", p.Number(), p.Name(), msg)

		default:
			return fmt.Errorf("phase %d returned unhandled state %d", p.Number(), st)
		}
	}
}

// BrokenError and ProgressingError are returned by RunCascade so cli.ExitCode
// can map them to exit codes 3 and 2 respectively.
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
