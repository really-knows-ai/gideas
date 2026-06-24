// Quench is the deterministic validator node of the Haiku Foundry Cycle.
//
// It reads the "haiku" artefact, validates the 5-7-5 syllable structure using
// a Go heuristic syllable counter. Quench ALWAYS stamps "linter" on the
// artefact to record that this version has been inspected. The stamp means
// "I have seen this", not "it is valid".
//
// Validation happens BEFORE feedback reconciliation:
//
//  1. Read the haiku and validate syllables.
//  2. Get existing feedback for the artefact.
//  3. If VALID — accept fixes on any ACTIONED feedback (the fix worked).
//  4. If INVALID — reject fixes on any ACTIONED feedback (the fix didn't
//     resolve the structural issue) and raise new HIGH-severity feedback.
//  5. Always stamp "linter" and route to Sort.
//
// This ordering preserves the feedback history chain: a failed fix is
// rejected (ACTIONED → REJECTED) rather than resolved and re-raised,
// so Refine sees the full history on the next attempt.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/syllable"
	flow "github.com/gideas/flow/sdk/go"
)

func main() {
	slog.Info("haiku-quench: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("haiku-quench: server failed", "error", err)
		os.Exit(1)
	}
}

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("haiku-quench: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("quench: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	return handleQuench(ctx, client)
}

// handleQuench contains the core quench logic. It is separated from
// handler so that tests can inject a spy-backed client directly.
func handleQuench(ctx context.Context, client *flow.Client) error {
	_, _ = client.Heartbeat(ctx)

	// 1. Read the haiku artefact.
	haikuResp, err := client.GetArtefact(ctx, "haiku")
	if err != nil {
		return fmt.Errorf("quench: read haiku: %w", err)
	}
	haiku := string(haikuResp.GetContent())
	slog.Info("haiku-quench: read haiku", "haiku", haiku)

	// 2. Validate syllable structure.
	counts, valid := syllable.ValidateHaiku(haiku)
	slog.Info("haiku-quench: validation result",
		"counts", fmt.Sprintf("%d-%d-%d", counts[0], counts[1], counts[2]),
		"valid", valid,
	)

	// 3. Get existing feedback.
	existingFeedback, err := client.GetFeedback(ctx, "haiku")
	if err != nil {
		return fmt.Errorf("quench: get feedback: %w", err)
	}

	// 4. Reconcile feedback based on validation result.
	if valid {
		acceptActionedFeedback(ctx, client, existingFeedback)
	} else {
		rejectActionedFeedback(ctx, client, existingFeedback, counts)

		fbMsg := buildFeedbackMessage(haiku, counts)
		slog.Info("haiku-quench: haiku FAILED validation, raising feedback",
			"expected", "5-7-5",
			"got", fmt.Sprintf("%d-%d-%d", counts[0], counts[1], counts[2]),
		)
		if _, err := client.AddFeedback(
			ctx, "haiku", false, fbMsg,
		); err != nil {
			return fmt.Errorf("quench: add feedback: %w", err)
		}
	}

	// 5. Always stamp "linter" — records that Quench has inspected this version.
	if _, err := client.StampArtefact(ctx, "haiku", "linter"); err != nil {
		return fmt.Errorf("quench: stamp haiku: %w", err)
	}
	slog.Info("haiku-quench: linter stamp applied")

	// 6. Always route to Sort — it decides what happens next.
	if _, err := client.RouteToOutput(ctx, "default"); err != nil {
		return fmt.Errorf("quench: route to sort: %w", err)
	}
	slog.Info("haiku-quench: done")
	return nil
}

// acceptActionedFeedback accepts fixes on all ACTIONED feedback items.
// Called when the haiku passes validation — the fix worked.
func acceptActionedFeedback(
	ctx context.Context,
	client *flow.Client,
	feedback []*flowv1.FeedbackItem,
) {
	slog.Info("haiku-quench: haiku PASSED validation")
	for _, fb := range feedback {
		if fb.GetState() == flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED {
			slog.Info("haiku-quench: accepting fix",
				"feedback_id", fb.GetId())
			if err := client.AcceptFix(ctx, fb.GetId()); err != nil {
				slog.Warn("haiku-quench: failed to accept fix",
					"feedback_id", fb.GetId(), "error", err)
			}
		}
	}
}

// rejectActionedFeedback rejects fixes on all ACTIONED feedback items.
// Called when the haiku fails validation — the fix didn't work.
func rejectActionedFeedback(
	ctx context.Context,
	client *flow.Client,
	feedback []*flowv1.FeedbackItem,
	counts [3]int,
) {
	rejMsg := buildRejectionMessage(counts)
	for _, fb := range feedback {
		if fb.GetState() == flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED {
			slog.Info("haiku-quench: rejecting fix",
				"feedback_id", fb.GetId())
			if err := client.RejectFix(ctx, fb.GetId(), rejMsg); err != nil {
				slog.Warn("haiku-quench: failed to reject fix",
					"feedback_id", fb.GetId(), "error", err)
			}
		}
	}
}

// buildFeedbackMessage constructs the feedback message with a per-word
// syllable breakdown for actionable guidance.
func buildFeedbackMessage(haiku string, counts [3]int) string {
	lines := strings.Split(haiku, "\n")
	var breakdown strings.Builder
	lineIdx := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		words := strings.Fields(line)
		parts := make([]string, 0, len(words))
		for _, w := range words {
			clean := strings.Trim(w, ",.!?;:'\"")
			parts = append(parts, fmt.Sprintf(
				"%s(%d)", w, syllable.Count(clean)))
		}
		breakdown.WriteString(fmt.Sprintf(
			"  Line %d: %s = %d syllables\n",
			lineIdx+1, strings.Join(parts, " + "), counts[lineIdx],
		))
		lineIdx++
	}
	return fmt.Sprintf(
		"Haiku syllable structure is %d-%d-%d, "+
			"must be exactly 5-7-5.\n"+
			"%sPlease revise to exactly 5-7-5 syllables.",
		counts[0], counts[1], counts[2], breakdown.String(),
	)
}

// buildRejectionMessage constructs the message for rejecting a fix
// that did not resolve the syllable issue.
func buildRejectionMessage(counts [3]int) string {
	return fmt.Sprintf(
		"Fix did not resolve syllable issue: "+
			"structure is still %d-%d-%d, must be 5-7-5.",
		counts[0], counts[1], counts[2],
	)
}
