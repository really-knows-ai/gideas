// Human-approval is the final human sign-off gate in the Haiku Foundry Cycle.
//
// It reads the "haiku" and "petition" artefacts, parks the Workitem in a HITL
// queue, and waits for a human decision. On "approve", it stamps "approval"
// on the haiku artefact and routes to Sort via the "approve" output. On
// "cancel", it completes the Workitem with COMPLETION_REASON_CANCELLED.
//
// The node follows the foundry-approval pattern: single main.go, no config
// file, no LLM. Human choices are hardcoded for the haiku demo use case.
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

const (
	choiceApprove = "approve"
	choiceCancel  = "cancel"
)

var validChoices = map[string]bool{
	choiceApprove: true,
	choiceCancel:  true,
}

func main() {
	slog.Info("human-approval: starting")

	qm, err := flow.NewQueueManager(
		flow.WithQueueName("human-approval"),
		flow.WithCustomRoutes(func(mux *http.ServeMux) {
			mux.HandleFunc("GET /choices", handleChoices)
		}),
	)
	if err != nil {
		slog.Error("human-approval: create queue manager failed", "error", err)
		os.Exit(1)
	}

	if err := flow.Start(handler(qm), flow.WithQueueManager(qm)); err != nil {
		slog.Error("human-approval: server failed", "error", err)
		os.Exit(1)
	}
}

// handler returns a flow.Handler that parks the Workitem in the HITL queue
// and waits for a human decision (approve or cancel).
func handler(qm flow.QueueManager) flow.Handler {
	return func(ctx context.Context, wctx *flowv1.WorkitemContext) error {
		_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())

		client, err := flow.NewClient()
		if err != nil {
			return fmt.Errorf("human-approval: create client: %w", err)
		}
		defer func() { _ = client.Close() }()

		workitem, err := client.GetWorkitem()
		if err != nil {
			return fmt.Errorf("human-approval: get workitem: %w", err)
		}

		return handleApproval(ctx, workitem, qm, wctx)
	}
}

// handleApproval is the core handler logic, extracted for testability.
func handleApproval(
	ctx context.Context,
	workitem *flow.Workitem,
	qm flow.QueueManager,
	wctx *flowv1.WorkitemContext,
) error {
	workitemID := wctx.GetWorkitemId()

	if err := workitem.Heartbeat(); err != nil {
		return fmt.Errorf("human-approval: heartbeat: %w", err)
	}

	slog.Info("human-approval: handling workitem",
		"workitem_id", workitemID,
	)

	// Step 1: Read artefacts for context display.
	if _, err := workitem.GetArtefact("haiku"); err != nil {
		return fmt.Errorf("human-approval: read haiku: %w", err)
	}
	if _, err := workitem.GetArtefact("petition"); err != nil {
		return fmt.Errorf("human-approval: read petition: %w", err)
	}

	// Step 2: Enqueue and pause timer.
	if err := qm.Enqueue(ctx, workitemID); err != nil {
		return fmt.Errorf("human-approval: enqueue: %w", err)
	}
	if err := workitem.PauseTimer(); err != nil {
		return fmt.Errorf("human-approval: pause timer: %w", err)
	}

	// Step 3: Wait for human decision.
	slog.Info("human-approval: awaiting human decision", "workitem_id", workitemID)
	choice, err := qm.WaitForDecision(ctx, workitemID)
	if err != nil {
		return fmt.Errorf("human-approval: wait for decision: %w", err)
	}

	// Step 4: Validate choice (before resuming timer — invalid choices
	// leave the timer paused so the operator can retry).
	if choice == "" {
		return fmt.Errorf("human-approval: empty choice (queue manager shut down before decision)")
	}
	if !validChoices[choice] {
		return fmt.Errorf("human-approval: invalid choice %q", choice)
	}

	slog.Info("human-approval: human decision received",
		"workitem_id", workitemID, "choice", choice,
	)

	// Step 5: Resume timer.
	if err := workitem.ResumeTimer(); err != nil {
		return fmt.Errorf("human-approval: resume timer: %w", err)
	}

	// Step 6: Dispatch based on choice.
	switch choice {
	case choiceCancel:
		return completeWithCancelled(workitem)
	case choiceApprove:
		return stampAndRoute(workitem)
	default:
		// Unreachable after validation, but guard against logic drift.
		return fmt.Errorf("human-approval: unreachable choice %q", choice)
	}
}

// completeWithCancelled calls Complete with COMPLETION_REASON_CANCELLED.
func completeWithCancelled(workitem *flow.Workitem) error {
	if err := workitem.Complete(flow.WithReason(flowv1.CompletionReason_COMPLETION_REASON_CANCELLED)); err != nil {
		return fmt.Errorf("human-approval: complete (cancelled): %w", err)
	}
	return nil
}

// stampAndRoute stamps "approval" on the haiku artefact and routes to "approve".
func stampAndRoute(workitem *flow.Workitem) error {
	art, err := workitem.GetArtefact("haiku")
	if err != nil {
		return fmt.Errorf("human-approval: get haiku artefact: %w", err)
	}
	if err := art.Stamp("approval"); err != nil {
		return fmt.Errorf("human-approval: stamp approval: %w", err)
	}
	slog.Info("human-approval: approval stamp applied")

	if err := workitem.RouteTo("approve"); err != nil {
		return fmt.Errorf("human-approval: route to approve: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// GET /choices
// ---------------------------------------------------------------------------

// choiceEntry is a single entry in the GET /choices response.
type choiceEntry struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Type  string `json:"type"`
}

// choicesResponse is the JSON body returned by GET /choices.
type choicesResponse struct {
	Choices     []choiceEntry `json:"choices"`
	HasFeedback bool          `json:"hasFeedback"`
	HasCancel   bool          `json:"hasCancel"`
}

// handleChoices serves the hardcoded choice list for the human-approval node.
func handleChoices(w http.ResponseWriter, r *http.Request) {
	resp := choicesResponse{
		Choices: []choiceEntry{
			{Value: "approve", Label: "Approve", Type: "route"},
			{Value: "cancel", Label: "Reject Petition", Type: "cancel"},
		},
		HasFeedback: false,
		HasCancel:   true,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
