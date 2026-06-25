package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc"
)

// Test constants for law IDs used across multiple tests.
const (
	testLawID10 = "law-10"
	testLawID20 = "law-20"
)

// ---------------------------------------------------------------------------
// Tests — extractLawID
// ---------------------------------------------------------------------------

func TestExtractLawID_Found(t *testing.T) {
	evt := makeThresholdEvent("evt-1", "law-42")
	got := extractLawID(evt)
	if got != "law-42" {
		t.Fatalf("expected law-42, got %q", got)
	}
}

func TestExtractLawID_Missing(t *testing.T) {
	evt := makeEventNoLawID("evt-2")
	got := extractLawID(evt)
	if got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

func TestExtractLawID_MultipleLabels(t *testing.T) {
	evt := &flowv1.FlowEvent{
		EventId:   "evt-3",
		EventType: eventType,
		Channel:   channel,
		Labels: []*flowv1.Label{
			{Key: "phase", Value: "active"},
			{Key: "law_id", Value: "law-99"},
			{Key: "tier", Value: "1"},
		},
	}
	got := extractLawID(evt)
	if got != "law-99" {
		t.Fatalf("expected law-99, got %q", got)
	}
}

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
// Tests — consumeEvents (integration with spy servers)
// ---------------------------------------------------------------------------

// setupEntryTestClient creates spy gRPC servers for Operator and Event Bus,
// starts them, and returns an EntryClient connected to them.
func setupEntryTestClient(
	t *testing.T,
	operatorSpy *spyOperator,
	eventBusSpy *spyEventBus,
) *flow.EntryClient {
	t.Helper()

	var opAddr, ebAddr string

	if operatorSpy != nil {
		opLis := newTestListener(t)
		opAddr = opLis.Addr().String()
		srv := grpc.NewServer()
		flowv1.RegisterOperatorServiceServer(srv, operatorSpy)
		go func() { _ = srv.Serve(opLis) }()
		t.Cleanup(func() { srv.GracefulStop() })
	}

	if eventBusSpy != nil {
		ebLis := newTestListener(t)
		ebAddr = ebLis.Addr().String()
		srv := grpc.NewServer()
		flowv1.RegisterFlowEventBusServiceServer(srv, eventBusSpy)
		go func() { _ = srv.Serve(ebLis) }()
		t.Cleanup(func() { srv.GracefulStop() })
	}

	ec, err := flow.NewEntryClientForTest(opAddr, ebAddr)
	if err != nil {
		t.Fatalf("NewEntryClientForTest() failed: %v", err)
	}
	t.Cleanup(func() { _ = ec.Close() })

	return ec
}

func TestConsumeEvents_CreatesWorkitems(t *testing.T) {
	opSpy := &spyOperator{returnID: "wi-hearing-001"}
	ebSpy := &spyEventBus{
		events: []*flowv1.FlowEvent{
			makeThresholdEvent("evt-1", testLawID10),
			makeThresholdEvent("evt-2", testLawID20),
		},
	}

	ec := setupEntryTestClient(t, opSpy, ebSpy)
	tracker := internal.NewPendingTracker()

	stream, err := ec.Subscribe(context.Background(), channel, eventType)
	if err != nil {
		t.Fatalf("Subscribe() failed: %v", err)
	}

	// consumeEvents should process both events and return nil (EOF).
	err = consumeEvents(context.Background(), ec, stream, tracker)
	if err != nil {
		t.Fatalf("consumeEvents() returned error: %v", err)
	}

	// Verify two CreateWorkitem calls.
	calls := opSpy.getCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 CreateWorkitem calls, got %d", len(calls))
	}

	if calls[0].GetMetadata()["law_id"] != testLawID10 {
		t.Errorf("first call law_id: expected %s, got %q", testLawID10, calls[0].GetMetadata()["law_id"])
	}
	if calls[1].GetMetadata()["law_id"] != testLawID20 {
		t.Errorf("second call law_id: expected %s, got %q", testLawID20, calls[1].GetMetadata()["law_id"])
	}
}

