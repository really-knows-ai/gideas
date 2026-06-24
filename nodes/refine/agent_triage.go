package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
)

// Compile-time assertion: TriageAgent implements flow.TriageContract.
var _ flow.TriageContract = (*TriageAgent)(nil)

// ---------------------------------------------------------------------------
// TriageAgent — concrete agent for per-item triage (Phase 1)
// ---------------------------------------------------------------------------

// TriageAgent wraps two flow.Agent instances — one for canWontFix=false
// (action-only, no refuse option) and one for canWontFix=true (action or
// refuse with structured justification). The Run() method delegates to the
// appropriate agent based on the feedback item's canWontFix flag.
type TriageAgent struct {
	actionOnlyAgent *flow.Agent // for canWontFix=false items
	fullAgent       *flow.Agent // for canWontFix=true items
	cfg             *refineConfig
}

// triageSchemaActionOnly allows only "action" decisions — used when
// canWontFix=false. The refiner must fix the issue and cannot refuse.
var triageSchemaActionOnly = []byte(`{
	"type": "object",
	"properties": {
		"decision":           { "type": "string", "enum": ["action"] },
		"message":            { "type": "string", "minLength": 1 },
		"justification_type": { "type": "string" },
		"citation_ids":       { "type": "array", "items": { "type": "string" } },
		"argument":           { "type": "string" }
	},
	"required": ["decision", "message"],
	"additionalProperties": false
}`)

// triageSchemaFull allows both "action" and "refuse" decisions — used when
// canWontFix=true. The refiner may refuse with a structured justification.
var triageSchemaFull = []byte(`{
	"type": "object",
	"properties": {
		"decision":           { "type": "string", "enum": ["action", "refuse"] },
		"message":            { "type": "string", "minLength": 1 },
		"justification_type": { "type": "string", "enum": ["citation", "novel_argument"] },
		"citation_ids":       { "type": "array", "items": { "type": "string" } },
		"argument":           { "type": "string" }
	},
	"required": ["decision", "message"],
	"additionalProperties": false
}`)

// triageOutput is the Go representation of the triage schema-validated JSON.
type triageOutput struct {
	Decision          string   `json:"decision"`
	Message           string   `json:"message"`
	JustificationType string   `json:"justification_type"`
	CitationIDs       []string `json:"citation_ids"`
	Argument          string   `json:"argument"`
}

// triageSystemData holds config-time data for rendering the system prompt.
type triageSystemData struct {
	OutputArtefact string
}

//nolint:lll // Prompt template — readability favors keeping the template intact.
const triageSystemPromptTemplate = `You are a {{.OutputArtefact}} poet revising your work. You will fix the issue described in the feedback.`

// triageQueryActionOnly is the query prompt for canWontFix=false items.
// The refiner must fix the issue — no refuse option.
//
//nolint:lll
const triageQueryActionOnly = `You made this. You will fix this issue.

## CURRENT {{.OutputArtefactUpper}}
{{.ReviewContent}}

## FEEDBACK
{{.FeedbackMessage}}

## RESPONSE FORMAT
{"decision": "action", "message": "description of the fix I will apply"}

Output ONLY the JSON object, nothing else.`

// triageQueryFull is the query prompt for canWontFix=true items.
// The refiner may action or refuse with a structured justification.
//
//nolint:lll
const triageQueryFull = `You made this. You will fix this issue.

## CURRENT {{.OutputArtefactUpper}}
{{.ReviewContent}}

## FEEDBACK
{{.FeedbackMessage}}
{{- if .History}}

## INVESTIGATION HISTORY
{{.History}}
{{- end}}
{{- if .Laws}}

## APPLICABLE LAWS
{{.Laws}}
{{- end}}

## RESPONSE FORMAT

If actioning:
{"decision": "action", "message": "description of the fix I will apply"}

If refusing with a law citation:
{"decision": "refuse", "message": "reason for refusal", "justification_type": "citation", "citation_ids": ["law-id"]}

If refusing with a novel argument:
{"decision": "refuse", "message": "reason for refusal",
 "justification_type": "novel_argument",
 "argument": "my reasoning"}

Output ONLY the JSON object, nothing else.`

// triageTemplateQueryData holds all fields for the query prompt template.
type triageTemplateQueryData struct {
	OutputArtefact      string
	OutputArtefactUpper string
	InputContent        string
	ReviewContent       string
	FeedbackMessage     string
	History             string
	Laws                string
}

