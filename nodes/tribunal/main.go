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
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/nodeconfig"
	"github.com/gideas/flow/nodes/internal/tally"
	flow "github.com/gideas/flow/sdk/go"
)

const (
	artefactLawReference   = "law-reference"
	artefactVerdictContext = "verdict-context"
)

const (
	outcomePromote = "promote"
	outcomeRetire  = "retire"
	outcomeDemote  = "demote"
)

const (
	defaultJurySize   int32 = 5
	defaultJurorNode        = "juror"
	defaultClerkNode        = "clerk-forge"
	defaultHungOutput       = "hung"
	defaultMaxRounds        = 3
)

type tribunalConfig struct {
	JurySize          int32  `yaml:"jurySize"`
	JurorNode         string `yaml:"jurorNode"`
	ConsensusStrategy string `yaml:"consensusStrategy"`
	MaxRounds         int    `yaml:"maxRounds"`
	ClerkNode         string `yaml:"clerkNode"`
	HungOutput        string `yaml:"hungOutput"`
}

func (c *tribunalConfig) jurySize() int32 {
	if c.JurySize < 1 {
		return defaultJurySize
	}
	return c.JurySize
}

func (c *tribunalConfig) jurorNode() string {
	if c.JurorNode == "" {
		return defaultJurorNode
	}
	return c.JurorNode
}

func (c *tribunalConfig) maxRounds() int {
	if c.MaxRounds < 1 {
		return defaultMaxRounds
	}
	return c.MaxRounds
}

func (c *tribunalConfig) clerkNode() string {
	if c.ClerkNode == "" {
		return defaultClerkNode
	}
	return c.ClerkNode
}

func (c *tribunalConfig) hungOutput() string {
	if c.HungOutput == "" {
		return defaultHungOutput
	}
	return c.HungOutput
}

func (c *tribunalConfig) consensusStrategy() flowv1.ConsensusStrategy {
	return nodeconfig.ParseConsensusStrategy(c.ConsensusStrategy)
}

type verdictContext struct {
	Trigger  string `json:"trigger"`
	Decision string `json:"decision"`
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

func handleTribunal(ctx context.Context, client *flow.Client, cfg *tribunalConfig) error {
	_, _ = client.Heartbeat(ctx)

	lawRef, err := client.GetArtefact(ctx, artefactLawReference)
	if err != nil {
		return fmt.Errorf("tribunal: get law-reference artefact: %w", err)
	}
	lawID := strings.TrimSpace(string(lawRef.GetContent()))
	if lawID == "" {
		return fmt.Errorf("tribunal: law-reference artefact is empty")
	}

	law, err := client.GetLaw(ctx, lawID)
	if err != nil {
		return fmt.Errorf("tribunal: get law %s: %w", lawID, err)
	}

	friction, err := client.QueryFriction(ctx, &flowv1.FrictionFilter{LawId: lawID})
	if err != nil {
		return fmt.Errorf("tribunal: query friction for %s: %w", lawID, err)
	}

	relatedLaws, err := queryRelatedLaws(ctx, client, law)
	if err != nil {
		return fmt.Errorf("tribunal: query related laws: %w", err)
	}

	evidence := assembleHearingEvidence(law, friction, relatedLaws)
	question, allowedOutcomes := frameHearingQuestion(law.GetTier())

	tallyCfg := tally.TallyConfig{
		ConsensusStrategy: cfg.consensusStrategy(),
		MaxRounds:         cfg.maxRounds(),
		JurySize:          int(cfg.jurySize()),
		JurorNode:         cfg.jurorNode(),
	}

	var priorReasoning string
	var lastResult tally.TallyResult

	for round := 1; round <= tallyCfg.MaxRounds; round++ {
		slog.Info("tribunal: deliberation round",
			"law_id", lawID,
			"round", round,
			"max_rounds", tallyCfg.MaxRounds,
		)

		tasks, buildErr := tally.BuildFanOutTasks(tallyCfg, tally.RoundInput{
			Question:            question,
			Evidence:            evidence,
			AllowedOutcomes:     allowedOutcomes,
			PriorRoundReasoning: priorReasoning,
		})
		if buildErr != nil {
			return fmt.Errorf("tribunal: build fan-out tasks (round %d): %w", round, buildErr)
		}

		roundChildren, fanErr := client.FanOut(ctx, tasks)
		if fanErr != nil {
			return fmt.Errorf("tribunal: fan-out (round %d): %w", round, fanErr)
		}

		allCompleted, awaitErr := client.AwaitChildren(ctx, flow.WithPollingInterval(time.Millisecond))
		if awaitErr != nil {
			return fmt.Errorf("tribunal: await children (round %d): %w", round, awaitErr)
		}

		roundCompleted := filterRoundChildren(allCompleted, roundChildren)
		votes, collectErr := tally.CollectVotes(ctx, client, roundCompleted)
		if collectErr != nil {
			return fmt.Errorf("tribunal: collect votes (round %d): %w", round, collectErr)
		}

		result := tally.Tally(votes, tallyCfg.ConsensusStrategy)
		result.Round = round
		lastResult = result

		slog.Info("tribunal: tally result",
			"law_id", lawID,
			"round", round,
			"consensus", result.IsConsensus,
			"outcome", result.Outcome,
			"vote_count", len(votes),
		)

		if result.IsConsensus {
			break
		}
		if round < tallyCfg.MaxRounds {
			priorReasoning = tally.SummariseRound(votes)
		}
	}

	if lastResult.IsHung {
		slog.Info("tribunal: hung after max rounds, routing to hung output",
			"law_id", lawID,
			"output", cfg.hungOutput(),
		)
		if _, err := client.RouteToOutput(ctx, cfg.hungOutput()); err != nil {
			return fmt.Errorf("tribunal: route to hung output: %w", err)
		}
		return nil
	}

	return spawnClerkChild(ctx, client, cfg, law, question, lastResult)
}

func queryRelatedLaws(
	ctx context.Context,
	client *flow.Client,
	law *flowv1.Law,
) ([]*flowv1.Law, error) {
	if len(law.GetAppliesTo()) == 0 {
		return nil, nil
	}
	return client.QueryLaws(ctx, law.GetAppliesTo()[0], "")
}

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

func spawnClerkChild(
	ctx context.Context,
	client *flow.Client,
	cfg *tribunalConfig,
	law *flowv1.Law,
	question string,
	result tally.TallyResult,
) error {
	decision := synthesizeDecision(law, question, result)
	vctxJSON, err := json.Marshal(verdictContext{
		Trigger:  "hearing",
		Decision: decision,
	})
	if err != nil {
		return fmt.Errorf("tribunal: marshal verdict-context: %w", err)
	}

	child, err := client.CreateChildWorkitem(ctx)
	if err != nil {
		return fmt.Errorf("tribunal: create clerk child: %w", err)
	}

	if _, err := child.StoreArtefact(ctx, artefactVerdictContext, "", vctxJSON); err != nil {
		return fmt.Errorf("tribunal: store verdict-context on child: %w", err)
	}
	if _, err := child.RouteTo(ctx, cfg.clerkNode()); err != nil {
		return fmt.Errorf("tribunal: route child to clerk: %w", err)
	}

	slog.Info("tribunal: consensus reached, clerk child created",
		"child_id", child.ID(),
		"clerk_node", cfg.clerkNode(),
		"outcome", result.Outcome,
	)

	if _, err := client.Complete(ctx); err != nil {
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

	var supporting []string
	for _, vote := range result.Votes {
		if vote.Outcome == result.Outcome && vote.Reasoning != "" {
			supporting = append(supporting, vote.Reasoning)
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
