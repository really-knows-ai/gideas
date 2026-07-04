// human-arbiter is the deadlock resolution node of the Haiku Foundry Cycle.
//
// It reads the haiku and petition artefacts for context, identifies all
// DEADLOCKED feedback items, parks the workitem in the QueueManager, and
// waits for a human decision:
//
//   - accept → LinkRuling("human-arbiter", WONT_FIX) for each deadlocked item,
//     stamps "arbitrated" on the haiku, routes to "accept".
//   - reject → LinkRuling("human-arbiter", REJECTED) for each deadlocked item,
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

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
)

// Constants for routing and stamping.
const (
	artefactHaiku    = "haiku"
	artefactPetition = "petition"
	stampArbitrated  = "arbitrated"
	sourceLawID      = "human-arbiter"
	choiceAccept     = "accept"
	choiceReject     = "reject"
	choiceCancel     = "cancel"
)

// choicesResponse is the JSON body returned by GET /choices.
type choiceEntry struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Type  string `json:"type"`
}

type choicesResponse struct {
	Choices     []choiceEntry `json:"choices"`
	HasFeedback bool          `json:"hasFeedback"`
	HasCancel   bool          `json:"hasCancel"`
}

func main() {
	slog.Info("human-arbiter: starting")

	qm, err := flow.NewQueueManager(
		flow.WithQueueName("human-arbiter"),
		flow.WithCustomRoutes(func(mux *http.ServeMux) {
			mux.HandleFunc("GET /choices", handleChoices)
		}),
	)
	if err != nil {
		slog.Error("human-arbiter: create queue manager failed", "error", err)
		os.Exit(1)
	}

	if err := flow.Start(handler(qm), flow.WithQueueManager(qm)); err != nil {
		slog.Error("human-arbiter: server failed", "error", err)
		os.Exit(1)
	}
}

// handler returns a flow.Handler that delegates to handleArbiter.
func handler(qm flow.QueueManager) flow.Handler {
	return func(ctx context.Context, wctx *flowv1.WorkitemContext) error {
		slog.Info("human-arbiter: received assignment",
			"workitem_id", wctx.GetWorkitemId(),
			"node_id", wctx.GetNodeId(),
		)

		_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())

		client, err := flow.NewClient()
		if err != nil {
			return fmt.Errorf("human-arbiter: create client: %w", err)
		}
		defer func() { _ = client.Close() }()

		workitem, err := client.GetWorkitem()
		if err != nil {
			return fmt.Errorf("human-arbiter: get workitem: %w", err)
		}

		return handleArbiter(ctx, workitem, qm, wctx)
	}
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
		return fmt.Errorf("human-arbiter: heartbeat: %w", err)
	}

	// ── Step 1: Read artefacts for context ─────────────────────────────
	haikuArt, err := workitem.GetArtefact(artefactHaiku)
	if err != nil {
		return fmt.Errorf("human-arbiter: read haiku artefact: %w", err)
	}
	_, err = workitem.GetArtefact(artefactPetition)
	if err != nil {
		return fmt.Errorf("human-arbiter: read petition artefact: %w", err)
	}

	// ── Step 2: Get feedback and filter deadlocked ─────────────────────
	items, err := workitem.GetFeedback(artefactHaiku)
	if err != nil {
		return fmt.Errorf("human-arbiter: get feedback: %w", err)
	}

	var deadlocked []*flow.Feedback
	for _, fb := range items {
		if fb.GetState() == flow.FeedbackStateDeadlocked {
			deadlocked = append(deadlocked, fb)
		}
	}

	// ── Step 3: No deadlocked items — graceful degradation ─────────────
	if len(deadlocked) == 0 {
		slog.Warn("human-arbiter: no deadlocked feedback found, degrading gracefully",
			"workitem_id", workitemID)
		if err := haikuArt.Stamp(stampArbitrated); err != nil {
			return fmt.Errorf("human-arbiter: stamp haiku: %w", err)
		}
		if err := workitem.RouteTo(choiceAccept); err != nil {
			return fmt.Errorf("human-arbiter: route to %s: %w", choiceAccept, err)
		}
		return nil
	}

	// ── Step 4: Enqueue and pause ──────────────────────────────────────
	slog.Info("human-arbiter: deadlocked feedback found",
		"workitem_id", workitemID,
		"count", len(deadlocked))
	if err := qm.Enqueue(ctx, workitemID); err != nil {
		return fmt.Errorf("human-arbiter: enqueue: %w", err)
	}
	if err := workitem.PauseTimer(); err != nil {
		return fmt.Errorf("human-arbiter: pause timer: %w", err)
	}

	// ── Step 5: Wait for human decision ────────────────────────────────
	slog.Info("human-arbiter: awaiting human decision", "workitem_id", workitemID)
	choice, err := qm.WaitForDecision(ctx, workitemID)
	if err != nil {
		return fmt.Errorf("human-arbiter: wait for decision: %w", err)
	}

	// Empty choice indicates QueueManager shutdown (not a human decision).
	if choice == "" {
		return fmt.Errorf("human-arbiter: received empty choice (queue manager shut down before decision)")
	}

	// ── Step 6: Dispatch based on choice ───────────────────────────────
	switch choice {
	case choiceAccept:
		if err := workitem.ResumeTimer(); err != nil {
			return fmt.Errorf("human-arbiter: resume timer: %w", err)
		}
		return linkRulingsAndRoute(workitem, haikuArt, deadlocked, flow.FeedbackStateWontFix, choice)

	case choiceReject:
		if err := workitem.ResumeTimer(); err != nil {
			return fmt.Errorf("human-arbiter: resume timer: %w", err)
		}
		return linkRulingsAndRoute(workitem, haikuArt, deadlocked, flow.FeedbackStateRejected, choice)

	case choiceCancel:
		if err := workitem.ResumeTimer(); err != nil {
			return fmt.Errorf("human-arbiter: resume timer: %w", err)
		}
		slog.Info("human-arbiter: cancel requested", "workitem_id", workitemID)
		if err := workitem.Complete(flow.WithReason(flowv1.CompletionReason_COMPLETION_REASON_CANCELLED)); err != nil {
			return fmt.Errorf("human-arbiter: complete cancelled: %w", err)
		}
		return nil

	default:
		return fmt.Errorf("human-arbiter: invalid choice %q", choice)
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
			return fmt.Errorf("human-arbiter: link ruling for %s: %w", fb.GetID(), err)
		}
	}
	if err := haikuArt.Stamp(stampArbitrated); err != nil {
		return fmt.Errorf("human-arbiter: stamp haiku: %w", err)
	}
	if err := workitem.RouteTo(output); err != nil {
		return fmt.Errorf("human-arbiter: route to %s: %w", output, err)
	}
	return nil
}

// handleChoices serves the hardcoded GET /choices response.
func handleChoices(w http.ResponseWriter, r *http.Request) {
	resp := choicesResponse{
		Choices: []choiceEntry{
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
