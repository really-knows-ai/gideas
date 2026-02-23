package service

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gideas/flow/frictionledger/internal/store/sqlite"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// --- Mock Event Bus ---

// mockEventBus implements FlowEventBusServiceServer for testing.
type mockEventBus struct {
	flowv1.UnimplementedFlowEventBusServiceServer

	mu          sync.Mutex
	published   []*flowv1.PublishRequest
	subscribers []chan *flowv1.FlowEvent
	seq         uint64
}

func (m *mockEventBus) Publish(_ context.Context, req *flowv1.PublishRequest) (*flowv1.PublishResponse, error) {
	m.mu.Lock()
	m.seq++
	seq := m.seq
	m.published = append(m.published, req)
	evt := req.GetEvent()
	if evt != nil {
		evt.Sequence = seq
		evt.Channel = req.GetChannel()
	}
	// Fan out to subscribers.
	for _, ch := range m.subscribers {
		select {
		case ch <- evt:
		default:
		}
	}
	m.mu.Unlock()
	return &flowv1.PublishResponse{Acknowledged: true, Sequence: seq}, nil
}

func (m *mockEventBus) Subscribe(
	req *flowv1.SubscribeRequest,
	stream flowv1.FlowEventBusService_SubscribeServer,
) error {
	ch := make(chan *flowv1.FlowEvent, 256)
	m.mu.Lock()
	m.subscribers = append(m.subscribers, ch)
	m.mu.Unlock()

	filter := req.GetFilter()

	for {
		select {
		case evt := <-ch:
			// Apply channel filter.
			if evt.GetChannel() != req.GetChannel() {
				continue
			}
			// Apply event type filter.
			if filter != nil && filter.GetEventType() != "" && evt.GetEventType() != filter.GetEventType() {
				continue
			}
			if err := stream.Send(evt); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}

func (m *mockEventBus) publishedOnFrictionChannel() []*flowv1.PublishRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*flowv1.PublishRequest
	for _, p := range m.published {
		if p.GetChannel() == flowv1.EventChannel_EVENT_CHANNEL_FRICTION {
			result = append(result, p)
		}
	}
	return result
}

// --- Test Harness ---

type testHarness struct {
	ledgerServer *FrictionLedgerServer
	ledgerClient flowv1.FrictionLedgerServiceClient

	eventBusClient flowv1.FlowEventBusServiceClient
	mockBus        *mockEventBus

	ledgerGRPC *grpc.Server
	busGRPC    *grpc.Server
	ledgerConn *grpc.ClientConn
	busConn    *grpc.ClientConn

	store *sqlite.Store
}

func newTestHarness(t *testing.T, thresholds ThresholdConfig) *testHarness {
	t.Helper()

	// --- Mock Event Bus (in-process gRPC) ---
	mockBus := &mockEventBus{}
	busGRPC := grpc.NewServer()
	flowv1.RegisterFlowEventBusServiceServer(busGRPC, mockBus)

	busLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bus listen: %v", err)
	}
	go func() { _ = busGRPC.Serve(busLis) }()

	busConn, err := grpc.NewClient(busLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("bus dial: %v", err)
	}
	eventBusClient := flowv1.NewFlowEventBusServiceClient(busConn)

	// --- Friction Ledger ---
	ledgerStore, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("new ledger store: %v", err)
	}

	ledgerSeq := 0
	ledgerIDGen := func() string {
		ledgerSeq++
		return fmt.Sprintf("ledger-auto-%d", ledgerSeq)
	}

	ledgerSrv := NewFrictionLedgerServer(ledgerStore, ledgerIDGen, thresholds)

	ledgerGRPC := grpc.NewServer()
	flowv1.RegisterFrictionLedgerServiceServer(ledgerGRPC, ledgerSrv)

	ledgerLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ledger listen: %v", err)
	}
	go func() { _ = ledgerGRPC.Serve(ledgerLis) }()

	ledgerConn, err := grpc.NewClient(ledgerLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("ledger dial: %v", err)
	}
	ledgerClient := flowv1.NewFrictionLedgerServiceClient(ledgerConn)

	t.Cleanup(func() {
		ledgerSrv.Stop()
		_ = ledgerConn.Close()
		_ = busConn.Close()
		ledgerGRPC.Stop()
		busGRPC.Stop()
		_ = ledgerStore.Close()
	})

	return &testHarness{
		ledgerServer:   ledgerSrv,
		ledgerClient:   ledgerClient,
		eventBusClient: eventBusClient,
		mockBus:        mockBus,
		ledgerGRPC:     ledgerGRPC,
		busGRPC:        busGRPC,
		ledgerConn:     ledgerConn,
		busConn:        busConn,
		store:          ledgerStore,
	}
}

