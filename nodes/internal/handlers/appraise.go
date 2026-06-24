package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/artefacts"
	flow "github.com/gideas/flow/sdk/go"
)

// AppraiseConfig holds handler-level configuration for the Appraise handler.
// Agent-level config (prompts, model, schema) is encapsulated in the
// concrete eval and finding agents.
type AppraiseConfig struct {
	InputArtefacts   []string          // artefact IDs to read as input (e.g. ["petition"])
	ReviewArtefact   string            // artefact ID to review (e.g. "haiku")
	GovernedArtefact string            // GovernedArtefact CR name (e.g. "haiku")
	StampName        string            // stamp to apply (e.g. "review")
	ReviewerNode     string            // target node for fan-out review (e.g. "reviewer")
	DivisionPrompts  map[string]string // division name → system prompt suffix
}

// Appraise-specific constants.
const (
	verdictAccept = "accept"
	verdictReject = "reject"

	// defaultDivision is the division name assigned to laws with an empty
	// division field.
	defaultDivision = "general"
)

// hasNovelArgument returns true if the feedback item carries a
// NovelArgument justification.
func hasNovelArgument(fb *flowv1.FeedbackItem) bool {
	j := fb.GetJustification()
	return j != nil && j.GetNovelArgument() != nil &&
		j.GetNovelArgument().GetArgument() != ""
}

// ---------------------------------------------------------------------------
// HandleEval (Appraise Phase 1)
// ---------------------------------------------------------------------------

// HandleEval executes the Phase 1 (evaluation) and Phase 2 (fan-out review)
// logic of the Appraise node, stamps the artefact, raises new feedback, and
// optionally runs Phase 3 (learning capture via FindingContract).
//
// This is the full Appraise orchestration handler.
//
//nolint:cyclop // Orchestration function — sequential phases are inherently complex.
func HandleAppraise(
	ctx context.Context,
	client *flow.Client,
	eval flow.EvalContract,
	finding flow.FindingContract,
	cfg AppraiseConfig,
) error {
	// ---------------------------------------------------------------
	// Pre-inference: read artefacts, query laws, get existing feedback
	// ---------------------------------------------------------------

	inputContent, err := artefacts.FetchInputs(ctx, client, cfg.InputArtefacts)
	if err != nil {
		return fmt.Errorf("appraise: read inputs: %w", err)
	}

	reviewResp, err := client.GetArtefact(ctx, cfg.ReviewArtefact)
	if err != nil {
		return fmt.Errorf("appraise: read %s: %w", cfg.ReviewArtefact, err)
	}
	reviewContent := string(reviewResp.GetContent())

	slog.Info("appraise: reviewing",
		"input_artefacts", cfg.InputArtefacts,
		"review_artefact", cfg.ReviewArtefact,
	)

	laws, _ := client.QueryLaws(ctx, cfg.GovernedArtefact, "")

	existingFeedback, err := client.GetFeedback(ctx, cfg.GovernedArtefact)
	if err != nil {
		return fmt.Errorf("appraise: get feedback: %w", err)
	}

	// ---------------------------------------------------------------
	// Phase 1: Evaluate ACTIONED and WONT_FIX feedback items (parallel)
	// ---------------------------------------------------------------

	novelResolved, err := evaluateFeedback(
		ctx, eval, client,
		existingFeedback, inputContent, reviewContent)
	if err != nil {
		return fmt.Errorf("appraise: evaluate feedback: %w", err)
	}

	// ---------------------------------------------------------------
	// Phase 2: Fan-out review — delegate to child Reviewer nodes
	// ---------------------------------------------------------------

	mergedFeedback, err := fanOutReview(
		ctx, client, cfg, laws, existingFeedback,
		inputContent, reviewContent)
	if err != nil {
		return fmt.Errorf("appraise: fan-out review: %w", err)
	}

	slog.Info("appraise: review complete",
		"feedback_count", len(mergedFeedback))

	// ---------------------------------------------------------------
	// Post-inference: stamp, raise feedback, cite laws
	// ---------------------------------------------------------------

	if _, err := client.StampArtefact(ctx, cfg.GovernedArtefact, cfg.StampName); err != nil {
		return fmt.Errorf("appraise: stamp %s: %w", cfg.StampName, err)
	}
	slog.Info("appraise: stamp applied", "stamp", cfg.StampName)

	for i, item := range mergedFeedback {
		if item.Message == "" {
			continue
		}

		feedbackID, err := client.AddFeedback(
			ctx, cfg.GovernedArtefact, true, item.Message)
		if err != nil {
			return fmt.Errorf("appraise: add feedback[%d]: %w", i, err)
		}
		slog.Info("appraise: feedback raised",
			"index", i,
			"feedback_id", feedbackID,
			"message", item.Message,
			"cited_laws", item.CitedLaws,
		)

		if len(item.CitedLaws) > 0 {
			if err := client.Cite(ctx, item.CitedLaws...); err != nil {
				slog.Error("appraise: failed to cite laws",
					"error", err, "law_ids", item.CitedLaws)
			} else {
				slog.Info("appraise: cited laws", "law_ids", item.CitedLaws)
			}
		}
	}

	if len(mergedFeedback) == 0 {
		slog.Info("appraise: no feedback — content looks good")
	}

	// ---------------------------------------------------------------
	// Phase 3: Learning capture — mint Tier 1 Findings from resolved
	// novel arguments
	// ---------------------------------------------------------------

	if len(novelResolved) > 0 {
		if err := mintFindings(ctx, finding, client, novelResolved); err != nil {
			return fmt.Errorf("appraise: mint findings: %w", err)
		}
	} else {
		slog.Info("appraise: no novel arguments resolved " +
			"— skipping learning capture")
	}

	// ---------------------------------------------------------------
	// Route onward
	// ---------------------------------------------------------------

	if _, err := client.RouteToOutput(ctx, "default"); err != nil {
		return fmt.Errorf("appraise: route to output: %w", err)
	}

	slog.Info("appraise: routed to output",
		"workitem_id", os.Getenv(flow.EnvWorkitemID))
	return nil
}

