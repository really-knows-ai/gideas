// Tribunal is the hearing conductor node of the Foundry Judiciary.
//
// The Tribunal convenes hearings on individual laws to decide their
// lifecycle progression. It is triggered by a Governance Flow workitem
// that carries a "law-reference" artefact identifying the law under review.
//
// The algorithm:
//
//  1. Read the "law-reference" artefact to get the law ID under review.
//  2. Retrieve the full law object from the Librarian.
//  3. Query friction data from the Friction Ledger.
//  4. Query related laws from the Librarian for context.
//  5. Assemble evidence: law goal, representations, friction summary,
//     related laws.
//  6. Frame the question based on law tier:
//     - Tier 1 (Finding):  "Should this Finding be promoted or retired?"
//     - Tier 2 (Ruling):   "Should this Ruling be promoted, retired, or demoted?"
//  7. Call Deliberate with tier-appropriate allowed_outcomes.
//  8. If hung: route to Advocate for HITL resolution.
//  9. Based on verdict + tier:
//     - Tier 1 promote: DraftLaw at tier=2, then Complete.
//     - Tier 1 retire:  DraftLaw retire, then Complete.
//     - Tier 2 promote: Route to Advocate for Tier 3 HITL ratification.
//     - Tier 2 retire:  DraftLaw retire, then Complete.
//     - Tier 2 demote:  DraftLaw at tier=1, then Complete.
//
// Configuration (YAML via NODE_CONFIG_PATH, default /etc/foundry/node-config.yaml):
//
//	consensusStrategy: SIMPLE_MAJORITY
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
	// outputAdvocate is the well-known output name for escalation to HITL.
	outputAdvocate = "advocate"

	// lawReferenceArtefact is the artefact ID that carries the law ID under review.
	lawReferenceArtefact = "law-reference"

	// defaultMaxRounds is the fallback maximum deliberation rounds.
	defaultMaxRounds int32 = 3

	// defaultJurySize is the fallback number of jurors.
	defaultJurySize int32 = 5

	// Outcome constants used in verdict matching and question framing.
	outcomePromote = "promote"
	outcomeRetire  = "retire"
	outcomeDemote  = "demote"
)

// tribunalConfig holds the Tribunal's runtime configuration.
type tribunalConfig struct {
	ConsensusStrategy string `yaml:"consensusStrategy"`
	MaxRounds         int32  `yaml:"maxRounds"`
	JurySize          int32  `yaml:"jurySize"`
}

func (c *tribunalConfig) strategy() flowv1.ConsensusStrategy {
	return nodeconfig.ParseConsensusStrategy(c.ConsensusStrategy)
}

func (c *tribunalConfig) maxRounds() int32 {
	if c.MaxRounds < 1 {
		return defaultMaxRounds
	}
	return c.MaxRounds
}

func (c *tribunalConfig) jurySize() int32 {
	if c.JurySize < 1 {
		return defaultJurySize
	}
	return c.JurySize
}

func main() {
	slog.Info("tribunal: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("tribunal: server failed", "error", err)
		os.Exit(1)
	}
}

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("tribunal: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("tribunal: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	cfg, err := nodeconfig.Load[tribunalConfig](nodeconfig.Path())
	if err != nil {
		return fmt.Errorf("tribunal: load config: %w", err)
	}

	return handleTribunal(ctx, client, cfg)
}

