// Shared deliberation and resume helpers for the judiciary node binaries.
//
// The Arbiter and Tribunal both run a multi-round jury fan-out loop and the
// Facilitator and Arbiter both detect resume phase from completed children
// and branch on a child's CompletionReason after resume. This file holds
// that shared orchestration so the sibling binaries do not copy divergent
// versions of the same sequence.
package tally

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/nodes/internal/nodeconfig"
	flow "github.com/foundry/flow/sdk/go"
)

// ---------------------------------------------------------------------------
// Defaults
// ---------------------------------------------------------------------------

const (
	// DefaultJurySize is the fallback number of jurors per round.
	DefaultJurySize int32 = 5

	// DefaultJurorNode is the fallback FoundryNode for juror children.
	DefaultJurorNode = "juror"

	// DefaultClerkNode is the fallback FoundryNode for the clerk child.
	DefaultClerkNode = "clerk-forge"

	// DefaultHungOutput is the fallback output name when the jury hangs.
	DefaultHungOutput = "hung"

	// DefaultMaxRounds is the fallback number of deliberation rounds.
	DefaultMaxRounds = 3
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// JuryConfig holds the runtime configuration shared by the jury-based
// judiciary nodes (Arbiter and Tribunal).
type JuryConfig struct {
	JurySize          int32  `yaml:"jurySize"`
	JurorNode         string `yaml:"jurorNode"`
	ConsensusStrategy string `yaml:"consensusStrategy"`
	MaxRounds         int    `yaml:"maxRounds"`
	ClerkNode         string `yaml:"clerkNode"`
	HungOutput        string `yaml:"hungOutput"`
}

// EffectiveJurySize returns the configured jury size, falling back to
// DefaultJurySize when unset.
func (c *JuryConfig) EffectiveJurySize() int32 {
	if c.JurySize < 1 {
		return DefaultJurySize
	}
	return c.JurySize
}

// EffectiveJurorNode returns the configured juror node, falling back to
// DefaultJurorNode when unset.
func (c *JuryConfig) EffectiveJurorNode() string {
	if c.JurorNode == "" {
		return DefaultJurorNode
	}
	return c.JurorNode
}

// EffectiveClerkNode returns the configured clerk node, falling back to
// DefaultClerkNode when unset.
func (c *JuryConfig) EffectiveClerkNode() string {
	if c.ClerkNode == "" {
		return DefaultClerkNode
	}
	return c.ClerkNode
}

// EffectiveHungOutput returns the configured hung output, falling back to
// DefaultHungOutput when unset.
func (c *JuryConfig) EffectiveHungOutput() string {
	if c.HungOutput == "" {
		return DefaultHungOutput
	}
	return c.HungOutput
}

// EffectiveMaxRounds returns the configured round count, falling back to
// DefaultMaxRounds when unset.
func (c *JuryConfig) EffectiveMaxRounds() int {
	if c.MaxRounds < 1 {
		return DefaultMaxRounds
	}
	return c.MaxRounds
}

// EffectiveConsensusStrategy parses the configured strategy string.
func (c *JuryConfig) EffectiveConsensusStrategy() flowv1.ConsensusStrategy {
	return nodeconfig.ParseConsensusStrategy(c.ConsensusStrategy)
}

// ---------------------------------------------------------------------------
// Verdict context
// ---------------------------------------------------------------------------

// VerdictContext carries a judiciary node's prose decision for downstream
// Clerk consumption. Two fields only — no structured fields.
type VerdictContext struct {
	Trigger  string `json:"trigger"`
	Decision string `json:"decision"`
}

// ---------------------------------------------------------------------------
// Phase detection and resume
// ---------------------------------------------------------------------------

// HasCompletedChild returns true if at least one child is in the Completed
// phase, indicating a post-resume invocation.
func HasCompletedChild(children []flow.ChildWorkitemStatus) bool {
	for _, ch := range children {
		if ch.Phase == flow.PhaseCompleted {
			return true
		}
	}
	return false
}

// HandlePostResume runs the shared post-resume sequence: find the first
// completed child, fail with a labelled error if none exists, and dispatch
// to onCancelled or onSuccess based on the child's CompletionReason.
func HandlePostResume(
	label string,
	workitem *flow.Workitem,
	children []flow.ChildWorkitemStatus,
	onCancelled func(child *flow.ChildWorkitemStatus) error,
	onSuccess func(child *flow.ChildWorkitemStatus) error,
) error {
	var completed *flow.ChildWorkitemStatus
	for i := range children {
		if children[i].Phase == flow.PhaseCompleted {
			completed = &children[i]
			break
		}
	}

	if completed == nil {
		return fmt.Errorf("%s: post-resume but no completed child found", label)
	}

	slog.Info(label+": post-resume",
		"child_id", completed.WorkitemID,
		"completion_reason", completed.CompletionReason,
	)

	if completed.CompletionReason == flowv1.CompletionReason_COMPLETION_REASON_CANCELLED.String() {
		return onCancelled(completed)
	}
	return onSuccess(completed)
}

// ---------------------------------------------------------------------------
// Deliberation loop
// ---------------------------------------------------------------------------

// Deliberate runs the multi-round jury deliberation loop: fan out to jurors,
// await and collect votes, tally them, and retry with prior-round reasoning
// until consensus or max rounds. label prefixes log lines and error strings
// so output is attributed to the calling node; logAttrs are appended to each
// round's log lines.
func Deliberate(
	ctx context.Context,
	client *flow.Client,
	workitem *flow.Workitem,
	cfg TallyConfig,
	input RoundInput,
	label string,
	logAttrs ...any,
) (TallyResult, error) {
	var priorReasoning string
	var lastResult TallyResult

	for round := 1; round <= cfg.MaxRounds; round++ {
		slog.Info(label+": deliberation round",
			append([]any{"round", round, "max_rounds", cfg.MaxRounds}, logAttrs...)...,
		)

		roundInput := input
		roundInput.PriorRoundReasoning = priorReasoning
		tasks, buildErr := BuildFanOutTasks(cfg, roundInput)
		if buildErr != nil {
			return TallyResult{}, fmt.Errorf("%s: build fan-out tasks (round %d): %w", label, round, buildErr)
		}

		roundChildren, fanErr := workitem.FanOut(tasks)
		if fanErr != nil {
			return TallyResult{}, fmt.Errorf("%s: fan-out (round %d): %w", label, round, fanErr)
		}

		allCompleted, awaitErr := workitem.AwaitAll()
		if awaitErr != nil {
			return TallyResult{}, fmt.Errorf("%s: await children (round %d): %w", label, round, awaitErr)
		}

		roundCompleted := filterRoundChildren(allCompleted, roundChildren)
		votes, collectErr := CollectVotes(ctx, client, workitem.ID(), roundCompleted)
		if collectErr != nil {
			return TallyResult{}, fmt.Errorf("%s: collect votes (round %d): %w", label, round, collectErr)
		}

		result := Tally(votes, cfg.ConsensusStrategy)
		result.Round = round
		lastResult = result

		slog.Info(label+": tally result",
			append([]any{
				"round", round,
				"consensus", result.IsConsensus,
				"outcome", result.Outcome,
				"vote_count", len(votes),
			}, logAttrs...)...,
		)

		if result.IsConsensus {
			break
		}
		if round < cfg.MaxRounds {
			priorReasoning = SummariseRound(votes)
		}
	}

	return lastResult, nil
}

// filterRoundChildren narrows AwaitAll's full child list down to the
// children created by the current round's fan-out.
func filterRoundChildren(
	allCompleted []flow.ChildWorkitemStatus,
	roundChildren []*flow.ChildWorkitem,
) []flow.ChildWorkitemStatus {
	roundChildIDs := make(map[string]bool, len(roundChildren))
	for _, ch := range roundChildren {
		roundChildIDs[ch.ID()] = true
	}

	roundCompleted := make([]flow.ChildWorkitemStatus, 0, len(roundChildren))
	for _, ch := range allCompleted {
		if roundChildIDs[ch.WorkitemID] {
			roundCompleted = append(roundCompleted, ch)
		}
	}
	return roundCompleted
}

// ---------------------------------------------------------------------------
// Decision prose
// ---------------------------------------------------------------------------

// SupportingArguments renders the prose summary of juror reasoning for the
// winning outcome — the tail of a synthesized decision. Returns an empty
// string when no supporting reasoning exists.
func SupportingArguments(result TallyResult) string {
	var supporting []string
	for _, v := range result.Votes {
		if v.Outcome == result.Outcome && v.Reasoning != "" {
			supporting = append(supporting, v.Reasoning)
		}
	}
	if len(supporting) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("Supporting arguments: ")
	for i, reason := range supporting {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(reason)
	}
	b.WriteString(".")
	return b.String()
}
