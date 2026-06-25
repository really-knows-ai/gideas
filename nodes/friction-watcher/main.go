// Friction Watcher is an entry-bound watcher node for the Foundry Flow
// judiciary subsystem.
//
// It subscribes to the Event Bus "friction" channel for
// "friction.threshold_crossed" events and creates hearing workitems. The
// handler stores the target law ID as a law-reference artefact and routes
// onward.
//
// Architecture:
//   - Entry function: subscribes to Event Bus, creates workitems on events.
//   - Handler: stores law-reference artefact, routes to "default" output.
//   - Dedup: per-replica in-memory tracking of pending law IDs (best-effort).
//
// Uses the SDK StartEntry pattern: the entry function and handler server run
// concurrently, with shared-nothing semantics between them.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal"
	flow "github.com/gideas/flow/sdk/go"
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

const (
	// channel is the Event Bus channel to subscribe to.
	channel = "friction"

	// eventType is the Event Bus event type filter.
	eventType = "friction.threshold_crossed"

	// reconnectBaseDelay is the initial backoff delay for reconnecting to the
	// Event Bus after a stream error.
	reconnectBaseDelay = 1 * time.Second

	// reconnectMaxDelay caps the exponential backoff.
	reconnectMaxDelay = 30 * time.Second
)

func main() {
	slog.Info("friction-watcher: starting")
	if err := flow.StartEntry(watchFriction, handleHearing); err != nil {
		slog.Error("friction-watcher: failed", "error", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Entry function — subscribes to Event Bus, creates workitems
// ---------------------------------------------------------------------------

// watchFriction is the entry function. It reconnects to the Event Bus with
// exponential backoff and creates hearing workitems for threshold events.
func watchFriction(ctx context.Context, entry *flow.EntryClient) error {
	tracker := internal.NewPendingTracker()
	delay := reconnectBaseDelay

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		events, err := entry.Subscribe(ctx, channel, eventType)
		if err != nil {
			slog.Warn("friction-watcher: subscribe failed, retrying",
				"error", err, "delay", delay)
			if !sleepCtx(ctx, delay) {
				return ctx.Err()
			}
			delay = nextBackoff(delay)
			continue
		}

		// Reset backoff on successful subscribe.
		delay = reconnectBaseDelay
		slog.Info("friction-watcher: subscribed to Event Bus",
			"channel", channel, "event_type", eventType)

		// Consume events from the stream.
		if err := consumeEvents(ctx, entry, events, tracker); err != nil {
			slog.Warn("friction-watcher: stream ended, reconnecting",
				"error", err)
			continue
		}
	}
}

// consumeEvents reads events from the stream and creates workitems.
// Returns nil on EOF, or an error on stream failure.
func consumeEvents(
	ctx context.Context,
	entry *flow.EntryClient,
	events *flow.EventStream,
	tracker *internal.PendingTracker,
) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		evt, err := events.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("recv: %w", err)
		}

		lawID := extractLawID(evt)
		if lawID == "" {
			slog.Warn("friction-watcher: event missing law_id, skipping",
				"event_id", evt.GetEventId())
			continue
		}

		// Best-effort dedup: skip if already pending on this replica.
		if !tracker.MarkPending(lawID) {
			slog.Debug("friction-watcher: law_id already pending, skipping",
				"law_id", lawID, "event_id", evt.GetEventId())
			continue
		}

		slog.Info("friction-watcher: creating hearing workitem",
			"law_id", lawID, "event_id", evt.GetEventId())

		if _, err := entry.CreateWorkitem(ctx, map[string]string{
			"law_id": lawID,
		}); err != nil {
			tracker.ClearPending(lawID)
			slog.Warn("friction-watcher: create workitem failed",
				"law_id", lawID, "error", err)
		}
	}
}

// extractLawID finds the law_id from an event's labels.
// The Friction Ledger publishes threshold_crossed events with a
// label key "law_id" identifying the target law.
func extractLawID(evt *flowv1.FlowEvent) string {
	for _, label := range evt.GetLabels() {
		if label.GetKey() == "law_id" {
			return label.GetValue()
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Handler — processes assigned hearing workitems
// ---------------------------------------------------------------------------

// handleHearing is the SDK handler entry point for hearing workitems.
// It creates an SDK client and delegates to processHearing.
func handleHearing(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	// Initialize SDK client for Sidecar operations.
	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("friction-watcher: handler: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	return processHearing(ctx, client, wctx)
}

// processHearing performs the core handler logic: validate metadata, heartbeat,
// store law-reference artefact, and route to default output.
func processHearing(ctx context.Context, client *flow.Client, wctx *flowv1.WorkitemContext) error {
	lawID := wctx.GetMetadata()["law_id"]
	if lawID == "" {
		return fmt.Errorf("friction-watcher: handler: missing law_id in metadata")
	}

	slog.Info("friction-watcher: handling hearing",
		"workitem_id", wctx.GetWorkitemId(),
		"law_id", lawID,
	)

	// Send a heartbeat to signal liveness.
	if _, err := client.Heartbeat(ctx); err != nil {
		return fmt.Errorf("friction-watcher: handler: heartbeat: %w", err)
	}

	// Store law-reference artefact.
	if _, err := client.StoreArtefact(ctx, "law-reference", "law-reference", []byte(lawID)); err != nil {
		return fmt.Errorf("friction-watcher: handler: store law-reference: %w", err)
	}

	slog.Info("friction-watcher: stored law-reference artefact", "law_id", lawID)

	// Route to default output (-> tribunal).
	if _, err := client.RouteToOutput(ctx, "default"); err != nil {
		return fmt.Errorf("friction-watcher: handler: route: %w", err)
	}

	slog.Info("friction-watcher: routed to default output",
		"workitem_id", wctx.GetWorkitemId())

	return nil
}

// ---------------------------------------------------------------------------
// Utility
// ---------------------------------------------------------------------------

// sleepCtx sleeps for the given duration, respecting context cancellation.
// Returns true if the sleep completed, false if cancelled.
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

// nextBackoff doubles the delay up to reconnectMaxDelay.
func nextBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > reconnectMaxDelay {
		return reconnectMaxDelay
	}
	return next
}
