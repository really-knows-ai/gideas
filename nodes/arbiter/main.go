// Arbiter is the deadlock resolution node of the Foundry Judiciary.
//
// When the Sort node detects that a feedback cycle has deadlocked (feedback
// depth exceeds threshold), it routes the Workitem to the Arbiter. The
// Arbiter gathers evidence from both sides of the dispute and convenes a
// Jury deliberation to reach a binding verdict.
//
// The algorithm:
//
//  1. Discover flow topology and artefact kinds from the exit contract.
//  2. For each artefact kind, scan feedback for DEADLOCKED items.
//  3. Assemble evidence: feedback debate history, artefact content excerpt,
//     cited laws from both sides, and friction cost summary.
//  4. Frame the question: "Should the reviewer's feedback be upheld, or
//     should the refiner's refusal stand?"
//  5. Call Deliberate with allowed_outcomes: favour_refiner, favour_reviewer.
//  6. If hung: route to Advocate for HITL resolution.
//  7. If verdict: mint Tier 2 Ruling via Clerk, LinkRuling on each
//     deadlocked feedback item, then route back to Sort.
//
// Configuration (YAML via NODE_CONFIG_PATH, default /etc/foundry/node-config.yaml):
//
//	consensusStrategy: SIMPLE_MAJORITY  # or SUPER_MAJORITY, UNANIMITY
//	maxRounds:         3
//	jurySize:          5
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/nodeconfig"
	flow "github.com/gideas/flow/sdk/go"
)

const (
	// outputSort is the well-known output name for routing back to the Sort gate.
	outputSort = "sort"

	// outputAdvocate is the well-known output name for escalation to HITL.
	outputAdvocate = "advocate"

	// defaultMaxRounds is the fallback maximum deliberation rounds.
	defaultMaxRounds int32 = 3

	// defaultJurySize is the fallback number of jurors.
	defaultJurySize int32 = 5
)

// arbiterConfig holds the Arbiter's runtime configuration.
type arbiterConfig struct {
	// ConsensusStrategy controls how many jurors must agree.
	// Valid values: SIMPLE_MAJORITY, SUPER_MAJORITY, UNANIMITY.
	ConsensusStrategy string `yaml:"consensusStrategy"`

	// MaxRounds is the maximum number of deliberation rounds before a
	// hung jury is declared. Default: 3.
	MaxRounds int32 `yaml:"maxRounds"`

	// JurySize is the number of jurors to empanel. Default: 5.
	JurySize int32 `yaml:"jurySize"`
}

// strategy returns the effective consensus strategy enum.
func (c *arbiterConfig) strategy() flowv1.ConsensusStrategy {
	return nodeconfig.ParseConsensusStrategy(c.ConsensusStrategy)
}

// maxRounds returns the effective max rounds, applying the default when unset.
func (c *arbiterConfig) maxRounds() int32 {
	if c.MaxRounds < 1 {
		return defaultMaxRounds
	}
	return c.MaxRounds
}

// jurySize returns the effective jury size, applying the default when unset.
func (c *arbiterConfig) jurySize() int32 {
	if c.JurySize < 1 {
		return defaultJurySize
	}
	return c.JurySize
}

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

