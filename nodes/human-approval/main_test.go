// Tests for the human-approval HITL node.
package main

import (
	"context"
	"strings"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

// ---------------------------------------------------------------------------
// Test 1: Approve
// ---------------------------------------------------------------------------

// TestApproval_Approve verifies the happy path: human approves the haiku,
// the "approval" stamp is applied, and the workitem is routed to "approve".
func TestApproval_Approve(t *testing.T) {
	spy := newApprovalSpy()
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)
	ctx := context.Background()
	wctx := newWorkitemContext("wi-approve-1")

	errCh := runHandler(ctx, client, qm, wctx)
	simulateDecision(t, ctx, qm, "wi-approve-1", "approve")

	if err := <-errCh; err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Both haiku and petition artefacts were read (plus a third read for stamping).
	if len(spy.ReadArtefacts) != 3 {
		t.Fatalf("expected 3 GetArtefact calls (haiku + petition + stamp read), got %d: %v",
			len(spy.ReadArtefacts), spy.ReadArtefacts)
	}
	if spy.ReadArtefacts[0] != "haiku" {
		t.Errorf("expected first read=haiku, got %s", spy.ReadArtefacts[0])
	}
	if spy.ReadArtefacts[1] != "petition" {
		t.Errorf("expected second read=petition, got %s", spy.ReadArtefacts[1])
	}

	// Timer was paused and resumed.
	if spy.PauseTimerCalls != 1 {
		t.Errorf("expected 1 PauseTimer call, got %d", spy.PauseTimerCalls)
	}
	if spy.ResumeTimerCalls != 1 {
		t.Errorf("expected 1 ResumeTimer call, got %d", spy.ResumeTimerCalls)
	}

	// Exactly one stamp: haiku artefact with "approval".
	if len(spy.StampedArtefacts) != 1 {
		t.Fatalf("expected 1 stamp, got %d: %v",
			len(spy.StampedArtefacts), spy.StampedArtefacts)
	}
	if spy.StampedArtefacts[0].ArtefactID != "haiku" {
		t.Errorf("expected stamp on haiku, got %s",
			spy.StampedArtefacts[0].ArtefactID)
	}
	if spy.StampedArtefacts[0].StampName != "approval" {
		t.Errorf("expected stamp name=approval, got %s",
			spy.StampedArtefacts[0].StampName)
	}

	// Exactly one route to "approve".
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "approve" {
		t.Errorf("expected route to 'approve', got %v", spy.RoutedOutputs)
	}

	// No completions (not cancelled).
	if len(spy.CompletedReasons) != 0 {
		t.Errorf("expected no completions, got %v", spy.CompletedReasons)
	}
}

// ---------------------------------------------------------------------------
// Test 2: Cancel
// ---------------------------------------------------------------------------

// TestApproval_Cancel verifies the cancel path: human rejects the petition,
// the workitem is completed with COMPLETION_REASON_CANCELLED.
func TestApproval_Cancel(t *testing.T) {
	spy := newApprovalSpy()
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)
	ctx := context.Background()
	wctx := newWorkitemContext("wi-cancel-1")

	errCh := runHandler(ctx, client, qm, wctx)
	simulateDecision(t, ctx, qm, "wi-cancel-1", "cancel")

	if err := <-errCh; err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Zero stamps applied.
	if len(spy.StampedArtefacts) != 0 {
		t.Errorf("expected no stamps, got %v", spy.StampedArtefacts)
	}

	// Zero routes.
	if len(spy.RoutedOutputs) != 0 {
		t.Errorf("expected no routes, got %v", spy.RoutedOutputs)
	}

	// Exactly one completion with CANCELLED reason.
	if len(spy.CompletedReasons) != 1 {
		t.Fatalf("expected 1 completion, got %d", len(spy.CompletedReasons))
	}
	if spy.CompletedReasons[0] != flowv1.CompletionReason_COMPLETION_REASON_CANCELLED {
		t.Errorf("expected COMPLETION_REASON_CANCELLED, got %v",
			spy.CompletedReasons[0])
	}

	// Timer was paused and resumed.
	if spy.PauseTimerCalls != 1 {
		t.Errorf("expected 1 PauseTimer, got %d", spy.PauseTimerCalls)
	}
	if spy.ResumeTimerCalls != 1 {
		t.Errorf("expected 1 ResumeTimer, got %d", spy.ResumeTimerCalls)
	}
}

// ---------------------------------------------------------------------------
// Test 3: Invalid choice
// ---------------------------------------------------------------------------

// TestApproval_InvalidChoice verifies that an unrecognised choice value
// returns an error, does not stamp, route, or complete, and does NOT
// call ResumeTimer.
func TestApproval_InvalidChoice(t *testing.T) {
	spy := newApprovalSpy()
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)
	ctx := context.Background()
	wctx := newWorkitemContext("wi-invalid-1")

	errCh := runHandler(ctx, client, qm, wctx)
	simulateDecision(t, ctx, qm, "wi-invalid-1", "bogus")

	err := <-errCh
	if err == nil {
		t.Fatal("expected error for invalid choice, got nil")
	}
	if !strings.Contains(err.Error(), "invalid choice") {
		t.Errorf("expected 'invalid choice' in error, got: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Nothing dispatched.
	if len(spy.StampedArtefacts) != 0 {
		t.Errorf("expected no stamps, got %v", spy.StampedArtefacts)
	}
	if len(spy.RoutedOutputs) != 0 {
		t.Errorf("expected no routes, got %v", spy.RoutedOutputs)
	}
	if len(spy.CompletedReasons) != 0 {
		t.Errorf("expected no completions, got %v", spy.CompletedReasons)
	}

	// ResumeTimer should NOT be called (error before dispatch).
	if spy.ResumeTimerCalls != 0 {
		t.Errorf("expected 0 ResumeTimer calls, got %d", spy.ResumeTimerCalls)
	}
}
