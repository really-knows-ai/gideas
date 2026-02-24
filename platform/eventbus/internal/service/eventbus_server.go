package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/gideas/flow/eventbus/internal/store/sqlite"
	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	maxPayloadBytes = 64 * 1024 // 64 KB
	replayBatch     = 500
)

// RetentionConfig holds per-channel retention limits.
type RetentionConfig struct {
	Duration time.Duration
	Size     int64 // bytes
}

// EventBusServer implements the FlowEventBusService gRPC API.
type EventBusServer struct {
	flowv1.UnimplementedFlowEventBusServiceServer

	store     *sqlite.Store
	reg       *registry
	idGen     func() string
	stopCh    chan struct{}
	retention map[string]RetentionConfig // per-channel
}

// NewEventBusServer creates a new server backed by the given store.
// The idGen function produces unique event IDs when the publisher does
// not supply one. retention is per-channel and may be nil.
func NewEventBusServer(
	store *sqlite.Store,
	idGen func() string,
	retention map[string]RetentionConfig,
) *EventBusServer {
	s := &EventBusServer{
		store:     store,
		reg:       newRegistry(),
		idGen:     idGen,
		stopCh:    make(chan struct{}),
		retention: retention,
	}
	if retention != nil {
		go s.evictionLoop()
	}
	return s
}

// Stop signals the eviction goroutine to exit.
func (s *EventBusServer) Stop() { close(s.stopCh) }

// Publish persists an event to the store and fans it out to active
// subscribers on the event's channel.
func (s *EventBusServer) Publish(ctx context.Context, req *flowv1.PublishRequest) (*flowv1.PublishResponse, error) {
	if req.GetChannel() == "" {
		return nil, status.Error(codes.InvalidArgument, "channel is required")
	}
	if req.GetEvent() == nil {
		return nil, status.Error(codes.InvalidArgument, "event is required")
	}

	evt := req.GetEvent()
	if evt.GetEventType() == "" {
		return nil, status.Error(codes.InvalidArgument, "event_type is required")
	}
	if len(evt.GetPayload()) > maxPayloadBytes {
		return nil, status.Errorf(codes.InvalidArgument, "payload exceeds %d bytes", maxPayloadBytes)
	}

	eventID := evt.GetEventId()
	if eventID == "" {
		eventID = s.idGen()
	}

	ts := time.Now().UTC()
	if evt.GetTimestamp() != nil {
		ts = evt.GetTimestamp().AsTime()
	}

	storeEvt := &sqlite.Event{
		ID:         eventID,
		Channel:    req.GetChannel(),
		EventType:  evt.GetEventType(),
		FlowID:     evt.GetFlowId(),
		NodeID:     evt.GetNodeId(),
		WorkitemID: evt.GetWorkitemId(),
		Timestamp:  ts,
		TraceID:    evt.GetTraceId(),
		Attributes: evt.GetAttributes(),
		Payload:    evt.GetPayload(),
		Labels:     protoLabelsToStore(evt.GetLabels()),
	}

	seq, err := s.store.Insert(ctx, storeEvt)
	if err != nil {
		slog.Error("Publish: insert failed", "error", err)
		return nil, status.Errorf(codes.Internal, "persist event: %v", err)
	}

	slog.Info("Event published",
		"event_id", eventID,
		"channel", req.GetChannel(),
		"event_type", evt.GetEventType(),
		"sequence", seq,
	)

	s.reg.fanOut(*storeEvt)

	return &flowv1.PublishResponse{
		Acknowledged: true,
		Sequence:     seq,
	}, nil
}

