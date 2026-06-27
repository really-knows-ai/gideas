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
	ArtefactReview       = "review"
	ArtefactReviewOutput = "review-output"
)

// AppraiserPersonalityData is the JSON structure passed via the "appraiserPersonality" artefact.
type AppraiserPersonalityData struct {
	ID          string `json:"id"`
	Personality string `json:"personality"`
}

// PassData is the JSON structure passed via the "pass" artefact.
type PassData struct {
	Pass int `json:"pass"`
	Of   int `json:"of"`
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

// reviewOutputData wraps the SDK ReviewResult with traceability metadata
// (appraiser ID and pass number). JSON tags produce the wire format expected
// by the parent Appraisal handler.
// ponytail: exists because ReviewResult has no json tags; if tags are added
// to the SDK types, this wrapper can be deleted.
type reviewOutputData struct {
	Feedback  []outputFeedbackItem `json:"feedback"`
	Appraiser string               `json:"appraiser,omitempty"`
	Pass      int                  `json:"pass,omitempty"`
}

// outputFeedbackItem is a single feedback observation in wire format.
type outputFeedbackItem struct {
	Message   string   `json:"message"`
	CitedLaws []string `json:"cited_laws"`
}

// HandleReview executes the Reviewer node handler logic using the provided
// contract implementation. The handler is generic — it works with any
// ReviewContract.
//
// Steps: fetch inputs → get review artefact → deserialize laws/history/
// appraiser personality from artefacts → call agent → marshal output → store
// review-output artefact → Complete().
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

	// Read and deserialize appraiserPersonality (optional, backward compat).
	var appraiserData AppraiserPersonalityData
	appraiserResp, appraiserErr := client.GetArtefact(ctx, ArtefactAppraiserPersonality)
	if appraiserErr == nil {
		if err := json.Unmarshal(appraiserResp.GetContent(), &appraiserData); err != nil {
			return fmt.Errorf("appraiser: unmarshal appraiserPersonality: %w", err)
		}
	}
	// If artefact is absent, appraiserData stays zero-valued — fine.

	// Read and deserialize pass (optional, backward compat).
	var passData PassData
	passResp, passErr := client.GetArtefact(ctx, ArtefactPass)
	if passErr == nil {
		if err := json.Unmarshal(passResp.GetContent(), &passData); err != nil {
			return fmt.Errorf("appraiser: unmarshal pass: %w", err)
		}
	}

	slog.Info("appraiser: reviewing",
		"appraiser", appraiserData.ID,
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
		"appraiser", appraiserData.ID,
		"feedback_count", len(out.Feedback),
	)

	// ---------------------------------------------------------------
	// Store review-output artefact for parent to collect
	// ---------------------------------------------------------------

	// Build extended output with traceability metadata.
	outData := reviewOutputData{
		Appraiser: appraiserData.ID,
		Pass:      passData.Pass,
	}
	outData.Feedback = make([]outputFeedbackItem, len(out.Feedback))
	for i, fb := range out.Feedback {
		outData.Feedback[i] = outputFeedbackItem{
			Message:   fb.Message,
			CitedLaws: fb.CitedLaws,
		}
	}

	outJSON, err := json.Marshal(outData)
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
		"appraiser", appraiserData.ID,
		"workitem_id", os.Getenv(flow.EnvWorkitemID),
	)
	return nil
}
