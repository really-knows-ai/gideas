package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal"
	flow "github.com/gideas/flow/sdk/go"
)

// Test constants for petition IDs used across multiple tests.
const (
	testPetitionID1 = "pet-1"
	testPetitionID2 = "pet-2"
)

// ---------------------------------------------------------------------------
// Tests — nextBackoff / sleepCtx
// ---------------------------------------------------------------------------

func TestNextBackoff(t *testing.T) {
	// Should double.
	if got := nextBackoff(1 * time.Second); got != 2*time.Second {
		t.Fatalf("expected 2s, got %v", got)
	}

	// Should cap at max.
	if got := nextBackoff(20 * time.Second); got != reconnectMaxDelay {
		t.Fatalf("expected %v, got %v", reconnectMaxDelay, got)
	}

	// At max should stay at max.
	if got := nextBackoff(reconnectMaxDelay); got != reconnectMaxDelay {
		t.Fatalf("expected %v, got %v", reconnectMaxDelay, got)
	}
}

func TestSleepCtx_Completes(t *testing.T) {
	ctx := context.Background()
	if !sleepCtx(ctx, 10*time.Millisecond) {
		t.Fatal("expected sleepCtx to complete")
	}
}

func TestSleepCtx_Cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	if sleepCtx(ctx, 10*time.Second) {
		t.Fatal("expected sleepCtx to return false on cancelled context")
	}
}

// ---------------------------------------------------------------------------
// Tests — consumeOutcomes (integration with spy servers)
// ---------------------------------------------------------------------------

func TestConsumeOutcomes_ProcessesEvents(t *testing.T) {
	fedSpy := &spyFederation{
		events: []*flowv1.PetitionOutcomeEvent{
			makeAcceptedEvent(testPetitionID1, "new-law-1"),
			makeAcceptedEvent(testPetitionID2, "new-law-2"),
		},
	}
	fedAddr := startFederationServer(t, fedSpy)

	fedClient, err := flow.NewFederationClientForTest(fedAddr)
	if err != nil {
		t.Fatalf("NewFederationClientForTest() failed: %v", err)
	}
	defer func() { _ = fedClient.Close() }()

	tracker := internal.NewPendingTracker()

	stream, err := fedClient.SubscribePetitionOutcomes(context.Background(), "test-flow")
	if err != nil {
		t.Fatalf("SubscribePetitionOutcomes() failed: %v", err)
	}

	// consumeOutcomes should process both events and return nil (EOF).
	// Pass nil entry client — this test does not exercise acceptance logic.
	err = consumeOutcomes(context.Background(), stream, tracker, nil)
	if err != nil {
		t.Fatalf("consumeOutcomes() returned error: %v", err)
	}

	// Verify both petition IDs were marked as pending.
	if tracker.MarkPending(testPetitionID1) {
		t.Error("expected pet-1 to be pending (MarkPending returned true)")
	}
	if tracker.MarkPending(testPetitionID2) {
		t.Error("expected pet-2 to be pending (MarkPending returned true)")
	}
}

func TestConsumeOutcomes_DeduplicatesSamePetitionID(t *testing.T) {
	fedSpy := &spyFederation{
		events: []*flowv1.PetitionOutcomeEvent{
			makeAcceptedEvent(testPetitionID1, "law-1"),
			makeAcceptedEvent(testPetitionID1, "law-1"), // duplicate
			makeRejectedEvent(testPetitionID2),
		},
	}
	fedAddr := startFederationServer(t, fedSpy)

	fedClient, err := flow.NewFederationClientForTest(fedAddr)
	if err != nil {
		t.Fatalf("NewFederationClientForTest() failed: %v", err)
	}
	defer func() { _ = fedClient.Close() }()

	tracker := internal.NewPendingTracker()

	stream, err := fedClient.SubscribePetitionOutcomes(context.Background(), "test-flow")
	if err != nil {
		t.Fatalf("SubscribePetitionOutcomes() failed: %v", err)
	}

	err = consumeOutcomes(context.Background(), stream, tracker, nil)
	if err != nil {
		t.Fatalf("consumeOutcomes() returned error: %v", err)
	}

	// Both IDs should be pending (but pet-1 was only logged once).
	if tracker.MarkPending(testPetitionID1) {
		t.Error("expected pet-1 to be pending")
	}
	if tracker.MarkPending(testPetitionID2) {
		t.Error("expected pet-2 to be pending")
	}
}

