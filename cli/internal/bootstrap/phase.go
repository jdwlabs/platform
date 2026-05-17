package bootstrap

import "context"

// State is the outcome of a Phase.Detect call (spec §5.3).
type State int

const (
	StateUnknown State = iota
	StateAlreadyDone
	StateInProgress
	StateNotStarted
	StateBroken
)

func (s State) String() string {
	switch s {
	case StateAlreadyDone:
		return "already_done"
	case StateInProgress:
		return "in_progress"
	case StateNotStarted:
		return "not_started"
	case StateBroken:
		return "broken"
	default:
		return "unknown"
	}
}

// Phase is the contract every bootstrap step implements (spec §5.3).
type Phase interface {
	Name() string
	Number() int
	Detect(ctx context.Context) (State, error)
	Apply(ctx context.Context) error
	Verify(ctx context.Context) error
}

// ProgressMessenger is an optional Phase extension. Phases that implement it
// can return a human-readable status string that RunCascade appends to the
// "waiting (X elapsed)" progress event during StateInProgress polling.
type ProgressMessenger interface {
	ProgressMessage(ctx context.Context) string
}
