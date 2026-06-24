package flow

import (
	"context"
)

// ReviewContract defines the boundary between the Reviewer handler and its
// review agent implementation. The agent produces fresh feedback observations
// for the artefact under review.
//
// Prompts, schemas, model choice, and output parsing are all encapsulated
// in the agent implementation.
type ReviewContract interface {
	// Run reviews the given content and returns feedback observations.
	// inputContent is the concatenated input artefact content (creative
	// brief / petition), reviewContent is the artefact under review, laws
	// are the governance laws to review against, and history is the prior
	// feedback state for deduplication.
	Run(
		ctx context.Context,
		inputContent, reviewContent string,
		laws []ReviewLaw,
		history []ReviewHistory,
	) (*ReviewResult, error)
}

// ReviewResult is the output of a review agent: a list of feedback
// observations.
type ReviewResult struct {
	// Feedback is the list of new feedback observations. An empty slice
	// means the reviewer found no issues.
	Feedback []ReviewFeedback
}

// ReviewFeedback is a single feedback observation from a review agent.
type ReviewFeedback struct {
	// Message is a specific, actionable observation stating the deviation
	// (1-2 sentences).
	Message string

	// CitedLaws lists governance law IDs that this feedback references.
	// Empty if the observation is novel (not tied to a specific law).
	CitedLaws []string
}

// ReviewLaw is the minimal law representation provided to review agents.
// Only the fields needed for review are included.
type ReviewLaw struct {
	// ID is the law's unique identifier (for citation).
	ID string

	// Tier is the law's governance tier (1-5).
	Tier int32

	// Goal is the law's governance goal statement.
	Goal string
}

// ReviewHistory is a single prior feedback entry provided to review agents
// for deduplication. Agents should not re-raise resolved items.
type ReviewHistory struct {
	// State is the feedback state (e.g. "FEEDBACK_STATE_RESOLVED").
	State string

	// Message is the feedback message text.
	Message string
}
