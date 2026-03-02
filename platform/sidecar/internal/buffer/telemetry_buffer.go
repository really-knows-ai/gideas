// Package buffer provides an async priority buffer for telemetry events
// published to the Event Bus via the Sidecar.
//
// The buffer ensures non-blocking submission for callers (nodes never block
// on telemetry delivery) and prioritises friction events (HIGH) over custom
// telemetry (NORMAL). When the NORMAL buffer is full, oldest NORMAL events
// are dropped. Friction events are only dropped when the HIGH buffer is full.
//
// Internally the buffer delegates to two [eventbus.AsyncPublisher] instances
// (one per priority) which handle buffered-channel queueing, background
// drain, exponential-backoff retry, and graceful shutdown.
//
// See: specs/04-sdk/06-sdk-telemetry.md
package buffer

import (
	"crypto/rand"
	"fmt"
	"strconv"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/pkg/eventbus"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Priority levels for telemetry events.
const (
	PriorityHigh   = iota // Friction events
	PriorityNormal        // Custom telemetry
)

// DefaultBufferSize is the default capacity per priority channel.
const DefaultBufferSize = 1000

// Event represents a telemetry event queued for async delivery.
type Event struct {
	// Priority is PriorityHigh for friction or PriorityNormal for telemetry.
	Priority int

	// --- Friction fields (Priority == PriorityHigh) ---
	// Namespace is the Kubernetes namespace (flow identity boundary).
	Namespace  string
	WorkitemID string
	NodeID     string
	LawIDs     []string
	Magnitude  float64

	// --- Telemetry fields (Priority == PriorityNormal) ---
	EventType string
	Payload   []byte
}

// TelemetryBuffer is an async priority buffer for telemetry events.
// It routes events by priority to two underlying [eventbus.AsyncPublisher]
// instances which handle buffered queueing, retry, and drain.
type TelemetryBuffer struct {
	highPub   *eventbus.AsyncPublisher
	normalPub *eventbus.AsyncPublisher
}

// NewTelemetryBuffer creates a buffer with the given capacity per priority
// channel. If size <= 0, DefaultBufferSize is used.
//
// The publisher argument is the Event Bus gRPC client (or any
// implementation of [eventbus.Publisher]) used to send events.
func NewTelemetryBuffer(pub eventbus.Publisher, size int) *TelemetryBuffer {
	if size <= 0 {
		size = DefaultBufferSize
	}
	return &TelemetryBuffer{
		highPub:   eventbus.NewAsyncPublisherFromPublisher(pub, eventbus.WithBufferSize(size)),
		normalPub: eventbus.NewAsyncPublisherFromPublisher(pub, eventbus.WithBufferSize(size)),
	}
}

// NewTelemetryBufferFromClient creates a buffer from a
// [flowv1.FlowEventBusServiceClient], adapting it to the [eventbus.Publisher]
// interface internally.
func NewTelemetryBufferFromClient(client flowv1.FlowEventBusServiceClient, size int) *TelemetryBuffer {
	if size <= 0 {
		size = DefaultBufferSize
	}
	return &TelemetryBuffer{
		highPub:   eventbus.NewAsyncPublisher(client, eventbus.WithBufferSize(size)),
		normalPub: eventbus.NewAsyncPublisher(client, eventbus.WithBufferSize(size)),
	}
}

// Submit enqueues an event for async delivery. Non-blocking: if the
// buffer for the event's priority is full, the event is dropped.
func (tb *TelemetryBuffer) Submit(evt Event) {
	req := eventToPublishRequest(evt)
	switch evt.Priority {
	case PriorityHigh:
		tb.highPub.Submit(req)
	default:
		tb.normalPub.Submit(req)
	}
}

// Stop signals both drain goroutines to exit and waits for them to finish.
// Remaining buffered events are flushed on a best-effort basis.
func (tb *TelemetryBuffer) Stop() {
	// Stop both publishers concurrently. Both drain independently.
	tb.highPub.Stop()
	tb.normalPub.Stop()
}

// DroppedNormal returns the number of dropped normal-priority events.
func (tb *TelemetryBuffer) DroppedNormal() int64 {
	return tb.normalPub.Dropped()
}

// DroppedHigh returns the number of dropped high-priority events.
func (tb *TelemetryBuffer) DroppedHigh() int64 {
	return tb.highPub.Dropped()
}

// eventToPublishRequest converts a domain Event into a [flowv1.PublishRequest].
func eventToPublishRequest(evt Event) *flowv1.PublishRequest {
	switch evt.Priority {
	case PriorityHigh:
		return frictionRequest(evt)
	default:
		return telemetryRequest(evt)
	}
}

func frictionRequest(evt Event) *flowv1.PublishRequest {
	attrs := map[string]string{
		"magnitude": strconv.FormatFloat(evt.Magnitude, 'f', -1, 64),
	}

	labels := make([]*flowv1.Label, 0, len(evt.LawIDs))
	for _, id := range evt.LawIDs {
		labels = append(labels, &flowv1.Label{Key: "law_id", Value: id})
	}

	return &flowv1.PublishRequest{
		Channel: "telemetry",
		Event: &flowv1.FlowEvent{
			EventId:       newEventID(),
			EventType:     "friction",
			FlowNamespace: evt.Namespace,
			NodeId:        evt.NodeID,
			WorkitemId:    evt.WorkitemID,
			Timestamp:     timestamppb.Now(),
			Attributes:    attrs,
			Labels:        labels,
		},
	}
}

func telemetryRequest(evt Event) *flowv1.PublishRequest {
	return &flowv1.PublishRequest{
		Channel: "telemetry",
		Event: &flowv1.FlowEvent{
			EventId:       newEventID(),
			EventType:     evt.EventType,
			FlowNamespace: evt.Namespace,
			NodeId:        evt.NodeID,
			WorkitemId:    evt.WorkitemID,
			Timestamp:     timestamppb.Now(),
			Payload:       evt.Payload,
		},
	}
}

// newEventID returns a random hex-encoded identifier for events.
func newEventID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return fmt.Sprintf("%x", b)
}
