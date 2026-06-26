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
//	inputArtefacts:
//	  - "input"
//	reviewArtefact: "review"
//	systemPrompt: ""      # optional override for system prompt template
//	queryTemplate: ""     # optional override for query prompt template
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/handlers"
	"github.com/gideas/flow/nodes/internal/nodeconfig"
	flow "github.com/gideas/flow/sdk/go"
)

// reviewerConfig holds the node's configuration, loaded from a
// ConfigMap-mounted YAML file via nodeconfig.Load.
type reviewerConfig struct {
	InputArtefacts []string `yaml:"inputArtefacts"` // artefact IDs for the input (e.g. ["input"])
	ReviewArtefact string   `yaml:"reviewArtefact"` // artefact ID for the artefact under review (e.g. "review")
	SystemPrompt   string   `yaml:"systemPrompt"`   // optional override for the system prompt template
	QueryTemplate  string   `yaml:"queryTemplate"`  // optional override for the query prompt template
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

	// Read the group artefact before constructing the agent so the
	// group prompt suffix is baked into the system prompt.
	groupResp, err := client.GetArtefact(ctx, handlers.ArtefactGroup)
	if err != nil {
		return fmt.Errorf("reviewer: read %s: %w", handlers.ArtefactGroup, err)
	}

	var groupData handlers.GroupData
	if err := json.Unmarshal(groupResp.GetContent(), &groupData); err != nil {
		return fmt.Errorf("reviewer: unmarshal group data: %w", err)
	}

	// Construct the agent with group suffix and optional prompt overrides.
	opts := &ReviewAgentOpts{
		SystemPrompt:  cfg.SystemPrompt,
		QueryTemplate: cfg.QueryTemplate,
	}
	agent, err := NewReviewAgent(client, cfg, groupData.PromptSuffix, opts)
	if err != nil {
		return fmt.Errorf("reviewer: create review agent: %w", err)
	}

	handlerCfg := handlers.ReviewConfig{
		InputArtefacts: cfg.InputArtefacts,
		ReviewArtefact: cfg.ReviewArtefact,
	}

	return handlers.HandleReview(ctx, client, agent, handlerCfg)
}
