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

// --- Event Bus Subscription Integration Tests ---

func TestSubscription_ProcessesFrictionEvents(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h.ledgerServer.StartSubscription(h.eventBusClient)

	// Give subscription time to establish.
	time.Sleep(100 * time.Millisecond)

	h.publishFriction(t, ctx, "evt-1", "law-1", 10.0)
	h.publishFriction(t, ctx, "evt-2", "law-1", 15.5)

	h.waitForCheckpoint(t, 2)

	resp, err := h.ledgerClient.QueryFriction(ctx, &flowv1.QueryFrictionRequest{
		Filter: &flowv1.FrictionFilter{LawId: "law-1"},
	})
	if err != nil {
		t.Fatalf("QueryFriction: %v", err)
	}

	aggs := resp.GetFrictionAggregates()
	if len(aggs) != 1 {
		t.Fatalf("expected 1 aggregate, got %d", len(aggs))
	}
	if aggs[0].GetTotalMagnitude() != 25.5 {
		t.Errorf("expected total 25.5, got %f", aggs[0].GetTotalMagnitude())
	}
	if aggs[0].GetEventCount() != 2 {
		t.Errorf("expected count 2, got %d", aggs[0].GetEventCount())
	}
}

func TestSubscription_MultipleLaws(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h.ledgerServer.StartSubscription(h.eventBusClient)
	time.Sleep(100 * time.Millisecond)

	h.publishFriction(t, ctx, "evt-1", "law-1,law-2", 10.0)

	h.waitForCheckpoint(t, 1)

	resp, err := h.ledgerClient.QueryFriction(ctx, &flowv1.QueryFrictionRequest{})
	if err != nil {
		t.Fatalf("QueryFriction: %v", err)
	}

	aggs := resp.GetFrictionAggregates()
	if len(aggs) != 2 {
		t.Fatalf("expected 2 aggregates (one per law), got %d", len(aggs))
	}
}

func TestSubscription_CheckpointPersistence(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h.ledgerServer.StartSubscription(h.eventBusClient)
	time.Sleep(100 * time.Millisecond)

	for i := 1; i <= 3; i++ {
		h.publishFriction(t, ctx,
			fmt.Sprintf("evt-%d", i),
			"law-1", float64(i)*10,
		)
	}

	h.waitForCheckpoint(t, 3)

	seq, err := h.store.GetCheckpoint(ctx, checkpointChannel)
	if err != nil {
		t.Fatalf("GetCheckpoint: %v", err)
	}
	if seq != 3 {
		t.Errorf("expected checkpoint 3, got %d", seq)
	}
}

// --- Threshold Tests ---

func TestThresholdCrossing_PublishesEvent(t *testing.T) {
	thresholds := ThresholdConfig{
		int32(flowv1.LawTier_LAW_TIER_FINDING): 20.0,
	}

	h := newTestHarness(t, thresholds)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h.ledgerServer.StartSubscription(h.eventBusClient)
	time.Sleep(100 * time.Millisecond)

	// Publish friction that exceeds threshold (10 + 15 = 25 > 20).
	h.publishFriction(t, ctx, "evt-1", "law-1", 10.0)
	h.publishFriction(t, ctx, "evt-2", "law-1", 15.0)

	h.waitForCheckpoint(t, 2)

	// Give threshold evaluator time to publish.
	time.Sleep(200 * time.Millisecond)

	// Check published events on friction channel.
	pubs := h.mockBus.publishedOnFrictionChannel()
	if len(pubs) != 1 {
		t.Fatalf("expected 1 friction channel publish, got %d", len(pubs))
	}

	evt := pubs[0].GetEvent()
	if evt.GetEventType() != "friction.threshold_crossed" {
		t.Errorf("event_type = %q, want %q", evt.GetEventType(), "friction.threshold_crossed")
	}
	// law_id should be in labels now.
	var lawID string
	for _, lbl := range evt.GetLabels() {
		if lbl.GetKey() == "law_id" {
			lawID = lbl.GetValue()
			break
		}
	}
	if lawID != "law-1" {
		t.Errorf("law_id label = %q, want %q", lawID, "law-1")
	}
	if evt.GetAttributes()["tier"] != "1" {
		t.Errorf("tier = %q, want %q", evt.GetAttributes()["tier"], "1")
	}
}

