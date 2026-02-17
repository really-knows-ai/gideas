// Sort is the central routing hub and gate of the Haiku Foundry Cycle.
//
// Every node in the cycle routes back to Sort. Sort inspects the governance
// state of the "haiku" artefact and makes routing decisions:
//
//  1. If the "linter" stamp is missing → route to "quench" (needs inspection)
//  2. If there is unresolved feedback → route to "refine" (needs revision)
//  3. If the "review" stamp is missing → route to "appraise" (needs review)
//  4. If linter + review stamps present and no unresolved feedback →
//     stamp "approval" and Complete()
//
// The "linter" stamp means "Quench has inspected this version", not
// "it is valid". Feedback from Quench carries the pass/fail signal.
// When Refine revises the haiku (new version), stamps are invalidated,
// so Quench must re-inspect.
//
// Sort is the only exit-bound node. The exit contract requires:
//
//	text/haiku: [linter, review, approval]
//
// Environment: none (pure deterministic routing logic).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
)

func main() {
	slog.Info("haiku-sort: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("haiku-sort: server failed", "error", err)
		os.Exit(1)
	}
}

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("haiku-sort: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("sort: create client: %w", err)
	}
	defer client.Close()

	client.Heartbeat(ctx)

	// Check governance state of the haiku artefact.
	hasLinter, err := client.HasStamp(ctx, "haiku", "linter")
	if err != nil {
		return fmt.Errorf("sort: check linter stamp: %w", err)
	}

	hasReview, err := client.HasStamp(ctx, "haiku", "review")
	if err != nil {
		return fmt.Errorf("sort: check review stamp: %w", err)
	}

	hasUnresolved, err := client.HasUnresolvedFeedback(ctx, "haiku")
	if err != nil {
		return fmt.Errorf("sort: check feedback: %w", err)
	}

	slog.Info("haiku-sort: governance state",
		"has_linter_stamp", hasLinter,
		"has_review_stamp", hasReview,
		"has_unresolved_feedback", hasUnresolved,
	)

	// Decision tree — ordered by governance priority.
	//
	// The linter stamp means "Quench has inspected this version". When it is
	// missing, the haiku must be sent to Quench before any other decision.
	// After Quench inspects, it always stamps linter. If validation fails,
	// Quench also raises feedback. Sort then sees linter + feedback and
	// routes to Refine. After Refine revises (new version), stamps are
	// invalidated, so we route to Quench again.
	switch {
	case !hasLinter:
		// Quench has not inspected this version → send for validation.
		slog.Info("haiku-sort: routing to quench (missing linter stamp)")
		if _, err := client.RouteToOutput(ctx, "quench"); err != nil {
			return fmt.Errorf("sort: route to quench: %w", err)
		}

	case hasUnresolved:
		// Quench inspected but raised feedback → needs revision.
		slog.Info("haiku-sort: routing to refine (unresolved feedback)")
		if _, err := client.RouteToOutput(ctx, "refine"); err != nil {
			return fmt.Errorf("sort: route to refine: %w", err)
		}

	case !hasReview:
		// Missing review stamp → subjective review required.
		slog.Info("haiku-sort: routing to appraise (missing review stamp)")
		if _, err := client.RouteToOutput(ctx, "appraise"); err != nil {
			return fmt.Errorf("sort: route to appraise: %w", err)
		}

	default:
		// All stamps present, no unresolved feedback → apply approval and complete.
		slog.Info("haiku-sort: all governance requirements met, stamping approval")
		if _, err := client.StampArtefact(ctx, "haiku", "approval"); err != nil {
			return fmt.Errorf("sort: stamp approval: %w", err)
		}

		slog.Info("haiku-sort: completing workitem")
		if _, err := client.Complete(ctx, ""); err != nil {
			return fmt.Errorf("sort: complete: %w", err)
		}
	}

	slog.Info("haiku-sort: done", "workitem_id", wctx.GetWorkitemId())
	return nil
}