func TestConsumeEvents_DeduplicatesSameLawID(t *testing.T) {
	opSpy := &spyOperator{returnID: "wi-hearing-001"}
	ebSpy := &spyEventBus{
		events: []*flowv1.FlowEvent{
			makeThresholdEvent("evt-1", testLawID10),
			makeThresholdEvent("evt-2", testLawID10), // duplicate law_id
			makeThresholdEvent("evt-3", testLawID20), // different law_id
		},
	}

	ec := setupEntryTestClient(t, opSpy, ebSpy)
	tracker := internal.NewPendingTracker()

	stream, err := ec.Subscribe(context.Background(), channel, eventType)
	if err != nil {
		t.Fatalf("Subscribe() failed: %v", err)
	}

	err = consumeEvents(context.Background(), ec, stream, tracker)
	if err != nil {
		t.Fatalf("consumeEvents() returned error: %v", err)
	}

	// Only 2 calls: law-10 (first occurrence) and law-20.
	calls := opSpy.getCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 CreateWorkitem calls (dedup), got %d", len(calls))
	}
	if calls[0].GetMetadata()["law_id"] != testLawID10 {
		t.Errorf("first call law_id: expected %s, got %q", testLawID10, calls[0].GetMetadata()["law_id"])
	}
	if calls[1].GetMetadata()["law_id"] != testLawID20 {
		t.Errorf("second call law_id: expected %s, got %q", testLawID20, calls[1].GetMetadata()["law_id"])
	}
}

func TestConsumeEvents_SkipsMissingLawID(t *testing.T) {
	opSpy := &spyOperator{returnID: "wi-hearing-001"}
	ebSpy := &spyEventBus{
		events: []*flowv1.FlowEvent{
			makeEventNoLawID("evt-1"),                // no law_id — skipped
			makeThresholdEvent("evt-2", testLawID10), // valid
		},
	}

	ec := setupEntryTestClient(t, opSpy, ebSpy)
	tracker := internal.NewPendingTracker()

	stream, err := ec.Subscribe(context.Background(), channel, eventType)
	if err != nil {
		t.Fatalf("Subscribe() failed: %v", err)
	}

	err = consumeEvents(context.Background(), ec, stream, tracker)
	if err != nil {
		t.Fatalf("consumeEvents() returned error: %v", err)
	}

	// Only 1 call (the event without law_id was skipped).
	calls := opSpy.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 CreateWorkitem call, got %d", len(calls))
	}
	if calls[0].GetMetadata()["law_id"] != testLawID10 {
		t.Errorf("call law_id: expected %s, got %q", testLawID10, calls[0].GetMetadata()["law_id"])
	}
}

func TestConsumeEvents_CreateWorkitemError_ClearsPending(t *testing.T) {
	opSpy := &spyOperator{returnErr: fmt.Errorf("permission denied")}
	ebSpy := &spyEventBus{
		events: []*flowv1.FlowEvent{
			makeThresholdEvent("evt-1", testLawID10),
		},
	}

	ec := setupEntryTestClient(t, opSpy, ebSpy)
	tracker := internal.NewPendingTracker()

	stream, err := ec.Subscribe(context.Background(), channel, eventType)
	if err != nil {
		t.Fatalf("Subscribe() failed: %v", err)
	}

	err = consumeEvents(context.Background(), ec, stream, tracker)
	if err != nil {
		t.Fatalf("consumeEvents() returned error: %v", err)
	}

	// After error, pending should be cleared — re-mark should succeed.
	if !tracker.MarkPending(testLawID10) {
		t.Fatal("expected law-10 to be cleared from pending after CreateWorkitem error")
	}
}

