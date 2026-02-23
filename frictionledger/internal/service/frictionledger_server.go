// Package service implements the FrictionLedgerService gRPC server.
//
// The Friction Ledger is the sole aggregation and query surface for friction
// data. It subscribes to the Event Bus telemetry channel for friction events,
// persists them to a local SQLite store, evaluates per-law thresholds, and
// publishes threshold-crossing events to the Event Bus friction channel.
//
// See: specs/02-flow/04-system-services.md (Service Invariant #15)
package service

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gideas/flow/frictionledger/internal/store/sqlite"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// checkpointChannel is the checkpoint key for the telemetry channel
	// subscription in the Friction Ledger's subscriber_checkpoint table.
	checkpointChannel = "telemetry"

	// reconnectBaseDelay is the initial backoff delay for Event Bus reconnection.
	reconnectBaseDelay = 1 * time.Second

	// reconnectMaxDelay caps the exponential backoff.
	reconnectMaxDelay = 30 * time.Second
)

// ThresholdConfig maps law tier numbers (1-5) to friction threshold values.
// When accumulated friction for a law exceeds its tier's threshold, a
// friction.threshold_crossed event is published.
type ThresholdConfig map[int32]float64

// IDGenerator produces unique event identifiers.
type IDGenerator func() string

// FrictionLedgerServer implements flowv1.FrictionLedgerServiceServer and
// manages the Event Bus subscription lifecycle.
type FrictionLedgerServer struct {
	flowv1.UnimplementedFrictionLedgerServiceServer

	store      *sqlite.Store
	newID      IDGenerator
	thresholds ThresholdConfig

	// eventBusClient is the gRPC client for the Event Bus. It is set after
	// the server is created via StartSubscription.
	eventBusClient flowv1.FlowEventBusServiceClient

	// stopCh signals the subscription goroutine to exit.
	stopCh chan struct{}

	// stopOnce ensures Stop is safe to call multiple times.
	stopOnce sync.Once

	// wg tracks background goroutines.
	wg sync.WaitGroup

	// crossedMu protects the crossedLaws map.
	crossedMu sync.Mutex
	// crossedLaws tracks which (law_id, tier) pairs have already had a
	// threshold-crossing event published, to avoid duplicate publications.
	crossedLaws map[string]struct{}
}

// NewFrictionLedgerServer returns a server backed by the given store.
func NewFrictionLedgerServer(
	store *sqlite.Store,
	idGen IDGenerator,
	thresholds ThresholdConfig,
) *FrictionLedgerServer {
	return &FrictionLedgerServer{
		store:       store,
		newID:       idGen,
		thresholds:  thresholds,
		stopCh:      make(chan struct{}),
		crossedLaws: make(map[string]struct{}),
	}
}

// Stop signals the subscription goroutine to exit and waits for it.
// Safe to call multiple times.
func (s *FrictionLedgerServer) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.wg.Wait()
}

// StartSubscription begins the Event Bus telemetry channel subscription
// in a background goroutine. It reads the checkpoint from the store to
// resume from the last processed sequence.
func (s *FrictionLedgerServer) StartSubscription(client flowv1.FlowEventBusServiceClient) {
	s.eventBusClient = client
	s.wg.Add(1)
	go s.subscriptionLoop()
}

// QueryFriction applies the LedgerFrictionFilter to the SQL store and returns
// aggregated friction data grouped by (law_id, node_id, workitem_id).
func (s *FrictionLedgerServer) QueryFriction(
	ctx context.Context, req *flowv1.LedgerQueryFrictionRequest,
) (*flowv1.LedgerQueryFrictionResponse, error) {
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

	results, err := s.store.QueryFriction(ctx, filter)
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

	slog.Info("QueryFriction result", "aggregate_count", len(aggregates))

	return &flowv1.LedgerQueryFrictionResponse{
		FrictionAggregates: aggregates,
	}, nil
}

// subscriptionLoop connects to the Event Bus and processes friction events.
// On disconnect, it retries with exponential backoff, resuming from the
// persisted checkpoint.
func (s *FrictionLedgerServer) subscriptionLoop() {
	defer s.wg.Done()

	delay := reconnectBaseDelay
	for {
		select {
		case <-s.stopCh:
			return
		default:
		}

		err := s.runSubscription()
		if err == nil {
			// Clean shutdown (stopCh closed).
			return
		}

		slog.Error("Event Bus subscription error, reconnecting",
			"error", err,
			"retry_delay", delay,
		)

		select {
		case <-time.After(delay):
			delay *= 2
			if delay > reconnectMaxDelay {
				delay = reconnectMaxDelay
			}
		case <-s.stopCh:
			return
		}
	}
}

// runSubscription connects to the Event Bus telemetry channel and processes
// events until an error occurs or the server is stopped.
func (s *FrictionLedgerServer) runSubscription() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Monitor stopCh to cancel context.
	go func() {
		select {
		case <-s.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	// Read checkpoint.
	lastSeq, err := s.store.GetCheckpoint(ctx, checkpointChannel)
	if err != nil {
		return fmt.Errorf("get checkpoint: %w", err)
	}

	slog.Info("Subscribing to Event Bus telemetry channel",
		"last_sequence", lastSeq,
	)

	stream, err := s.eventBusClient.Subscribe(ctx, &flowv1.SubscribeRequest{
		Channel:      flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY,
		Filter:       &flowv1.SubscribeFilter{EventType: "friction"},
		LastSequence: lastSeq,
	})
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	// Reset backoff on successful connection.
	for {
		evt, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				return fmt.Errorf("stream closed by server")
			}
			return fmt.Errorf("recv: %w", err)
		}

		if err := s.processEvent(ctx, evt); err != nil {
			slog.Error("Failed to process event",
				"event_id", evt.GetEventId(),
				"error", err,
			)
			// Continue processing; don't disconnect for individual failures.
			continue
		}

		// Update checkpoint.
		if err := s.store.SetCheckpoint(ctx, checkpointChannel, evt.GetSequence()); err != nil {
			slog.Error("Failed to update checkpoint",
				"sequence", evt.GetSequence(),
				"error", err,
			)
		}
	}
}

