package deliberation

import (
	"context"
	"fmt"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/jury/internal/jurors"
)

// ---------------------------------------------------------------------------
// Mock Juror
// ---------------------------------------------------------------------------

// mockJuror implements jurors.Juror with a configurable vote.
type mockJuror struct {
	name    string
	outcome string
	reason  string
	err     error
}

func (m *mockJuror) Run(_ context.Context, _ jurors.JurorQueryData) (*jurors.JurorOutput, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &jurors.JurorOutput{
		Outcome:   m.outcome,
		Reasoning: m.reason,
	}, nil
}

func (m *mockJuror) Name() string {
	return m.name
}

func newMockJuror(name, outcome, reason string) jurors.Juror {
	return &mockJuror{name: name, outcome: outcome, reason: reason}
}

func newFailingJuror(name string, err error) jurors.Juror {
	return &mockJuror{name: name, err: err}
}

// ---------------------------------------------------------------------------
// Consensus Counting Tests
// ---------------------------------------------------------------------------

func TestCheckConsensus_SimpleMajority(t *testing.T) {
	tests := []struct {
		name    string
		votes   []*jurorVote
		want    string
		reached bool
	}{
		{
			name: "3 of 5 — reached",
			votes: []*jurorVote{
				{Outcome: "A"}, {Outcome: "A"}, {Outcome: "A"},
				{Outcome: "B"}, {Outcome: "B"},
			},
			want:    "A",
			reached: true,
		},
		{
			name: "2 of 4 — not reached (tie, not >50%)",
			votes: []*jurorVote{
				{Outcome: "A"}, {Outcome: "A"},
				{Outcome: "B"}, {Outcome: "B"},
			},
			want:    "",
			reached: false,
		},
		{
			name: "1 of 1 — reached",
			votes: []*jurorVote{
				{Outcome: "A"},
			},
			want:    "A",
			reached: true,
		},
		{
			name: "unanimous 5 — reached",
			votes: []*jurorVote{
				{Outcome: "X"}, {Outcome: "X"}, {Outcome: "X"},
				{Outcome: "X"}, {Outcome: "X"},
			},
			want:    "X",
			reached: true,
		},
		{
			name:    "empty votes",
			votes:   []*jurorVote{},
			want:    "",
			reached: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, reached := checkConsensus(tc.votes, flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY)
			if got != tc.want || reached != tc.reached {
				t.Errorf("got (%q, %v), want (%q, %v)", got, reached, tc.want, tc.reached)
			}
		})
	}
}

func TestCheckConsensus_SuperMajority(t *testing.T) {
	tests := []struct {
		name    string
		votes   []*jurorVote
		want    string
		reached bool
	}{
		{
			name: "4 of 5 (80%) — reached",
			votes: []*jurorVote{
				{Outcome: "A"}, {Outcome: "A"}, {Outcome: "A"}, {Outcome: "A"},
				{Outcome: "B"},
			},
			want:    "A",
			reached: true,
		},
		{
			name: "2 of 3 (66.7%) — reached (exact threshold)",
			votes: []*jurorVote{
				{Outcome: "A"}, {Outcome: "A"},
				{Outcome: "B"},
			},
			want:    "A",
			reached: true,
		},
		{
			name: "3 of 5 (60%) — not reached",
			votes: []*jurorVote{
				{Outcome: "A"}, {Outcome: "A"}, {Outcome: "A"},
				{Outcome: "B"}, {Outcome: "B"},
			},
			want:    "",
			reached: false,
		},
		{
			name: "1 of 2 (50%) — not reached",
			votes: []*jurorVote{
				{Outcome: "A"},
				{Outcome: "B"},
			},
			want:    "",
			reached: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, reached := checkConsensus(tc.votes, flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SUPER_MAJORITY)
			if got != tc.want || reached != tc.reached {
				t.Errorf("got (%q, %v), want (%q, %v)", got, reached, tc.want, tc.reached)
			}
		})
	}
}

func TestCheckConsensus_Unanimity(t *testing.T) {
	tests := []struct {
		name    string
		votes   []*jurorVote
		want    string
		reached bool
	}{
		{
			name: "all same — reached",
			votes: []*jurorVote{
				{Outcome: "A"}, {Outcome: "A"}, {Outcome: "A"},
			},
			want:    "A",
			reached: true,
		},
		{
			name: "one dissenter — not reached",
			votes: []*jurorVote{
				{Outcome: "A"}, {Outcome: "A"}, {Outcome: "B"},
			},
			want:    "",
			reached: false,
		},
		{
			name: "single juror — reached",
			votes: []*jurorVote{
				{Outcome: "X"},
			},
			want:    "X",
			reached: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, reached := checkConsensus(tc.votes, flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_UNANIMITY)
			if got != tc.want || reached != tc.reached {
				t.Errorf("got (%q, %v), want (%q, %v)", got, reached, tc.want, tc.reached)
			}
		})
	}
}