// publishFriction publishes a friction event to the mock Event Bus telemetry channel.
func (h *testHarness) publishFriction(
	t *testing.T,
	ctx context.Context,
	eventID string,
	lawIDs string,
	magnitude float64,
) {
	t.Helper()
	_, err := h.eventBusClient.Publish(ctx, &flowv1.PublishRequest{
		Channel: flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY,
		Event: &flowv1.FlowEvent{
			EventId:    eventID,
			EventType:  "friction",
			FlowId:     "flow-1",
			NodeId:     "node-a",
			WorkitemId: "wi-1",
			Timestamp:  timestamppb.Now(),
			Attributes: map[string]string{
				"law_ids":   lawIDs,
				"magnitude": fmt.Sprintf("%g", magnitude),
			},
		},
	})
	if err != nil {
		t.Fatalf("publishFriction %s: %v", eventID, err)
	}
}

// waitForCheckpoint waits for the Friction Ledger to advance its checkpoint.
func (h *testHarness) waitForCheckpoint(t *testing.T, minSeq uint64) {
	t.Helper()
	const timeout = 5 * time.Second
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		seq, err := h.store.GetCheckpoint(context.Background(), checkpointChannel)
		if err != nil {
			t.Fatalf("GetCheckpoint: %v", err)
		}
		if seq >= minSeq {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for checkpoint >= %d", minSeq)
}

// --- QueryFriction Tests ---

func TestQueryFriction_Empty(t *testing.T) {
	h := newTestHarness(t, nil)

	resp, err := h.ledgerClient.QueryFriction(context.Background(),
		&flowv1.QueryFrictionRequest{})
	if err != nil {
		t.Fatalf("QueryFriction: %v", err)
	}
	if len(resp.GetFrictionAggregates()) != 0 {
		t.Fatalf("expected 0 aggregates, got %d", len(resp.GetFrictionAggregates()))
	}
}

func TestQueryFriction_DirectStore(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := h.store.AddFriction(ctx, "evt-1", sqlite.FrictionEvent{
		FlowID: "flow-1", WorkitemID: "wi-1", NodeID: "node-a",
		Magnitude: 10.5, Timestamp: now,
	}, []string{"law-1"}); err != nil {
		t.Fatalf("AddFriction: %v", err)
	}
	if err := h.store.AddFriction(ctx, "evt-2", sqlite.FrictionEvent{
		FlowID: "flow-1", WorkitemID: "wi-1", NodeID: "node-a",
		Magnitude: 20.0, Timestamp: now,
	}, []string{"law-1"}); err != nil {
		t.Fatalf("AddFriction: %v", err)
	}

	resp, err := h.ledgerClient.QueryFriction(ctx, &flowv1.QueryFrictionRequest{
		Filter: &flowv1.FrictionFilter{LawId: "law-1"},
	})
	if err != nil {
		t.Fatalf("QueryFriction: %v", err)
	}

	aggs := resp.GetFrictionAggregates()
	if len(aggs) != 1 {
		t.Fatalf("expected 1 aggregate, got %d", len(aggs))
	}
	if aggs[0].GetTotalMagnitude() != 30.5 {
		t.Errorf("expected total 30.5, got %f", aggs[0].GetTotalMagnitude())
	}
	if aggs[0].GetEventCount() != 2 {
		t.Errorf("expected count 2, got %d", aggs[0].GetEventCount())
	}
}

func TestQueryFriction_FilterByNode(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := h.store.AddFriction(ctx, "evt-1", sqlite.FrictionEvent{
		FlowID: "flow-1", WorkitemID: "wi-1", NodeID: "node-a",
		Magnitude: 10.0, Timestamp: now,
	}, nil); err != nil {
		t.Fatalf("AddFriction: %v", err)
	}
	if err := h.store.AddFriction(ctx, "evt-2", sqlite.FrictionEvent{
		FlowID: "flow-1", WorkitemID: "wi-1", NodeID: "node-b",
		Magnitude: 20.0, Timestamp: now,
	}, nil); err != nil {
		t.Fatalf("AddFriction: %v", err)
	}

	resp, err := h.ledgerClient.QueryFriction(ctx, &flowv1.QueryFrictionRequest{
		Filter: &flowv1.FrictionFilter{NodeId: "node-b"},
	})
	if err != nil {
		t.Fatalf("QueryFriction: %v", err)
	}

	aggs := resp.GetFrictionAggregates()
	if len(aggs) != 1 {
		t.Fatalf("expected 1 aggregate, got %d", len(aggs))
	}
	if aggs[0].GetTotalMagnitude() != 20.0 {
		t.Errorf("expected 20.0, got %f", aggs[0].GetTotalMagnitude())
	}
}

func TestQueryFriction_FilterByTimeRange(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx := context.Background()

	t1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)

	if err := h.store.AddFriction(ctx, "evt-1", sqlite.FrictionEvent{
		FlowID: "flow-1", WorkitemID: "wi-1", NodeID: "node-a",
		Magnitude: 10.0, Timestamp: t1,
	}, nil); err != nil {
		t.Fatalf("AddFriction: %v", err)
	}
	if err := h.store.AddFriction(ctx, "evt-2", sqlite.FrictionEvent{
		FlowID: "flow-1", WorkitemID: "wi-1", NodeID: "node-a",
		Magnitude: 20.0, Timestamp: t2,
	}, nil); err != nil {
		t.Fatalf("AddFriction: %v", err)
	}

	start := t1
	end := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	resp, err := h.ledgerClient.QueryFriction(ctx, &flowv1.QueryFrictionRequest{
		Filter: &flowv1.FrictionFilter{
			TimeRange: &flowv1.TimeRange{
				Start: timestamppb.New(start),
				End:   timestamppb.New(end),
			},
		},
	})
	if err != nil {
		t.Fatalf("QueryFriction: %v", err)
	}

	aggs := resp.GetFrictionAggregates()
	if len(aggs) != 1 {
		t.Fatalf("expected 1 aggregate, got %d", len(aggs))
	}
	if aggs[0].GetTotalMagnitude() != 10.0 {
		t.Errorf("expected 10.0, got %f", aggs[0].GetTotalMagnitude())
	}
}

