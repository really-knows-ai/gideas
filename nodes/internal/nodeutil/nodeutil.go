// Package nodeutil provides shared utility functions used across node
// implementations — context-aware sleep and exponential backoff.
package nodeutil

import (
	"context"
	"time"
)

// SleepCtx sleeps for the given duration, returning early if the context is
// cancelled. Returns true if the sleep completed, false if cancelled.
func SleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// NextBackoff calculates the next exponential backoff delay: doubles the
// current value and caps it at max. No jitter — suitable for simple reconnect
// loops where reproducible timing is more important than thundering-herd
// avoidance.
// ponytail: no jitter by design; callers that need jitter can add it.
func NextBackoff(current, max time.Duration) time.Duration {
	next := current * 2
	if next > max {
		return max
	}
	return next
}
