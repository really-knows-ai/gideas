// Deliberation Gate is a generic consensus tally node for the Foundry Judiciary.
//
// It reads juror verdict artefacts collected by its parent (Arbiter or
// Tribunal), applies a configurable consensus strategy, tracks round count,
// and routes to one of three well-known outputs:
//
//   - "consensus" — a winning outcome met the strategy threshold
//   - "retry"     — no consensus, but rounds remain; the parent should
//     fan out another round with prior-round reasoning
//   - "hung"      — no consensus and max rounds exhausted
//
// The gate also stores two artefacts before routing:
//
//   - "deliberation-result" — JSON with outcome, justifications, round,
//     and hung status (consumed by downstream nodes)
//   - "round-count" — plain-text integer of the current round (consumed by
//     the parent on retry to increment and by the gate on subsequent passes)
//
// Consensus logic is ported from the deleted jury/internal/deliberation/engine.go.
//
// Configuration (YAML via NODE_CONFIG_PATH):
//
//	consensusStrategy: SIMPLE_MAJORITY  # or SUPER_MAJORITY, UNANIMITY
//	maxRounds:         3
//
// Usage:
//
//	go run ./nodes/deliberation-gate/main.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/nodeconfig"
	flow "github.com/gideas/flow/sdk/go"
)

// ---------------------------------------------------------------------------
// Well-known constants
// ---------------------------------------------------------------------------

