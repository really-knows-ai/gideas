// Arbiter is the deadlock resolution node of the Foundry Judiciary.
//
// When the Facilitator detects that a feedback cycle has deadlocked, it
// assembles an evidence-bundle artefact and routes the Workitem to the
// Arbiter. The Arbiter frames a question, fans out to Juror nodes for
// deliberation, tallies votes, and resolves the dispute.
//
// The handler uses a two-invocation pattern:
//
//  1. First invocation: run a deliberation loop (up to maxRounds),
//     fan out to Jurors each round, and tally votes.
//     - Resolved (consensus outcome = "resolved"): Complete() directly.
//     - Consensus (law change needed): synthesize prose decision, store
//     verdict-context, create Clerk child, Suspend().
//     - Hung (max rounds exhausted): RouteToOutput("hung").
//
//  2. Post-resume: check the Clerk child's CompletionReason.
//     - Cancelled → Complete(WithReason(cancelled)).
//     - Success → Complete().
//
// Configuration (YAML via NODE_CONFIG_PATH):
//
//	jurySize:            5                 # jurors per round
//	jurorNode:           juror             # FoundryNode for juror children
//	consensusStrategy:   SIMPLE_MAJORITY   # SIMPLE_MAJORITY | SUPER_MAJORITY | UNANIMITY
//	maxRounds:           3                 # max deliberation rounds
//	clerkNode:           clerk-forge       # FoundryNode for clerk child
//	hungOutput:          hung              # output name when max rounds exhausted
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/nodeconfig"
	"github.com/gideas/flow/nodes/internal/tally"
	flow "github.com/gideas/flow/sdk/go"
)

// ---------------------------------------------------------------------------
// Well-known artefact IDs
// ---------------------------------------------------------------------------

const (
	// artefactEvidenceBundle is the pre-assembled evidence artefact written
	// by the Facilitator.
	artefactEvidenceBundle = "evidence-bundle"

	// artefactVerdictContext is the prose verdict-context artefact stored
	// on the Clerk child for downstream consumption.
	artefactVerdictContext = "verdict-context"
)

// ---------------------------------------------------------------------------
// Well-known outcomes
// ---------------------------------------------------------------------------

const (
	// outcomeResolved indicates the jury decided no law change is needed.
	outcomeResolved = "resolved"

	// outcomeLawChange indicates the jury decided a law change is needed.
	outcomeLawChange = "law-change"
)

// ---------------------------------------------------------------------------
// Defaults
// ---------------------------------------------------------------------------

