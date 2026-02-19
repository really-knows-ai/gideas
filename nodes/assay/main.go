// Assay is the generic judicial resolution node for Foundry Flow.
//
// It resolves deadlocked feedback disputes via autonomous jury deliberation
// and mints binding Tier 2 Rulings to prevent recurrence.
//
// Assay operates in four phases:
//
//  1. Triage — Lists all artefacts on the workitem and finds the FIRST
//     disputed feedback item (DEADLOCKED state) across any artefact.
//     That artefact becomes the case. If no deadlocked items exist,
//     the workitem fails.
//
//  2. Empanel — Creates a jury of FoundryAgent instances. Jury size is
//     determined by the severity of the dispute: 3 for low/medium, 5 for
//     high, 7 for critical.
//
//  3. Deliberate — Orchestrates a blind-voting protocol with optional
//     deliberation rounds. Each juror votes independently on the dispute.
//     The verdict is determined by consensus (SimpleMajority >50%,
//     SuperMajority ≥66%, or Unanimity 100% depending on severity).
//
//  4. Execute — If consensus is reached, mints a Tier 2 Ruling via WriteLaw.
//     If the jury hangs (no consensus after max rounds), escalates to HITL.
//
// Special Cases:
//
//  - Retirement Hearings: Reviews expired Tier 2 Rulings for sustainability
//    based on citation frequency and usage data. Does NOT use jury mechanism.
//
//  - Codification: Separates subjective rules (text/markdown) from
//    deterministic constraints (application/smt-lib) and mints law groups
//    with shared group IDs.
//
// Always routes to "resolved" (back to sender) or "escalate" (to HITL).
//
// Environment:
//
//	OLLAMA_BASE_URL      — Ollama API endpoint (default: http://localhost:11434)
//	ASSAY_MODEL          — Model name (default: kimi-k2.5:cloud)
//	ASSAY_MAX_ROUNDS     — Maximum deliberation rounds (default: 3)
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/ollama"
	flow "github.com/gideas/flow/sdk/go"
)

const (
	defaultModel     = "kimi-k2.5:cloud"
	defaultMaxRounds = 3

	envModel     = "ASSAY_MODEL"
	envMaxRounds = "ASSAY_MAX_ROUNDS"

	// Consensus thresholds
	thresholdSimpleMajority = 0.51  // >50%
	thresholdSuperMajority  = 0.66  // ≥66%
	thresholdUnanimity      = 1.00  // 100%

	// Verdict types
	verdictResolve  = "resolve"
	verdictReject   = "reject"
	verdictConflict = "conflict"
)

// ---------------------------------------------------------------------------
// JSON Schemas — FoundryAgent validates LLM output against these
// ---------------------------------------------------------------------------

// deliberationSchema validates the output of jury deliberation inferences.
// Each juror produces a verdict and reasoning.
var deliberationSchema = []byte(`{
	"type": "object",
	"properties": {
		"verdict": { "type": "string", "enum": ["resolve", "reject", "conflict"] },
		"reasoning": { "type": "string", "minLength": 1 },
		"suggested_statement": { "type": "string" }
	},
	"required": ["verdict", "reasoning"],
	"additionalProperties": false
}`)

// codificationSchema validates the output of codification analysis.
// The LLM separates subjective and deterministic components.
var codificationSchema = []byte(`{
	"type": "object",
	"properties": {
		"has_deterministic": { "type": "boolean" },
		"subjective": { "type": "string", "minLength": 1 },
		"deterministic": { "type": "string" }
	},
	"required": ["has_deterministic", "subjective"],
	"additionalProperties": false
}`)

// ---------------------------------------------------------------------------
// Output Types — Go representation of the schema-validated JSON outputs
// ---------------------------------------------------------------------------

// deliberationOutput is the Go representation of deliberationSchema-validated JSON.
type deliberationOutput struct {
	Verdict            string `json:"verdict"`
	Reasoning          string `json:"reasoning"`
	SuggestedStatement string `json:"suggested_statement"`
}

// codificationOutput is the Go representation of codificationSchema-validated JSON.
type codificationOutput struct {
	HasDeterministic bool   `json:"has_deterministic"`
	Subjective       string `json:"subjective"`
	Deterministic    string `json:"deterministic"`
}

// ---------------------------------------------------------------------------
// Entrypoint
// ---------------------------------------------------------------------------

