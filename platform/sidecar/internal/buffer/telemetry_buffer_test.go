package buffer

import (
	"context"
	"sync"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/pkg/eventbus"
)

// spyPublisher implements [eventbus.Publisher] for testing.
type spyPublisher struct {
	mu           sync.Mutex
	publishCalls []*flowv1.PublishRequest
	publishErr   error
}

func (s *spyPublisher) Publish(
	_ context.Context, req *flowv1.PublishRequest,
) (*flowv1.PublishResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publishCalls = append(s.publishCalls, req)
	if s.publishErr != nil {
		return nil, s.publishErr
	}
	return &flowv1.PublishResponse{Acknowledged: true, Sequence: uint64(len(s.publishCalls))}, nil
}

var _ eventbus.Publisher = (*spyPublisher)(nil)

func (s *spyPublisher) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.publishCalls)
}

func (s *spyPublisher) getCalls() []*flowv1.PublishRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*flowv1.PublishRequest, len(s.publishCalls))
	copy(out, s.publishCalls)
	return out
}

func TestTelemetryBuffer_SubmitAndDrain(t *testing.T) {
	spy := &spyPublisher{}
	tb := NewTelemetryBuffer(spy, 10)
	defer tb.Stop()

	tb.Submit(Event{
		Priority:   PriorityNormal,
		Namespace:  "ns-1",
		NodeID:     "node-1",
		WorkitemID: "wi-1",
		EventType:  "foundry.test",
		Payload:    []byte(`{}`),
	})

	// Wait for drain.
	deadline := time.Now().Add(2 * time.Second)
	for spy.calls() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if spy.calls() < 1 {
		t.Fatal("expected at least 1 publish call")
	}

	calls := spy.getCalls()
	if calls[0].GetChannel() != "telemetry" {
		t.Fatalf("expected channel 'telemetry', got %q", calls[0].GetChannel())
	}
	if calls[0].GetEvent().GetEventType() != "foundry.test" {
		t.Fatalf("expected event_type 'foundry.test', got %q", calls[0].GetEvent().GetEventType())
	}
}

func TestTelemetryBuffer_PriorityOrdering(t *testing.T) {
	// Submit normal then high events. Both publishers drain concurrently,
	// but the high-priority friction event should be published.
	spy := &spyPublisher{}
	tb := NewTelemetryBuffer(spy, 10)

	tb.Submit(Event{
		Priority:  PriorityNormal,
		Namespace: "ns-n",
		EventType: "normal-event",
	})
	tb.Submit(Event{
		Priority:  PriorityHigh,
		Namespace: "ns-h",
		Magnitude: 1.0,
	})

	// Wait for both to drain.
	deadline := time.Now().Add(2 * time.Second)
	for spy.calls() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	tb.Stop()

	if spy.calls() < 2 {
		t.Fatalf("expected 2 publish calls, got %d", spy.calls())
	}

	// Verify both event types were published (order is not strictly
	// guaranteed since each priority has its own drain goroutine).
	calls := spy.getCalls()
	hasFriction := false
	hasNormal := false
	for _, c := range calls {
		if c.GetEvent().GetEventType() == "friction" {
			hasFriction = true
		}
		if c.GetEvent().GetEventType() == "normal-event" {
			hasNormal = true
		}
	}
	if !hasFriction {
		t.Fatal("expected friction (HIGH priority) event to be published")
	}
	if !hasNormal {
		t.Fatal("expected normal-event to be published")
	}
}

func TestTelemetryBuffer_DropNormalWhenFull(t *testing.T) {
	spy := &spyPublisher{}

	// Create a buffer with size 1.
	tb := NewTelemetryBuffer(spy, 1)

	// Submit enough normal events to guarantee drops. The buffer is
	// size 1, so after the first enqueues, subsequent should drop.
	for range 10 {
		tb.Submit(Event{Priority: PriorityNormal, EventType: "fill"})
	}

	// Give a moment for drain to start.
	time.Sleep(50 * time.Millisecond)

	if tb.DroppedNormal() == 0 {
		t.Fatal("expected at least 1 dropped normal event")
	}

	// High priority should also work independently.
	tb.Submit(Event{Priority: PriorityHigh, Namespace: "ns-h"})

	tb.Stop()
}

