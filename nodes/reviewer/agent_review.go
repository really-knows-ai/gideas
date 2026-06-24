package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	"github.com/gideas/flow/nodes/internal/artefacts"
	flow "github.com/gideas/flow/sdk/go"
)

// Compile-time assertion: ReviewAgent implements flow.ReviewContract.
var _ flow.ReviewContract = (*ReviewAgent)(nil)

// ---------------------------------------------------------------------------
// ReviewAgent — concrete agent for fresh review (Phase 2)
// ---------------------------------------------------------------------------

// ReviewAgent wraps a flow.Agent with review-specific schema, prompts, and
// a typed Run() interface. It reviews content against governance laws and
// produces zero or more new feedback observations.
type ReviewAgent struct {
	agent *flow.Agent
	cfg   *reviewerConfig
}

// reviewRawOutput is the Go representation of the reviewSchema-validated JSON.
// It maps the snake_case JSON wire format to the SDK's ReviewResult/ReviewFeedback
// types.
type reviewRawOutput struct {
	Feedback []reviewRawItem `json:"feedback"`
}

// reviewRawItem is a single feedback observation from the fresh review in
// wire-format (snake_case).
type reviewRawItem struct {
	Message   string   `json:"message"`
	CitedLaws []string `json:"cited_laws"`
}

// reviewSchema validates the output of fresh review inferences.
// The LLM produces zero or more feedback items, each with a message
// and optional law citations.
var reviewSchema = []byte(`{
	"type": "object",
	"properties": {
		"feedback": {
			"type": "array",
			"items": {
				"type": "object",
				"properties": {
					"message":    { "type": "string", "minLength": 1 },
					"cited_laws": { "type": "array", "items": { "type": "string" } }
				},
				"required": ["message", "cited_laws"],
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
	DivisionSuffix string
}

//nolint:lll // Prompt template — readability favors keeping the template intact.
const reviewSystemPromptTemplate = `You are a {{.ReviewArtefact}} reviewer for a governed creative pipeline.

Your job is to review the {{.ReviewArtefact}} and produce NEW feedback observations. You are NOT approving or rejecting — you are producing observations. If you have no new issues, return an empty feedback array.

Feedback states a deviation from a required standard. Every observation must identify what is being deviated from:
- Law violation: the artefact breaches a governance law. Cite the law by ID.
- Prompt deviation: the artefact strays from the creative brief (theme, subject, mood, or explicit instructions).
- Both: the deviation violates both a law and the creative brief.
{{- if .DivisionSuffix}}

{{.DivisionSuffix}}
{{- end}}`

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
- "message": a specific, actionable observation stating the deviation (1-2 sentences)
- "cited_laws": array of law IDs this feedback references (empty array if prompt-only deviation)

If the {{.ReviewArtefact}} is excellent and you have no NEW feedback, return:
{"feedback": []}

Examples:

No issues:
{"feedback": []}

Law violation:
{"feedback": [
  {"message": "Violates a specific governance law.",
   "cited_laws": ["{{.ExampleLawID}}"]}
]}

Prompt deviation:
{"feedback": [
  {"message": "The final section is about penguins but the prompt requested cats.",
   "cited_laws": []}
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

// ReviewAgentOpts holds optional overrides for ReviewAgent construction.
// Zero values mean "use baked-in defaults".
type ReviewAgentOpts struct {
	SystemPrompt  string // override the default system prompt template
	QueryTemplate string // override the default query prompt template
}

// NewReviewAgent creates a ReviewAgent with the given client, config, and
// optional division prompt suffix. The model (KimiK2Ollama) is created
// internally — model choice is a code-time decision, not deploy-time config.
//
// opts may be nil to use all baked-in defaults.
func NewReviewAgent(
	client *flow.Client, cfg *reviewerConfig,
	divisionPromptSuffix string, opts *ReviewAgentOpts,
) (*ReviewAgent, error) {
	inputLabel := artefacts.InputLabel(cfg.InputArtefacts)

	// Resolve system prompt template (override or default).
	systemTmplSrc := reviewSystemPromptTemplate
	if opts != nil && opts.SystemPrompt != "" {
		systemTmplSrc = opts.SystemPrompt
	}

	sysData := reviewSystemData{
		ReviewArtefact: cfg.ReviewArtefact,
		InputArtefact:  inputLabel,
		DivisionSuffix: divisionPromptSuffix,
	}

	// 1. Render system prompt with config + division suffix.
	sysTmpl, err := template.New("system").Parse(systemTmplSrc)
	if err != nil {
		return nil, fmt.Errorf("review agent: parse system template: %w", err)
	}

	var sysBuf bytes.Buffer
	if err := sysTmpl.Execute(&sysBuf, sysData); err != nil {
		return nil, fmt.Errorf("review agent: render system prompt: %w", err)
	}

	// Resolve query prompt template (override or default).
	queryTmplSrc := reviewQueryPromptTemplate
	if opts != nil && opts.QueryTemplate != "" {
		queryTmplSrc = opts.QueryTemplate
	}

	// 2. Parse query template.
	queryTmpl, err := template.New("query").Parse(queryTmplSrc)
	if err != nil {
		return nil, fmt.Errorf("review agent: parse query template: %w", err)
	}

	// 3. Create flow.Agent with schema, model, prompts.
	agent, err := flow.NewAgent(client,
		flow.WithSchema(reviewSchema),
		flow.WithModel(flow.NewKimiK2Ollama()),
		flow.WithSystemPrompt(sysBuf.String()),
		flow.WithQueryTemplate(queryTmpl),
	)
	if err != nil {
		return nil, fmt.Errorf("review agent: create agent: %w", err)
	}

	return &ReviewAgent{agent: agent, cfg: cfg}, nil
}

// Run performs a fresh review and returns the review result.
func (r *ReviewAgent) Run(
	ctx context.Context,
	inputContent, reviewContent string,
	laws []flow.ReviewLaw,
	history []flow.ReviewHistory,
) (*flow.ReviewResult, error) {
	// Build law block.
	var lawBlock strings.Builder
	if len(laws) > 0 {
		for _, law := range laws {
			fmt.Fprintf(&lawBlock, "- [%s] (Tier %d): %s\n",
				law.ID, law.Tier, law.Goal)
		}
	}

	// Build history block.
	var historyBlock strings.Builder
	if len(history) > 0 {
		for _, h := range history {
			fmt.Fprintf(&historyBlock, "- [%s] %s\n", h.State, h.Message)
		}
	}

	// Pick example law ID.
	exampleLawID := "example-law-id"
	if len(laws) > 0 {
		exampleLawID = laws[0].ID
	}

	inputLabel := artefacts.InputLabel(r.cfg.InputArtefacts)

	data := reviewTemplateQueryData{
		InputArtefact:       inputLabel,
		InputArtefactUpper:  strings.ToUpper(inputLabel),
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

	var rawOut reviewRawOutput
	if err := json.Unmarshal(raw, &rawOut); err != nil {
		return nil, fmt.Errorf("review agent: unmarshal output: %w", err)
	}

	// Map wire-format items to SDK types.
	result := &flow.ReviewResult{
		Feedback: make([]flow.ReviewFeedback, len(rawOut.Feedback)),
	}
	for i, item := range rawOut.Feedback {
		result.Feedback[i] = flow.ReviewFeedback{
			Message:   item.Message,
			CitedLaws: item.CitedLaws,
		}
	}

	return result, nil
}
