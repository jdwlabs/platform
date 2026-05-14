package k8s

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWaitFor_Success(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	attempts := 0
	err := WaitFor(ctx, 20*time.Millisecond, func(ctx context.Context) (bool, error) {
		attempts++
		return attempts >= 3, nil
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if attempts < 3 {
		t.Fatalf("attempts %d", attempts)
	}
}

func TestWaitFor_Timeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := WaitFor(ctx, 10*time.Millisecond, func(ctx context.Context) (bool, error) {
		return false, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want DeadlineExceeded, got %v", err)
	}
}
