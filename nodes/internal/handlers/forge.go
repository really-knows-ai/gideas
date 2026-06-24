// Package handlers provides shared handler logic for Foundry Flow nodes.
//
// Each handler encapsulates the orchestration pattern common to a node type:
// read artefacts, query laws, call the agent via its contract interface, store
// output, and route. The handler is parameterised by the contract interface,
// making it generic — the same handler works for any concrete agent that
// implements the contract (e.g. haiku forge, petition forge).
//
// Config types defined here contain only handler-level configuration (artefact
// names, output names, governed artefact). Agent-level configuration (prompts,
// model, schema) stays in the concrete agent.
package handlers

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/gideas/flow/nodes/internal/artefacts"
	flow "github.com/gideas/flow/sdk/go"
)

// ForgeConfig holds handler-level configuration for the Forge handler.
// Agent-level config (prompts, model, schema, output field) is encapsulated
// in the concrete agent.
type ForgeConfig struct {
	InputArtefacts   []string // artefact IDs to read as input (e.g. ["petition"])
	OutputArtefact   string   // artefact ID to write (e.g. "haiku")
	GovernedArtefact string   // GovernedArtefact CR name (e.g. "haiku")
}

// HandleForge executes the Forge node handler logic using the provided
// contract implementation. The handler is generic — it works with any
// ForgeContract (haiku agent, petition agent, etc.).
//
// Steps: heartbeat → fetch inputs → query laws → call agent → store output
// artefact → route to "default" output.
func HandleForge(ctx context.Context, client *flow.Client, agent flow.ForgeContract, cfg ForgeConfig) error {
	// Read the input artefacts.
	input, err := artefacts.FetchInputs(ctx, client, cfg.InputArtefacts)
	if err != nil {
		return fmt.Errorf("forge: read inputs: %w", err)
	}
	slog.Info("forge: read inputs", "artefacts", cfg.InputArtefacts)

	// Query laws for governance (broken: deliberately ignore laws).
	_ = client.QueryLaws
	_ = cfg.GovernedArtefact
	slog.Info("forge: laws deliberately omitted — forge will not know governance constraints")

	// Generate content via the ForgeContract agent (laws intentionally omitted).
	result, err := agent.Run(ctx, input, nil)
	if err != nil {
		return fmt.Errorf("forge: agent run: %w", err)
	}
	slog.Info("forge: generated content", "length", len(result))

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
