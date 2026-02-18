// Appraise is the reviewer node of the Haiku Foundry Cycle.
//
// It reads the "petition" (creative brief) and "haiku" artefacts, then uses an
// LLM (kimi-k2.5:cloud via Ollama) to review how well the haiku adheres to the
// petition and the active governance laws.
//
// Appraise operates in three phases:
//
//  1. Fix/Refusal Evaluation — For each ACTIONED or WONT_FIX feedback item, a
//     separate FoundryAgent inference call evaluates the fix or refusal. These
//     run in parallel, each with managed heartbeat and cost telemetry. The LLM
//     decides accept or reject per item.
//
//  2. Fresh Review — A single FoundryAgent inference call reviews the haiku
//     against the petition and governance laws, producing zero or more new
//     feedback items with severity and optional law citations.
//
//  3. Learning Capture — If Phase 1 resolved any feedback items that carried
//     a NovelArgument justification, a single batch FoundryAgent call distils
//     the learnings from those resolved discussions into Tier 1 Findings,
//     which are recorded in the Library via RecordFinding.
//
// Appraise always stamps "review" — meaning "I have appraised this version",
// not "this version is valid".
//
// Always routes back to Sort.
//
// Environment:
//
//	OLLAMA_BASE_URL     — Ollama API endpoint (default: http://localhost:11434)
//	APPRAISE_MODEL      — Model name (default: kimi-k2.5:cloud)
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
	defaultModel = "kimi-k2.5:cloud"
	envModel     = "APPRAISE_MODEL"

	verdictAccept = "accept"
	verdictReject = "reject"
)

// ---------------------------------------------------------------------------
// JSON Schemas — FoundryAgent validates LLM output against these
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Output Types — Go representation of the schema-validated JSON outputs
// ---------------------------------------------------------------------------

