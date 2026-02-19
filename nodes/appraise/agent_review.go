package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
)

// ---------------------------------------------------------------------------
// ReviewAgent — concrete agent for fresh review (Phase 2)
// ---------------------------------------------------------------------------

// ReviewAgent wraps a flow.Agent with review-specific schema, prompts, and
// a typed Run() interface. It reviews content against governance laws and
// produces zero or more new feedback observations.
type ReviewAgent struct {
	agent *flow.Agent
	cfg   *appraiseConfig
}

// reviewOutput is the Go representation of the reviewSchema-validated JSON.
type reviewOutput struct {
	Feedback []reviewItem `json:"feedback"`
}

// reviewItem is a single feedback observation from the fresh review.
type reviewItem struct {
	Message   string   `json:"message"`
	Severity  string   `json:"severity"`
	CitedLaws []string `json:"cited_laws"`
}

// reviewSchema validates the output of fresh review inferences.
// The LLM produces zero or more feedback items, each with severity and
// optional law citations.
var reviewSchema = []byte(`{
	"type": "object",
	"properties": {
		"feedback": {
			"type": "array",
			"items": {
				"type": "object",
				"properties": {
					"message":    { "type": "string", "minLength": 1 },
					"severity":   { "type": "string", "enum": ["low", "medium", "high", "critical"] },
					"cited_laws": { "type": "array", "items": { "type": "string" } }
				},
				"required": ["message", "severity", "cited_laws"],
				"additionalProperties": false
			}
		}
	},
	"required": ["feedback"],
	"additionalProperties": false
}`)

// reviewSystemData holds config-time data for rendering the system prompt.
type reviewSystemData struct {
	ReviewArtefact string
	InputArtefact  string
}

//nolint:lll // Prompt template — readability favors keeping the template intact.
const reviewSystemPromptTemplate = `You are a {{.ReviewArtefact}} reviewer for a governed creative pipeline.

Your job is to review the {{.ReviewArtefact}} and produce NEW feedback observations. You are NOT approving or rejecting — you are producing observations. If you have no new issues, return an empty feedback array.

Every piece of feedback must either:
1. CITE one or more governance laws by ID — the {{.ReviewArtefact}} violates or insufficiently addresses the law.
2. Offer a NOVEL observation — something not covered by any law but worth improving. Use an empty cited_laws array for these.

Each feedback item must include a severity level:
- "low": Minor style or preference issue
- "medium": Quality issue that should be addressed
- "high": Functional or structural concern
- "critical": Blocking issue`

//nolint:lll // Prompt template — readability favors keeping the template intact.
const reviewQueryPromptTemplate = `## THE {{.InputArtefactUpper}}

The {{.ReviewArtefact}} was written to fulfil this creative brief:

> {{.InputContent}}

The {{.ReviewArtefact}} must faithfully address the {{.InputArtefact}}'s theme, subject, and mood.

---

## THE {{.ReviewArtefactUpper}} UNDER REVIEW

{{.ReviewContent}}

---
{{- if .Laws}}

## GOVERNANCE LAWS

The following laws are active. The {{.ReviewArtefact}} MUST comply with all of them.
If a law is violated, cite it by ID in your feedback.

{{.Laws}}
{{- end}}
{{- if .History}}

## PREVIOUS FEEDBACK HISTORY

These items have already been raised. Do NOT re-raise resolved items.
Only raise NEW observations not covered by existing feedback.

{{.History}}
{{- end}}

---

## RESPONSE FORMAT

Respond with ONLY a JSON object containing a "feedback" array.
Each item has:
- "message": a specific, actionable observation (1-2 sentences)
- "severity": one of "low", "medium", "high", "critical"
- "cited_laws": array of law IDs this feedback references (empty array if novel)

If the {{.ReviewArtefact}} is excellent and you have no NEW feedback, return:
{"feedback": []}

Examples:

No issues:
{"feedback": []}

Law violation:
{"feedback": [
  {"message": "Violates a specific governance law.",
   "severity": "medium", "cited_laws": ["{{.ExampleLawID}}"]}
]}

Novel observation:
{"feedback": [
  {"message": "The final section feels rushed.",
   "severity": "low", "cited_laws": []}
]}

Output ONLY the JSON object. No markdown fences, no explanation, no other text.`

// reviewTemplateQueryData holds all fields for the query prompt template.
type reviewTemplateQueryData struct {
	InputArtefact       string
	InputArtefactUpper  string
	ReviewArtefact      string
	ReviewArtefactUpper string
	InputContent        string
	ReviewContent       string
	Laws                string
	History             string
	ExampleLawID        string
}

// NewReviewAgent creates a ReviewAgent with the given client, model, and config.
func NewReviewAgent(client *flow.Client, model *flow.Model, cfg *appraiseConfig) (*ReviewAgent, error) {
	sysData := reviewSystemData{
		ReviewArtefact: cfg.ReviewArtefact,
		InputArtefact:  cfg.InputArtefact,
	}

	agent, err := buildAgent(client, model, "review agent",
		reviewSystemPromptTemplate, sysData,
		reviewQueryPromptTemplate, reviewSchema)
	if err != nil {
		return nil, err
	}

	return &ReviewAgent{agent: agent, cfg: cfg}, nil
}

// Run performs a fresh review and returns the review output.
func (r *ReviewAgent) Run(
	ctx context.Context,
	inputContent, reviewContent string,
	laws []*flowv1.Law,
	existingFeedback []*flowv1.FeedbackItem,
) (*reviewOutput, error) {
	// Build law block.
	var lawBlock strings.Builder
	if len(laws) > 0 {
		for _, law := range laws {
			fmt.Fprintf(&lawBlock, "- [%s] (Tier %d): %s\n",
				law.GetId(), law.GetTier(), law.GetGoal())
		}
	}

	// Build history block.
	var historyBlock strings.Builder
	if len(existingFeedback) > 0 {
		for _, fb := range existingFeedback {
			fmt.Fprintf(&historyBlock, "- [%s] %s\n",
				fb.GetState().String(), fb.GetMessage())
		}
	}

	// Pick example law ID.
	exampleLawID := "example-law-id"
	if len(laws) > 0 {
		exampleLawID = laws[0].GetId()
	}

	data := reviewTemplateQueryData{
		InputArtefact:       r.cfg.InputArtefact,
		InputArtefactUpper:  strings.ToUpper(r.cfg.InputArtefact),
		ReviewArtefact:      r.cfg.ReviewArtefact,
		ReviewArtefactUpper: strings.ToUpper(r.cfg.ReviewArtefact),
		InputContent:        inputContent,
		ReviewContent:       reviewContent,
		Laws:                lawBlock.String(),
		History:             historyBlock.String(),
		ExampleLawID:        exampleLawID,
	}

	raw, err := r.agent.Run(ctx, data)
	if err != nil {
		return nil, err
	}

	var out reviewOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("review agent: unmarshal output: %w", err)
	}

	return &out, nil
}
