package buffer

import (
	"context"
	"sync"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/sidecar/internal/proxy"
	"google.golang.org/grpc"
)

// spyEventBusClient implements FlowEventBusServiceClient for testing.
type spyEventBusClient struct {
	flowv1.FlowEventBusServiceClient

	mu           sync.Mutex
	publishCalls []*flowv1.PublishRequest
	publishErr   error
}

func (s *spyEventBusClient) Publish(
	_ context.Context, req *flowv1.PublishRequest, _ ...grpc.CallOption,
) (*flowv1.PublishResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publishCalls = append(s.publishCalls, req)
	if s.publishErr != nil {
		return nil, s.publishErr
	}
	return &flowv1.PublishResponse{Acknowledged: true, Sequence: uint64(len(s.publishCalls))}, nil
}

func (s *spyEventBusClient) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.publishCalls)
}

func TestTelemetryBuffer_SubmitAndDrain(t *testing.T) {
	spy := &spyEventBusClient{}
	ebProxy := proxy.NewEventBusProxyFromClient(spy)
	tb := NewTelemetryBuffer(ebProxy, 10)
	defer tb.Stop()

	tb.Submit(Event{
		Priority:   PriorityNormal,
		FlowID:     "flow-1",
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
}

func TestTelemetryBuffer_PriorityOrdering(t *testing.T) {
	spy := &spyEventBusClient{}
	ebProxy := proxy.NewEventBusProxyFromClient(spy)

	// Create buffer but don't start draining yet — use a large buffer.
	tb := &TelemetryBuffer{
		ebProxy:  ebProxy,
		highCh:   make(chan Event, 10),
		normalCh: make(chan Event, 10),
		stopCh:   make(chan struct{}),
	}

	// Queue normal first, then high.
	tb.Submit(Event{
		Priority:  PriorityNormal,
		FlowID:    "flow-n",
		EventType: "normal-event",
	})
	tb.Submit(Event{
		Priority:  PriorityHigh,
		FlowID:    "flow-h",
		Magnitude: 1.0,
	})

	// Now start drain loop.
	tb.wg.Add(1)
	go tb.drainLoop()

	// Wait for both to drain.
	deadline := time.Now().Add(2 * time.Second)
	for spy.calls() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	tb.Stop()

	if spy.calls() < 2 {
		t.Fatalf("expected 2 publish calls, got %d", spy.calls())
	}

	// First call should be the HIGH priority (friction) event.
	spy.mu.Lock()
	first := spy.publishCalls[0]
	spy.mu.Unlock()
	if first.GetEvent().GetEventType() != "friction" {
		t.Fatalf("expected first event to be friction (HIGH priority), got %q", first.GetEvent().GetEventType())
	}
}

func TestTelemetryBuffer_DropNormalWhenFull(t *testing.T) {
	spy := &spyEventBusClient{}
	ebProxy := proxy.NewEventBusProxyFromClient(spy)

	// Create a buffer with size 1 but don't start draining.
	tb := &TelemetryBuffer{
		ebProxy:  ebProxy,
		highCh:   make(chan Event, 1),
		normalCh: make(chan Event, 1),
		stopCh:   make(chan struct{}),
	}

	// Fill the normal buffer.
	tb.Submit(Event{Priority: PriorityNormal, EventType: "first"})
	// This should be dropped.
	tb.Submit(Event{Priority: PriorityNormal, EventType: "second"})

	if tb.DroppedNormal() != 1 {
		t.Fatalf("expected 1 dropped normal event, got %d", tb.DroppedNormal())
	}

	// High priority should still work.
	tb.Submit(Event{Priority: PriorityHigh, FlowID: "flow-h"})
	if tb.DroppedHigh() != 0 {
		t.Fatalf("expected 0 dropped high events, got %d", tb.DroppedHigh())
	}

	// Second high should be dropped.
	tb.Submit(Event{Priority: PriorityHigh, FlowID: "flow-h2"})
	if tb.DroppedHigh() != 1 {
		t.Fatalf("expected 1 dropped high event, got %d", tb.DroppedHigh())
	}
}

func TestTelemetryBuffer_NonBlocking(t *testing.T) {
	spy := &spyEventBusClient{}
	ebProxy := proxy.NewEventBusProxyFromClient(spy)

	// Use size 1 buffer with no drain goroutine.
	tb := &TelemetryBuffer{
		ebProxy:  ebProxy,
		highCh:   make(chan Event, 1),
		normalCh: make(chan Event, 1),
		stopCh:   make(chan struct{}),
	}

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
}