// Subscribe streams events to the caller. If last_sequence > 0 the
// server replays stored events first, then switches to live delivery.
func (s *EventBusServer) Subscribe(
	req *flowv1.SubscribeRequest,
	stream flowv1.FlowEventBusService_SubscribeServer,
) error {
	if req.GetChannel() == "" {
		return status.Error(codes.InvalidArgument, "channel is required")
	}

	ch := req.GetChannel()
	filter := subscribeFilter{}
	if f := req.GetFilter(); f != nil {
		filter.eventType = f.GetEventType()
		filter.matchLabels = protoLabelsToStore(f.GetMatchLabels())
	}

	// Replay from store if requested.
	lastSeq := req.GetLastSequence()
	if lastSeq > 0 {
		// Verify the requested sequence is within retention.
		minSeq, err := s.store.MinSequence(stream.Context(), ch)
		if err != nil {
			return status.Errorf(codes.Internal, "min sequence: %v", err)
		}
		if minSeq > 0 && lastSeq < minSeq {
			return status.Errorf(codes.OutOfRange,
				"SEQUENCE_EXPIRED: requested sequence %d is before earliest retained %d",
				lastSeq, minSeq)
		}

		// Replay in batches.
		cursor := lastSeq
		for {
			events, err := s.store.GetSince(stream.Context(), ch, cursor, replayBatch)
			if err != nil {
				return status.Errorf(codes.Internal, "replay: %v", err)
			}
			if len(events) == 0 {
				break
			}
			for i := range events {
				if !matchesFilter(events[i], filter) {
					cursor = events[i].Sequence
					continue
				}
				if err := stream.Send(toProto(events[i])); err != nil {
					return err
				}
				cursor = events[i].Sequence
			}
		}
	}

	// Switch to live delivery.
	sub := s.reg.add(ch, filter)
	defer s.reg.remove(ch, sub)

	for {
		select {
		case evt, ok := <-sub.ch:
			if !ok {
				return nil
			}
			if err := stream.Send(toProto(evt)); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return stream.Context().Err()
		case <-s.stopCh:
			return status.Error(codes.Unavailable, "server shutting down")
		}
	}
}

// evictionLoop periodically evicts events that exceed retention.
func (s *EventBusServer) evictionLoop() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.runEviction()
		case <-s.stopCh:
			return
		}
	}
}

func (s *EventBusServer) runEviction() {
	ctx := context.Background()
	for ch, cfg := range s.retention {
		n, err := s.store.Evict(ctx, ch, cfg.Duration, cfg.Size)
		if err != nil {
			slog.Error("Eviction failed", "channel", ch, "error", err)
			continue
		}
		if n > 0 {
			slog.Info("Eviction complete", "channel", ch, "deleted", n)
		}
	}
}

// toProto converts a store event to the protobuf FlowEvent message.
func toProto(evt sqlite.Event) *flowv1.FlowEvent {
	return &flowv1.FlowEvent{
		EventId:    evt.ID,
		Sequence:   evt.Sequence,
		Channel:    evt.Channel,
		EventType:  evt.EventType,
		FlowId:     evt.FlowID,
		NodeId:     evt.NodeID,
		WorkitemId: evt.WorkitemID,
		Timestamp:  timestamppb.New(evt.Timestamp),
		TraceId:    evt.TraceID,
		Attributes: evt.Attributes,
		Payload:    evt.Payload,
		Labels:     storeLabelsToProto(evt.Labels),
	}
}

// protoLabelsToStore converts proto Label messages to store Label values.
func protoLabelsToStore(pls []*flowv1.Label) []sqlite.Label {
	if len(pls) == 0 {
		return nil
	}
	labels := make([]sqlite.Label, len(pls))
	for i, pl := range pls {
		labels[i] = sqlite.Label{Key: pl.GetKey(), Value: pl.GetValue()}
	}
	return labels
}

// storeLabelsToProto converts store Label values to proto Label messages.
func storeLabelsToProto(sls []sqlite.Label) []*flowv1.Label {
	if len(sls) == 0 {
		return nil
	}
	labels := make([]*flowv1.Label, len(sls))
	for i, sl := range sls {
		labels[i] = &flowv1.Label{Key: sl.Key, Value: sl.Value}
	}
	return labels
}
