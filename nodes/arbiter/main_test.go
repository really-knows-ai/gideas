package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

// ---------------------------------------------------------------------------
// Happy path tests
// ---------------------------------------------------------------------------

func TestArbiter_FavourReviewer_VerdictReached(t *testing.T) {
	spy := newArbiterSpy()
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{
			Id:      "fb-1",
			Source:  "appraise",
			State:   flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
			Message: "Haiku lacks seasonal reference",
		},
	}
	spy.DeliberateResponse = &flowv1.DeliberateResponse{
		Outcome: "favour_reviewer",
		Justifications: []*flowv1.JurorJustification{
			{JurorId: "textualist", Outcome: "favour_reviewer", Reasoning: "The law requires seasonal reference"},
		},
		RoundsUsed: 1,
	}

	client := setupArbiterTest(t, spy)
	cfg := &arbiterConfig{}

	if err := handleArbiter(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleArbiter() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Should have called Deliberate once.
	if len(spy.DeliberateCalls) != 1 {
		t.Fatalf("expected 1 Deliberate call, got %d", len(spy.DeliberateCalls))
	}
	dc := spy.DeliberateCalls[0]
	if len(dc.AllowedOutcomes) != 2 {
		t.Fatalf("expected 2 allowed outcomes, got %v", dc.AllowedOutcomes)
	}

	// Should have called DraftLaw once with Tier 2 (RULING).
	if len(spy.DraftLawCalls) != 1 {
		t.Fatalf("expected 1 DraftLaw call, got %d", len(spy.DraftLawCalls))
	}
	dl := spy.DraftLawCalls[0]
	if dl.Tier != int32(flowv1.LawTier_LAW_TIER_RULING) {
		t.Fatalf("expected Tier 2 (RULING=%d), got %d", int32(flowv1.LawTier_LAW_TIER_RULING), dl.Tier)
	}
	if dl.Outcome != "favour_reviewer" {
		t.Fatalf("expected DraftLaw outcome favour_reviewer, got %s", dl.Outcome)
	}

	// Should have linked ruling to the deadlocked feedback item.
	if len(spy.LinkedRulings) != 1 {
		t.Fatalf("expected 1 LinkRuling call, got %d", len(spy.LinkedRulings))
	}
	if spy.LinkedRulings[0].FeedbackID != "fb-1" {
		t.Fatalf("expected LinkRuling for fb-1, got %s", spy.LinkedRulings[0].FeedbackID)
	}
	if spy.LinkedRulings[0].LawID != "law-ruling-001" {
		t.Fatalf("expected law_id=law-ruling-001, got %s", spy.LinkedRulings[0].LawID)
	}

	// Should route to sort.
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputSort {
		t.Fatalf("expected route to sort, got %v", spy.RoutedOutputs)
	}
}

func TestArbiter_FavourRefiner_VerdictReached(t *testing.T) {
	spy := newArbiterSpy()
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{
			Id:      "fb-2",
			Source:  "appraise",
			State:   flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
			Message: "Reviewer feedback excessive",
		},
	}
	spy.DeliberateResponse = &flowv1.DeliberateResponse{
		Outcome:    "favour_refiner",
		RoundsUsed: 2,
	}

	client := setupArbiterTest(t, spy)
	cfg := &arbiterConfig{}

	if err := handleArbiter(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleArbiter() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if len(spy.DraftLawCalls) != 1 {
		t.Fatalf("expected 1 DraftLaw call, got %d", len(spy.DraftLawCalls))
	}
	if spy.DraftLawCalls[0].Outcome != "favour_refiner" {
		t.Fatalf("expected outcome favour_refiner, got %s", spy.DraftLawCalls[0].Outcome)
	}

	if len(spy.LinkedRulings) != 1 || spy.LinkedRulings[0].FeedbackID != "fb-2" {
		t.Fatalf("expected LinkRuling for fb-2, got %v", spy.LinkedRulings)
	}

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputSort {
		t.Fatalf("expected route to sort, got %v", spy.RoutedOutputs)
	}
}

func TestArbiter_MultipleDeadlockedFeedback_AllLinked(t *testing.T) {
	spy := newArbiterSpy()
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-a", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED, Source: "appraise"},
		{Id: "fb-b", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED, Source: "quench"},
		{Id: "fb-resolved", State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED},
	}

	client := setupArbiterTest(t, spy)
	cfg := &arbiterConfig{}

	if err := handleArbiter(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleArbiter() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Should link ruling to both deadlocked items, not the resolved one.
	if len(spy.LinkedRulings) != 2 {
		t.Fatalf("expected 2 LinkRuling calls, got %d", len(spy.LinkedRulings))
	}
	ids := map[string]bool{}
	for _, lr := range spy.LinkedRulings {
		ids[lr.FeedbackID] = true
	}
	if !ids["fb-a"] || !ids["fb-b"] {
		t.Fatalf("expected LinkRuling for fb-a and fb-b, got %v", spy.LinkedRulings)
	}
}

// ---------------------------------------------------------------------------
// Hung jury tests
// ---------------------------------------------------------------------------

