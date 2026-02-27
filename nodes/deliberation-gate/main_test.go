package main

import (
	"context"
	"encoding/json"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Consensus — Simple Majority
// ---------------------------------------------------------------------------

func TestGate_Consensus_SimpleMajority(t *testing.T) {
	spy := newGateSpy()
	seedChildren(spy, map[string]jurorVerdict{
		"child-1": {Outcome: "favour_refiner", Reasoning: "reason A"},
		"child-2": {Outcome: "favour_refiner", Reasoning: "reason B"},
		"child-3": {Outcome: "favour_reviewer", Reasoning: "reason C"},
	})

	client := setupGateTest(t, spy)
	cfg := &gateConfig{ConsensusStrategy: "SIMPLE_MAJORITY", MaxRounds: 3}

	if err := handleDeliberationGate(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleDeliberationGate: %v", err)
	}

	// Should route to consensus.
	assertRoutedTo(t, spy, outputConsensus)

	// Check stored deliberation result.
	result := parseStoredResult(t, spy)
	if result.Outcome != "favour_refiner" {
		t.Errorf("outcome = %q, want %q", result.Outcome, "favour_refiner")
	}
	if result.Hung {
		t.Error("result should not be hung")
	}
	if result.RoundsUsed != 1 {
		t.Errorf("rounds_used = %d, want 1", result.RoundsUsed)
	}
	if len(result.Justifications) != 3 {
		t.Errorf("justifications count = %d, want 3", len(result.Justifications))
	}
}

func TestGate_Consensus_SuperMajority(t *testing.T) {
	spy := newGateSpy()
	seedChildren(spy, map[string]jurorVerdict{
		"child-1": {Outcome: "promote", Reasoning: "yes"},
		"child-2": {Outcome: "promote", Reasoning: "yes"},
		"child-3": {Outcome: "retire", Reasoning: "no"},
	})

	client := setupGateTest(t, spy)
	cfg := &gateConfig{ConsensusStrategy: "SUPER_MAJORITY", MaxRounds: 3}

	if err := handleDeliberationGate(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleDeliberationGate: %v", err)
	}

	// 2/3 = 66% meets super majority threshold.
	assertRoutedTo(t, spy, outputConsensus)

	result := parseStoredResult(t, spy)
	if result.Outcome != "promote" {
		t.Errorf("outcome = %q, want %q", result.Outcome, "promote")
	}
}

func TestGate_Consensus_Unanimity(t *testing.T) {
	spy := newGateSpy()
	seedChildren(spy, map[string]jurorVerdict{
		"child-1": {Outcome: "retire", Reasoning: "yes"},
		"child-2": {Outcome: "retire", Reasoning: "yes"},
		"child-3": {Outcome: "retire", Reasoning: "yes"},
	})

	client := setupGateTest(t, spy)
	cfg := &gateConfig{ConsensusStrategy: "UNANIMITY", MaxRounds: 3}

	if err := handleDeliberationGate(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleDeliberationGate: %v", err)
	}

	assertRoutedTo(t, spy, outputConsensus)

	result := parseStoredResult(t, spy)
	if result.Outcome != "retire" {
		t.Errorf("outcome = %q, want %q", result.Outcome, "retire")
	}
}

// ---------------------------------------------------------------------------
// Retry (no consensus, rounds remain)
// ---------------------------------------------------------------------------

func TestGate_Retry_NoConsensus_RoundsRemain(t *testing.T) {
	spy := newGateSpy()
	// Split vote — no majority.
	seedChildren(spy, map[string]jurorVerdict{
		"child-1": {Outcome: "favour_refiner", Reasoning: "A"},
		"child-2": {Outcome: "favour_reviewer", Reasoning: "B"},
	})
	// Round 1, max 3.
	spy.Artefacts[artefactRoundCount] = []byte("1")

	client := setupGateTest(t, spy)
	cfg := &gateConfig{ConsensusStrategy: "SIMPLE_MAJORITY", MaxRounds: 3}

	if err := handleDeliberationGate(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleDeliberationGate: %v", err)
	}

	assertRoutedTo(t, spy, outputRetry)

	// Verify round count was incremented.
	roundContent := string(spy.StoredArtefacts[artefactRoundCount])
	if roundContent != "2" {
		t.Errorf("stored round-count = %q, want %q", roundContent, "2")
	}
}

// ---------------------------------------------------------------------------
// Hung Jury (no consensus, max rounds exhausted)
// ---------------------------------------------------------------------------

func TestGate_Hung_MaxRoundsExhausted(t *testing.T) {
	spy := newGateSpy()
	// Split vote.
	seedChildren(spy, map[string]jurorVerdict{
		"child-1": {Outcome: "favour_refiner", Reasoning: "A"},
		"child-2": {Outcome: "favour_reviewer", Reasoning: "B"},
	})
	// Already at max rounds.
	spy.Artefacts[artefactRoundCount] = []byte("3")

	client := setupGateTest(t, spy)
	cfg := &gateConfig{ConsensusStrategy: "SIMPLE_MAJORITY", MaxRounds: 3}

	if err := handleDeliberationGate(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleDeliberationGate: %v", err)
	}

	assertRoutedTo(t, spy, outputHung)

	result := parseStoredResult(t, spy)
	if !result.Hung {
		t.Error("result should be hung")
	}
	if result.Outcome != "" {
		t.Errorf("hung result outcome = %q, want empty", result.Outcome)
	}
}

// ---------------------------------------------------------------------------
// Unanimity fails → retry
// ---------------------------------------------------------------------------

func TestGate_Unanimity_SplitVote_Retry(t *testing.T) {
	spy := newGateSpy()
	seedChildren(spy, map[string]jurorVerdict{
		"child-1": {Outcome: "promote", Reasoning: "yes"},
		"child-2": {Outcome: "promote", Reasoning: "yes"},
		"child-3": {Outcome: "retire", Reasoning: "no"},
	})

	client := setupGateTest(t, spy)
	cfg := &gateConfig{ConsensusStrategy: "UNANIMITY", MaxRounds: 3}

	if err := handleDeliberationGate(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleDeliberationGate: %v", err)
	}

	// Not unanimous, round 1 of 3 → retry.
	assertRoutedTo(t, spy, outputRetry)
}

// ---------------------------------------------------------------------------
// No round-count artefact → defaults to round 1
// ---------------------------------------------------------------------------

func TestGate_NoRoundCountArtefact_DefaultsToRound1(t *testing.T) {
	spy := newGateSpy()
	seedChildren(spy, map[string]jurorVerdict{
		"child-1": {Outcome: "a", Reasoning: "r"},
		"child-2": {Outcome: "b", Reasoning: "r"},
	})
	// No round-count artefact seeded → should default to round 1.

	client := setupGateTest(t, spy)
	cfg := &gateConfig{ConsensusStrategy: "SIMPLE_MAJORITY", MaxRounds: 3}

	if err := handleDeliberationGate(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleDeliberationGate: %v", err)
	}

	// Split vote, round 1 of 3 → retry.
	assertRoutedTo(t, spy, outputRetry)

	result := parseStoredResult(t, spy)
	if result.RoundsUsed != 1 {
		t.Errorf("rounds_used = %d, want 1", result.RoundsUsed)
	}
}

// ---------------------------------------------------------------------------
// No children → error
// ---------------------------------------------------------------------------

func TestGate_Error_NoChildren(t *testing.T) {
	spy := newGateSpy()
	// No children seeded.

	client := setupGateTest(t, spy)
	cfg := &gateConfig{ConsensusStrategy: "SIMPLE_MAJORITY", MaxRounds: 3}

	err := handleDeliberationGate(context.Background(), client, cfg)
	if err == nil {
		t.Fatal("expected error when no juror verdicts")
	}
}

// ---------------------------------------------------------------------------
// GetChildren error
// ---------------------------------------------------------------------------

func TestGate_Error_GetChildrenFails(t *testing.T) {
	spy := newGateSpy()
	spy.GetChildrenErr = status.Errorf(codes.Internal, "operator down")

	client := setupGateTest(t, spy)
	cfg := &gateConfig{ConsensusStrategy: "SIMPLE_MAJORITY", MaxRounds: 3}

	err := handleDeliberationGate(context.Background(), client, cfg)
	if err == nil {
		t.Fatal("expected error when GetChildren fails")
	}
}

// ---------------------------------------------------------------------------
// StoreArtefact error
// ---------------------------------------------------------------------------

func TestGate_Error_StoreResultFails(t *testing.T) {
	spy := newGateSpy()
	seedChildren(spy, map[string]jurorVerdict{
		"child-1": {Outcome: "a", Reasoning: "r"},
		"child-2": {Outcome: "a", Reasoning: "r"},
	})
	spy.StoreArtefactErr = status.Errorf(codes.Internal, "archivist down")

	client := setupGateTest(t, spy)
	cfg := &gateConfig{ConsensusStrategy: "SIMPLE_MAJORITY", MaxRounds: 3}

	err := handleDeliberationGate(context.Background(), client, cfg)
	if err == nil {
		t.Fatal("expected error when store fails")
	}
}

// ---------------------------------------------------------------------------
// Config defaults
// ---------------------------------------------------------------------------

func TestGateConfig_DefaultMaxRounds(t *testing.T) {
	cfg := &gateConfig{}
	if cfg.maxRounds() != defaultMaxRounds {
		t.Errorf("maxRounds() = %d, want %d", cfg.maxRounds(), defaultMaxRounds)
	}
}

func TestGateConfig_DefaultStrategy(t *testing.T) {
	cfg := &gateConfig{}
	if cfg.strategy() != flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY {
		t.Errorf("strategy() = %v, want SIMPLE_MAJORITY", cfg.strategy())
	}
}

// ---------------------------------------------------------------------------
// Consensus logic unit tests
// ---------------------------------------------------------------------------

func TestCheckConsensus_SimpleMajority(t *testing.T) {
	tests := []struct {
		name    string
		votes   []jurorVerdict
		want    string
		reached bool
	}{
		{
			name:    "3-0",
			votes:   nVotes("a", 3),
			want:    "a",
			reached: true,
		},
		{
			name:    "2-1",
			votes:   append(nVotes("a", 2), nVotes("b", 1)...),
			want:    "a",
			reached: true,
		},
		{
			name:    "1-1 tie",
			votes:   append(nVotes("a", 1), nVotes("b", 1)...),
			want:    "",
			reached: false,
		},
		{
			name:    "empty",
			votes:   nil,
			want:    "",
			reached: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := checkConsensus(tt.votes, flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY)
			if ok != tt.reached || got != tt.want {
				t.Errorf("checkConsensus() = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.reached)
			}
		})
	}
}

func TestCheckConsensus_SuperMajority(t *testing.T) {
	tests := []struct {
		name    string
		votes   []jurorVerdict
		want    string
		reached bool
	}{
		{
			name:    "3-0 (100%)",
			votes:   nVotes("a", 3),
			want:    "a",
			reached: true,
		},
		{
			name:    "2-1 (66%)",
			votes:   append(nVotes("a", 2), nVotes("b", 1)...),
			want:    "a",
			reached: true,
		},
		{
			name:    "3-2 (60% < 66%)",
			votes:   append(nVotes("a", 3), nVotes("b", 2)...),
			want:    "",
			reached: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := checkConsensus(tt.votes, flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SUPER_MAJORITY)
			if ok != tt.reached || got != tt.want {
				t.Errorf("checkConsensus() = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.reached)
			}
		})
	}
}

func TestCheckConsensus_Unanimity(t *testing.T) {
	tests := []struct {
		name    string
		votes   []jurorVerdict
		want    string
		reached bool
	}{
		{
			name:    "3-0",
			votes:   nVotes("a", 3),
			want:    "a",
			reached: true,
		},
		{
			name:    "2-1 not unanimous",
			votes:   append(nVotes("a", 2), nVotes("b", 1)...),
			want:    "",
			reached: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := checkConsensus(tt.votes, flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_UNANIMITY)
			if ok != tt.reached || got != tt.want {
				t.Errorf("checkConsensus() = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.reached)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func assertRoutedTo(t *testing.T, spy *gateSpy, expected string) {
	t.Helper()
	if len(spy.RoutedOutputs) != 1 {
		t.Fatalf("expected 1 routed output, got %d: %v", len(spy.RoutedOutputs), spy.RoutedOutputs)
	}
	if spy.RoutedOutputs[0] != expected {
		t.Errorf("routed to %q, want %q", spy.RoutedOutputs[0], expected)
	}
}

func parseStoredResult(t *testing.T, spy *gateSpy) deliberationResult {
	t.Helper()
	data, ok := spy.StoredArtefacts[artefactDeliberationResult]
	if !ok {
		t.Fatal("deliberation-result artefact was not stored")
	}
	var result deliberationResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("unmarshal deliberation-result: %v", err)
	}
	return result
}

func nVotes(outcome string, n int) []jurorVerdict {
	votes := make([]jurorVerdict, n)
	for i := range votes {
		votes[i] = jurorVerdict{Outcome: outcome, Reasoning: "reason"}
	}
	return votes
}
