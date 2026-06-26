// Appraise is the review orchestrator node of the Foundry Cycle.
//
// It reads one or more input artefacts (e.g. "petition") and a review artefact
// (e.g. "haiku"), then orchestrates group-aware governance review using a
// fan-out pattern.
//
// Appraise operates in three phases:
//
//  1. Fix/Refusal Evaluation — For each ACTIONED or WONT_FIX feedback item,
//     the EvalAgent runs a focused evaluation to decide accept or reject.
//     These run in parallel, each with managed heartbeat and cost telemetry.
//
//  2. Fan-Out Review — Laws are partitioned by group, evaluation units and
//     a dispatch matrix are computed, and each dispatch is delegated to a
//     child Reviewer node via FanOut/AwaitChildren/CollectArtefacts. The
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
//	reviewerNode:     "reviewer"
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
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"text/template"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/handlers"
	"github.com/gideas/flow/nodes/internal/nodeconfig"
	flow "github.com/gideas/flow/sdk/go"
)

// appraiseConfig holds the node's configuration, loaded from a
// ConfigMap-mounted YAML file via nodeconfig.Load.
type appraiseConfig struct {
	InputArtefacts   []string          `yaml:"inputArtefacts"`   // artefact IDs to read as input (e.g. ["petition"])
	ReviewArtefact   string            `yaml:"reviewArtefact"`   // artefact ID to review (e.g. "haiku")
	GovernedArtefact string            `yaml:"governedArtefact"` // GovernedArtefact CR name (e.g. "haiku")
	ReviewerNode     string            `yaml:"reviewerNode"`     // target node for fan-out review (e.g. "reviewer")
	Appraisers       []AppraiserConfig `yaml:"appraisers"`       // appraiser persona configs

	// Optional ConfigMap prompt overrides. Empty strings use baked-in defaults.
	EvalSystemPrompt     string `yaml:"evalSystemPrompt"`     // override eval agent system prompt template
	EvalQueryTemplate    string `yaml:"evalQueryTemplate"`    // override eval agent query prompt template
	FindingSystemPrompt  string `yaml:"findingSystemPrompt"`  // override finding agent system prompt template
	FindingQueryTemplate string `yaml:"findingQueryTemplate"` // override finding agent query prompt template
}

// AppraiserConfig defines a single appraiser persona.
// ponytail: duplicated in nodes/internal/handlers/appraise.go;
// promote to SDK if a third definition appears.
type AppraiserConfig struct {
	ID          string `yaml:"id"`
	Personality string `yaml:"personality"`
}

// ---------------------------------------------------------------------------
// Agent Construction Helper
// ---------------------------------------------------------------------------

// buildAgent is the shared construction pattern for all appraise agents.
// It renders the system prompt template, parses the query template, and
// creates a flow.Agent with schema, model (KimiK2Ollama), and prompts.
//
// The model is created internally — model choice is a code-time decision
// coupled to the prompts, not deploy-time config.
func buildAgent(
	client *flow.Client,
	name string,
	sysTmplStr string,
	sysData any,
	queryTmplStr string,
	schema []byte,
) (*flow.Agent, error) {
	// 1. Render system prompt with config params.
	sysTmpl, err := template.New("system").Parse(sysTmplStr)
	if err != nil {
		return nil, fmt.Errorf("%s: parse system template: %w", name, err)
	}

	var sysBuf bytes.Buffer
	if err := sysTmpl.Execute(&sysBuf, sysData); err != nil {
		return nil, fmt.Errorf("%s: render system prompt: %w", name, err)
	}

	// 2. Parse query template.
	queryTmpl, err := template.New("query").Parse(queryTmplStr)
	if err != nil {
		return nil, fmt.Errorf("%s: parse query template: %w", name, err)
	}

	// 3. Create flow.Agent with schema, model, prompts.
	agent, err := flow.NewAgent(client,
		flow.WithSchema(schema),
		flow.WithModel(flow.NewKimiK2Ollama()),
		flow.WithSystemPrompt(sysBuf.String()),
		flow.WithQueryTemplate(queryTmpl),
	)
	if err != nil {
		return nil, fmt.Errorf("%s: create agent: %w", name, err)
	}

	return agent, nil
}

// ---------------------------------------------------------------------------
// Entrypoint
// ---------------------------------------------------------------------------

func main() {
	slog.Info("appraise: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("appraise: server failed", "error", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("appraise: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("appraise: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	// Load configuration from ConfigMap-mounted YAML.
	cfg, err := nodeconfig.Load[appraiseConfig](nodeconfig.Path())
	if err != nil {
		return fmt.Errorf("appraise: load config: %w", err)
	}

	// Create agents. Phase 2 (review) is delegated to child Reviewer nodes.
	evalAgent, err := NewEvalAgent(client, cfg)
	if err != nil {
		return fmt.Errorf("appraise: create eval agent: %w", err)
	}

	findingAgent, err := NewFindingAgent(client, cfg)
	if err != nil {
		return fmt.Errorf("appraise: create finding agent: %w", err)
	}

	// Build handler-level appraiser configs.
	appraisers := make([]handlers.AppraiserConfig, len(cfg.Appraisers))
	for i, a := range cfg.Appraisers {
		appraisers[i] = handlers.AppraiserConfig{
			ID:          a.ID,
			Personality: a.Personality,
		}
	}

	// Delegate to the shared handler with handler-level config.
	handlerCfg := handlers.AppraiseConfig{
		InputArtefacts:   cfg.InputArtefacts,
		ReviewArtefact:   cfg.ReviewArtefact,
		GovernedArtefact: cfg.GovernedArtefact,
		ReviewerNode:     cfg.ReviewerNode,
		Appraisers:       appraisers,
	}

	return handlers.HandleAppraise(ctx, client, evalAgent, findingAgent, handlerCfg)
}
