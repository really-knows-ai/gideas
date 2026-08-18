package flow

import (
	"io"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// ---------------------------------------------------------------------------
// Tests — EntryClient.Subscribe
// ---------------------------------------------------------------------------

func TestEntryClient_Subscribe_RecvEventsAndStop(t *testing.T) {
	events := []*flowv1.FlowEvent{
		{EventId: "evt-1", EventType: "friction.threshold_crossed", Channel: "friction"},
		{EventId: "evt-2", EventType: "friction.threshold_crossed", Channel: "friction"},
	}
	spy := &entrySpyEventBus{events: events}
	ec := setupEntryTestEnv(t, nil, spy)

	stream, err := ec.Subscribe("friction", "friction.threshold_crossed")
	if err != nil {
		t.Fatalf("Subscribe() returned error: %v", err)
	}

	// Read all events.
	var received []*flowv1.FlowEvent
	for {
		evt, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			t.Fatalf("Recv() returned error: %v", recvErr)
		}
		received = append(received, evt)
	}

	if len(received) != 2 {
		t.Fatalf("expected 2 events, got %d", len(received))
	}

	// Stop after reading all events, verify post-stop error.
	stream.Stop()
	_, err = stream.Recv()
	if err == nil {
		t.Fatal("expected error from Recv() after Stop(), got nil")
	}

	// Verify the subscribe request was correct.
	if spy.lastReq.GetChannel() != "friction" {
		t.Fatalf("expected channel=friction, got %q", spy.lastReq.GetChannel())
	}
	if spy.lastReq.GetFilter().GetEventType() != "friction.threshold_crossed" {
		t.Fatalf("expected event_type filter, got %q", spy.lastReq.GetFilter().GetEventType())
	}
}

func TestEntryClient_Subscribe_RecvThenStop(t *testing.T) {
	events := []*flowv1.FlowEvent{
		{EventId: "evt-1", EventType: "test", Channel: "ch"},
	}
	spy := &entrySpyEventBus{events: events}
	ec := setupEntryTestEnv(t, nil, spy)

	stream, err := ec.Subscribe("ch", "test")
	if err != nil {
		t.Fatalf("Subscribe() returned error: %v", err)
	}

	// Read one event.
	evt, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv() first event returned error: %v", err)
	}
	if evt.GetEventId() != "evt-1" {
		t.Fatalf("expected event_id evt-1, got %q", evt.GetEventId())
	}

	// Stop must not panic.
	stream.Stop()
}

func TestEntryClient_Subscribe_NoConnection(t *testing.T) {
	ec := &EntryClient{}
	_, err := ec.Subscribe("friction", "any")
	if err == nil {
		t.Fatal("expected error when no event bus connection, got nil")
	}
}