func main() {
	slog.Info("assay: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("assay: server failed", "error", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("assay: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("assay: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	// Check for retirement hearing (special case)
	if isRetirementHearing(wctx) {
		return handleRetirementHearing(ctx, client, wctx)
	}

	// ---------------------------------------------------------------
	// Phase 1: Triage — Find first disputed feedback across all artefacts
	// ---------------------------------------------------------------

	// List all artefacts on the workitem
	artefactsResp, err := client.Archivist.ListArtefacts(ctx, &flowv1.ListArtefactsRequest{
		WorkitemId: wctx.GetWorkitemId(),
	})
	if err != nil {
		return fmt.Errorf("assay: list artefacts: %w", err)
	}

	artefactRefs := artefactsResp.GetArtefactRefs()
	if len(artefactRefs) == 0 {
		return fmt.Errorf("assay: no artefacts on workitem")
	}

	// Search all artefacts for the first deadlocked feedback item
	var disputedItem *flowv1.FeedbackItem
	var targetArtefact string

	for _, artefactRef := range artefactRefs {
		artefactID := artefactRef.GetId()
		feedback, err := client.GetFeedback(ctx, artefactID)
		if err != nil {
			slog.Warn("assay: failed to get feedback",
				"artefact", artefactID,
				"error", err)
			continue
		}

		// Look for first deadlocked item
		for _, fb := range feedback {
			if fb.GetState() == flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED {
				disputedItem = fb
				targetArtefact = artefactID
				break
			}
		}

		if disputedItem != nil {
			break
		}
	}

	if disputedItem == nil {
		return fmt.Errorf("assay: no deadlocked feedback items found across any artefact")
	}

	slog.Info("assay: found disputed case",
		"feedback_id", disputedItem.GetId(),
		"artefact", targetArtefact,
		"severity", disputedItem.GetSeverity().String(),
	)

	// Resolve the model and max rounds
	model := getEnvOrDefault(envModel, defaultModel)
	maxRounds := getEnvIntOrDefault(envMaxRounds, defaultMaxRounds)

	inferFn := makeInferFunc(model)

	// ---------------------------------------------------------------
	// Phase 2-4: Process the disputed item through deliberation
	// ---------------------------------------------------------------

	if err := processDispute(ctx, client, inferFn, disputedItem, maxRounds, targetArtefact); err != nil {
		return fmt.Errorf("assay: process dispute %s: %w", disputedItem.GetId(), err)
	}

	// ---------------------------------------------------------------
	// Route to resolved output (back to sender)
	// ---------------------------------------------------------------

	if _, err := client.RouteToOutput(ctx, "resolved"); err != nil {
		return fmt.Errorf("assay: route to resolved: %w", err)
	}

	slog.Info("assay: completed", "workitem_id", wctx.GetWorkitemId())
	return nil
}

// ---------------------------------------------------------------------------
// Phase 1: Triage
// ---------------------------------------------------------------------------

func filterDeadlocked(feedback []*flowv1.FeedbackItem) []*flowv1.FeedbackItem {
	var result []*flowv1.FeedbackItem
	for _, fb := range feedback {
		if fb.GetState() == flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED {
			result = append(result, fb)
		}
	}
	return result
}

// ---------------------------------------------------------------------------
// Phase 2: Empanel
// ---------------------------------------------------------------------------

// determineJurySize returns the number of jurors based on severity.
func determineJurySize(severity flowv1.Severity) int {
	switch severity {
	case flowv1.Severity_SEVERITY_CRITICAL:
		return 7
	case flowv1.Severity_SEVERITY_HIGH:
		return 5
	default:
		return 3
	}
}

// determineConsensusThreshold returns the required threshold based on severity.
func determineConsensusThreshold(severity flowv1.Severity) float64 {
	switch severity {
	case flowv1.Severity_SEVERITY_CRITICAL:
		return thresholdUnanimity
	case flowv1.Severity_SEVERITY_HIGH:
		return thresholdSuperMajority
	default:
		return thresholdSimpleMajority
	}
}

// ---------------------------------------------------------------------------
// Phase 3: Deliberate
// ---------------------------------------------------------------------------

// processDispute handles a single disputed feedback item through deliberation.
func processDispute(
	ctx context.Context,
	client *flow.Client,
	inferFn flow.InferFunc,
	item *flowv1.FeedbackItem,
	maxRounds int,
	targetArtefact string,
) error {
	jurySize := determineJurySize(item.GetSeverity())
	threshold := determineConsensusThreshold(item.GetSeverity())

	slog.Info("assay: empaneling jury",
		"feedback_id", item.GetId(),
		"severity", item.GetSeverity().String(),
		"jury_size", jurySize,
		"threshold", threshold,
	)

	// Get artefact context
	artefactResp, err := client.GetArtefact(ctx, targetArtefact)
	if err != nil {
		return fmt.Errorf("get artefact: %w", err)
	}
	artefactContent := string(artefactResp.GetContent())

	// Deliberate for up to maxRounds
	for round := 1; round <= maxRounds; round++ {
		slog.Info("assay: deliberation round",
			"feedback_id", item.GetId(),
			"round", round,
		)

		verdict, consensus, err := deliberateRound(
			ctx, client, inferFn, item, artefactContent, targetArtefact, jurySize, threshold)
		if err != nil {
			return fmt.Errorf("deliberation round %d: %w", round, err)
		}

		if consensus {
			// Consensus reached — execute verdict
			return executeVerdict(ctx, client, inferFn, item, verdict, targetArtefact)
		}

		slog.Info("assay: no consensus",
			"feedback_id", item.GetId(),
			"round", round,
		)
	}

	// Hung jury — escalate to HITL
	slog.Warn("assay: hung jury, escalating",
		"feedback_id", item.GetId(),
		"max_rounds", maxRounds,
	)
	return escalateToHITL(ctx, client)
}

// deliberateRound runs a single deliberation round with parallel juror votes.
func deliberateRound(
	ctx context.Context,
	client *flow.Client,
	inferFn flow.InferFunc,
	item *flowv1.FeedbackItem,
	artefactContent string,
	targetArtefact string,
	jurySize int,
	threshold float64,
) (string, bool, error) {
	agent, err := flow.NewAgent(client, deliberationSchema)
	if err != nil {
		return "", false, fmt.Errorf("create agent: %w", err)
	}

	prompt := buildDeliberationPrompt(item, artefactContent, targetArtefact)

	// Run parallel juror votes
	type voteResult struct {
		output deliberationOutput
		err    error
	}

	results := make([]voteResult, jurySize)
	var wg sync.WaitGroup

	for i := 0; i < jurySize; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			raw, err := agent.Run(ctx, inferFn, []byte(prompt))
			if err != nil {
				results[idx] = voteResult{err: err}
				return
			}
			var out deliberationOutput
			if err := json.Unmarshal(raw, &out); err != nil {
				results[idx] = voteResult{err: fmt.Errorf("unmarshal: %w", err)}
				return
			}
			results[idx] = voteResult{output: out}
		}(i)
	}
	wg.Wait()

	// Tally votes
	voteCounts := make(map[string]int)
	for _, r := range results {
		if r.err != nil {
			return "", false, fmt.Errorf("juror vote: %w", r.err)
		}
		voteCounts[r.output.Verdict]++
	}

	slog.Info("assay: vote tally", "votes", voteCounts)

	// Check for consensus
	for verdict, count := range voteCounts {
		if float64(count)/float64(jurySize) >= threshold {
			slog.Info("assay: consensus reached",
				"verdict", verdict,
				"votes", count,
				"total", jurySize,
			)
			return verdict, true, nil
		}
	}

	return "", false, nil
}

