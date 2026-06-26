// Appraiser is a lightweight appraiser node that only does Phase 2 (fresh review).
//
// It receives all data as artefacts from its parent orchestrator (Appraisal):
//
//   - input:   The creative brief / petition text.
//   - review:  The artefact under review.
//   - laws:    JSON array of laws to review against.
//   - history: JSON array of previous feedback items.
//   - appraiserPersonality: JSON object with appraiser id and personality.
//
// The Appraiser runs an AppraiserAgent against the laws for its appraiser, stores
// the review output as a "review-output" artefact, and calls Complete().
//
// The Appraiser does not query the Librarian or any external service — all data
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

// appraiserNodeConfig holds the node's configuration, loaded from a
// ConfigMap-mounted YAML file via nodeconfig.Load.
type appraiserNodeConfig struct {
	InputArtefacts []string `yaml:"inputArtefacts"` // artefact IDs for the input (e.g. ["input"])
	ReviewArtefact string   `yaml:"reviewArtefact"` // artefact ID for the artefact under review (e.g. "review")
	SystemPrompt   string   `yaml:"systemPrompt"`   // optional override for the system prompt template
	QueryTemplate  string   `yaml:"queryTemplate"`  // optional override for the query prompt template
}

// ---------------------------------------------------------------------------
// Entrypoint
// ---------------------------------------------------------------------------

func main() {
	slog.Info("appraiser: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("appraiser: server failed", "error", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("appraiser: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("appraiser: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	cfg, err := nodeconfig.Load[appraiserNodeConfig](nodeconfig.Path())
	if err != nil {
		return fmt.Errorf("appraiser: load config: %w", err)
	}

	// Read the appraiser personality artefact before constructing the agent so the
	// personality prompt suffix is baked into the system prompt.
	apprResp, err := client.GetArtefact(ctx, handlers.ArtefactAppraiserPersonality)
	if err != nil {
		return fmt.Errorf("appraiser: read %s: %w", handlers.ArtefactAppraiserPersonality, err)
	}

	var apprData handlers.AppraiserPersonalityData
	if err := json.Unmarshal(apprResp.GetContent(), &apprData); err != nil {
		return fmt.Errorf("appraiser: unmarshal appraiser data: %w", err)
	}

	// Construct the agent with appraiser personality and optional prompt overrides.
	opts := &AppraiserAgentOpts{
		SystemPrompt:  cfg.SystemPrompt,
		QueryTemplate: cfg.QueryTemplate,
	}
	agent, err := NewAppraiserAgent(client, cfg, apprData.Personality, opts)
	if err != nil {
		return fmt.Errorf("appraiser: create appraiser agent: %w", err)
	}

	handlerCfg := handlers.ReviewConfig{
		InputArtefacts: cfg.InputArtefacts,
		ReviewArtefact: cfg.ReviewArtefact,
	}

	return handlers.HandleReview(ctx, client, agent, handlerCfg)
}
