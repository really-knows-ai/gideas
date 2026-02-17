package service

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/gideas/flow/monitor/internal/store/sqlite"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"time"
)

// sequentialID returns an IDGenerator that produces sequential IDs.
func sequentialID() IDGenerator {
	var counter atomic.Int64
	return func() string {
		return fmt.Sprintf("evt-%d", counter.Add(1))
	}
}

func newTestServer(t *testing.T) *MonitorServer {
	t.Helper()
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewMonitorServer(store, sequentialID())
}

// --- AddFriction Tests ---

func TestAddFriction_Success(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.AddFriction(ctx, &flowv1.AddFrictionRequest{
		FlowId:     "flow-1",
		WorkitemId: "wi-1",
		NodeId:     "node-a",
		LawIds:     []string{"law-1", "law-2"},
		Magnitude:  10,
	})
	if err != nil {
		t.Fatalf("AddFriction: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}
}

func TestAddFriction_MissingFlowID(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.AddFriction(ctx, &flowv1.AddFrictionRequest{
		WorkitemId: "wi-1",
		Magnitude:  10,
	})
	if err == nil {
		t.Fatal("expected error for missing flow_id")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestAddFriction_MissingWorkitemID(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.AddFriction(ctx, &flowv1.AddFrictionRequest{
		FlowId:    "flow-1",
		Magnitude: 10,
	})
	if err == nil {
		t.Fatal("expected error for missing workitem_id")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestAddFriction_NegativeMagnitude(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.AddFriction(ctx, &flowv1.AddFrictionRequest{
		FlowId:     "flow-1",
		WorkitemId: "wi-1",
		Magnitude:  -5,
	})
	if err == nil {
		t.Fatal("expected error for negative magnitude")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestAddFriction_ZeroMagnitude(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.AddFriction(ctx, &flowv1.AddFrictionRequest{
		FlowId:     "flow-1",
		WorkitemId: "wi-1",
		Magnitude:  0,
	})
	if err != nil {
		t.Fatalf("AddFriction with zero magnitude should succeed: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}
}

// --- RecordTelemetry Tests ---

func TestRecordTelemetry_Success(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.RecordTelemetry(ctx, &flowv1.RecordTelemetryRequest{
		FlowId:     "flow-1",
		NodeId:     "node-a",
		WorkitemId: "wi-1",
		EventType:  "checkpoint",
		Payload:    []byte(`{"step": 1}`),
	})
	if err != nil {
		t.Fatalf("RecordTelemetry: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}
}

func TestRecordTelemetry_MissingFlowID(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.RecordTelemetry(ctx, &flowv1.RecordTelemetryRequest{
		EventType: "checkpoint",
	})
	if err == nil {
		t.Fatal("expected error for missing flow_id")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestRecordTelemetry_MissingEventType(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.RecordTelemetry(ctx, &flowv1.RecordTelemetryRequest{
		FlowId: "flow-1",
	})
	if err == nil {
		t.Fatal("expected error for missing event_type")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestRecordTelemetry_PayloadTooLarge(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	largePayload := make([]byte, 65*1024+1) // 65KB + 1
	_, err := srv.RecordTelemetry(ctx, &flowv1.RecordTelemetryRequest{
		FlowId:    "flow-1",
		EventType: "big-event",
		Payload:   largePayload,
	})
	if err == nil {
		t.Fatal("expected error for oversized payload")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

// --- QueryFriction Tests ---

func TestQueryFriction_Empty(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.QueryFriction(ctx, &flowv1.QueryFrictionRequest{})
	if err != nil {
		t.Fatalf("QueryFriction: %v", err)
	}
	if len(resp.GetFrictionAggregates()) != 0 {
		t.Fatalf("expected 0 aggregates, got %d", len(resp.GetFrictionAggregates()))
	}
}

func TestQueryFriction_AggregatesAcrossLaws(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Add friction attributed to two laws.
	_, err := srv.AddFriction(ctx, &flowv1.AddFrictionRequest{
		FlowId:     "flow-1",
		WorkitemId: "wi-1",
		NodeId:     "node-a",
		LawIds:     []string{"law-1", "law-2"},
		Magnitude:  15,
	})
	if err != nil {
		t.Fatalf("AddFriction: %v", err)
	}

	// Add another event for law-1 only.
	_, err = srv.AddFriction(ctx, &flowv1.AddFrictionRequest{
		FlowId:     "flow-1",
		WorkitemId: "wi-1",
		NodeId:     "node-a",
		LawIds:     []string{"law-1"},
		Magnitude:  10,
	})
	if err != nil {
		t.Fatalf("AddFriction: %v", err)
	}

	// Query for law-1 — should aggregate to 25.
	resp, err := srv.QueryFriction(ctx, &flowv1.QueryFrictionRequest{
		Filter: &flowv1.FrictionFilter{
			LawId: "law-1",
		},
	})
	if err != nil {
		t.Fatalf("QueryFriction: %v", err)
	}

	aggregates := resp.GetFrictionAggregates()
	if len(aggregates) != 1 {
		t.Fatalf("expected 1 aggregate for law-1, got %d", len(aggregates))
	}
	if aggregates[0].GetTotalMagnitude() != 25 {
		t.Fatalf("expected total magnitude 25, got %f", aggregates[0].GetTotalMagnitude())
	}
	if aggregates[0].GetEventCount() != 2 {
		t.Fatalf("expected event count 2, got %d", aggregates[0].GetEventCount())
	}
}

func TestQueryFriction_FilterByNode(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	if _, err := srv.AddFriction(ctx, &flowv1.AddFrictionRequest{
		FlowId: "flow-1", WorkitemId: "wi-1", NodeId: "node-a", Magnitude: 10,
	}); err != nil {
		t.Fatalf("AddFriction node-a: %v", err)
	}
	if _, err := srv.AddFriction(ctx, &flowv1.AddFrictionRequest{
		FlowId: "flow-1", WorkitemId: "wi-1", NodeId: "node-b", Magnitude: 20,
	}); err != nil {
		t.Fatalf("AddFriction node-b: %v", err)
	}

	resp, err := srv.QueryFriction(ctx, &flowv1.QueryFrictionRequest{
		Filter: &flowv1.FrictionFilter{NodeId: "node-b"},
	})
	if err != nil {
		t.Fatalf("QueryFriction: %v", err)
	}

	if len(resp.GetFrictionAggregates()) != 1 {
		t.Fatalf("expected 1 aggregate, got %d", len(resp.GetFrictionAggregates()))
	}
	if resp.GetFrictionAggregates()[0].GetTotalMagnitude() != 20 {
		t.Fatalf("expected 20, got %f", resp.GetFrictionAggregates()[0].GetTotalMagnitude())
	}
}

func TestQueryFriction_FilterByTimeRange(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Add several friction events, then query with a time filter.
	// Since timestamp is server-generated, we test that time filtering
	// at minimum returns the events we just added (within the last second).
	if _, err := srv.AddFriction(ctx, &flowv1.AddFrictionRequest{
		FlowId: "flow-1", WorkitemId: "wi-1", NodeId: "node-a", Magnitude: 10,
	}); err != nil {
		t.Fatalf("AddFriction 1: %v", err)
	}
	if _, err := srv.AddFriction(ctx, &flowv1.AddFrictionRequest{
		FlowId: "flow-1", WorkitemId: "wi-1", NodeId: "node-a", Magnitude: 20,
	}); err != nil {
		t.Fatalf("AddFriction 2: %v", err)
	}

	// Query with a time range covering "now".
	past := time.Now().UTC().Add(-1 * time.Minute)
	future := time.Now().UTC().Add(1 * time.Minute)

	resp, err := srv.QueryFriction(ctx, &flowv1.QueryFrictionRequest{
		Filter: &flowv1.FrictionFilter{
			TimeRange: &flowv1.TimeRange{
				Start: timestamppb.New(past),
				End:   timestamppb.New(future),
			},
		},
	})
	if err != nil {
		t.Fatalf("QueryFriction: %v", err)
	}

	if len(resp.GetFrictionAggregates()) != 1 {
		t.Fatalf("expected 1 aggregate, got %d", len(resp.GetFrictionAggregates()))
	}
	if resp.GetFrictionAggregates()[0].GetTotalMagnitude() != 30 {
		t.Fatalf("expected 30, got %f", resp.GetFrictionAggregates()[0].GetTotalMagnitude())
	}
}

func TestQueryFriction_TimestampsPresent(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	if _, err := srv.AddFriction(ctx, &flowv1.AddFrictionRequest{
		FlowId: "flow-1", WorkitemId: "wi-1", NodeId: "node-a", Magnitude: 10,
	}); err != nil {
		t.Fatalf("AddFriction: %v", err)
	}

	resp, err := srv.QueryFriction(ctx, &flowv1.QueryFrictionRequest{})
	if err != nil {
		t.Fatalf("QueryFriction: %v", err)
	}

	agg := resp.GetFrictionAggregates()
	if len(agg) != 1 {
		t.Fatalf("expected 1 aggregate, got %d", len(agg))
	}
	if agg[0].GetEarliest() == nil {
		t.Fatal("expected earliest timestamp to be set")
	}
	if agg[0].GetLatest() == nil {
		t.Fatal("expected latest timestamp to be set")
	}
}

// --- End-to-End Integration Test ---

func TestEndToEnd_FrictionAndTelemetry(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Record friction events.
	for i := range 5 {
		_, err := srv.AddFriction(ctx, &flowv1.AddFrictionRequest{
			FlowId:     "flow-1",
			WorkitemId: "wi-1",
			NodeId:     "node-a",
			LawIds:     []string{"law-1"},
			Magnitude:  int32(i + 1),
		})
		if err != nil {
			t.Fatalf("AddFriction %d: %v", i, err)
		}
	}

	// Record telemetry events.
	for i := range 3 {
		_, err := srv.RecordTelemetry(ctx, &flowv1.RecordTelemetryRequest{
			FlowId:     "flow-1",
			NodeId:     "node-a",
			WorkitemId: "wi-1",
			EventType:  "progress",
			Payload:    fmt.Appendf(nil, `{"step": %d}`, i),
		})
		if err != nil {
			t.Fatalf("RecordTelemetry %d: %v", i, err)
		}
	}

	// Query friction — should sum to 1+2+3+4+5 = 15.
	resp, err := srv.QueryFriction(ctx, &flowv1.QueryFrictionRequest{
		Filter: &flowv1.FrictionFilter{LawId: "law-1"},
	})
	if err != nil {
		t.Fatalf("QueryFriction: %v", err)
	}
	aggregates := resp.GetFrictionAggregates()
	if len(aggregates) != 1 {
		t.Fatalf("expected 1 aggregate, got %d", len(aggregates))
	}
	if aggregates[0].GetTotalMagnitude() != 15 {
		t.Fatalf("expected total magnitude 15, got %f", aggregates[0].GetTotalMagnitude())
	}
	if aggregates[0].GetEventCount() != 5 {
		t.Fatalf("expected event count 5, got %d", aggregates[0].GetEventCount())
	}
}
