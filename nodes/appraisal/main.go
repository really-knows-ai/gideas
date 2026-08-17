// Appraisal is the review orchestrator node of the Foundry Cycle.
//
// It reads one or more input artefacts (e.g. "petition") and a review artefact
// (e.g. "haiku"), then orchestrates group-aware governance review using a
// fan-out pattern.
//
// Appraisal operates in three phases:
//
//  1. Fix/Refusal Evaluation — For each ACTIONED or WONT_FIX feedback item,
//     the EvalAgent runs a focused evaluation to decide accept or reject.
//     These run in parallel, each with managed heartbeat and cost telemetry.
//
//  2. Fan-Out Review — Laws are partitioned by group, evaluation units and
//     a dispatch matrix are computed, and each dispatch is delegated to a
//     child Appraiser node via FanOut/AwaitChildren/CollectArtefacts. The
//     parent collects and merges all review results, applies per-group and
//     per-law stamps, and emits coverage/attestation events.
//
//  3. Learning Capture — If Phase 1 resolved any feedback items that carried
//     a NovelArgument justification, the FindingAgent distils the learnings
//     into Tier 1 Findings recorded in the Library.
//
// Always routes back to Sort.
//
// Configuration is loaded from a ConfigMap-mounted YAML file:
//
//	inputArtefacts:
//	  - "petition"
//	reviewArtefact:   "haiku"
//	governedArtefact: "haiku"
//	reviewerNode:     "appraiser"
//	appraisers:
//	  - id: "skeptic"
//	    personality: "You are strict but fair."
//	  - id: "auditor"
//	    personality: "You audit for compliance."
//	evalSystemPrompt:    ""   # optional override
//	evalQueryTemplate:   ""   # optional override
//	findingSystemPrompt: ""   # optional override
//	findingQueryTemplate: ""  # optional override
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

// appraisalConfig holds the node's configuration, loaded from a
// ConfigMap-mounted YAML file via nodeconfig.Load.
type appraisalConfig struct {
	InputArtefacts   []string                              `yaml:"inputArtefacts"`
	ReviewArtefact   string                                `yaml:"reviewArtefact"`
	GovernedArtefact string                                `yaml:"governedArtefact"`
	ReviewerNode     string                                `yaml:"reviewerNode"`
	Appraisers       []handlers.AppraiserPersonalityConfig `yaml:"appraisers"`

	// Optional ConfigMap prompt overrides. Empty strings use baked-in defaults.
	EvalSystemPrompt     string `yaml:"evalSystemPrompt"`     // override eval agent system prompt template
	EvalQueryTemplate    string `yaml:"evalQueryTemplate"`    // override eval agent query prompt template
	FindingSystemPrompt  string `yaml:"findingSystemPrompt"`  // override finding agent system prompt template
	FindingQueryTemplate string `yaml:"findingQueryTemplate"` // override finding agent query prompt template
}

// ---------------------------------------------------------------------------
// Entrypoint
// ---------------------------------------------------------------------------

func main() {
	slog.Info("appraisal: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("appraisal: server failed", "error", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	client, workitem, err := nodeutil.SetupHandler(ctx, wctx, "appraisal")
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	// Load configuration from ConfigMap-mounted YAML.
	cfg, err := nodeconfig.Load[appraisalConfig](nodeconfig.Path())
	if err != nil {
		return fmt.Errorf("appraisal: load config: %w", err)
	}

	// Create agents. Phase 2 (review) is delegated to child Appraiser nodes.
	evalAgent, err := NewEvalAgent(client, cfg)
	if err != nil {
		return fmt.Errorf("appraisal: create eval agent: %w", err)
	}

	findingAgent, err := NewFindingAgent(client, cfg)
	if err != nil {
		return fmt.Errorf("appraisal: create finding agent: %w", err)
	}

	// Delegate to the shared handler with handler-level config.
	handlerCfg := handlers.AppraisalConfig{
		InputArtefacts:   cfg.InputArtefacts,
		ReviewArtefact:   cfg.ReviewArtefact,
		GovernedArtefact: cfg.GovernedArtefact,
		ReviewerNode:     cfg.ReviewerNode,
		Appraisers:       cfg.Appraisers,
	}

	return handlers.HandleAppraisal(ctx, workitem, client, evalAgent, findingAgent, handlerCfg)
}
