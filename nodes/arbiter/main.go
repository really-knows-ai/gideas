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

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/nodes/internal/nodeconfig"
	"github.com/foundry/flow/nodes/internal/nodeutil"
	"github.com/foundry/flow/nodes/internal/tally"
	flow "github.com/foundry/flow/sdk/go"
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
// Configuration
// ---------------------------------------------------------------------------

// arbiterConfig holds the Arbiter's runtime configuration. It shares the
// jury deliberation settings with the Tribunal via tally.JuryConfig.
type arbiterConfig = tally.JuryConfig

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
	client, workitem, err := nodeutil.SetupHandler(ctx, wctx, "arbiter")
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	cfg, err := nodeconfig.Load[arbiterConfig](nodeconfig.Path())
	if err != nil {
		return fmt.Errorf("arbiter: load config: %w", err)
	}

	return handleArbiter(ctx, client, workitem, cfg)
}

// ---------------------------------------------------------------------------
// Core logic
// ---------------------------------------------------------------------------

// handleArbiter contains all Arbiter logic, separated from handler
// boilerplate for testability.
//
// Phase detection: if GetChildren returns any completed children, this is a
// post-resume invocation. Otherwise it is the first invocation.
func handleArbiter(ctx context.Context, client *flow.Client, workitem *flow.Workitem, cfg *arbiterConfig) error {
	// ── Heartbeat ────────────────────────────────────────────────────
	_ = workitem.Heartbeat()

	// ── Phase detection ──────────────────────────────────────────────
	children, err := workitem.GetChildren()
	if err != nil {
		return fmt.Errorf("arbiter: get children: %w", err)
	}
	if tally.HasCompletedChild(children) {
		return tally.HandlePostResume("arbiter", workitem, children,
			func(*flow.ChildWorkitemStatus) error {
				slog.Info("arbiter: clerk child cancelled, propagating cancellation")
				if err := workitem.Complete(flow.WithReason(
					flowv1.CompletionReason_COMPLETION_REASON_CANCELLED,
				)); err != nil {
					return fmt.Errorf("arbiter: complete with cancelled: %w", err)
				}
				return nil
			},
			func(*flow.ChildWorkitemStatus) error {
				slog.Info("arbiter: clerk child succeeded, completing")
				if err := workitem.Complete(); err != nil {
					return fmt.Errorf("arbiter: complete (post-resume): %w", err)
				}
				return nil
			},
		)
	}

	return handleFirstInvocation(ctx, client, workitem, cfg)
}

// ---------------------------------------------------------------------------
// First invocation — deliberation loop
// ---------------------------------------------------------------------------

func handleFirstInvocation(ctx context.Context, client *flow.Client,
	workitem *flow.Workitem, cfg *arbiterConfig) error {
	// ── Step 1: Read evidence-bundle artefact ────────────────────────
	evidenceArt, err := workitem.GetArtefact(artefactEvidenceBundle)
	if err != nil {
		return fmt.Errorf("arbiter: read evidence-bundle: %w", err)
	}
	evidenceContent, err := evidenceArt.GetContent()
	if err != nil {
		return fmt.Errorf("arbiter: read evidence-bundle content: %w", err)
	}
	evidence := string(evidenceContent)

	// ── Step 2: Frame question ──────────────────────────────────────
	question := "Should the reviewer's feedback be upheld (requiring a law change), " +
		"or should the dispute be resolved without a law change?"
	allowedOutcomes := []string{outcomeLawChange, outcomeResolved}

	// ── Step 3: Deliberation loop ───────────────────────────────────
	tallyCfg := tally.TallyConfig{
		ConsensusStrategy: cfg.EffectiveConsensusStrategy(),
		MaxRounds:         cfg.EffectiveMaxRounds(),
		JurySize:          int(cfg.EffectiveJurySize()),
		JurorNode:         cfg.EffectiveJurorNode(),
	}

	lastResult, err := tally.Deliberate(ctx, client, workitem, tallyCfg, tally.RoundInput{
		Question:        question,
		Evidence:        evidence,
		AllowedOutcomes: allowedOutcomes,
	}, "arbiter")
	if err != nil {
		return err
	}

	// ── Step 4: Post-loop outcomes ──────────────────────────────────
	return handleDeliberationOutcome(workitem, cfg, lastResult)
}

// handleDeliberationOutcome branches on the tally result.
func handleDeliberationOutcome(
	workitem *flow.Workitem,
	cfg *arbiterConfig,
	result tally.TallyResult,
) error {
	// Hung — max rounds exhausted with no consensus.
	if result.IsHung {
		slog.Info("arbiter: hung after max rounds, routing to hung output")
		if err := workitem.RouteTo(cfg.EffectiveHungOutput()); err != nil {
			return fmt.Errorf("arbiter: route to hung: %w", err)
		}
		return nil
	}

	// Resolved — jury says no law change needed.
	if result.Outcome == outcomeResolved {
		slog.Info("arbiter: resolved, completing")
		if err := workitem.Complete(); err != nil {
			return fmt.Errorf("arbiter: complete (resolved): %w", err)
		}
		return nil
	}

	// Consensus for law change — create Clerk child and suspend.
	return spawnClerkAndSuspend(workitem, cfg, result)
}

// spawnClerkAndSuspend synthesizes the prose verdict-context, creates a
// Clerk child with the verdict-context artefact, and suspends.
func spawnClerkAndSuspend(
	workitem *flow.Workitem,
	cfg *arbiterConfig,
	result tally.TallyResult,
) error {
	// Synthesize prose decision from jury reasoning.
	decision := synthesizeDecision(result)

	vctx := tally.VerdictContext{
		Trigger:  "deadlock-resolution",
		Decision: decision,
	}
	vctxJSON, err := json.Marshal(vctx)
	if err != nil {
		return fmt.Errorf("arbiter: marshal verdict-context: %w", err)
	}

	// Create Clerk child.
	child, err := workitem.CreateChild()
	if err != nil {
		return fmt.Errorf("arbiter: create clerk child: %w", err)
	}

	// Store verdict-context on the child.
	if err := child.StoreArtefact(artefactVerdictContext, "", vctxJSON); err != nil {
		return fmt.Errorf("arbiter: store verdict-context on child: %w", err)
	}

	// Route child to clerk node.
	if err := child.RouteTo(cfg.EffectiveClerkNode()); err != nil {
		return fmt.Errorf("arbiter: route child to clerk: %w", err)
	}

	slog.Info("arbiter: clerk child created, suspending",
		"child_id", child.ID(),
		"clerk_node", cfg.EffectiveClerkNode(),
	)

	// Suspend until clerk child completes.
	if err := workitem.Suspend(
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

	b.WriteString(tally.SupportingArguments(result))

	return b.String()
}
