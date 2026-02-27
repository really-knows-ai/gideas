package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

// ---------------------------------------------------------------------------
// Arbiter-hung escalation tests
// ---------------------------------------------------------------------------

func TestAdvocate_ArbiterHung_FavourRefiner(t *testing.T) {
	advCtx := &advocateContext{
		Type:         escalationArbiterHung,
		ArtefactKind: "haiku",
		FeedbackIDs:  []string{"fb-1", "fb-2"},
		Choices:      []string{"favour_refiner", "favour_reviewer"},
	}

	spy := newAdvocateSpy(advCtx)
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)

	wctx := &flowv1.WorkitemContext{WorkitemId: "wi-arb-hung-1"}

	errCh := make(chan error, 1)
	go func() {
		errCh <- handleAdvocate(context.Background(), client, qm, wctx)
	}()

	waitForEnqueue(t, qm, "wi-arb-hung-1")
	_, _ = qm.Claim(context.Background(), "wi-arb-hung-1")
	if err := qm.Decide(context.Background(), "wi-arb-hung-1", "favour_refiner"); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Should have stored a human-decision artefact.
	decision := mustGetStoredDecision(t, spy)
	if decision.EscalationType != escalationArbiterHung {
		t.Fatalf("expected escalation_type=arbiter-hung, got %s", decision.EscalationType)
	}
	if decision.Choice != "favour_refiner" {
		t.Fatalf("expected choice=favour_refiner, got %s", decision.Choice)
	}
	if decision.ArtefactKind != "haiku" {
		t.Fatalf("expected artefact_kind=haiku, got %s", decision.ArtefactKind)
	}
	if len(decision.FeedbackIDs) != 2 {
		t.Fatalf("expected 2 feedback_ids, got %d", len(decision.FeedbackIDs))
	}

	// Should route to clerk.
	assertRoutedTo(t, spy, outputClerk)

	// Should have paused and resumed timer.
	if spy.PauseTimerCalls != 1 {
		t.Errorf("expected 1 PauseTimer call, got %d", spy.PauseTimerCalls)
	}
	if spy.ResumeTimerCalls != 1 {
		t.Errorf("expected 1 ResumeTimer call, got %d", spy.ResumeTimerCalls)
	}
}

func TestAdvocate_ArbiterHung_FavourReviewer(t *testing.T) {
	advCtx := &advocateContext{
		Type:         escalationArbiterHung,
		ArtefactKind: "haiku",
		FeedbackIDs:  []string{"fb-3"},
		Choices:      []string{"favour_refiner", "favour_reviewer"},
	}

	spy := newAdvocateSpy(advCtx)
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)

	wctx := &flowv1.WorkitemContext{WorkitemId: "wi-arb-hung-2"}

	errCh := make(chan error, 1)
	go func() {
		errCh <- handleAdvocate(context.Background(), client, qm, wctx)
	}()

	waitForEnqueue(t, qm, "wi-arb-hung-2")
	_, _ = qm.Claim(context.Background(), "wi-arb-hung-2")
	if err := qm.Decide(context.Background(), "wi-arb-hung-2", "favour_reviewer"); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	decision := mustGetStoredDecision(t, spy)
	if decision.Choice != "favour_reviewer" {
		t.Fatalf("expected choice=favour_reviewer, got %s", decision.Choice)
	}

	assertRoutedTo(t, spy, outputClerk)
}

// ---------------------------------------------------------------------------
// Tribunal-hung escalation tests
// ---------------------------------------------------------------------------

