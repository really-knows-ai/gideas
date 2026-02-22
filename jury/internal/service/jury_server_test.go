package service

import (
	"context"
	"fmt"
	"net"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/jury/internal/jurors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ---------------------------------------------------------------------------
// Mock Juror Factory
// ---------------------------------------------------------------------------

// mockFactory creates mock jurors that return a fixed outcome.
type mockFactory struct {
	outcome string
	reason  string
	err     error
}

func (f *mockFactory) BuildPanel(allowedOutcomes []string, jurySize int32) ([]jurors.Juror, error) {
	if f.err != nil {
		return nil, f.err
	}
	panel := make([]jurors.Juror, jurySize)
	types := SelectJurorTypes(jurySize)
	for i := range jurySize {
		panel[i] = &mockJuror{
			name:    types[i],
			outcome: f.outcome,
			reason:  f.reason,
		}
	}
	return panel, nil
}

// splitFactory creates jurors where the majority votes one way and the rest vote another.
type splitFactory struct {
	majorityOutcome  string
	minorityOutcome  string
	majorityFraction float64 // e.g. 0.6 for 60%
}

func (f *splitFactory) BuildPanel(allowedOutcomes []string, jurySize int32) ([]jurors.Juror, error) {
	panel := make([]jurors.Juror, jurySize)
	types := SelectJurorTypes(jurySize)
	majorityCount := int32(float64(jurySize) * f.majorityFraction)
	if majorityCount == 0 {
		majorityCount = 1
	}
	for i := range jurySize {
		outcome := f.minorityOutcome
		if i < majorityCount {
			outcome = f.majorityOutcome
		}
		panel[i] = &mockJuror{
			name:    types[i],
			outcome: outcome,
			reason:  fmt.Sprintf("mock reasoning for %s", outcome),
		}
	}
	return panel, nil
}

// mockJuror implements jurors.Juror with a fixed vote.
type mockJuror struct {
	name    string
	outcome string
	reason  string
}

func (m *mockJuror) Run(_ context.Context, _ jurors.JurorQueryData) (*jurors.JurorOutput, error) {
	return &jurors.JurorOutput{
		Outcome:   m.outcome,
		Reasoning: m.reason,
	}, nil
}

func (m *mockJuror) Name() string { return m.name }

// ---------------------------------------------------------------------------
// Test Helpers
// ---------------------------------------------------------------------------

func setupTestServer(t *testing.T, factory JurorFactory) flowv1.JuryServiceClient {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	srv := grpc.NewServer()
	jurySrv := NewJuryServer(factory)
	flowv1.RegisterJuryServiceServer(srv, jurySrv)

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })

	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return flowv1.NewJuryServiceClient(conn)
}

// ---------------------------------------------------------------------------
// Server Tests
// ---------------------------------------------------------------------------

func TestDeliberate_UnanimousConsensus(t *testing.T) {
	client := setupTestServer(t, &mockFactory{
		outcome: "favour_refiner",
		reason:  "clear legal basis",
	})

	resp, err := client.Deliberate(context.Background(), &flowv1.DeliberateRequest{
		Question:          "Should the refiner's work be accepted?",
		Evidence:          "evidence bundle",
		AllowedOutcomes:   []string{"favour_refiner", "favour_reviewer"},
		ConsensusStrategy: flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY,
		MaxRounds:         3,
		JurySize:          5,
	})
	if err != nil {
		t.Fatalf("Deliberate() error: %v", err)
	}

	if resp.GetHung() {
		t.Error("expected consensus, got hung")
	}
	if resp.GetOutcome() != "favour_refiner" {
		t.Errorf("outcome = %q, want %q", resp.GetOutcome(), "favour_refiner")
	}
	if resp.GetRoundsUsed() != 1 {
		t.Errorf("rounds_used = %d, want 1", resp.GetRoundsUsed())
	}
	if len(resp.GetJustifications()) != 5 {
		t.Errorf("justifications = %d, want 5", len(resp.GetJustifications()))
	}
}

func TestDeliberate_HungJury(t *testing.T) {
	// Perfect 50/50 split will never reach simple majority.
	client := setupTestServer(t, &splitFactory{
		majorityOutcome:  "A",
		minorityOutcome:  "B",
		majorityFraction: 0.5,
	})

	resp, err := client.Deliberate(context.Background(), &flowv1.DeliberateRequest{
		Question:          "deadlock question",
		Evidence:          "evidence",
		AllowedOutcomes:   []string{"A", "B"},
		ConsensusStrategy: flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_UNANIMITY,
		MaxRounds:         2,
		JurySize:          4,
	})
	if err != nil {
		t.Fatalf("Deliberate() error: %v", err)
	}

	if !resp.GetHung() {
		t.Error("expected hung, got consensus")
	}
	if resp.GetOutcome() != "" {
		t.Errorf("outcome = %q, want empty", resp.GetOutcome())
	}
	if resp.GetRoundsUsed() != 2 {
		t.Errorf("rounds_used = %d, want 2", resp.GetRoundsUsed())
	}
}

