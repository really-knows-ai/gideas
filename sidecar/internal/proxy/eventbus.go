package proxy

import (
	"crypto/rand"
	"fmt"
	"log/slog"
	"strconv"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/pkg/eventbus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// EventBusProxy wraps a FlowEventBusServiceClient and provides helper
// methods for publishing friction and telemetry events to the Event Bus.
// It handles the translation from the SDK-facing request types to the
// FlowEvent envelope that the Event Bus expects. Publishing is
// non-blocking via the shared AsyncPublisher.
type EventBusProxy struct {
	client    flowv1.FlowEventBusServiceClient
	conn      *grpc.ClientConn
	publisher *eventbus.AsyncPublisher
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
	client := flowv1.NewFlowEventBusServiceClient(conn)
	return &EventBusProxy{
		client:    client,
		conn:      conn,
		publisher: eventbus.NewAsyncPublisher(client),
	}, nil
}

// NewEventBusProxyFromClient creates an EventBusProxy from an existing
// client, useful for testing.
func NewEventBusProxyFromClient(client flowv1.FlowEventBusServiceClient) *EventBusProxy {
	return &EventBusProxy{
		client:    client,
		publisher: eventbus.NewAsyncPublisher(client),
	}
}

// Client returns the underlying FlowEventBusServiceClient.
func (p *EventBusProxy) Client() flowv1.FlowEventBusServiceClient {
	return p.client
}

// Close stops the async publisher and releases the underlying gRPC connection.
func (p *EventBusProxy) Close() error {
	if p.publisher != nil {
		p.publisher.Stop()
	}
	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}

// PublishFriction builds a FlowEvent with channel="telemetry" and
// event_type="friction", serialises law_ids as labels and magnitude into
// attributes, and submits asynchronously to the Event Bus.
func (p *EventBusProxy) PublishFriction(
	flowID, workitemID, nodeID string,
	lawIDs []string,
	magnitude float64,
) {
	attrs := map[string]string{
		"magnitude": strconv.FormatFloat(magnitude, 'f', -1, 64),
	}

	// Build labels for law_ids (one label per law_id for multi-valued filtering).
	labels := make([]*flowv1.Label, 0, len(lawIDs))
	for _, id := range lawIDs {
		labels = append(labels, &flowv1.Label{Key: "law_id", Value: id})
	}

	p.publisher.Submit(&flowv1.PublishRequest{
		Channel: "telemetry",
		Event: &flowv1.FlowEvent{
			EventId:    newEventID(),
			EventType:  "friction",
			FlowId:     flowID,
			NodeId:     nodeID,
			WorkitemId: workitemID,
			Timestamp:  timestamppb.Now(),
			Attributes: attrs,
			Labels:     labels,
		},
	})

	slog.Info("Friction event submitted to Event Bus",
		"flow_id", flowID,
		"node_id", nodeID,
		"workitem_id", workitemID,
		"magnitude", magnitude,
		"law_ids", lawIDs,
	)
}

// PublishTelemetry builds a FlowEvent with channel="telemetry" and the
// caller's event_type, then submits asynchronously to the Event Bus.
func (p *EventBusProxy) PublishTelemetry(
	flowID, nodeID, workitemID, eventType string,
	payload []byte,
) {
	p.publisher.Submit(&flowv1.PublishRequest{
		Channel: "telemetry",
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

	slog.Info("Telemetry event submitted to Event Bus",
		"flow_id", flowID,
		"node_id", nodeID,
		"event_type", eventType,
	)
}

// newEventID returns a random hex-encoded identifier for events.
func newEventID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return fmt.Sprintf("%x", b)
}
