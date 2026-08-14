package nodeutil

import (
	"context"
	"fmt"
	"log/slog"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flow "github.com/foundry/flow/sdk/go"
)

// AwaitHumanDecision parks the workitem in the HITL queue, pauses the sidecar
// inactivity timer, waits for a human decision, validates it, and resumes the
// timer. It returns the validated choice.
//
// name is the log/error prefix (e.g. "hitl", "human-approval"). When valid is
// nil the returned choice is accepted without validation — including an empty
// choice (hitl-appraise intentionally accepts any decision). Otherwise an
// empty choice (queue-manager shutdown) is an error and a choice outside the
// valid set is rejected, both before the timer is resumed.
func AwaitHumanDecision(
	ctx context.Context,
	qm flow.QueueManager,
	workitem *flow.Workitem,
	workitemID string,
	name string,
	valid map[string]bool,
) (string, error) {
	if err := qm.Enqueue(ctx, workitemID); err != nil {
		return "", fmt.Errorf("%s: enqueue: %w", name, err)
	}
	if err := workitem.PauseTimer(); err != nil {
		return "", fmt.Errorf("%s: pause timer: %w", name, err)
	}

	slog.Info(name+": awaiting human decision", "workitem_id", workitemID)
	choice, err := qm.WaitForDecision(ctx, workitemID)
	if err != nil {
		return "", fmt.Errorf("%s: wait for decision: %w", name, err)
	}

	if valid != nil {
		// Empty choice indicates QueueManager shutdown (not a human decision).
		if choice == "" {
			return "", fmt.Errorf("%s: received empty choice (queue manager shut down before decision)", name)
		}
		if !valid[choice] {
			return "", fmt.Errorf("%s: invalid choice %q: not in valid set", name, choice)
		}
	}

	slog.Info(name+": human decision received", "workitem_id", workitemID, "choice", choice)

	if err := workitem.ResumeTimer(); err != nil {
		return "", fmt.Errorf("%s: resume timer: %w", name, err)
	}
	return choice, nil
}

// StampAndRoute applies each stamp to its governed artefact, then routes the
// workitem to routeTo. An empty routeTo skips routing. Used by the HITL node
// binaries after a human decision.
func StampAndRoute(
	workitem *flow.Workitem,
	stamps []flow.StampCapability,
	name string,
	routeTo string,
) error {
	for _, sc := range stamps {
		art, err := workitem.GetArtefact(sc.GovernedArtefact)
		if err != nil {
			return fmt.Errorf("%s: get artefact %s: %w", name, sc.GovernedArtefact, err)
		}
		if err := art.Stamp(sc.StampName); err != nil {
			return fmt.Errorf("%s: stamp %s/%s: %w", name, sc.GovernedArtefact, sc.StampName, err)
		}
	}
	if routeTo != "" {
		if err := workitem.RouteTo(routeTo); err != nil {
			return fmt.Errorf("%s: route to output %q: %w", name, routeTo, err)
		}
	}
	return nil
}

// CompleteCancelled completes the workitem with COMPLETION_REASON_CANCELLED.
func CompleteCancelled(workitem *flow.Workitem, name string) error {
	if err := workitem.Complete(flow.WithReason(flowv1.CompletionReason_COMPLETION_REASON_CANCELLED)); err != nil {
		return fmt.Errorf("%s: complete (cancelled): %w", name, err)
	}
	return nil
}