// evalOutput is the Go representation of the evalSchema-validated JSON.
type evalOutput struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`
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

// ---------------------------------------------------------------------------
// Severity Mapping
// ---------------------------------------------------------------------------

// severityMap maps lowercase string severity values from the LLM to the
// proto enum. Returns SEVERITY_MEDIUM for unknown values as a safe default.
var severityMap = map[string]flowv1.Severity{
	"low":      flowv1.Severity_SEVERITY_LOW,
	"medium":   flowv1.Severity_SEVERITY_MEDIUM,
	"high":     flowv1.Severity_SEVERITY_HIGH,
	"critical": flowv1.Severity_SEVERITY_CRITICAL,
}

func parseSeverity(s string) flowv1.Severity {
	if sev, ok := severityMap[strings.ToLower(s)]; ok {
		return sev
	}
	return flowv1.Severity_SEVERITY_MEDIUM
}

// ---------------------------------------------------------------------------
// Entrypoint
// ---------------------------------------------------------------------------

func main() {
	slog.Info("haiku-appraise: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("haiku-appraise: server failed", "error", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("haiku-appraise: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("appraise: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	// ---------------------------------------------------------------
	// Pre-inference: read artefacts, query laws, get existing feedback
	// ---------------------------------------------------------------

	petitionResp, err := client.GetArtefact(ctx, "petition")
	if err != nil {
		return fmt.Errorf("appraise: read petition: %w", err)
	}
	petition := string(petitionResp.GetContent())

	haikuResp, err := client.GetArtefact(ctx, "haiku")
	if err != nil {
		return fmt.Errorf("appraise: read haiku: %w", err)
	}
	haiku := string(haikuResp.GetContent())

	slog.Info("haiku-appraise: reviewing",
		"petition", petition,
		"haiku", haiku,
	)

	laws, _ := client.QueryLaws(ctx, "haiku", "")

	existingFeedback, err := client.GetFeedback(ctx, "haiku")
	if err != nil {
		return fmt.Errorf("appraise: get feedback: %w", err)
	}

	// Resolve the model name.
	model := os.Getenv(envModel)
	if model == "" {
		model = defaultModel
	}

	inferFn := makeInferFunc(model)

	// ---------------------------------------------------------------
	// Phase 1: Evaluate ACTIONED and WONT_FIX feedback items (parallel)
	// ---------------------------------------------------------------

	evalAgent, err := flow.NewAgent(client, evalSchema)
	if err != nil {
		return fmt.Errorf("appraise: create eval agent: %w", err)
	}

	novelResolved, err := evaluateFeedback(
		ctx, evalAgent, inferFn, client,
		existingFeedback, petition, haiku)
	if err != nil {
		return fmt.Errorf("appraise: evaluate feedback: %w", err)
	}

	// ---------------------------------------------------------------
	// Phase 2: Fresh review — new feedback items
	// ---------------------------------------------------------------

	reviewAgent, err := flow.NewAgent(client, reviewSchema)
	if err != nil {
		return fmt.Errorf("appraise: create review agent: %w", err)
	}

	reviewPrompt := buildReviewPrompt(petition, haiku, laws, existingFeedback)
	reviewOut, err := reviewAgent.Run(ctx, inferFn, []byte(reviewPrompt))
	if err != nil {
		return fmt.Errorf("appraise: review run: %w", err)
	}

	var review reviewOutput
	if err := json.Unmarshal(reviewOut, &review); err != nil {
		return fmt.Errorf("appraise: unmarshal review: %w", err)
	}

	slog.Info("haiku-appraise: review complete", "feedback_count", len(review.Feedback))

	// ---------------------------------------------------------------
	// Post-inference: stamp, raise feedback, cite laws, route
	// ---------------------------------------------------------------

	if _, err := client.StampArtefact(ctx, "haiku", "review"); err != nil {
		return fmt.Errorf("appraise: stamp review: %w", err)
	}
	slog.Info("haiku-appraise: review stamp applied")

	for i, item := range review.Feedback {
		if item.Message == "" {
			continue
		}

		severity := parseSeverity(item.Severity)
		feedbackID, err := client.AddFeedback(ctx, "haiku", severity, item.Message)
		if err != nil {
			return fmt.Errorf("appraise: add feedback[%d]: %w", i, err)
		}
		slog.Info("haiku-appraise: feedback raised",
			"index", i,
			"feedback_id", feedbackID,
			"severity", item.Severity,
			"message", item.Message,
			"cited_laws", item.CitedLaws,
		)

		if len(item.CitedLaws) > 0 {
			if err := client.Cite(ctx, item.CitedLaws...); err != nil {
				slog.Error("haiku-appraise: failed to cite laws",
					"error", err, "law_ids", item.CitedLaws)
			} else {
				slog.Info("haiku-appraise: cited laws", "law_ids", item.CitedLaws)
			}
		}
	}

	if len(review.Feedback) == 0 {
		slog.Info("haiku-appraise: no feedback — haiku looks good")
	}

	// ---------------------------------------------------------------
	// Phase 3: Learning capture — mint Tier 1 Findings from resolved
	// novel arguments
	// ---------------------------------------------------------------

	if len(novelResolved) > 0 {
		findingAgent, err := flow.NewAgent(client, findingSchema)
		if err != nil {
			return fmt.Errorf(
				"appraise: create finding agent: %w", err)
		}
		if err := mintFindings(
			ctx, findingAgent, inferFn, client, novelResolved,
		); err != nil {
			return fmt.Errorf(
				"appraise: mint findings: %w", err)
		}
	} else {
		slog.Info("haiku-appraise: no novel arguments resolved " +
			"— skipping learning capture")
	}

	// ---------------------------------------------------------------
	// Post-inference: stamp, route
	// ---------------------------------------------------------------

	if _, err := client.RouteToOutput(ctx, "default"); err != nil {
		return fmt.Errorf("appraise: route to sort: %w", err)
	}

	slog.Info("haiku-appraise: routed to sort", "workitem_id", wctx.GetWorkitemId())
	return nil
}

// ---------------------------------------------------------------------------
// Phase 1: Parallel Fix/Refusal Evaluation
// ---------------------------------------------------------------------------

// evaluateFeedback runs parallel LLM evaluations for ACTIONED and WONT_FIX
// feedback items. Each item gets a focused inference call that decides
// accept or reject.
//
// Returns the subset of feedback items that were resolved (accepted) AND
// carry a NovelArgument justification. These are candidates for Tier 1
// Finding promotion in the learning-capture phase.
func evaluateFeedback(
	ctx context.Context,
	agent *flow.Agent,
	inferFn flow.InferFunc,
	client *flow.Client,
	feedback []*flowv1.FeedbackItem,
	petition, haiku string,
) ([]*flowv1.FeedbackItem, error) {
	type evalTask struct {
		item   *flowv1.FeedbackItem
		prompt string
	}

	var tasks []evalTask
	for _, fb := range feedback {
		switch fb.GetState() {
		case flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED:
			tasks = append(tasks, evalTask{
				item:   fb,
				prompt: buildEvalPrompt(fb, petition, haiku, "actioned"),
			})
		case flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX:
			tasks = append(tasks, evalTask{
				item:   fb,
				prompt: buildEvalPrompt(fb, petition, haiku, "wont_fix"),
			})
		}
	}

	if len(tasks) == 0 {
		slog.Info("haiku-appraise: no feedback items to evaluate")
		return nil, nil
	}

	slog.Info("haiku-appraise: evaluating feedback items", "count", len(tasks))

	type evalResult struct {
		task evalTask
		out  evalOutput
		err  error
	}

	results := make([]evalResult, len(tasks))
	var wg sync.WaitGroup
	for i, task := range tasks {
		wg.Add(1)
		go func(idx int, t evalTask) {
			defer wg.Done()
			raw, err := agent.Run(ctx, inferFn, []byte(t.prompt))
			if err != nil {
				results[idx] = evalResult{task: t, err: err}
				return
			}
			var out evalOutput
			if err := json.Unmarshal(raw, &out); err != nil {
				results[idx] = evalResult{task: t, err: fmt.Errorf("unmarshal eval: %w", err)}
				return
			}
			results[idx] = evalResult{task: t, out: out}
		}(i, task)
	}
	wg.Wait()

	// Apply verdicts sequentially (gRPC calls to Archivist).
	// Collect resolved items that carry a novel argument justification.
	var novelResolved []*flowv1.FeedbackItem

	for _, r := range results {
		if r.err != nil {
			return nil, fmt.Errorf(
				"appraise: eval feedback %s: %w",
				r.task.item.GetId(), r.err)
		}

		fbID := r.task.item.GetId()
		state := r.task.item.GetState()

		switch {
		case state == flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED &&
			r.out.Verdict == verdictAccept:
			slog.Info("haiku-appraise: accepting fix",
				"feedback_id", fbID, "reason", r.out.Reason)
			if err := client.AcceptFix(ctx, fbID); err != nil {
				return nil, fmt.Errorf("appraise: accept fix %s: %w", fbID, err)
			}
			if hasNovelArgument(r.task.item) {
				novelResolved = append(novelResolved, r.task.item)
			}

		case state == flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED &&
			r.out.Verdict == verdictReject:
			slog.Info("haiku-appraise: rejecting fix",
				"feedback_id", fbID, "reason", r.out.Reason)
			if err := client.RejectFix(ctx, fbID, r.out.Reason); err != nil {
				return nil, fmt.Errorf("appraise: reject fix %s: %w", fbID, err)
			}

		case state == flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX &&
			r.out.Verdict == verdictAccept:
			slog.Info("haiku-appraise: accepting refusal",
				"feedback_id", fbID, "reason", r.out.Reason)
			if err := client.AcceptRefusal(ctx, fbID); err != nil {
				return nil, fmt.Errorf(
					"appraise: accept refusal %s: %w", fbID, err)
			}
			if hasNovelArgument(r.task.item) {
				novelResolved = append(novelResolved, r.task.item)
			}

		case state == flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX &&
			r.out.Verdict == verdictReject:
			slog.Info("haiku-appraise: rejecting refusal",
				"feedback_id", fbID, "reason", r.out.Reason)
			if err := client.RejectRefusal(ctx, fbID, r.out.Reason); err != nil {
				return nil, fmt.Errorf(
					"appraise: reject refusal %s: %w", fbID, err)
			}
		}
	}

	return novelResolved, nil
}

// hasNovelArgument returns true if the feedback item carries a
// NovelArgument justification.
func hasNovelArgument(fb *flowv1.FeedbackItem) bool {
	j := fb.GetJustification()
	return j != nil && j.GetNovelArgument() != nil &&
		j.GetNovelArgument().GetArgument() != ""
}

// ---------------------------------------------------------------------------
// Phase 3: Learning Capture — Mint Tier 1 Findings
// ---------------------------------------------------------------------------

// mintFindings runs a single batch LLM call to distill governance learnings
// from resolved feedback items that carried novel arguments. The LLM sees
// all items together and produces zero or more findings. Each finding is
// recorded as a Tier 1 Finding via the Librarian.
func mintFindings(
	ctx context.Context,
	agent *flow.Agent,
	inferFn flow.InferFunc,
	client *flow.Client,
	novelItems []*flowv1.FeedbackItem,
) error {
	if len(novelItems) == 0 {
		return nil
	}

	slog.Info("haiku-appraise: capturing learnings from resolved "+
		"novel arguments", "count", len(novelItems))

	prompt := buildFindingPrompt(novelItems)
	raw, err := agent.Run(ctx, inferFn, []byte(prompt))
	if err != nil {
		return fmt.Errorf("finding inference: %w", err)
	}

	var out findingsOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("unmarshal findings: %w", err)
	}

	slog.Info("haiku-appraise: LLM produced findings",
		"count", len(out.Findings))

	for i, f := range out.Findings {
		lawID, err := client.RecordFinding(
			ctx, f.Goal, f.AppliesTo,
			[]*flowv1.Representation{
				{Type: "text/markdown", Content: f.Rationale},
			},
		)
		if err != nil {
			return fmt.Errorf(
				"record finding[%d]: %w", i, err)
		}
		slog.Info("haiku-appraise: minted Tier 1 Finding",
			"law_id", lawID,
			"goal", f.Goal,
			"applies_to", f.AppliesTo,
		)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Prompt Builders
// ---------------------------------------------------------------------------

// buildFindingPrompt creates a batch prompt for the learning-capture phase.
// It presents all resolved novel-argument feedback items together so the LLM
// can synthesize learnings across related discussions.
func buildFindingPrompt(items []*flowv1.FeedbackItem) string {
	var discussions strings.Builder
	for i, fb := range items {
		discussions.WriteString(fmt.Sprintf(
			"### Discussion %d\n\n", i+1))
		discussions.WriteString(fmt.Sprintf(
			"**Original feedback**: %s\n", fb.GetMessage()))
		discussions.WriteString(fmt.Sprintf(
			"**Severity**: %s\n", fb.GetSeverity().String()))

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
			discussions.WriteString(fmt.Sprintf(
				"**Resolution**: Resolved (state: %s)\n",
				fb.GetState().String()))
		}

		// Novel argument.
		arg := fb.GetJustification().GetNovelArgument().GetArgument()
		discussions.WriteString(fmt.Sprintf(
			"**Novel argument**: %s\n", arg))

		// History.
		if history := fb.GetHistory(); len(history) > 0 {
			discussions.WriteString("\n**Discussion history**:\n")
			for _, ev := range history {
				discussions.WriteString(fmt.Sprintf(
					"- [%s] %s: %s\n",
					ev.GetAction(),
					ev.GetActor(),
					ev.GetMessage()))
			}
		}
		discussions.WriteString("\n---\n\n")
	}

	return fmt.Sprintf(`You are a governance analyst for a haiku review pipeline.