func TestDeliberate_ValidationErrors(t *testing.T) {
	client := setupTestServer(t, &mockFactory{outcome: "A", reason: "r"})

	tests := []struct {
		name string
		req  *flowv1.DeliberateRequest
	}{
		{
			name: "empty question",
			req: &flowv1.DeliberateRequest{
				AllowedOutcomes:   []string{"A"},
				ConsensusStrategy: flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY,
				MaxRounds:         1,
				JurySize:          1,
			},
		},
		{
			name: "empty allowed_outcomes",
			req: &flowv1.DeliberateRequest{
				Question:          "q",
				ConsensusStrategy: flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY,
				MaxRounds:         1,
				JurySize:          1,
			},
		},
		{
			name: "zero jury_size",
			req: &flowv1.DeliberateRequest{
				Question:          "q",
				AllowedOutcomes:   []string{"A"},
				ConsensusStrategy: flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY,
				MaxRounds:         1,
				JurySize:          0,
			},
		},
		{
			name: "zero max_rounds",
			req: &flowv1.DeliberateRequest{
				Question:          "q",
				AllowedOutcomes:   []string{"A"},
				ConsensusStrategy: flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY,
				MaxRounds:         0,
				JurySize:          1,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.Deliberate(context.Background(), tc.req)
			if err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

func TestDeliberate_FactoryError(t *testing.T) {
	client := setupTestServer(t, &mockFactory{
		err: fmt.Errorf("provider unavailable"),
	})

	_, err := client.Deliberate(context.Background(), &flowv1.DeliberateRequest{
		Question:          "q",
		Evidence:          "e",
		AllowedOutcomes:   []string{"A"},
		ConsensusStrategy: flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY,
		MaxRounds:         1,
		JurySize:          3,
	})
	if err == nil {
		t.Fatal("expected error from factory failure")
	}
}

func TestDeliberate_NoFactory(t *testing.T) {
	client := setupTestServer(t, nil)

	_, err := client.Deliberate(context.Background(), &flowv1.DeliberateRequest{
		Question:          "q",
		Evidence:          "e",
		AllowedOutcomes:   []string{"A"},
		ConsensusStrategy: flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY,
		MaxRounds:         1,
		JurySize:          3,
	})
	if err == nil {
		t.Fatal("expected error when factory is nil")
	}
}

func TestDeliberate_SuperMajority(t *testing.T) {
	// 4 of 5 vote for A (80%) — exceeds 66% threshold.
	client := setupTestServer(t, &splitFactory{
		majorityOutcome:  "promote",
		minorityOutcome:  "retire",
		majorityFraction: 0.8,
	})

	resp, err := client.Deliberate(context.Background(), &flowv1.DeliberateRequest{
		Question:          "Should this law be promoted?",
		Evidence:          "friction data",
		AllowedOutcomes:   []string{"promote", "retire"},
		ConsensusStrategy: flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SUPER_MAJORITY,
		MaxRounds:         1,
		JurySize:          5,
	})
	if err != nil {
		t.Fatalf("Deliberate() error: %v", err)
	}

	if resp.GetHung() {
		t.Error("expected consensus, got hung")
	}
	if resp.GetOutcome() != "promote" {
		t.Errorf("outcome = %q, want %q", resp.GetOutcome(), "promote")
	}
}

func TestDeliberate_SingleJuror(t *testing.T) {
	client := setupTestServer(t, &mockFactory{
		outcome: "retire",
		reason:  "outdated",
	})

	resp, err := client.Deliberate(context.Background(), &flowv1.DeliberateRequest{
		Question:          "Should this finding be retired?",
		Evidence:          "evidence",
		AllowedOutcomes:   []string{"promote", "retire"},
		ConsensusStrategy: flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_UNANIMITY,
		MaxRounds:         1,
		JurySize:          1,
	})
	if err != nil {
		t.Fatalf("Deliberate() error: %v", err)
	}

	if resp.GetHung() {
		t.Error("expected consensus with single juror")
	}
	if resp.GetOutcome() != "retire" {
		t.Errorf("outcome = %q, want %q", resp.GetOutcome(), "retire")
	}
}

// ---------------------------------------------------------------------------
// Juror Selection Tests
// ---------------------------------------------------------------------------

func TestSelectJurorTypes_Diversity(t *testing.T) {
	types := SelectJurorTypes(5)
	if len(types) != 5 {
		t.Fatalf("expected 5 types, got %d", len(types))
	}

	// All 5 should be unique.
	seen := make(map[string]bool)
	for _, typ := range types {
		if seen[typ] {
			t.Errorf("duplicate juror type: %s", typ)
		}
		seen[typ] = true
	}
}

func TestSelectJurorTypes_LargerThanPool(t *testing.T) {
	types := SelectJurorTypes(7)
	if len(types) != 7 {
		t.Fatalf("expected 7 types, got %d", len(types))
	}

	// First 5 should be all distinct, then cycling.
	seen := make(map[string]int)
	for _, typ := range types {
		seen[typ]++
	}

	// Should have all 5 types represented.
	if len(seen) != 5 {
		t.Errorf("expected all 5 types represented, got %d", len(seen))
	}

	// Two types should appear twice (7 = 5 + 2).
	doubleCount := 0
	for _, count := range seen {
		if count == 2 {
			doubleCount++
		}
	}
	if doubleCount != 2 {
		t.Errorf("expected 2 types with count=2, got %d", doubleCount)
	}
}

func TestSelectJurorTypes_SmallPanel(t *testing.T) {
	types := SelectJurorTypes(3)
	if len(types) != 3 {
		t.Fatalf("expected 3 types, got %d", len(types))
	}

	// Should be the first 3 from the canonical order.
	expected := []string{"textualist", "pragmatist", "conservator"}
	for i, want := range expected {
		if types[i] != want {
			t.Errorf("types[%d] = %q, want %q", i, types[i], want)
		}
	}
}
