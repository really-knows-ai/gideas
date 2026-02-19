// Forge is the creator node of the Foundry Cycle.
//
// It reads an input artefact (e.g. "petition"), queries governance laws, and
// generates content using an LLM via the FoundryAgent abstraction. The
// generated content is stored as an output artefact and routed onward.
//
// Forge uses FoundryAgent with a concrete ForgeAgent to wrap the LLM call,
// providing:
//   - Provider-abstracted inference (Ollama, OpenAI-compat, etc.)
//   - Managed heartbeats during inference (no timeout during long generations)
//   - JSON Schema validation of the structured output before storage
//   - Automatic foundry.cost.llm telemetry with token counts and timing
//
// Configuration is loaded from a ConfigMap-mounted YAML file:
//
//	inputArtefact:    "petition"
//	outputArtefact:   "haiku"
//	governedArtefact: "haiku"
//	model:            "gpt-oss:120b-cloud"
//	outputField:      "haiku"
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/nodeconfig"
	flow "github.com/gideas/flow/sdk/go"
)

// forgeConfig holds the node's configuration, loaded from a ConfigMap-mounted
// YAML file via nodeconfig.Load.
type forgeConfig struct {
	InputArtefact    string `yaml:"inputArtefact"`    // artefact ID to read (e.g. "petition")
	OutputArtefact   string `yaml:"outputArtefact"`   // artefact ID to write (e.g. "haiku")
	GovernedArtefact string `yaml:"governedArtefact"` // GovernedArtefact CR name (e.g. "haiku")
	Model            string `yaml:"model"`            // LLM model name
	OutputField      string `yaml:"outputField"`      // JSON key to extract from validated output
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

	// Create provider and agent.
	provider := flow.NewOllamaProvider()
	agent, err := NewForgeAgent(client, provider, cfg)
	if err != nil {
		return fmt.Errorf("forge: create agent: %w", err)
	}

	return handleForge(ctx, client, agent, cfg)
}

// handleForge performs the core forge logic: read input, query laws, generate
// content, store output, and route onward.
func handleForge(ctx context.Context, client *flow.Client, agent *ForgeAgent, cfg *forgeConfig) error {
	// Read the input artefact.
	inputResp, err := client.GetArtefact(ctx, cfg.InputArtefact)
	if err != nil {
		return fmt.Errorf("forge: read %s: %w", cfg.InputArtefact, err)
	}
	input := string(inputResp.GetContent())
	slog.Info("forge: read input", "artefact", cfg.InputArtefact, "content", input)

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