// processEvent parses a friction FlowEvent and persists it to the store,
// then evaluates thresholds.
func (s *FrictionLedgerServer) processEvent(ctx context.Context, evt *flowv1.FlowEvent) error {
	attrs := evt.GetAttributes()

	// Parse law_ids from comma-separated string.
	var lawIDs []string
	if raw, ok := attrs["law_ids"]; ok && raw != "" {
		lawIDs = strings.Split(raw, ",")
	}

	// Parse magnitude from string.
	var magnitude float64
	if raw, ok := attrs["magnitude"]; ok {
		var err error
		magnitude, err = strconv.ParseFloat(raw, 64)
		if err != nil {
			return fmt.Errorf("parse magnitude %q: %w", raw, err)
		}
	}

	if magnitude < 0 {
		return fmt.Errorf("negative magnitude %f", magnitude)
	}

	ts := time.Now().UTC()
	if evt.GetTimestamp() != nil {
		ts = evt.GetTimestamp().AsTime()
	}

	id := s.newID()
	event := sqlite.FrictionEvent{
		FlowID:     evt.GetFlowId(),
		WorkitemID: evt.GetWorkitemId(),
		NodeID:     evt.GetNodeId(),
		Magnitude:  magnitude,
		Timestamp:  ts,
	}

	if err := s.store.AddFriction(ctx, id, event, lawIDs); err != nil {
		return fmt.Errorf("store friction event: %w", err)
	}

	slog.Info("Friction event persisted",
		"event_id", evt.GetEventId(),
		"internal_id", id,
		"flow_id", event.FlowID,
		"magnitude", magnitude,
		"law_ids", lawIDs,
	)

	// Evaluate thresholds for each law.
	for _, lawID := range lawIDs {
		if err := s.evaluateThreshold(ctx, lawID, evt); err != nil {
			slog.Error("Threshold evaluation failed",
				"law_id", lawID,
				"error", err,
			)
		}
	}

	return nil
}

// evaluateThreshold checks if the accumulated friction for a law has crossed
// any configured tier threshold. If so, it publishes a threshold-crossing
// event to the Event Bus friction channel.
func (s *FrictionLedgerServer) evaluateThreshold(
	ctx context.Context,
	lawID string,
	sourceEvt *flowv1.FlowEvent,
) error {
	if s.eventBusClient == nil || len(s.thresholds) == 0 {
		return nil
	}

	accumulated, err := s.store.QueryFrictionByLaw(ctx, lawID)
	if err != nil {
		return fmt.Errorf("query accumulated friction: %w", err)
	}

	for tier, threshold := range s.thresholds {
		if accumulated < threshold {
			continue
		}

		key := fmt.Sprintf("%s:%d", lawID, tier)
		s.crossedMu.Lock()
		_, alreadyCrossed := s.crossedLaws[key]
		if !alreadyCrossed {
			s.crossedLaws[key] = struct{}{}
		}
		s.crossedMu.Unlock()

		if alreadyCrossed {
			continue
		}

		slog.Info("Friction threshold crossed",
			"law_id", lawID,
			"tier", tier,
			"accumulated", accumulated,
			"threshold", threshold,
		)

		// Publish threshold-crossing event to friction channel.
		_, err := s.eventBusClient.Publish(ctx, &flowv1.PublishRequest{
			Channel: flowv1.EventChannel_EVENT_CHANNEL_FRICTION,
			Event: &flowv1.FlowEvent{
				EventId:    s.newID(),
				EventType:  "friction.threshold_crossed",
				FlowId:     sourceEvt.GetFlowId(),
				NodeId:     sourceEvt.GetNodeId(),
				WorkitemId: sourceEvt.GetWorkitemId(),
				Timestamp:  timestamppb.Now(),
				Attributes: map[string]string{
					"law_id":               lawID,
					"tier":                 strconv.Itoa(int(tier)),
					"accumulated_friction": strconv.FormatFloat(accumulated, 'f', -1, 64),
					"threshold":            strconv.FormatFloat(threshold, 'f', -1, 64),
				},
			},
		})
		if err != nil {
			// Log but do not fail — threshold crossing is best-effort
			// and will be retried on the next friction event.
			s.crossedMu.Lock()
			delete(s.crossedLaws, key)
			s.crossedMu.Unlock()
			return fmt.Errorf("publish threshold crossing: %w", err)
		}
	}

	return nil
}

// ConnectEventBus dials the Event Bus at the given address and returns the
// client connection. The caller is responsible for closing the connection.
func ConnectEventBus(address string) (*grpc.ClientConn, flowv1.FlowEventBusServiceClient, error) {
	conn, err := grpc.NewClient(address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("dial event bus: %w", err)
	}
	return conn, flowv1.NewFlowEventBusServiceClient(conn), nil
}
