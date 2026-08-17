// Tribunal is the hearing orchestrator of the Foundry Judiciary.
//
// The Tribunal handles watcher-triggered hearings only. It reads a
// law-reference artefact, assembles hearing evidence, fans out to Juror nodes,
// tallies votes internally, and either:
//
//  1. creates a Clerk-cycle child and completes immediately when the jury
//     reaches consensus, or
//  2. routes to its hung output when no consensus emerges after maxRounds.
//
// Configuration (YAML via NODE_CONFIG_PATH):
//
//	jurySize:            5                 # jurors per round
//	jurorNode:           juror             # FoundryNode for juror children
//	consensusStrategy:   SIMPLE_MAJORITY   # SIMPLE_MAJORITY | SUPER_MAJORITY | UNANIMITY
//	maxRounds:           3                 # max deliberation rounds
//	clerkNode:           clerk-forge       # FoundryNode for clerk-cycle entry
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

const (
	artefactVerdictContext = "verdict-context"
)

const (
	outcomePromote = "promote"
	outcomeRetire  = "retire"
	outcomeDemote  = "demote"
)

// tribunalConfig holds the Tribunal's runtime configuration. It shares the
// jury deliberation settings with the Arbiter via tally.JuryConfig.
type tribunalConfig = tally.JuryConfig

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

func main() {
	slog.Info("tribunal: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("tribunal: server failed", "error", err)
		os.Exit(1)
	}
}

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	client, workitem, err := nodeutil.SetupHandler(ctx, wctx, "tribunal")
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	cfg, err := nodeconfig.Load[tribunalConfig](nodeconfig.Path())
	if err != nil {
		return fmt.Errorf("tribunal: load config: %w", err)
	}

	return handleTribunal(ctx, client, workitem, cfg)
}

func handleTribunal(ctx context.Context, client *flow.Client, workitem *flow.Workitem, cfg *tribunalConfig) error {
	_ = workitem.Heartbeat()

	lawRef, err := workitem.GetArtefact(nodeutil.LawReferenceArtefact)
	if err != nil {
		return fmt.Errorf("tribunal: get law-reference artefact: %w", err)
	}
	lawRefContent, err := lawRef.GetContent()
	if err != nil {
		return fmt.Errorf("tribunal: get law-reference content: %w", err)
	}
	lawID := strings.TrimSpace(string(lawRefContent))
	if lawID == "" {
		return fmt.Errorf("tribunal: law-reference artefact is empty")
	}

	law, err := client.GetLaw(lawID)
	if err != nil {
		return fmt.Errorf("tribunal: get law %s: %w", lawID, err)
	}

	friction, err := workitem.QueryFriction(&flowv1.FrictionFilter{LawId: lawID})
	if err != nil {
		return fmt.Errorf("tribunal: query friction for %s: %w", lawID, err)
	}

	relatedLaws, err := queryRelatedLaws(ctx, client, law)
	if err != nil {
		return fmt.Errorf("tribunal: query related laws: %w", err)
	}

	evidence := assembleHearingEvidence(law.PB(), friction, relatedLaws)
	question, allowedOutcomes := frameHearingQuestion(flowv1.LawTier(law.GetTier()))

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
	}, "tribunal", "law_id", lawID)
	if err != nil {
		return err
	}

	if lastResult.IsHung {
		slog.Info("tribunal: hung after max rounds, routing to hung output",
			"law_id", lawID,
			"output", cfg.EffectiveHungOutput(),
		)
		if err := workitem.RouteTo(cfg.EffectiveHungOutput()); err != nil {
			return fmt.Errorf("tribunal: route to hung output: %w", err)
		}
		return nil
	}

	return spawnClerkChild(workitem, cfg, law, question, lastResult)
}

func queryRelatedLaws(
	ctx context.Context,
	client *flow.Client,
	law *flow.Law,
) ([]*flowv1.Law, error) {
	appliesTo := law.PB().GetAppliesTo()
	if len(appliesTo) == 0 {
		return nil, nil
	}
	resp, err := client.RawLibrarian().QueryLaws(ctx, &flowv1.QueryLawsRequest{
		Filter: &flowv1.LawFilter{GovernedArtefact: appliesTo[0]},
	})
	if err != nil {
		return nil, fmt.Errorf("tribunal: query related laws: %w", err)
	}
	return resp.GetLaws(), nil
}

