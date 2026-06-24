// Refine is the revision node of the Haiku Foundry Cycle.
//
// It reads the "petition" (creative brief), the current "haiku", applicable
// governance laws, and any unresolved feedback, then uses an LLM
// (gpt-oss:120b-cloud via Ollama) to decide how to handle each item and
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
// Agent Construction Helper
// ---------------------------------------------------------------------------

// buildAgent is the shared construction pattern for the triage and revision
// agents. It renders the system prompt template, parses the query template,
// and creates a flow.Agent with schema, model (GptOss120bOllama), and prompts.
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
		flow.WithModel(flow.NewGptOss120bOllama()),
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
	slog.Info("refine: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("refine: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	// Load configuration from ConfigMap-mounted YAML.
	cfg, err := nodeconfig.Load[refineConfig](nodeconfig.Path())
	if err != nil {
		return fmt.Errorf("refine: load config: %w", err)
	}

	// Create agents (model is created internally by buildAgent).
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

	return handlers.HandleRefine(ctx, client, triageAgent, revisionAgent, handlerCfg)
}