func TestConsumeEvents_ContextCancelled(t *testing.T) {
	// Event bus that sends one event, then the context is cancelled.
	ebSpy := &spyEventBus{
		events: []*flowv1.FlowEvent{
			makeThresholdEvent("evt-1", testLawID10),
		},
	}

	ec := setupEntryTestClient(t, &spyOperator{returnID: "wi-1"}, ebSpy)
	tracker := internal.NewPendingTracker()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	stream, err := ec.Subscribe(ctx, channel, eventType)
	if err != nil {
		// Subscribe may fail immediately on cancelled context, that's fine.
		return
	}

	err = consumeEvents(ctx, ec, stream, tracker)
	if err == nil {
		t.Fatal("expected error from consumeEvents on cancelled context")
	}
}

// ---------------------------------------------------------------------------
// Tests — processHearing (handler logic via spy client)
// ---------------------------------------------------------------------------

func TestProcessHearing_Success(t *testing.T) {
	spy := &handlerSpy{}
	client := newHandlerTestClient(t, spy)

	wctx := &flowv1.WorkitemContext{
		FlowNamespace: "test-ns",
		WorkitemId:    "wi-hearing-001",
		NodeId:        "friction-watcher",
		Metadata:      map[string]string{"law_id": "law-42"},
	}

	err := processHearing(context.Background(), client, wctx)
	if err != nil {
		t.Fatalf("processHearing() returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// Verify heartbeat was sent.
	if spy.heartbeatCount != 1 {
		t.Errorf("expected 1 heartbeat, got %d", spy.heartbeatCount)
	}

	// Verify law-reference artefact was stored.
	if len(spy.storedArtefacts) != 1 {
		t.Fatalf("expected 1 stored artefact, got %d", len(spy.storedArtefacts))
	}
	stored := spy.storedArtefacts[0]
	if stored.GetArtefactId() != "law-reference" {
		t.Errorf("expected artefact_id=law-reference, got %q", stored.GetArtefactId())
	}
	if stored.GetGovernedArtefact() != "law-reference" {
		t.Errorf("expected governed_artefact=law-reference, got %q", stored.GetGovernedArtefact())
	}
	if string(stored.GetContent()) != "law-42" {
		t.Errorf("expected content=law-42, got %q", string(stored.GetContent()))
	}

	// Verify routed to "default" output.
	if len(spy.routedOutputs) != 1 {
		t.Fatalf("expected 1 routed output, got %d", len(spy.routedOutputs))
	}
	if spy.routedOutputs[0] != "default" {
		t.Errorf("expected route target=default, got %q", spy.routedOutputs[0])
	}
}

func TestProcessHearing_MissingLawID(t *testing.T) {
	spy := &handlerSpy{}
	client := newHandlerTestClient(t, spy)

	wctx := &flowv1.WorkitemContext{
		FlowNamespace: "test-ns",
		WorkitemId:    "wi-hearing-002",
		NodeId:        "friction-watcher",
		Metadata:      map[string]string{}, // no law_id
	}

	err := processHearing(context.Background(), client, wctx)
	if err == nil {
		t.Fatal("expected error for missing law_id, got nil")
	}
	if !strings.Contains(err.Error(), "missing law_id") {
		t.Fatalf("expected 'missing law_id' in error, got: %v", err)
	}

	// No operations should have been performed.
	spy.mu.Lock()
	defer spy.mu.Unlock()
	if spy.heartbeatCount != 0 {
		t.Errorf("expected 0 heartbeats on error path, got %d", spy.heartbeatCount)
	}
}

func TestProcessHearing_NilMetadata(t *testing.T) {
	spy := &handlerSpy{}
	client := newHandlerTestClient(t, spy)

	wctx := &flowv1.WorkitemContext{
		FlowNamespace: "test-ns",
		WorkitemId:    "wi-hearing-003",
		NodeId:        "friction-watcher",
		// No Metadata field set at all.
	}

	err := processHearing(context.Background(), client, wctx)
	if err == nil {
		t.Fatal("expected error for nil metadata, got nil")
	}
	if !strings.Contains(err.Error(), "missing law_id") {
		t.Fatalf("expected 'missing law_id' in error, got: %v", err)
	}
}
