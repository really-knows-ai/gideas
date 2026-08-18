package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestReplayFromSequence(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Publish 5 events.
	for i := 1; i <= 5; i++ {
		h.publish(t, ctx, "telemetry", &flowv1.FlowEvent{
			EventId:   fmt.Sprintf("evt-%d", i),
			EventType: "friction",
			Timestamp: timestamppb.Now(),
		})
	}

	// Subscribe with replay from sequence 3.
	stream, err := h.client.Subscribe(ctx, &flowv1.SubscribeRequest{
		Channel:      "telemetry",
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

func TestReplayAllFromSequenceOne(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Publish 3 events.
	for i := 1; i <= 3; i++ {
		h.publish(t, ctx, "audit", &flowv1.FlowEvent{
			EventId:   fmt.Sprintf("audit-%d", i),
			EventType: "audit.test",
			Timestamp: timestamppb.Now(),
		})
	}

	// Subscribe with last_sequence=1 to replay from after first event.
	stream, err := h.client.Subscribe(ctx, &flowv1.SubscribeRequest{
		Channel:      "audit",
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

func TestReplayWithLabelFilter(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Publish events with different labels.
	h.publish(t, ctx, "workitem", &flowv1.FlowEvent{
		EventId:   "wi-1",
		EventType: "workitem.phase_changed",
		Timestamp: timestamppb.Now(),
		Labels: []*flowv1.Label{
			{Key: "parent_workitem_id", Value: "parent-A"},
			{Key: "phase", Value: "Running"},
		},
	})
	h.publish(t, ctx, "workitem", &flowv1.FlowEvent{
		EventId:   "wi-2",
		EventType: "workitem.phase_changed",
		Timestamp: timestamppb.Now(),
		Labels: []*flowv1.Label{
			{Key: "parent_workitem_id", Value: "parent-B"},
			{Key: "phase", Value: "Running"},
		},
	})
	h.publish(t, ctx, "workitem", &flowv1.FlowEvent{
		EventId:   "wi-3",
		EventType: "workitem.phase_changed",
		Timestamp: timestamppb.Now(),
		Labels: []*flowv1.Label{
			{Key: "parent_workitem_id", Value: "parent-A"},
			{Key: "phase", Value: "Completed"},
		},
	})

	// Replay with label filter for parent-A only.
	stream, err := h.client.Subscribe(ctx, &flowv1.SubscribeRequest{
		Channel:      "workitem",
		LastSequence: 0, // live-only won't replay; use 1-based trick below
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	// Cancel this stream since we need replay.
	_ = stream.CloseSend()

	// Now subscribe with replay and filter.
	replayStream, err := h.client.Subscribe(ctx, &flowv1.SubscribeRequest{
		Channel: "workitem",
		Filter: &flowv1.SubscribeFilter{
			MatchLabels: []*flowv1.Label{
				{Key: "parent_workitem_id", Value: "parent-A"},
			},
		},
		LastSequence: 1, // replay from after the first sequence (0 means live-only)
	})
	if err != nil {
		t.Fatalf("Subscribe replay: %v", err)
	}

	// Should receive only wi-3 (parent-A, after sequence 1).
	// wi-2 is parent-B (filtered out), wi-1 is sequence 1 (before cursor).
	evt, err := replayStream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if evt.GetEventId() != "wi-3" {
		t.Errorf("EventId = %q, want %q", evt.GetEventId(), "wi-3")
	}
}
