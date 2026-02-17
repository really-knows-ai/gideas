// Package service implements the FlowMonitorService gRPC server.
//
// The Flow Monitor is the central telemetry and friction aggregation service
// for the Control Plane. It serves as a mandatory runtime output surface for
// nodes and a query source for the Librarian's law lifecycle triggers.
package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/gideas/flow/monitor/internal/store/sqlite"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const maxPayloadBytes = 64 * 1024 // 64 KB

// IDGenerator produces unique event identifiers.
type IDGenerator func() string

// MonitorServer implements flowv1.FlowMonitorServiceServer backed by a
// SQLite store.
type MonitorServer struct {
	flowv1.UnimplementedFlowMonitorServiceServer
	store *sqlite.Store
	newID IDGenerator
}

// NewMonitorServer returns a MonitorServer backed by the given store.
// The idGen function is called to produce a unique ID for each ingested event.
func NewMonitorServer(s *sqlite.Store, idGen IDGenerator) *MonitorServer {
	return &MonitorServer{store: s, newID: idGen}
}

// AddFriction validates and persists a friction event with its associated law
// identifiers in a single transaction.
func (m *MonitorServer) AddFriction(
	ctx context.Context, req *flowv1.AddFrictionRequest,
) (*flowv1.AddFrictionResponse, error) {
	// Validate mandatory fields.
	if req.GetFlowId() == "" {
		return nil, status.Error(codes.InvalidArgument, "flow_id is required")
	}
	if req.GetWorkitemId() == "" {
		return nil, status.Error(codes.InvalidArgument, "workitem_id is required")
	}
	// Magnitude must be purely additive (non-negative).
	if req.GetMagnitude() < 0 {
		return nil, status.Error(codes.InvalidArgument, "magnitude must be non-negative")
	}

	id := m.newID()
	now := time.Now().UTC()

	event := sqlite.FrictionEvent{
		ID:         id,
		FlowID:     req.GetFlowId(),
		WorkitemID: req.GetWorkitemId(),
		NodeID:     req.GetNodeId(),
		Magnitude:  req.GetMagnitude(),
		Timestamp:  now,
	}

	slog.Info("AddFriction",
		"id", id,
		"flow_id", event.FlowID,
		"workitem_id", event.WorkitemID,
		"node_id", event.NodeID,
		"magnitude", event.Magnitude,
		"law_ids", req.GetLawIds(),
	)

	if err := m.store.AddFriction(ctx, id, event, req.GetLawIds()); err != nil {
		slog.Error("AddFriction failed", "error", err)
		return nil, status.Errorf(codes.Internal, "store friction event: %v", err)
	}

	return &flowv1.AddFrictionResponse{Acknowledged: true}, nil
}

// RecordTelemetry validates the payload size and persists a telemetry event.
func (m *MonitorServer) RecordTelemetry(
	ctx context.Context, req *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	// Validate mandatory fields.
	if req.GetFlowId() == "" {
		return nil, status.Error(codes.InvalidArgument, "flow_id is required")
	}
	if req.GetEventType() == "" {
		return nil, status.Error(codes.InvalidArgument, "event_type is required")
	}

	// Enforce 64 KB payload limit.
	if len(req.GetPayload()) > maxPayloadBytes {
		return nil, status.Errorf(codes.InvalidArgument,
			"payload exceeds maximum size of %d bytes (got %d)",
			maxPayloadBytes, len(req.GetPayload()))
	}

	id := m.newID()
	now := time.Now().UTC()

	event := sqlite.TelemetryEvent{
		ID:         id,
		FlowID:     req.GetFlowId(),
		NodeID:     req.GetNodeId(),
		WorkitemID: req.GetWorkitemId(),
		EventType:  req.GetEventType(),
		Payload:    req.GetPayload(),
		Timestamp:  now,
	}

	slog.Info("RecordTelemetry",
		"id", id,
		"flow_id", event.FlowID,
		"node_id", event.NodeID,
		"workitem_id", event.WorkitemID,
		"event_type", event.EventType,
		"payload_bytes", len(event.Payload),
	)

	if err := m.store.RecordTelemetry(ctx, event); err != nil {
		slog.Error("RecordTelemetry failed", "error", err)
		return nil, status.Errorf(codes.Internal, "store telemetry event: %v", err)
	}

	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

// QueryFriction applies the FrictionFilter to the SQL store and returns
// aggregated friction data grouped by (law_id, node_id, workitem_id).
func (m *MonitorServer) QueryFriction(
	ctx context.Context, req *flowv1.QueryFrictionRequest,
) (*flowv1.QueryFrictionResponse, error) {
	filter := sqlite.FrictionFilter{}

	if f := req.GetFilter(); f != nil {
		filter.LawID = f.GetLawId()
		filter.NodeID = f.GetNodeId()
		filter.WorkitemID = f.GetWorkitemId()
		filter.Tier = int32(f.GetTier())

		if tr := f.GetTimeRange(); tr != nil {
			if tr.GetStart() != nil {
				t := tr.GetStart().AsTime()
				filter.StartTime = &t
			}
			if tr.GetEnd() != nil {
				t := tr.GetEnd().AsTime()
				filter.EndTime = &t
			}
		}
	}

	slog.Info("QueryFriction",
		"law_id", filter.LawID,
		"node_id", filter.NodeID,
		"workitem_id", filter.WorkitemID,
		"tier", filter.Tier,
	)

	results, err := m.store.QueryFriction(ctx, filter)
	if err != nil {
		slog.Error("QueryFriction failed", "error", err)
		return nil, status.Errorf(codes.Internal, "query friction: %v", err)
	}

	aggregates := make([]*flowv1.FrictionAggregate, 0, len(results))
	for _, r := range results {
		agg := &flowv1.FrictionAggregate{
			LawId:          r.LawID,
			NodeId:         r.NodeID,
			WorkitemId:     r.WorkitemID,
			Tier:           flowv1.LawTier(r.Tier),
			TotalMagnitude: r.TotalMagnitude,
			EventCount:     r.EventCount,
			Earliest:       timestamppb.New(r.Earliest),
			Latest:         timestamppb.New(r.Latest),
		}
		aggregates = append(aggregates, agg)
	}

	slog.Info("QueryFriction result",
		"aggregate_count", len(aggregates),
	)

	return &flowv1.QueryFrictionResponse{
		FrictionAggregates: aggregates,
	}, nil
}