func TestCheckConsensus_Unspecified_FallsBackToSimpleMajority(t *testing.T) {
	votes := []*jurorVote{
		{Outcome: "A"}, {Outcome: "A"}, {Outcome: "A"},
		{Outcome: "B"}, {Outcome: "B"},
	}
	got, reached := checkConsensus(votes, flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_UNSPECIFIED)
	if !reached || got != "A" {
		t.Errorf("UNSPECIFIED fallback: got (%q, %v), want (\"A\", true)", got, reached)
	}
}

// ---------------------------------------------------------------------------
// Deliberation Tests (using mock jurors)
// ---------------------------------------------------------------------------

func TestDeliberate_ConsensusRound1(t *testing.T) {
	engine := NewEngine()

	panel := []jurors.Juror{
		newMockJuror("textualist", "favour_refiner", "strong legal basis"),
		newMockJuror("pragmatist", "favour_refiner", "practical outcome"),
		newMockJuror("conservator", "favour_refiner", "precedent supports"),
		newMockJuror("reformer", "favour_reviewer", "improvement needed"),
		newMockJuror("devils-advocate", "favour_refiner", "reasoning holds"),
	}

	result, err := engine.Deliberate(context.Background(), &DeliberationInput{
		Question:          "test question",
		Evidence:          "test evidence",
		AllowedOutcomes:   []string{"favour_refiner", "favour_reviewer"},
		ConsensusStrategy: flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY,
		MaxRounds:         3,
		Panel:             panel,
	})
	if err != nil {
		t.Fatalf("Deliberate() error: %v", err)
	}

	if result.Hung {
		t.Error("expected consensus, got hung")
	}
	if result.Outcome != "favour_refiner" {
		t.Errorf("outcome = %q, want %q", result.Outcome, "favour_refiner")
	}
	if result.RoundsUsed != 1 {
		t.Errorf("rounds_used = %d, want 1", result.RoundsUsed)
	}
	if len(result.Justifications) != 5 {
		t.Errorf("justifications = %d, want 5", len(result.Justifications))
	}
}

func TestDeliberate_HungAfterMaxRounds(t *testing.T) {
	engine := NewEngine()

	// Perfect tie — will never reach consensus.
	panel := []jurors.Juror{
		newMockJuror("textualist", "A", "reason A"),
		newMockJuror("pragmatist", "B", "reason B"),
	}

	result, err := engine.Deliberate(context.Background(), &DeliberationInput{
		Question:          "deadlock question",
		Evidence:          "evidence",
		AllowedOutcomes:   []string{"A", "B"},
		ConsensusStrategy: flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY,
		MaxRounds:         2,
		Panel:             panel,
	})
	if err != nil {
		t.Fatalf("Deliberate() error: %v", err)
	}

	if !result.Hung {
		t.Error("expected hung, got consensus")
	}
	if result.Outcome != "" {
		t.Errorf("outcome = %q, want empty", result.Outcome)
	}
	if result.RoundsUsed != 2 {
		t.Errorf("rounds_used = %d, want 2", result.RoundsUsed)
	}
}

// roundAwareJuror changes its vote on round 2+ (simulates deliberation).
type roundAwareJuror struct {
	name   string
	round  int
	round1 string
	round2 string
}

func (r *roundAwareJuror) Run(_ context.Context, data jurors.JurorQueryData) (*jurors.JurorOutput, error) {
	r.round++
	outcome := r.round1
	if r.round > 1 {
		outcome = r.round2
	}
	return &jurors.JurorOutput{
		Outcome:   outcome,
		Reasoning: fmt.Sprintf("round %d reasoning", r.round),
	}, nil
}

func (r *roundAwareJuror) Name() string { return r.name }

func TestDeliberate_ConsensusOnRound2(t *testing.T) {
	engine := NewEngine()

	// Round 1: tie (A, A, B). Round 2: dissenter switches to A.
	panel := []jurors.Juror{
		newMockJuror("textualist", "A", "reason A"),
		newMockJuror("pragmatist", "A", "reason A"),
		&roundAwareJuror{name: "reformer", round1: "B", round2: "A"},
	}

	result, err := engine.Deliberate(context.Background(), &DeliberationInput{
		Question:          "question",
		Evidence:          "evidence",
		AllowedOutcomes:   []string{"A", "B"},
		ConsensusStrategy: flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_UNANIMITY,
		MaxRounds:         3,
		Panel:             panel,
	})
	if err != nil {
		t.Fatalf("Deliberate() error: %v", err)
	}

	if result.Hung {
		t.Error("expected consensus on round 2, got hung")
	}
	if result.Outcome != "A" {
		t.Errorf("outcome = %q, want %q", result.Outcome, "A")
	}
	if result.RoundsUsed != 2 {
		t.Errorf("rounds_used = %d, want 2", result.RoundsUsed)
	}
}