const (
	defaultJurySize   int32 = 5
	defaultJurorNode        = "juror"
	defaultClerkNode        = "clerk-forge"
	defaultHungOutput       = "hung"
	defaultMaxRounds        = 3
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// arbiterConfig holds the Arbiter's runtime configuration.
type arbiterConfig struct {
	JurySize          int32  `yaml:"jurySize"`
	JurorNode         string `yaml:"jurorNode"`
	ConsensusStrategy string `yaml:"consensusStrategy"`
	MaxRounds         int    `yaml:"maxRounds"`
	ClerkNode         string `yaml:"clerkNode"`
	HungOutput        string `yaml:"hungOutput"`
}

func (c *arbiterConfig) jurySize() int32 {
	if c.JurySize < 1 {
		return defaultJurySize
	}
	return c.JurySize
}

func (c *arbiterConfig) jurorNode() string {
	if c.JurorNode == "" {
		return defaultJurorNode
	}
	return c.JurorNode
}

func (c *arbiterConfig) clerkNode() string {
	if c.ClerkNode == "" {
		return defaultClerkNode
	}
	return c.ClerkNode
}

func (c *arbiterConfig) hungOutput() string {
	if c.HungOutput == "" {
		return defaultHungOutput
	}
	return c.HungOutput
}

func (c *arbiterConfig) maxRounds() int {
	if c.MaxRounds < 1 {
		return defaultMaxRounds
	}
	return c.MaxRounds
}

func (c *arbiterConfig) consensusStrategy() flowv1.ConsensusStrategy {
	return nodeconfig.ParseConsensusStrategy(c.ConsensusStrategy)
}

// ---------------------------------------------------------------------------
// Verdict Context (prose-only)
// ---------------------------------------------------------------------------

// verdictContext carries the Arbiter's prose decision for downstream Clerk
// consumption. Two fields only — no structured fields.
type verdictContext struct {
	Trigger  string `json:"trigger"`
	Decision string `json:"decision"`
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

func main() {
	slog.Info("arbiter: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("arbiter: server failed", "error", err)
		os.Exit(1)
	}
}

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("arbiter: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("arbiter: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	cfg, err := nodeconfig.Load[arbiterConfig](nodeconfig.Path())
	if err != nil {
		return fmt.Errorf("arbiter: load config: %w", err)
	}

	return handleArbiter(ctx, client, cfg)
}

// ---------------------------------------------------------------------------
// Core logic
// ---------------------------------------------------------------------------

// handleArbiter contains all Arbiter logic, separated from handler
// boilerplate for testability.
//
// Phase detection: if GetChildren returns any completed children, this is a
// post-resume invocation. Otherwise it is the first invocation.
func handleArbiter(ctx context.Context, client *flow.Client, cfg *arbiterConfig) error {
	// ── Heartbeat ────────────────────────────────────────────────────
	_, _ = client.Heartbeat(ctx)

	// ── Phase detection ──────────────────────────────────────────────
	// Use the raw Operator stub because the SDK's GetChildren() strips
	// CompletionReason from the response.
	resp, err := client.Operator.GetChildren(ctx, &flowv1.GetChildrenRequest{})
	if err != nil {
		return fmt.Errorf("arbiter: get children: %w", err)
	}

	children := resp.GetChildren()
	if hasCompletedChild(children) {
		return handlePostResume(ctx, client, children)
	}

	return handleFirstInvocation(ctx, client, cfg)
}

// hasCompletedChild returns true if at least one child is in the Completed
// phase, indicating this is a post-resume invocation.
func hasCompletedChild(children []*flowv1.ChildWorkitemStatus) bool {
	for _, ch := range children {
		if ch.GetPhase() == flow.PhaseCompleted {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// First invocation — deliberation loop
// ---------------------------------------------------------------------------

func handleFirstInvocation(ctx context.Context, client *flow.Client, cfg *arbiterConfig) error {
	// ── Step 1: Read evidence-bundle artefact ────────────────────────
	evidenceResp, err := client.GetArtefact(ctx, artefactEvidenceBundle)
	if err != nil {
		return fmt.Errorf("arbiter: read evidence-bundle: %w", err)
	}
	evidence := string(evidenceResp.GetContent())

	// ── Step 2: Frame question ──────────────────────────────────────
	question := "Should the reviewer's feedback be upheld (requiring a law change), " +
		"or should the dispute be resolved without a law change?"
	allowedOutcomes := []string{outcomeLawChange, outcomeResolved}

	// ── Step 3: Deliberation loop ───────────────────────────────────
	tallyCfg := tally.TallyConfig{
		ConsensusStrategy: cfg.consensusStrategy(),
		MaxRounds:         cfg.maxRounds(),
		JurySize:          int(cfg.jurySize()),
		JurorNode:         cfg.jurorNode(),
	}

	var priorReasoning string
	var lastResult tally.TallyResult

	for round := 1; round <= tallyCfg.MaxRounds; round++ {
		slog.Info("arbiter: deliberation round",
			"round", round,
			"max_rounds", tallyCfg.MaxRounds,
		)

		// Build fan-out tasks.
		input := tally.RoundInput{
			Question:            question,
			Evidence:            evidence,
			AllowedOutcomes:     allowedOutcomes,
			PriorRoundReasoning: priorReasoning,
		}
		tasks, buildErr := tally.BuildFanOutTasks(tallyCfg, input)
		if buildErr != nil {
			return fmt.Errorf("arbiter: build fan-out tasks (round %d): %w", round, buildErr)
		}

		// Fan out to jurors.
		roundChildren, fanErr := client.FanOut(ctx, tasks)
		if fanErr != nil {
			return fmt.Errorf("arbiter: fan-out (round %d): %w", round, fanErr)
		}

		// Await all children (returns all children, including prior rounds).
		allCompleted, awaitErr := client.AwaitChildren(ctx, flow.WithPollingInterval(time.Millisecond))
		if awaitErr != nil {
			return fmt.Errorf("arbiter: await children (round %d): %w", round, awaitErr)
		}

		// Filter to only this round's children for vote collection.
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

		// Collect votes from this round's children only.
		votes, collectErr := tally.CollectVotes(ctx, client, roundCompleted)
		if collectErr != nil {
			return fmt.Errorf("arbiter: collect votes (round %d): %w", round, collectErr)
		}

		// Tally votes.
		result := tally.Tally(votes, tallyCfg.ConsensusStrategy)
		result.Round = round
		lastResult = result

		slog.Info("arbiter: tally result",
			"round", round,
			"consensus", result.IsConsensus,
			"outcome", result.Outcome,
			"vote_count", len(votes),
		)

		if result.IsConsensus {
			break
		}

		// Hung this round — build prior-round reasoning for retry.
		if round < tallyCfg.MaxRounds {
			priorReasoning = tally.SummariseRound(votes)
		}
	}

	// ── Step 4: Post-loop outcomes ──────────────────────────────────
	return handleDeliberationOutcome(ctx, client, cfg, lastResult)
}

// handleDeliberationOutcome branches on the tally result.
func handleDeliberationOutcome(
	ctx context.Context,
	client *flow.Client,
	cfg *arbiterConfig,
	result tally.TallyResult,
) error {
	// Hung — max rounds exhausted with no consensus.
	if result.IsHung {
		slog.Info("arbiter: hung after max rounds, routing to hung output")
		if _, err := client.RouteToOutput(ctx, cfg.hungOutput()); err != nil {
			return fmt.Errorf("arbiter: route to hung: %w", err)
		}
		return nil
	}

	// Resolved — jury says no law change needed.
	if result.Outcome == outcomeResolved {
		slog.Info("arbiter: resolved, completing")
		if _, err := client.Complete(ctx); err != nil {
			return fmt.Errorf("arbiter: complete (resolved): %w", err)
		}
		return nil
	}

	// Consensus for law change — create Clerk child and suspend.
	return spawnClerkAndSuspend(ctx, client, cfg, result)
}

// spawnClerkAndSuspend synthesizes the prose verdict-context, creates a
// Clerk child with the verdict-context artefact, and suspends.
func spawnClerkAndSuspend(
	ctx context.Context,
	client *flow.Client,
	cfg *arbiterConfig,
	result tally.TallyResult,
) error {
	// Synthesize prose decision from jury reasoning.
	decision := synthesizeDecision(result)

	vctx := verdictContext{
		Trigger:  "deadlock-resolution",
		Decision: decision,
	}
	vctxJSON, err := json.Marshal(vctx)
	if err != nil {
		return fmt.Errorf("arbiter: marshal verdict-context: %w", err)
	}

	// Create Clerk child.
	child, err := client.CreateChildWorkitem(ctx)
	if err != nil {
		return fmt.Errorf("arbiter: create clerk child: %w", err)
	}

	// Store verdict-context on the child.
	if _, err := child.StoreArtefact(ctx, artefactVerdictContext, "", vctxJSON); err != nil {
		return fmt.Errorf("arbiter: store verdict-context on child: %w", err)
	}

	// Route child to clerk node.
	if _, err := child.RouteTo(ctx, cfg.clerkNode()); err != nil {
		return fmt.Errorf("arbiter: route child to clerk: %w", err)
	}

	slog.Info("arbiter: clerk child created, suspending",
		"child_id", child.ID(),
		"clerk_node", cfg.clerkNode(),
	)

	// Suspend until clerk child completes.
	if err := client.Suspend(ctx,
		flow.WithCondition(`children.all(c, c.phase == "Completed")`),
	); err != nil {
		return fmt.Errorf("arbiter: suspend: %w", err)
	}

	return nil
}

// synthesizeDecision builds a natural-language decision summary from jury
// votes. This is the prose that the downstream Clerk uses to draft the
// petition.
func synthesizeDecision(result tally.TallyResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The jury reached consensus for %q after %d round(s). ", result.Outcome, result.Round)

	// Collect reasoning from jurors who voted for the winning outcome.
	var supporting []string
	for _, v := range result.Votes {
		if v.Outcome == result.Outcome && v.Reasoning != "" {
			supporting = append(supporting, v.Reasoning)
		}
	}

	if len(supporting) > 0 {
		b.WriteString("Supporting arguments: ")
		for i, reason := range supporting {
			if i > 0 {
				b.WriteString("; ")
			}
			b.WriteString(reason)
		}
		b.WriteString(".")
	}

	return b.String()
}

// ---------------------------------------------------------------------------
// Post-resume — check Clerk child outcome
// ---------------------------------------------------------------------------

// handlePostResume runs after the Operator re-dispatches the Arbiter because
// the suspend condition was met (clerk child completed). It checks the
// child's CompletionReason and completes accordingly.
func handlePostResume(
	ctx context.Context,
	client *flow.Client,
	children []*flowv1.ChildWorkitemStatus,
) error {
	// Find the first completed child.
	var completed *flowv1.ChildWorkitemStatus
	for _, ch := range children {
		if ch.GetPhase() == flow.PhaseCompleted {
			completed = ch
			break
		}
	}

	if completed == nil {
		return fmt.Errorf("arbiter: post-resume but no completed child found")
	}

	slog.Info("arbiter: post-resume",
		"child_id", completed.GetWorkitemId(),
		"completion_reason", completed.GetCompletionReason().String(),
	)

	reason := completed.GetCompletionReason()

	if reason == flowv1.CompletionReason_COMPLETION_REASON_CANCELLED {
		slog.Info("arbiter: clerk child cancelled, propagating cancellation")
		if _, err := client.Complete(ctx, flow.WithReason(
			flowv1.CompletionReason_COMPLETION_REASON_CANCELLED,
		)); err != nil {
			return fmt.Errorf("arbiter: complete with cancelled: %w", err)
		}
		return nil
	}

	// Success — clerk completed normally.
	slog.Info("arbiter: clerk child succeeded, completing")
	if _, err := client.Complete(ctx); err != nil {
		return fmt.Errorf("arbiter: complete (post-resume): %w", err)
	}
	return nil
}
