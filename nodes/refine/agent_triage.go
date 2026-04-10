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

// TriageAgent wraps a flow.Agent with triage-specific schema, prompts, and
// a typed Run() interface. It evaluates each feedback item to decide whether
// to action (fix) or refuse (won't fix) the issue.
type TriageAgent struct {
	agent *flow.Agent
	cfg   *refineConfig
}

// triageOutput is the Go representation of the triageSchema-validated JSON.
type triageOutput struct {
	Decision          string   `json:"decision"`
	Message           string   `json:"message"`
	JustificationType string   `json:"justification_type"`
	CitationIDs       []string `json:"citation_ids"`
	Argument          string   `json:"argument"`
}

// triageSchema validates the output of per-item triage inferences.
// The LLM decides whether to action or refuse each feedback item.
// Justification fields are only relevant when decision is "refuse" — the
// Go code validates this business rule after unmarshalling.
var triageSchema = []byte(`{
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

// triageSystemData holds config-time data for rendering the system prompt.
type triageSystemData struct {
	OutputArtefact string
}

//nolint:lll // Prompt template — readability favors keeping the template intact.
const triageSystemPromptTemplate = `You are a {{.OutputArtefact}} poet deciding how to handle feedback on your work.

You will be given context about the original request and the current {{.OutputArtefact}}, along with a feedback item and its investigation history. Your job is to decide whether to action (fix) or refuse (won't fix) the feedback.

If you refuse, you must provide a structured justification:
- Set "justification_type" to "citation" and provide "citation_ids" (array of law IDs) if existing governance supports your position.
- Set "justification_type" to "novel_argument" and provide "argument" (free-text reasoning) if your position is based on new reasoning.`

// triageQueryPromptTemplate is the query prompt template, rendered per Run()
// call with runtime data (input content, review content, feedback, laws).
const triageQueryPromptTemplate = `## CONTEXT

Your {{.OutputArtefact}} was written to fulfil this petition:
> {{.InputContent}}

Your current {{.OutputArtefact}}:
{{.ReviewContent}}

## FEEDBACK ITEM

Message: {{.FeedbackMessage}}
Severity: {{.FeedbackSeverity}}

## INVESTIGATION HISTORY

{{.History}}
{{- if .Laws}}

## APPLICABLE LAWS

{{.Laws}}
You may cite law IDs if refusing based on existing governance.
{{- end}}

## YOUR TASK

Decide how to handle this feedback:

1. "action" — You agree the feedback has merit and will fix the issue in
   your revision. Provide a message describing the fix you will apply.

2. "refuse" — You believe the feedback is wrong, subjective, or contradicts
   governance. You must provide a structured justification:
   - Set "justification_type" to "citation" and provide "citation_ids"
     (array of law IDs) if existing governance supports your position.
   - Set "justification_type" to "novel_argument" and provide "argument"
     (free-text reasoning) if your position is based on new reasoning.

## RESPONSE FORMAT

Respond with ONLY a JSON object.

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
	OutputArtefact   string
	InputContent     string
	ReviewContent    string
	FeedbackMessage  string
	FeedbackSeverity string
	History          string
	Laws             string
}

// NewTriageAgent creates a TriageAgent with the given client and config.
// The model (GptOss120bOllama) is created internally by buildAgent.
// If cfg provides non-empty TriageSystemPrompt or TriageQueryTemplate
// overrides, those replace the baked-in defaults.
func NewTriageAgent(client *flow.Client, cfg *refineConfig) (*TriageAgent, error) {
	sysData := triageSystemData{
		OutputArtefact: cfg.OutputArtefact,
	}

	sysTmpl := triageSystemPromptTemplate
	if cfg.TriageSystemPrompt != "" {
		sysTmpl = cfg.TriageSystemPrompt
	}

	queryTmpl := triageQueryPromptTemplate
	if cfg.TriageQueryTemplate != "" {
		queryTmpl = cfg.TriageQueryTemplate
	}

	agent, err := buildAgent(client, "triage agent",
		sysTmpl, sysData,
		queryTmpl, triageSchema)
	if err != nil {
		return nil, err
	}

	return &TriageAgent{agent: agent, cfg: cfg}, nil
}

// Run evaluates a single feedback item and returns the triage decision.
func (t *TriageAgent) Run(
	ctx context.Context,
	fb *flowv1.FeedbackItem,
	inputContent, reviewContent string,
	laws []*flowv1.Law,
) (*flow.TriageResult, error) {
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
		OutputArtefact:   t.cfg.OutputArtefact,
		InputContent:     inputContent,
		ReviewContent:    reviewContent,
		FeedbackMessage:  fb.GetMessage(),
		FeedbackSeverity: fb.GetSeverity().String(),
		History:          historyBlock.String(),
		Laws:             lawBlock.String(),
	}

	raw, err := t.agent.Run(ctx, data)
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
