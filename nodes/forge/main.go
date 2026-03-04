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

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/artefacts"
	"github.com/gideas/flow/nodes/internal/nodeconfig"
	flow "github.com/gideas/flow/sdk/go"
)

// forgeConfig holds the node's configuration, loaded from a ConfigMap-mounted
// YAML file via nodeconfig.Load.
type forgeConfig struct {
	InputArtefacts   []string `yaml:"inputArtefacts"`   // artefact IDs to read as input (e.g. ["petition"])
	OutputArtefact   string   `yaml:"outputArtefact"`   // artefact ID to write (e.g. "haiku")
	GovernedArtefact string   `yaml:"governedArtefact"` // GovernedArtefact CR name (e.g. "haiku")
	OutputField      string   `yaml:"outputField"`      // JSON key to extract from validated output
}

func main() {
	slog.Info("forge: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("forge: server failed", "error", err)
		os.Exit(1)
	}
}

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("forge: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("forge: create client: %w", err)
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

	return handleForge(ctx, client, agent, cfg)
}

// handleForge performs the core forge logic: read input, query laws, generate
// content, store output, and route onward.
func handleForge(ctx context.Context, client *flow.Client, agent *ForgeAgent, cfg *forgeConfig) error {
	// Read the input artefacts.
	input, err := artefacts.FetchInputs(ctx, client, cfg.InputArtefacts)
	if err != nil {
		return fmt.Errorf("forge: read inputs: %w", err)
	}
	slog.Info("forge: read inputs", "artefacts", cfg.InputArtefacts)

	// Query laws for governance (if any exist).
	laws, _ := client.QueryLaws(ctx, cfg.GovernedArtefact, "")

	// Generate content via the ForgeAgent.
	result, err := agent.Run(ctx, input, laws)
	if err != nil {
		return fmt.Errorf("forge: agent run: %w", err)
	}
	slog.Info("forge: generated content", "field", cfg.OutputField, "content", result)

	// Store the output artefact.
	storeResp, err := client.StoreArtefact(ctx, cfg.OutputArtefact, cfg.GovernedArtefact, []byte(result))
	if err != nil {
		return fmt.Errorf("forge: store %s: %w", cfg.OutputArtefact, err)
	}
	slog.Info("forge: stored artefact",
		"artefact", cfg.OutputArtefact,
		"version_hash", storeResp.GetVersionHash(),
		"is_new_version", storeResp.GetIsNewVersion(),
	)

	// Route onward.
	if _, err := client.RouteToOutput(ctx, "default"); err != nil {
		return fmt.Errorf("forge: route to output: %w", err)
	}

	slog.Info("forge: routed to output")
	return nil
}