func spawnClerkChild(
	workitem *flow.Workitem,
	cfg *tribunalConfig,
	law *flow.Law,
	question string,
	result tally.TallyResult,
) error {
	decision := synthesizeDecision(law.PB(), question, result)
	vctxJSON, err := json.Marshal(tally.VerdictContext{
		Trigger:  "hearing",
		Decision: decision,
	})
	if err != nil {
		return fmt.Errorf("tribunal: marshal verdict-context: %w", err)
	}

	child, err := workitem.CreateChild()
	if err != nil {
		return fmt.Errorf("tribunal: create clerk child: %w", err)
	}

	if err := child.StoreArtefact(artefactVerdictContext, "", vctxJSON); err != nil {
		return fmt.Errorf("tribunal: store verdict-context on child: %w", err)
	}
	if err := child.RouteTo(cfg.EffectiveClerkNode()); err != nil {
		return fmt.Errorf("tribunal: route child to clerk: %w", err)
	}

	slog.Info("tribunal: consensus reached, clerk child created",
		"child_id", child.ID(),
		"clerk_node", cfg.EffectiveClerkNode(),
		"outcome", result.Outcome,
	)

	if err := workitem.Complete(); err != nil {
		return fmt.Errorf("tribunal: complete: %w", err)
	}
	return nil
}

func synthesizeDecision(law *flowv1.Law, question string, result tally.TallyResult) string {
	var b strings.Builder
	fmt.Fprintf(&b,
		"The court has reviewed law %q (tier %s) and reached consensus after %d round(s). ",
		law.GetId(), law.GetTier().String(), result.Round,
	)
	fmt.Fprintf(&b, "The hearing question was: %s ", question)
	fmt.Fprintf(&b, "The court recommends %q. ", result.Outcome)
	if law.GetGoal() != "" {
		fmt.Fprintf(&b, "The law's current goal is: %s. ", law.GetGoal())
	}

	b.WriteString(tally.SupportingArguments(result))

	return b.String()
}

func frameHearingQuestion(tier flowv1.LawTier) (string, []string) {
	switch tier {
	case flowv1.LawTier_LAW_TIER_FINDING:
		return "Should this Finding be promoted to a Ruling, or retired?",
			[]string{outcomePromote, outcomeRetire}
	case flowv1.LawTier_LAW_TIER_RULING:
		return "Should this Ruling be promoted to a Local Statute, retired, or demoted to a Finding?",
			[]string{outcomePromote, outcomeRetire, outcomeDemote}
	default:
		return fmt.Sprintf("Should this Tier %d law be promoted, retired, or demoted?", int32(tier)),
			[]string{outcomePromote, outcomeRetire, outcomeDemote}
	}
}

func assembleHearingEvidence(
	law *flowv1.Law,
	friction []*flowv1.FrictionAggregate,
	relatedLaws []*flowv1.Law,
) string {
	var b strings.Builder

	b.WriteString("## Law Under Review\n\n")
	fmt.Fprintf(&b, "- **ID**: %s\n", law.GetId())
	fmt.Fprintf(&b, "- **Goal**: %s\n", law.GetGoal())
	fmt.Fprintf(&b, "- **Tier**: %d (%s)\n", int32(law.GetTier()), law.GetTier().String())
	fmt.Fprintf(&b, "- **Applies To**: %s\n\n", strings.Join(law.GetAppliesTo(), ", "))

	b.WriteString("### Representations\n\n")
	for _, rep := range law.GetRepresentations() {
		fmt.Fprintf(&b, "**%s**:\n%s\n\n", rep.GetType(), rep.GetContent())
	}

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

	b.WriteString("## Related Laws\n\n")
	if len(relatedLaws) == 0 {
		b.WriteString("No related laws found.\n\n")
	} else {
		for _, rl := range relatedLaws {
			if rl.GetId() == law.GetId() {
				continue
			}
			fmt.Fprintf(&b, "- **%s** (Tier %d): %s\n",
				rl.GetId(), int32(rl.GetTier()), rl.GetGoal())
		}
		b.WriteString("\n")
	}

	return b.String()
}
