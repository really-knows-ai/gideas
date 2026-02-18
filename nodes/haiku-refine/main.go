// Refine is the revision node of the Haiku Foundry Cycle.
//
// It reads the "petition" (creative brief), the current "haiku", applicable
// governance laws, and any unresolved feedback, then uses an LLM
// (gpt-oss:120b-cloud via Ollama) to decide how to handle each item and
// produce a revised haiku.
//
// Refine operates in two phases:
//
//  1. Per-Item Triage — For each NEW or REJECTED feedback item, a separate
//     FoundryAgent inference call decides whether to action (fix) or refuse
//     (won't fix) the item. These run in parallel, each with managed heartbeat
//     and cost telemetry. Refusals require a structured justification (law
//     citation or novel argument). If a REJECTED item has a linked ruling
//     (contempt guard), it is force-actioned without LLM inference.
//
//  2. Revision — A single FoundryAgent inference call takes the petition,
//     current haiku, applicable laws, and the actioned items from Phase 1
//     to produce a revised haiku addressing all committed fixes.
//
// If Phase 1 produces no actioned items (all feedback refused), Phase 2 is
// skipped — the existing haiku is stored unchanged and routed back to Sort.
//
// Always routes back to Sort for governance triage of the new version.
//
// Environment:
//
//	OLLAMA_BASE_URL   — Ollama API endpoint (default: http://localhost:11434)
//	REFINE_MODEL      — Model name (default: gpt-oss:120b-cloud)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/ollama"
	flow "github.com/gideas/flow/sdk/go"
)

const (
	defaultModel = "gpt-oss:120b-cloud"
	envModel     = "REFINE_MODEL"

	decisionAction = "action"
	decisionRefuse = "refuse"

	justTypeCitation      = "citation"
	justTypeNovelArgument = "novel_argument"

	contemptMessage = "Complying with judicial ruling"
)

// ---------------------------------------------------------------------------
// JSON Schemas — FoundryAgent validates LLM output against these
// ---------------------------------------------------------------------------

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

// haikuSchema validates the output of the revision inference.
// The LLM produces a revised haiku in structured JSON.
var haikuSchema = []byte(`{
	"type": "object",
	"properties": {
		"haiku": { "type": "string", "minLength": 1 }
	},
	"required": ["haiku"],
	"additionalProperties": false
}`)

// ---------------------------------------------------------------------------
// Output Types — Go representation of the schema-validated JSON outputs
// ---------------------------------------------------------------------------

// triageOutput is the Go representation of the triageSchema-validated JSON.
type triageOutput struct {
	Decision          string   `json:"decision"`
	Message           string   `json:"message"`
	JustificationType string   `json:"justification_type"`
	CitationIDs       []string `json:"citation_ids"`
	Argument          string   `json:"argument"`
}

// haikuOutput is the Go representation of the haikuSchema-validated JSON.
type haikuOutput struct {
	Haiku string `json:"haiku"`
}

// ---------------------------------------------------------------------------
// Entrypoint
// ---------------------------------------------------------------------------

