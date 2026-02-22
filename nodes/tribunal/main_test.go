package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

// ---------------------------------------------------------------------------
// Tier 1 (Finding) tests
// ---------------------------------------------------------------------------

func TestTribunal_Tier1_Promote(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	spy.DeliberateResponse = &flowv1.DeliberateResponse{
		Outcome:    "promote",
		RoundsUsed: 1,
	}

	client := setupTribunalTest(t, spy)
	cfg := &tribunalConfig{}

	if err := handleTribunal(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleTribunal() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Should call DraftLaw with Tier 2 (RULING).
	if len(spy.DraftLawCalls) != 1 {
		t.Fatalf("expected 1 DraftLaw call, got %d", len(spy.DraftLawCalls))
	}
	dl := spy.DraftLawCalls[0]
	if dl.Tier != int32(flowv1.LawTier_LAW_TIER_RULING) {
		t.Fatalf("expected Tier 2 (RULING=%d), got %d", int32(flowv1.LawTier_LAW_TIER_RULING), dl.Tier)
	}
	if dl.Outcome != "promote" {
		t.Fatalf("expected outcome promote, got %s", dl.Outcome)
	}

	// Should Complete, not route.
	if !spy.Completed {
		t.Fatal("expected Complete() to be called")
	}
	if len(spy.RoutedOutputs) != 0 {
		t.Fatalf("expected no routing, got %v", spy.RoutedOutputs)
	}
}

func TestTribunal_DraftAndComplete_Verdicts(t *testing.T) {
	tests := []struct {
		name        string
		tier        flowv1.LawTier
		outcome     string
		wantTier    int32
		wantOutcome string
	}{
		{
			name:        "Tier1_Retire",
			tier:        flowv1.LawTier_LAW_TIER_FINDING,
			outcome:     "retire",
			wantTier:    int32(flowv1.LawTier_LAW_TIER_FINDING),
			wantOutcome: "retire",
		},
		{
			name:        "Tier2_Retire",
			tier:        flowv1.LawTier_LAW_TIER_RULING,
			outcome:     "retire",
			wantTier:    int32(flowv1.LawTier_LAW_TIER_RULING),
			wantOutcome: "retire",
		},
		{
			name:        "Tier2_Demote",
			tier:        flowv1.LawTier_LAW_TIER_RULING,
			outcome:     "demote",
			wantTier:    int32(flowv1.LawTier_LAW_TIER_FINDING),
			wantOutcome: "demote",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := newTribunalSpy(tt.tier)
			spy.DeliberateResponse = &flowv1.DeliberateResponse{
				Outcome:    tt.outcome,
				RoundsUsed: 1,
			}

			client := setupTribunalTest(t, spy)
			cfg := &tribunalConfig{}

			if err := handleTribunal(context.Background(), client, cfg); err != nil {
				t.Fatalf("handleTribunal() error: %v", err)
			}

			spy.mu.Lock()
			defer spy.mu.Unlock()

			if len(spy.DraftLawCalls) != 1 {
				t.Fatalf("expected 1 DraftLaw call, got %d", len(spy.DraftLawCalls))
			}
			dl := spy.DraftLawCalls[0]
			if dl.Tier != tt.wantTier {
				t.Fatalf("expected tier %d, got %d", tt.wantTier, dl.Tier)
			}
			if dl.Outcome != tt.wantOutcome {
				t.Fatalf("expected outcome %s, got %s", tt.wantOutcome, dl.Outcome)
			}
			if !spy.Completed {
				t.Fatal("expected Complete()")
			}
		})
	}
}