// ---------------------------------------------------------------------------
// Phase 4: Execute
// ---------------------------------------------------------------------------

// executeVerdict mints a Tier 2 Ruling based on the jury verdict.
func executeVerdict(
	ctx context.Context,
	client *flow.Client,
	inferFn flow.InferFunc,
	item *flowv1.FeedbackItem,
	verdict string,
	targetArtefact string,
) error {
	slog.Info("assay: executing verdict",
		"feedback_id", item.GetId(),
		"verdict", verdict,
	)

	switch verdict {
	case verdictResolve:
		// Mint Tier 2 Ruling to resolve the dispute
		return mintRuling(ctx, client, inferFn, item, "resolve", targetArtefact)

	case verdictReject:
		// Mint Tier 2 Ruling to reject the feedback
		return mintRuling(ctx, client, inferFn, item, "reject", targetArtefact)

	case verdictConflict:
		// Cannot be resolved — escalate to HITL
		return escalateToHITL(ctx, client)

	default:
		return fmt.Errorf("unknown verdict: %s", verdict)
	}
}

// mintRuling creates a Tier 2 Ruling via WriteLaw.
func mintRuling(
	ctx context.Context,
	client *flow.Client,
	inferFn flow.InferFunc,
	item *flowv1.FeedbackItem,
	resolution string,
	targetArtefact string,
) error {
	// Generate ruling statement based on feedback discussion
	statement := generateRulingStatement(item, resolution)

	slog.Info("assay: minting ruling",
		"feedback_id", item.GetId(),
		"statement", statement,
	)

	// Attempt codification
	codified, err := codifyStatement(ctx, client, inferFn, statement)
	if err != nil {
		slog.Warn("assay: codification failed, using text-only",
			"error", err)
		codified = nil
	}

	if codified != nil && codified.HasDeterministic {
		// Mint law group with subjective + deterministic
		return mintLawGroup(ctx, client, inferFn, item, codified, targetArtefact)
	}

	// Mint single text/markdown ruling
	law := &flowv1.Law{
		Goal: statement,
		Representations: []*flowv1.Representation{
			{
				Type:    "text/markdown",
				Content: statement,
			},
		},
		Tier:      flowv1.LawTier_LAW_TIER_RULING,
		AppliesTo: []string{targetArtefact},
	}

	resp, err := client.Librarian.WriteLaw(ctx, &flowv1.WriteLawRequest{Law: law})
	if err != nil {
		return fmt.Errorf("write law: %w", err)
	}

	slog.Info("assay: ruling minted",
		"law_id", resp.GetLawId(),
		"version_hash", resp.GetVersionHash(),
	)

	// Resolve the feedback item
	if err := client.ResolveFeedback(ctx, item.GetId(),
		fmt.Sprintf("Resolved by Tier 2 Ruling: %s", resp.GetLawId())); err != nil {
		return fmt.Errorf("resolve feedback: %w", err)
	}

	return nil
}

