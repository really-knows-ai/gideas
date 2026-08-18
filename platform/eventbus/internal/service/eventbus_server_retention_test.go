package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestSequenceExpired(t *testing.T) {
	// Configure retention so eviction actually runs.
	retention := map[string]RetentionConfig{
		"telemetry": {Duration: 1 * time.Hour},
	}
	h := newTestHarness(t, retention)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Publish 3 events with timestamps 3 hours ago.
	for i := 1; i <= 3; i++ {
		h.publish(t, ctx, "telemetry", &flowv1.FlowEvent{
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
	h.publish(t, ctx, "telemetry", &flowv1.FlowEvent{
		EventId:   "recent",
		EventType: "test",
		Timestamp: timestamppb.Now(),
	})

	// min is now 4 (the recent event). Requesting replay from 1 should fail.
	stream, err := h.client.Subscribe(ctx, &flowv1.SubscribeRequest{
		Channel:      "telemetry",
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

func TestRetentionEviction(t *testing.T) {
	retention := map[string]RetentionConfig{
		"telemetry": {Duration: 1 * time.Hour},
	}
	h := newTestHarness(t, retention)
	ctx := context.Background()

	// Publish old events (timestamps 2 hours ago).
	for i := 1; i <= 5; i++ {
		h.publish(t, ctx, "telemetry", &flowv1.FlowEvent{
			EventId:   fmt.Sprintf("old-%d", i),
			EventType: "test",
			Timestamp: timestamppb.New(time.Now().Add(-2 * time.Hour)),
		})
	}

	// Run eviction.
	h.server.runEviction()

	// Verify all old events were evicted by checking the store directly.
	events, err := h.store.GetSince(ctx, "telemetry", 0, 100)
	if err != nil {
		t.Fatalf("GetSince: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events after eviction, got %d", len(events))
	}
}
