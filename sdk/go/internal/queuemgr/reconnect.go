package queuemgr

import (
	"context"
	"time"
)

// BackoffFn returns the delay to wait before the retry at the given 1-based
// attempt number (1 = first retry after a failure).
type BackoffFn func(attempt int) time.Duration

// SleepFn sleeps for d, returning false early if ctx is cancelled.
type SleepFn func(ctx context.Context, d time.Duration) bool

// ReconnectStream runs the subscribe→consume loop with exponential reconnect
// backoff used by WaitForDecision. subscribe attempts to open the EventBus
// stream; on success it is consumed via consume. A subscribe failure backs off
// with backoff(retry) and retries (retry counts consecutive failures, reset to
// 0 on a successful subscribe); a consume error (stream drop) backs off with
// backoff(1) and re-subscribes. It returns nil after a single successful
// subscribe+consume, and aborts with ctx.Err() whenever sleep reports
// cancellation or the ctx is already done at the top of the loop.
//
// This is the SDK-local reimplementation of
// nodes/internal/nodeutil.ReconnectStream semantics WITHOUT that import: the
// SDK module cannot depend on nodes (module cycle, and nodes/internal is
// module-private). The injectable backoff/sleep keep tests millisecond-fast
// with no real sleeps.
func ReconnectStream[SUB any](
	ctx context.Context,
	subscribe func() (SUB, error),
	consume func(SUB) error,
	backoff BackoffFn,
	sleep SleepFn,
) error {
	retry := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		sub, err := subscribe()
		if err != nil {
			retry++
			if !sleep(ctx, backoff(retry)) {
				return ctx.Err()
			}
			continue
		}

		// Backoff resets to 0 on a successful subscribe.
		retry = 0

		if err := consume(sub); err != nil {
			// Stream dropped mid-wait: back off (retry=1) and re-subscribe.
			if !sleep(ctx, backoff(1)) {
				return ctx.Err()
			}
			continue
		}

		// One successful subscribe+consume: done.
		return nil
	}
}

// sleepCtx sleeps for d, returning false early if ctx is cancelled — the
// SleepFn analogue of nodeutil.SleepCtx.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// waitDecisionBackoff is the default reconnect backoff for WaitForDecision:
// 100ms base, doubling per attempt, capped at 30s. The d <= 0 guard catches
// the int64 overflow that would otherwise wrap negative once 1<<attempt
// exceeds max duration, so a long outage still saturates at the cap.
func waitDecisionBackoff(attempt int) time.Duration {
	d := time.Duration(1<<uint(attempt)) * 100 * time.Millisecond
	if d > 30*time.Second || d <= 0 {
		d = 30 * time.Second
	}
	return d
}