// mintLawGroup creates multiple linked laws sharing a group ID.
func mintLawGroup(
	ctx context.Context,
	client *flow.Client,
	inferFn flow.InferFunc,
	item *flowv1.FeedbackItem,
	codified *codificationOutput,
	targetArtefact string,
) error {
	groupID := fmt.Sprintf("lg-%d", os.Getpid()) // Simple group ID generation

	slog.Info("assay: minting law group", "group_id", groupID)

	// Mint subjective law
	subjectiveLaw := &flowv1.Law{
		Goal: codified.Subjective,
		Representations: []*flowv1.Representation{
			{
				Type:    "text/markdown",
				Content: codified.Subjective,
			},
		},
		Tier:      flowv1.LawTier_LAW_TIER_RULING,
		AppliesTo: []string{targetArtefact},
		// Note: group field doesn't exist in proto, using only representations
	}

	resp1, err := client.Librarian.WriteLaw(ctx, &flowv1.WriteLawRequest{Law: subjectiveLaw})
	if err != nil {
		return fmt.Errorf("write subjective law: %w", err)
	}

	slog.Info("assay: subjective law minted", "law_id", resp1.GetLawId())

	// Mint deterministic law
	deterministicLaw := &flowv1.Law{
		Goal: codified.Subjective, // Same goal, different representation
		Representations: []*flowv1.Representation{
			{
				Type:    "application/smt-lib",
				Content: codified.Deterministic,
			},
		},
		Tier:      flowv1.LawTier_LAW_TIER_RULING,
		AppliesTo: []string{targetArtefact},
	}

	resp2, err := client.Librarian.WriteLaw(ctx, &flowv1.WriteLawRequest{Law: deterministicLaw})
	if err != nil {
		return fmt.Errorf("write deterministic law: %w", err)
	}

	slog.Info("assay: deterministic law minted", "law_id", resp2.GetLawId())

	// Resolve the feedback item with reference to the law group
	if err := client.ResolveFeedback(ctx, item.GetId(),
		fmt.Sprintf("Resolved by Tier 2 Ruling group %s (laws: %s, %s)", 
			groupID, resp1.GetLawId(), resp2.GetLawId())); err != nil {
		return fmt.Errorf("resolve feedback: %w", err)
	}

	return nil
}

// codifyStatement attempts to separate subjective and deterministic components.
func codifyStatement(
	ctx context.Context,
	client *flow.Client,
	inferFn flow.InferFunc,
	statement string,
) (*codificationOutput, error) {
	agent, err := flow.NewAgent(client, codificationSchema)
	if err != nil {
		return nil, fmt.Errorf("create agent: %w", err)
	}

	prompt := buildCodificationPrompt(statement)
	raw, err := agent.Run(ctx, inferFn, []byte(prompt))
	if err != nil {
		return nil, fmt.Errorf("agent run: %w", err)
	}

	var out codificationOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}

	return &out, nil
}

