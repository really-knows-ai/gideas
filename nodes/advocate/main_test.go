package main

import (
	"context"
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

	// Should have drafted a Tier 2 Ruling.
	if len(spy.DraftLawCalls) != 1 {
		t.Fatalf("expected 1 DraftLaw call, got %d", len(spy.DraftLawCalls))
	}
	dl := spy.DraftLawCalls[0]
	if dl.Tier != int32(flowv1.LawTier_LAW_TIER_RULING) {
		t.Fatalf("expected Tier 2 (RULING), got %d", dl.Tier)
	}
	if dl.Outcome != "favour_refiner" {
		t.Fatalf("expected outcome favour_refiner, got %s", dl.Outcome)
	}

	// Should have linked ruling to both feedback items.
	if len(spy.LinkedRulings) != 2 {
		t.Fatalf("expected 2 LinkRuling calls, got %d", len(spy.LinkedRulings))
	}
	ids := map[string]bool{}
	for _, lr := range spy.LinkedRulings {
		ids[lr.FeedbackID] = true
		if lr.LawID != "law-advocate-001" {
			t.Fatalf("expected law_id=law-advocate-001, got %s", lr.LawID)
		}
	}
	if !ids["fb-1"] || !ids["fb-2"] {
		t.Fatalf("expected LinkRuling for fb-1 and fb-2")
	}

	// Should route to sort.
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "sort" {
		t.Fatalf("expected route to sort, got %v", spy.RoutedOutputs)
	}

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

	if spy.DraftLawCalls[0].Outcome != "favour_reviewer" {
		t.Fatalf("expected outcome favour_reviewer, got %s", spy.DraftLawCalls[0].Outcome)
	}
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "sort" {
		t.Fatalf("expected route to sort, got %v", spy.RoutedOutputs)
	}
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

	if len(spy.DraftLawCalls) != 1 {
		t.Fatalf("expected 1 DraftLaw call, got %d", len(spy.DraftLawCalls))
	}
	dl := spy.DraftLawCalls[0]
	if dl.Tier != int32(flowv1.LawTier_LAW_TIER_LOCAL_STATUTE) {
		t.Fatalf("expected Tier 3 (LOCAL_STATUTE), got %d", dl.Tier)
	}
	if !spy.Completed {
		t.Fatal("expected Complete()")
	}
	if len(spy.RoutedOutputs) != 0 {
		t.Fatalf("expected no routing, got %v", spy.RoutedOutputs)
	}
}