func TestArbiter_HungJury_RoutesToAdvocate(t *testing.T) {
	spy := newArbiterSpy()
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-3", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED, Source: "appraise"},
	}
	spy.DeliberateResponse = &flowv1.DeliberateResponse{
		Hung:       true,
		RoundsUsed: 3,
	}

	client := setupArbiterTest(t, spy)
	cfg := &arbiterConfig{}

	if err := handleArbiter(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleArbiter() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Should NOT call DraftLaw or LinkRuling.
	if len(spy.DraftLawCalls) != 0 {
		t.Fatalf("expected 0 DraftLaw calls for hung jury, got %d", len(spy.DraftLawCalls))
	}
	if len(spy.LinkedRulings) != 0 {
		t.Fatalf("expected 0 LinkRuling calls for hung jury, got %d", len(spy.LinkedRulings))
	}

	// Should route to advocate.
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "advocate" {
		t.Fatalf("expected route to advocate, got %v", spy.RoutedOutputs)
	}
}

// ---------------------------------------------------------------------------
// Evidence assembly tests
// ---------------------------------------------------------------------------

func TestArbiter_EvidenceBundleContainsAllSections(t *testing.T) {
	spy := newArbiterSpy()
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{
			Id:      "fb-ev",
			State:   flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
			Source:  "appraise",
			Message: "Missing kigo",
			History: []*flowv1.FeedbackEvent{
				{Actor: "appraise", Action: "add_feedback", Message: "Missing kigo"},
				{Actor: "refine", Action: "refuse", Message: "Kigo is implicit"},
			},
		},
	}
	spy.ArtefactContent = []byte("An old silent pond / A frog jumps into the pond / Splash! Silence again")
	spy.Laws = []*flowv1.Law{
		{
			Id:   "law-1",
			Goal: "Haiku must contain a seasonal reference",
			Tier: flowv1.LawTier_LAW_TIER_FINDING,
		},
	}
	spy.FrictionAggregates = []*flowv1.FrictionAggregate{
		{LawId: "law-1", NodeId: "appraise", EventCount: 5, TotalMagnitude: 12.5},
	}

	// Capture evidence via Deliberate call.
	var capturedEvidence string
	origResp := spy.DeliberateResponse
	spy.DeliberateResponse = origResp
	client := setupArbiterTest(t, spy)
	cfg := &arbiterConfig{}

	if err := handleArbiter(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleArbiter() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if len(spy.DeliberateCalls) != 1 {
		t.Fatalf("expected 1 Deliberate call, got %d", len(spy.DeliberateCalls))
	}
	capturedEvidence = spy.DeliberateCalls[0].Evidence

	// Verify all sections are present.
	sections := []string{
		"## Artefact",
		"An old silent pond",
		"## Deadlocked Feedback",
		"fb-ev",
		"Missing kigo",
		"## Relevant Laws",
		"law-1",
		"## Friction Summary",
		"magnitude=12.50",
	}
	for _, section := range sections {
		if !strings.Contains(capturedEvidence, section) {
			t.Errorf("evidence missing section %q", section)
		}
	}
}

// ---------------------------------------------------------------------------
// Configuration tests
// ---------------------------------------------------------------------------

func TestArbiter_ConfigPassedToDeliberate(t *testing.T) {
	spy := newArbiterSpy()
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-cfg", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED, Source: "appraise"},
	}

	client := setupArbiterTest(t, spy)
	cfg := &arbiterConfig{
		ConsensusStrategy: "SUPER_MAJORITY",
		MaxRounds:         5,
		JurySize:          3,
	}

	if err := handleArbiter(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleArbiter() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if len(spy.DeliberateCalls) != 1 {
		t.Fatalf("expected 1 Deliberate call, got %d", len(spy.DeliberateCalls))
	}
	dc := spy.DeliberateCalls[0]
	if dc.Strategy != flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SUPER_MAJORITY {
		t.Fatalf("expected SUPER_MAJORITY, got %v", dc.Strategy)
	}
	if dc.MaxRounds != 5 {
		t.Fatalf("expected maxRounds=5, got %d", dc.MaxRounds)
	}
	if dc.JurySize != 3 {
		t.Fatalf("expected jurySize=3, got %d", dc.JurySize)
	}
}

func TestArbiter_DefaultConfig(t *testing.T) {
	spy := newArbiterSpy()
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-def", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED, Source: "appraise"},
	}

	client := setupArbiterTest(t, spy)
	cfg := &arbiterConfig{} // all zero values

	if err := handleArbiter(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleArbiter() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	dc := spy.DeliberateCalls[0]
	if dc.Strategy != flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY {
		t.Fatalf("expected SIMPLE_MAJORITY default, got %v", dc.Strategy)
	}
	if dc.MaxRounds != defaultMaxRounds {
		t.Fatalf("expected default maxRounds=%d, got %d", defaultMaxRounds, dc.MaxRounds)
	}
	if dc.JurySize != defaultJurySize {
		t.Fatalf("expected default jurySize=%d, got %d", defaultJurySize, dc.JurySize)
	}
}

