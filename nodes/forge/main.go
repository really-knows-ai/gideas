// Forge is the creator node of the Foundry Cycle.
//
// It reads one or more input artefacts (e.g. "petition"), queries governance
// laws, and generates content using an LLM via the FoundryAgent abstraction.
// The generated content is stored as an output artefact and routed onward.
//
// Forge uses FoundryAgent with a concrete ForgeAgent to wrap the LLM call,
// providing:
//   - Model-encapsulated inference (model choice is code, not config)
//   - Managed heartbeats during inference (no timeout during long generations)
//   - JSON Schema validation of the structured output before storage
//   - Automatic foundry.cost.llm telemetry with token counts and timing
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

// forgeConfig holds the node's configuration, loaded from a ConfigMap-mounted
// YAML file via nodeconfig.Load.
type forgeConfig struct {
	InputArtefacts    []string `yaml:"inputArtefacts"`          // artefact IDs to read as input (e.g. ["petition"])
	OutputArtefact    string   `yaml:"outputArtefact"`          // artefact ID to write (e.g. "haiku")
	GovernedArtefact  string   `yaml:"governedArtefact"`        // GovernedArtefact CR name (e.g. "haiku")
	OutputField       string   `yaml:"outputField"`             // JSON key to extract from validated output
	SystemPrompt      string   `yaml:"systemPrompt"`            // optional: overrides baked-in system prompt template
	QueryTemplate     string   `yaml:"queryTemplate"`           // optional: overrides baked-in query prompt template
	ValidationRetries int      `yaml:"outputValidationRetries"` // optional: invalid output retry count
}

func main() {
	slog.Info("forge: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("forge: server failed", "error", err)
		os.Exit(1)
	}
}

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	client, workitem, err := nodeutil.SetupHandler(ctx, wctx, "forge")
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	// Load configuration from ConfigMap-mounted YAML.
	cfg, err := nodeconfig.Load[forgeConfig](nodeconfig.Path())
	if err != nil {
		return fmt.Errorf("forge: load config: %w", err)
	}

	// Create agent (model is created internally by ForgeAgent).
	agent, err := NewForgeAgent(client, cfg)
	if err != nil {
		return fmt.Errorf("forge: create agent: %w", err)
	}

	handlerCfg := handlers.ForgeConfig{
		InputArtefacts:   cfg.InputArtefacts,
		OutputArtefact:   cfg.OutputArtefact,
		GovernedArtefact: cfg.GovernedArtefact,
	}

	return handlers.HandleForge(ctx, workitem, agent, handlerCfg)
}
