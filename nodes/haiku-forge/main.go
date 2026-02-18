// Forge is the creator node of the Haiku Foundry Cycle.
//
// It reads the "petition" artefact (the creative brief) and generates a haiku
// using an LLM (gpt-oss:120b-cloud via Ollama). The generated haiku is stored
// as the "haiku" artefact (kind: text/haiku), then routed to Sort for
// governance triage.
//
// Forge uses FoundryAgent to wrap the LLM call, providing:
//   - Managed heartbeats during inference (no timeout during long generations)
//   - JSON Schema validation of the structured output before storage
//   - Automatic foundry.cost.llm telemetry with token counts and timing
//
// Environment:
//
//	OLLAMA_BASE_URL   — Ollama API endpoint (default: http://localhost:11434)
//	FORGE_MODEL       — Model name (default: gpt-oss:120b-cloud)
package main

import (
	"context"
	"encoding/json"
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

// haikuSchema is the JSON Schema for the structured output of the haiku
// generation step. FoundryAgent validates every inference output against
// this schema before it can enter the governed pipeline.
var haikuSchema = []byte(`{
	"type": "object",
	"properties": {
		"haiku": { "type": "string", "minLength": 1 }
	},
	"required": ["haiku"],
	"additionalProperties": false
}`)

// haikuOutput is the Go representation of the schema-validated JSON output.
type haikuOutput struct {
	Haiku string `json:"haiku"`
}

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

	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("forge: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	// Read the petition (creative brief).
	petitionResp, err := client.GetArtefact(ctx, "petition")
	if err != nil {
		return fmt.Errorf("forge: read petition: %w", err)
	}
	petition := string(petitionResp.GetContent())
	slog.Info("haiku-forge: read petition", "petition", petition)

	// Query laws for haiku governance (if any exist).
	laws, _ := client.QueryLaws(ctx, "haiku", "")
	var lawContext string
	if len(laws) > 0 {
		lawContext = "\n\nApplicable governance laws:\n"
		for _, law := range laws {
			lawContext += fmt.Sprintf("- %s\n", law.GetGoal())
		}
	}

	// Build the prompt input for the inference function.
	prompt := fmt.Sprintf(`You are a haiku poet. Write a single haiku
(three lines: 5 syllables, 7 syllables, 5 syllables) based on the
following request:

%s%s

IMPORTANT: Respond with a JSON object containing a single key "haiku"
whose value is the three-line haiku. Example:
{"haiku": "autumn moonlight\na worm digs silently\ninto the chestnut"}

Output ONLY the JSON object, nothing else.`, petition, lawContext)

	// Resolve the model name.
	model := os.Getenv(envModel)
	if model == "" {
		model = defaultModel
	}

	// Create the FoundryAgent with the haiku output schema.
	agent, err := flow.NewAgent(client, haikuSchema)
	if err != nil {
		return fmt.Errorf("forge: create agent: %w", err)
	}

	// Define the inference function that calls Ollama and returns
	// structured output with cost metadata.
	inferFn := makeInferFunc(model)

	// Run inference with managed heartbeat, schema validation, and
	// cost telemetry.
	output, err := agent.Run(ctx, inferFn, []byte(prompt))
	if err != nil {
		return fmt.Errorf("forge: agent run: %w", err)
	}

	// Extract the haiku text from the validated JSON output.
	var parsed haikuOutput
	if err := json.Unmarshal(output, &parsed); err != nil {
		return fmt.Errorf("forge: unmarshal output: %w", err)
	}
	slog.Info("haiku-forge: generated haiku", "haiku", parsed.Haiku)

	// Store the haiku artefact.
	storeResp, err := client.StoreArtefact(ctx, "haiku", "haiku", []byte(parsed.Haiku))
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

// makeInferFunc returns an InferFunc that generates a haiku via Ollama.
// The prompt is received as the input parameter from Agent.Run.
func makeInferFunc(model string) flow.InferFunc {
	return func(ctx context.Context, input []byte) (*flow.InferResult, error) {
		llm := ollama.New()
		result, err := llm.GenerateRich(ctx, model, string(input))
		if err != nil {
			return nil, fmt.Errorf("ollama generate: %w", err)
		}

		return &flow.InferResult{
			Output:       []byte(result.Response),
			Model:        model,
			InputTokens:  result.PromptTokens,
			OutputTokens: result.OutputTokens,
			DurationMs:   result.DurationMs,
			Extra:        map[string]any{"provider": "ollama"},
		}, nil
	}
}
