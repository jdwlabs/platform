package bootstrap

import (
	"context"
	"testing"
)

type dummyPhase struct {
	name   string
	num    int
	detect func() (State, error)
	apply  func() error
	verify func() error
}

func (d *dummyPhase) Name() string                                    { return d.name }
func (d *dummyPhase) Number() int                                     { return d.num }
func (d *dummyPhase) Detect(ctx context.Context) (State, error)      { return d.detect() }
func (d *dummyPhase) Apply(ctx context.Context) error                 { return d.apply() }
func (d *dummyPhase) Verify(ctx context.Context) error                { return d.verify() }

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
