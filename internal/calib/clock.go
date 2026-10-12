package calib

import (
	"context"
	"time"
)

// Clock is the engine's view of time. The real clock uses the wall clock and a
// cancellable sleep; a test clock drives virtual time and advances a simulated
// battery, so the whole multi-hour state machine runs deterministically in a
// unit test.
type Clock interface {
	Now() time.Time
	// Sleep blocks for d or until ctx is cancelled, returning ctx.Err() if so.
	Sleep(ctx context.Context, d time.Duration) error
}

// RealClock is the production Clock.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

func (RealClock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