The following feedback discussions have concluded. Each involved a novel
argument — a new insight not covered by existing governance laws. Your task
is to capture the learnings from these discussions to provide clarity in
future review cycles.

For each distinct learning, produce a concise, forward-looking governance
statement (the "goal") that future reviewers and refiners can reference.
If multiple discussions converge on the same insight, consolidate them
into a single finding. If a discussion produced no reusable learning,
omit it.

Each finding must specify:
- "goal": A concise governance statement (1-2 sentences). Write it as a
  principle or rule, not a description of what happened.
- "applies_to": Which artefact kinds this finding applies to (e.g.
  ["haiku"]). Use the GovernedArtefact kind name.
- "rationale": A brief explanation of why this learning matters,
  referencing the discussion that produced it. This will be preserved
  as the finding's initial representation.

If no discussions produced reusable learnings, return:
{"findings": []}

---

## RESOLVED DISCUSSIONS

%s
## RESPONSE FORMAT

Respond with ONLY a JSON object:
{"findings": [
  {"goal": "...", "applies_to": ["haiku"], "rationale": "..."}
]}

Output ONLY the JSON object. No markdown fences, no explanation.`,
		discussions.String())
}

// buildEvalPrompt creates a focused evaluation prompt for a single feedback
// item that has been actioned (fix applied) or refused (wont_fix).
func buildEvalPrompt(fb *flowv1.FeedbackItem, petition, haiku, kind string) string {
	var historyBlock strings.Builder
	for _, ev := range fb.GetHistory() {
		historyBlock.WriteString(fmt.Sprintf("- [%s] %s: %s\n", ev.GetAction(), ev.GetActor(), ev.GetMessage()))
	}

	var kindInstruction string
	switch kind {
	case "actioned":
		kindInstruction = `The refining node claims to have FIXED this issue.