func TestQueryFriction_TimestampsPresent(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx := context.Background()

	if err := h.store.AddFriction(ctx, "evt-1", sqlite.FrictionEvent{
		FlowID: "flow-1", WorkitemID: "wi-1", NodeID: "node-a",
		Magnitude: 10.0, Timestamp: time.Now().UTC(),
	}, nil); err != nil {
		t.Fatalf("AddFriction: %v", err)
	}

	resp, err := h.ledgerClient.QueryFriction(ctx, &flowv1.QueryFrictionRequest{})
	if err != nil {
		t.Fatalf("QueryFriction: %v", err)
	}

	aggs := resp.GetFrictionAggregates()
	if len(aggs) != 1 {
		t.Fatalf("expected 1 aggregate, got %d", len(aggs))
	}
	if aggs[0].GetEarliest() == nil {
		t.Error("expected earliest timestamp to be set")
	}
	if aggs[0].GetLatest() == nil {
		t.Error("expected latest timestamp to be set")
	}
}

// --- Event Bus Subscription Integration Tests ---

func TestSubscription_ProcessesFrictionEvents(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h.ledgerServer.StartSubscription(h.eventBusClient)

	// Give subscription time to establish.
	time.Sleep(100 * time.Millisecond)

	h.publishFriction(t, ctx, "evt-1", "law-1", 10.0)
	h.publishFriction(t, ctx, "evt-2", "law-1", 15.5)

	h.waitForCheckpoint(t, 2)

	resp, err := h.ledgerClient.QueryFriction(ctx, &flowv1.QueryFrictionRequest{
		Filter: &flowv1.FrictionFilter{LawId: "law-1"},
	})
	if err != nil {
		t.Fatalf("QueryFriction: %v", err)
	}

	aggs := resp.GetFrictionAggregates()
	if len(aggs) != 1 {
		t.Fatalf("expected 1 aggregate, got %d", len(aggs))
	}
	if aggs[0].GetTotalMagnitude() != 25.5 {
		t.Errorf("expected total 25.5, got %f", aggs[0].GetTotalMagnitude())
	}
	if aggs[0].GetEventCount() != 2 {
		t.Errorf("expected count 2, got %d", aggs[0].GetEventCount())
	}
}

