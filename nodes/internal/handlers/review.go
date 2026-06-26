package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/gideas/flow/nodes/internal/artefacts"
	flow "github.com/gideas/flow/sdk/go"
)

// ReviewConfig holds handler-level configuration for the Reviewer handler.
// Agent-level config (prompts, model, schema) is encapsulated in the
// concrete review agent.
type ReviewConfig struct {
	InputArtefacts []string // artefact IDs for the input (e.g. ["input"])
	ReviewArtefact string   // artefact ID for the artefact under review (e.g. "review")
}

// Convention artefact IDs shared between the parent Appraise orchestrator
// and the child Reviewer node. These are not configurable.
const (
	ArtefactLaws         = "laws"
	ArtefactHistory      = "history"
	ArtefactGroup        = "group"
	ArtefactReviewOutput = "review-output"
)

// GroupData is the JSON structure passed via the "group" artefact.
type GroupData struct {
	Name         string `json:"name"`
	PromptSuffix string `json:"promptSuffix"`
}

// AppraiserPersonalityData is the JSON structure passed via the "appraiserPersonality" artefact.
type AppraiserPersonalityData struct {
	ID          string `json:"id"`
	Personality string `json:"personality"`
}

// LawData is the minimal law representation passed via the "laws" artefact.
// Only the fields the AppraiserAgent needs are included.
type LawData struct {
	ID   string `json:"id"`
	Tier int32  `json:"tier"`
	Goal string `json:"goal"`
}

// HistoryData is a single feedback history item passed via the "history"
// artefact.
type HistoryData struct {
	State   string `json:"state"`
	Message string `json:"message"`
}

// HandleReview executes the Reviewer node handler logic using the provided
// contract implementation. The handler is generic — it works with any
// ReviewContract.
//
// Steps: fetch inputs → get review artefact → deserialize laws/history/
// division from artefacts → call agent → marshal output → store review-output
// artefact → Complete().
func HandleReview(
	ctx context.Context,
	client *flow.Client,
	agent flow.ReviewContract,
	cfg ReviewConfig,
) error {
	// ---------------------------------------------------------------
	// Read artefacts from parent
	// ---------------------------------------------------------------

	inputContent, err := artefacts.FetchInputs(ctx, client, cfg.InputArtefacts)
	if err != nil {
		return fmt.Errorf("appraiser: read inputs: %w", err)
	}

	reviewResp, err := client.GetArtefact(ctx, cfg.ReviewArtefact)
	if err != nil {
		return fmt.Errorf("appraiser: read %s: %w", cfg.ReviewArtefact, err)
	}
	reviewContent := string(reviewResp.GetContent())

	// Read and deserialize laws.
	lawsResp, err := client.GetArtefact(ctx, ArtefactLaws)
	if err != nil {
		return fmt.Errorf("appraiser: read %s: %w", ArtefactLaws, err)
	}

	var lawItems []LawData
	if err := json.Unmarshal(lawsResp.GetContent(), &lawItems); err != nil {
		return fmt.Errorf("appraiser: unmarshal laws: %w", err)
	}

	// Read and deserialize history.
	historyResp, err := client.GetArtefact(ctx, ArtefactHistory)
	if err != nil {
		return fmt.Errorf("appraiser: read %s: %w", ArtefactHistory, err)
	}

	var historyItems []HistoryData
	if err := json.Unmarshal(historyResp.GetContent(), &historyItems); err != nil {
		return fmt.Errorf("appraiser: unmarshal history: %w", err)
	}

	// Read and deserialize group data.
	groupResp, err := client.GetArtefact(ctx, ArtefactGroup)
	if err != nil {
		return fmt.Errorf("appraiser: read %s: %w", ArtefactGroup, err)
	}

	var groupData GroupData
	if err := json.Unmarshal(groupResp.GetContent(), &groupData); err != nil {
		return fmt.Errorf("appraiser: unmarshal group data: %w", err)
	}

	slog.Info("appraiser: reviewing",
		"group", groupData.Name,
		"law_count", len(lawItems),
		"history_count", len(historyItems),
	)

	// ---------------------------------------------------------------
	// Convert to SDK contract types and call agent
	// ---------------------------------------------------------------

	laws := make([]flow.ReviewLaw, len(lawItems))
	for i, l := range lawItems {
		laws[i] = flow.ReviewLaw{
			ID:   l.ID,
			Tier: l.Tier,
			Goal: l.Goal,
		}
	}

	history := make([]flow.ReviewHistory, len(historyItems))
	for i, h := range historyItems {
		history[i] = flow.ReviewHistory{
			State:   h.State,
			Message: h.Message,
		}
	}

	out, err := agent.Run(ctx, inputContent, reviewContent, laws, history)
	if err != nil {
		return fmt.Errorf("appraiser: review run: %w", err)
	}

	slog.Info("appraiser: review complete",
		"group", groupData.Name,
		"feedback_count", len(out.Feedback),
	)

	// ---------------------------------------------------------------
	// Store review-output artefact for parent to collect
	// ---------------------------------------------------------------

	outJSON, err := json.Marshal(out)
	if err != nil {
		return fmt.Errorf("appraiser: marshal review output: %w", err)
	}

	// The governed artefact for child data transfer is "review-data" —
	// internal plumbing, not a governed work product.
	if _, err := client.StoreArtefact(ctx, ArtefactReviewOutput, "review-data", outJSON); err != nil {
		return fmt.Errorf("appraiser: store %s: %w", ArtefactReviewOutput, err)
	}

	// ---------------------------------------------------------------
	// Signal completion
	// ---------------------------------------------------------------

	if _, err := client.Complete(ctx); err != nil {
		return fmt.Errorf("appraiser: complete: %w", err)
	}

	slog.Info("appraiser: completed",
		"group", groupData.Name,
		"workitem_id", os.Getenv(flow.EnvWorkitemID),
	)
	return nil
}
