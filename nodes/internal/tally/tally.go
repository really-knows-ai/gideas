// Package tally provides shared consensus tally logic for judiciary nodes.
//
// Both the Arbiter (deadlock resolution) and the Tribunal (law review) fan
// out to juror nodes and need to count votes, check consensus, and build
// retry rounds. This package extracts that common logic.
package tally

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
)

// ---------------------------------------------------------------------------
// Well-known artefact IDs
// ---------------------------------------------------------------------------

const (
	// ArtefactQuestion is the artefact ID for the deliberation question.
	ArtefactQuestion = "question"

	// ArtefactEvidence is the artefact ID for the evidence bundle.
	ArtefactEvidence = "evidence"

	// ArtefactOutcomes is the artefact ID for allowed outcomes (JSON array).
	ArtefactOutcomes = "allowed-outcomes"

	// ArtefactPriorRound is the artefact ID for prior-round reasoning.
	ArtefactPriorRound = "prior-round-reasoning"

	// ArtefactVerdict is the artefact ID for a juror's vote.
	ArtefactVerdict = "verdict"

	// GovernedQuestion is the GovernedArtefact name for question.
	GovernedQuestion = "question"

	// GovernedEvidence is the GovernedArtefact name for evidence.
	GovernedEvidence = "evidence"

	// GovernedOutcomes is the GovernedArtefact name for allowed-outcomes.
	GovernedOutcomes = "allowed-outcomes"

	// GovernedPriorRound is the GovernedArtefact name for prior-round-reasoning.
	GovernedPriorRound = "prior-round-reasoning"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// TallyConfig holds configuration for a deliberation round.
type TallyConfig struct {
	// ConsensusStrategy determines how votes are counted.
	ConsensusStrategy flowv1.ConsensusStrategy

	// MaxRounds is the maximum number of deliberation rounds before
	// declaring hung. Must be >= 1.
	MaxRounds int

	// JurySize is the number of jurors to fan out to per round.
	JurySize int

	// JurorNode is the FoundryNode name to route juror children to.
	JurorNode string
}

// JurorVote is a single juror's vote parsed from the verdict artefact.
type JurorVote struct {
	Outcome   string `json:"outcome"`
	Reasoning string `json:"reasoning"`
}

// TallyResult holds the result of tallying a set of juror votes.
type TallyResult struct {
	// Outcome is the winning outcome, or empty if hung.
	Outcome string

	// IsConsensus is true when the votes meet the consensus strategy.
	IsConsensus bool

	// IsHung is true when no outcome meets the consensus threshold.
	IsHung bool

	// Votes is the original set of votes that were tallied.
	Votes []JurorVote

	// Round is the deliberation round number (1-indexed, set by caller).
	Round int
}

// RoundInput holds the inputs for a single deliberation round.
type RoundInput struct {
	// Question is the deliberation question posed to jurors.
	Question string

	// Evidence is the evidence bundle (typically markdown).
	Evidence string

	// AllowedOutcomes lists the valid vote values.
	AllowedOutcomes []string

	// PriorRoundReasoning is the anonymised reasoning from the previous
	// round. Empty on round 1.
	PriorRoundReasoning string
}

// ---------------------------------------------------------------------------
// Tally
// ---------------------------------------------------------------------------

// Tally counts votes and applies the consensus strategy.
//
// Returns a TallyResult with IsConsensus=true and the winning Outcome if
// the threshold is met, or IsHung=true with an empty Outcome otherwise.
// An empty votes slice is always hung.
func Tally(votes []JurorVote, strategy flowv1.ConsensusStrategy) TallyResult {
	if len(votes) == 0 {
		return TallyResult{IsHung: true, Votes: votes}
	}

	// Count votes per outcome.
	counts := make(map[string]int, len(votes))
	for _, v := range votes {
		counts[v.Outcome]++
	}

	// Find the outcome with the most votes.
	bestOutcome := ""
	bestCount := 0
	for outcome, count := range counts {
		if count > bestCount {
			bestOutcome = outcome
			bestCount = count
		}
	}

	total := len(votes)
	consensus := checkThreshold(bestCount, total, strategy)

	if consensus {
		return TallyResult{
			Outcome:     bestOutcome,
			IsConsensus: true,
			Votes:       votes,
		}
	}
	return TallyResult{
		IsHung: true,
		Votes:  votes,
	}
}

// checkThreshold applies the strategy threshold to determine consensus.
func checkThreshold(best, total int, strategy flowv1.ConsensusStrategy) bool {
	switch strategy {
	case flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SUPER_MAJORITY:
		// >= 66% of votes.
		return best*3 >= total*2
	case flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_UNANIMITY:
		// 100% of votes.
		return best == total
	default:
		// SIMPLE_MAJORITY and UNSPECIFIED: > 50% of votes.
		return best*2 > total
	}
}

// ---------------------------------------------------------------------------
// BuildFanOutTasks
// ---------------------------------------------------------------------------

// BuildFanOutTasks builds fan-out tasks for a deliberation round.
//
// It creates cfg.JurySize tasks, each targeting cfg.JurorNode, with the
// question, evidence, allowed-outcomes, and (if non-empty) prior-round
// reasoning artefacts attached.
func BuildFanOutTasks(cfg TallyConfig, input RoundInput) ([]flow.FanOutTask, error) {
	if cfg.JurySize < 1 {
		return nil, fmt.Errorf("tally: jury size must be >= 1, got %d", cfg.JurySize)
	}
	if cfg.JurorNode == "" {
		return nil, fmt.Errorf("tally: juror node name is required")
	}
	if input.Question == "" {
		return nil, fmt.Errorf("tally: question is required")
	}

	outcomesJSON, err := json.Marshal(input.AllowedOutcomes)
	if err != nil {
		return nil, fmt.Errorf("tally: marshal allowed-outcomes: %w", err)
	}

	tasks := make([]flow.FanOutTask, cfg.JurySize)
	for i := range tasks {
		artefacts := []flow.ChildArtefact{
			{ID: ArtefactQuestion, GovernedArtefact: GovernedQuestion, Content: []byte(input.Question)},
			{ID: ArtefactEvidence, GovernedArtefact: GovernedEvidence, Content: []byte(input.Evidence)},
			{ID: ArtefactOutcomes, GovernedArtefact: GovernedOutcomes, Content: outcomesJSON},
		}
		if input.PriorRoundReasoning != "" {
			artefacts = append(artefacts, flow.ChildArtefact{
				ID:               ArtefactPriorRound,
				GovernedArtefact: GovernedPriorRound,
				Content:          []byte(input.PriorRoundReasoning),
			})
		}
		tasks[i] = flow.FanOutTask{
			TargetNode: cfg.JurorNode,
			Artefacts:  artefacts,
		}
	}
	return tasks, nil
}

// ---------------------------------------------------------------------------
// CollectVotes
// ---------------------------------------------------------------------------

// CollectVotes reads verdict artefacts from completed children and parses
// them into JurorVote values. The order matches the children slice.
//
// If a child is in the Failed phase, CollectArtefacts returns an error
// (propagated to the caller). If a child has no verdict artefact, it is
// skipped with a warning — the vote count will be lower than expected.
func CollectVotes(
	ctx context.Context,
	client *flow.Client,
	children []flow.ChildWorkitemStatus,
) ([]JurorVote, error) {
	results, err := client.CollectArtefacts(ctx, children, ArtefactVerdict)
	if err != nil {
		return nil, fmt.Errorf("tally: collect verdict artefacts: %w", err)
	}

	votes := make([]JurorVote, 0, len(results))
	for _, r := range results {
		raw := r.Artefacts[ArtefactVerdict]
		if raw == nil {
			// Child completed but produced no verdict — skip.
			continue
		}
		var v JurorVote
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("tally: unmarshal verdict from child %s: %w",
				r.Status.WorkitemID, err)
		}
		votes = append(votes, v)
	}
	return votes, nil
}

// ---------------------------------------------------------------------------
// SummariseRound
// ---------------------------------------------------------------------------

// SummariseRound builds an anonymised summary of juror reasoning from the
// given votes. This is passed to jurors in subsequent rounds so they can
// consider peer arguments without knowing who said what.
//
// Returns an empty string if votes is empty.
func SummariseRound(votes []JurorVote) string {
	if len(votes) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("Prior round reasoning:\n\n")
	for i, v := range votes {
		fmt.Fprintf(&b, "Juror %d (voted %q):\n%s\n\n", i+1, v.Outcome, v.Reasoning)
	}
	return b.String()
}