func TestAdvocate_TribunalHung_Promote(t *testing.T) {
	advCtx := &advocateContext{
		Type:         escalationTribunalHung,
		LawID:        "law-001",
		LawGoal:      "Haiku must have kigo",
		LawAppliesTo: []string{"haiku"},
		LawTier:      int32(flowv1.LawTier_LAW_TIER_RULING),
		Choices:      []string{"promote", "retire", "demote"},
	}

	spy := newAdvocateSpy(advCtx)
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)

	wctx := &flowv1.WorkitemContext{WorkitemId: "wi-trib-hung-1"}

	errCh := make(chan error, 1)
	go func() {
		errCh <- handleAdvocate(context.Background(), client, qm, wctx)
	}()

	waitForEnqueue(t, qm, "wi-trib-hung-1")
	_, _ = qm.Claim(context.Background(), "wi-trib-hung-1")
	if err := qm.Decide(context.Background(), "wi-trib-hung-1", "promote"); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Should store a human-decision artefact with tribunal-hung metadata.
	decision := mustGetStoredDecision(t, spy)
	if decision.EscalationType != escalationTribunalHung {
		t.Fatalf("expected escalation_type=tribunal-hung, got %s", decision.EscalationType)
	}
	if decision.Choice != "promote" {
		t.Fatalf("expected choice=promote, got %s", decision.Choice)
	}
	if decision.LawID != "law-001" {
		t.Fatalf("expected law_id=law-001, got %s", decision.LawID)
	}
	if decision.LawTier != int32(flowv1.LawTier_LAW_TIER_RULING) {
		t.Fatalf("expected law_tier=2, got %d", decision.LawTier)
	}

	// Should route to clerk.
	assertRoutedTo(t, spy, outputClerk)

	// Should NOT complete.
	if spy.Completed {
		t.Fatal("expected no Complete() for tribunal-hung")
	}
}

func TestAdvocate_TribunalHung_RetireAndDemote(t *testing.T) {
	tests := []struct {
		name   string
		choice string
	}{
		{"retire", "retire"},
		{"demote", "demote"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			advCtx := &advocateContext{
				Type:         escalationTribunalHung,
				LawID:        "law-" + tt.name,
				LawGoal:      "Test " + tt.name,
				LawAppliesTo: []string{"haiku"},
				LawTier:      int32(flowv1.LawTier_LAW_TIER_RULING),
				Choices:      []string{"promote", "retire", "demote"},
			}

			spy := newAdvocateSpy(advCtx)
			client := newSpyClient(t, spy)
			qm := newTestQueueManager(t)

			wid := "wi-trib-hung-" + tt.name
			wctx := &flowv1.WorkitemContext{WorkitemId: wid}

			errCh := make(chan error, 1)
			go func() {
				errCh <- handleAdvocate(context.Background(), client, qm, wctx)
			}()

			waitForEnqueue(t, qm, wid)
			_, _ = qm.Claim(context.Background(), wid)
			if err := qm.Decide(context.Background(), wid, tt.choice); err != nil {
				t.Fatalf("Decide failed: %v", err)
			}

			if err := <-errCh; err != nil {
				t.Fatalf("handler returned error: %v", err)
			}

			spy.mu.Lock()
			defer spy.mu.Unlock()

			// Should store human-decision with the correct choice.
			decision := mustGetStoredDecision(t, spy)
			if decision.Choice != tt.choice {
				t.Fatalf("expected choice=%s, got %s", tt.choice, decision.Choice)
			}

			// Should route to clerk.
			assertRoutedTo(t, spy, outputClerk)
		})
	}
}

// ---------------------------------------------------------------------------
// Accept-and-route-to-clerk tests (tribunal-promote + judiciary-ratify)
// ---------------------------------------------------------------------------

