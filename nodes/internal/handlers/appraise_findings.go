package handlers

import (
	"context"
	"fmt"
	"log/slog"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flow "github.com/foundry/flow/sdk/go"
)

// mintFindings runs the FindingContract agent to distill governance learnings
// from resolved feedback items that carried novel arguments. Each finding is
// recorded as a Tier 1 Finding via the Librarian.
func mintFindings(
	ctx context.Context,
	finding flow.FindingContract,
	client *flow.Client,
	novelItems []*flow.Feedback,
) error {
	slog.Info("appraisal: capturing learnings from resolved "+
		"novel arguments", "count", len(novelItems))

	// Convert domain feedback items to proto for the FindingContract.
	protoItems := make([]*flowv1.FeedbackItem, len(novelItems))
	for i, fb := range novelItems {
		protoItems[i] = fb.PB()
	}

	out, err := finding.Run(ctx, protoItems)
	if err != nil {
		return fmt.Errorf("finding inference: %w", err)
	}
	if out == nil {
		return nil
	}

	slog.Info("appraisal: LLM produced findings",
		"count", len(out.Findings))

	for i, f := range out.Findings {
		lawID, err := client.RecordFinding(
			f.Goal, f.AppliesTo,
			[]*flowv1.Representation{
				{Type: "text/markdown", Content: f.Rationale},
			},
		)
		if err != nil {
			return fmt.Errorf("record finding[%d]: %w", i, err)
		}
		slog.Info("appraisal: minted Tier 1 Finding",
			"law_id", lawID,
			"goal", f.Goal,
			"applies_to", f.AppliesTo,
		)
	}

	return nil
}
