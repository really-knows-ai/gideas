package tally_test

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/tally"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Test outcome constants to satisfy goconst.
const (
	outcomeUphold  = "uphold"
	outcomeDismiss = "dismiss"
)

// ═══════════════════════════════════════════════════════════════════════════
// Tally (pure function)
// ═══════════════════════════════════════════════════════════════════════════

func TestTally_SimpleMajority_Consensus(t *testing.T) {
	t.Parallel()
	votes := []tally.JurorVote{
		{Outcome: outcomeUphold, Reasoning: "clear violation"},
		{Outcome: outcomeUphold, Reasoning: "agreed"},
		{Outcome: outcomeDismiss, Reasoning: "disagree"},
	}
	result := tally.Tally(votes, flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY)
	if !result.IsConsensus {
		t.Fatal("expected consensus")
	}
	if result.IsHung {
		t.Fatal("should not be hung")
	}
	if result.Outcome != outcomeUphold {
		t.Errorf("outcome = %q, want %q", result.Outcome, outcomeUphold)
	}
	if len(result.Votes) != 3 {
		t.Errorf("votes count = %d, want 3", len(result.Votes))
	}
}

func TestTally_SimpleMajority_Hung(t *testing.T) {
	t.Parallel()
	// 50-50 split is NOT a simple majority (need >50%).
	votes := []tally.JurorVote{
		{Outcome: outcomeUphold, Reasoning: "a"},
		{Outcome: outcomeDismiss, Reasoning: "b"},
	}
	result := tally.Tally(votes, flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY)
	if result.IsConsensus {
		t.Fatal("50-50 should not be consensus for simple majority")
	}
	if !result.IsHung {
		t.Fatal("should be hung")
	}
	if result.Outcome != "" {
		t.Errorf("hung result should have empty outcome, got %q", result.Outcome)
	}
}

func TestTally_SuperMajority_Consensus(t *testing.T) {
	t.Parallel()
	// 2 out of 3 = 66.67% >= 66% threshold.
	votes := []tally.JurorVote{
		{Outcome: outcomeUphold, Reasoning: "a"},
		{Outcome: outcomeUphold, Reasoning: "b"},
		{Outcome: outcomeDismiss, Reasoning: "c"},
	}
	result := tally.Tally(votes, flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SUPER_MAJORITY)
	if !result.IsConsensus {
		t.Fatal("2/3 should meet super majority threshold")
	}
	if result.Outcome != outcomeUphold {
		t.Errorf("outcome = %q, want %q", result.Outcome, outcomeUphold)
	}
}

func TestTally_SuperMajority_Hung(t *testing.T) {
	t.Parallel()
	// 3 out of 5 = 60% < 66% threshold.
	votes := []tally.JurorVote{
		{Outcome: outcomeUphold, Reasoning: "a"},
		{Outcome: outcomeUphold, Reasoning: "b"},
		{Outcome: outcomeUphold, Reasoning: "c"},
		{Outcome: outcomeDismiss, Reasoning: "d"},
		{Outcome: outcomeDismiss, Reasoning: "e"},
	}
	result := tally.Tally(votes, flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SUPER_MAJORITY)
	if result.IsConsensus {
		t.Fatal("3/5 (60%) should not meet super majority threshold")
	}
	if !result.IsHung {
		t.Fatal("should be hung")
	}
}

func TestTally_Unanimity_Consensus(t *testing.T) {
	t.Parallel()
	votes := []tally.JurorVote{
		{Outcome: outcomeUphold, Reasoning: "a"},
		{Outcome: outcomeUphold, Reasoning: "b"},
		{Outcome: outcomeUphold, Reasoning: "c"},
	}
	result := tally.Tally(votes, flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_UNANIMITY)
	if !result.IsConsensus {
		t.Fatal("all same vote should be unanimous")
	}
	if result.Outcome != outcomeUphold {
		t.Errorf("outcome = %q, want %q", result.Outcome, outcomeUphold)
	}
}