// handleArbiter contains the Arbiter's core logic, separated from handler
// boilerplate for testability.
func handleArbiter(ctx context.Context, client *flow.Client, cfg *arbiterConfig) error {
	_, _ = client.Heartbeat(ctx)

	// ── Step 1: Discover topology ────────────────────────────────────
	topology, err := client.GetFlowTopology(ctx)
	if err != nil {
		return fmt.Errorf("arbiter: get flow topology: %w", err)
	}

	exitContract := topology.GetExitContract()

	// ── Step 2: For each artefact kind, find DEADLOCKED feedback ─────
	for kind := range exitContract {
		deadlockedItems, err := findDeadlockedFeedback(ctx, client, kind)
		if err != nil {
			return err
		}
		if len(deadlockedItems) == 0 {
			continue
		}

		// ── Step 3: Gather evidence ──────────────────────────────────
		evidence, err := assembleEvidence(ctx, client, kind, deadlockedItems)
		if err != nil {
			return err
		}

		// ── Step 4: Frame question and deliberate ────────────────────
		question := "Should the reviewer's feedback be upheld, or should the refiner's refusal stand?"
		verdict, err := client.Deliberate(
			ctx,
			question,
			evidence,
			[]string{"favour_refiner", "favour_reviewer"},
			cfg.strategy(),
			cfg.maxRounds(),
			cfg.jurySize(),
		)
		if err != nil {
			return fmt.Errorf("arbiter: deliberate: %w", err)
		}

		// ── Step 5: Handle verdict ───────────────────────────────────
		if verdict.GetHung() {
			slog.Info("arbiter: hung jury, escalating to advocate",
				"artefact_kind", kind,
				"rounds_used", verdict.GetRoundsUsed())

			// Write advocate-context artefact for the Advocate node.
			advCtx := map[string]any{
				"type":          "arbiter-hung",
				"artefact_kind": kind,
				"feedback_ids":  feedbackIDs(deadlockedItems),
				"choices":       []string{"favour_refiner", "favour_reviewer"},
			}
			advCtxJSON, err := json.Marshal(advCtx)
			if err != nil {
				return fmt.Errorf("arbiter: marshal advocate-context: %w", err)
			}
			if _, err := client.StoreArtefact(ctx, "advocate-context", "", advCtxJSON); err != nil {
				return fmt.Errorf("arbiter: store advocate-context: %w", err)
			}

			_, err = client.RouteToOutput(ctx, outputAdvocate)
			if err != nil {
				return fmt.Errorf("arbiter: route to advocate: %w", err)
			}
			return nil
		}

		// ── Step 6: Verdict reached → Clerk + LinkRuling ─────────────
		goal := synthesizeGoal(kind, deadlockedItems, verdict)
		appliesTo := []string{kind}

		lawResp, err := client.DraftLaw(ctx, verdict, goal, int32(flowv1.LawTier_LAW_TIER_RULING), appliesTo)
		if err != nil {
			return fmt.Errorf("arbiter: draft law: %w", err)
		}

		slog.Info("arbiter: ruling minted",
			"law_id", lawResp.GetLawId(),
			"outcome", verdict.GetOutcome(),
			"artefact_kind", kind)

		// Link ruling to each deadlocked feedback item.
		// Determine terminal state: favour_refiner → WONT_FIX (refusal stands),
		// favour_reviewer → REJECTED (reviewer's feedback upheld, refiner must act).
		targetState := flowv1.FeedbackState_FEEDBACK_STATE_REJECTED
		if verdict.GetOutcome() == "favour_refiner" {
			targetState = flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX
		}
		for _, item := range deadlockedItems {
			if _, err := client.LinkRuling(ctx, item.GetId(), lawResp.GetLawId(), targetState); err != nil {
				return fmt.Errorf("arbiter: link ruling to feedback %s: %w", item.GetId(), err)
			}
		}

		// Route back to Sort for re-evaluation.
		_, err = client.RouteToOutput(ctx, outputSort)
		if err != nil {
			return fmt.Errorf("arbiter: route to sort: %w", err)
		}
		return nil
	}

	// No deadlocked feedback found (shouldn't happen, but handle gracefully).
	slog.Warn("arbiter: no deadlocked feedback found, routing back to sort")
	_, err = client.RouteToOutput(ctx, outputSort)
	if err != nil {
		return fmt.Errorf("arbiter: route to sort (no deadlock): %w", err)
	}
	return nil
}

// findDeadlockedFeedback scans feedback items for the given artefact kind
// and returns those in DEADLOCKED state.
func findDeadlockedFeedback(
	ctx context.Context, client *flow.Client, artefactID string,
) ([]*flowv1.FeedbackItem, error) {
	items, err := client.GetFeedback(ctx, artefactID)
	if err != nil {
		return nil, fmt.Errorf("arbiter: get feedback for %s: %w", artefactID, err)
	}

	var deadlocked []*flowv1.FeedbackItem
	for _, item := range items {
		if item.GetState() == flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED {
			deadlocked = append(deadlocked, item)
		}
	}
	return deadlocked, nil
}