// ---------------------------------------------------------------------------
// Phase 2: Fan-Out Review
// ---------------------------------------------------------------------------

// reviewItem is a single feedback observation from a child Reviewer's output.
type reviewItem struct {
	Message   string   `json:"message"`
	CitedLaws []string `json:"cited_laws"`
}

// reviewOutput is the Go representation of a child Reviewer's review-output
// artefact.
type reviewOutput struct {
	Feedback []reviewItem `json:"feedback"`
}

// groupLawsByDivision groups laws by their Division field. Laws with an
// empty division are placed under defaultDivision ("general").
func groupLawsByDivision(laws []*flowv1.Law) map[string][]*flowv1.Law {
	groups := make(map[string][]*flowv1.Law)
	for _, law := range laws {
		div := law.GetDivision()
		if div == "" {
			div = defaultDivision
		}
		groups[div] = append(groups[div], law)
	}
	return groups
}

// fanOutReview groups laws by division, creates FanOutTasks for each group,
// dispatches them to child Reviewer nodes, waits for completion, and merges
// the review outputs into a single slice of review items.
//
//nolint:cyclop // Orchestration function — sequential steps are inherently complex.
func fanOutReview(
	ctx context.Context,
	client *flow.Client,
	cfg AppraiseConfig,
	laws []*flowv1.Law,
	existingFeedback []*flowv1.FeedbackItem,
	inputContent, reviewContent string,
) ([]reviewItem, error) {
	// Group laws by division.
	groups := groupLawsByDivision(laws)

	slog.Info("appraise: fan-out review",
		"division_count", len(groups),
		"total_laws", len(laws),
	)

	// If no laws, return empty — nothing to review against.
	if len(groups) == 0 {
		return nil, nil
	}

	// Serialize history (shared across all divisions).
	historyItems := make([]HistoryData, 0, len(existingFeedback))
	for _, fb := range existingFeedback {
		historyItems = append(historyItems, HistoryData{
			State:   fb.GetState().String(),
			Message: fb.GetMessage(),
		})
	}

	historyJSON, err := json.Marshal(historyItems)
	if err != nil {
		return nil, fmt.Errorf("marshal history: %w", err)
	}

	// Build FanOutTasks — one per division.
	tasks := make([]flow.FanOutTask, 0, len(groups))
	for divName, divLaws := range groups {
		// Serialize laws for this division.
		lawItems := make([]LawData, 0, len(divLaws))
		for _, law := range divLaws {
			lawItems = append(lawItems, LawData{
				ID:   law.GetId(),
				Tier: int32(law.GetTier()),
				Goal: law.GetGoal(),
			})
		}

		lawsJSON, jsonErr := json.Marshal(lawItems)
		if jsonErr != nil {
			return nil, fmt.Errorf("marshal laws for division %s: %w", divName, jsonErr)
		}

		// Build division data with optional prompt suffix.
		divData := DivisionData{
			Name:         divName,
			PromptSuffix: cfg.DivisionPrompts[divName],
		}

		divJSON, jsonErr := json.Marshal(divData)
		if jsonErr != nil {
			return nil, fmt.Errorf("marshal division data for %s: %w", divName, jsonErr)
		}

		task := flow.FanOutTask{
			TargetNode: cfg.ReviewerNode,
			Artefacts: []flow.ChildArtefact{
				{ID: "inputs", GovernedArtefact: "review-data", Content: []byte(inputContent)},
				{ID: cfg.ReviewArtefact, GovernedArtefact: "review-data", Content: []byte(reviewContent)},
				{ID: ArtefactLaws, GovernedArtefact: "review-data", Content: lawsJSON},
				{ID: ArtefactHistory, GovernedArtefact: "review-data", Content: historyJSON},
				{ID: ArtefactDivision, GovernedArtefact: "review-data", Content: divJSON},
			},
		}
		tasks = append(tasks, task)

		slog.Info("appraise: fan-out task",
			"division", divName,
			"law_count", len(divLaws),
		)
	}

	// FanOut: create child workitems, attach artefacts, route to reviewer.
	_, err = client.FanOut(ctx, tasks)
	if err != nil {
		return nil, fmt.Errorf("fan-out: %w", err)
	}

	// AwaitChildren: block until all children reach terminal state.
	statuses, err := client.AwaitChildren(ctx)
	if err != nil {
		return nil, fmt.Errorf("await children: %w", err)
	}

	// CollectArtefacts: gather review-output from each child.
	results, err := client.CollectArtefacts(ctx, statuses, ArtefactReviewOutput)
	if err != nil {
		return nil, fmt.Errorf("collect artefacts: %w", err)
	}

	// Merge all review outputs.
	var merged []reviewItem
	for i, result := range results {
		raw := result.Artefacts[ArtefactReviewOutput]
		if raw == nil {
			slog.Warn("appraise: child returned no review-output",
				"child_index", i,
				"workitem_id", result.Status.WorkitemID,
			)
			continue
		}

		var out reviewOutput
		if jsonErr := json.Unmarshal(raw, &out); jsonErr != nil {
			return nil, fmt.Errorf(
				"unmarshal review-output from child %s: %w",
				result.Status.WorkitemID, jsonErr)
		}
		merged = append(merged, out.Feedback...)
	}

	return merged, nil
}

