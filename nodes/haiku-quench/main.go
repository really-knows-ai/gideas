// Quench is the deterministic validator node of the Haiku Foundry Cycle.
//
// It reads the "haiku" artefact, validates the 5-7-5 syllable structure using
// a Go heuristic syllable counter. Quench ALWAYS stamps "linter" on the
// artefact to record that this version has been inspected. The stamp means
// "I have seen this", not "it is valid".
//
// If validation fails, Quench raises HIGH-severity feedback describing the
// syllable mismatch. Sort will see the linter stamp plus unresolved feedback
// and route to Refine.
//
// If validation passes, Quench resolves any ACTIONED feedback from prior
// cycles (accepting the fix), and Sort will see linter stamp with no
// unresolved feedback and proceed to Appraise.
//
// Always routes back to Sort.
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

	os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("quench: create client: %w", err)
	}
	defer client.Close()

	client.Heartbeat(ctx)

	// Resolve any ACTIONED feedback from prior cycles. Refine already revised
	// the haiku to address this feedback; Quench (as the structural authority)
	// accepts the fix so the feedback is fully resolved and won't cause Sort
	// to route back to Refine.
	existingFeedback, err := client.GetFeedback(ctx, "haiku")
	if err != nil {
		return fmt.Errorf("quench: get feedback: %w", err)
	}
	for _, fb := range existingFeedback {
		if fb.GetState() == flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED {
			slog.Info("haiku-quench: accepting fix for prior feedback", "feedback_id", fb.GetId())
			if err := client.AcceptFix(ctx, fb.GetId()); err != nil {
				slog.Warn("haiku-quench: failed to accept fix", "feedback_id", fb.GetId(), "error", err)
			}
		}
	}

	// Read the haiku artefact.
	haikuResp, err := client.GetArtefact(ctx, "haiku")
	if err != nil {
		return fmt.Errorf("quench: read haiku: %w", err)
	}
	haiku := string(haikuResp.GetContent())
	slog.Info("haiku-quench: read haiku", "haiku", haiku)

	// Validate syllable structure.
	counts, valid := syllable.ValidateHaiku(haiku)
	slog.Info("haiku-quench: validation result",
		"counts", fmt.Sprintf("%d-%d-%d", counts[0], counts[1], counts[2]),
		"valid", valid,
	)

	if valid {
		slog.Info("haiku-quench: haiku PASSED validation")
	} else {
		// Build per-word syllable breakdown for actionable feedback.
		lines := strings.Split(haiku, "\n")
		var breakdown strings.Builder
		for i, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			words := strings.Fields(line)
			var parts []string
			for _, w := range words {
				clean := strings.Trim(w, ",.!?;:'\"")
				parts = append(parts, fmt.Sprintf("%s(%d)", w, syllable.Count(clean)))
			}
			breakdown.WriteString(fmt.Sprintf("  Line %d: %s = %d syllables\n", i+1, strings.Join(parts, " + "), counts[i]))
		}
		msg := fmt.Sprintf("Haiku syllable structure is %d-%d-%d, must be exactly 5-7-5.\n%sPlease revise to exactly 5-7-5 syllables.", counts[0], counts[1], counts[2], breakdown.String())
		slog.Info("haiku-quench: haiku FAILED validation, raising feedback",
			"expected", "5-7-5",
			"got", fmt.Sprintf("%d-%d-%d", counts[0], counts[1], counts[2]),
		)
		if _, err := client.AddFeedback(ctx, "haiku", flowv1.Severity_SEVERITY_HIGH, msg); err != nil {
			return fmt.Errorf("quench: add feedback: %w", err)
		}
	}

	// Always stamp "linter" — records that Quench has inspected this version.
	// The stamp means "I have seen this", the feedback carries pass/fail.
	if _, err := client.StampArtefact(ctx, "haiku", "linter"); err != nil {
		return fmt.Errorf("quench: stamp haiku: %w", err)
	}
	slog.Info("haiku-quench: linter stamp applied")

	// Always route to Sort — it decides what happens next.
	if _, err := client.RouteToOutput(ctx, "default"); err != nil {
		return fmt.Errorf("quench: route to sort: %w", err)
	}

	slog.Info("haiku-quench: routed to sort", "workitem_id", wctx.GetWorkitemId())
	return nil
}