func TestDeliberate_JurorError(t *testing.T) {
	engine := NewEngine()

	panel := []jurors.Juror{
		newMockJuror("textualist", "A", "reason"),
		newFailingJuror("pragmatist", fmt.Errorf("inference timeout")),
	}

	_, err := engine.Deliberate(context.Background(), &DeliberationInput{
		Question:          "question",
		Evidence:          "evidence",
		AllowedOutcomes:   []string{"A", "B"},
		ConsensusStrategy: flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY,
		MaxRounds:         1,
		Panel:             panel,
	})
	if err == nil {
		t.Fatal("expected error from failing juror")
	}
}

func TestDeliberate_EmptyPanel(t *testing.T) {
	engine := NewEngine()

	_, err := engine.Deliberate(context.Background(), &DeliberationInput{
		Question:          "question",
		Evidence:          "evidence",
		AllowedOutcomes:   []string{"A"},
		ConsensusStrategy: flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY,
		MaxRounds:         1,
		Panel:             nil,
	})
	if err == nil {
		t.Fatal("expected error for empty panel")
	}
}

func TestDeliberate_EmptyAllowedOutcomes(t *testing.T) {
	engine := NewEngine()

	_, err := engine.Deliberate(context.Background(), &DeliberationInput{
		Question:        "question",
		Evidence:        "evidence",
		AllowedOutcomes: nil,
		MaxRounds:       1,
		Panel:           []jurors.Juror{newMockJuror("a", "x", "y")},
	})
	if err == nil {
		t.Fatal("expected error for empty allowed_outcomes")
	}
}

func TestDeliberate_InvalidMaxRounds(t *testing.T) {
	engine := NewEngine()

	_, err := engine.Deliberate(context.Background(), &DeliberationInput{
		Question:        "question",
		Evidence:        "evidence",
		AllowedOutcomes: []string{"A"},
		MaxRounds:       0,
		Panel:           []jurors.Juror{newMockJuror("a", "A", "y")},
	})
	if err == nil {
		t.Fatal("expected error for zero max_rounds")
	}
}

// ---------------------------------------------------------------------------
// Peer Arguments Tests
// ---------------------------------------------------------------------------

func TestBuildPeerArguments(t *testing.T) {
	votes := []*jurorVote{
		{JurorName: "textualist", Outcome: "A", Reasoning: "legal basis"},
		{JurorName: "pragmatist", Outcome: "B", Reasoning: "practical concern"},
	}

	result := buildPeerArguments(votes)

	// Should be anonymised (no juror names).
	if result == "" {
		t.Fatal("expected non-empty peer arguments")
	}
	// Should contain vote outcomes and reasoning.
	if !containsAll(result, "A", "B", "legal basis", "practical concern") {
		t.Errorf("peer arguments missing expected content: %s", result)
	}
	// Should NOT contain juror names (anonymised).
	if containsAll(result, "textualist") || containsAll(result, "pragmatist") {
		t.Errorf("peer arguments should not contain juror names: %s", result)
	}
}

// ---------------------------------------------------------------------------
// Justification Building Tests
// ---------------------------------------------------------------------------

func TestBuildJustifications(t *testing.T) {
	votes := []*jurorVote{
		{JurorName: "textualist", Outcome: "A", Reasoning: "reason A"},
		{JurorName: "pragmatist", Outcome: "B", Reasoning: "reason B"},
	}

	justifications := buildJustifications(votes)

	if len(justifications) != 2 {
		t.Fatalf("expected 2 justifications, got %d", len(justifications))
	}
	if justifications[0].GetJurorId() != "textualist" {
		t.Errorf("juror_id = %q, want %q", justifications[0].GetJurorId(), "textualist")
	}
	if justifications[0].GetOutcome() != "A" {
		t.Errorf("outcome = %q, want %q", justifications[0].GetOutcome(), "A")
	}
	if justifications[1].GetReasoning() != "reason B" {
		t.Errorf("reasoning = %q, want %q", justifications[1].GetReasoning(), "reason B")
	}
}

// containsAll checks if s contains all the given substrings.
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && searchString(s, sub)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
