// Forge is the creator node of the Haiku Foundry Cycle.
//
// It reads the "petition" artefact (the creative brief) and generates a haiku
// using an LLM (gpt-oss:120b-cloud via Ollama). The generated haiku is stored
// as the "haiku" artefact (kind: text/haiku), then routed to Sort for
// governance triage.
//
// Environment:
//
//	OLLAMA_BASE_URL   — Ollama API endpoint (default: http://localhost:11434)
//	FORGE_MODEL       — Model name (default: gpt-oss:120b-cloud)
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/ollama"
	flow "github.com/gideas/flow/sdk/go"
)

const (
	defaultModel = "gpt-oss:120b-cloud"
	envModel     = "FORGE_MODEL"
)

func main() {
	slog.Info("haiku-forge: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("haiku-forge: server failed", "error", err)
		os.Exit(1)
	}
}

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("haiku-forge: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("forge: create client: %w", err)
	}
	defer client.Close()

	client.Heartbeat(ctx)

	// Read the petition (creative brief).
	petitionResp, err := client.GetArtefact(ctx, "petition")
	if err != nil {
		return fmt.Errorf("forge: read petition: %w", err)
	}
	petition := string(petitionResp.GetContent())
	slog.Info("haiku-forge: read petition", "petition", petition)

	// Query laws for haiku governance (if any exist).
	laws, _ := client.QueryLaws(ctx, "text/haiku", "")
	var lawContext string
	if len(laws) > 0 {
		lawContext = "\n\nApplicable governance laws:\n"
		for _, law := range laws {
			lawContext += fmt.Sprintf("- %s\n", law.GetGoal())
		}
	}

	// Generate haiku via LLM.
	model := os.Getenv(envModel)
	if model == "" {
		model = defaultModel
	}

	prompt := fmt.Sprintf(`You are a haiku poet. Write a single haiku (three lines: 5 syllables, 7 syllables, 5 syllables) based on the following request:

%s%s

IMPORTANT: Output ONLY the three lines of the haiku, nothing else. No title, no explanation, no quotes.`, petition, lawContext)

	llm := ollama.New()
	haiku, err := llm.Generate(ctx, model, prompt)
	if err != nil {
		return fmt.Errorf("forge: generate haiku: %w", err)
	}
	slog.Info("haiku-forge: generated haiku", "haiku", haiku)

	// Store the haiku artefact.
	storeResp, err := client.StoreArtefact(ctx, "haiku", "text/haiku", []byte(haiku))
	if err != nil {
		return fmt.Errorf("forge: store haiku: %w", err)
	}
	slog.Info("haiku-forge: stored haiku",
		"version_hash", storeResp.GetVersionHash(),
		"is_new_version", storeResp.GetIsNewVersion(),
	)

	// Route to Sort for governance triage.
	if _, err := client.RouteToOutput(ctx, "default"); err != nil {
		return fmt.Errorf("forge: route to sort: %w", err)
	}

	slog.Info("haiku-forge: routed to sort", "workitem_id", wctx.GetWorkitemId())
	return nil
}