// handleTribunal contains the Tribunal's core logic, separated from handler
// boilerplate for testability.
func handleTribunal(ctx context.Context, client *flow.Client, cfg *tribunalConfig) error {
	_, _ = client.Heartbeat(ctx)

	// ── Step 1: Read the law-reference artefact ──────────────────────
	lawRef, err := client.GetArtefact(ctx, lawReferenceArtefact)
	if err != nil {
		return fmt.Errorf("tribunal: get law-reference artefact: %w", err)
	}
	lawID := strings.TrimSpace(string(lawRef.GetContent()))
	if lawID == "" {
		return fmt.Errorf("tribunal: law-reference artefact is empty")
	}

	slog.Info("tribunal: reviewing law", "law_id", lawID)

	// ── Step 2: Get the full law object ──────────────────────────────
	law, err := client.GetLaw(ctx, lawID)
	if err != nil {
		return fmt.Errorf("tribunal: get law %s: %w", lawID, err)
	}

	// ── Step 3: Query friction data ──────────────────────────────────
	friction, err := client.QueryFriction(ctx, &flowv1.FrictionFilter{
		LawId: lawID,
	})
	if err != nil {
		return fmt.Errorf("tribunal: query friction for %s: %w", lawID, err)
	}

	// ── Step 4: Query related laws for context ───────────────────────
	var relatedLaws []*flowv1.Law
	if len(law.GetAppliesTo()) > 0 {
		relatedLaws, err = client.QueryLaws(ctx, law.GetAppliesTo()[0], "")
		if err != nil {
			return fmt.Errorf("tribunal: query related laws: %w", err)
		}
	}

	// ── Step 5: Assemble evidence ────────────────────────────────────
	evidence := assembleHearingEvidence(law, friction, relatedLaws)

	// ── Step 6: Frame question and determine allowed outcomes ────────
	tier := law.GetTier()
	question, allowedOutcomes := frameQuestion(tier)

	// ── Step 7: Deliberate ───────────────────────────────────────────
	verdict, err := client.Deliberate(
		ctx,
		question,
		evidence,
		allowedOutcomes,
		cfg.strategy(),
		cfg.maxRounds(),
		cfg.jurySize(),
	)
	if err != nil {
		return fmt.Errorf("tribunal: deliberate: %w", err)
	}

	// ── Step 8: Hung jury → Advocate ─────────────────────────────────
	if verdict.GetHung() {
		slog.Info("tribunal: hung jury, escalating to advocate",
			"law_id", lawID,
			"tier", int32(tier),
			"rounds_used", verdict.GetRoundsUsed())

		// Write advocate-context artefact for the Advocate node.
		advCtx := map[string]any{
			"type":           "tribunal-hung",
			"law_id":         law.GetId(),
			"law_goal":       law.GetGoal(),
			"law_applies_to": law.GetAppliesTo(),
			"law_tier":       int32(tier),
			"choices":        allowedOutcomes,
		}
		advCtxJSON, err := json.Marshal(advCtx)
		if err != nil {
			return fmt.Errorf("tribunal: marshal advocate-context: %w", err)
		}
		if _, err := client.StoreArtefact(ctx, "advocate-context", "", advCtxJSON); err != nil {
			return fmt.Errorf("tribunal: store advocate-context: %w", err)
		}

		_, err = client.RouteToOutput(ctx, outputAdvocate)
		if err != nil {
			return fmt.Errorf("tribunal: route to advocate: %w", err)
		}
		return nil
	}

	// ── Step 9: Apply verdict based on tier ──────────────────────────
	return applyVerdict(ctx, client, law, verdict)
}

// frameQuestion returns the question and allowed outcomes for the given tier.
func frameQuestion(tier flowv1.LawTier) (string, []string) {
	switch tier {
	case flowv1.LawTier_LAW_TIER_FINDING:
		return "Should this Finding be promoted to a Ruling, or retired?",
			[]string{outcomePromote, outcomeRetire}
	case flowv1.LawTier_LAW_TIER_RULING:
		return "Should this Ruling be promoted to a Local Statute, retired, or demoted to a Finding?",
			[]string{outcomePromote, outcomeRetire, outcomeDemote}
	default:
		return fmt.Sprintf("Should this Tier %d law be promoted, retired, or demoted?", int32(tier)),
			[]string{"promote", "retire", "demote"}
	}
}