func TestConsumeOutcomes_ContextCancelled(t *testing.T) {
	fedSpy := &spyFederation{
		events: []*flowv1.PetitionOutcomeEvent{
			makeAcceptedEvent(testPetitionID1, "law-1"),
		},
	}
	fedAddr := startFederationServer(t, fedSpy)

	fedClient, err := flow.NewFederationClientForTest(fedAddr)
	if err != nil {
		t.Fatalf("NewFederationClientForTest() failed: %v", err)
	}
	defer func() { _ = fedClient.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	stream, err := fedClient.SubscribePetitionOutcomes(ctx, "test-flow")
	if err != nil {
		// Subscribe may fail immediately on cancelled context, that's fine.
		return
	}

	tracker := internal.NewPendingTracker()
	err = consumeOutcomes(ctx, stream, tracker, nil)
	if err == nil {
		t.Fatal("expected error from consumeOutcomes on cancelled context")
	}
}

// ---------------------------------------------------------------------------
// Tests — watchOutcomes reconnect loop
// ---------------------------------------------------------------------------

func TestWatchOutcomes_ConnectsToFederation(t *testing.T) {
	fedSpy := &spyFederation{
		events: []*flowv1.PetitionOutcomeEvent{
			makeAcceptedEvent(testPetitionID1, "law-1"),
		},
	}
	fedAddr := startFederationServer(t, fedSpy)

	fedClient, err := flow.NewFederationClientForTest(fedAddr)
	if err != nil {
		t.Fatalf("NewFederationClientForTest() failed: %v", err)
	}
	defer func() { _ = fedClient.Close() }()

	// Use a short-lived context so the reconnect loop exits after one
	// successful subscribe + consume cycle (stream ends with EOF, then
	// next subscribe attempt sees cancelled context).
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = watchOutcomesWithClient(ctx, fedClient, "test-flow", nil)
	if err == nil || err != context.DeadlineExceeded {
		// The function should return context.DeadlineExceeded when the
		// timeout fires during the reconnect loop.
		// It may also return context.Canceled — both are acceptable.
		if err != context.Canceled {
			t.Fatalf("expected context error, got: %v", err)
		}
	}

	// Federation spy should have received at least one subscribe call.
	if fedSpy.getSubCalls() < 1 {
		t.Fatalf("expected at least 1 subscribe call, got %d", fedSpy.getSubCalls())
	}
}

func TestWatchOutcomes_ReconnectsOnStreamError(t *testing.T) {
	fedSpy := &spyFederation{
		returnErr: fmt.Errorf("stream broken"),
	}
	fedAddr := startFederationServer(t, fedSpy)

	fedClient, err := flow.NewFederationClientForTest(fedAddr)
	if err != nil {
		t.Fatalf("NewFederationClientForTest() failed: %v", err)
	}
	defer func() { _ = fedClient.Close() }()

	// Let the watcher run briefly — it should attempt to subscribe,
	// get an error, and try again (reconnect loop).
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err = watchOutcomesWithClient(ctx, fedClient, "test-flow", nil)
	// Should eventually return context error.
	if err == nil {
		t.Fatal("expected error from watchOutcomesWithClient")
	}

	// Should have attempted multiple subscribes due to reconnect.
	calls := fedSpy.getSubCalls()
	if calls < 2 {
		t.Fatalf("expected at least 2 subscribe calls (reconnect), got %d", calls)
	}
}

// ---------------------------------------------------------------------------
// Tests — handleOutcome (handler stub)
// ---------------------------------------------------------------------------

func TestHandleOutcome_Stub(t *testing.T) {
	spy := &handlerSpy{}
	client := newHandlerTestClient(t, spy)

	wctx := &flowv1.WorkitemContext{
		FlowNamespace: "test-ns",
		WorkitemId:    "wi-petition-001",
		NodeId:        "petition-watcher",
		Metadata: map[string]string{
			"petition_id": testPetitionID1,
		},
	}

	err := processOutcome(context.Background(), client, wctx)
	if err != nil {
		t.Fatalf("processOutcome() returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Verify heartbeat was sent.
	if spy.heartbeatCount != 1 {
		t.Errorf("expected 1 heartbeat, got %d", spy.heartbeatCount)
	}

	// Stub handler should complete the workitem.
	if spy.completedCount != 1 {
		t.Errorf("expected 1 complete call, got %d", spy.completedCount)
	}
}

// ---------------------------------------------------------------------------
// Tests — acceptance path (RetireDisputeRecord on ACCEPTED events)
// ---------------------------------------------------------------------------

func TestConsumeOutcomes_Accepted_CallsRetireDisputeRecord(t *testing.T) {
	fedSpy := &spyFederation{
		events: []*flowv1.PetitionOutcomeEvent{
			makeAcceptedEvent(testPetitionID1, "new-law-1"),
		},
	}
	fedAddr := startFederationServer(t, fedSpy)

	fedClient, err := flow.NewFederationClientForTest(fedAddr)
	if err != nil {
		t.Fatalf("NewFederationClientForTest() failed: %v", err)
	}
	defer func() { _ = fedClient.Close() }()

	entrySpy := &entryClientSpy{}
	entryClient := newEntryTestClient(t, entrySpy)

	tracker := internal.NewPendingTracker()

	stream, err := fedClient.SubscribePetitionOutcomes(context.Background(), "test-flow")
	if err != nil {
		t.Fatalf("SubscribePetitionOutcomes() failed: %v", err)
	}

	err = consumeOutcomes(context.Background(), stream, tracker, entryClient)
	if err != nil {
		t.Fatalf("consumeOutcomes() returned error: %v", err)
	}

	// Verify RetireDisputeRecord was called with the correct petition_id.
	retired := entrySpy.getRetiredPetitionIDs()
	if len(retired) != 1 || retired[0] != testPetitionID1 {
		t.Fatalf("expected RetireDisputeRecord(%q), got %v", testPetitionID1, retired)
	}
}

func TestConsumeOutcomes_Accepted_RetireNotFound_LogsAndContinues(t *testing.T) {
	fedSpy := &spyFederation{
		events: []*flowv1.PetitionOutcomeEvent{
			makeAcceptedEvent(testPetitionID1, "law-1"),
			makeAcceptedEvent(testPetitionID2, "law-2"),
		},
	}
	fedAddr := startFederationServer(t, fedSpy)

	fedClient, err := flow.NewFederationClientForTest(fedAddr)
	if err != nil {
		t.Fatalf("NewFederationClientForTest() failed: %v", err)
	}
	defer func() { _ = fedClient.Close() }()

	// RetireDisputeRecord returns NotFound for all calls.
	entrySpy := &entryClientSpy{retireErr: notFoundErr()}
	entryClient := newEntryTestClient(t, entrySpy)

	tracker := internal.NewPendingTracker()

	stream, err := fedClient.SubscribePetitionOutcomes(context.Background(), "test-flow")
	if err != nil {
		t.Fatalf("SubscribePetitionOutcomes() failed: %v", err)
	}

	// Should NOT return an error — NotFound is logged and processing continues.
	err = consumeOutcomes(context.Background(), stream, tracker, entryClient)
	if err != nil {
		t.Fatalf("consumeOutcomes() returned error: %v (expected nil, NotFound should be handled)", err)
	}

	// Both petitions should still have been attempted.
	retired := entrySpy.getRetiredPetitionIDs()
	if len(retired) != 2 {
		t.Fatalf("expected 2 RetireDisputeRecord calls, got %d: %v", len(retired), retired)
	}
}

func TestConsumeOutcomes_Accepted_RetireOtherError_LogsAndContinues(t *testing.T) {
	fedSpy := &spyFederation{
		events: []*flowv1.PetitionOutcomeEvent{
			makeAcceptedEvent(testPetitionID1, "law-1"),
			makeAcceptedEvent(testPetitionID2, "law-2"),
		},
	}
	fedAddr := startFederationServer(t, fedSpy)

	fedClient, err := flow.NewFederationClientForTest(fedAddr)
	if err != nil {
		t.Fatalf("NewFederationClientForTest() failed: %v", err)
	}
	defer func() { _ = fedClient.Close() }()

	// RetireDisputeRecord returns a non-NotFound error.
	entrySpy := &entryClientSpy{retireErr: fmt.Errorf("librarian unavailable")}
	entryClient := newEntryTestClient(t, entrySpy)

	tracker := internal.NewPendingTracker()

	stream, err := fedClient.SubscribePetitionOutcomes(context.Background(), "test-flow")
	if err != nil {
		t.Fatalf("SubscribePetitionOutcomes() failed: %v", err)
	}

	// Best-effort: should NOT return an error, just log and continue.
	err = consumeOutcomes(context.Background(), stream, tracker, entryClient)
	if err != nil {
		t.Fatalf("consumeOutcomes() returned error: %v (expected nil, error should be logged)", err)
	}

	// Both petitions should still have been attempted.
	retired := entrySpy.getRetiredPetitionIDs()
	if len(retired) != 2 {
		t.Fatalf("expected 2 RetireDisputeRecord calls, got %d: %v", len(retired), retired)
	}
}

// ---------------------------------------------------------------------------
// Tests — rejection path (RetireDisputeRecord + CreateWorkitem on REJECTED)
// ---------------------------------------------------------------------------

func TestConsumeOutcomes_Rejected_CallsRetireDisputeRecord(t *testing.T) {
	fedSpy := &spyFederation{
		events: []*flowv1.PetitionOutcomeEvent{
			makeRejectedEvent(testPetitionID1),
		},
	}
	fedAddr := startFederationServer(t, fedSpy)

	fedClient, err := flow.NewFederationClientForTest(fedAddr)
	if err != nil {
		t.Fatalf("NewFederationClientForTest() failed: %v", err)
	}
	defer func() { _ = fedClient.Close() }()

	entrySpy := &entryClientSpy{}
	entryClient := newEntryTestClient(t, entrySpy)

	tracker := internal.NewPendingTracker()

	stream, err := fedClient.SubscribePetitionOutcomes(context.Background(), "test-flow")
	if err != nil {
		t.Fatalf("SubscribePetitionOutcomes() failed: %v", err)
	}

	err = consumeOutcomes(context.Background(), stream, tracker, entryClient)
	if err != nil {
		t.Fatalf("consumeOutcomes() returned error: %v", err)
	}

	// Verify RetireDisputeRecord was called with the correct petition_id.
	retired := entrySpy.getRetiredPetitionIDs()
	if len(retired) != 1 || retired[0] != testPetitionID1 {
		t.Fatalf("expected RetireDisputeRecord(%q), got %v", testPetitionID1, retired)
	}
}

func TestConsumeOutcomes_Rejected_CreatesClerkCycleWorkitem(t *testing.T) {
	fedSpy := &spyFederation{
		events: []*flowv1.PetitionOutcomeEvent{
			makeRejectedEvent(testPetitionID1),
		},
	}
	fedAddr := startFederationServer(t, fedSpy)

	fedClient, err := flow.NewFederationClientForTest(fedAddr)
	if err != nil {
		t.Fatalf("NewFederationClientForTest() failed: %v", err)
	}
	defer func() { _ = fedClient.Close() }()

	entrySpy := &entryClientSpy{}
	entryClient := newEntryTestClient(t, entrySpy)

	tracker := internal.NewPendingTracker()

	stream, err := fedClient.SubscribePetitionOutcomes(context.Background(), "test-flow")
	if err != nil {
		t.Fatalf("SubscribePetitionOutcomes() failed: %v", err)
	}

	err = consumeOutcomes(context.Background(), stream, tracker, entryClient)
	if err != nil {
		t.Fatalf("consumeOutcomes() returned error: %v", err)
	}

	// Verify CreateWorkitem was called exactly once.
	created := entrySpy.getCreatedWorkitems()
	if len(created) != 1 {
		t.Fatalf("expected 1 CreateWorkitem call, got %d", len(created))
	}

	md := created[0]

	// Verify petition_id metadata.
	if got := md["petition_id"]; got != testPetitionID1 {
		t.Errorf("expected metadata petition_id=%q, got %q", testPetitionID1, got)
	}

	// Verify trigger metadata.
	if got := md["trigger"]; got != "petition-rejected" {
		t.Errorf("expected metadata trigger=%q, got %q", "petition-rejected", got)
	}

	// Verify rejection_report is valid JSON with expected fields.
	reportJSON, ok := md["rejection_report"]
	if !ok {
		t.Fatal("expected metadata to contain rejection_report key")
	}

	var report map[string]any
	if err := json.Unmarshal([]byte(reportJSON), &report); err != nil {
		t.Fatalf("rejection_report is not valid JSON: %v", err)
	}

	if got := report["petition_id"]; got != testPetitionID1 {
		t.Errorf("expected rejection_report.petition_id=%q, got %v", testPetitionID1, got)
	}

	if got := report["reason"]; got != "PUBLICATION_REJECTION_REASON_CONFLICT" {
		t.Errorf("expected rejection_report.reason=%q, got %v",
			"PUBLICATION_REJECTION_REASON_CONFLICT", got)
	}

	// conflicting_law_ids should be ["law-A", "law-B"].
	lawIDs, ok := report["conflicting_law_ids"].([]any)
	if !ok || len(lawIDs) != 2 {
		t.Fatalf("expected rejection_report.conflicting_law_ids=[law-A, law-B], got %v", report["conflicting_law_ids"])
	}
	if lawIDs[0] != "law-A" || lawIDs[1] != "law-B" {
		t.Errorf("expected conflicting_law_ids=[law-A, law-B], got %v", lawIDs)
	}

	if got := report["remediation_text"]; got != "Conflicts with existing law" {
		t.Errorf("expected rejection_report.remediation_text=%q, got %v",
			"Conflicts with existing law", got)
	}
}

func TestConsumeOutcomes_Rejected_RetireNotFound_StillCreatesWorkitem(t *testing.T) {
	fedSpy := &spyFederation{
		events: []*flowv1.PetitionOutcomeEvent{
			makeRejectedEvent(testPetitionID1),
		},
	}
	fedAddr := startFederationServer(t, fedSpy)

	fedClient, err := flow.NewFederationClientForTest(fedAddr)
	if err != nil {
		t.Fatalf("NewFederationClientForTest() failed: %v", err)
	}
	defer func() { _ = fedClient.Close() }()

	// RetireDisputeRecord returns NotFound — should still create workitem.
	entrySpy := &entryClientSpy{retireErr: notFoundErr()}
	entryClient := newEntryTestClient(t, entrySpy)

	tracker := internal.NewPendingTracker()

	stream, err := fedClient.SubscribePetitionOutcomes(context.Background(), "test-flow")
	if err != nil {
		t.Fatalf("SubscribePetitionOutcomes() failed: %v", err)
	}

	err = consumeOutcomes(context.Background(), stream, tracker, entryClient)
	if err != nil {
		t.Fatalf("consumeOutcomes() returned error: %v", err)
	}

	// RetireDisputeRecord was attempted.
	retired := entrySpy.getRetiredPetitionIDs()
	if len(retired) != 1 {
		t.Fatalf("expected 1 RetireDisputeRecord call, got %d", len(retired))
	}

	// CreateWorkitem should still have been called despite retire failure.
	created := entrySpy.getCreatedWorkitems()
	if len(created) != 1 {
		t.Fatalf("expected 1 CreateWorkitem call after retire NotFound, got %d", len(created))
	}

	if got := created[0]["petition_id"]; got != testPetitionID1 {
		t.Errorf("expected workitem metadata petition_id=%q, got %q", testPetitionID1, got)
	}
}

func TestConsumeOutcomes_Rejected_CreateWorkitemError_LogsAndContinues(t *testing.T) {
	fedSpy := &spyFederation{
		events: []*flowv1.PetitionOutcomeEvent{
			makeRejectedEvent(testPetitionID1),
			makeRejectedEvent(testPetitionID2),
		},
	}
	fedAddr := startFederationServer(t, fedSpy)

	fedClient, err := flow.NewFederationClientForTest(fedAddr)
	if err != nil {
		t.Fatalf("NewFederationClientForTest() failed: %v", err)
	}
	defer func() { _ = fedClient.Close() }()

	// CreateWorkitem returns an error.
	entrySpy := &entryClientSpy{createWIErr: fmt.Errorf("operator unavailable")}
	entryClient := newEntryTestClient(t, entrySpy)

	tracker := internal.NewPendingTracker()

	stream, err := fedClient.SubscribePetitionOutcomes(context.Background(), "test-flow")
	if err != nil {
		t.Fatalf("SubscribePetitionOutcomes() failed: %v", err)
	}

	// Should NOT return an error — CreateWorkitem failure is logged and
	// processing continues to the next event.
	err = consumeOutcomes(context.Background(), stream, tracker, entryClient)
	if err != nil {
		t.Fatalf("consumeOutcomes() returned error: %v (expected nil)", err)
	}

	// Both petitions should have attempted CreateWorkitem.
	created := entrySpy.getCreatedWorkitems()
	if len(created) != 2 {
		t.Fatalf("expected 2 CreateWorkitem calls, got %d", len(created))
	}

	// Both should also have attempted RetireDisputeRecord.
	retired := entrySpy.getRetiredPetitionIDs()
	if len(retired) != 2 {
		t.Fatalf("expected 2 RetireDisputeRecord calls, got %d", len(retired))
	}
}

// ---------------------------------------------------------------------------
// Tests — held workitem discovery and resume (Slice 13.10.4)
// ---------------------------------------------------------------------------

func TestConsumeOutcomes_Accepted_ResumesHeldWorkitems(t *testing.T) {
	fedSpy := &spyFederation{
		events: []*flowv1.PetitionOutcomeEvent{
			makeAcceptedEvent(testPetitionID1, "new-law-1"),
		},
	}
	fedAddr := startFederationServer(t, fedSpy)

	fedClient, err := flow.NewFederationClientForTest(fedAddr)
	if err != nil {
		t.Fatalf("NewFederationClientForTest() failed: %v", err)
	}
	defer func() { _ = fedClient.Close() }()

	// Configure spy to return two held workitems for the petition.
	entrySpy := &entryClientSpy{
		listSuspendedResp: []*flowv1.SuspendedWorkitemInfo{
			{WorkitemId: "wi-held-001", ResumeCondition: `dispute_retired("` + testPetitionID1 + `")`},
			{WorkitemId: "wi-held-002", ResumeCondition: `dispute_retired("` + testPetitionID1 + `")`},
		},
	}
	entryClient := newEntryTestClient(t, entrySpy)

	tracker := internal.NewPendingTracker()

	stream, err := fedClient.SubscribePetitionOutcomes(context.Background(), "test-flow")
	if err != nil {
		t.Fatalf("SubscribePetitionOutcomes() failed: %v", err)
	}

	err = consumeOutcomes(context.Background(), stream, tracker, entryClient)
	if err != nil {
		t.Fatalf("consumeOutcomes() returned error: %v", err)
	}

	// Verify both held workitems were resumed.
	resumed := entrySpy.getResumedWorkitemIDs()
	if len(resumed) != 2 {
		t.Fatalf("expected 2 ResumeWorkitem calls, got %d: %v", len(resumed), resumed)
	}
	if resumed[0] != "wi-held-001" || resumed[1] != "wi-held-002" {
		t.Errorf("expected resumed workitems [wi-held-001, wi-held-002], got %v", resumed)
	}
}

func TestConsumeOutcomes_Accepted_ZeroHeldWorkitems_NoError(t *testing.T) {
	fedSpy := &spyFederation{
		events: []*flowv1.PetitionOutcomeEvent{
			makeAcceptedEvent(testPetitionID1, "new-law-1"),
		},
	}
	fedAddr := startFederationServer(t, fedSpy)

	fedClient, err := flow.NewFederationClientForTest(fedAddr)
	if err != nil {
		t.Fatalf("NewFederationClientForTest() failed: %v", err)
	}
	defer func() { _ = fedClient.Close() }()

	// ListSuspendedWorkitems returns empty list.
	entrySpy := &entryClientSpy{
		listSuspendedResp: nil, // empty
	}
	entryClient := newEntryTestClient(t, entrySpy)

	tracker := internal.NewPendingTracker()

	stream, err := fedClient.SubscribePetitionOutcomes(context.Background(), "test-flow")
	if err != nil {
		t.Fatalf("SubscribePetitionOutcomes() failed: %v", err)
	}

	// Should NOT return an error — zero held workitems is normal.
	err = consumeOutcomes(context.Background(), stream, tracker, entryClient)
	if err != nil {
		t.Fatalf("consumeOutcomes() returned error: %v", err)
	}

	// No ResumeWorkitem calls expected.
	resumed := entrySpy.getResumedWorkitemIDs()
	if len(resumed) != 0 {
		t.Fatalf("expected 0 ResumeWorkitem calls, got %d: %v", len(resumed), resumed)
	}
}

func TestConsumeOutcomes_Accepted_ListSuspendedError_LogsAndContinues(t *testing.T) {
	fedSpy := &spyFederation{
		events: []*flowv1.PetitionOutcomeEvent{
			makeAcceptedEvent(testPetitionID1, "law-1"),
			makeAcceptedEvent(testPetitionID2, "law-2"),
		},
	}
	fedAddr := startFederationServer(t, fedSpy)

	fedClient, err := flow.NewFederationClientForTest(fedAddr)
	if err != nil {
		t.Fatalf("NewFederationClientForTest() failed: %v", err)
	}
	defer func() { _ = fedClient.Close() }()

	// ListSuspendedWorkitems returns an error.
	entrySpy := &entryClientSpy{
		listSuspendedErr: fmt.Errorf("operator unavailable"),
	}
	entryClient := newEntryTestClient(t, entrySpy)

	tracker := internal.NewPendingTracker()

	stream, err := fedClient.SubscribePetitionOutcomes(context.Background(), "test-flow")
	if err != nil {
		t.Fatalf("SubscribePetitionOutcomes() failed: %v", err)
	}

	// Should NOT return an error — list failure is logged and processing continues.
	err = consumeOutcomes(context.Background(), stream, tracker, entryClient)
	if err != nil {
		t.Fatalf("consumeOutcomes() returned error: %v (expected nil)", err)
	}

	// Both petitions should have been attempted (retire called for both).
	retired := entrySpy.getRetiredPetitionIDs()
	if len(retired) != 2 {
		t.Fatalf("expected 2 RetireDisputeRecord calls, got %d", len(retired))
	}
}

func TestConsumeOutcomes_Accepted_ResumeFailure_DoesNotBlockOthers(t *testing.T) {
	fedSpy := &spyFederation{
		events: []*flowv1.PetitionOutcomeEvent{
			makeAcceptedEvent(testPetitionID1, "new-law-1"),
		},
	}
	fedAddr := startFederationServer(t, fedSpy)

	fedClient, err := flow.NewFederationClientForTest(fedAddr)
	if err != nil {
		t.Fatalf("NewFederationClientForTest() failed: %v", err)
	}
	defer func() { _ = fedClient.Close() }()

	// Two held workitems, but ResumeWorkitem will error.
	entrySpy := &entryClientSpy{
		listSuspendedResp: []*flowv1.SuspendedWorkitemInfo{
			{WorkitemId: "wi-held-001", ResumeCondition: `dispute_retired("` + testPetitionID1 + `")`},
			{WorkitemId: "wi-held-002", ResumeCondition: `dispute_retired("` + testPetitionID1 + `")`},
		},
		resumeErr: fmt.Errorf("resume failed"),
	}
	entryClient := newEntryTestClient(t, entrySpy)

	tracker := internal.NewPendingTracker()

	stream, err := fedClient.SubscribePetitionOutcomes(context.Background(), "test-flow")
	if err != nil {
		t.Fatalf("SubscribePetitionOutcomes() failed: %v", err)
	}

	// Should NOT return an error — resume failures are logged per-workitem.
	err = consumeOutcomes(context.Background(), stream, tracker, entryClient)
	if err != nil {
		t.Fatalf("consumeOutcomes() returned error: %v (expected nil)", err)
	}

	// Both workitems should have been attempted even though both fail.
	resumed := entrySpy.getResumedWorkitemIDs()
	if len(resumed) != 2 {
		t.Fatalf("expected 2 ResumeWorkitem attempts, got %d: %v", len(resumed), resumed)
	}
}

func TestConsumeOutcomes_Rejected_ResumesHeldWorkitems(t *testing.T) {
	fedSpy := &spyFederation{
		events: []*flowv1.PetitionOutcomeEvent{
			makeRejectedEvent(testPetitionID1),
		},
	}
	fedAddr := startFederationServer(t, fedSpy)

	fedClient, err := flow.NewFederationClientForTest(fedAddr)
	if err != nil {
		t.Fatalf("NewFederationClientForTest() failed: %v", err)
	}
	defer func() { _ = fedClient.Close() }()

	// Configure spy to return one held workitem for the petition.
	entrySpy := &entryClientSpy{
		listSuspendedResp: []*flowv1.SuspendedWorkitemInfo{
			{WorkitemId: "wi-held-001", ResumeCondition: `dispute_retired("` + testPetitionID1 + `")`},
		},
	}
	entryClient := newEntryTestClient(t, entrySpy)

	tracker := internal.NewPendingTracker()

	stream, err := fedClient.SubscribePetitionOutcomes(context.Background(), "test-flow")
	if err != nil {
		t.Fatalf("SubscribePetitionOutcomes() failed: %v", err)
	}

	err = consumeOutcomes(context.Background(), stream, tracker, entryClient)
	if err != nil {
		t.Fatalf("consumeOutcomes() returned error: %v", err)
	}

	// Verify held workitem was resumed on rejection path too.
	resumed := entrySpy.getResumedWorkitemIDs()
	if len(resumed) != 1 || resumed[0] != "wi-held-001" {
		t.Fatalf("expected ResumeWorkitem(wi-held-001), got %v", resumed)
	}

	// Also verify Clerk cycle workitem was still created.
	created := entrySpy.getCreatedWorkitems()
	if len(created) != 1 {
		t.Fatalf("expected 1 CreateWorkitem call, got %d", len(created))
	}
}
