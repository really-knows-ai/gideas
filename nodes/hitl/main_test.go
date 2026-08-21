package main

import (
	"context"
	"strings"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// Test constants for repeated string literals (goconst).
const (
	artefactPetition       = "petition"
	artefactEvidenceBundle = "evidence-bundle"
	outputApproved         = "approved"
	outputResolution       = "resolution"
)

// ---------------------------------------------------------------------------
// Happy path: hitl-appraise CRD instance
// ---------------------------------------------------------------------------

// TestHITL_HitlAppraise_Approved exercises the hitl-appraise CRD pattern:
// read petition artefact, stamp petition/reviewed, route to "approved".
func TestHITL_HitlAppraise_Approved(t *testing.T) {
	spy := newHITLAppraiseSpy()
	client := newSpyClient(t, spy)
	h := newTestQueueManager(t)
	cfg := configWithLabels(map[string]string{"approved": "Approve Petition"})
	ctx := context.Background()
	wctx := newWorkitemContext("wi-appraise-1")

	errCh := runHandler(ctx, client, h.qm, cfg, wctx)
	h.decide(t, ctx, "wi-appraise-1", outputApproved)

	if err := <-errCh; err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Topology was queried (once via workitem.GetTopology and once via raw fallback).
	// ponytail: two calls because the new Flow type does not expose GetSelf().GetOutputs().
	if spy.TopologyCalls != 2 {
		t.Errorf("expected 2 GetFlowTopology calls, got %d", spy.TopologyCalls)
	}

	// Petition artefact was read (from READ:artefact/petition).
	// An additional GetArtefact call occurs in stampAndRoute for the stamp target.
	if len(spy.ReadArtefacts) != 2 {
		t.Fatalf("expected 2 GetArtefact calls, got %d", len(spy.ReadArtefacts))
	}
	if spy.ReadArtefacts[0] != artefactPetition {
		t.Errorf("expected read=petition, got %s", spy.ReadArtefacts[0])
	}

	// Timer was paused and resumed.
	if spy.PauseTimerCalls != 1 {
		t.Errorf("expected 1 PauseTimer call, got %d", spy.PauseTimerCalls)
	}
	if spy.ResumeTimerCalls != 1 {
		t.Errorf("expected 1 ResumeTimer call, got %d", spy.ResumeTimerCalls)
	}

	// Petition was stamped with "reviewed".
	if len(spy.StampedArtefacts) != 1 {
		t.Fatalf("expected 1 stamp, got %d", len(spy.StampedArtefacts))
	}
	if spy.StampedArtefacts[0].ArtefactID != artefactPetition {
		t.Errorf("expected stamp on petition, got %s", spy.StampedArtefacts[0].ArtefactID)
	}
	if spy.StampedArtefacts[0].StampName != "reviewed" {
		t.Errorf("expected stamp name=reviewed, got %s", spy.StampedArtefacts[0].StampName)
	}

	// Routed to "approved".
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputApproved {
		t.Errorf("expected route to 'approved', got %v", spy.RoutedOutputs)
	}

	// No completions (not cancelled).
	if len(spy.CompletedReasons) != 0 {
		t.Errorf("expected no completions, got %v", spy.CompletedReasons)
	}
}

// ---------------------------------------------------------------------------
// Happy path: arbiter-hitl-resolve CRD instance
// ---------------------------------------------------------------------------

// TestHITL_ArbiterResolve_Resolution exercises the arbiter-hitl-resolve CRD
// pattern: read evidence-bundle, no stamp, route to "resolution".
func TestHITL_ArbiterResolve_Resolution(t *testing.T) {
	spy := newArbiterHITLResolveSpy()
	client := newSpyClient(t, spy)
	h := newTestQueueManager(t)
	cfg := defaultConfig()
	ctx := context.Background()
	wctx := newWorkitemContext("wi-arbiter-1")

	errCh := runHandler(ctx, client, h.qm, cfg, wctx)
	h.decide(t, ctx, "wi-arbiter-1", outputResolution)

	if err := <-errCh; err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Evidence bundle was read.
	if len(spy.ReadArtefacts) != 1 || spy.ReadArtefacts[0] != artefactEvidenceBundle {
		t.Errorf("expected read=['evidence-bundle'], got %v", spy.ReadArtefacts)
	}

	// No stamps (no STAMP capability).
	if len(spy.StampedArtefacts) != 0 {
		t.Errorf("expected no stamps, got %v", spy.StampedArtefacts)
	}

	// Routed to "resolution".
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputResolution {
		t.Errorf("expected route to 'resolution', got %v", spy.RoutedOutputs)
	}

	// Timer lifecycle.
	if spy.PauseTimerCalls != 1 {
		t.Errorf("expected 1 PauseTimer, got %d", spy.PauseTimerCalls)
	}
	if spy.ResumeTimerCalls != 1 {
		t.Errorf("expected 1 ResumeTimer, got %d", spy.ResumeTimerCalls)
	}
}

// ---------------------------------------------------------------------------
// Happy path: tribunal-hitl-resolve CRD instance
// ---------------------------------------------------------------------------

// TestHITL_TribunalResolve_Resolution exercises the tribunal-hitl-resolve CRD
// pattern: read evidence-bundle, no stamp, route to "resolution".
func TestHITL_TribunalResolve_Resolution(t *testing.T) {
	spy := newTribunalHITLResolveSpy()
	client := newSpyClient(t, spy)
	h := newTestQueueManager(t)
	cfg := defaultConfig()
	ctx := context.Background()
	wctx := newWorkitemContext("wi-tribunal-1")

	errCh := runHandler(ctx, client, h.qm, cfg, wctx)
	h.decide(t, ctx, "wi-tribunal-1", outputResolution)

	if err := <-errCh; err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Evidence bundle was read.
	if len(spy.ReadArtefacts) != 1 || spy.ReadArtefacts[0] != artefactEvidenceBundle {
		t.Errorf("expected read=['evidence-bundle'], got %v", spy.ReadArtefacts)
	}

	// No stamps.
	if len(spy.StampedArtefacts) != 0 {
		t.Errorf("expected no stamps, got %v", spy.StampedArtefacts)
	}

	// Routed to "resolution".
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != outputResolution {
		t.Errorf("expected route to 'resolution', got %v", spy.RoutedOutputs)
	}
}

// ---------------------------------------------------------------------------
// Happy path: minimal (no stamp, no feedback, no exit)
// ---------------------------------------------------------------------------

// TestHITL_Minimal_Route exercises the simplest HITL: one output, no stamps,
// no feedback, no exit contract.
func TestHITL_Minimal_Route(t *testing.T) {
	spy := newMinimalSpy()
	client := newSpyClient(t, spy)
	h := newTestQueueManager(t)
	cfg := defaultConfig()
	ctx := context.Background()
	wctx := newWorkitemContext("wi-minimal-1")

	errCh := runHandler(ctx, client, h.qm, cfg, wctx)
	h.decide(t, ctx, "wi-minimal-1", "default")

	if err := <-errCh; err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// No artefacts read (no READ:artefact/<kind>).
	if len(spy.ReadArtefacts) != 0 {
		t.Errorf("expected no artefact reads, got %v", spy.ReadArtefacts)
	}

	// No stamps.
	if len(spy.StampedArtefacts) != 0 {
		t.Errorf("expected no stamps, got %v", spy.StampedArtefacts)
	}

	// Routed to "default".
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "default" {
		t.Errorf("expected route to 'default', got %v", spy.RoutedOutputs)
	}

	// No completions.
	if len(spy.CompletedReasons) != 0 {
		t.Errorf("expected no completions, got %v", spy.CompletedReasons)
	}
}

// ---------------------------------------------------------------------------
// deriveBehaviour — pure function tests
// ---------------------------------------------------------------------------

func TestDeriveBehaviour_HITLAppraise(t *testing.T) {
	spy := newHITLAppraiseSpy()
	flowObj := newFlowFromTopology(t, spy)
	b, err := deriveBehaviour(flowObj, spy.Topology, defaultConfig())
	if err != nil {
		t.Fatalf("deriveBehaviour returned error: %v", err)
	}

	if len(b.readArtefacts) != 1 || b.readArtefacts[0] != artefactPetition {
		t.Errorf("readArtefacts=%v, want [petition]", b.readArtefacts)
	}
	if len(b.stamps) != 1 {
		t.Fatalf("stamps=%d, want 1", len(b.stamps))
	}
	if b.stamps[0].GovernedArtefact != artefactPetition || b.stamps[0].StampName != "reviewed" {
		t.Errorf("stamp=%v, want petition/reviewed", b.stamps[0])
	}
	if !b.hasFeedback {
		t.Error("expected hasFeedback=true")
	}
	if !b.hasCancel {
		t.Error("expected hasCancel=true (exit-bound)")
	}
	if len(b.outputChoices) != 1 || b.outputChoices[0] != outputApproved {
		t.Errorf("outputChoices=%v, want [approved]", b.outputChoices)
	}
	// Valid choices: "approved" + "cancel".
	if !b.validChoices[outputApproved] {
		t.Error("expected 'approved' in validChoices")
	}
	if !b.validChoices["cancel"] {
		t.Error("expected 'cancel' in validChoices")
	}
	if b.validChoices["unknown"] {
		t.Error("'unknown' should not be in validChoices")
	}
}

func TestDeriveBehaviour_ArbiterResolve(t *testing.T) {
	spy := newArbiterHITLResolveSpy()
	flowObj := newFlowFromTopology(t, spy)
	b, err := deriveBehaviour(flowObj, spy.Topology, defaultConfig())
	if err != nil {
		t.Fatalf("deriveBehaviour returned error: %v", err)
	}

	if len(b.readArtefacts) != 1 || b.readArtefacts[0] != artefactEvidenceBundle {
		t.Errorf("readArtefacts=%v, want [evidence-bundle]", b.readArtefacts)
	}
	if len(b.stamps) != 0 {
		t.Errorf("stamps=%d, want 0", len(b.stamps))
	}
	if b.hasFeedback {
		t.Error("expected hasFeedback=false")
	}
	if !b.hasCancel {
		t.Error("expected hasCancel=true (exit-bound)")
	}
	if len(b.outputChoices) != 1 || b.outputChoices[0] != outputResolution {
		t.Errorf("outputChoices=%v, want [resolution]", b.outputChoices)
	}
}

func TestDeriveBehaviour_Minimal(t *testing.T) {
	spy := newMinimalSpy()
	flowObj := newFlowFromTopology(t, spy)
	b, err := deriveBehaviour(flowObj, spy.Topology, defaultConfig())
	if err != nil {
		t.Fatalf("deriveBehaviour returned error: %v", err)
	}

	if len(b.readArtefacts) != 0 {
		t.Errorf("readArtefacts=%v, want []", b.readArtefacts)
	}
	if len(b.stamps) != 0 {
		t.Errorf("stamps=%d, want 0", len(b.stamps))
	}
	if b.hasFeedback {
		t.Error("expected hasFeedback=false")
	}
	if b.hasCancel {
		t.Error("expected hasCancel=false (no exit contract)")
	}
	// Valid choices: just "default" (no cancel).
	if !b.validChoices["default"] {
		t.Error("expected 'default' in validChoices")
	}
	if b.validChoices["cancel"] {
		t.Error("'cancel' should not be in validChoices (no exit)")
	}
}

// ---------------------------------------------------------------------------
// Choices restriction (config restricts the presented routing choices)
// ---------------------------------------------------------------------------

// TestHITL_ChoicesRestriction_SubsetRoutes verifies that when hitl config
// restricts choices to a subset of topology outputs, deciding a listed output
// routes, and deciding an unlisted output fails with the invalid-choice path.
func TestHITL_ChoicesRestriction_SubsetRoutes(t *testing.T) {
	spy := newThreeOutputSpy()
	client := newSpyClient(t, spy)
	h := newTestQueueManager(t)
	cfg := configWithChoices(choiceEntry{Output: "b", Label: "Bravo"})
	ctx := context.Background()
	wctx := newWorkitemContext("wi-restrict-1")

	errCh := runHandler(ctx, client, h.qm, cfg, wctx)
	h.decide(t, ctx, "wi-restrict-1", "b")

	if err := <-errCh; err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Routed to "b".
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "b" {
		t.Errorf("expected route to 'b', got %v", spy.RoutedOutputs)
	}
}

// TestHITL_ChoicesRestriction_UnlistedChoiceFails verifies that deciding an
// output not in the restricted choices set is rejected by the handler.
func TestHITL_ChoicesRestriction_UnlistedChoiceFails(t *testing.T) {
	spy := newThreeOutputSpy()
	client := newSpyClient(t, spy)
	h := newTestQueueManager(t)
	cfg := configWithChoices(choiceEntry{Output: "b"})
	ctx := context.Background()
	wctx := newWorkitemContext("wi-restrict-2")

	errCh := runHandler(ctx, client, h.qm, cfg, wctx)
	h.decide(t, ctx, "wi-restrict-2", "a")

	err := <-errCh
	if err == nil {
		t.Fatal("expected error for unlisted choice, got nil")
	}
	if !strings.Contains(err.Error(), "invalid choice") {
		t.Errorf("expected 'invalid choice' in error, got: %v", err)
	}
}

// TestHITL_ChoicesRestriction_ConfiguredOutputNotInTopology verifies that a
// configured choices output absent from the topology yields a handler error
// before enqueue (no queue item parked).
func TestHITL_ChoicesRestriction_ConfiguredOutputNotInTopology(t *testing.T) {
	spy := newThreeOutputSpy()
	client := newSpyClient(t, spy)
	h := newTestQueueManager(t)
	cfg := configWithChoices(choiceEntry{Output: "b"}, choiceEntry{Output: "nope"})
	ctx := context.Background()
	wctx := newWorkitemContext("wi-restrict-3")

	errCh := runHandler(ctx, client, h.qm, cfg, wctx)

	err := <-errCh
	if err == nil {
		t.Fatal("expected error for out-of-topology choice, got nil")
	}
	if !strings.Contains(err.Error(), "not a valid output in topology") {
		t.Errorf("expected 'not a valid output in topology' in error, got: %v", err)
	}

	// No queue item was parked (error happened before enqueue).
	spy.mu.Lock()
	defer spy.mu.Unlock()
	if spy.PauseTimerCalls != 0 {
		t.Errorf("expected 0 PauseTimer calls (no enqueue before error), got %d", spy.PauseTimerCalls)
	}
}

// TestHITL_ChoicesRestriction_AbsentPresentsAll verifies that empty/absent
// choices preserves the current behavior of presenting all topology outputs.
func TestHITL_ChoicesRestriction_AbsentPresentsAll(t *testing.T) {
	spy := newThreeOutputSpy()
	flowObj := newFlowFromTopology(t, spy)
	b, err := deriveBehaviour(flowObj, spy.Topology, defaultConfig())
	if err != nil {
		t.Fatalf("deriveBehaviour returned error: %v", err)
	}
	if len(b.outputChoices) != 3 {
		t.Fatalf("outputChoices=%v, want 3 outputs", b.outputChoices)
	}
	for _, o := range []string{"a", "b", "c"} {
		if !b.validChoices[o] {
			t.Errorf("expected %q in validChoices", o)
		}
	}
}

// ---------------------------------------------------------------------------
// Approval as a hitl:latest instance (purely via config: output approve →
// sort, STAMP:artefact/haiku/approval, exit-bound standard-exit)
// ---------------------------------------------------------------------------

// TestHITL_ApprovalAsInstance_Approve verifies deciding "approve" stamps the
// haiku with "approval" and routes to "approve", without completing.
func TestHITL_ApprovalAsInstance_Approve(t *testing.T) {
	spy := newApprovalSpy()
	client := newSpyClient(t, spy)
	h := newTestQueueManager(t)
	cfg := configWithLabels(map[string]string{"approve": "Approve Petition"})
	ctx := context.Background()
	wctx := newWorkitemContext("wi-approval-1")

	errCh := runHandler(ctx, client, h.qm, cfg, wctx)
	h.decide(t, ctx, "wi-approval-1", "approve")

	if err := <-errCh; err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Stamped haiku/approval.
	if len(spy.StampedArtefacts) != 1 {
		t.Fatalf("expected 1 stamp, got %d", len(spy.StampedArtefacts))
	}
	if spy.StampedArtefacts[0].ArtefactID != "haiku" || spy.StampedArtefacts[0].StampName != "approval" {
		t.Errorf("expected stamp haiku/approval, got %+v", spy.StampedArtefacts[0])
	}

	// Routed to "approve".
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "approve" {
		t.Errorf("expected route to 'approve', got %v", spy.RoutedOutputs)
	}

	// No completions.
	if len(spy.CompletedReasons) != 0 {
		t.Errorf("expected no completions, got %v", spy.CompletedReasons)
	}
}

// TestHITL_ApprovalAsInstance_Cancel verifies deciding "cancel" completes with
// CANCELLED and applies no stamp.
func TestHITL_ApprovalAsInstance_Cancel(t *testing.T) {
	spy := newApprovalSpy()
	client := newSpyClient(t, spy)
	h := newTestQueueManager(t)
	cfg := configWithLabels(map[string]string{"approve": "Approve Petition"})
	ctx := context.Background()
	wctx := newWorkitemContext("wi-approval-2")

	errCh := runHandler(ctx, client, h.qm, cfg, wctx)
	h.decide(t, ctx, "wi-approval-2", "cancel")

	if err := <-errCh; err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// No stamps on cancel.
	if len(spy.StampedArtefacts) != 0 {
		t.Errorf("expected no stamps on cancel, got %v", spy.StampedArtefacts)
	}

	// No routes on cancel.
	if len(spy.RoutedOutputs) != 0 {
		t.Errorf("expected no routes on cancel, got %v", spy.RoutedOutputs)
	}

	// Completed with CANCELLED.
	if len(spy.CompletedReasons) != 1 {
		t.Fatalf("expected 1 completion, got %d", len(spy.CompletedReasons))
	}
	if spy.CompletedReasons[0] != flowv1.CompletionReason_COMPLETION_REASON_CANCELLED {
		t.Errorf("expected COMPLETION_REASON_CANCELLED, got %v", spy.CompletedReasons[0])
	}
}

// ---------------------------------------------------------------------------
// Edge case: cancel choice (exit-bound node)
// ---------------------------------------------------------------------------

// TestHITL_HitlAppraise_Cancel exercises the cancel path: the human chooses
// "cancel" on an exit-bound node. Expects Complete(CANCELLED), no stamps, no route.
func TestHITL_HitlAppraise_Cancel(t *testing.T) {
	spy := newHITLAppraiseSpy()
	client := newSpyClient(t, spy)
	h := newTestQueueManager(t)
	cfg := configWithLabels(map[string]string{"approved": "Approve Petition"})
	ctx := context.Background()
	wctx := newWorkitemContext("wi-cancel-1")

	errCh := runHandler(ctx, client, h.qm, cfg, wctx)
	h.decide(t, ctx, "wi-cancel-1", "cancel")

	if err := <-errCh; err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// No stamps applied (cancel skips stamping).
	if len(spy.StampedArtefacts) != 0 {
		t.Errorf("expected no stamps on cancel, got %v", spy.StampedArtefacts)
	}

	// No routes (cancel does not route).
	if len(spy.RoutedOutputs) != 0 {
		t.Errorf("expected no routes on cancel, got %v", spy.RoutedOutputs)
	}

	// Completed with CANCELLED.
	if len(spy.CompletedReasons) != 1 {
		t.Fatalf("expected 1 completion, got %d", len(spy.CompletedReasons))
	}
	if spy.CompletedReasons[0] != flowv1.CompletionReason_COMPLETION_REASON_CANCELLED {
		t.Errorf("expected COMPLETION_REASON_CANCELLED, got %v", spy.CompletedReasons[0])
	}

	// Timer lifecycle still holds: paused then resumed.
	if spy.PauseTimerCalls != 1 {
		t.Errorf("expected 1 PauseTimer, got %d", spy.PauseTimerCalls)
	}
	if spy.ResumeTimerCalls != 1 {
		t.Errorf("expected 1 ResumeTimer, got %d", spy.ResumeTimerCalls)
	}
}

// ---------------------------------------------------------------------------
// Edge case: invalid choice
// ---------------------------------------------------------------------------

// TestHITL_InvalidChoice verifies that a choice not in the valid set
// returns an error and does not stamp, route, or complete.
func TestHITL_InvalidChoice(t *testing.T) {
	spy := newHITLAppraiseSpy()
	client := newSpyClient(t, spy)
	h := newTestQueueManager(t)
	cfg := defaultConfig()
	ctx := context.Background()
	wctx := newWorkitemContext("wi-invalid-1")

	errCh := runHandler(ctx, client, h.qm, cfg, wctx)
	h.decide(t, ctx, "wi-invalid-1", "bogus")

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

// ---------------------------------------------------------------------------
// Edge case: non-exit-bound node rejects "cancel"
// ---------------------------------------------------------------------------

// TestHITL_Minimal_CancelRejected verifies that "cancel" is invalid when the
// node has no exit contract (non-exit-bound).
func TestHITL_Minimal_CancelRejected(t *testing.T) {
	spy := newMinimalSpy()
	client := newSpyClient(t, spy)
	h := newTestQueueManager(t)
	cfg := defaultConfig()
	ctx := context.Background()
	wctx := newWorkitemContext("wi-nocancel-1")

	errCh := runHandler(ctx, client, h.qm, cfg, wctx)
	h.decide(t, ctx, "wi-nocancel-1", "cancel")

	err := <-errCh
	if err == nil {
		t.Fatal("expected error for cancel on non-exit-bound node, got nil")
	}
	if !strings.Contains(err.Error(), "invalid choice") {
		t.Errorf("expected 'invalid choice' in error, got: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if len(spy.RoutedOutputs) != 0 {
		t.Errorf("expected no routes, got %v", spy.RoutedOutputs)
	}
	if len(spy.CompletedReasons) != 0 {
		t.Errorf("expected no completions, got %v", spy.CompletedReasons)
	}
}

// ---------------------------------------------------------------------------
// Edge case: context cancellation while waiting for decision
// ---------------------------------------------------------------------------

// TestHITL_ContextCancelled verifies that cancelling the context while blocked
// on WaitForDecision returns an error.
func TestHITL_ContextCancelled(t *testing.T) {
	spy := newMinimalSpy()
	client := newSpyClient(t, spy)
	h := newTestQueueManager(t)
	cfg := defaultConfig()
	ctx, cancel := context.WithCancel(context.Background())
	wctx := newWorkitemContext("wi-ctx-cancel-1")

	errCh := runHandler(ctx, client, h.qm, cfg, wctx)

	// Wait for enqueue, then cancel the context.
	waitForEnqueue(t, h.qm, "wi-ctx-cancel-1")
	cancel()

	err := <-errCh
	if err == nil {
		t.Fatal("expected error on context cancellation, got nil")
	}
	// The error should mention either context or wait failure.
	if !strings.Contains(err.Error(), "context canceled") &&
		!strings.Contains(err.Error(), "wait for decision") {
		t.Errorf("expected context/wait error, got: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if len(spy.RoutedOutputs) != 0 {
		t.Errorf("expected no routes, got %v", spy.RoutedOutputs)
	}
	if len(spy.CompletedReasons) != 0 {
		t.Errorf("expected no completions, got %v", spy.CompletedReasons)
	}
}

// ---------------------------------------------------------------------------
// Edge case: multiple stamps applied on route
// ---------------------------------------------------------------------------

// TestHITL_MultipleStamps verifies that when a node has multiple STAMP
// capabilities, all stamps are applied before routing.
func TestHITL_MultipleStamps(t *testing.T) {
	spy := newMultiStampSpy()
	client := newSpyClient(t, spy)
	h := newTestQueueManager(t)
	cfg := defaultConfig()
	ctx := context.Background()
	wctx := newWorkitemContext("wi-multistamp-1")

	errCh := runHandler(ctx, client, h.qm, cfg, wctx)
	h.decide(t, ctx, "wi-multistamp-1", "done")

	if err := <-errCh; err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Both stamps applied.
	if len(spy.StampedArtefacts) != 2 {
		t.Fatalf("expected 2 stamps, got %d: %v", len(spy.StampedArtefacts), spy.StampedArtefacts)
	}
	// Verify each stamp (order follows capability list order).
	stamps := map[string]string{}
	for _, sr := range spy.StampedArtefacts {
		stamps[sr.ArtefactID+"/"+sr.StampName] = ""
	}
	if _, ok := stamps["petition/reviewed"]; !ok {
		t.Error("expected stamp petition/reviewed")
	}
	if _, ok := stamps["petition/approved"]; !ok {
		t.Error("expected stamp petition/approved")
	}

	// Routed to "done".
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "done" {
		t.Errorf("expected route to 'done', got %v", spy.RoutedOutputs)
	}

	// No completions.
	if len(spy.CompletedReasons) != 0 {
		t.Errorf("expected no completions, got %v", spy.CompletedReasons)
	}
}

// ---------------------------------------------------------------------------
// Capability parsing helpers — unit tests
// ---------------------------------------------------------------------------

func TestParseReadArtefacts(t *testing.T) {
	tests := []struct {
		name         string
		capabilities []string
		want         []string
	}{
		{
			name:         "single qualified",
			capabilities: []string{"READ:artefact/petition"},
			want:         []string{"petition"},
		},
		{
			name:         "multiple qualified",
			capabilities: []string{"READ:artefact/petition", "READ:artefact/evidence-bundle"},
			want:         []string{"petition", "evidence-bundle"},
		},
		{
			name:         "bare READ:artefact skipped",
			capabilities: []string{"READ:artefact"},
			want:         nil,
		},
		{
			name:         "mixed capabilities",
			capabilities: []string{"READ:flow", "READ:artefact/petition", "WRITE:feedback/new", "STAMP:artefact/haiku/review"},
			want:         []string{"petition"},
		},
		{
			name:         "empty",
			capabilities: nil,
			want:         nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseReadArtefacts(tt.capabilities)
			if len(got) != len(tt.want) {
				t.Fatalf("parseReadArtefacts()=%v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("parseReadArtefacts()[%d]=%q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestHasWriteFeedback(t *testing.T) {
	tests := []struct {
		name         string
		capabilities []string
		want         bool
	}{
		{
			name:         "qualified WRITE:feedback/new",
			capabilities: []string{"READ:flow", "WRITE:feedback/new"},
			want:         true,
		},
		{
			name:         "multiple feedback capabilities",
			capabilities: []string{"WRITE:feedback/new", "WRITE:feedback/resolved"},
			want:         true,
		},
		{
			name:         "no feedback capability",
			capabilities: []string{"READ:flow", "READ:artefact/petition"},
			want:         false,
		},
		{
			name:         "empty",
			capabilities: nil,
			want:         false,
		},
		{
			name:         "WRITE:artefact is not feedback",
			capabilities: []string{"WRITE:artefact"},
			want:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasWriteFeedback(tt.capabilities)
			if got != tt.want {
				t.Errorf("hasWriteFeedback()=%v, want %v", got, tt.want)
			}
		})
	}
}