func TestSubscription_MultipleLaws(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h.ledgerServer.StartSubscription(h.eventBusClient)
	time.Sleep(100 * time.Millisecond)

	h.publishFriction(t, ctx, "evt-1", "law-1,law-2", 10.0)

	h.waitForCheckpoint(t, 1)

	resp, err := h.ledgerClient.QueryFriction(ctx, &flowv1.QueryFrictionRequest{})
	if err != nil {
		t.Fatalf("QueryFriction: %v", err)
	}

	aggs := resp.GetFrictionAggregates()
	if len(aggs) != 2 {
		t.Fatalf("expected 2 aggregates (one per law), got %d", len(aggs))
	}
}

func TestSubscription_CheckpointPersistence(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h.ledgerServer.StartSubscription(h.eventBusClient)
	time.Sleep(100 * time.Millisecond)

	for i := 1; i <= 3; i++ {
		h.publishFriction(t, ctx,
			fmt.Sprintf("evt-%d", i),
			"law-1", float64(i)*10,
		)
	}

	h.waitForCheckpoint(t, 3)

	seq, err := h.store.GetCheckpoint(ctx, checkpointChannel)
	if err != nil {
		t.Fatalf("GetCheckpoint: %v", err)
	}
	if seq != 3 {
		t.Errorf("expected checkpoint 3, got %d", seq)
	}
}

// --- Threshold Tests ---

func TestThresholdCrossing_PublishesEvent(t *testing.T) {
	thresholds := ThresholdConfig{
		int32(flowv1.LawTier_LAW_TIER_FINDING): 20.0,
	}

	h := newTestHarness(t, thresholds)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h.ledgerServer.StartSubscription(h.eventBusClient)
	time.Sleep(100 * time.Millisecond)

	// Publish friction that exceeds threshold (10 + 15 = 25 > 20).
	h.publishFriction(t, ctx, "evt-1", "law-1", 10.0)
	h.publishFriction(t, ctx, "evt-2", "law-1", 15.0)

	h.waitForCheckpoint(t, 2)

	// Give threshold evaluator time to publish.
	time.Sleep(200 * time.Millisecond)

	// Check published events on friction channel.
	pubs := h.mockBus.publishedOnFrictionChannel()
	if len(pubs) != 1 {
		t.Fatalf("expected 1 friction channel publish, got %d", len(pubs))
	}

	evt := pubs[0].GetEvent()
	if evt.GetEventType() != "friction.threshold_crossed" {
		t.Errorf("event_type = %q, want %q", evt.GetEventType(), "friction.threshold_crossed")
	}
	if evt.GetAttributes()["law_id"] != "law-1" {
		t.Errorf("law_id = %q, want %q", evt.GetAttributes()["law_id"], "law-1")
	}
	if evt.GetAttributes()["tier"] != "1" {
		t.Errorf("tier = %q, want %q", evt.GetAttributes()["tier"], "1")
	}
}

func TestThresholdCrossing_NoDuplicate(t *testing.T) {
	thresholds := ThresholdConfig{
		int32(flowv1.LawTier_LAW_TIER_FINDING): 10.0,
	}

	h := newTestHarness(t, thresholds)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h.ledgerServer.StartSubscription(h.eventBusClient)
	time.Sleep(100 * time.Millisecond)

	// Both events exceed threshold.
	h.publishFriction(t, ctx, "evt-1", "law-1", 15.0)
	h.publishFriction(t, ctx, "evt-2", "law-1", 20.0)

	h.waitForCheckpoint(t, 2)
	time.Sleep(200 * time.Millisecond)

	// Should have exactly one threshold event.
	pubs := h.mockBus.publishedOnFrictionChannel()
	if len(pubs) != 1 {
		t.Fatalf("expected exactly 1 friction channel publish (no duplicate), got %d", len(pubs))
	}
}