const (
	// Output names.
	outputConsensus = "consensus"
	outputRetry     = "retry"
	outputHung      = "hung"

	// Artefact IDs.
	artefactVerdictPrefix      = "verdict" // child artefacts: "verdict"
	artefactDeliberationResult = "deliberation-result"
	artefactRoundCount         = "round-count"

	// Defaults.
	defaultMaxRounds int32 = 3
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

type gateConfig struct {
	ConsensusStrategy string `yaml:"consensusStrategy"`
	MaxRounds         int32  `yaml:"maxRounds"`
}

func (c *gateConfig) strategy() flowv1.ConsensusStrategy {
	return nodeconfig.ParseConsensusStrategy(c.ConsensusStrategy)
}

func (c *gateConfig) maxRounds() int32 {
	if c.MaxRounds < 1 {
		return defaultMaxRounds
	}
	return c.MaxRounds
}

// ---------------------------------------------------------------------------
// Deliberation Result (stored as artefact, consumed downstream)
// ---------------------------------------------------------------------------

// deliberationResult is the JSON structure stored as the
// "deliberation-result" artefact.
type deliberationResult struct {
	Outcome        string               `json:"outcome"`
	Justifications []jurorJustification `json:"justifications"`
	RoundsUsed     int32                `json:"rounds_used"`
	Hung           bool                 `json:"hung"`
}

type jurorJustification struct {
	JurorID   string `json:"juror_id"`
	Outcome   string `json:"outcome"`
	Reasoning string `json:"reasoning"`
}

// ---------------------------------------------------------------------------
// Juror Verdict (read from child artefacts)
// ---------------------------------------------------------------------------

type jurorVerdict struct {
	Outcome   string `json:"outcome"`
	Reasoning string `json:"reasoning"`
}

// ---------------------------------------------------------------------------
// Entry Point
// ---------------------------------------------------------------------------

func main() {
	slog.Info("deliberation-gate: starting")
	if err := flow.Start(handler); err != nil {
		slog.Error("deliberation-gate: server failed", "error", err)
		os.Exit(1)
	}
}

func handler(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	slog.Info("deliberation-gate: received assignment",
		"workitem_id", wctx.GetWorkitemId(),
		"node_id", wctx.GetNodeId(),
	)

	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("deliberation-gate: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	cfg, err := nodeconfig.Load[gateConfig](nodeconfig.Path())
	if err != nil {
		return fmt.Errorf("deliberation-gate: load config: %w", err)
	}

	return handleDeliberationGate(ctx, client, cfg)
}

// ---------------------------------------------------------------------------
// Core Logic
// ---------------------------------------------------------------------------

func handleDeliberationGate(ctx context.Context, client *flow.Client, cfg *gateConfig) error {
	_, _ = client.Heartbeat(ctx)

	// ── Step 1: Determine current round ─────────────────────────────
	round := int32(1)
	roundResp, err := client.GetArtefact(ctx, artefactRoundCount)
	if err == nil {
		parsed, parseErr := strconv.ParseInt(strings.TrimSpace(string(roundResp.GetContent())), 10, 32)
		if parseErr == nil && parsed > 0 {
			round = int32(parsed)
		}
	}

	slog.Info("deliberation-gate: tallying",
		"round", round,
		"max_rounds", cfg.maxRounds(),
		"strategy", cfg.strategy().String(),
	)

	// ── Step 2: Collect juror verdicts from children ─────────────────
	verdicts, err := collectVerdicts(ctx, client)
	if err != nil {
		return err
	}

	if len(verdicts) == 0 {
		return fmt.Errorf("deliberation-gate: no juror verdicts found")
	}

	// ── Step 3: Check consensus ─────────────────────────────────────
	outcome, reached := checkConsensus(verdicts, cfg.strategy())

	// ── Step 4: Build result and store artefacts ─────────────────────
	result := deliberationResult{
		Justifications: buildJustifications(verdicts),
		RoundsUsed:     round,
	}

	if reached {
		result.Outcome = outcome
		result.Hung = false
	} else if round >= cfg.maxRounds() {
		result.Outcome = ""
		result.Hung = true
	}
	// else: no consensus, rounds remain — retry

	resultJSON, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("deliberation-gate: marshal result: %w", err)
	}
	if _, err := client.StoreArtefact(ctx, artefactDeliberationResult, "", resultJSON); err != nil {
		return fmt.Errorf("deliberation-gate: store deliberation-result: %w", err)
	}

	// Store updated round count.
	nextRound := strconv.FormatInt(int64(round+1), 10)
	if _, err := client.StoreArtefact(ctx, artefactRoundCount, "", []byte(nextRound)); err != nil {
		return fmt.Errorf("deliberation-gate: store round-count: %w", err)
	}

	// ── Step 5: Route ────────────────────────────────────────────────
	if reached {
		slog.Info("deliberation-gate: consensus reached",
			"outcome", outcome,
			"round", round,
		)
		if _, err := client.RouteToOutput(ctx, outputConsensus); err != nil {
			return fmt.Errorf("deliberation-gate: route to consensus: %w", err)
		}
		return nil
	}

	if round >= cfg.maxRounds() {
		slog.Info("deliberation-gate: hung jury",
			"rounds_used", round,
		)
		if _, err := client.RouteToOutput(ctx, outputHung); err != nil {
			return fmt.Errorf("deliberation-gate: route to hung: %w", err)
		}
		return nil
	}

	slog.Info("deliberation-gate: no consensus, retrying",
		"round", round,
		"max_rounds", cfg.maxRounds(),
	)
	if _, err := client.RouteToOutput(ctx, outputRetry); err != nil {
		return fmt.Errorf("deliberation-gate: route to retry: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Verdict Collection
// ---------------------------------------------------------------------------

// collectVerdicts reads juror verdicts from child Workitems. The parent
// (Arbiter/Tribunal) fans out to Jurors, awaits them, then routes to this
// gate. The children's "verdict" artefacts are accessible via the parent
// Workitem's child artefact API.
func collectVerdicts(ctx context.Context, client *flow.Client) ([]jurorVerdict, error) {
	children, err := client.GetChildren(ctx)
	if err != nil {
		return nil, fmt.Errorf("deliberation-gate: get children: %w", err)
	}

	var verdicts []jurorVerdict
	for _, child := range children {
		if child.Phase != flow.PhaseCompleted {
			continue
		}

		resp, err := client.GetChildArtefact(ctx, child.WorkitemID, artefactVerdictPrefix)
		if err != nil {
			slog.Warn("deliberation-gate: could not read verdict from child",
				"child_id", child.WorkitemID,
				"error", err,
			)
			continue
		}

		var v jurorVerdict
		if err := json.Unmarshal(resp.GetContent(), &v); err != nil {
			slog.Warn("deliberation-gate: invalid verdict JSON from child",
				"child_id", child.WorkitemID,
				"error", err,
			)
			continue
		}
		verdicts = append(verdicts, v)
	}

	return verdicts, nil
}

// ---------------------------------------------------------------------------
// Consensus Logic (ported from jury/internal/deliberation/engine.go)
// ---------------------------------------------------------------------------

// checkConsensus checks whether the votes meet the specified consensus
// strategy. Returns the winning outcome and whether consensus was reached.
func checkConsensus(verdicts []jurorVerdict, strategy flowv1.ConsensusStrategy) (string, bool) {
	if len(verdicts) == 0 {
		return "", false
	}

	// Count votes per outcome.
	counts := make(map[string]int)
	for _, v := range verdicts {
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

	total := len(verdicts)

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
// Helpers
// ---------------------------------------------------------------------------

// buildJustifications converts verdicts into the result format.
func buildJustifications(verdicts []jurorVerdict) []jurorJustification {
	justifications := make([]jurorJustification, len(verdicts))
	for i, v := range verdicts {
		justifications[i] = jurorJustification{
			JurorID:   fmt.Sprintf("juror-%d", i+1),
			Outcome:   v.Outcome,
			Reasoning: v.Reasoning,
		}
	}
	return justifications
}
