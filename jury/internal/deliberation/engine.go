// Package deliberation implements the core Jury deliberation engine.
//
// The engine receives a question, evidence, allowed outcomes, and a panel
// of jurors, then runs multi-round blind voting with consensus checking.
package deliberation

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/jury/internal/jurors"
)

// ---------------------------------------------------------------------------
// Engine
// ---------------------------------------------------------------------------

// Engine orchestrates multi-round deliberation among a panel of jurors.
type Engine struct{}

// NewEngine creates a deliberation engine.
func NewEngine() *Engine {
	return &Engine{}
}

// DeliberationInput holds the parameters for a single deliberation.
type DeliberationInput struct {
	Question          string
	Evidence          string
	AllowedOutcomes   []string
	ConsensusStrategy flowv1.ConsensusStrategy
	MaxRounds         int32
	Panel             []jurors.Juror
}

// DeliberationResult holds the outcome of a deliberation.
type DeliberationResult struct {
	Outcome        string
	Justifications []*flowv1.JurorJustification
	RoundsUsed     int32
	Hung           bool
}

// Deliberate runs the deliberation engine with the given input.
//
// It executes jurors in parallel per round, checks consensus after each round,
// and if hung, augments subsequent rounds with anonymised peer arguments.
func (e *Engine) Deliberate(ctx context.Context, input *DeliberationInput) (*DeliberationResult, error) {
	if len(input.Panel) == 0 {
		return nil, fmt.Errorf("deliberation: panel must not be empty")
	}
	if len(input.AllowedOutcomes) == 0 {
		return nil, fmt.Errorf("deliberation: allowed_outcomes must not be empty")
	}
	if input.MaxRounds <= 0 {
		return nil, fmt.Errorf("deliberation: max_rounds must be positive")
	}

	var lastVotes []*jurorVote

	for round := int32(1); round <= input.MaxRounds; round++ {
		slog.Info("deliberation: starting round",
			"round", round,
			"max_rounds", input.MaxRounds,
			"panel_size", len(input.Panel),
		)

		// Build peer arguments from prior round (empty for round 1).
		peerArgs := ""
		if round > 1 && len(lastVotes) > 0 {
			peerArgs = buildPeerArguments(lastVotes)
		}

		// Run all jurors in parallel.
		votes, err := runRound(ctx, input.Panel, input.Question, input.Evidence, input.AllowedOutcomes, peerArgs)
		if err != nil {
			return nil, fmt.Errorf("deliberation round %d: %w", round, err)
		}

		lastVotes = votes

		// Check consensus.
		outcome, reached := checkConsensus(votes, input.ConsensusStrategy)
		if reached {
			slog.Info("deliberation: consensus reached",
				"round", round,
				"outcome", outcome,
			)
			return &DeliberationResult{
				Outcome:        outcome,
				Justifications: buildJustifications(votes),
				RoundsUsed:     round,
				Hung:           false,
			}, nil
		}

		slog.Info("deliberation: no consensus",
			"round", round,
			"strategy", input.ConsensusStrategy.String(),
		)
	}

	// Hung after all rounds.
	slog.Info("deliberation: hung jury", "rounds_used", input.MaxRounds)
	return &DeliberationResult{
		Outcome:        "",
		Justifications: buildJustifications(lastVotes),
		RoundsUsed:     input.MaxRounds,
		Hung:           true,
	}, nil
}

// ---------------------------------------------------------------------------
// Internal — Round Execution
// ---------------------------------------------------------------------------

// jurorVote holds a single juror's vote from a round.
type jurorVote struct {
	JurorName string
	Outcome   string
	Reasoning string
}

// runRound executes all jurors in parallel for a single voting round.
func runRound(
	ctx context.Context,
	panel []jurors.Juror,
	question, evidence string,
	allowedOutcomes []string,
	peerArguments string,
) ([]*jurorVote, error) {
	data := jurors.JurorQueryData{
		Question:        question,
		Evidence:        evidence,
		AllowedOutcomes: allowedOutcomes,
		PeerArguments:   peerArguments,
	}

	votes := make([]*jurorVote, len(panel))
	errs := make([]error, len(panel))

	var wg sync.WaitGroup
	wg.Add(len(panel))

	for i, j := range panel {
		go func(idx int, juror jurors.Juror) {
			defer wg.Done()

			output, err := juror.Run(ctx, data)
			if err != nil {
				errs[idx] = err
				return
			}

			votes[idx] = &jurorVote{
				JurorName: juror.Name(),
				Outcome:   output.Outcome,
				Reasoning: output.Reasoning,
			}
		}(i, j)
	}

	wg.Wait()

	// Check for errors — fail the round if any juror fails.
	for i, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("juror %d (%s): %w", i, panel[i].Name(), err)
		}
	}

	return votes, nil
}

// ---------------------------------------------------------------------------
// Internal — Consensus Checking
// ---------------------------------------------------------------------------

// checkConsensus checks whether the votes meet the specified consensus strategy.
// Returns the winning outcome and whether consensus was reached.
func checkConsensus(votes []*jurorVote, strategy flowv1.ConsensusStrategy) (string, bool) {
	if len(votes) == 0 {
		return "", false
	}

	// Count votes per outcome.
	counts := make(map[string]int)
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

	switch strategy {
	case flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY:
		// >50% of votes.
		if bestCount*2 > total {
			return bestOutcome, true
		}
	case flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SUPER_MAJORITY:
		// >=66% of votes.
		if bestCount*3 >= total*2 {
			return bestOutcome, true
		}
	case flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_UNANIMITY:
		// 100% of votes.
		if bestCount == total {
			return bestOutcome, true
		}
	default:
		// UNSPECIFIED — treat as simple majority.
		if bestCount*2 > total {
			return bestOutcome, true
		}
	}

	return "", false
}

// ---------------------------------------------------------------------------
// Internal — Peer Arguments
// ---------------------------------------------------------------------------

// buildPeerArguments constructs an anonymised summary of prior-round reasoning
// for use in subsequent rounds. Juror identities are stripped.
func buildPeerArguments(votes []*jurorVote) string {
	var b strings.Builder
	for i, v := range votes {
		fmt.Fprintf(&b, "Juror %d voted \"%s\":\n%s\n", i+1, v.Outcome, v.Reasoning)
		if i < len(votes)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Internal — Justification Building
// ---------------------------------------------------------------------------

// buildJustifications converts internal votes into proto JurorJustification messages.
func buildJustifications(votes []*jurorVote) []*flowv1.JurorJustification {
	justifications := make([]*flowv1.JurorJustification, len(votes))
	for i, v := range votes {
		justifications[i] = &flowv1.JurorJustification{
			JurorId:   v.JurorName,
			Outcome:   v.Outcome,
			Reasoning: v.Reasoning,
		}
	}
	return justifications
}
