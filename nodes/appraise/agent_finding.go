package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
)

// Compile-time assertion: FindingAgent implements flow.FindingContract.
var _ flow.FindingContract = (*FindingAgent)(nil)

// ---------------------------------------------------------------------------
// FindingAgent — concrete agent for learning capture (Phase 3)
// ---------------------------------------------------------------------------

// FindingAgent wraps a flow.Agent with finding-specific schema, prompts, and
// a typed Run() interface. It distils resolved novel-argument feedback into
// governance findings.
type FindingAgent struct {
	agent *flow.Agent
	cfg   *appraiseConfig
}

// findingsOutput is the Go representation of the findingSchema-validated JSON.
type findingsOutput struct {
	Findings []findingItem `json:"findings"`
}

// findingItem is a single governance learning distilled from resolved
// novel-argument feedback.
type findingItem struct {
	Goal      string   `json:"goal"`
	AppliesTo []string `json:"applies_to"`
	Rationale string   `json:"rationale"`
}

// findingSchema validates the output of the learning-capture inference.
// The LLM distills resolved novel arguments into zero or more governance
// findings, each with a goal statement, applicable artefact kinds, and
// a rationale explaining why this learning matters.
var findingSchema = []byte(`{
	"type": "object",
	"properties": {
		"findings": {
			"type": "array",
			"items": {
				"type": "object",
				"properties": {
					"goal":       { "type": "string", "minLength": 1 },
					"applies_to": {
						"type": "array",
						"items": { "type": "string", "minLength": 1 },
						"minItems": 1
					},
					"rationale":  { "type": "string", "minLength": 1 }
				},
				"required": ["goal", "applies_to", "rationale"],
				"additionalProperties": false
			}
		}
	},
	"required": ["findings"],
	"additionalProperties": false
}`)

// findingSystemData holds config-time data for rendering the system prompt.
type findingSystemData struct {
	ReviewArtefact   string
	GovernedArtefact string
}

//nolint:lll // Prompt template — readability favors keeping the template intact.
const findingSystemPromptTemplate = `You are a governance analyst for a {{.ReviewArtefact}} review pipeline.

Your task is to capture learnings from resolved feedback discussions that involved novel arguments — new insights not covered by existing governance laws. You will produce concise, forward-looking governance statements that future reviewers and refiners can reference.`

//nolint:lll // Prompt template — readability favors keeping the template intact.
const findingQueryPromptTemplate = `The following feedback discussions have concluded. Each involved a novel argument — a new insight not covered by existing governance laws.

For each distinct learning, produce a concise, forward-looking governance statement (the "goal") that future reviewers and refiners can reference. If multiple discussions converge on the same insight, consolidate them into a single finding. If a discussion produced no reusable learning, omit it.

Each finding must specify:
- "goal": A concise governance statement (1-2 sentences). Write it as a principle or rule, not a description of what happened.
- "applies_to": Which artefact kinds this finding applies to (e.g. ["{{.GovernedArtefact}}"]). Use the GovernedArtefact kind name.
- "rationale": A brief explanation of why this learning matters, referencing the discussion that produced it. This will be preserved as the finding's initial representation.

If no discussions produced reusable learnings, return:
{"findings": []}

---

## RESOLVED DISCUSSIONS

{{.Discussions}}
## RESPONSE FORMAT

Respond with ONLY a JSON object:
{"findings": [
  {"goal": "...", "applies_to": ["{{.GovernedArtefact}}"], "rationale": "..."}
]}

Output ONLY the JSON object. No markdown fences, no explanation.`

// findingTemplateQueryData holds all fields for the finding query prompt template.
type findingTemplateQueryData struct {
	GovernedArtefact string
	Discussions      string
}

// NewFindingAgent creates a FindingAgent with the given client and config.
// The model (KimiK2Ollama) is created internally — model choice is a
// code-time decision, not deploy-time config.
//
// If cfg.FindingSystemPrompt or cfg.FindingQueryTemplate are non-empty,
// they override the baked-in defaults.
func NewFindingAgent(client *flow.Client, cfg *appraiseConfig) (*FindingAgent, error) {
	sysTmplStr := findingSystemPromptTemplate
	if cfg.FindingSystemPrompt != "" {
		sysTmplStr = cfg.FindingSystemPrompt
	}

	queryTmplStr := findingQueryPromptTemplate
	if cfg.FindingQueryTemplate != "" {
		queryTmplStr = cfg.FindingQueryTemplate
	}

	sysData := findingSystemData{
		ReviewArtefact:   cfg.ReviewArtefact,
		GovernedArtefact: cfg.GovernedArtefact,
	}

	agent, err := buildAgent(client, "finding agent",
		sysTmplStr, sysData,
		queryTmplStr, findingSchema)
	if err != nil {
		return nil, err
	}

	return &FindingAgent{agent: agent, cfg: cfg}, nil
}

// Run distils governance findings from resolved novel-argument feedback items.
//
// If items is nil or empty, it short-circuits and returns nil (no inference).
func (f *FindingAgent) Run(
	ctx context.Context,
	items []*flowv1.FeedbackItem,
) (*flow.FindingsResult, error) {
	if len(items) == 0 {
		return nil, nil
	}

	// Build discussions block.
	var discussions strings.Builder
	for i, fb := range items {
		fmt.Fprintf(&discussions, "### Discussion %d\n\n", i+1)
		fmt.Fprintf(&discussions, "**Original feedback**: %s\n", fb.GetMessage())
		fmt.Fprintf(&discussions, "**Severity**: %s\n", fb.GetSeverity().String())

		// Resolution path.
		switch fb.GetState() {
		case flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED:
			discussions.WriteString(
				"**Resolution**: Fix was accepted (the novel " +
					"argument informed the revision)\n")
		case flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX:
			discussions.WriteString(
				"**Resolution**: Refusal was accepted (the " +
					"novel argument justified not changing)\n")
		default:
			fmt.Fprintf(&discussions, "**Resolution**: Resolved (state: %s)\n",
				fb.GetState().String())
		}

		// Novel argument.
		arg := fb.GetJustification().GetNovelArgument().GetArgument()
		fmt.Fprintf(&discussions, "**Novel argument**: %s\n", arg)

		// History.
		if history := fb.GetHistory(); len(history) > 0 {
			discussions.WriteString("\n**Discussion history**:\n")
			for _, ev := range history {
				fmt.Fprintf(&discussions, "- [%s] %s: %s\n",
					ev.GetAction(), ev.GetActor(), ev.GetMessage())
			}
		}
		discussions.WriteString("\n---\n\n")
	}

	data := findingTemplateQueryData{
		GovernedArtefact: f.cfg.GovernedArtefact,
		Discussions:      discussions.String(),
	}

	raw, err := f.agent.Run(ctx, data)
	if err != nil {
		return nil, err
	}

	var out findingsOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("finding agent: unmarshal output: %w", err)
	}

	// Map internal types to contract types.
	findings := make([]flow.Finding, len(out.Findings))
	for i, f := range out.Findings {
		findings[i] = flow.Finding{
			Goal:      f.Goal,
			AppliesTo: f.AppliesTo,
			Rationale: f.Rationale,
		}
	}

	return &flow.FindingsResult{Findings: findings}, nil
}
