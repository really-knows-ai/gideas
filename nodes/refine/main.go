// Refine is the revision node of the Haiku Foundry Cycle.
//
// It reads the "petition" (creative brief), the current "haiku", applicable
// governance laws, and any unresolved feedback, then uses an LLM
// (deepseek-v4-flash:cloud via Ollama) to decide how to handle each item and
// produce a revised haiku.
//
// Refine operates in two phases:
//
//  1. Per-Item Triage — For each NEW or REJECTED feedback item, a single
//     FoundryAgent inference call decides whether to action (fix) or refuse
//     (won't fix) the item. Items are processed sequentially — each decision
//     completes before the next begins. Refusals require a structured
//     justification (law citation or novel argument). If a REJECTED item has a
//     linked ruling (contempt guard), it is force-actioned without LLM
//     inference.
//
//  2. Revision — A single FoundryAgent inference call takes the petition,
//     current haiku, applicable laws, and the actioned items from Phase 1
//     to produce a revised haiku addressing all committed fixes.
//
// If Phase 1 produces no actioned items (all feedback refused), Phase 2 is
// skipped — the existing haiku is stored unchanged and routed back to Sort.
//
// Always routes back to Sort for governance triage of the new version.
//
// Configuration is loaded from a ConfigMap-mounted YAML file:
//
//	inputArtefacts:
//	  - "petition"
//	outputArtefact:   "haiku"
//	governedArtefact: "haiku"
//	outputField:      "haiku"
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/nodes/internal/handlers"
	"github.com/foundry/flow/nodes/internal/nodeconfig"
	"github.com/foundry/flow/nodes/internal/nodeutil"
	flow "github.com/foundry/flow/sdk/go"
)

// refineConfig holds the node's configuration, loaded from a
// ConfigMap-mounted YAML file via nodeconfig.Load.
type refineConfig struct {
	InputArtefacts   []string `yaml:"inputArtefacts"`   // artefact IDs for the creative brief (e.g. ["petition"])
	OutputArtefact   string   `yaml:"outputArtefact"`   // artefact ID to revise and store back (e.g. "haiku")
	GovernedArtefact string   `yaml:"governedArtefact"` // GovernedArtefact CR name (e.g. "haiku")
	OutputField      string   `yaml:"outputField"`      // JSON key in revision output (e.g. "haiku")

	// Optional prompt overrides from ConfigMap. Empty = use baked-in defaults.
	TriageSystemPrompt    string `yaml:"triageSystemPrompt"`
	TriageQueryTemplate   string `yaml:"triageQueryTemplate"`
	RevisionSystemPrompt  string `yaml:"revisionSystemPrompt"`
	RevisionQueryTemplate string `yaml:"revisionQueryTemplate"`
}

// ---------------------------------------------------------------------------
// Entrypoint
// ---------------------------------------------------------------------------

func main() {
	slog.Info("refine: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("refine: server failed", "error", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	client, workitem, err := nodeutil.SetupHandler(ctx, wctx, "refine")
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	// Load configuration from ConfigMap-mounted YAML.
	cfg, err := nodeconfig.Load[refineConfig](nodeconfig.Path())
	if err != nil {
		return fmt.Errorf("refine: load config: %w", err)
	}

	// Create agents (model is created internally by nodeutil.BuildAgent).
	triageAgent, err := NewTriageAgent(client, cfg)
	if err != nil {
		return fmt.Errorf("refine: create triage agent: %w", err)
	}

	revisionAgent, err := NewRevisionAgent(client, cfg)
	if err != nil {
		return fmt.Errorf("refine: create revision agent: %w", err)
	}

	handlerCfg := handlers.RefineConfig{
		InputArtefacts:   cfg.InputArtefacts,
		OutputArtefact:   cfg.OutputArtefact,
		GovernedArtefact: cfg.GovernedArtefact,
	}

	return handlers.HandleRefine(ctx, workitem, triageAgent, revisionAgent, handlerCfg)
}