Your job: decide if the fix is adequate given the current haiku.
- "accept" means the fix sufficiently addresses the original feedback.
- "reject" means the fix is incomplete or misguided — explain why.`
	case "wont_fix":
		kindInstruction = `The refining node REFUSED to fix this issue and provided a justification.
Your job: decide if the refusal is justified.
- "accept" means the justification is reasonable — the feedback can be resolved.
- "reject" means the refusal is unjustified — explain why it should be addressed.`
	}

	var justificationBlock string
	if fb.GetJustification() != nil {
		j := fb.GetJustification()
		switch {
		case j.GetCitation() != nil:
			justificationBlock = fmt.Sprintf("\n## REFINER'S JUSTIFICATION (citation)\n\nCited laws: %v\n",
				j.GetCitation().GetCitationIds())
		case j.GetNovelArgument() != nil:
			justificationBlock = fmt.Sprintf("\n## REFINER'S JUSTIFICATION (novel argument)\n\n%s\n",
				j.GetNovelArgument().GetArgument())
		}
	}

	return fmt.Sprintf(`You are a haiku reviewer evaluating a previous feedback item.

## CONTEXT

The haiku was written to fulfil this petition:
> %s

The current haiku:
%s

## ORIGINAL FEEDBACK

Message: %s
Severity: %s

