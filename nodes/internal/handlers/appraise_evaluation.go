package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	flow "github.com/foundry/flow/sdk/go"
)

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
	feedback []*flow.Feedback,
	fanOutObservations []reviewItem,
	inputContent, reviewContent string,
) ([]*flow.Feedback, error) {
	type evalTask struct {
		fb   *flow.Feedback
		kind string
	}

	var tasks []evalTask
	for _, fb := range feedback {
		switch fb.GetState() {
		case flow.FeedbackStateActioned:
			tasks = append(tasks, evalTask{fb: fb, kind: "actioned"})
		case flow.FeedbackStateWontFix:
			tasks = append(tasks, evalTask{fb: fb, kind: "wont_fix"})
		}
	}

	if len(tasks) == 0 {
		slog.Info("appraisal: no feedback items to evaluate")
		return nil, nil
	}

	slog.Info("appraisal: evaluating feedback items", "count", len(tasks))

	// Build a summary of fresh observations from the fan-out review so the
	// eval agent can see whether a fix merely moved a violation to a different
	// law rather than resolving it.
	var augmentedReviewContent string
	if len(fanOutObservations) > 0 {
		var obs strings.Builder
		obs.WriteString("\n\n--- Fresh review observations (NOT yet raised as feedback) ---\n")
		for _, o := range fanOutObservations {
			obs.WriteString("- " + o.Message)
			if len(o.CitedLaws) > 0 {
				obs.WriteString(" [cited: " + strings.Join(o.CitedLaws, ", ") + "]")
			}
			obs.WriteString("\n")
		}
		augmentedReviewContent = reviewContent + obs.String()
	} else {
		augmentedReviewContent = reviewContent
	}

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
			// EvalContract.Run takes *flowv1.FeedbackItem — use PB() escape hatch.
			out, err := eval.Run(ctx, t.fb.PB(), inputContent, augmentedReviewContent, t.kind)
			results[idx] = evalResultItem{task: t, out: out, err: err}
		}(i, task)
	}
	wg.Wait()

	// Apply verdicts sequentially (gRPC calls to Archivist).
	// Collect resolved items that carry a novel argument justification.
	var novelResolved []*flow.Feedback

	for _, r := range results {
		if r.err != nil {
			return nil, fmt.Errorf(
				"appraisal: eval feedback %s: %w",
				r.task.fb.GetID(), r.err)
		}

		fbID := r.task.fb.GetID()
		state := r.task.fb.GetState()
		protoFB := r.task.fb.PB()

		switch {
		case state == flow.FeedbackStateActioned &&
			r.out.Verdict == verdictAccept:
			slog.Info("appraisal: accepting fix",
				"feedback_id", fbID, "reason", r.out.Reason)
			if err := r.task.fb.AcceptFix(); err != nil {
				return nil, fmt.Errorf("appraisal: accept fix %s: %w", fbID, err)
			}
			if hasNovelArgument(protoFB) {
				novelResolved = append(novelResolved, r.task.fb)
			}

		case state == flow.FeedbackStateActioned &&
			r.out.Verdict == verdictReject:
			slog.Info("appraisal: rejecting fix",
				"feedback_id", fbID, "reason", r.out.Reason)
			if err := r.task.fb.RejectFix(r.out.Reason); err != nil {
				return nil, fmt.Errorf("appraisal: reject fix %s: %w", fbID, err)
			}

		case state == flow.FeedbackStateWontFix &&
			r.out.Verdict == verdictAccept:
			slog.Info("appraisal: accepting refusal",
				"feedback_id", fbID, "reason", r.out.Reason)
			if err := r.task.fb.AcceptRefusal(); err != nil {
				return nil, fmt.Errorf(
					"appraisal: accept refusal %s: %w", fbID, err)
			}
			if hasNovelArgument(protoFB) {
				novelResolved = append(novelResolved, r.task.fb)
			}

		case state == flow.FeedbackStateWontFix &&
			r.out.Verdict == verdictReject:
			slog.Info("appraisal: rejecting refusal",
				"feedback_id", fbID, "reason", r.out.Reason)
			if err := r.task.fb.RejectRefusal(r.out.Reason); err != nil {
				return nil, fmt.Errorf(
					"appraisal: reject refusal %s: %w", fbID, err)
			}
		}
	}

	return novelResolved, nil
}