// ---------------------------------------------------------------------------
// Phase 1: Parallel Fix/Refusal Evaluation
// ---------------------------------------------------------------------------

// evaluateFeedback runs parallel EvalContract calls for ACTIONED and WONT_FIX
// feedback items. Each item gets a focused inference call that decides
// accept or reject.
//
// Returns the subset of feedback items that were resolved (accepted) AND
// carry a NovelArgument justification. These are candidates for Tier 1
// Finding promotion in the learning-capture phase.
func evaluateFeedback(
	ctx context.Context,
	eval flow.EvalContract,
	client *flow.Client,
	feedback []*flowv1.FeedbackItem,
	inputContent, reviewContent string,
) ([]*flowv1.FeedbackItem, error) {
	type evalTask struct {
		item *flowv1.FeedbackItem
		kind string
	}

	var tasks []evalTask
	for _, fb := range feedback {
		switch fb.GetState() {
		case flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED:
			tasks = append(tasks, evalTask{item: fb, kind: "actioned"})
		case flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX:
			tasks = append(tasks, evalTask{item: fb, kind: "wont_fix"})
		}
	}

	if len(tasks) == 0 {
		slog.Info("appraise: no feedback items to evaluate")
		return nil, nil
	}

	slog.Info("appraise: evaluating feedback items", "count", len(tasks))

	type evalResultItem struct {
		task evalTask
		out  *flow.EvalResult
		err  error
	}

	results := make([]evalResultItem, len(tasks))
	var wg sync.WaitGroup
	for i, task := range tasks {
		wg.Add(1)
		go func(idx int, t evalTask) {
			defer wg.Done()
			out, err := eval.Run(ctx, t.item, inputContent, reviewContent, t.kind)
			results[idx] = evalResultItem{task: t, out: out, err: err}
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
			slog.Info("appraise: accepting fix",
				"feedback_id", fbID, "reason", r.out.Reason)
			if err := client.AcceptFix(ctx, fbID); err != nil {
				return nil, fmt.Errorf("appraise: accept fix %s: %w", fbID, err)
			}
			if hasNovelArgument(r.task.item) {
				novelResolved = append(novelResolved, r.task.item)
			}

		case state == flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED &&
			r.out.Verdict == verdictReject:
			slog.Info("appraise: rejecting fix",
				"feedback_id", fbID, "reason", r.out.Reason)
			if err := client.RejectFix(ctx, fbID, r.out.Reason); err != nil {
				return nil, fmt.Errorf("appraise: reject fix %s: %w", fbID, err)
			}

		case state == flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX &&
			r.out.Verdict == verdictAccept:
			slog.Info("appraise: accepting refusal",
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
			slog.Info("appraise: rejecting refusal",
				"feedback_id", fbID, "reason", r.out.Reason)
			if err := client.RejectRefusal(ctx, fbID, r.out.Reason); err != nil {
				return nil, fmt.Errorf(
					"appraise: reject refusal %s: %w", fbID, err)
			}
		}
	}

	return novelResolved, nil
}

// ---------------------------------------------------------------------------
// Phase 3: Learning Capture — Mint Tier 1 Findings
// ---------------------------------------------------------------------------

// mintFindings runs the FindingContract agent to distill governance learnings
// from resolved feedback items that carried novel arguments. Each finding is
// recorded as a Tier 1 Finding via the Librarian.
func mintFindings(
	ctx context.Context,
	finding flow.FindingContract,
	client *flow.Client,
	novelItems []*flowv1.FeedbackItem,
) error {
	slog.Info("appraise: capturing learnings from resolved "+
		"novel arguments", "count", len(novelItems))

	out, err := finding.Run(ctx, novelItems)
	if err != nil {
		return fmt.Errorf("finding inference: %w", err)
	}
	if out == nil {
		return nil
	}

	slog.Info("appraise: LLM produced findings",
		"count", len(out.Findings))

	for i, f := range out.Findings {
		lawID, err := client.RecordFinding(
			ctx, f.Goal, f.AppliesTo,
			[]*flowv1.Representation{
				{Type: "text/markdown", Content: f.Rationale},
			},
		)
		if err != nil {
			return fmt.Errorf("record finding[%d]: %w", i, err)
		}
		slog.Info("appraise: minted Tier 1 Finding",
			"law_id", lawID,
			"goal", f.Goal,
			"applies_to", f.AppliesTo,
		)
	}

	return nil
}
