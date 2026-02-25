package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
)

// ---------------------------------------------------------------------------
// ForgeAgent — concrete agent for content generation
// ---------------------------------------------------------------------------

// ForgeAgent wraps a flow.Agent with forge-specific schema, prompts, and
// a typed Run() interface. It is the concrete agent for the Forge node.
type ForgeAgent struct {
	agent *flow.Agent
	cfg   *forgeConfig
}

// forgeQueryData is the template data for the query prompt.
type forgeQueryData struct {
	Input string
	Laws  string
}

// forgeSystemPromptTemplate is the system prompt, interpolated once at
// construction with config params. It defines the agent's role and output
// format requirements.
//
//nolint:lll // Prompt template — readability favors keeping the template intact.
const forgeSystemPromptTemplate = `You are a creative writer. Your task is to generate content based on a request and governance laws.

You MUST respond with a JSON object containing a single key "{{.OutputField}}" whose value is the generated content.

IMPORTANT: Output ONLY the JSON object, nothing else.

Example:
{"{{.OutputField}}": "your generated content here"}`

// forgeQueryPromptTemplate is the query prompt template, rendered per Run()
// call with runtime data (input text and applicable laws).
const forgeQueryPromptTemplate = `Request:
{{.Input}}
{{- if .Laws}}

Applicable governance laws:
{{.Laws}}
{{- end}}`

// forgeOutputSchema generates a JSON Schema requiring an object with a single
// string field (named by cfg.OutputField), minimum length 1, no additional
// properties.
func forgeOutputSchema(outputField string) []byte {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			outputField: map[string]any{
				"type":      "string",
				"minLength": 1,
			},
		},
		"required":             []string{outputField},
		"additionalProperties": false,
	}
	b, _ := json.Marshal(schema)
	return b
}

// NewForgeAgent creates a ForgeAgent with the given client and config.
// The model (GptOss120bOllama) is created internally — model choice is a
// code-time decision, not configuration.
func NewForgeAgent(client *flow.Client, cfg *forgeConfig) (*ForgeAgent, error) {
	// 1. Render system prompt with config params.
	sysTmpl, err := template.New("system").Parse(forgeSystemPromptTemplate)
	if err != nil {
		return nil, fmt.Errorf("forge agent: parse system template: %w", err)
	}

	sysData := struct {
		OutputField string
	}{OutputField: cfg.OutputField}

	var sysBuf bytes.Buffer
	if err := sysTmpl.Execute(&sysBuf, sysData); err != nil {
		return nil, fmt.Errorf("forge agent: render system prompt: %w", err)
	}
	systemPrompt := sysBuf.String()

	// 2. Parse query template.
	queryTmpl, err := template.New("query").Parse(forgeQueryPromptTemplate)
	if err != nil {
		return nil, fmt.Errorf("forge agent: parse query template: %w", err)
	}

	// 3. Generate output schema.
	schemaBytes := forgeOutputSchema(cfg.OutputField)

	// 4. Create flow.Agent with schema, model, prompts.
	agent, err := flow.NewAgent(client,
		flow.WithSchema(schemaBytes),
		flow.WithModel(flow.NewGptOss120bOllama()),
		flow.WithSystemPrompt(systemPrompt),
		flow.WithQueryTemplate(queryTmpl),
	)
	if err != nil {
		return nil, fmt.Errorf("forge agent: create agent: %w", err)
	}

	return &ForgeAgent{agent: agent, cfg: cfg}, nil
}

// Run generates content from the input and governance laws.
//
// It builds template data from the input and laws, calls the underlying
// flow.Agent.Run(), and extracts the output field from the validated JSON.
func (f *ForgeAgent) Run(ctx context.Context, input string, laws []*flowv1.Law) (string, error) {
	// 1. Build law context string.
	var lawContext strings.Builder
	if len(laws) > 0 {
		for _, law := range laws {
			fmt.Fprintf(&lawContext, "- %s\n", law.GetGoal())
		}
	}

	// 2. Build template data.
	data := forgeQueryData{
		Input: input,
		Laws:  lawContext.String(),
	}

	// 3. Call the underlying agent.
	output, err := f.agent.Run(ctx, data)
	if err != nil {
		return "", err
	}

	// 4. Extract the output field from validated JSON.
	var parsed map[string]any
	if err := json.Unmarshal(output, &parsed); err != nil {
		return "", fmt.Errorf("forge agent: unmarshal output: %w", err)
	}

	value, ok := parsed[f.cfg.OutputField]
	if !ok {
		return "", fmt.Errorf("forge agent: output field %q not found in response", f.cfg.OutputField)
	}

	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("forge agent: output field %q is not a string", f.cfg.OutputField)
	}

	return text, nil
}
