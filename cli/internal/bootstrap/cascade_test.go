package bootstrap

import (
	"context"
	"errors"
	"testing"
)

type stubPhase struct {
	name      string
	num       int
	state     State
	stateErr  error
	applied   bool
	verified  bool
	applyErr  error
	verifyErr error
}

func (s *stubPhase) Name() string                              { return s.name }
func (s *stubPhase) Number() int                               { return s.num }
func (s *stubPhase) Detect(_ context.Context) (State, error)  { return s.state, s.stateErr }
func (s *stubPhase) Apply(_ context.Context) error            { s.applied = true; return s.applyErr }
func (s *stubPhase) Verify(_ context.Context) error           { s.verified = true; return s.verifyErr }

var noopEvent EventFunc = func(_, _, _, _ string) {}

func TestCascade_AppliesNotStarted(t *testing.T) {
	p := &stubPhase{name: "demo", num: 1, state: StateNotStarted}
	if err := RunCascade(context.Background(), []Phase{p}, CascadeOptions{OnEvent: noopEvent}); err != nil {
		t.Fatal(err)
	}
	if !p.applied {
		t.Fatal("apply not called")
	}
	if !p.verified {
		t.Fatal("verify not called")
	}
}

func TestCascade_SkipsApplyOnAlreadyDone(t *testing.T) {
	p := &stubPhase{name: "demo", num: 1, state: StateAlreadyDone}
	if err := RunCascade(context.Background(), []Phase{p}, CascadeOptions{OnEvent: noopEvent}); err != nil {
		t.Fatal(err)
	}
	if p.applied {
		t.Fatal("apply should not run on already_done")
	}
	if !p.verified {
		t.Fatal("verify should still run on already_done")
	}
}

func TestCascade_BrokenStops(t *testing.T) {
	p := &stubPhase{name: "demo", num: 1, state: StateBroken, stateErr: errors.New("stuck finalizer")}
	err := RunCascade(context.Background(), []Phase{p}, CascadeOptions{OnEvent: noopEvent})
	if err == nil {
		t.Fatal("expected error")
	}
	var broken *BrokenError
	if !errors.As(err, &broken) {
		t.Fatalf("expected BrokenError, got %T: %v", err, err)
	}
}

func TestCascade_ApplyErrorStops(t *testing.T) {
	p := &stubPhase{name: "demo", num: 1, state: StateNotStarted, applyErr: errors.New("helm failed")}
	err := RunCascade(context.Background(), []Phase{p}, CascadeOptions{OnEvent: noopEvent})
	if err == nil {
		t.Fatal("expected error from apply failure")
	}
}

func TestCascade_MultiphaseAllNotStarted(t *testing.T) {
	phases := []Phase{
		&stubPhase{name: "p1", num: 1, state: StateNotStarted},
		&stubPhase{name: "p2", num: 2, state: StateNotStarted},
	}
	if err := RunCascade(context.Background(), phases, CascadeOptions{OnEvent: noopEvent}); err != nil {
		t.Fatal(err)
	}
	for _, ph := range phases {
		s := ph.(*stubPhase)
		if !s.applied || !s.verified {
			t.Fatalf("%s: applied=%v verified=%v", s.name, s.applied, s.verified)
		}
	}
}
