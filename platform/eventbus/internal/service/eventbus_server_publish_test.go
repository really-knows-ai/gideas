package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestMultiSubscriberFanOut(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start two subscribers.
	stream1, err := h.client.Subscribe(ctx, &flowv1.SubscribeRequest{
		Channel: "telemetry",
	})
	if err != nil {
		t.Fatalf("Subscribe 1: %v", err)
	}
	stream2, err := h.client.Subscribe(ctx, &flowv1.SubscribeRequest{
		Channel: "telemetry",
	})
	if err != nil {
		t.Fatalf("Subscribe 2: %v", err)
	}

	// Publish one event (small delay to let server register subscribers).
	time.Sleep(50 * time.Millisecond)
	h.publish(t, ctx, "telemetry", &flowv1.FlowEvent{
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

func TestChannelIsolation(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Subscribe to audit channel.
	stream, err := h.client.Subscribe(ctx, &flowv1.SubscribeRequest{
		Channel: "audit",
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Publish to telemetry channel — audit subscriber should NOT receive it.
	time.Sleep(50 * time.Millisecond)
	h.publish(t, ctx, "telemetry", &flowv1.FlowEvent{
		EventId:   "wrong-channel",
		EventType: "test",
		Timestamp: timestamppb.Now(),
	})

	// Publish to audit channel — subscriber should receive it.
	h.publish(t, ctx, "audit", &flowv1.FlowEvent{
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
		Channel: "telemetry",
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
			Channel: "telemetry",
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

func TestLiveOnlyWhenLastSequenceZero(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Publish 2 events before subscribing.
	for i := 1; i <= 2; i++ {
		h.publish(t, ctx, "telemetry", &flowv1.FlowEvent{
			EventId:   fmt.Sprintf("pre-%d", i),
			EventType: "test",
			Timestamp: timestamppb.Now(),
		})
	}

	// Subscribe with last_sequence=0 (live-only).
	stream, err := h.client.Subscribe(ctx, &flowv1.SubscribeRequest{
		Channel:      "telemetry",
		LastSequence: 0,
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Publish a live event (small delay to let server register subscriber).
	time.Sleep(50 * time.Millisecond)
	h.publish(t, ctx, "telemetry", &flowv1.FlowEvent{
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