func TestTribunal_Tier1_AllowedOutcomes(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	client := setupTribunalTest(t, spy)
	cfg := &tribunalConfig{}

	if err := handleTribunal(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleTribunal() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if len(spy.DeliberateCalls) != 1 {
		t.Fatalf("expected 1 Deliberate call, got %d", len(spy.DeliberateCalls))
	}
	dc := spy.DeliberateCalls[0]
	// Tier 1 should have exactly 2 outcomes: promote, retire.
	if len(dc.AllowedOutcomes) != 2 {
		t.Fatalf("expected 2 allowed outcomes for Tier 1, got %v", dc.AllowedOutcomes)
	}
	if dc.AllowedOutcomes[0] != "promote" || dc.AllowedOutcomes[1] != "retire" {
		t.Fatalf("expected [promote, retire], got %v", dc.AllowedOutcomes)
	}
}

// ---------------------------------------------------------------------------
// Tier 2 (Ruling) tests
// ---------------------------------------------------------------------------

func TestTribunal_Tier2_Promote_RoutesToAdvocate(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_RULING)
	spy.DeliberateResponse = &flowv1.DeliberateResponse{
		Outcome:    "promote",
		RoundsUsed: 2,
	}

	client := setupTribunalTest(t, spy)
	cfg := &tribunalConfig{}

	if err := handleTribunal(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleTribunal() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Tier 2 promote routes to Advocate for HITL ratification.
	// Should NOT call DraftLaw or Complete.
	if len(spy.DraftLawCalls) != 0 {
		t.Fatalf("expected 0 DraftLaw calls for Tier 2 promote, got %d", len(spy.DraftLawCalls))
	}
	if spy.Completed {
		t.Fatal("expected no Complete for Tier 2 promote")
	}

	// Should route to advocate.
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "advocate" {
		t.Fatalf("expected route to advocate, got %v", spy.RoutedOutputs)
	}
}

func TestTribunal_Tier2_AllowedOutcomes(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_RULING)
	client := setupTribunalTest(t, spy)
	cfg := &tribunalConfig{}

	if err := handleTribunal(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleTribunal() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	dc := spy.DeliberateCalls[0]
	// Tier 2 should have 3 outcomes: promote, retire, demote.
	if len(dc.AllowedOutcomes) != 3 {
		t.Fatalf("expected 3 allowed outcomes for Tier 2, got %v", dc.AllowedOutcomes)
	}
}

// ---------------------------------------------------------------------------
// Hung jury tests
// ---------------------------------------------------------------------------

func TestTribunal_HungJury_RoutesToAdvocate(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	spy.DeliberateResponse = &flowv1.DeliberateResponse{
		Hung:       true,
		RoundsUsed: 3,
	}

	client := setupTribunalTest(t, spy)
	cfg := &tribunalConfig{}

	if err := handleTribunal(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleTribunal() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if len(spy.DraftLawCalls) != 0 {
		t.Fatalf("expected 0 DraftLaw calls for hung jury, got %d", len(spy.DraftLawCalls))
	}
	if spy.Completed {
		t.Fatal("expected no Complete for hung jury")
	}

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "advocate" {
		t.Fatalf("expected route to advocate, got %v", spy.RoutedOutputs)
	}
}

// ---------------------------------------------------------------------------
// Evidence assembly tests
// ---------------------------------------------------------------------------

func TestTribunal_EvidenceContainsLawAndFriction(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	spy.FrictionAggregates = []*flowv1.FrictionAggregate{
		{LawId: "law-under-review-001", NodeId: "sort", EventCount: 3, TotalMagnitude: 7.5},
	}
	spy.RelatedLaws = []*flowv1.Law{
		{Id: "law-related-001", Goal: "Related law", Tier: flowv1.LawTier_LAW_TIER_RULING},
	}

	client := setupTribunalTest(t, spy)
	cfg := &tribunalConfig{}

	if err := handleTribunal(context.Background(), client, cfg); err != nil {
		t.Fatalf("handleTribunal() error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if len(spy.DeliberateCalls) != 1 {
		t.Fatalf("expected 1 Deliberate call, got %d", len(spy.DeliberateCalls))
	}
	evidence := spy.DeliberateCalls[0].Evidence

	sections := []string{
		"## Law Under Review",
		"law-under-review-001",
		"seasonal reference",
		"## Friction Summary",
		"magnitude=7.50",
		"## Related Laws",
		"law-related-001",
	}
	for _, section := range sections {
		if !strings.Contains(evidence, section) {
			t.Errorf("evidence missing section %q", section)
		}
	}
}

// ---------------------------------------------------------------------------
// Error propagation tests
// ---------------------------------------------------------------------------

func TestTribunal_Error_GetArtefactFails(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	spy.GetArtefactErr = fmt.Errorf("artefact unavailable")
	client := setupTribunalTest(t, spy)

	err := handleTribunal(context.Background(), client, &tribunalConfig{})
	if err == nil {
		t.Fatal("expected error from GetArtefact failure")
	}
}

func TestTribunal_Error_GetLawFails(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	spy.GetLawErr = fmt.Errorf("librarian unavailable")
	client := setupTribunalTest(t, spy)

	err := handleTribunal(context.Background(), client, &tribunalConfig{})
	if err == nil {
		t.Fatal("expected error from GetLaw failure")
	}
}

func TestTribunal_Error_QueryFrictionFails(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	spy.QueryFrictionErr = fmt.Errorf("monitor unavailable")
	client := setupTribunalTest(t, spy)

	err := handleTribunal(context.Background(), client, &tribunalConfig{})
	if err == nil {
		t.Fatal("expected error from QueryFriction failure")
	}
}

func TestTribunal_Error_DeliberateFails(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	spy.DeliberateErr = fmt.Errorf("jury unavailable")
	client := setupTribunalTest(t, spy)

	err := handleTribunal(context.Background(), client, &tribunalConfig{})
	if err == nil {
		t.Fatal("expected error from Deliberate failure")
	}
}

func TestTribunal_Error_DraftLawFails(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	spy.DraftLawErr = fmt.Errorf("clerk unavailable")
	client := setupTribunalTest(t, spy)

	err := handleTribunal(context.Background(), client, &tribunalConfig{})
	if err == nil {
		t.Fatal("expected error from DraftLaw failure")
	}
}

func TestTribunal_Error_CompleteFails(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	spy.CompleteErr = fmt.Errorf("completion rejected")
	client := setupTribunalTest(t, spy)

	err := handleTribunal(context.Background(), client, &tribunalConfig{})
	if err == nil {
		t.Fatal("expected error from Complete failure")
	}
}

func TestTribunal_Error_EmptyLawReference(t *testing.T) {
	spy := newTribunalSpy(flowv1.LawTier_LAW_TIER_FINDING)
	spy.LawReferenceContent = []byte("")
	client := setupTribunalTest(t, spy)

	err := handleTribunal(context.Background(), client, &tribunalConfig{})
	if err == nil {
		t.Fatal("expected error for empty law reference")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("expected 'empty' in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// frameQuestion unit tests
// ---------------------------------------------------------------------------

func TestFrameQuestion_Tier1(t *testing.T) {
	q, outcomes := frameQuestion(flowv1.LawTier_LAW_TIER_FINDING)
	if !strings.Contains(q, "Finding") {
		t.Errorf("expected 'Finding' in question, got: %s", q)
	}
	if len(outcomes) != 2 {
		t.Fatalf("expected 2 outcomes for Tier 1, got %v", outcomes)
	}
}

func TestFrameQuestion_Tier2(t *testing.T) {
	q, outcomes := frameQuestion(flowv1.LawTier_LAW_TIER_RULING)
	if !strings.Contains(q, "Ruling") {
		t.Errorf("expected 'Ruling' in question, got: %s", q)
	}
	if len(outcomes) != 3 {
		t.Fatalf("expected 3 outcomes for Tier 2, got %v", outcomes)
	}
}
