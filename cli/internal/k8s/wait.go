package k8s

import (
	"context"
	"time"
)

// WaitFor invokes check at the given poll interval until either:
//   - check returns (true, nil): success
//   - check returns (_, err): failure
//   - ctx is canceled / deadline exceeded: returns ctx.Err()
//
// First check fires immediately (no leading sleep).
func WaitFor(ctx context.Context, interval time.Duration, check func(ctx context.Context) (bool, error)) error {
	for {
		ok, err := check(ctx)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}