func TestTally_Unanimity_Hung(t *testing.T) {
	t.Parallel()
	votes := []tally.JurorVote{
		{Outcome: outcomeUphold, Reasoning: "a"},
		{Outcome: outcomeUphold, Reasoning: "b"},
		{Outcome: outcomeDismiss, Reasoning: "c"},
	}
	result := tally.Tally(votes, flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_UNANIMITY)
	if result.IsConsensus {
		t.Fatal("should not be unanimous with mixed votes")
	}
	if !result.IsHung {
		t.Fatal("should be hung")
	}
}

func TestTally_Unspecified_TreatedAsSimpleMajority(t *testing.T) {
	t.Parallel()
	votes := []tally.JurorVote{
		{Outcome: outcomeUphold, Reasoning: "a"},
		{Outcome: outcomeUphold, Reasoning: "b"},
		{Outcome: outcomeDismiss, Reasoning: "c"},
	}
	result := tally.Tally(votes, flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_UNSPECIFIED)
	if !result.IsConsensus {
		t.Fatal("UNSPECIFIED should behave as simple majority")
	}
	if result.Outcome != outcomeUphold {
		t.Errorf("outcome = %q, want %q", result.Outcome, outcomeUphold)
	}
}

func TestTally_EmptyVotes_IsHung(t *testing.T) {
	t.Parallel()
	result := tally.Tally(nil, flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY)
	if !result.IsHung {
		t.Fatal("empty votes should be hung")
	}
	if result.IsConsensus {
		t.Fatal("empty votes should not have consensus")
	}
}

func TestTally_SingleVote_SimpleMajority(t *testing.T) {
	t.Parallel()
	votes := []tally.JurorVote{
		{Outcome: outcomeUphold, Reasoning: "only one"},
	}
	result := tally.Tally(votes, flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY)
	if !result.IsConsensus {
		t.Fatal("single vote should be consensus for simple majority")
	}
	if result.Outcome != outcomeUphold {
		t.Errorf("outcome = %q, want %q", result.Outcome, outcomeUphold)
	}
}

func TestTally_ThreeWaySplit_Hung(t *testing.T) {
	t.Parallel()
	votes := []tally.JurorVote{
		{Outcome: "a", Reasoning: "r1"},
		{Outcome: "b", Reasoning: "r2"},
		{Outcome: "c", Reasoning: "r3"},
	}
	result := tally.Tally(votes, flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY)
	if result.IsConsensus {
		t.Fatal("three-way split should be hung")
	}
}