// ---------------------------------------------------------------------------
// No deadlocked feedback
// ---------------------------------------------------------------------------

func TestArbiter_NoDeadlockedFeedback_RoutesToSort(t *testing.T) {
	spy := newArbiterSpy()
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-ok", State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED},
	}

	client := setupArbiterTest(t, spy)
	cfg := &arbiterConfig{}

	if err := handleArbiter(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleArbiter() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Should not deliberate.
	if len(spy.DeliberateCalls) != 0 {
		t.Fatalf("expected 0 Deliberate calls, got %d", len(spy.DeliberateCalls))
	}

	// Should route back to sort.
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputSort {
		t.Fatalf("expected route to sort, got %v", spy.RoutedOutputs)
	}
}

// ---------------------------------------------------------------------------
// Error propagation tests
// ---------------------------------------------------------------------------

func TestArbiter_Error_GetFlowTopologyFails(t *testing.T) {
	spy := newArbiterSpy()
	spy.GetFlowTopologyErr = fmt.Errorf("topology unavailable")
	client := setupArbiterTest(t, spy)

	err := handleArbiter(context.Background(), client, &arbiterConfig{})
	if err == nil {
		t.Fatal("expected error from GetFlowTopology failure")
	}
}

func TestArbiter_Error_GetFeedbackFails(t *testing.T) {
	spy := newArbiterSpy()
	spy.GetFeedbackErr = fmt.Errorf("feedback unavailable")
	client := setupArbiterTest(t, spy)

	err := handleArbiter(context.Background(), client, &arbiterConfig{})
	if err == nil {
		t.Fatal("expected error from GetFeedback failure")
	}
}

func TestArbiter_Error_GetArtefactFails(t *testing.T) {
	spy := newArbiterSpy()
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-err", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED, Source: "appraise"},
	}
	spy.GetArtefactErr = fmt.Errorf("artefact unavailable")
	client := setupArbiterTest(t, spy)

	err := handleArbiter(context.Background(), client, &arbiterConfig{})
	if err == nil {
		t.Fatal("expected error from GetArtefact failure")
	}
}

func TestArbiter_Error_DeliberateFails(t *testing.T) {
	spy := newArbiterSpy()
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-err", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED, Source: "appraise"},
	}
	spy.DeliberateErr = fmt.Errorf("jury unavailable")
	client := setupArbiterTest(t, spy)

	err := handleArbiter(context.Background(), client, &arbiterConfig{})
	if err == nil {
		t.Fatal("expected error from Deliberate failure")
	}
}

func TestArbiter_Error_DraftLawFails(t *testing.T) {
	spy := newArbiterSpy()
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-err", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED, Source: "appraise"},
	}
	spy.DraftLawErr = fmt.Errorf("clerk unavailable")
	client := setupArbiterTest(t, spy)

	err := handleArbiter(context.Background(), client, &arbiterConfig{})
	if err == nil {
		t.Fatal("expected error from DraftLaw failure")
	}
}

func TestArbiter_Error_LinkRulingFails(t *testing.T) {
	spy := newArbiterSpy()
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{Id: "fb-err", State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED, Source: "appraise"},
	}
	spy.LinkRulingErr = fmt.Errorf("archivist link failed")
	client := setupArbiterTest(t, spy)

	err := handleArbiter(context.Background(), client, &arbiterConfig{})
	if err == nil {
		t.Fatal("expected error from LinkRuling failure")
	}
}

// ---------------------------------------------------------------------------
// parseConsensusStrategy unit tests
// ---------------------------------------------------------------------------

func TestParseConsensusStrategy(t *testing.T) {
	tests := []struct {
		input string
		want  flowv1.ConsensusStrategy
	}{
		{"", flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY},
		{"SIMPLE_MAJORITY", flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY},
		{"SUPER_MAJORITY", flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SUPER_MAJORITY},
		{"UNANIMITY", flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_UNANIMITY},
		{"  super_majority  ", flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SUPER_MAJORITY},
		{"unknown", flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("input=%q", tt.input), func(t *testing.T) {
			got := parseConsensusStrategy(tt.input)
			if got != tt.want {
				t.Fatalf("parseConsensusStrategy(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Config defaults tests
// ---------------------------------------------------------------------------

func TestArbiterConfig_Defaults(t *testing.T) {
	cfg := &arbiterConfig{}
	if cfg.maxRounds() != defaultMaxRounds {
		t.Fatalf("expected default maxRounds=%d, got %d", defaultMaxRounds, cfg.maxRounds())
	}
	if cfg.jurySize() != defaultJurySize {
		t.Fatalf("expected default jurySize=%d, got %d", defaultJurySize, cfg.jurySize())
	}
	if cfg.strategy() != flowv1.ConsensusStrategy_CONSENSUS_STRATEGY_SIMPLE_MAJORITY {
		t.Fatalf("expected default SIMPLE_MAJORITY, got %v", cfg.strategy())
	}
}