func TestAdvocate_AcceptRoutesToClerk(t *testing.T) {
	tests := []struct {
		name           string
		escalation     escalationType
		lawID          string
		lawGoal        string
		lawAppliesTo   []string
		lawTier        int32
		wantWorkitemID string
	}{
		{
			name:           "tribunal-promote",
			escalation:     escalationTribunalPromote,
			lawID:          "law-004",
			lawGoal:        "Well-proven ruling",
			lawAppliesTo:   []string{"haiku"},
			lawTier:        int32(flowv1.LawTier_LAW_TIER_RULING),
			wantWorkitemID: "wi-promote-accept",
		},
		{
			name:           "judiciary-ratify",
			escalation:     escalationJudiciaryRatify,
			lawID:          "law-010",
			lawGoal:        "Codified statute",
			lawAppliesTo:   []string{"haiku"},
			lawTier:        int32(flowv1.LawTier_LAW_TIER_LOCAL_STATUTE),
			wantWorkitemID: "wi-jud-accept",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			advCtx := &advocateContext{
				Type:         tt.escalation,
				LawID:        tt.lawID,
				LawGoal:      tt.lawGoal,
				LawAppliesTo: tt.lawAppliesTo,
				LawTier:      tt.lawTier,
				Choices:      []string{"accept", "reject"},
			}

			spy := newAdvocateSpy(advCtx)
			client := newSpyClient(t, spy)
			qm := newTestQueueManager(t)

			wctx := &flowv1.WorkitemContext{WorkitemId: tt.wantWorkitemID}

			errCh := make(chan error, 1)
			go func() {
				errCh <- handleAdvocate(context.Background(), client, qm, wctx)
			}()

			waitForEnqueue(t, qm, tt.wantWorkitemID)
			_, _ = qm.Claim(context.Background(), tt.wantWorkitemID)
			if err := qm.Decide(context.Background(), tt.wantWorkitemID, "accept"); err != nil {
				t.Fatalf("Decide failed: %v", err)
			}

			if err := <-errCh; err != nil {
				t.Fatalf("handler returned error: %v", err)
			}

			spy.mu.Lock()
			defer spy.mu.Unlock()

			// Should store human-decision with correct escalation type.
			decision := mustGetStoredDecision(t, spy)
			if decision.EscalationType != tt.escalation {
				t.Fatalf("expected escalation_type=%s, got %s", tt.escalation, decision.EscalationType)
			}
			if decision.Choice != "accept" {
				t.Fatalf("expected choice=accept, got %s", decision.Choice)
			}
			if decision.LawID != tt.lawID {
				t.Fatalf("expected law_id=%s, got %s", tt.lawID, decision.LawID)
			}

			// Should route to clerk (not Complete).
			assertRoutedTo(t, spy, outputClerk)
			if spy.Completed {
				t.Fatal("expected no Complete() for accept")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Reject-and-complete tests (tribunal-promote + judiciary-ratify)
// ---------------------------------------------------------------------------

func TestAdvocate_RejectCompletes(t *testing.T) {
	tests := []struct {
		name       string
		escalation escalationType
		lawID      string
		workitemID string
	}{
		{
			name:       "tribunal-promote",
			escalation: escalationTribunalPromote,
			lawID:      "law-005",
			workitemID: "wi-promote-reject",
		},
		{
			name:       "judiciary-ratify",
			escalation: escalationJudiciaryRatify,
			lawID:      "law-011",
			workitemID: "wi-jud-reject",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			advCtx := &advocateContext{
				Type:    tt.escalation,
				LawID:   tt.lawID,
				Choices: []string{"accept", "reject"},
			}

			spy := newAdvocateSpy(advCtx)
			client := newSpyClient(t, spy)
			qm := newTestQueueManager(t)

			wctx := &flowv1.WorkitemContext{WorkitemId: tt.workitemID}

			errCh := make(chan error, 1)
			go func() {
				errCh <- handleAdvocate(context.Background(), client, qm, wctx)
			}()

			waitForEnqueue(t, qm, tt.workitemID)
			_, _ = qm.Claim(context.Background(), tt.workitemID)
			if err := qm.Decide(context.Background(), tt.workitemID, "reject"); err != nil {
				t.Fatalf("Decide failed: %v", err)
			}

			if err := <-errCh; err != nil {
				t.Fatalf("handler returned error: %v", err)
			}

			spy.mu.Lock()
			defer spy.mu.Unlock()

			// Reject: no human-decision artefact, just Complete.
			if spy.getStoredArtefact(humanDecisionArtefact) != nil {
				t.Fatal("expected no human-decision artefact for reject")
			}
			if !spy.Completed {
				t.Fatal("expected Complete()")
			}
			if len(spy.RoutedOutputs) != 0 {
				t.Fatalf("expected no routing for reject, got %v", spy.RoutedOutputs)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Error cases
// ---------------------------------------------------------------------------

func TestAdvocate_InvalidChoice(t *testing.T) {
	advCtx := &advocateContext{
		Type:    escalationTribunalPromote,
		Choices: []string{"accept", "reject"},
	}

	spy := newAdvocateSpy(advCtx)
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)

	wctx := &flowv1.WorkitemContext{WorkitemId: "wi-invalid"}

	errCh := make(chan error, 1)
	go func() {
		errCh <- handleAdvocate(context.Background(), client, qm, wctx)
	}()

	waitForEnqueue(t, qm, "wi-invalid")
	_, _ = qm.Claim(context.Background(), "wi-invalid")
	if err := qm.Decide(context.Background(), "wi-invalid", "promote"); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	err := <-errCh
	if err == nil {
		t.Fatal("expected error for invalid choice")
	}
	if !strings.Contains(err.Error(), "invalid choice") {
		t.Errorf("expected 'invalid choice' in error, got: %v", err)
	}
}

func TestAdvocate_Error_GetArtefactFails(t *testing.T) {
	advCtx := &advocateContext{
		Type:    escalationTribunalPromote,
		Choices: []string{"accept"},
	}

	spy := newAdvocateSpy(advCtx)
	spy.GetArtefactErr = fmt.Errorf("artefact unavailable")
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)

	wctx := &flowv1.WorkitemContext{WorkitemId: "wi-err-art"}

	err := handleAdvocate(context.Background(), client, qm, wctx)
	if err == nil {
		t.Fatal("expected error from GetArtefact failure")
	}
}

func TestAdvocate_Error_StoreArtefactFails(t *testing.T) {
	advCtx := &advocateContext{
		Type:         escalationArbiterHung,
		ArtefactKind: "haiku",
		FeedbackIDs:  []string{"fb-1"},
		Choices:      []string{"favour_refiner"},
	}

	spy := newAdvocateSpy(advCtx)
	spy.StoreArtefactErr = fmt.Errorf("archivist unavailable")
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)

	wctx := &flowv1.WorkitemContext{WorkitemId: "wi-err-store"}

	errCh := make(chan error, 1)
	go func() {
		errCh <- handleAdvocate(context.Background(), client, qm, wctx)
	}()

	waitForEnqueue(t, qm, "wi-err-store")
	_, _ = qm.Claim(context.Background(), "wi-err-store")
	if err := qm.Decide(context.Background(), "wi-err-store", "favour_refiner"); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	err := <-errCh
	if err == nil {
		t.Fatal("expected error from StoreArtefact failure")
	}
	if !strings.Contains(err.Error(), "store human-decision") {
		t.Errorf("expected 'store human-decision' in error, got: %v", err)
	}
}

func TestAdvocate_Error_RouteToOutputFails(t *testing.T) {
	advCtx := &advocateContext{
		Type:         escalationTribunalHung,
		LawID:        "law-err",
		LawGoal:      "Error test",
		LawAppliesTo: []string{"haiku"},
		LawTier:      int32(flowv1.LawTier_LAW_TIER_RULING),
		Choices:      []string{"promote"},
	}

	spy := newAdvocateSpy(advCtx)
	spy.RouteToOutputErr = fmt.Errorf("routing unavailable")
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)

	wctx := &flowv1.WorkitemContext{WorkitemId: "wi-err-route"}

	errCh := make(chan error, 1)
	go func() {
		errCh <- handleAdvocate(context.Background(), client, qm, wctx)
	}()

	waitForEnqueue(t, qm, "wi-err-route")
	_, _ = qm.Claim(context.Background(), "wi-err-route")
	if err := qm.Decide(context.Background(), "wi-err-route", "promote"); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	err := <-errCh
	if err == nil {
		t.Fatal("expected error from RouteToOutput failure")
	}
	if !strings.Contains(err.Error(), "route to clerk") {
		t.Errorf("expected 'route to clerk' in error, got: %v", err)
	}
}

func TestAdvocate_ContextCancellation(t *testing.T) {
	advCtx := &advocateContext{
		Type:    escalationTribunalPromote,
		Choices: []string{"accept"},
	}

	spy := newAdvocateSpy(advCtx)
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)

	ctx, cancel := context.WithCancel(context.Background())
	wctx := &flowv1.WorkitemContext{WorkitemId: "wi-ctx-cancel"}

	errCh := make(chan error, 1)
	go func() {
		errCh <- handleAdvocate(ctx, client, qm, wctx)
	}()

	waitForEnqueue(t, qm, "wi-ctx-cancel")
	cancel()

	err := <-errCh
	if err == nil {
		t.Fatal("expected error on context cancellation")
	}
}

func TestAdvocate_Error_EmptyAdvocateContext(t *testing.T) {
	spy := newAdvocateSpy(&advocateContext{})
	// Override with empty content.
	spy.ArtefactContent = []byte("")
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)

	wctx := &flowv1.WorkitemContext{WorkitemId: "wi-empty-ctx"}

	err := handleAdvocate(context.Background(), client, qm, wctx)
	if err == nil {
		t.Fatal("expected error for empty advocate-context")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("expected 'empty' in error, got: %v", err)
	}
}

func TestAdvocate_Error_NoChoices(t *testing.T) {
	advCtx := &advocateContext{
		Type:    escalationTribunalPromote,
		Choices: []string{},
	}

	spy := newAdvocateSpy(advCtx)
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)

	wctx := &flowv1.WorkitemContext{WorkitemId: "wi-no-choices"}

	err := handleAdvocate(context.Background(), client, qm, wctx)
	if err == nil {
		t.Fatal("expected error for no choices")
	}
	if !strings.Contains(err.Error(), "no choices") {
		t.Errorf("expected 'no choices' in error, got: %v", err)
	}
}

func TestAdvocate_Error_UnknownEscalationType(t *testing.T) {
	advCtx := &advocateContext{
		Type:    "unknown-type",
		Choices: []string{"something"},
	}

	spy := newAdvocateSpy(advCtx)
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)

	wctx := &flowv1.WorkitemContext{WorkitemId: "wi-unknown-type"}

	errCh := make(chan error, 1)
	go func() {
		errCh <- handleAdvocate(context.Background(), client, qm, wctx)
	}()

	waitForEnqueue(t, qm, "wi-unknown-type")
	_, _ = qm.Claim(context.Background(), "wi-unknown-type")
	if err := qm.Decide(context.Background(), "wi-unknown-type", "something"); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	err := <-errCh
	if err == nil {
		t.Fatal("expected error for unknown escalation type")
	}
	if !strings.Contains(err.Error(), "unknown escalation type") {
		t.Errorf("expected 'unknown escalation type' in error, got: %v", err)
	}
}

