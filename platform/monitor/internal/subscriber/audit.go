package subscriber

import (
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"github.com/gideas/flow/monitor/internal/metrics"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

// AuditSubscriber subscribes to the Event Bus audit channel and writes
// each event as a JSON Line to stdout.
type AuditSubscriber struct {
	cs     channelSubscriber
	output *json.Encoder
}

// NewAuditSubscriber returns a subscriber that writes JSON Lines to stdout.
func NewAuditSubscriber(client flowv1.FlowEventBusServiceClient, cp Checkpoint) *AuditSubscriber {
	s := &AuditSubscriber{
		output: json.NewEncoder(os.Stdout),
	}
	s.cs = channelSubscriber{
		client:      client,
		checkpoint:  cp,
		channel:     "audit",
		channelName: "audit",
		handler:     s.processEvent,
		stopCh:      make(chan struct{}),
	}
	return s
}

// Start begins the subscription in a background goroutine.
func (s *AuditSubscriber) Start() {
	s.cs.wg.Go(func() {
		s.cs.loop()
	})
}

// Stop signals the subscription to exit and waits for it.
func (s *AuditSubscriber) Stop() {
	s.cs.stopOnce.Do(func() { close(s.cs.stopCh) })
	s.cs.wg.Wait()
}

// auditLine is the JSON structure for each audit log entry.
type auditLine struct {
	EventID    string            `json:"event_id"`
	Sequence   uint64            `json:"sequence"`
	EventType  string            `json:"event_type"`
	FlowID     string            `json:"flow_id"`
	NodeID     string            `json:"node_id"`
	WorkitemID string            `json:"workitem_id"`
	Timestamp  string            `json:"timestamp"`
	TraceID    string            `json:"trace_id,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// processEvent serialises a FlowEvent as a JSON Line and writes it to stdout.
func (s *AuditSubscriber) processEvent(evt *flowv1.FlowEvent) {
	metrics.AuditEvents.WithLabelValues(evt.GetEventType()).Inc()

	ts := ""
	if evt.GetTimestamp() != nil {
		ts = evt.GetTimestamp().AsTime().UTC().Format(time.RFC3339Nano)
	}

	line := auditLine{
		EventID:    evt.GetEventId(),
		Sequence:   evt.GetSequence(),
		EventType:  evt.GetEventType(),
		FlowID:     evt.GetFlowId(),
		NodeID:     evt.GetNodeId(),
		WorkitemID: evt.GetWorkitemId(),
		Timestamp:  ts,
		TraceID:    evt.GetTraceId(),
		Attributes: evt.GetAttributes(),
	}

	if err := s.output.Encode(line); err != nil {
		slog.Error("Failed to write audit JSON line",
			"event_id", evt.GetEventId(),
			"error", err,
		)
	}
}
