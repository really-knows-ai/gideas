package flow

import (
	"context"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

// ---------------------------------------------------------------------------
// Triage Contract (Refine Phase 1: per-item decision)
// ---------------------------------------------------------------------------

// TriageContract defines the boundary between the Refine handler and its
// triage agent implementation. For each feedback item, the agent decides
// whether to action (fix) or refuse (won't fix).
//
// Prompts, schemas, model choice, and output parsing are all encapsulated
// in the agent implementation.
type TriageContract interface {
	// Run triages a single feedback item. The handler provides the
	// feedback item, the concatenated input artefact content, the current
	// output artefact content, and applicable governance laws.
	Run(
		ctx context.Context,
		fb *flowv1.FeedbackItem,
		inputContent, reviewContent string,
		laws []*flowv1.Law,
	) (*TriageResult, error)
}

// TriageResult is the outcome of triaging a single feedback item.
type TriageResult struct {
	// Decision is "action" (will fix) or "refuse" (won't fix).
	Decision string

	// Message is a human-readable explanation of the decision. For
	// "action", it describes the fix to be applied. For "refuse", it
	// explains why the feedback is being declined.
	Message string

	// JustificationType is set when Decision is "refuse". Valid values
	// are "citation" (existing law supports the refusal) or
	// "novel_argument" (new reasoning not covered by law).
	JustificationType string

	// CitationIDs lists governance law IDs cited when JustificationType
	// is "citation".
	CitationIDs []string

	// Argument is the free-text reasoning when JustificationType is
	// "novel_argument".
	Argument string
}

// ---------------------------------------------------------------------------
// Revision Contract (Refine Phase 2: content revision)
// ---------------------------------------------------------------------------

// RevisionContract defines the boundary between the Refine handler and its
// revision agent implementation. The agent takes the original input, current
// content, governance laws, and the list of committed fixes, and produces
// revised content.
//
// Prompts, schemas, model choice, and output parsing are all encapsulated
// in the agent implementation.
type RevisionContract interface {
	// Run produces revised content incorporating the committed fixes.
	// inputContent is the concatenated input artefact content,
	// reviewContent is the current output artefact content, laws are the
	// active governance laws, and fixes are the feedback items that the
	// triage phase committed to actioning.
	Run(
		ctx context.Context,
		inputContent, reviewContent string,
		laws []*flowv1.Law,
		fixes []ActionedFeedback,
	) (string, error)
}

// ActionedFeedback represents a feedback item that the triage phase
// committed to fixing. The revision agent uses this to understand what
// changes are expected.
type ActionedFeedback struct {
	// FeedbackID is the unique identifier of the feedback item.
	FeedbackID string

	// Message is the original feedback message.
	Message string

	// FixDescription is the triage agent's description of the fix to apply.
	FixDescription string
}