func TestAdvocate_Error_MissingTypeField(t *testing.T) {
	spy := newAdvocateSpy(&advocateContext{
		Choices: []string{"accept"},
	})
	// The JSON will have type:"" which should be rejected.
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)

	wctx := &flowv1.WorkitemContext{WorkitemId: "wi-missing-type"}

	err := handleAdvocate(context.Background(), client, qm, wctx)
	if err == nil {
		t.Fatal("expected error for missing type field")
	}
	if !strings.Contains(err.Error(), "missing type") {
		t.Errorf("expected 'missing type' in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Human-decision artefact content validation
// ---------------------------------------------------------------------------

func TestAdvocate_HumanDecisionArtefact_ArbiterHung(t *testing.T) {
	advCtx := &advocateContext{
		Type:         escalationArbiterHung,
		ArtefactKind: "sonnet",
		FeedbackIDs:  []string{"fb-a", "fb-b", "fb-c"},
		Choices:      []string{"favour_refiner", "favour_reviewer"},
	}

	spy := newAdvocateSpy(advCtx)
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)

	wctx := &flowv1.WorkitemContext{WorkitemId: "wi-decision-test"}

	errCh := make(chan error, 1)
	go func() {
		errCh <- handleAdvocate(context.Background(), client, qm, wctx)
	}()

	waitForEnqueue(t, qm, "wi-decision-test")
	_, _ = qm.Claim(context.Background(), "wi-decision-test")
	if err := qm.Decide(context.Background(), "wi-decision-test", "favour_refiner"); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	decision := mustGetStoredDecision(t, spy)

	// Validate all fields are propagated correctly.
	if decision.EscalationType != escalationArbiterHung {
		t.Errorf("escalation_type: want arbiter-hung, got %s", decision.EscalationType)
	}
	if decision.Choice != "favour_refiner" {
		t.Errorf("choice: want favour_refiner, got %s", decision.Choice)
	}
	if decision.ArtefactKind != "sonnet" {
		t.Errorf("artefact_kind: want sonnet, got %s", decision.ArtefactKind)
	}
	if len(decision.FeedbackIDs) != 3 {
		t.Errorf("feedback_ids: want 3, got %d", len(decision.FeedbackIDs))
	}
}

func TestAdvocate_HumanDecisionArtefact_TribunalHung(t *testing.T) {
	advCtx := &advocateContext{
		Type:         escalationTribunalHung,
		LawID:        "law-100",
		LawGoal:      "Must have seasonal reference",
		LawAppliesTo: []string{"haiku", "sonnet"},
		LawTier:      int32(flowv1.LawTier_LAW_TIER_LOCAL_STATUTE),
		Choices:      []string{"promote", "retire", "demote"},
	}

	spy := newAdvocateSpy(advCtx)
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)

	wctx := &flowv1.WorkitemContext{WorkitemId: "wi-decision-trib"}

	errCh := make(chan error, 1)
	go func() {
		errCh <- handleAdvocate(context.Background(), client, qm, wctx)
	}()

	waitForEnqueue(t, qm, "wi-decision-trib")
	_, _ = qm.Claim(context.Background(), "wi-decision-trib")
	if err := qm.Decide(context.Background(), "wi-decision-trib", "demote"); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	decision := mustGetStoredDecision(t, spy)

	if decision.LawID != "law-100" {
		t.Errorf("law_id: want law-100, got %s", decision.LawID)
	}
	if decision.LawGoal != "Must have seasonal reference" {
		t.Errorf("law_goal: want 'Must have seasonal reference', got %s", decision.LawGoal)
	}
	if len(decision.LawAppliesTo) != 2 {
		t.Errorf("law_applies_to: want 2, got %d", len(decision.LawAppliesTo))
	}
	if decision.LawTier != int32(flowv1.LawTier_LAW_TIER_LOCAL_STATUTE) {
		t.Errorf("law_tier: want 3, got %d", decision.LawTier)
	}
	if decision.Choice != "demote" {
		t.Errorf("choice: want demote, got %s", decision.Choice)
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// mustGetStoredDecision reads and parses the human-decision artefact from the
// spy. Fails the test if not found or invalid.
func mustGetStoredDecision(t *testing.T, spy *advocateSpy) humanDecision {
	t.Helper()
	raw := spy.getStoredArtefact(humanDecisionArtefact)
	if raw == nil {
		t.Fatal("expected human-decision artefact to be stored")
	}
	var decision humanDecision
	if err := json.Unmarshal(raw, &decision); err != nil {
		t.Fatalf("unmarshal human-decision: %v", err)
	}
	return decision
}

// assertRoutedTo checks that the spy recorded exactly one route to the given
// output name. Fails the test otherwise.
func assertRoutedTo(t *testing.T, spy *advocateSpy, output string) { //nolint:unparam // generic helper
	t.Helper()
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != output {
		t.Fatalf("expected route to %s, got %v", output, spy.RoutedOutputs)
	}
}
