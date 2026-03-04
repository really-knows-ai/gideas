package main

import (
	"context"
	"strings"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
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
	qm := newTestQueueManager(t)
	cfg := configWithLabels(map[string]string{"approved": "Approve Petition"})
	ctx := context.Background()
	wctx := newWorkitemContext("wi-appraise-1")

	errCh := runHandler(ctx, client, qm, cfg, wctx)
	simulateDecision(t, ctx, qm, "wi-appraise-1", outputApproved)

	if err := <-errCh; err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Topology was queried.
	if spy.TopologyCalls != 1 {
		t.Errorf("expected 1 GetFlowTopology call, got %d", spy.TopologyCalls)
	}

	// Petition artefact was read (from READ:artefact/petition).
	if len(spy.ReadArtefacts) != 1 {
		t.Fatalf("expected 1 GetArtefact call, got %d", len(spy.ReadArtefacts))
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
	qm := newTestQueueManager(t)
	cfg := defaultConfig()
	ctx := context.Background()
	wctx := newWorkitemContext("wi-arbiter-1")

	errCh := runHandler(ctx, client, qm, cfg, wctx)
	simulateDecision(t, ctx, qm, "wi-arbiter-1", outputResolution)

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
	qm := newTestQueueManager(t)
	cfg := defaultConfig()
	ctx := context.Background()
	wctx := newWorkitemContext("wi-tribunal-1")

	errCh := runHandler(ctx, client, qm, cfg, wctx)
	simulateDecision(t, ctx, qm, "wi-tribunal-1", outputResolution)

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
	qm := newTestQueueManager(t)
	cfg := defaultConfig()
	ctx := context.Background()
	wctx := newWorkitemContext("wi-minimal-1")

	errCh := runHandler(ctx, client, qm, cfg, wctx)
	simulateDecision(t, ctx, qm, "wi-minimal-1", "default")

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
	b := deriveBehaviour(spy.Topology)

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
	b := deriveBehaviour(spy.Topology)

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
	b := deriveBehaviour(spy.Topology)

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
// buildChoicesResponse — pure function tests
// ---------------------------------------------------------------------------

func TestBuildChoicesResponse_HITLAppraise_WithLabels(t *testing.T) {
	spy := newHITLAppraiseSpy()
	cfg := configWithLabels(map[string]string{
		"approved": "Approve Petition",
		"cancel":   "Reject & Cancel",
	})

	resp := buildChoicesResponse(spy.Topology, cfg)

	if !resp.HasFeedback {
		t.Error("expected HasFeedback=true")
	}
	if !resp.HasCancel {
		t.Error("expected HasCancel=true")
	}
	// 2 choices: approved (route) + cancel.
	if len(resp.Choices) != 2 {
		t.Fatalf("expected 2 choices, got %d", len(resp.Choices))
	}

	// First: route choice.
	c0 := resp.Choices[0]
	if c0.Value != "approved" {
		t.Errorf("choice[0].Value=%q, want 'approved'", c0.Value)
	}
	if c0.Label != "Approve Petition" {
		t.Errorf("choice[0].Label=%q, want 'Approve Petition'", c0.Label)
	}
	if c0.Type != choiceTypeRoute {
		t.Errorf("choice[0].Type=%q, want 'route'", c0.Type)
	}

	// Second: cancel choice.
	c1 := resp.Choices[1]
	if c1.Value != "cancel" {
		t.Errorf("choice[1].Value=%q, want 'cancel'", c1.Value)
	}
	if c1.Label != "Reject & Cancel" {
		t.Errorf("choice[1].Label=%q, want 'Reject & Cancel'", c1.Label)
	}
	if c1.Type != choiceTypeCancel {
		t.Errorf("choice[1].Type=%q, want 'cancel'", c1.Type)
	}
}

func TestBuildChoicesResponse_ArbiterResolve_DefaultLabels(t *testing.T) {
	spy := newArbiterHITLResolveSpy()
	cfg := defaultConfig()

	resp := buildChoicesResponse(spy.Topology, cfg)

	if resp.HasFeedback {
		t.Error("expected HasFeedback=false")
	}
	if !resp.HasCancel {
		t.Error("expected HasCancel=true")
	}
	if len(resp.Choices) != 2 {
		t.Fatalf("expected 2 choices, got %d", len(resp.Choices))
	}

	// Route uses output name as label when no config label.
	if resp.Choices[0].Label != outputResolution {
		t.Errorf("expected label='resolution' (default), got %q", resp.Choices[0].Label)
	}

	// Cancel uses "Cancel" as default label.
	if resp.Choices[1].Label != "Cancel" {
		t.Errorf("expected label='Cancel' (default), got %q", resp.Choices[1].Label)
	}
}

func TestBuildChoicesResponse_Minimal_NoCancel(t *testing.T) {
	spy := newMinimalSpy()
	cfg := defaultConfig()

	resp := buildChoicesResponse(spy.Topology, cfg)

	if resp.HasFeedback {
		t.Error("expected HasFeedback=false")
	}
	if resp.HasCancel {
		t.Error("expected HasCancel=false")
	}
	// Only 1 choice: "default" route. No cancel.
	if len(resp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
	}
	if resp.Choices[0].Value != "default" {
		t.Errorf("choice[0].Value=%q, want 'default'", resp.Choices[0].Value)
	}
	if resp.Choices[0].Type != choiceTypeRoute {
		t.Errorf("choice[0].Type=%q, want 'route'", resp.Choices[0].Type)
	}
}

func TestBuildChoicesResponse_MultipleOutputs(t *testing.T) {
	topology := &flowv1.GetFlowTopologyResponse{
		Self: &flowv1.FlowNode{
			Name:         "hitl-multi",
			Capabilities: []string{"READ:flow"},
			Outputs: []*flowv1.FlowOutput{
				{Name: "approve", Target: "node-a"},
				{Name: "reject", Target: "node-b"},
				{Name: "escalate", Target: "node-c"},
			},
		},
	}
	cfg := configWithLabels(map[string]string{
		"approve":  "Approve",
		"reject":   "Reject",
		"escalate": "Escalate to Manager",
	})

	resp := buildChoicesResponse(topology, cfg)

	if len(resp.Choices) != 3 {
		t.Fatalf("expected 3 choices, got %d", len(resp.Choices))
	}

	expected := []struct {
		value string
		label string
	}{
		{"approve", "Approve"},
		{"reject", "Reject"},
		{"escalate", "Escalate to Manager"},
	}
	for i, e := range expected {
		if resp.Choices[i].Value != e.value {
			t.Errorf("choice[%d].Value=%q, want %q", i, resp.Choices[i].Value, e.value)
		}
		if resp.Choices[i].Label != e.label {
			t.Errorf("choice[%d].Label=%q, want %q", i, resp.Choices[i].Label, e.label)
		}
		if resp.Choices[i].Type != choiceTypeRoute {
			t.Errorf("choice[%d].Type=%q, want 'route'", i, resp.Choices[i].Type)
		}
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
	qm := newTestQueueManager(t)
	cfg := configWithLabels(map[string]string{"approved": "Approve Petition"})
	ctx := context.Background()
	wctx := newWorkitemContext("wi-cancel-1")

	errCh := runHandler(ctx, client, qm, cfg, wctx)
	simulateDecision(t, ctx, qm, "wi-cancel-1", "cancel")

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
	qm := newTestQueueManager(t)
	cfg := defaultConfig()
	ctx := context.Background()
	wctx := newWorkitemContext("wi-invalid-1")

	errCh := runHandler(ctx, client, qm, cfg, wctx)
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

// ---------------------------------------------------------------------------
// Edge case: non-exit-bound node rejects "cancel"
// ---------------------------------------------------------------------------

// TestHITL_Minimal_CancelRejected verifies that "cancel" is invalid when the
// node has no exit contract (non-exit-bound).
func TestHITL_Minimal_CancelRejected(t *testing.T) {
	spy := newMinimalSpy()
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)
	cfg := defaultConfig()
	ctx := context.Background()
	wctx := newWorkitemContext("wi-nocancel-1")

	errCh := runHandler(ctx, client, qm, cfg, wctx)
	simulateDecision(t, ctx, qm, "wi-nocancel-1", "cancel")

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
// Edge case: empty choice (QueueManager shutdown)
// ---------------------------------------------------------------------------

// TestHITL_EmptyChoice_QMShutdown verifies that when the QueueManager is
// stopped before a decision, the handler receives an empty choice and returns
// an error.
func TestHITL_EmptyChoice_QMShutdown(t *testing.T) {
	spy := newMinimalSpy()
	client := newSpyClient(t, spy)
	qm, stopQM := newTestQueueManagerWithStop(t)
	cfg := defaultConfig()
	ctx := context.Background()
	wctx := newWorkitemContext("wi-shutdown-1")

	errCh := runHandler(ctx, client, qm, cfg, wctx)

	// Wait for the item to be enqueued, then stop the QM (unblocks WaitForDecision
	// with empty choice).
	waitForEnqueue(t, qm, "wi-shutdown-1")
	if err := stopQM(); err != nil {
		t.Fatalf("QueueManager.Stop() failed: %v", err)
	}

	err := <-errCh
	if err == nil {
		t.Fatal("expected error for empty choice (QM shutdown), got nil")
	}
	if !strings.Contains(err.Error(), "empty choice") {
		t.Errorf("expected 'empty choice' in error, got: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Nothing dispatched.
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
	qm := newTestQueueManager(t)
	cfg := defaultConfig()
	ctx, cancel := context.WithCancel(context.Background())
	wctx := newWorkitemContext("wi-ctx-cancel-1")

	errCh := runHandler(ctx, client, qm, cfg, wctx)

	// Wait for enqueue, then cancel the context.
	waitForEnqueue(t, qm, "wi-ctx-cancel-1")
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
	qm := newTestQueueManager(t)
	cfg := defaultConfig()
	ctx := context.Background()
	wctx := newWorkitemContext("wi-multistamp-1")

	errCh := runHandler(ctx, client, qm, cfg, wctx)
	simulateDecision(t, ctx, qm, "wi-multistamp-1", "done")

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
