// Reviewer is a lightweight review node that only does Phase 2 (fresh review).
//
// It receives all data as artefacts from its parent orchestrator (Appraise):
//
//   - input:   The creative brief / petition text.
//   - review:  The artefact under review.
//   - laws:    JSON array of laws to review against.
//   - history: JSON array of previous feedback items.
//   - division: JSON object with division name and optional prompt suffix.
//
// The Reviewer runs a ReviewAgent against the laws for its division, stores
// the review output as a "review-output" artefact, and calls Complete().
//
// The Reviewer does not query the Librarian or any external service — all data
// arrives via artefacts from the parent.
//
// Configuration is loaded from a ConfigMap-mounted YAML file:
//
//	inputArtefact:  "input"
//	reviewArtefact: "review"
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/nodeconfig"
	flow "github.com/gideas/flow/sdk/go"
)

// reviewerConfig holds the node's configuration, loaded from a
// ConfigMap-mounted YAML file via nodeconfig.Load.
type reviewerConfig struct {
	InputArtefact  string `yaml:"inputArtefact"`  // artefact ID for the input (e.g. "input")
	ReviewArtefact string `yaml:"reviewArtefact"` // artefact ID for the artefact under review (e.g. "review")
}

// Convention artefact IDs shared between the parent Appraise orchestrator
// and the child Reviewer node. These are not configurable.
const (
	artefactLaws         = "laws"
	artefactHistory      = "history"
	artefactDivision     = "division"
	artefactReviewOutput = "review-output"
)

// divisionData is the JSON structure passed via the "division" artefact.
type divisionData struct {
	Name         string `json:"name"`
	PromptSuffix string `json:"promptSuffix"`
}

// lawData is the minimal law representation passed via the "laws" artefact.
// Only the fields the ReviewAgent needs are included.
type lawData struct {
	ID   string `json:"id"`
	Tier int32  `json:"tier"`
	Goal string `json:"goal"`
}

// historyData is a single feedback history item passed via the "history" artefact.
type historyData struct {
	State   string `json:"state"`
	Message string `json:"message"`
}

// ---------------------------------------------------------------------------
// Entrypoint
// ---------------------------------------------------------------------------

func main() {
	slog.Info("reviewer: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("reviewer: server failed", "error", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("reviewer: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("reviewer: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	cfg, err := nodeconfig.Load[reviewerConfig](nodeconfig.Path())
	if err != nil {
		return fmt.Errorf("reviewer: load config: %w", err)
	}

	return handleReview(ctx, client, cfg)
}

// ---------------------------------------------------------------------------
// Core Logic
// ---------------------------------------------------------------------------

// handleReview performs the Reviewer's single responsibility: fresh review
// against a set of laws for a specific division.
func handleReview(
	ctx context.Context,
	client *flow.Client,
	cfg *reviewerConfig,
) error {
	// ---------------------------------------------------------------
	// Read artefacts from parent
	// ---------------------------------------------------------------

	inputResp, err := client.GetArtefact(ctx, cfg.InputArtefact)
	if err != nil {
		return fmt.Errorf("reviewer: read %s: %w", cfg.InputArtefact, err)
	}
	inputContent := string(inputResp.GetContent())

	reviewResp, err := client.GetArtefact(ctx, cfg.ReviewArtefact)
	if err != nil {
		return fmt.Errorf("reviewer: read %s: %w", cfg.ReviewArtefact, err)
	}
	reviewContent := string(reviewResp.GetContent())

	// Read and deserialize laws.
	lawsResp, err := client.GetArtefact(ctx, artefactLaws)
	if err != nil {
		return fmt.Errorf("reviewer: read %s: %w", artefactLaws, err)
	}

	var laws []lawData
	if err := json.Unmarshal(lawsResp.GetContent(), &laws); err != nil {
		return fmt.Errorf("reviewer: unmarshal laws: %w", err)
	}

	// Read and deserialize history.
	historyResp, err := client.GetArtefact(ctx, artefactHistory)
	if err != nil {
		return fmt.Errorf("reviewer: read %s: %w", artefactHistory, err)
	}

	var history []historyData
	if err := json.Unmarshal(historyResp.GetContent(), &history); err != nil {
		return fmt.Errorf("reviewer: unmarshal history: %w", err)
	}

	// Read and deserialize division.
	divisionResp, err := client.GetArtefact(ctx, artefactDivision)
	if err != nil {
		return fmt.Errorf("reviewer: read %s: %w", artefactDivision, err)
	}

	var division divisionData
	if err := json.Unmarshal(divisionResp.GetContent(), &division); err != nil {
		return fmt.Errorf("reviewer: unmarshal division: %w", err)
	}

	slog.Info("reviewer: reviewing",
		"division", division.Name,
		"law_count", len(laws),
		"history_count", len(history),
	)

	// ---------------------------------------------------------------
	// Build and run ReviewAgent
	// ---------------------------------------------------------------

	agent, err := NewReviewAgent(client, cfg, division.PromptSuffix)
	if err != nil {
		return fmt.Errorf("reviewer: create review agent: %w", err)
	}

	out, err := agent.Run(ctx, inputContent, reviewContent, laws, history)
	if err != nil {
		return fmt.Errorf("reviewer: review run: %w", err)
	}

	slog.Info("reviewer: review complete",
		"division", division.Name,
		"feedback_count", len(out.Feedback),
	)

	// ---------------------------------------------------------------
	// Store review-output artefact for parent to collect
	// ---------------------------------------------------------------

	outJSON, err := json.Marshal(out)
	if err != nil {
		return fmt.Errorf("reviewer: marshal review output: %w", err)
	}

	// The governed artefact for child data transfer is "review-data" —
	// internal plumbing, not a governed work product.
	if _, err := client.StoreArtefact(ctx, artefactReviewOutput, "review-data", outJSON); err != nil {
		return fmt.Errorf("reviewer: store %s: %w", artefactReviewOutput, err)
	}

	// ---------------------------------------------------------------
	// Signal completion
	// ---------------------------------------------------------------

	if _, err := client.Complete(ctx); err != nil {
		return fmt.Errorf("reviewer: complete: %w", err)
	}

	slog.Info("reviewer: completed",
		"division", division.Name,
		"workitem_id", os.Getenv(flow.EnvWorkitemID),
	)
	return nil
}
