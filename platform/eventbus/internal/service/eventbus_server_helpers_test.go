package service

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/foundry/flow/eventbus/internal/store/sqlite"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// testHarness spins up an in-process gRPC server with the Event Bus.
type testHarness struct {
	client  flowv1.FlowEventBusServiceClient
	server  *EventBusServer
	store   *sqlite.Store
	grpcSrv *grpc.Server
	conn    *grpc.ClientConn
}

func newTestHarness(t *testing.T, retention map[string]RetentionConfig) *testHarness {
	t.Helper()

	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	seq := 0
	idGen := func() string {
		seq++
		return fmt.Sprintf("auto-%d", seq)
	}

	srv := NewEventBusServer(store, idGen, retention)

	grpcSrv := grpc.NewServer()
	flowv1.RegisterFlowEventBusServiceServer(grpcSrv, srv)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	go func() { _ = grpcSrv.Serve(lis) }()

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	client := flowv1.NewFlowEventBusServiceClient(conn)

	t.Cleanup(func() {
		srv.Stop()
		_ = conn.Close()
		grpcSrv.Stop()
		_ = store.Close()
	})

	return &testHarness{
		client:  client,
		server:  srv,
		store:   store,
		grpcSrv: grpcSrv,
		conn:    conn,
	}
}

// publish is a test helper that publishes and fails the test on error.
func (h *testHarness) publish(
	t *testing.T,
	ctx context.Context,
	channel string,
	evt *flowv1.FlowEvent,
) *flowv1.PublishResponse {
	t.Helper()
	resp, err := h.client.Publish(ctx, &flowv1.PublishRequest{
		Channel: channel,
		Event:   evt,
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	return resp
}

func TestPublishAndReceive(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start subscriber before publishing.
	stream, err := h.client.Subscribe(ctx, &flowv1.SubscribeRequest{
		Channel: "telemetry",
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Publish an event (small delay to let server register subscriber).
	time.Sleep(50 * time.Millisecond)
	resp := h.publish(t, ctx, "telemetry", &flowv1.FlowEvent{
		EventId:       "test-1",
		EventType:     "friction",
		FlowNamespace: "flow-1",
		NodeId:        "node-1",
		WorkitemId:    "wi-1",
		Timestamp:     timestamppb.Now(),
		Attributes:    map[string]string{"magnitude": "5.0"},
		Labels: []*flowv1.Label{
			{Key: "law_id", Value: "law-1"},
		},
		Payload: []byte("data"),
	})
	if !resp.GetAcknowledged() {
		t.Error("expected acknowledged=true")
	}
	if resp.GetSequence() != 1 {
		t.Errorf("sequence = %d, want 1", resp.GetSequence())
	}

	// Receive from subscriber.
	evt, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if evt.GetEventId() != "test-1" {
		t.Errorf("EventId = %q, want %q", evt.GetEventId(), "test-1")
	}
	if evt.GetSequence() != 1 {
		t.Errorf("Sequence = %d, want 1", evt.GetSequence())
	}
	if evt.GetAttributes()["magnitude"] != "5.0" {
		t.Errorf("magnitude = %q, want %q", evt.GetAttributes()["magnitude"], "5.0")
	}
	if evt.GetChannel() != "telemetry" {
		t.Errorf("Channel = %q, want %q", evt.GetChannel(), "telemetry")
	}
	// Verify labels round-trip.
	if len(evt.GetLabels()) != 1 {
		t.Fatalf("Labels count = %d, want 1", len(evt.GetLabels()))
	}
	if evt.GetLabels()[0].GetKey() != "law_id" || evt.GetLabels()[0].GetValue() != "law-1" {
		t.Errorf("Labels[0] = %+v, want {law_id, law-1}", evt.GetLabels()[0])
	}
}

func TestPublishValidation(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx := context.Background()

	tests := []struct {
		name string
		req  *flowv1.PublishRequest
		code codes.Code
	}{
		{
			name: "missing channel",
			req: &flowv1.PublishRequest{
				Event: &flowv1.FlowEvent{EventType: "test"},
			},
			code: codes.InvalidArgument,
		},
		{
			name: "missing event",
			req: &flowv1.PublishRequest{
				Channel: "telemetry",
			},
			code: codes.InvalidArgument,
		},
		{
			name: "missing event_type",
			req: &flowv1.PublishRequest{
				Channel: "telemetry",
				Event:   &flowv1.FlowEvent{},
			},
			code: codes.InvalidArgument,
		},
		{
			name: "payload too large",
			req: &flowv1.PublishRequest{
				Channel: "telemetry",
				Event: &flowv1.FlowEvent{
					EventType: "test",
					Payload:   make([]byte, 65*1024),
				},
			},
			code: codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := h.client.Publish(ctx, tt.req)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("expected gRPC status error, got %v", err)
			}
			if st.Code() != tt.code {
				t.Errorf("code = %v, want %v", st.Code(), tt.code)
			}
		})
	}
}

func TestAutoGeneratedEventID(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx := context.Background()

	resp, err := h.client.Publish(ctx, &flowv1.PublishRequest{
		Channel: "telemetry",
		Event: &flowv1.FlowEvent{
			EventType: "test",
			Timestamp: timestamppb.Now(),
		},
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Error("expected acknowledged")
	}
}

func TestConcurrentPublish(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx := context.Background()

	const goroutines = 10
	const perGoroutine = 20

	var wg sync.WaitGroup
	errs := make(chan error, goroutines*perGoroutine)

	for g := range goroutines {
		wg.Go(func() {
			for i := range perGoroutine {
				_, err := h.client.Publish(ctx, &flowv1.PublishRequest{
					Channel: "telemetry",
					Event: &flowv1.FlowEvent{
						EventId:   fmt.Sprintf("g%d-e%d", g, i),
						EventType: "test",
						Timestamp: timestamppb.Now(),
					},
				})
				if err != nil {
					errs <- err
				}
			}
		})
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent publish error: %v", err)
	}
}

func TestSubscriberCleanup(t *testing.T) {
	h := newTestHarness(t, nil)

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := h.client.Subscribe(ctx, &flowv1.SubscribeRequest{
		Channel: "telemetry",
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Cancel the context which should trigger cleanup.
	cancel()

	// Recv should return an error.
	_, err = stream.Recv()
	if err == nil {
		t.Error("expected error after context cancellation")
	}

	// Publishing after cleanup should still work (no panic).
	pubCtx := context.Background()
	_, err = h.client.Publish(pubCtx, &flowv1.PublishRequest{
		Channel: "telemetry",
		Event: &flowv1.FlowEvent{
			EventId:   "after-cleanup",
			EventType: "test",
			Timestamp: timestamppb.Now(),
		},
	})
	if err != nil {
		t.Fatalf("Publish after cleanup: %v", err)
	}
}