func TestThresholdCrossing_NoDuplicate(t *testing.T) {
	thresholds := ThresholdConfig{
		int32(flowv1.LawTier_LAW_TIER_FINDING): 10.0,
	}

	h := newTestHarness(t, thresholds)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h.ledgerServer.StartSubscription(h.eventBusClient)
	time.Sleep(100 * time.Millisecond)

	// Both events exceed threshold.
	h.publishFriction(t, ctx, "evt-1", "law-1", 15.0)
	h.publishFriction(t, ctx, "evt-2", "law-1", 20.0)

	h.waitForCheckpoint(t, 2)
	time.Sleep(200 * time.Millisecond)

	// Should have exactly one threshold event.
	pubs := h.mockBus.publishedOnFrictionChannel()
	if len(pubs) != 1 {
		t.Fatalf("expected exactly 1 friction channel publish (no duplicate), got %d", len(pubs))
	}
}

func TestThresholdCrossing_BelowThreshold(t *testing.T) {
	thresholds := ThresholdConfig{
		int32(flowv1.LawTier_LAW_TIER_FINDING): 100.0,
	}

	h := newTestHarness(t, thresholds)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h.ledgerServer.StartSubscription(h.eventBusClient)
	time.Sleep(100 * time.Millisecond)

	h.publishFriction(t, ctx, "evt-1", "law-1", 5.0)

	h.waitForCheckpoint(t, 1)
	time.Sleep(200 * time.Millisecond)

	pubs := h.mockBus.publishedOnFrictionChannel()
	if len(pubs) != 0 {
		t.Fatalf("expected 0 friction channel publishes, got %d", len(pubs))
	}
}

func TestThresholdCrossing_MultipleTiers(t *testing.T) {
	thresholds := ThresholdConfig{
		int32(flowv1.LawTier_LAW_TIER_FINDING): 10.0,
		int32(flowv1.LawTier_LAW_TIER_RULING):  25.0,
	}

	h := newTestHarness(t, thresholds)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h.ledgerServer.StartSubscription(h.eventBusClient)
	time.Sleep(100 * time.Millisecond)

	// Push friction to cross both thresholds.
	h.publishFriction(t, ctx, "evt-1", "law-1", 30.0)

	h.waitForCheckpoint(t, 1)
	time.Sleep(200 * time.Millisecond)

	// Should have two threshold events (one per tier).
	pubs := h.mockBus.publishedOnFrictionChannel()
	if len(pubs) != 2 {
		t.Fatalf("expected 2 friction channel publishes (one per tier), got %d", len(pubs))
	}
}

// --- Reconnection Test ---

func TestSubscription_StopsCleanly(t *testing.T) {
	h := newTestHarness(t, nil)

	h.ledgerServer.StartSubscription(h.eventBusClient)
	time.Sleep(100 * time.Millisecond)

	// Stop should complete without hanging.
	done := make(chan struct{})
	go func() {
		h.ledgerServer.Stop()
		close(done)
	}()

	select {
	case <-done:
		// OK.
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() hung for > 5s")
	}
}

// --- Error Handling Test ---

func TestProcessEvent_InvalidMagnitude(t *testing.T) {
	h := newTestHarness(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h.ledgerServer.StartSubscription(h.eventBusClient)
	time.Sleep(100 * time.Millisecond)

	// Publish event with non-numeric magnitude.
	_, err := h.eventBusClient.Publish(ctx, &flowv1.PublishRequest{
		Channel: "telemetry",
		Event: &flowv1.FlowEvent{
			EventId:       "bad-1",
			EventType:     "friction",
			FlowNamespace: "flow-1",
			Timestamp:     timestamppb.Now(),
			Attributes:    map[string]string{"magnitude": "not-a-number"},
		},
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	// Publish a valid event after the bad one.
	h.publishFriction(t, ctx, "good-1", "law-1", 5.0)

	// The good event should still be processed despite the bad one.
	h.waitForCheckpoint(t, 2)

	resp, err := h.ledgerClient.QueryFriction(ctx, &flowv1.QueryFrictionRequest{
		Filter: &flowv1.FrictionFilter{LawId: "law-1"},
	})
	if err != nil {
		t.Fatalf("QueryFriction: %v", err)
	}

	aggs := resp.GetFrictionAggregates()
	if len(aggs) != 1 {
		t.Fatalf("expected 1 aggregate, got %d", len(aggs))
	}
	if aggs[0].GetTotalMagnitude() != 5.0 {
		t.Errorf("expected 5.0, got %f", aggs[0].GetTotalMagnitude())
	}
}

// Silence unused import warning for status and codes packages.
var (
	_ = status.Code
	_ = codes.OK
)
