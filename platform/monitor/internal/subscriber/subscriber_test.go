package subscriber

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// --- In-Memory Checkpoint ---

type memCheckpoint struct {
	mu   sync.Mutex
	data map[string]uint64
}

func newMemCheckpoint() *memCheckpoint {
	return &memCheckpoint{data: make(map[string]uint64)}
}

func (m *memCheckpoint) Get(channel string) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.data[channel], nil
}

func (m *memCheckpoint) Set(channel string, seq uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[channel] = seq
	return nil
}

// --- Mock Event Bus ---

type mockEventBus struct {
	flowv1.UnimplementedFlowEventBusServiceServer

	mu          sync.Mutex
	published   []*flowv1.PublishRequest
	subscribers []mockSubscriber
	seq         uint64
}

type mockSubscriber struct {
	ch      chan *flowv1.FlowEvent
	channel string
	filter  *flowv1.SubscribeFilter
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
	for _, sub := range m.subscribers {
		if sub.channel != req.GetChannel() {
			continue
		}
		select {
		case sub.ch <- evt:
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
	m.subscribers = append(m.subscribers, mockSubscriber{
		ch:      ch,
		channel: req.GetChannel(),
		filter:  req.GetFilter(),
	})
	m.mu.Unlock()

	for {
		select {
		case evt := <-ch:
			if err := stream.Send(evt); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return stream.Context().Err()
		}
	}
}

// --- Test Helpers ---

type testHarness struct {
	mockBus   *mockEventBus
	busClient flowv1.FlowEventBusServiceClient
	busGRPC   *grpc.Server
	busConn   *grpc.ClientConn
	cp        *memCheckpoint
}

func newTestHarness(t *testing.T) *testHarness {
	t.Helper()

	mock := &mockEventBus{}
	srv := grpc.NewServer()
	flowv1.RegisterFlowEventBusServiceServer(srv, mock)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	t.Cleanup(func() {
		_ = conn.Close()
		srv.Stop()
	})

	return &testHarness{
		mockBus:   mock,
		busClient: flowv1.NewFlowEventBusServiceClient(conn),
		busGRPC:   srv,
		busConn:   conn,
		cp:        newMemCheckpoint(),
	}
}

func (h *testHarness) publishTelemetry(
	t *testing.T,
	ctx context.Context,
	eventID, eventType string,
	attrs map[string]string,
) {
	t.Helper()
	_, err := h.busClient.Publish(ctx, &flowv1.PublishRequest{
		Channel: "telemetry",
		Event: &flowv1.FlowEvent{
			EventId:    eventID,
			EventType:  eventType,
			FlowId:     "flow-1",
			NodeId:     "node-a",
			WorkitemId: "wi-1",
			Timestamp:  timestamppb.Now(),
			Attributes: attrs,
		},
	})
	if err != nil {
		t.Fatalf("publishTelemetry %s: %v", eventID, err)
	}
}

func (h *testHarness) publishAudit(
	t *testing.T,
	ctx context.Context,
	eventID, eventType string,
	attrs map[string]string,
) {
	t.Helper()
	_, err := h.busClient.Publish(ctx, &flowv1.PublishRequest{
		Channel: "audit",
		Event: &flowv1.FlowEvent{
			EventId:    eventID,
			EventType:  eventType,
			FlowId:     "flow-1",
			NodeId:     "node-a",
			WorkitemId: "wi-1",
			Timestamp:  timestamppb.Now(),
			Attributes: attrs,
		},
	})
	if err != nil {
		t.Fatalf("publishAudit %s: %v", eventID, err)
	}
}

func waitForCheckpoint(t *testing.T, cp *memCheckpoint, channel string, minSeq uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		seq, _ := cp.Get(channel)
		if seq >= minSeq {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s checkpoint >= %d", channel, minSeq)
}

// --- Telemetry Subscriber Tests ---

func TestTelemetrySubscriber_ProcessesFrictionEvents(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	sub := NewTelemetrySubscriber(h.busClient, h.cp)
	sub.Start()
	defer sub.Stop()

	time.Sleep(100 * time.Millisecond)

	h.publishTelemetry(t, ctx, "evt-1", "friction", map[string]string{
		"law_ids":   "law-1",
		"magnitude": "10.5",
	})
	h.publishTelemetry(t, ctx, "evt-2", "friction", map[string]string{
		"law_ids":   "law-1,law-2",
		"magnitude": "5.0",
	})

	waitForCheckpoint(t, h.cp, "telemetry", 2)

	// Verify checkpoint was updated.
	seq, _ := h.cp.Get("telemetry")
	if seq < 2 {
		t.Errorf("expected checkpoint >= 2, got %d", seq)
	}
}

func TestTelemetrySubscriber_ProcessesCustomEvents(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	sub := NewTelemetrySubscriber(h.busClient, h.cp)
	sub.Start()
	defer sub.Stop()

	time.Sleep(100 * time.Millisecond)

	h.publishTelemetry(t, ctx, "evt-1", "foundry.cost.llm", map[string]string{
		"model": "gpt-4",
		"cost":  "0.03",
	})

	waitForCheckpoint(t, h.cp, "telemetry", 1)
}

func TestTelemetrySubscriber_StopsCleanly(t *testing.T) {
	h := newTestHarness(t)

	sub := NewTelemetrySubscriber(h.busClient, h.cp)
	sub.Start()
	time.Sleep(100 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		sub.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() hung for > 5s")
	}
}

// --- Audit Subscriber Tests ---

func TestAuditSubscriber_ProcessesAuditEvents(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	sub := NewAuditSubscriber(h.busClient, h.cp)
	sub.Start()
	defer sub.Stop()

	time.Sleep(100 * time.Millisecond)

	h.publishAudit(t, ctx, "audit-1", "audit.artefact.version_created", map[string]string{
		"action":      "version_created",
		"resource_id": "art-1",
	})

	waitForCheckpoint(t, h.cp, "audit", 1)

	seq, _ := h.cp.Get("audit")
	if seq < 1 {
		t.Errorf("expected checkpoint >= 1, got %d", seq)
	}
}

func TestAuditSubscriber_StopsCleanly(t *testing.T) {
	h := newTestHarness(t)

	sub := NewAuditSubscriber(h.busClient, h.cp)
	sub.Start()
	time.Sleep(100 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		sub.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() hung for > 5s")
	}
}

// --- Checkpoint Tests ---

func TestFileCheckpoint_PersistAndReload(t *testing.T) {
	path := t.TempDir() + "/checkpoint.json"

	cp1, err := NewFileCheckpoint(path)
	if err != nil {
		t.Fatalf("NewFileCheckpoint: %v", err)
	}

	// Initial value should be 0.
	seq, _ := cp1.Get("telemetry")
	if seq != 0 {
		t.Errorf("expected initial seq 0, got %d", seq)
	}

	// Set and reload.
	if err := cp1.Set("telemetry", 42); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := cp1.Set("audit", 17); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Reload from same file.
	cp2, err := NewFileCheckpoint(path)
	if err != nil {
		t.Fatalf("NewFileCheckpoint reload: %v", err)
	}

	seq, _ = cp2.Get("telemetry")
	if seq != 42 {
		t.Errorf("expected telemetry seq 42, got %d", seq)
	}
	seq, _ = cp2.Get("audit")
	if seq != 17 {
		t.Errorf("expected audit seq 17, got %d", seq)
	}
}

func TestFileCheckpoint_MissingFile(t *testing.T) {
	path := t.TempDir() + "/nonexistent.json"

	cp, err := NewFileCheckpoint(path)
	if err != nil {
		t.Fatalf("NewFileCheckpoint: %v", err)
	}

	seq, _ := cp.Get("telemetry")
	if seq != 0 {
		t.Errorf("expected 0 for missing checkpoint, got %d", seq)
	}
}

// --- Integration Test ---

func TestTelemetryAndAudit_Concurrent(t *testing.T) {
	h := newTestHarness(t)
	ctx := context.Background()

	telSub := NewTelemetrySubscriber(h.busClient, h.cp)
	telSub.Start()
	defer telSub.Stop()

	auditSub := NewAuditSubscriber(h.busClient, h.cp)
	auditSub.Start()
	defer auditSub.Stop()

	time.Sleep(100 * time.Millisecond)

	// Publish to both channels.
	for i := range 5 {
		h.publishTelemetry(t, ctx, fmt.Sprintf("tel-%d", i), "friction", map[string]string{
			"law_ids":   "law-1",
			"magnitude": "1.0",
		})
		h.publishAudit(t, ctx, fmt.Sprintf("aud-%d", i), "audit.workitem.completed", nil)
	}

	waitForCheckpoint(t, h.cp, "telemetry", 5)
	waitForCheckpoint(t, h.cp, "audit", 5)
}