## INVESTIGATION HISTORY

%s%s

## YOUR TASK

%s

## RESPONSE FORMAT

Respond with ONLY a JSON object:
{"verdict": "accept", "reason": "brief explanation"}
or
{"verdict": "reject", "reason": "brief explanation of why this is inadequate"}

Output ONLY the JSON object, nothing else.`,
		petition, haiku,
		fb.GetMessage(), fb.GetSeverity().String(),
		historyBlock.String(), justificationBlock,
		kindInstruction)
}

// buildReviewPrompt creates the main review prompt for fresh feedback
// generation.
func buildReviewPrompt(
	petition, haiku string,
	laws []*flowv1.Law,
	existingFeedback []*flowv1.FeedbackItem,
) string {
	var lawBlock string
	if len(laws) > 0 {
		lawBlock = "\n## GOVERNANCE LAWS\n\n" +
			"The following laws are active. The haiku MUST comply with all of them.\n" +
			"If a law is violated, cite it by ID in your feedback.\n\n"
		for _, law := range laws {
			lawBlock += fmt.Sprintf("- [%s] (Tier %d): %s\n", law.GetId(), law.GetTier(), law.GetGoal())
		}
	}

	var historyBlock string
	if len(existingFeedback) > 0 {
		historyBlock = "\n## PREVIOUS FEEDBACK HISTORY\n\n" +
			"These items have already been raised. Do NOT re-raise resolved items.\n" +
			"Only raise NEW observations not covered by existing feedback.\n\n"
		for _, fb := range existingFeedback {
			historyBlock += fmt.Sprintf("- [%s] %s\n", fb.GetState().String(), fb.GetMessage())
		}
	}

	exampleLawID := "example-law-id"
	if len(laws) > 0 {
		exampleLawID = laws[0].GetId()
	}

	return fmt.Sprintf(`You are a haiku reviewer for a governed creative pipeline.

Your job is to review the haiku and produce NEW feedback observations. You are
NOT approving or rejecting — you are producing observations. If you have no
new issues, return an empty feedback array.

Every piece of feedback must either:
1. CITE one or more governance laws by ID — the haiku violates or
   insufficiently addresses the law.
2. Offer a NOVEL observation — something not covered by any law but
   worth improving. Use an empty cited_laws array for these.

Each feedback item must include a severity level:
- "low": Minor style or preference issue
- "medium": Quality issue that should be addressed
- "high": Functional or structural concern
- "critical": Blocking issue

---

## THE PETITION

The haiku was written to fulfil this creative brief:

> %s

The haiku must faithfully address the petition's theme, subject, and mood.

---

## THE HAIKU UNDER REVIEW

%s

---%s%s

---

## RESPONSE FORMAT

Respond with ONLY a JSON object containing a "feedback" array.
Each item has:
- "message": a specific, actionable observation (1-2 sentences)
- "severity": one of "low", "medium", "high", "critical"
- "cited_laws": array of law IDs this feedback references (empty array if novel)

If the haiku is excellent and you have no NEW feedback, return:
{"feedback": []}

Examples:

No issues:
{"feedback": []}

Law violation:
{"feedback": [
  {"message": "Names the season directly.",
   "severity": "medium", "cited_laws": ["%s"]}
]}

Novel observation:
{"feedback": [
  {"message": "The final line feels rushed.",
   "severity": "low", "cited_laws": []}
]}

Output ONLY the JSON object. No markdown fences, no explanation, no other text.`,
		petition, haiku, lawBlock, historyBlock, exampleLawID)
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