func main() {
	slog.Info("haiku-refine: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("haiku-refine: server failed", "error", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("haiku-refine: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("refine: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	// ---------------------------------------------------------------
	// Pre-inference: read artefacts, query laws, get existing feedback
	// ---------------------------------------------------------------

	petitionResp, err := client.GetArtefact(ctx, "petition")
	if err != nil {
		return fmt.Errorf("refine: read petition: %w", err)
	}
	petition := string(petitionResp.GetContent())

	haikuResp, err := client.GetArtefact(ctx, "haiku")
	if err != nil {
		return fmt.Errorf("refine: read haiku: %w", err)
	}
	haiku := string(haikuResp.GetContent())

	slog.Info("haiku-refine: context",
		"petition", petition,
		"current_haiku", haiku,
	)

	laws, _ := client.QueryLaws(ctx, "text/haiku", "")

	feedbackItems, err := client.GetFeedback(ctx, "haiku")
	if err != nil {
		return fmt.Errorf("refine: get feedback: %w", err)
	}

	// Resolve the model name.
	model := os.Getenv(envModel)
	if model == "" {
		model = defaultModel
	}

	inferFn := makeInferFunc(model)

	// ---------------------------------------------------------------
	// Phase 1: Per-item triage (parallel)
	// ---------------------------------------------------------------

	triageAgent, err := flow.NewAgent(client, triageSchema)
	if err != nil {
		return fmt.Errorf("refine: create triage agent: %w", err)
	}

	actionedItems, err := triageFeedback(ctx, triageAgent, inferFn, client, feedbackItems, petition, haiku, laws)
	if err != nil {
		return fmt.Errorf("refine: triage feedback: %w", err)
	}

	// ---------------------------------------------------------------
	// Phase 2: Revision — produce a revised haiku addressing actioned items
	// ---------------------------------------------------------------

	var revised string
	if len(actionedItems) > 0 {
		revisionAgent, err := flow.NewAgent(client, haikuSchema)
		if err != nil {
			return fmt.Errorf("refine: create revision agent: %w", err)
		}

		revisionPrompt := buildRevisionPrompt(petition, haiku, laws, actionedItems)
		revisionOut, err := revisionAgent.Run(ctx, inferFn, []byte(revisionPrompt))
		if err != nil {
			return fmt.Errorf("refine: revision run: %w", err)
		}

		var parsed haikuOutput
		if err := json.Unmarshal(revisionOut, &parsed); err != nil {
			return fmt.Errorf("refine: unmarshal revision: %w", err)
		}
		revised = parsed.Haiku
		slog.Info("haiku-refine: revised haiku", "haiku", revised)
	} else {
		// All feedback refused — store the existing haiku unchanged.
		revised = haiku
		slog.Info("haiku-refine: no actioned items — haiku unchanged")
	}

	// ---------------------------------------------------------------
	// Post-inference: store revised haiku and route back to Sort
	// ---------------------------------------------------------------

	storeResp, err := client.StoreArtefact(ctx, "haiku", "text/haiku", []byte(revised))
	if err != nil {
		return fmt.Errorf("refine: store revised haiku: %w", err)
	}
	slog.Info("haiku-refine: stored revised haiku",
		"version_hash", storeResp.GetVersionHash(),
		"is_new_version", storeResp.GetIsNewVersion(),
	)

	if _, err := client.RouteToOutput(ctx, "default"); err != nil {
		return fmt.Errorf("refine: route to sort: %w", err)
	}

	slog.Info("haiku-refine: routed to sort", "workitem_id", wctx.GetWorkitemId())
	return nil
}

// ---------------------------------------------------------------------------
// Phase 1: Parallel Per-Item Triage
// ---------------------------------------------------------------------------

// actionedItem records a feedback item that Phase 1 decided to fix.
type actionedItem struct {
	FeedbackID string
	Message    string // the original feedback message
	FixDesc    string // what the LLM promised to fix
}

// triageFeedback runs parallel LLM triage for NEW and REJECTED feedback items.
// Each item gets a focused inference call that decides action or refuse.
// Returns the list of items that were actioned (for Phase 2 context).
func triageFeedback(
	ctx context.Context,
	agent *flow.Agent,
	inferFn flow.InferFunc,
	client *flow.Client,
	feedback []*flowv1.FeedbackItem,
	petition, haiku string,
	laws []*flowv1.Law,
) ([]actionedItem, error) {
	type triageTask struct {
		item          *flowv1.FeedbackItem
		prompt        string
		forceActioned bool // contempt guard — skip LLM
	}

	var tasks []triageTask
	for _, fb := range feedback {
		state := fb.GetState()
		if state != flowv1.FeedbackState_FEEDBACK_STATE_NEW &&
			state != flowv1.FeedbackState_FEEDBACK_STATE_REJECTED {
			continue
		}

		// Contempt guard: linked ruling on a REJECTED item forces action.
		if fb.GetLinkedRuling() != "" && state == flowv1.FeedbackState_FEEDBACK_STATE_REJECTED {
			tasks = append(tasks, triageTask{
				item:          fb,
				forceActioned: true,
			})
			continue
		}

		tasks = append(tasks, triageTask{
			item:   fb,
			prompt: buildTriagePrompt(fb, petition, haiku, laws),
		})
	}

	if len(tasks) == 0 {
		slog.Info("haiku-refine: no feedback items to triage")
		return nil, nil
	}

	slog.Info("haiku-refine: triaging feedback items", "count", len(tasks))

	type triageResult struct {
		task triageTask
		out  triageOutput
		err  error
	}

	results := make([]triageResult, len(tasks))
	var wg sync.WaitGroup
	for i, task := range tasks {
		if task.forceActioned {
			results[i] = triageResult{
				task: task,
				out: triageOutput{
					Decision: decisionAction,
					Message:  contemptMessage,
				},
			}
			continue
		}

		wg.Add(1)
		go func(idx int, t triageTask) {
			defer wg.Done()
			raw, err := agent.Run(ctx, inferFn, []byte(t.prompt))
			if err != nil {
				results[idx] = triageResult{task: t, err: err}
				return
			}
			var out triageOutput
			if err := json.Unmarshal(raw, &out); err != nil {
				results[idx] = triageResult{task: t, err: fmt.Errorf("unmarshal triage: %w", err)}
				return
			}
			results[idx] = triageResult{task: t, out: out}
		}(i, task)
	}
	wg.Wait()

	// Apply decisions sequentially (gRPC calls to Archivist).
	var actioned []actionedItem
	for _, r := range results {
		if r.err != nil {
			return nil, fmt.Errorf("refine: triage feedback %s: %w", r.task.item.GetId(), r.err)
		}

		fbID := r.task.item.GetId()

		switch r.out.Decision {
		case decisionAction:
			slog.Info("haiku-refine: actioning feedback",
				"feedback_id", fbID, "message", r.out.Message)
			if err := client.ResolveFeedback(ctx, fbID, r.out.Message); err != nil {
				return nil, fmt.Errorf("refine: resolve feedback %s: %w", fbID, err)
			}
			actioned = append(actioned, actionedItem{
				FeedbackID: fbID,
				Message:    r.task.item.GetMessage(),
				FixDesc:    r.out.Message,
			})

		case decisionRefuse:
			justification, err := buildJustification(r.out)
			if err != nil {
				return nil, fmt.Errorf("refine: build justification for %s: %w", fbID, err)
			}
			slog.Info("haiku-refine: refusing feedback",
				"feedback_id", fbID,
				"justification_type", r.out.JustificationType,
				"message", r.out.Message)
			if err := client.RefuseFeedback(ctx, fbID, justification); err != nil {
				return nil, fmt.Errorf("refine: refuse feedback %s: %w", fbID, err)
			}

		default:
			return nil, fmt.Errorf("refine: unexpected decision %q for feedback %s", r.out.Decision, fbID)
		}
	}

	return actioned, nil
}

// buildJustification converts the LLM's triage output into a proto
// Justification for the RefuseFeedback call.
func buildJustification(out triageOutput) (*flowv1.Justification, error) {
	switch out.JustificationType {
	case justTypeCitation:
		if len(out.CitationIDs) == 0 {
			return nil, fmt.Errorf("citation justification requires at least one citation_id")
		}
		return &flowv1.Justification{
			Kind: &flowv1.Justification_Citation{
				Citation: &flowv1.Citation{CitationIds: out.CitationIDs},
			},
		}, nil

	case justTypeNovelArgument:
		if out.Argument == "" {
			return nil, fmt.Errorf("novel_argument justification requires a non-empty argument")
		}
		return &flowv1.Justification{
			Kind: &flowv1.Justification_NovelArgument{
				NovelArgument: &flowv1.NovelArgument{Argument: out.Argument},
			},
		}, nil

	default:
		return nil, fmt.Errorf("refuse decision requires justification_type (citation or novel_argument), got %q",
			out.JustificationType)
	}
}

// ---------------------------------------------------------------------------
// Prompt Builders
// ---------------------------------------------------------------------------

// buildTriagePrompt creates a focused triage prompt for a single feedback
// item. The LLM decides whether to action (fix) or refuse (won't fix).
func buildTriagePrompt(
	fb *flowv1.FeedbackItem,
	petition, haiku string,
	laws []*flowv1.Law,
) string {
	var historyBlock strings.Builder
	for _, ev := range fb.GetHistory() {
		historyBlock.WriteString(fmt.Sprintf("- [%s] %s: %s\n", ev.GetAction(), ev.GetActor(), ev.GetMessage()))
	}

	var lawBlock string
	if len(laws) > 0 {
		lawBlock = "\n## APPLICABLE LAWS\n\n"
		for _, law := range laws {
			lawBlock += fmt.Sprintf("- [%s] (Tier %d): %s\n", law.GetId(), law.GetTier(), law.GetGoal())
		}
		lawBlock += "\nYou may cite law IDs if refusing based on existing governance.\n"
	}

	return fmt.Sprintf(`You are a haiku poet deciding how to handle feedback on your work.

## CONTEXT

Your haiku was written to fulfil this petition:
> %s

Your current haiku:
%s

## FEEDBACK ITEM

Message: %s
Severity: %s

## INVESTIGATION HISTORY

%s%s
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

Output ONLY the JSON object, nothing else.`,
		petition, haiku,
		fb.GetMessage(), fb.GetSeverity().String(),
		historyBlock.String(), lawBlock)
}

// buildRevisionPrompt creates the revision prompt that produces a new haiku
// addressing all actioned feedback items from Phase 1.
func buildRevisionPrompt(
	petition, haiku string,
	laws []*flowv1.Law,
	actioned []actionedItem,
) string {
	var lawBlock string
	if len(laws) > 0 {
		lawBlock = "\n## GOVERNANCE LAWS\n\n" +
			"Your revised haiku must comply with all active governance laws.\n\n"
		for _, law := range laws {
			lawBlock += fmt.Sprintf("- [%s] (Tier %d): %s\n", law.GetId(), law.GetTier(), law.GetGoal())
		}
	}

	var fixBlock strings.Builder
	fixBlock.WriteString("\n## FIXES TO APPLY\n\n")
	fixBlock.WriteString("You have committed to the following fixes. Your revised haiku MUST address each one.\n\n")
	for _, a := range actioned {
		fixBlock.WriteString(fmt.Sprintf("- Feedback: %s\n  Fix: %s\n\n", a.Message, a.FixDesc))
	}

	return fmt.Sprintf(`You are a haiku poet revising your work. You must address the committed
fixes while staying true to the original request.

## THE PETITION

> %s

## CURRENT HAIKU

%s
%s%s
## INSTRUCTIONS

Write a revised haiku (three lines: EXACTLY 5 syllables, 7 syllables,
5 syllables) that addresses all committed fixes while remaining faithful
to the petition. Count syllables carefully.

## RESPONSE FORMAT

Respond with ONLY a JSON object:
{"haiku": "line one\nline two\nline three"}

Output ONLY the JSON object, nothing else.`,
		petition, haiku, lawBlock, fixBlock.String())
}

// ---------------------------------------------------------------------------
// Inference Function
// ---------------------------------------------------------------------------

// makeInferFunc returns an InferFunc that generates text via Ollama.
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