func TestThresholdCrossing_BelowThreshold(t *testing.T) {
	thresholds := ThresholdConfig{
		int32(flowv1.LawTier_LAW_TIER_FINDING): 100.0,
	}

	h := newTestHarness(t, thresholds)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h.ledgerServer.StartSubscription(h.eventBusClient)
	time.Sleep(100 * time.Millisecond)

	h.publishFriction(t, ctx, "evt-1", "law-1", 5.0)

	h.waitForCheckpoint(t, 1)
	time.Sleep(200 * time.Millisecond)

	pubs := h.mockBus.publishedOnFrictionChannel()
	if len(pubs) != 0 {
		t.Fatalf("expected 0 friction channel publishes, got %d", len(pubs))
	}
}

func TestThresholdCrossing_MultipleTiers(t *testing.T) {
	thresholds := ThresholdConfig{
		int32(flowv1.LawTier_LAW_TIER_FINDING): 10.0,
		int32(flowv1.LawTier_LAW_TIER_RULING):  25.0,
	}

	h := newTestHarness(t, thresholds)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h.ledgerServer.StartSubscription(h.eventBusClient)
	time.Sleep(100 * time.Millisecond)

	// Push friction to cross both thresholds.
	h.publishFriction(t, ctx, "evt-1", "law-1", 30.0)

	h.waitForCheckpoint(t, 1)
	time.Sleep(200 * time.Millisecond)

	// Should have two threshold events (one per tier).
	pubs := h.mockBus.publishedOnFrictionChannel()
	if len(pubs) != 2 {
		t.Fatalf("expected 2 friction channel publishes (one per tier), got %d", len(pubs))
	}
}

// --- Reconnection Test ---

func TestSubscription_StopsCleanly(t *testing.T) {
	h := newTestHarness(t, nil)

	h.ledgerServer.StartSubscription(h.eventBusClient)
	time.Sleep(100 * time.Millisecond)

	// Stop should complete without hanging.
	done := make(chan struct{})
	go func() {
		h.ledgerServer.Stop()
		close(done)
	}()

	select {
	case <-done:
		// OK.
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() hung for > 5s")
	}
}

// --- Error Handling Test ---

func TestProcessEvent_InvalidMagnitude(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h.ledgerServer.StartSubscription(h.eventBusClient)
	time.Sleep(100 * time.Millisecond)

	// Publish event with non-numeric magnitude.
	_, err := h.eventBusClient.Publish(ctx, &flowv1.PublishRequest{
		Channel: flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY,
		Event: &flowv1.FlowEvent{
			EventId:    "bad-1",
			EventType:  "friction",
			FlowId:     "flow-1",
			Timestamp:  timestamppb.Now(),
			Attributes: map[string]string{"magnitude": "not-a-number"},
		},
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Publish a valid event after the bad one.
	h.publishFriction(t, ctx, "good-1", "law-1", 5.0)

	// The good event should still be processed despite the bad one.
	h.waitForCheckpoint(t, 2)

	resp, err := h.ledgerClient.QueryFriction(ctx, &flowv1.QueryFrictionRequest{
		Filter: &flowv1.FrictionFilter{LawId: "law-1"},
	})
	if err != nil {
		t.Fatalf("QueryFriction: %v", err)
	}

	aggs := resp.GetFrictionAggregates()
	if len(aggs) != 1 {
		t.Fatalf("expected 1 aggregate, got %d", len(aggs))
	}
	if aggs[0].GetTotalMagnitude() != 5.0 {
		t.Errorf("expected 5.0, got %f", aggs[0].GetTotalMagnitude())
	}
}

// Silence unused import warning for status and codes packages.
var (
	_ = status.Code
	_ = codes.OK
)
