package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/artefacts"
	flow "github.com/gideas/flow/sdk/go"
)

// ---------------------------------------------------------------------------
// EvalAgent — concrete agent for fix/refusal evaluation (Phase 1)
// ---------------------------------------------------------------------------

// EvalAgent wraps a flow.Agent with eval-specific schema, prompts, and
// a typed Run() interface. It evaluates whether a fix or refusal to a
// feedback item is adequate.
type EvalAgent struct {
	agent *flow.Agent
	cfg   *appraiseConfig
}

// evalOutput is the Go representation of the evalSchema-validated JSON.
type evalOutput struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
}

// evalSchema validates the output of fix/refusal evaluation inferences.
// Each evaluation produces a verdict (accept/reject) and a reason.
var evalSchema = []byte(`{
	"type": "object",
	"properties": {
		"verdict": { "type": "string", "enum": ["accept", "reject"] },
		"reason":  { "type": "string", "minLength": 1 }
	},
	"required": ["verdict", "reason"],
	"additionalProperties": false
}`)

//nolint:lll // Prompt template — readability favors keeping the template intact.
const evalSystemPromptTemplate = `You are a {{.ReviewArtefact}} reviewer evaluating a previous feedback item.

You will be given context about the original {{.InputArtefact}} and the current {{.ReviewArtefact}}, along with the feedback item and its investigation history. Your job is to decide whether the response to the feedback is adequate.`

const evalQueryPromptTemplate = `## CONTEXT

The {{.ReviewArtefact}} was written to fulfil this {{.InputArtefact}}:
> {{.InputContent}}

The current {{.ReviewArtefact}}:
{{.ReviewContent}}

## ORIGINAL FEEDBACK

Message: {{.FeedbackMessage}}
Severity: {{.FeedbackSeverity}}

## INVESTIGATION HISTORY

{{.History}}{{.Justification}}

## YOUR TASK

{{.KindInstruction}}

## RESPONSE FORMAT

Respond with ONLY a JSON object:
{"verdict": "accept", "reason": "brief explanation"}
or
{"verdict": "reject", "reason": "brief explanation of why this is inadequate"}

Output ONLY the JSON object, nothing else.`

// evalSystemData holds config-time data for rendering the system prompt.
type evalSystemData struct {
	ReviewArtefact string
	InputArtefact  string
}

// evalTemplateQueryData extends evalQueryData with config fields for template rendering.
type evalTemplateQueryData struct {
	ReviewArtefact   string
	InputArtefact    string
	InputContent     string
	ReviewContent    string
	FeedbackMessage  string
	FeedbackSeverity string
	History          string
	Justification    string
	KindInstruction  string
}

// NewEvalAgent creates an EvalAgent with the given client and config.
// The model (KimiK2Ollama) is created internally — model choice is a
// code-time decision, not deploy-time config.
func NewEvalAgent(client *flow.Client, cfg *appraiseConfig) (*EvalAgent, error) {
	inputLabel := artefacts.InputLabel(cfg.InputArtefacts)

	sysData := evalSystemData{
		ReviewArtefact: cfg.ReviewArtefact,
		InputArtefact:  inputLabel,
	}

	agent, err := buildAgent(client, "eval agent",
		evalSystemPromptTemplate, sysData,
		evalQueryPromptTemplate, evalSchema)
	if err != nil {
		return nil, err
	}

	return &EvalAgent{agent: agent, cfg: cfg}, nil
}

// Run evaluates a single feedback item and returns the verdict.
//
// The kind parameter must be "actioned" or "wont_fix", determining which
// evaluation instructions the LLM receives.
func (e *EvalAgent) Run(
	ctx context.Context,
	fb *flowv1.FeedbackItem,
	inputContent, reviewContent, kind string,
) (*evalOutput, error) {
	// Build history block.
	var historyBlock strings.Builder
	for _, ev := range fb.GetHistory() {
		fmt.Fprintf(&historyBlock, "- [%s] %s: %s\n",
			ev.GetAction(), ev.GetActor(), ev.GetMessage())
	}

	// Build justification block.
	var justificationBlock string
	if j := fb.GetJustification(); j != nil {
		switch {
		case j.GetCitation() != nil:
			justificationBlock = fmt.Sprintf(
				"\n## REFINER'S JUSTIFICATION (citation)\n\nCited laws: %v\n",
				j.GetCitation().GetCitationIds())
		case j.GetNovelArgument() != nil:
			justificationBlock = fmt.Sprintf(
				"\n## REFINER'S JUSTIFICATION (novel argument)\n\n%s\n",
				j.GetNovelArgument().GetArgument())
		}
	}

	// Build kind instruction.
	var kindInstruction string
	switch kind {
	case "actioned":
		kindInstruction = fmt.Sprintf(`The refining node claims to have FIXED this issue.
Your job: decide if the fix is adequate given the current %s.
- "accept" means the fix sufficiently addresses the original feedback.
- "reject" means the fix is incomplete or misguided — explain why.`, e.cfg.ReviewArtefact)
	case "wont_fix":
		kindInstruction = `The refining node REFUSED to fix this issue and provided a justification.
Your job: decide if the refusal is justified.
- "accept" means the justification is reasonable — the feedback can be resolved.
- "reject" means the refusal is unjustified — explain why it should be addressed.`
	}

	data := evalTemplateQueryData{
		ReviewArtefact:   e.cfg.ReviewArtefact,
		InputArtefact:    artefacts.InputLabel(e.cfg.InputArtefacts),
		InputContent:     inputContent,
		ReviewContent:    reviewContent,
		FeedbackMessage:  fb.GetMessage(),
		FeedbackSeverity: fb.GetSeverity().String(),
		History:          historyBlock.String(),
		Justification:    justificationBlock,
		KindInstruction:  kindInstruction,
	}

	raw, err := e.agent.Run(ctx, data)
	if err != nil {
		return nil, err
	}

	var out evalOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("eval agent: unmarshal output: %w", err)
	}

	return &out, nil
}
