package service

import (
	"context"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

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

func TestFilterByEventType(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Subscribe only to "friction" events.
	stream, err := h.client.Subscribe(ctx, &flowv1.SubscribeRequest{
		Channel: "telemetry",
		Filter:  &flowv1.SubscribeFilter{EventType: "friction"},
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Publish a non-matching event (small delay to let server register subscriber).
	time.Sleep(50 * time.Millisecond)
	h.publish(t, ctx, "telemetry", &flowv1.FlowEvent{
		EventId:   "other-1",
		EventType: "custom",
		Timestamp: timestamppb.Now(),
	})

	// Publish a matching event.
	h.publish(t, ctx, "telemetry", &flowv1.FlowEvent{
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

func TestFilterByLabel(t *testing.T) {
	tests := []struct {
		name           string
		matchLabels    []*flowv1.Label
		noMatchID      string
		noMatchLabels  []*flowv1.Label
		matchID        string
		matchEvtLabels []*flowv1.Label
	}{
		{
			name:        "single label match",
			matchLabels: []*flowv1.Label{{Key: "law_id", Value: "law-2"}},
			noMatchID:   "no-match",
			noMatchLabels: []*flowv1.Label{
				{Key: "law_id", Value: "law-1"},
			},
			matchID: "match",
			matchEvtLabels: []*flowv1.Label{
				{Key: "law_id", Value: "law-1"},
				{Key: "law_id", Value: "law-2"},
				{Key: "law_id", Value: "law-3"},
			},
		},
		{
			name:        "exact value not prefix",
			matchLabels: []*flowv1.Label{{Key: "law_id", Value: "law-1"}},
			noMatchID:   "prefix-only",
			noMatchLabels: []*flowv1.Label{
				{Key: "law_id", Value: "law-10"},
				{Key: "law_id", Value: "law-11"},
			},
			matchID: "exact",
			matchEvtLabels: []*flowv1.Label{
				{Key: "law_id", Value: "law-1"},
			},
		},
		{
			name: "AND semantics — both labels must match",
			matchLabels: []*flowv1.Label{
				{Key: "law_id", Value: "law-1"},
				{Key: "phase", Value: "Running"},
			},
			noMatchID: "partial",
			noMatchLabels: []*flowv1.Label{
				{Key: "law_id", Value: "law-1"},
				{Key: "phase", Value: "Pending"},
			},
			matchID: "full",
			matchEvtLabels: []*flowv1.Label{
				{Key: "law_id", Value: "law-1"},
				{Key: "phase", Value: "Running"},
			},
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
				Channel: "telemetry",
				Filter: &flowv1.SubscribeFilter{
					MatchLabels: tt.matchLabels,
				},
			})
			if err != nil {
				t.Fatalf("Subscribe: %v", err)
			}

			time.Sleep(50 * time.Millisecond)
			h.publish(t, ctx, "telemetry", &flowv1.FlowEvent{
				EventId:   tt.noMatchID,
				EventType: "friction",
				Timestamp: timestamppb.Now(),
				Labels:    tt.noMatchLabels,
			})

			h.publish(t, ctx, "telemetry", &flowv1.FlowEvent{
				EventId:   tt.matchID,
				EventType: "friction",
				Timestamp: timestamppb.Now(),
				Labels:    tt.matchEvtLabels,
			})

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