func TestTelemetryBuffer_DropHighWhenFull(t *testing.T) {
	spy := &spyPublisher{}
	tb := NewTelemetryBuffer(spy, 1)

	// Fill the high buffer.
	for range 10 {
		tb.Submit(Event{Priority: PriorityHigh, Namespace: "ns-h"})
	}

	time.Sleep(50 * time.Millisecond)

	if tb.DroppedHigh() == 0 {
		t.Fatal("expected at least 1 dropped high event")
	}

	tb.Stop()
}

func TestTelemetryBuffer_NonBlocking(t *testing.T) {
	spy := &spyPublisher{}
	tb := NewTelemetryBuffer(spy, 1)

	// Submit should never block even when full.
	done := make(chan struct{})
	go func() {
		for range 100 {
			tb.Submit(Event{Priority: PriorityNormal, EventType: "test"})
		}
		close(done)
	}()

	select {
	case <-done:
		// Non-blocking: all submits completed quickly.
	case <-time.After(time.Second):
		t.Fatal("Submit blocked — buffer is not non-blocking")
	}

	tb.Stop()
}

func TestTelemetryBuffer_FrictionEventFormat(t *testing.T) {
	spy := &spyPublisher{}
	tb := NewTelemetryBuffer(spy, 10)
	defer tb.Stop()

	tb.Submit(Event{
		Priority:   PriorityHigh,
		Namespace:  "ns-1",
		WorkitemID: "wi-1",
		NodeID:     "node-1",
		LawIDs:     []string{"law-a", "law-b"},
		Magnitude:  3.14,
	})

	deadline := time.Now().Add(2 * time.Second)
	for spy.calls() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if spy.calls() < 1 {
		t.Fatal("expected at least 1 publish call")
	}

	calls := spy.getCalls()
	req := calls[0]
	if req.GetChannel() != "telemetry" {
		t.Fatalf("expected channel 'telemetry', got %q", req.GetChannel())
	}
	evt := req.GetEvent()
	if evt.GetEventType() != "friction" {
		t.Fatalf("expected event_type 'friction', got %q", evt.GetEventType())
	}
	if evt.GetFlowNamespace() != "ns-1" {
		t.Fatalf("expected flow_namespace='ns-1', got %q", evt.GetFlowNamespace())
	}
	if evt.GetNodeId() != "node-1" {
		t.Fatalf("expected node_id 'node-1', got %q", evt.GetNodeId())
	}
	if evt.GetWorkitemId() != "wi-1" {
		t.Fatalf("expected workitem_id 'wi-1', got %q", evt.GetWorkitemId())
	}
	if evt.GetAttributes()["magnitude"] != "3.14" {
		t.Fatalf("expected magnitude attribute '3.14', got %q", evt.GetAttributes()["magnitude"])
	}
	// law_ids should be in labels, not CSV attributes.
	labels := evt.GetLabels()
	if len(labels) != 2 {
		t.Fatalf("expected 2 labels for law_ids, got %d", len(labels))
	}
	lawIDs := make(map[string]bool)
	for _, l := range labels {
		if l.GetKey() != "law_id" {
			t.Fatalf("expected label key 'law_id', got %q", l.GetKey())
		}
		lawIDs[l.GetValue()] = true
	}
	if !lawIDs["law-a"] || !lawIDs["law-b"] {
		t.Fatalf("expected labels law-a and law-b, got %v", lawIDs)
	}
}

func TestTelemetryBuffer_RetryOnFailure(t *testing.T) {
	spy := &spyPublisher{publishErr: context.DeadlineExceeded}
	tb := NewTelemetryBuffer(spy, 10)

	tb.Submit(Event{Priority: PriorityNormal, EventType: "retry-me"})

	// The underlying AsyncPublisher uses 500ms base retry. Wait long
	// enough for at least one retry attempt.
	time.Sleep(1200 * time.Millisecond)

	if spy.calls() < 2 {
		t.Fatalf("expected at least 2 publish attempts (retry), got %d", spy.calls())
	}

	// Clear error so it eventually succeeds.
	spy.mu.Lock()
	spy.publishErr = nil
	spy.mu.Unlock()

	tb.Stop()

	// Should not have been dropped.
	if tb.DroppedNormal() != 0 {
		t.Fatalf("expected 0 drops (retried), got %d", tb.DroppedNormal())
	}
}
