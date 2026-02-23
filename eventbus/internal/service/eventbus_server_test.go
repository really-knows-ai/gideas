package service

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gideas/flow/eventbus/internal/store/sqlite"
	flowv1 "github.com/gideas/flow/gen/flow/v1"
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

func newTestHarness(t *testing.T, retention map[int32]RetentionConfig) *testHarness {
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
	ch flowv1.EventChannel,
	evt *flowv1.FlowEvent,
) *flowv1.PublishResponse {
	t.Helper()
	resp, err := h.client.Publish(ctx, &flowv1.PublishRequest{
		Channel: ch,
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
		Channel: flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY,
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Publish an event (small delay to let server register subscriber).
	time.Sleep(50 * time.Millisecond)
	resp := h.publish(t, ctx, flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY, &flowv1.FlowEvent{
		EventId:    "test-1",
		EventType:  "friction",
		FlowId:     "flow-1",
		NodeId:     "node-1",
		WorkitemId: "wi-1",
		Timestamp:  timestamppb.Now(),
		Attributes: map[string]string{"law_ids": "law-1", "magnitude": "5.0"},
		Payload:    []byte("data"),
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
				Channel: flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY,
			},
			code: codes.InvalidArgument,
		},
		{
			name: "missing event_type",
			req: &flowv1.PublishRequest{
				Channel: flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY,
				Event:   &flowv1.FlowEvent{},
			},
			code: codes.InvalidArgument,
		},
		{
			name: "payload too large",
			req: &flowv1.PublishRequest{
				Channel: flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY,
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

func TestSubscribeValidation(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := h.client.Subscribe(ctx, &flowv1.SubscribeRequest{})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected error for missing channel")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", st.Code())
	}
}

func TestReplayFromSequence(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Publish 5 events.
	for i := 1; i <= 5; i++ {
		h.publish(t, ctx, flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY, &flowv1.FlowEvent{
			EventId:   fmt.Sprintf("evt-%d", i),
			EventType: "friction",
			Timestamp: timestamppb.Now(),
		})
	}

	// Subscribe with replay from sequence 3.
	stream, err := h.client.Subscribe(ctx, &flowv1.SubscribeRequest{
		Channel:      flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY,
		LastSequence: 3,
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Should receive events 4 and 5.
	received := make([]*flowv1.FlowEvent, 0, 2)
	for range 2 {
		evt, err := stream.Recv()
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		received = append(received, evt)
	}

	if len(received) != 2 {
		t.Fatalf("received %d events, want 2", len(received))
	}
	if received[0].GetSequence() != 4 {
		t.Errorf("first replayed sequence = %d, want 4", received[0].GetSequence())
	}
	if received[1].GetSequence() != 5 {
		t.Errorf("second replayed sequence = %d, want 5", received[1].GetSequence())
	}
}

func TestMultiSubscriberFanOut(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start two subscribers.
	stream1, err := h.client.Subscribe(ctx, &flowv1.SubscribeRequest{
		Channel: flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY,
	})
	if err != nil {
		t.Fatalf("Subscribe 1: %v", err)
	}
	stream2, err := h.client.Subscribe(ctx, &flowv1.SubscribeRequest{
		Channel: flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY,
	})
	if err != nil {
		t.Fatalf("Subscribe 2: %v", err)
	}

	// Publish one event (small delay to let server register subscribers).
	time.Sleep(50 * time.Millisecond)
	h.publish(t, ctx, flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY, &flowv1.FlowEvent{
		EventId:   "fanout-1",
		EventType: "test",
		Timestamp: timestamppb.Now(),
	})

	// Both subscribers should receive it.
	evt1, err := stream1.Recv()
	if err != nil {
		t.Fatalf("Recv stream1: %v", err)
	}
	evt2, err := stream2.Recv()
	if err != nil {
		t.Fatalf("Recv stream2: %v", err)
	}

	if evt1.GetEventId() != "fanout-1" || evt2.GetEventId() != "fanout-1" {
		t.Error("both subscribers should receive the same event")
	}
}

func TestFilterByEventType(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Subscribe only to "friction" events.
	stream, err := h.client.Subscribe(ctx, &flowv1.SubscribeRequest{
		Channel: flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY,
		Filter:  &flowv1.SubscribeFilter{EventType: "friction"},
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Publish a non-matching event (small delay to let server register subscriber).
	time.Sleep(50 * time.Millisecond)
	h.publish(t, ctx, flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY, &flowv1.FlowEvent{
		EventId:   "other-1",
		EventType: "custom",
		Timestamp: timestamppb.Now(),
	})

	// Publish a matching event.
	h.publish(t, ctx, flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY, &flowv1.FlowEvent{
		EventId:   "friction-1",
		EventType: "friction",
		Timestamp: timestamppb.Now(),
	})

	// Should receive only the friction event.
	evt, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if evt.GetEventId() != "friction-1" {
		t.Errorf("EventId = %q, want %q", evt.GetEventId(), "friction-1")
	}
}

func TestFilterByLawID(t *testing.T) {
	tests := []struct {
		name        string
		filterLawID string
		noMatchID   string
		noMatchLaws string
		matchID     string
		matchLaws   string
	}{
		{
			name:        "element in CSV list",
			filterLawID: "law-2",
			noMatchID:   "no-match",
			noMatchLaws: "law-1",
			matchID:     "match",
			matchLaws:   "law-1,law-2,law-3",
		},
		{
			name:        "exact element not prefix",
			filterLawID: "law-1",
			noMatchID:   "prefix-only",
			noMatchLaws: "law-10,law-11",
			matchID:     "exact",
			matchLaws:   "law-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHarness(t, nil)
			ctx, cancel := context.WithTimeout(
				context.Background(), 5*time.Second,
			)
			defer cancel()

			stream, err := h.client.Subscribe(ctx, &flowv1.SubscribeRequest{
				Channel: flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY,
				Filter:  &flowv1.SubscribeFilter{LawId: tt.filterLawID},
			})
			if err != nil {
				t.Fatalf("Subscribe: %v", err)
			}

			time.Sleep(50 * time.Millisecond)
			h.publish(t, ctx,
				flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY,
				&flowv1.FlowEvent{
					EventId:   tt.noMatchID,
					EventType: "friction",
					Timestamp: timestamppb.Now(),
					Attributes: map[string]string{
						"law_ids": tt.noMatchLaws,
					},
				},
			)

			h.publish(t, ctx,
				flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY,
				&flowv1.FlowEvent{
					EventId:   tt.matchID,
					EventType: "friction",
					Timestamp: timestamppb.Now(),
					Attributes: map[string]string{
						"law_ids": tt.matchLaws,
					},
				},
			)

			evt, err := stream.Recv()
			if err != nil {
				t.Fatalf("Recv: %v", err)
			}
			if evt.GetEventId() != tt.matchID {
				t.Errorf("EventId = %q, want %q",
					evt.GetEventId(), tt.matchID)
			}
		})
	}
}

func TestSequenceExpired(t *testing.T) {
	// Configure retention so eviction actually runs.
	retention := map[int32]RetentionConfig{
		int32(flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY): {
			Duration: 1 * time.Hour,
		},
	}
	h := newTestHarness(t, retention)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Publish 3 events with timestamps 3 hours ago.
	for i := 1; i <= 3; i++ {
		h.publish(t, ctx, flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY, &flowv1.FlowEvent{
			EventId:   fmt.Sprintf("evt-%d", i),
			EventType: "test",
			Timestamp: timestamppb.New(time.Now().Add(-3 * time.Hour)),
		})
	}

	// Evict events older than 1 hour.
	h.server.runEviction()

	// Now try to replay from sequence 1 — should get SEQUENCE_EXPIRED
	// because all events have been evicted and min sequence is now 0
	// (empty). However, if min=0 the check passes. Let's verify by
	// publishing a new event so min > 1.
	h.publish(t, ctx, flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY, &flowv1.FlowEvent{
		EventId:   "recent",
		EventType: "test",
		Timestamp: timestamppb.Now(),
	})

	// min is now 4 (the recent event). Requesting replay from 1 should fail.
	stream, err := h.client.Subscribe(ctx, &flowv1.SubscribeRequest{
		Channel:      flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY,
		LastSequence: 1,
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected error for expired sequence")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.OutOfRange {
		t.Errorf("code = %v, want OutOfRange", st.Code())
	}
}

func TestChannelIsolation(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Subscribe to audit channel.
	stream, err := h.client.Subscribe(ctx, &flowv1.SubscribeRequest{
		Channel: flowv1.EventChannel_EVENT_CHANNEL_AUDIT,
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Publish to telemetry channel — audit subscriber should NOT receive it.
	time.Sleep(50 * time.Millisecond)
	h.publish(t, ctx, flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY, &flowv1.FlowEvent{
		EventId:   "wrong-channel",
		EventType: "test",
		Timestamp: timestamppb.Now(),
	})

	// Publish to audit channel — subscriber should receive it.
	h.publish(t, ctx, flowv1.EventChannel_EVENT_CHANNEL_AUDIT, &flowv1.FlowEvent{
		EventId:   "right-channel",
		EventType: "audit.test",
		Timestamp: timestamppb.Now(),
	})

	evt, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if evt.GetEventId() != "right-channel" {
		t.Errorf("EventId = %q, want %q", evt.GetEventId(), "right-channel")
	}
}

func TestSlowSubscriberDoesNotBlockPublisher(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start a subscriber but do NOT read from it (simulates slow consumer).
	_, err := h.client.Subscribe(ctx, &flowv1.SubscribeRequest{
		Channel: flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY,
	})
	if err != nil {
		t.Fatalf("Subscribe slow: %v", err)
	}

	// Flood the bus with more events than the subscriber buffer size.
	// The key invariant: publishing must complete without blocking,
	// even though the subscriber is not reading.
	time.Sleep(50 * time.Millisecond)
	const total = subscriberBufSize + 100
	for i := range total {
		_, err := h.client.Publish(ctx, &flowv1.PublishRequest{
			Channel: flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY,
			Event: &flowv1.FlowEvent{
				EventId:   fmt.Sprintf("flood-%d", i),
				EventType: "test",
				Timestamp: timestamppb.Now(),
			},
		})
		if err != nil {
			t.Fatalf("Publish %d: %v (slow subscriber blocked publisher)", i, err)
		}
	}
	// If we reach here, the publisher was never blocked. Test passes.
}

func TestAutoGeneratedEventID(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx := context.Background()

	resp, err := h.client.Publish(ctx, &flowv1.PublishRequest{
		Channel: flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY,
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

func TestLiveOnlyWhenLastSequenceZero(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Publish 2 events before subscribing.
	for i := 1; i <= 2; i++ {
		h.publish(t, ctx, flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY, &flowv1.FlowEvent{
			EventId:   fmt.Sprintf("pre-%d", i),
			EventType: "test",
			Timestamp: timestamppb.Now(),
		})
	}

	// Subscribe with last_sequence=0 (live-only).
	stream, err := h.client.Subscribe(ctx, &flowv1.SubscribeRequest{
		Channel:      flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY,
		LastSequence: 0,
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Publish a live event (small delay to let server register subscriber).
	time.Sleep(50 * time.Millisecond)
	h.publish(t, ctx, flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY, &flowv1.FlowEvent{
		EventId:   "live-1",
		EventType: "test",
		Timestamp: timestamppb.Now(),
	})

	// Should receive only the live event, not the pre-published ones.
	evt, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if evt.GetEventId() != "live-1" {
		t.Errorf("EventId = %q, want %q (last_sequence=0 means live-only)", evt.GetEventId(), "live-1")
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
					Channel: flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY,
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
		Channel: flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY,
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
		Channel: flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY,
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

func TestRetentionEviction(t *testing.T) {
	retention := map[int32]RetentionConfig{
		int32(flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY): {
			Duration: 1 * time.Hour,
		},
	}
	h := newTestHarness(t, retention)
	ctx := context.Background()

	// Publish old events (timestamps 2 hours ago).
	for i := 1; i <= 5; i++ {
		h.publish(t, ctx, flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY, &flowv1.FlowEvent{
			EventId:   fmt.Sprintf("old-%d", i),
			EventType: "test",
			Timestamp: timestamppb.New(time.Now().Add(-2 * time.Hour)),
		})
	}

	// Run eviction.
	h.server.runEviction()

	// Verify all old events were evicted by checking the store directly.
	events, err := h.store.GetSince(ctx, int32(flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY), 0, 100)
	if err != nil {
		t.Fatalf("GetSince: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events after eviction, got %d", len(events))
	}
}

func TestReplayAllFromSequenceOne(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Publish 3 events.
	for i := 1; i <= 3; i++ {
		h.publish(t, ctx, flowv1.EventChannel_EVENT_CHANNEL_AUDIT, &flowv1.FlowEvent{
			EventId:   fmt.Sprintf("audit-%d", i),
			EventType: "audit.test",
			Timestamp: timestamppb.Now(),
		})
	}

	// Subscribe with last_sequence=0 but on purpose request a replay
	// that captures everything by using sequence 0 (which means live).
	// Instead, use last_sequence=1 to replay from after first event.
	stream, err := h.client.Subscribe(ctx, &flowv1.SubscribeRequest{
		Channel:      flowv1.EventChannel_EVENT_CHANNEL_AUDIT,
		LastSequence: 1,
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Should get events 2 and 3 via replay.
	for _, wantSeq := range []uint64{2, 3} {
		evt, err := stream.Recv()
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if evt.GetSequence() != wantSeq {
			t.Errorf("Sequence = %d, want %d", evt.GetSequence(), wantSeq)
		}
	}
}
