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

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/nodes/internal/nodeutil"
	flow "github.com/foundry/flow/sdk/go"
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

	if err := nodeutil.RunHITLNode("human-approval", handler,
		flow.WithQueueName("human-approval"),
		flow.WithCustomRoutes(func(mux *http.ServeMux) {
			mux.HandleFunc("GET /choices", handleChoices)
		}),
	); err != nil {
		slog.Error("human-approval: server failed", "error", err)
		os.Exit(1)
	}
}

// handler returns a flow.Handler that parks the Workitem in the HITL queue
// and waits for a human decision (approve or cancel).
func handler(qm flow.QueueManager) flow.Handler {
	return nodeutil.NewHITLHandler(qm, "human-approval", func(
		ctx context.Context,
		_ *flow.Client,
		workitem *flow.Workitem,
		qm flow.QueueManager,
		wctx *flowv1.WorkitemContext,
	) error {
		return handleApproval(ctx, workitem, qm, wctx)
	})
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

	// Step 2-5: Enqueue, pause, wait, validate, resume.
	choice, err := nodeutil.AwaitHumanDecision(ctx, qm, workitem, workitemID, "human-approval", validChoices)
	if err != nil {
		return err
	}

	// Step 6: Dispatch based on choice.
	switch choice {
	case choiceCancel:
		return nodeutil.CompleteCancelled(workitem, "human-approval")
	case choiceApprove:
		return nodeutil.StampAndRoute(
			workitem,
			[]flow.StampCapability{{GovernedArtefact: "haiku", StampName: "approval"}},
			"human-approval",
			"approve",
		)
	default:
		// Unreachable after validation, but guard against logic drift.
		return fmt.Errorf("human-approval: unreachable choice %q", choice)
	}
}

// ---------------------------------------------------------------------------
// GET /choices
// ---------------------------------------------------------------------------

// handleChoices serves the hardcoded choice list for the human-approval node.
func handleChoices(w http.ResponseWriter, r *http.Request) {
	resp := nodeutil.ChoicesResponse{
		Choices: []nodeutil.ChoiceEntry{
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