// assembleEvidence builds a markdown evidence bundle for the Jury.
func assembleEvidence(
	ctx context.Context,
	client *flow.Client,
	artefactKind string,
	deadlockedItems []*flowv1.FeedbackItem,
) (string, error) {
	var b strings.Builder

	// ── Artefact content excerpt ─────────────────────────────────────
	b.WriteString("## Artefact\n\n")
	artefactResp, err := client.GetArtefact(ctx, artefactKind)
	if err != nil {
		return "", fmt.Errorf("arbiter: get artefact %s: %w", artefactKind, err)
	}
	content := string(artefactResp.GetContent())
	if len(content) > 2000 {
		content = content[:2000] + "\n\n[... truncated ...]"
	}
	b.WriteString(content)
	b.WriteString("\n\n")

	// ── Feedback debate history ──────────────────────────────────────
	b.WriteString("## Deadlocked Feedback\n\n")
	for _, item := range deadlockedItems {
		fmt.Fprintf(&b, "### Feedback: %s\n\n", item.GetId())
		fmt.Fprintf(&b, "- **Source**: %s\n", item.GetSource())
		fmt.Fprintf(&b, "- **Severity**: %s\n", item.GetSeverity().String())
		fmt.Fprintf(&b, "- **Message**: %s\n\n", item.GetMessage())

		if j := item.GetJustification(); j != nil {
			b.WriteString("**Justification**:\n")
			if c := j.GetCitation(); c != nil {
				fmt.Fprintf(&b, "- Citation: law_ids=%v\n", c.GetCitationIds())
			}
			if n := j.GetNovelArgument(); n != nil {
				fmt.Fprintf(&b, "- Novel argument: %s\n", n.GetArgument())
			}
			b.WriteString("\n")
		}

		b.WriteString("**Debate History**:\n\n")
		for _, event := range item.GetHistory() {
			fmt.Fprintf(&b, "- [%s] %s: %s\n",
				event.GetAction(),
				event.GetActor(),
				event.GetMessage())
		}
		b.WriteString("\n")
	}

	// ── Cited laws from both sides ───────────────────────────────────
	b.WriteString("## Relevant Laws\n\n")
	laws, err := client.QueryLaws(ctx, artefactKind, "")
	if err != nil {
		return "", fmt.Errorf("arbiter: query laws for %s: %w", artefactKind, err)
	}
	for _, law := range laws {
		fmt.Fprintf(&b, "### %s (Tier %d)\n\n", law.GetId(), int32(law.GetTier()))
		fmt.Fprintf(&b, "- **Goal**: %s\n", law.GetGoal())
		for _, rep := range law.GetRepresentations() {
			fmt.Fprintf(&b, "- **%s**: %s\n", rep.GetType(), rep.GetContent())
		}
		b.WriteString("\n")
	}

	// ── Friction cost summary ────────────────────────────────────────
	b.WriteString("## Friction Summary\n\n")
	friction, err := client.QueryFriction(ctx, &flowv1.FrictionFilter{})
	if err != nil {
		return "", fmt.Errorf("arbiter: query friction: %w", err)
	}
	if len(friction) == 0 {
		b.WriteString("No friction data available.\n\n")
	} else {
		for _, agg := range friction {
			fmt.Fprintf(&b, "- law=%s node=%s events=%d magnitude=%.2f\n",
				agg.GetLawId(), agg.GetNodeId(), agg.GetEventCount(), agg.GetTotalMagnitude())
		}
		b.WriteString("\n")
	}

	return b.String(), nil
}

// synthesizeGoal builds a goal string for the Clerk from the dispute context.
func synthesizeGoal(
	artefactKind string,
	deadlockedItems []*flowv1.FeedbackItem,
	verdict *flowv1.DeliberateResponse,
) string {
	if len(deadlockedItems) == 0 {
		return fmt.Sprintf("Ruling on %s dispute: %s", artefactKind, verdict.GetOutcome())
	}
	first := deadlockedItems[0]
	return fmt.Sprintf("Ruling on %s dispute (feedback %s from %s): %s",
		artefactKind, first.GetId(), first.GetSource(), verdict.GetOutcome())
}

// feedbackIDs extracts the IDs from a slice of feedback items.
func feedbackIDs(items []*flowv1.FeedbackItem) []string {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.GetId()
	}
	return ids
}
