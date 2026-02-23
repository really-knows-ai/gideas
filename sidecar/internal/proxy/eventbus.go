package proxy

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// EventBusProxy wraps a FlowEventBusServiceClient and provides helper
// methods for publishing friction and telemetry events to the Event Bus.
// It handles the translation from the SDK-facing request types to the
// FlowEvent envelope that the Event Bus expects.
type EventBusProxy struct {
	client flowv1.FlowEventBusServiceClient
	conn   *grpc.ClientConn
}

// NewEventBusProxy dials the Event Bus gRPC endpoint and returns a proxy.
func NewEventBusProxy(eventBusAddr string) (*EventBusProxy, error) {
	conn, err := grpc.NewClient(
		eventBusAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, err
	}
	return &EventBusProxy{
		client: flowv1.NewFlowEventBusServiceClient(conn),
		conn:   conn,
	}, nil
}

// NewEventBusProxyFromClient creates an EventBusProxy from an existing
// client, useful for testing.
func NewEventBusProxyFromClient(client flowv1.FlowEventBusServiceClient) *EventBusProxy {
	return &EventBusProxy{client: client}
}

// Close releases the underlying gRPC connection.
func (p *EventBusProxy) Close() error {
	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}

// PublishFriction builds a FlowEvent with channel=TELEMETRY and
// event_type="friction", serialises law_ids and magnitude into attributes,
// and publishes to the Event Bus.
func (p *EventBusProxy) PublishFriction(
	ctx context.Context,
	flowID, workitemID, nodeID string,
	lawIDs []string,
	magnitude float64,
) error {
	attrs := map[string]string{
		"magnitude": strconv.FormatFloat(magnitude, 'f', -1, 64),
	}
	if len(lawIDs) > 0 {
		attrs["law_ids"] = strings.Join(lawIDs, ",")
	}

	_, err := p.client.Publish(ctx, &flowv1.PublishRequest{
		Channel: flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY,
		Event: &flowv1.FlowEvent{
			EventId:    newEventID(),
			EventType:  "friction",
			FlowId:     flowID,
			NodeId:     nodeID,
			WorkitemId: workitemID,
			Timestamp:  timestamppb.Now(),
			Attributes: attrs,
		},
	})
	if err != nil {
		slog.Warn("PublishFriction failed", "error", err)
		return fmt.Errorf("publish friction: %w", err)
	}

	slog.Info("Friction event published to Event Bus",
		"flow_id", flowID,
		"node_id", nodeID,
		"workitem_id", workitemID,
		"magnitude", magnitude,
		"law_ids", lawIDs,
	)
	return nil
}

// PublishTelemetry builds a FlowEvent with channel=TELEMETRY and the
// caller's event_type, then publishes to the Event Bus.
func (p *EventBusProxy) PublishTelemetry(
	ctx context.Context,
	flowID, nodeID, workitemID, eventType string,
	payload []byte,
) error {
	_, err := p.client.Publish(ctx, &flowv1.PublishRequest{
		Channel: flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY,
		Event: &flowv1.FlowEvent{
			EventId:    newEventID(),
			EventType:  eventType,
			FlowId:     flowID,
			NodeId:     nodeID,
			WorkitemId: workitemID,
			Timestamp:  timestamppb.Now(),
			Payload:    payload,
		},
	})
	if err != nil {
		slog.Warn("PublishTelemetry failed", "error", err)
		return fmt.Errorf("publish telemetry: %w", err)
	}

	slog.Info("Telemetry event published to Event Bus",
		"flow_id", flowID,
		"node_id", nodeID,
		"event_type", eventType,
	)
	return nil
}

// newEventID returns a random hex-encoded identifier for events.
func newEventID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return fmt.Sprintf("%x", b)
}