// applyVerdict executes the verdict based on the law's tier and the jury's outcome.
func applyVerdict(
	ctx context.Context,
	client *flow.Client,
	law *flowv1.Law,
	verdict *flowv1.DeliberateResponse,
) error {
	tier := law.GetTier()
	outcome := verdict.GetOutcome()

	slog.Info("tribunal: applying verdict",
		"law_id", law.GetId(),
		"tier", int32(tier),
		"outcome", outcome)

	switch {
	// ── Tier 1 Finding ───────────────────────────────────────────────
	case tier == flowv1.LawTier_LAW_TIER_FINDING && outcome == outcomePromote:
		return draftAndComplete(ctx, client, verdict, law, int32(flowv1.LawTier_LAW_TIER_RULING), "promote finding")

	case tier == flowv1.LawTier_LAW_TIER_FINDING && outcome == outcomeRetire:
		return draftAndComplete(ctx, client, verdict, law, int32(tier), "retire finding")

	// ── Tier 2 Ruling ────────────────────────────────────────────────
	case tier == flowv1.LawTier_LAW_TIER_RULING && outcome == outcomePromote:
		// Tier 2 promote → Route to Advocate for Tier 3 HITL ratification.
		slog.Info("tribunal: tier 2 promote, escalating to advocate for ratification",
			"law_id", law.GetId())

		// Write advocate-context artefact for the Advocate node.
		advCtx := map[string]any{
			"type":           "tribunal-promote",
			"law_id":         law.GetId(),
			"law_goal":       law.GetGoal(),
			"law_applies_to": law.GetAppliesTo(),
			"law_tier":       int32(tier),
			"choices":        []string{"accept", "reject"},
		}
		advCtxJSON, err := json.Marshal(advCtx)
		if err != nil {
			return fmt.Errorf("tribunal: marshal advocate-context (promote): %w", err)
		}
		if _, err := client.StoreArtefact(ctx, "advocate-context", "", advCtxJSON); err != nil {
			return fmt.Errorf("tribunal: store advocate-context (promote): %w", err)
		}

		_, err = client.RouteToOutput(ctx, outputAdvocate)
		if err != nil {
			return fmt.Errorf("tribunal: route to advocate (promote ruling): %w", err)
		}
		return nil

	case tier == flowv1.LawTier_LAW_TIER_RULING && outcome == outcomeRetire:
		return draftAndComplete(ctx, client, verdict, law, int32(tier), "retire ruling")

	case tier == flowv1.LawTier_LAW_TIER_RULING && outcome == outcomeDemote:
		return draftAndComplete(ctx, client, verdict, law, int32(flowv1.LawTier_LAW_TIER_FINDING), "demote ruling")

	default:
		return fmt.Errorf("tribunal: unhandled verdict tier=%d outcome=%s", int32(tier), outcome)
	}
}

// draftAndComplete calls DraftLaw and then Complete. Used by multiple verdict
// branches to avoid code duplication.
func draftAndComplete(
	ctx context.Context,
	client *flow.Client,
	verdict *flowv1.DeliberateResponse,
	law *flowv1.Law,
	tier int32,
	label string,
) error {
	_, err := client.DraftLaw(ctx, verdict, law.GetGoal(), tier, law.GetAppliesTo())
	if err != nil {
		return fmt.Errorf("tribunal: draft law (%s): %w", label, err)
	}
	if _, err := client.Complete(ctx, ""); err != nil {
		return fmt.Errorf("tribunal: complete (%s): %w", label, err)
	}
	return nil
}

// assembleHearingEvidence builds a markdown evidence bundle for the Jury.
func assembleHearingEvidence(
	law *flowv1.Law,
	friction []*flowv1.FrictionAggregate,
	relatedLaws []*flowv1.Law,
) string {
	var b strings.Builder

	// ── Law under review ─────────────────────────────────────────────
	b.WriteString("## Law Under Review\n\n")
	fmt.Fprintf(&b, "- **ID**: %s\n", law.GetId())
	fmt.Fprintf(&b, "- **Goal**: %s\n", law.GetGoal())
	fmt.Fprintf(&b, "- **Tier**: %d (%s)\n", int32(law.GetTier()), law.GetTier().String())
	fmt.Fprintf(&b, "- **Applies To**: %s\n\n", strings.Join(law.GetAppliesTo(), ", "))

	b.WriteString("### Representations\n\n")
	for _, rep := range law.GetRepresentations() {
		fmt.Fprintf(&b, "**%s**:\n%s\n\n", rep.GetType(), rep.GetContent())
	}

	// ── Friction data ────────────────────────────────────────────────
	b.WriteString("## Friction Summary\n\n")
	if len(friction) == 0 {
		b.WriteString("No friction data recorded for this law.\n\n")
	} else {
		var totalMagnitude float64
		var totalEvents int32
		for _, agg := range friction {
			fmt.Fprintf(&b, "- node=%s events=%d magnitude=%.2f\n",
				agg.GetNodeId(), agg.GetEventCount(), agg.GetTotalMagnitude())
			totalMagnitude += agg.GetTotalMagnitude()
			totalEvents += agg.GetEventCount()
		}
		fmt.Fprintf(&b, "\n**Total**: %d events, %.2f cumulative magnitude\n\n",
			totalEvents, totalMagnitude)
	}

	// ── Related laws ─────────────────────────────────────────────────
	b.WriteString("## Related Laws\n\n")
	if len(relatedLaws) == 0 {
		b.WriteString("No related laws found.\n\n")
	} else {
		for _, rl := range relatedLaws {
			if rl.GetId() == law.GetId() {
				continue // skip self
			}
			fmt.Fprintf(&b, "- **%s** (Tier %d): %s\n",
				rl.GetId(), int32(rl.GetTier()), rl.GetGoal())
		}
		b.WriteString("\n")
	}

	return b.String()
}