func TestTally_PreservesVotes(t *testing.T) {
	t.Parallel()
	votes := []tally.JurorVote{
		{Outcome: outcomeUphold, Reasoning: "reason-1"},
		{Outcome: outcomeDismiss, Reasoning: "reason-2"},
	}
	result := tally.Tally(votes, flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_UNANIMITY)
	if len(result.Votes) != 2 {
		t.Fatalf("votes count = %d, want 2", len(result.Votes))
	}
	if result.Votes[0].Reasoning != "reason-1" {
		t.Errorf("votes[0].Reasoning = %q, want %q", result.Votes[0].Reasoning, "reason-1")
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// BuildFanOutTasks (pure function)
// ═══════════════════════════════════════════════════════════════════════════

func TestBuildFanOutTasks_HappyPath(t *testing.T) {
	t.Parallel()
	cfg := tally.TallyConfig{
		JurySize:  3,
		JurorNode: "juror",
	}
	input := tally.RoundInput{
		Question:        "Should the law be upheld?",
		Evidence:        "Evidence bundle here",
		AllowedOutcomes: []string{outcomeUphold, outcomeDismiss},
	}

	tasks, err := tally.BuildFanOutTasks(cfg, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("task count = %d, want 3", len(tasks))
	}

	for i, task := range tasks {
		if task.TargetNode != "juror" {
			t.Errorf("task[%d].TargetNode = %q, want %q", i, task.TargetNode, "juror")
		}
		// Without prior-round reasoning: 3 artefacts.
		if len(task.Artefacts) != 3 {
			t.Errorf("task[%d] artefact count = %d, want 3", i, len(task.Artefacts))
		}
	}

	// Verify artefact contents on first task.
	first := tasks[0]
	if string(first.Artefacts[0].Content) != "Should the law be upheld?" {
		t.Errorf("question content = %q", string(first.Artefacts[0].Content))
	}
	if string(first.Artefacts[1].Content) != "Evidence bundle here" {
		t.Errorf("evidence content = %q", string(first.Artefacts[1].Content))
	}

	var outcomes []string
	if err := json.Unmarshal(first.Artefacts[2].Content, &outcomes); err != nil {
		t.Fatalf("unmarshal allowed-outcomes: %v", err)
	}
	if len(outcomes) != 2 || outcomes[0] != outcomeUphold || outcomes[1] != outcomeDismiss {
		t.Errorf("allowed-outcomes = %v, want [%s %s]", outcomes, outcomeUphold, outcomeDismiss)
	}
}

func TestBuildFanOutTasks_WithPriorRoundReasoning(t *testing.T) {
	t.Parallel()
	cfg := tally.TallyConfig{
		JurySize:  2,
		JurorNode: "juror",
	}
	input := tally.RoundInput{
		Question:            "Reconsider?",
		Evidence:            "Evidence",
		AllowedOutcomes:     []string{outcomeUphold},
		PriorRoundReasoning: "Prior reasoning from round 1",
	}

	tasks, err := tally.BuildFanOutTasks(cfg, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("task count = %d, want 2", len(tasks))
	}
	// With prior-round reasoning: 4 artefacts.
	for i, task := range tasks {
		if len(task.Artefacts) != 4 {
			t.Errorf("task[%d] artefact count = %d, want 4", i, len(task.Artefacts))
		}
	}
	// Check last artefact is prior-round-reasoning.
	last := tasks[0].Artefacts[3]
	if last.ID != tally.ArtefactPriorRound {
		t.Errorf("last artefact ID = %q, want %q", last.ID, tally.ArtefactPriorRound)
	}
	if string(last.Content) != "Prior reasoning from round 1" {
		t.Errorf("prior-round content = %q", string(last.Content))
	}
}

func TestBuildFanOutTasks_GovernedArtefactNames(t *testing.T) {
	t.Parallel()
	cfg := tally.TallyConfig{JurySize: 1, JurorNode: "juror"}
	input := tally.RoundInput{
		Question:            "Q",
		Evidence:            "E",
		AllowedOutcomes:     []string{"a"},
		PriorRoundReasoning: "P",
	}

	tasks, err := tally.BuildFanOutTasks(cfg, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	arts := tasks[0].Artefacts
	expected := []struct {
		id       string
		governed string
	}{
		{tally.ArtefactQuestion, tally.GovernedQuestion},
		{tally.ArtefactEvidence, tally.GovernedEvidence},
		{tally.ArtefactOutcomes, tally.GovernedOutcomes},
		{tally.ArtefactPriorRound, tally.GovernedPriorRound},
	}
	for i, exp := range expected {
		if arts[i].ID != exp.id {
			t.Errorf("artefact[%d].ID = %q, want %q", i, arts[i].ID, exp.id)
		}
		if arts[i].GovernedArtefact != exp.governed {
			t.Errorf("artefact[%d].GovernedArtefact = %q, want %q", i, arts[i].GovernedArtefact, exp.governed)
		}
	}
}

func TestBuildFanOutTasks_InvalidJurySize(t *testing.T) {
	t.Parallel()
	cfg := tally.TallyConfig{JurySize: 0, JurorNode: "juror"}
	input := tally.RoundInput{Question: "Q", Evidence: "E", AllowedOutcomes: []string{"a"}}

	_, err := tally.BuildFanOutTasks(cfg, input)
	if err == nil {
		t.Fatal("expected error for zero jury size")
	}
}

func TestBuildFanOutTasks_EmptyJurorNode(t *testing.T) {
	t.Parallel()
	cfg := tally.TallyConfig{JurySize: 3, JurorNode: ""}
	input := tally.RoundInput{Question: "Q", Evidence: "E", AllowedOutcomes: []string{"a"}}

	_, err := tally.BuildFanOutTasks(cfg, input)
	if err == nil {
		t.Fatal("expected error for empty juror node")
	}
}

func TestBuildFanOutTasks_EmptyQuestion(t *testing.T) {
	t.Parallel()
	cfg := tally.TallyConfig{JurySize: 3, JurorNode: "juror"}
	input := tally.RoundInput{Question: "", Evidence: "E", AllowedOutcomes: []string{"a"}}

	_, err := tally.BuildFanOutTasks(cfg, input)
	if err == nil {
		t.Fatal("expected error for empty question")
	}
}

func TestBuildFanOutTasks_EmptyAllowedOutcomes(t *testing.T) {
	t.Parallel()
	// Empty outcomes is valid — produces "[]" JSON. The juror node handles
	// validation of allowed outcomes.
	cfg := tally.TallyConfig{JurySize: 1, JurorNode: "juror"}
	input := tally.RoundInput{Question: "Q", Evidence: "E", AllowedOutcomes: nil}

	tasks, err := tally.BuildFanOutTasks(cfg, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var outcomes []string
	if err := json.Unmarshal(tasks[0].Artefacts[2].Content, &outcomes); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if outcomes != nil {
		t.Errorf("expected null/nil outcomes, got %v", outcomes)
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// SummariseRound (pure function)
// ═══════════════════════════════════════════════════════════════════════════

func TestSummariseRound_HappyPath(t *testing.T) {
	t.Parallel()
	votes := []tally.JurorVote{
		{Outcome: outcomeUphold, Reasoning: "Law was clearly violated"},
		{Outcome: outcomeDismiss, Reasoning: "Insufficient evidence"},
	}
	summary := tally.SummariseRound(votes)

	if summary == "" {
		t.Fatal("expected non-empty summary")
	}
	// Should contain both juror entries.
	if !strContains(summary, "Juror 1") {
		t.Error("missing Juror 1")
	}
	if !strContains(summary, "Juror 2") {
		t.Error("missing Juror 2")
	}
	if !strContains(summary, outcomeUphold) {
		t.Error("missing outcome 'uphold'")
	}
	if !strContains(summary, outcomeDismiss) {
		t.Error("missing outcome 'dismiss'")
	}
	if !strContains(summary, "Law was clearly violated") {
		t.Error("missing reasoning text")
	}
}

func TestSummariseRound_EmptyVotes(t *testing.T) {
	t.Parallel()
	summary := tally.SummariseRound(nil)
	if summary != "" {
		t.Errorf("expected empty summary for nil votes, got %q", summary)
	}
}

func TestSummariseRound_AnonymisedNumbering(t *testing.T) {
	t.Parallel()
	votes := []tally.JurorVote{
		{Outcome: "a", Reasoning: "r1"},
		{Outcome: "b", Reasoning: "r2"},
		{Outcome: "c", Reasoning: "r3"},
	}
	summary := tally.SummariseRound(votes)

	// Jurors are numbered 1-indexed.
	if !strContains(summary, "Juror 1") || !strContains(summary, "Juror 3") {
		t.Error("expected 1-indexed juror numbering")
	}
	// Should NOT contain any identifying information beyond the index.
	if strContains(summary, "Juror 0") {
		t.Error("numbering should be 1-indexed, not 0-indexed")
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// CollectVotes (requires gRPC spy)
// ═══════════════════════════════════════════════════════════════════════════

func TestCollectVotes_HappyPath(t *testing.T) {
	spy := newTallySpy()
	seedVerdictArtefact(spy, "child-1", outcomeUphold, "clear violation")
	seedVerdictArtefact(spy, "child-2", outcomeDismiss, "weak case")

	client := setupTallyTest(t, spy)
	children := []flow.ChildWorkitemStatus{
		{WorkitemID: "child-1", Phase: flow.PhaseCompleted},
		{WorkitemID: "child-2", Phase: flow.PhaseCompleted},
	}

	votes, err := tally.CollectVotes(context.Background(), client, children)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(votes) != 2 {
		t.Fatalf("vote count = %d, want 2", len(votes))
	}
	if votes[0].Outcome != outcomeUphold {
		t.Errorf("votes[0].Outcome = %q, want %q", votes[0].Outcome, outcomeUphold)
	}
	if votes[1].Outcome != outcomeDismiss {
		t.Errorf("votes[1].Outcome = %q, want %q", votes[1].Outcome, outcomeDismiss)
	}
	if votes[0].Reasoning != "clear violation" {
		t.Errorf("votes[0].Reasoning = %q, want %q", votes[0].Reasoning, "clear violation")
	}
}

func TestCollectVotes_MissingVerdict_Skipped(t *testing.T) {
	spy := newTallySpy()
	seedVerdictArtefact(spy, "child-1", outcomeUphold, "reason")
	// child-2 has NO verdict artefact.

	client := setupTallyTest(t, spy)
	children := []flow.ChildWorkitemStatus{
		{WorkitemID: "child-1", Phase: flow.PhaseCompleted},
		{WorkitemID: "child-2", Phase: flow.PhaseCompleted},
	}

	votes, err := tally.CollectVotes(context.Background(), client, children)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(votes) != 1 {
		t.Fatalf("vote count = %d, want 1 (missing verdict should be skipped)", len(votes))
	}
	if votes[0].Outcome != outcomeUphold {
		t.Errorf("votes[0].Outcome = %q, want %q", votes[0].Outcome, outcomeUphold)
	}
}

func TestCollectVotes_FailedChild_Error(t *testing.T) {
	spy := newTallySpy()
	seedVerdictArtefact(spy, "child-1", outcomeUphold, "reason")

	client := setupTallyTest(t, spy)
	children := []flow.ChildWorkitemStatus{
		{WorkitemID: "child-1", Phase: flow.PhaseCompleted},
		{WorkitemID: "child-2", Phase: flow.PhaseFailed},
	}

	_, err := tally.CollectVotes(context.Background(), client, children)
	if err == nil {
		t.Fatal("expected error for failed child")
	}
}

func TestCollectVotes_InvalidJSON_Error(t *testing.T) {
	spy := newTallySpy()
	spy.ChildArtefacts["child-1:verdict"] = []byte("not-json")

	client := setupTallyTest(t, spy)
	children := []flow.ChildWorkitemStatus{
		{WorkitemID: "child-1", Phase: flow.PhaseCompleted},
	}

	_, err := tally.CollectVotes(context.Background(), client, children)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strContains(err.Error(), "unmarshal") {
		t.Errorf("error should mention unmarshal, got: %v", err)
	}
}

func TestCollectVotes_EmptyChildren(t *testing.T) {
	spy := newTallySpy()
	client := setupTallyTest(t, spy)

	votes, err := tally.CollectVotes(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(votes) != 0 {
		t.Errorf("vote count = %d, want 0", len(votes))
	}
}

func TestCollectVotes_PreservesOrder(t *testing.T) {
	spy := newTallySpy()
	seedVerdictArtefact(spy, "child-a", "alpha", "r-a")
	seedVerdictArtefact(spy, "child-b", "beta", "r-b")
	seedVerdictArtefact(spy, "child-c", "gamma", "r-c")

	client := setupTallyTest(t, spy)
	children := []flow.ChildWorkitemStatus{
		{WorkitemID: "child-a", Phase: flow.PhaseCompleted},
		{WorkitemID: "child-b", Phase: flow.PhaseCompleted},
		{WorkitemID: "child-c", Phase: flow.PhaseCompleted},
	}

	votes, err := tally.CollectVotes(context.Background(), client, children)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(votes) != 3 {
		t.Fatalf("vote count = %d, want 3", len(votes))
	}
	if votes[0].Outcome != "alpha" || votes[1].Outcome != "beta" || votes[2].Outcome != "gamma" {
		t.Errorf("order not preserved: %v", votes)
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Integration: BuildFanOutTasks → Tally → SummariseRound
// ═══════════════════════════════════════════════════════════════════════════

func TestRoundTrip_BuildTally_Summarise(t *testing.T) {
	t.Parallel()

	cfg := tally.TallyConfig{
		ConsensusStrategy: flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY,
		MaxRounds:         3,
		JurySize:          5,
		JurorNode:         "juror",
	}

	// Round 1: build tasks.
	input := tally.RoundInput{
		Question:        "Should law-42 be retired?",
		Evidence:        "The law has been violated 17 times.",
		AllowedOutcomes: []string{"retire", "keep"},
	}
	tasks, err := tally.BuildFanOutTasks(cfg, input)
	if err != nil {
		t.Fatalf("BuildFanOutTasks: %v", err)
	}
	if len(tasks) != 5 {
		t.Fatalf("task count = %d, want 5", len(tasks))
	}

	// Simulate juror votes: 3 retire, 2 keep.
	votes := []tally.JurorVote{
		{Outcome: "retire", Reasoning: "Outdated"},
		{Outcome: "retire", Reasoning: "No longer relevant"},
		{Outcome: "keep", Reasoning: "Still useful"},
		{Outcome: "retire", Reasoning: "Overly strict"},
		{Outcome: "keep", Reasoning: "Edge cases exist"},
	}

	// Tally.
	result := tally.Tally(votes, cfg.ConsensusStrategy)
	result.Round = 1
	if !result.IsConsensus {
		t.Fatal("3/5 should be simple majority consensus")
	}
	if result.Outcome != "retire" {
		t.Errorf("outcome = %q, want %q", result.Outcome, "retire")
	}

	// Summarise (would be used if retry was needed).
	summary := tally.SummariseRound(result.Votes)
	if summary == "" {
		t.Fatal("expected non-empty summary")
	}
	if !strContains(summary, "Juror 5") {
		t.Error("summary should reference all 5 jurors")
	}
}

func TestMultiRound_RetryFlow(t *testing.T) {
	t.Parallel()

	cfg := tally.TallyConfig{
		ConsensusStrategy: flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_UNANIMITY,
		MaxRounds:         3,
		JurySize:          3,
		JurorNode:         "juror",
	}

	// Round 1: hung (not unanimous).
	round1Votes := []tally.JurorVote{
		{Outcome: outcomeUphold, Reasoning: "Clear"},
		{Outcome: outcomeUphold, Reasoning: "Agreed"},
		{Outcome: outcomeDismiss, Reasoning: "Disagree"},
	}
	result1 := tally.Tally(round1Votes, cfg.ConsensusStrategy)
	result1.Round = 1
	if result1.IsConsensus {
		t.Fatal("round 1 should be hung for unanimity")
	}

	// Build round 2 input with prior reasoning.
	summary := tally.SummariseRound(result1.Votes)
	input2 := tally.RoundInput{
		Question:            "Reconsider: should the law be upheld?",
		Evidence:            "Same evidence",
		AllowedOutcomes:     []string{outcomeUphold, outcomeDismiss},
		PriorRoundReasoning: summary,
	}
	tasks2, err := tally.BuildFanOutTasks(cfg, input2)
	if err != nil {
		t.Fatalf("BuildFanOutTasks round 2: %v", err)
	}
	if len(tasks2) != 3 {
		t.Fatalf("round 2 task count = %d, want 3", len(tasks2))
	}
	// Verify prior-round reasoning is attached (4th artefact).
	if len(tasks2[0].Artefacts) != 4 {
		t.Errorf("round 2 artefact count = %d, want 4", len(tasks2[0].Artefacts))
	}

	// Round 2: consensus (all agree).
	round2Votes := []tally.JurorVote{
		{Outcome: outcomeUphold, Reasoning: "Reconsidered"},
		{Outcome: outcomeUphold, Reasoning: "Convinced"},
		{Outcome: outcomeUphold, Reasoning: "Changed mind"},
	}
	result2 := tally.Tally(round2Votes, cfg.ConsensusStrategy)
	result2.Round = 2
	if !result2.IsConsensus {
		t.Fatal("round 2 should be consensus (unanimous)")
	}
	if result2.Outcome != outcomeUphold {
		t.Errorf("outcome = %q, want %q", result2.Outcome, outcomeUphold)
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Consensus edge cases
// ═══════════════════════════════════════════════════════════════════════════

func TestTally_SuperMajority_ExactThreshold(t *testing.T) {
	t.Parallel()
	// 4 out of 6 = 66.67% — should pass (4*3=12 >= 6*2=12).
	votes := make([]tally.JurorVote, 6)
	for i := range votes {
		if i < 4 {
			votes[i] = tally.JurorVote{Outcome: outcomeUphold, Reasoning: "yes"}
		} else {
			votes[i] = tally.JurorVote{Outcome: outcomeDismiss, Reasoning: "no"}
		}
	}
	result := tally.Tally(votes, flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SUPER_MAJORITY)
	if !result.IsConsensus {
		t.Fatal("4/6 should meet super majority (exactly 66.67%)")
	}
}

func TestTally_SuperMajority_BelowThreshold(t *testing.T) {
	t.Parallel()
	// 1 out of 2 = 50% < 66%.  (1*3=3 < 2*2=4).
	votes := []tally.JurorVote{
		{Outcome: outcomeUphold, Reasoning: "yes"},
		{Outcome: outcomeDismiss, Reasoning: "no"},
	}
	result := tally.Tally(votes, flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SUPER_MAJORITY)
	if result.IsConsensus {
		t.Fatal("1/2 should NOT meet super majority threshold")
	}
}

func TestTally_SimpleMajority_OddJury(t *testing.T) {
	t.Parallel()
	// 3 out of 5 = 60% > 50%.
	votes := []tally.JurorVote{
		{Outcome: outcomeUphold, Reasoning: "a"},
		{Outcome: outcomeUphold, Reasoning: "b"},
		{Outcome: outcomeUphold, Reasoning: "c"},
		{Outcome: outcomeDismiss, Reasoning: "d"},
		{Outcome: outcomeDismiss, Reasoning: "e"},
	}
	result := tally.Tally(votes, flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY)
	if !result.IsConsensus {
		t.Fatal("3/5 should be simple majority")
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Test spy and helpers
// ═══════════════════════════════════════════════════════════════════════════

// tallySpy implements just enough gRPC to support CollectVotes through the
// flow.Client. CollectVotes calls CollectArtefacts, which calls
// GetChildArtefact (GetArtefact with TargetWorkitemId).
type tallySpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer

	mu             sync.Mutex
	ChildArtefacts map[string][]byte // "childID:artefactID" → content
}

func newTallySpy() *tallySpy {
	return &tallySpy{
		ChildArtefacts: make(map[string][]byte),
	}
}

func (s *tallySpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *tallySpy) GetArtefact(
	_ context.Context, req *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// CollectVotes uses GetChildArtefact which sets TargetWorkitemId.
	target := req.GetTargetWorkitemId()
	if target == "" {
		return nil, status.Errorf(codes.NotFound, "no parent artefacts in spy")
	}
	key := target + ":" + req.GetArtefactId()
	content, ok := s.ChildArtefacts[key]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "child artefact %q not found", key)
	}
	return &flowv1.GetArtefactResponse{Content: content}, nil
}

func seedVerdictArtefact(spy *tallySpy, childID, outcome, reasoning string) {
	vote := tally.JurorVote{Outcome: outcome, Reasoning: reasoning}
	data, _ := json.Marshal(vote)
	spy.ChildArtefacts[childID+":verdict"] = data
}

func setupTallyTest(t *testing.T, spy *tallySpy) *flow.Client {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })

	t.Setenv(flow.EnvWorkitemID, "test-workitem")

	client, err := flow.NewClient(
		flow.WithSidecarAddress(lis.Addr().String()),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return client
}

func strContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
