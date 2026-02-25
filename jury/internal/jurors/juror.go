// Package jurors defines the Juror interface and the 5 standard juror
// personality types used by the Jury deliberation engine.
//
// Each juror wraps a flow.Agent (composition, same pattern as ForgeAgent)
// with a distinct judicial philosophy encoded in its system prompt. All
// jurors share a common query template and dynamically-built output JSON
// schema derived from allowed_outcomes.
package jurors

import (
	"context"
	"encoding/json"
	"fmt"
	"text/template"

	flow "github.com/gideas/flow/sdk/go"
)

// ---------------------------------------------------------------------------
// Juror Interface
// ---------------------------------------------------------------------------

// Juror is the interface implemented by all juror personality types.
type Juror interface {
	// Run executes a single voting round with the given query data.
	Run(ctx context.Context, data JurorQueryData) (*JurorOutput, error)
	// Name returns the juror's personality type identifier.
	Name() string
}

// ---------------------------------------------------------------------------
// Shared Types
// ---------------------------------------------------------------------------

// JurorQueryData is the template data rendered into each juror's query prompt.
type JurorQueryData struct {
	// Question is the deliberation question framed by the calling node.
	Question string
	// Evidence is the structured markdown evidence bundle.
	Evidence string
	// AllowedOutcomes lists the valid vote values.
	AllowedOutcomes []string
	// PeerArguments is the anonymised reasoning from the prior round.
	// Empty on round 1 (blind voting).
	PeerArguments string
}

// JurorOutput is the structured output from a single juror vote.
type JurorOutput struct {
	// Outcome is the juror's vote — one of the allowed outcomes.
	Outcome string `json:"outcome"`
	// Reasoning is the juror's justification for their vote.
	Reasoning string `json:"reasoning"`
}

// ---------------------------------------------------------------------------
// Shared Query Template
// ---------------------------------------------------------------------------

// QueryTemplate is the shared query prompt template used by all jurors.
// It renders question, evidence, allowed outcomes, and optional peer
// arguments (for round 2+).
//
//nolint:lll // Template readability favors keeping it intact.
const queryTemplateText = `Question:
{{.Question}}

Evidence:
{{.Evidence}}

Allowed outcomes: {{range $i, $o := .AllowedOutcomes}}{{if $i}}, {{end}}{{$o}}{{end}}
{{- if .PeerArguments}}

Prior round arguments from other jurors (anonymous):
{{.PeerArguments}}

Consider these arguments carefully but form your own independent judgment.
{{- end}}

You MUST respond with a JSON object containing exactly two keys:
- "outcome": one of the allowed outcomes listed above
- "reasoning": your justification for choosing that outcome

Output ONLY the JSON object, nothing else.`

// ParseQueryTemplate parses the shared query template. Called once at juror
// construction time.
func ParseQueryTemplate() (*template.Template, error) {
	return template.New("juror-query").Parse(queryTemplateText)
}

// ---------------------------------------------------------------------------
// Dynamic Output Schema
// ---------------------------------------------------------------------------

// BuildOutputSchema generates a JSON Schema that validates juror output
// against the allowed outcomes. The "outcome" field is constrained to an
// enum of the allowed values.
func BuildOutputSchema(allowedOutcomes []string) ([]byte, error) {
	if len(allowedOutcomes) == 0 {
		return nil, fmt.Errorf("juror schema: allowed_outcomes must not be empty")
	}

	// Build enum values as []any for JSON marshalling.
	enumValues := make([]any, len(allowedOutcomes))
	for i, o := range allowedOutcomes {
		enumValues[i] = o
	}

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"outcome": map[string]any{
				"type": "string",
				"enum": enumValues,
			},
			"reasoning": map[string]any{
				"type":      "string",
				"minLength": 1,
			},
		},
		"required":             []string{"outcome", "reasoning"},
		"additionalProperties": false,
	}

	b, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("juror schema: marshal failed: %w", err)
	}
	return b, nil
}

// ---------------------------------------------------------------------------
// Base Juror (shared construction logic)
// ---------------------------------------------------------------------------

// baseJuror provides the shared implementation for all juror types.
// Each concrete juror embeds baseJuror and provides its own Name() and
// system prompt.
type baseJuror struct {
	name  string
	agent *flow.Agent
}

// Run executes the underlying flow.Agent and extracts the typed JurorOutput.
func (b *baseJuror) Run(ctx context.Context, data JurorQueryData) (*JurorOutput, error) {
	output, err := b.agent.Run(ctx, data)
	if err != nil {
		return nil, fmt.Errorf("juror %s: %w", b.name, err)
	}

	var result JurorOutput
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, fmt.Errorf("juror %s: unmarshal output: %w", b.name, err)
	}

	return &result, nil
}

// Name returns the juror's personality type identifier.
func (b *baseJuror) Name() string {
	return b.name
}

// NewBaseJuror constructs a baseJuror with the given parameters.
// This is the shared construction path for all 5 juror types.
// The model (KimiK2Ollama) is created internally — callers do not
// provide a model.
func NewBaseJuror(
	name string,
	client *flow.Client,
	systemPrompt string,
	schemaBytes []byte,
	queryTmpl *template.Template,
) (*baseJuror, error) {
	agent, err := flow.NewAgent(client,
		flow.WithSchema(schemaBytes),
		flow.WithModel(flow.NewKimiK2Ollama()),
		flow.WithSystemPrompt(systemPrompt),
		flow.WithQueryTemplate(queryTmpl),
	)
	if err != nil {
		return nil, fmt.Errorf("juror %s: create agent: %w", name, err)
	}

	return &baseJuror{name: name, agent: agent}, nil
}
