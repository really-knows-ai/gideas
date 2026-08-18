package service

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/foundry/flow/frictionledger/internal/store/sqlite"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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
		if p.GetChannel() == "friction" {
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
	// Build law_id labels from comma-separated string.
	var labels []*flowv1.Label
	if lawIDs != "" {
		for id := range strings.SplitSeq(lawIDs, ",") {
			labels = append(labels, &flowv1.Label{Key: "law_id", Value: id})
		}
	}

	_, err := h.eventBusClient.Publish(ctx, &flowv1.PublishRequest{
		Channel: "telemetry",
		Event: &flowv1.FlowEvent{
			EventId:       eventID,
			EventType:     "friction",
			FlowNamespace: "flow-1",
			NodeId:        "node-a",
			WorkitemId:    "wi-1",
			Timestamp:     timestamppb.Now(),
			Labels:        labels,
			Attributes: map[string]string{
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
