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

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/nodes/internal"
	"github.com/foundry/flow/nodes/internal/nodeutil"
	flow "github.com/foundry/flow/sdk/go"
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

const (
	// channel is the Event Bus channel to subscribe to.
	channel = "friction"

	// eventType is the Event Bus event type filter.
	eventType = "friction.threshold_crossed"
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
	var events *flow.EventStream

	return nodeutil.ReconnectStream(ctx, "friction-watcher",
		func() error {
			var err error
			events, err = entry.Subscribe(channel, eventType)
			return err
		},
		func() error { return consumeEvents(ctx, entry, events, tracker) },
		func() {
			slog.Info("friction-watcher: subscribed to Event Bus",
				"channel", channel, "event_type", eventType)
		},
	)
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

		if _, err := entry.CreateWorkitem(map[string]string{
			nodeutil.LawIDKey: lawID,
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
func handleHearing(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	return nodeutil.HandleHearing(ctx, wctx, "friction-watcher")
}

// processHearing performs the core handler logic: validate metadata, heartbeat,
// store law-reference artefact, and route to default output.
func processHearing(workitem *flow.Workitem, wctx *flowv1.WorkitemContext) error {
	return nodeutil.ProcessHearing(workitem, wctx, "friction-watcher")
}

// ---------------------------------------------------------------------------
// Utility
// ---------------------------------------------------------------------------