// NewTriageAgent creates a TriageAgent with the given client and config.
// It creates two flow.Agent instances:
//   - actionOnlyAgent for canWontFix=false items (action-only schema, simplified prompt)
//   - fullAgent for canWontFix=true items (action-or-refuse schema, full prompt)
//
// The model (GptOss120bOllama) is created internally by buildAgent.
// If cfg.TriageSystemPrompt is non-empty, it overrides the system prompt for
// the full agent only. If cfg.TriageQueryTemplate is non-empty, it overrides
// the query template for the full agent only.
func NewTriageAgent(client *flow.Client, cfg *refineConfig) (*TriageAgent, error) {
	sysData := triageSystemData{
		OutputArtefact: cfg.OutputArtefact,
	}

	// Resolve system prompt for full agent (respects ConfigMap override).
	fullSysTmpl := triageSystemPromptTemplate
	if cfg.TriageSystemPrompt != "" {
		fullSysTmpl = cfg.TriageSystemPrompt
	}

	// Resolve query template for full agent (respects ConfigMap override).
	fullQueryTmpl := triageQueryFull
	if cfg.TriageQueryTemplate != "" {
		fullQueryTmpl = cfg.TriageQueryTemplate
	}

	// Create action-only agent (always uses baked-in defaults — cannot refuse).
	actionOnlyAgent, err := buildAgent(client, "triage agent (action-only)",
		triageSystemPromptTemplate, sysData,
		triageQueryActionOnly, triageSchemaActionOnly)
	if err != nil {
		return nil, err
	}

	// Create full agent (may refuse with justification).
	fullAgent, err := buildAgent(client, "triage agent (full)",
		fullSysTmpl, sysData,
		fullQueryTmpl, triageSchemaFull)
	if err != nil {
		// Clean up action-only agent.
		return nil, err
	}

	return &TriageAgent{
		actionOnlyAgent: actionOnlyAgent,
		fullAgent:       fullAgent,
		cfg:             cfg,
	}, nil
}

// Run evaluates a single feedback item and returns the triage decision.
// It delegates to the action-only agent (if canWontFix=false) or the full
// agent (if canWontFix=true), selecting the appropriate prompt and schema.
func (t *TriageAgent) Run(
	ctx context.Context,
	fb *flowv1.FeedbackItem,
	inputContent, reviewContent string,
	laws []*flowv1.Law,
) (*flow.TriageResult, error) {
	canWontFix := fb.GetCanWontFix()

	if !canWontFix {
		// Action-only: simplified query, no refuse option.
		data := triageTemplateQueryData{
			OutputArtefact:      t.cfg.OutputArtefact,
			OutputArtefactUpper: strings.ToUpper(t.cfg.OutputArtefact),
			ReviewContent:       reviewContent,
			FeedbackMessage:     fb.GetMessage(),
		}

		raw, err := t.actionOnlyAgent.Run(ctx, data)
		if err != nil {
			return nil, err
		}

		var out triageOutput
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("triage agent: unmarshal output: %w", err)
		}

		return &flow.TriageResult{
			Decision: out.Decision,
			Message:  out.Message,
		}, nil
	}

	// Build history block.
	var historyBlock strings.Builder
	for _, ev := range fb.GetHistory() {
		fmt.Fprintf(&historyBlock, "- [%s] %s: %s\n",
			ev.GetAction(), ev.GetActor(), ev.GetMessage())
	}

	// Build law block.
	var lawBlock strings.Builder
	if len(laws) > 0 {
		for _, law := range laws {
			fmt.Fprintf(&lawBlock, "- [%s] (Tier %d): %s\n",
				law.GetId(), law.GetTier(), law.GetGoal())
		}
	}

	data := triageTemplateQueryData{
		OutputArtefact:      t.cfg.OutputArtefact,
		OutputArtefactUpper: strings.ToUpper(t.cfg.OutputArtefact),
		InputContent:        inputContent,
		ReviewContent:       reviewContent,
		FeedbackMessage:     fb.GetMessage(),
		History:             historyBlock.String(),
		Laws:                lawBlock.String(),
	}

	raw, err := t.fullAgent.Run(ctx, data)
	if err != nil {
		return nil, err
	}

	var out triageOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("triage agent: unmarshal output: %w", err)
	}

	return &flow.TriageResult{
		Decision:          out.Decision,
		Message:           out.Message,
		JustificationType: out.JustificationType,
		CitationIDs:       out.CitationIDs,
		Argument:          out.Argument,
	}, nil
}
