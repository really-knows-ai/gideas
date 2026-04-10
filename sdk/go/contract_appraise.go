package flow

import (
	"context"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

// ---------------------------------------------------------------------------
// Eval Contract (Appraise Phase 1: fix/refusal evaluation)
// ---------------------------------------------------------------------------

// EvalContract defines the boundary between the Appraise handler and its
// evaluation agent implementation. For each ACTIONED or WONT_FIX feedback
// item, the agent decides whether the response is adequate (accept) or
// inadequate (reject).
//
// Prompts, schemas, model choice, and output parsing are all encapsulated
// in the agent implementation.
type EvalContract interface {
	// Run evaluates a single feedback item's resolution. The handler
	// provides the feedback item, the concatenated input artefact content,
	// the current review artefact content, and the evaluation kind
	// ("actioned" or "wont_fix").
	Run(
		ctx context.Context,
		fb *flowv1.FeedbackItem,
		inputContent, reviewContent string,
		kind string,
	) (*EvalResult, error)
}

// EvalResult is the outcome of evaluating a single feedback item's
// resolution.
type EvalResult struct {
	// Verdict is "accept" (resolution adequate) or "reject" (resolution
	// inadequate).
	Verdict string

	// Reason is a brief explanation of the verdict.
	Reason string
}

// ---------------------------------------------------------------------------
// Finding Contract (Appraise Phase 3: learning capture)
// ---------------------------------------------------------------------------

// FindingContract defines the boundary between the Appraise handler and its
// finding agent implementation. The agent distils governance learnings from
// resolved feedback items that carried novel arguments, producing Tier 1
// Finding candidates.
//
// Prompts, schemas, model choice, and output parsing are all encapsulated
// in the agent implementation.
type FindingContract interface {
	// Run distils governance learnings from the given feedback items.
	// All items carry a NovelArgument justification and have been resolved
	// (accepted). Returns nil if no reusable learnings are found.
	Run(
		ctx context.Context,
		items []*flowv1.FeedbackItem,
	) (*FindingsResult, error)
}

// FindingsResult is the output of the finding agent: a list of governance
// learnings distilled from resolved novel arguments.
type FindingsResult struct {
	// Findings is the list of governance learnings. An empty slice means
	// no reusable learnings were found.
	Findings []Finding
}

// Finding is a single governance learning distilled from a feedback
// discussion involving a novel argument.
type Finding struct {
	// Goal is a concise, forward-looking governance statement (1-2
	// sentences) that future reviewers and refiners can reference.
	Goal string

	// AppliesTo lists the GovernedArtefact kind names this finding
	// applies to (e.g. ["haiku"]).
	AppliesTo []string

	// Rationale explains why this learning matters, referencing the
	// discussion that produced it. Preserved as the finding's initial
	// representation.
	Rationale string
}