func TestAdvocate_TribunalHung_RetireAndDemote(t *testing.T) {
	tests := []struct {
		name     string
		choice   string
		wantTier int32
	}{
		{"retire", "retire", int32(flowv1.LawTier_LAW_TIER_RULING)},
		{"demote", "demote", int32(flowv1.LawTier_LAW_TIER_FINDING)},
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

			dl := spy.DraftLawCalls[0]
			if dl.Tier != tt.wantTier {
				t.Fatalf("expected tier %d for %s, got %d", tt.wantTier, tt.choice, dl.Tier)
			}
			if !spy.Completed {
				t.Fatal("expected Complete()")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tribunal-promote (Tier 3 ratification) tests
// ---------------------------------------------------------------------------

func TestAdvocate_TribunalPromote_Accept(t *testing.T) {
	advCtx := &advocateContext{
		Type:         escalationTribunalPromote,
		LawID:        "law-004",
		LawGoal:      "Well-proven ruling",
		LawAppliesTo: []string{"haiku"},
		Choices:      []string{"accept", "reject"},
	}

	spy := newAdvocateSpy(advCtx)
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)

	wctx := &flowv1.WorkitemContext{WorkitemId: "wi-promote-1"}

	errCh := make(chan error, 1)
	go func() {
		errCh <- handleAdvocate(context.Background(), client, qm, wctx)
	}()

	waitForEnqueue(t, qm, "wi-promote-1")
	_, _ = qm.Claim(context.Background(), "wi-promote-1")
	if err := qm.Decide(context.Background(), "wi-promote-1", "accept"); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Should have drafted a Tier 3 Local Statute.
	if len(spy.DraftLawCalls) != 1 {
		t.Fatalf("expected 1 DraftLaw call, got %d", len(spy.DraftLawCalls))
	}
	dl := spy.DraftLawCalls[0]
	if dl.Tier != int32(flowv1.LawTier_LAW_TIER_LOCAL_STATUTE) {
		t.Fatalf("expected Tier 3 (LOCAL_STATUTE), got %d", dl.Tier)
	}
	if !spy.Completed {
		t.Fatal("expected Complete()")
	}
}

func TestAdvocate_TribunalPromote_Reject(t *testing.T) {
	advCtx := &advocateContext{
		Type:         escalationTribunalPromote,
		LawID:        "law-005",
		LawGoal:      "Controversial ruling",
		LawAppliesTo: []string{"haiku"},
		Choices:      []string{"accept", "reject"},
	}

	spy := newAdvocateSpy(advCtx)
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)

	wctx := &flowv1.WorkitemContext{WorkitemId: "wi-promote-2"}

	errCh := make(chan error, 1)
	go func() {
		errCh <- handleAdvocate(context.Background(), client, qm, wctx)
	}()

	waitForEnqueue(t, qm, "wi-promote-2")
	_, _ = qm.Claim(context.Background(), "wi-promote-2")
	if err := qm.Decide(context.Background(), "wi-promote-2", "reject"); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Reject: no DraftLaw, just Complete.
	if len(spy.DraftLawCalls) != 0 {
		t.Fatalf("expected 0 DraftLaw calls for reject, got %d", len(spy.DraftLawCalls))
	}
	if !spy.Completed {
		t.Fatal("expected Complete()")
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

func TestAdvocate_Error_ServiceCallFails(t *testing.T) {
	tests := []struct {
		name    string
		advCtx  *advocateContext
		setupFn func(spy *advocateSpy)
		choice  string
	}{
		{
			name: "DraftLawFails",
			advCtx: &advocateContext{
				Type: escalationTribunalPromote, LawGoal: "Test",
				LawAppliesTo: []string{"haiku"}, Choices: []string{"accept"},
			},
			setupFn: func(spy *advocateSpy) { spy.DraftLawErr = fmt.Errorf("clerk unavailable") },
			choice:  "accept",
		},
		{
			name: "LinkRulingFails",
			advCtx: &advocateContext{
				Type: escalationArbiterHung, ArtefactKind: "haiku",
				FeedbackIDs: []string{"fb-err"}, Choices: []string{"favour_refiner"},
			},
			setupFn: func(spy *advocateSpy) { spy.LinkRulingErr = fmt.Errorf("link ruling failed") },
			choice:  "favour_refiner",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := newAdvocateSpy(tt.advCtx)
			tt.setupFn(spy)
			client := newSpyClient(t, spy)
			qm := newTestQueueManager(t)

			wid := "wi-err-" + tt.name
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

			err := <-errCh
			if err == nil {
				t.Fatalf("expected error for %s", tt.name)
			}
		})
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

// ---------------------------------------------------------------------------
// tierForTribunalChoice unit tests
// ---------------------------------------------------------------------------

func TestTierForTribunalChoice(t *testing.T) {
	tests := []struct {
		choice       string
		originalTier int32
		want         int32
	}{
		{"promote", int32(flowv1.LawTier_LAW_TIER_RULING), int32(flowv1.LawTier_LAW_TIER_LOCAL_STATUTE)},
		{"promote", int32(flowv1.LawTier_LAW_TIER_FINDING), int32(flowv1.LawTier_LAW_TIER_RULING)},
		{"demote", int32(flowv1.LawTier_LAW_TIER_RULING), int32(flowv1.LawTier_LAW_TIER_FINDING)},
		{"retire", int32(flowv1.LawTier_LAW_TIER_RULING), int32(flowv1.LawTier_LAW_TIER_RULING)},
		{"retire", int32(flowv1.LawTier_LAW_TIER_FINDING), int32(flowv1.LawTier_LAW_TIER_FINDING)},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_tier%d", tt.choice, tt.originalTier), func(t *testing.T) {
			got := tierForTribunalChoice(tt.choice, tt.originalTier)
			if got != tt.want {
				t.Fatalf("tierForTribunalChoice(%q, %d) = %d, want %d", tt.choice, tt.originalTier, got, tt.want)
			}
		})
	}
}
