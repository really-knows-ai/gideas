// hitl-arbiter is the deadlock resolution node of the Haiku Foundry Cycle.
//
// It reads the haiku and petition artefacts for context, identifies all
// DEADLOCKED feedback items, parks the workitem in the QueueManager, and
// waits for a human decision:
//
//   - accept → LinkRuling("hitl-arbiter", WONT_FIX) for each deadlocked item,
//     stamps "arbitrated" on the haiku, routes to "accept".
//   - reject → LinkRuling("hitl-arbiter", REJECTED) for each deadlocked item,
//     stamps "arbitrated" on the haiku, routes to "reject".
//   - cancel → Complete(COMPLETION_REASON_CANCELLED).
//
// If no DEADLOCKED feedback is found (edge case), the handler degrades
// gracefully: stamps "arbitrated" and routes to "accept".
//
// The node serves GET /choices with hardcoded accept/reject/cancel choices
// for the Dashboard.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/nodes/internal/nodeutil"
	flow "github.com/foundry/flow/sdk/go"
)

// Constants for routing and stamping.
const (
	artefactHaiku    = "haiku"
	artefactPetition = "petition"
	stampArbitrated  = "arbitrated"
	sourceLawID      = "hitl-arbiter"
	choiceAccept     = "accept"
	choiceReject     = "reject"
	choiceCancel     = "cancel"
)

func main() {
	slog.Info("hitl-arbiter: starting")

	if err := nodeutil.RunHITLNode("hitl-arbiter", handler,
		flow.WithQueueName("hitl-arbiter"),
		flow.WithCustomRoutes(func(mux *http.ServeMux) {
			mux.HandleFunc("GET /choices", handleChoices)
		}),
	); err != nil {
		slog.Error("hitl-arbiter: server failed", "error", err)
		os.Exit(1)
	}
}

// handler returns a flow.Handler that delegates to handleArbiter.
func handler(qm flow.QueueManager) flow.Handler {
	return nodeutil.NewHITLHandler(qm, "hitl-arbiter", func(
		ctx context.Context,
		_ *flow.Client,
		workitem *flow.Workitem,
		qm flow.QueueManager,
		wctx *flowv1.WorkitemContext,
	) error {
		return handleArbiter(ctx, workitem, qm, wctx)
	})
}

// handleArbiter contains the core arbiter logic, extracted for testability.
func handleArbiter(
	ctx context.Context,
	workitem *flow.Workitem,
	qm flow.QueueManager,
	wctx *flowv1.WorkitemContext,
) error {
	workitemID := wctx.GetWorkitemId()

	if err := workitem.Heartbeat(); err != nil {
		return fmt.Errorf("hitl-arbiter: heartbeat: %w", err)
	}

	// ── Step 1: Read artefacts for context ─────────────────────────────
	haikuArt, err := workitem.GetArtefact(artefactHaiku)
	if err != nil {
		return fmt.Errorf("hitl-arbiter: read haiku artefact: %w", err)
	}
	_, err = workitem.GetArtefact(artefactPetition)
	if err != nil {
		return fmt.Errorf("hitl-arbiter: read petition artefact: %w", err)
	}

	// ── Step 2: Get feedback and filter deadlocked ─────────────────────
	items, err := workitem.GetFeedback(artefactHaiku)
	if err != nil {
		return fmt.Errorf("hitl-arbiter: get feedback: %w", err)
	}

	var deadlocked []*flow.Feedback
	for _, fb := range items {
		if fb.GetState() == flow.FeedbackStateDeadlocked {
			deadlocked = append(deadlocked, fb)
		}
	}

	// ── Step 3: No deadlocked items — graceful degradation ─────────────
	if len(deadlocked) == 0 {
		slog.Warn("hitl-arbiter: no deadlocked feedback found, degrading gracefully",
			"workitem_id", workitemID)
		if err := haikuArt.Stamp(stampArbitrated); err != nil {
			return fmt.Errorf("hitl-arbiter: stamp haiku: %w", err)
		}
		if err := workitem.RouteTo(choiceAccept); err != nil {
			return fmt.Errorf("hitl-arbiter: route to %s: %w", choiceAccept, err)
		}
		return nil
	}

	// ── Step 4: Enqueue, pause, wait, validate, resume ────────────────
	slog.Info("hitl-arbiter: deadlocked feedback found",
		"workitem_id", workitemID,
		"count", len(deadlocked))
	choice, err := nodeutil.AwaitHumanDecision(ctx, qm, workitem, workitemID, "hitl-arbiter", map[string]bool{
		choiceAccept: true,
		choiceReject: true,
		choiceCancel: true,
	})
	if err != nil {
		return err
	}

	// ── Step 5: Dispatch based on choice ──────────────────────────────
	switch choice {
	case choiceAccept:
		return linkRulingsAndRoute(workitem, haikuArt, deadlocked, flow.FeedbackStateWontFix, choice)

	case choiceReject:
		return linkRulingsAndRoute(workitem, haikuArt, deadlocked, flow.FeedbackStateRejected, choice)

	case choiceCancel:
		slog.Info("hitl-arbiter: cancel requested", "workitem_id", workitemID)
		if err := workitem.Complete(flow.WithReason(flowv1.CompletionReason_COMPLETION_REASON_CANCELLED)); err != nil {
			return fmt.Errorf("hitl-arbiter: complete cancelled: %w", err)
		}
		return nil
	default:
		// Unreachable after validation, but guard against logic drift.
		return fmt.Errorf("hitl-arbiter: unreachable choice %q", choice)
	}
}

// linkRulingsAndRoute applies LinkRuling for each deadlocked item, stamps
// the haiku, and routes to the chosen output.
func linkRulingsAndRoute(
	workitem *flow.Workitem,
	haikuArt *flow.Artefact,
	deadlocked []*flow.Feedback,
	targetState flow.FeedbackState,
	output string,
) error {
	for _, fb := range deadlocked {
		if err := fb.LinkRuling(sourceLawID, targetState); err != nil {
			return fmt.Errorf("hitl-arbiter: link ruling for %s: %w", fb.GetID(), err)
		}
	}
	if err := haikuArt.Stamp(stampArbitrated); err != nil {
		return fmt.Errorf("hitl-arbiter: stamp haiku: %w", err)
	}
	if err := workitem.RouteTo(output); err != nil {
		return fmt.Errorf("hitl-arbiter: route to %s: %w", output, err)
	}
	return nil
}

// handleChoices serves the hardcoded GET /choices response.
func handleChoices(w http.ResponseWriter, r *http.Request) {
	resp := nodeutil.ChoicesResponse{
		Choices: []nodeutil.ChoiceEntry{
			{Value: choiceAccept, Label: "Accept — Mark as WONT_FIX", Type: "route"},
			{Value: choiceReject, Label: "Demand Fix — Mark as REJECTED", Type: "route"},
			{Value: choiceCancel, Label: "Reject Workitem", Type: "cancel"},
		},
		HasFeedback: true,
		HasCancel:   true,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
