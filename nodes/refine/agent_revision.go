package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
)

// Compile-time assertion: RevisionAgent implements flow.RevisionContract.
var _ flow.RevisionContract = (*RevisionAgent)(nil)

// ---------------------------------------------------------------------------
// RevisionAgent — concrete agent for content revision (Phase 2)
// ---------------------------------------------------------------------------

// RevisionAgent wraps a flow.Agent with revision-specific schema, prompts,
// and a typed Run() interface. It produces a revised version of the content
// that addresses all actioned feedback items.
type RevisionAgent struct {
	agent *flow.Agent
	cfg   *refineConfig
}

// revisionOutputSchema generates a JSON Schema requiring an object with a
// single string field (named by outputField), minimum length 1, no additional
// properties. Same pattern as forge's forgeOutputSchema.
func revisionOutputSchema(outputField string) []byte {
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

// revisionSystemData holds config-time data for rendering the system prompt.
type revisionSystemData struct {
	OutputArtefact string
	OutputField    string
}

//nolint:lll // Prompt template — readability favors keeping the template intact.
const revisionSystemPromptTemplate = `You are a {{.OutputArtefact}} poet revising your work. You must address the committed fixes while staying true to the original request.

You MUST respond with a JSON object containing a single key "{{.OutputField}}" whose value is the revised content.

IMPORTANT: Output ONLY the JSON object, nothing else.`

// revisionQueryPromptTemplate is the query prompt template, rendered per
// Run() call with runtime data (input content, review content, laws, fixes).
const revisionQueryPromptTemplate = `## THE PETITION

> {{.InputContent}}

## CURRENT {{.OutputArtefactUpper}}

{{.ReviewContent}}
{{- if .Laws}}

## GOVERNANCE LAWS

Your revised {{.OutputArtefact}} must comply with all active governance laws.

{{.Laws}}
{{- end}}

## FIXES TO APPLY

You have committed to the following fixes. Your revised {{.OutputArtefact}} MUST address each one.

{{.Fixes}}

## INSTRUCTIONS

Write a revised {{.OutputArtefact}} (three lines: EXACTLY 5 syllables, 7 syllables,
5 syllables) that addresses all committed fixes while remaining faithful
to the petition. Count syllables carefully.

## RESPONSE FORMAT

Respond with ONLY a JSON object:
{"{{.OutputField}}": "line one\nline two\nline three"}

Output ONLY the JSON object, nothing else.`

// revisionTemplateQueryData holds all fields for the query prompt template.
type revisionTemplateQueryData struct {
	OutputArtefact      string
	OutputArtefactUpper string
	OutputField         string
	InputContent        string
	ReviewContent       string
	Laws                string
	Fixes               string
}

// NewRevisionAgent creates a RevisionAgent with the given client and config.
// The model (GptOss120bOllama) is created internally by buildAgent.
// If cfg provides non-empty RevisionSystemPrompt or RevisionQueryTemplate
// overrides, those replace the baked-in defaults.
func NewRevisionAgent(client *flow.Client, cfg *refineConfig) (*RevisionAgent, error) {
	sysData := revisionSystemData{
		OutputArtefact: cfg.OutputArtefact,
		OutputField:    cfg.OutputField,
	}

	sysTmpl := revisionSystemPromptTemplate
	if cfg.RevisionSystemPrompt != "" {
		sysTmpl = cfg.RevisionSystemPrompt
	}

	queryTmpl := revisionQueryPromptTemplate
	if cfg.RevisionQueryTemplate != "" {
		queryTmpl = cfg.RevisionQueryTemplate
	}

	schema := revisionOutputSchema(cfg.OutputField)

	agent, err := buildAgent(client, "revision agent",
		sysTmpl, sysData,
		queryTmpl, schema)
	if err != nil {
		return nil, err
	}

	return &RevisionAgent{agent: agent, cfg: cfg}, nil
}

// Run produces revised content that addresses all actioned feedback items.
// Returns the revised content string extracted from the output field.
func (r *RevisionAgent) Run(
	ctx context.Context,
	inputContent, reviewContent string,
	laws []*flowv1.Law,
	fixes []flow.ActionedFeedback,
) (string, error) {
	// Build law block.
	var lawBlock strings.Builder
	if len(laws) > 0 {
		for _, law := range laws {
			fmt.Fprintf(&lawBlock, "- [%s] (Tier %d): %s\n",
				law.GetId(), law.GetTier(), law.GetGoal())
		}
	}

	// Build fix block.
	var fixBlock strings.Builder
	for _, a := range fixes {
		fmt.Fprintf(&fixBlock, "- Feedback: %s\n  Fix: %s\n\n", a.Message, a.FixDescription)
	}

	data := revisionTemplateQueryData{
		OutputArtefact:      r.cfg.OutputArtefact,
		OutputArtefactUpper: strings.ToUpper(r.cfg.OutputArtefact),
		OutputField:         r.cfg.OutputField,
		InputContent:        inputContent,
		ReviewContent:       reviewContent,
		Laws:                lawBlock.String(),
		Fixes:               fixBlock.String(),
	}

	raw, err := r.agent.Run(ctx, data)
	if err != nil {
		return "", err
	}

	// Extract the output field from validated JSON.
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("revision agent: unmarshal output: %w", err)
	}

	value, ok := parsed[r.cfg.OutputField]
	if !ok {
		return "", fmt.Errorf("revision agent: output field %q not found in response", r.cfg.OutputField)
	}

	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("revision agent: output field %q is not a string", r.cfg.OutputField)
	}

	return text, nil
}