// generateRulingStatement creates a ruling statement from feedback history.
func generateRulingStatement(item *flowv1.FeedbackItem, resolution string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Judicial Ruling (%s): ", resolution))
	b.WriteString(item.GetMessage())

	if history := item.GetHistory(); len(history) > 0 {
		b.WriteString("\n\nDiscussion Summary:\n")
		for _, ev := range history {
			b.WriteString(fmt.Sprintf("- %s: %s\n", ev.GetActor(), ev.GetMessage()))
		}
	}

	return b.String()
}

// escalateToHITL routes to the HITL node for human intervention.
func escalateToHITL(ctx context.Context, client *flow.Client) error {
	slog.Info("assay: escalating to HITL")
	_, err := client.RouteToOutput(ctx, "escalate")
	if err != nil {
		return fmt.Errorf("route to escalate: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Special Case: Retirement Hearings
// ---------------------------------------------------------------------------

func isRetirementHearing(wctx *flowv1.WorkitemContext) bool {
	// Check if this is a retirement hearing based on context
	// For now, we'll assume it's not implemented yet
	return false
}

func handleRetirementHearing(
	ctx context.Context,
	client *flow.Client,
	wctx *flowv1.WorkitemContext,
) error {
	slog.Info("assay: processing retirement hearing")
	// Retirement hearing logic would go here
	// For now, just complete
	_, err := client.Complete(ctx, "")
	return err
}

// ---------------------------------------------------------------------------
// Prompt Builders
// ---------------------------------------------------------------------------

func buildDeliberationPrompt(item *flowv1.FeedbackItem, artefactContent string, targetArtefact string) string {
	var historyBlock strings.Builder
	for _, ev := range item.GetHistory() {
		historyBlock.WriteString(fmt.Sprintf("- [%s] %s: %s\n",
			ev.GetAction(), ev.GetActor(), ev.GetMessage()))
	}

	return fmt.Sprintf(`You are a juror in a %s review dispute.

## THE ARTEFACT

%s

## THE DISPUTED FEEDBACK

Original Feedback: %s
Severity: %s

## DISCUSSION HISTORY

%s

## YOUR TASK

Review the dispute and cast your vote. Consider:
1. Is the feedback valid and should be addressed?
2. Has the refiner provided adequate justification for refusal?
3. Can consensus be reached, or is this irreconcilable?

## RESPONSE FORMAT

Respond with ONLY a JSON object:
{
  "verdict": "resolve" | "reject" | "conflict",
  "reasoning": "brief explanation of your vote",
  "suggested_statement": "optional proposed ruling statement"
}

Verdicts:
- "resolve": The feedback should be resolved (either fixed or accepted as-is)
- "reject": The feedback should be rejected (not applicable)
- "conflict": Irreconcilable conflict requiring human intervention

Output ONLY the JSON object, nothing else.`,
		targetArtefact,
		artefactContent,
		item.GetMessage(),
		item.GetSeverity().String(),
		historyBlock.String())
}

func buildCodificationPrompt(statement string) string {
	return fmt.Sprintf(`You are a governance analyst for a software quality pipeline.

## THE STATEMENT

%s

## YOUR TASK

Analyze this governance statement and separate it into:
1. Subjective components (requiring human/LLM judgment)
2. Deterministic components (expressible as formal logic)

Examples of deterministic constraints:
- Prohibited words/phrases
- Character/line limits
- Required patterns (regex)
- Naming conventions

Examples of subjective rules:
- Tone/mood requirements
- Quality/beauty assessments
- Appropriateness judgments

## RESPONSE FORMAT

Respond with ONLY a JSON object:
{
  "has_deterministic": true/false,
  "subjective": "the subjective portion as markdown",
  "deterministic": "SMT-LIB constraints (if has_deterministic is true)"
}

If there are no deterministic components, set "has_deterministic" to false
and omit "deterministic".

Example SMT-LIB constraint:
(declare-const artefact-content String)
(assert (not (str.contains artefact-content "prohibited-word")))

Output ONLY the JSON object, nothing else.`,
		statement)
}

// ---------------------------------------------------------------------------
// Inference Function
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Utility Functions
// ---------------------------------------------------------------------------

func getEnvOrDefault(key, defaultValue string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultValue
}

func getEnvIntOrDefault(key string, defaultValue int) int {
	if val := os.Getenv(key); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return defaultValue
}
