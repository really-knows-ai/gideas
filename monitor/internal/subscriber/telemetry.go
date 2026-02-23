// Package subscriber provides Event Bus subscription handlers for the
// Flow Monitor's stateless pipeline adapter.
//
// See: specs/02-flow/04-system-services.md (Service Invariant #16)
package subscriber

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gideas/flow/monitor/internal/metrics"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	// reconnectBaseDelay is the initial backoff delay for Event Bus reconnection.
	reconnectBaseDelay = 1 * time.Second

	// reconnectMaxDelay caps the exponential backoff.
	reconnectMaxDelay = 30 * time.Second
)

// Checkpoint persists the last-seen sequence per channel to a file.
// This allows replay on restart.
type Checkpoint interface {
	// Get returns the last-seen sequence for the given channel, or 0 if none.
	Get(channel string) (uint64, error)
	// Set persists the last-seen sequence for the given channel.
	Set(channel string, seq uint64) error
}

// eventHandler processes a single FlowEvent from the subscription stream.
type eventHandler func(evt *flowv1.FlowEvent)

// channelSubscriber encapsulates the shared reconnect-and-stream logic
// used by both the telemetry and audit subscribers.
type channelSubscriber struct {
	client      flowv1.FlowEventBusServiceClient
	checkpoint  Checkpoint
	channel     flowv1.EventChannel
	channelName string // "telemetry" or "audit" — used for checkpoint key and logging
	handler     eventHandler

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// loop runs the subscription with exponential-backoff reconnection.
func (cs *channelSubscriber) loop() {
	delay := reconnectBaseDelay
	for {
		select {
		case <-cs.stopCh:
			return
		default:
		}

		err := cs.run()
		if err == nil {
			return
		}

		metrics.SubscriberErrors.WithLabelValues(cs.channelName).Inc()
		slog.Error("Subscription error, reconnecting",
			"channel", cs.channelName,
			"error", err,
			"retry_delay", delay,
		)

		select {
		case <-time.After(delay):
			delay *= 2
			if delay > reconnectMaxDelay {
				delay = reconnectMaxDelay
			}
		case <-cs.stopCh:
			return
		}
	}
}

// run connects to the Event Bus and streams events until an error occurs
// or the subscriber is stopped.
func (cs *channelSubscriber) run() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		select {
		case <-cs.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	lastSeq, err := cs.checkpoint.Get(cs.channelName)
	if err != nil {
		return fmt.Errorf("get checkpoint: %w", err)
	}

	slog.Info("Subscribing to Event Bus channel",
		"channel", cs.channelName,
		"last_sequence", lastSeq,
	)

	stream, err := cs.client.Subscribe(ctx, &flowv1.SubscribeRequest{
		Channel:      cs.channel,
		LastSequence: lastSeq,
	})
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	for {
		evt, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				return fmt.Errorf("stream closed by server")
			}
			return fmt.Errorf("recv: %w", err)
		}

		cs.handler(evt)

		if cpErr := cs.checkpoint.Set(cs.channelName, evt.GetSequence()); cpErr != nil {
			slog.Error("Failed to update checkpoint",
				"channel", cs.channelName,
				"sequence", evt.GetSequence(),
				"error", cpErr,
			)
		}
	}
}

// TelemetrySubscriber subscribes to the Event Bus telemetry channel and
// transforms events into Prometheus metrics.
type TelemetrySubscriber struct {
	cs channelSubscriber
}

// NewTelemetrySubscriber returns a subscriber that will connect to the Event Bus
// at the given address.
func NewTelemetrySubscriber(client flowv1.FlowEventBusServiceClient, cp Checkpoint) *TelemetrySubscriber {
	s := &TelemetrySubscriber{}
	s.cs = channelSubscriber{
		client:      client,
		checkpoint:  cp,
		channel:     flowv1.EventChannel_EVENT_CHANNEL_TELEMETRY,
		channelName: "telemetry",
		handler:     s.processEvent,
		stopCh:      make(chan struct{}),
	}
	return s
}

// Start begins the subscription in a background goroutine.
func (s *TelemetrySubscriber) Start() {
	s.cs.wg.Go(func() {
		s.cs.loop()
	})
}

// Stop signals the subscription to exit and waits for it.
func (s *TelemetrySubscriber) Stop() {
	s.cs.stopOnce.Do(func() { close(s.cs.stopCh) })
	s.cs.wg.Wait()
}

// processEvent transforms a telemetry FlowEvent into Prometheus metrics.
func (s *TelemetrySubscriber) processEvent(evt *flowv1.FlowEvent) {
	eventType := evt.GetEventType()
	nodeID := evt.GetNodeId()

	// Increment the generic telemetry event counter.
	metrics.TelemetryEvents.WithLabelValues(eventType, nodeID).Inc()

	switch eventType {
	case "friction":
		s.processFriction(evt)
	case "friction.threshold_crossed":
		s.processThresholdCrossing(evt)
	}
}

func (s *TelemetrySubscriber) processFriction(evt *flowv1.FlowEvent) {
	attrs := evt.GetAttributes()
	nodeID := evt.GetNodeId()

	var magnitude float64
	if raw, ok := attrs["magnitude"]; ok {
		var err error
		magnitude, err = strconv.ParseFloat(raw, 64)
		if err != nil {
			slog.Warn("Invalid friction magnitude",
				"event_id", evt.GetEventId(),
				"magnitude", raw,
			)
			return
		}
	}

	// Split law_ids and increment per-law counters.
	var lawIDs []string
	if raw, ok := attrs["law_ids"]; ok && raw != "" {
		lawIDs = strings.Split(raw, ",")
	}

	if len(lawIDs) == 0 {
		// No law association — record under empty law_id.
		metrics.FrictionTotal.WithLabelValues("", nodeID).Add(magnitude)
		metrics.FrictionEvents.WithLabelValues("", nodeID).Inc()
		return
	}

	for _, lawID := range lawIDs {
		metrics.FrictionTotal.WithLabelValues(lawID, nodeID).Add(magnitude)
		metrics.FrictionEvents.WithLabelValues(lawID, nodeID).Inc()
	}
}

func (s *TelemetrySubscriber) processThresholdCrossing(evt *flowv1.FlowEvent) {
	attrs := evt.GetAttributes()
	lawID := attrs["law_id"]
	tier := attrs["tier"]
	metrics.ThresholdCrossings.WithLabelValues(lawID, tier).Inc()
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
