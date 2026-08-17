package nodeutil

import (
	"context"
	"log/slog"
	"time"
)

// Reconnect backoff bounds shared by the watcher reconnect loops.
const (
	// ReconnectBaseDelay is the initial backoff delay for reconnecting to a
	// stream after a subscribe error.
	ReconnectBaseDelay = 1 * time.Second

	// ReconnectMaxDelay caps the exponential backoff.
	ReconnectMaxDelay = 30 * time.Second
)

// ReconnectStream runs a subscribe/reconnect loop used by the watcher node
// entry functions. subscribe attempts to open a stream (subscribing via a
// concrete client); on success the stream is consumed via consume. Between
// subscribe failures it backs off exponentially, capped at ReconnectMaxDelay;
// the delay resets after a successful subscribe. The loop continues until ctx
// is cancelled.
//
// name is the log prefix. Each subscribe failure is logged as a warn; a
// consume error is logged as a debug and the loop reconnects. The closures
// capture their concrete stream and client types, so subscribe/consume stay
// type-checked at each call site.
func ReconnectStream(
	ctx context.Context,
	name string,
	subscribe func() error,
	consume func() error,
	onSubscribe func(),
) error {
	delay := ReconnectBaseDelay

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		if err := subscribe(); err != nil {
			slog.Warn(name+": subscribe failed, retrying",
				"error", err, "delay", delay)
			if !SleepCtx(ctx, delay) {
				return ctx.Err()
			}
			delay = NextBackoff(delay, ReconnectMaxDelay)
			continue
		}

		// Reset backoff on successful subscribe.
		delay = ReconnectBaseDelay
		if onSubscribe != nil {
			onSubscribe()
		}

		// Consume events from the stream; on error log and reconnect.
		if err := consume(); err != nil {
			slog.Debug(name+": stream ended, reconnecting", "error", err)
		}
	}
}
