// Package buffer provides an async priority buffer for telemetry events
// published to the Event Bus via the Sidecar.
//
// The buffer ensures non-blocking submission for callers (nodes never block
// on telemetry delivery) and prioritises friction events (HIGH) over custom
// telemetry (NORMAL). When the NORMAL buffer is full, oldest NORMAL events
// are dropped. Friction events are only dropped when the HIGH buffer is full.
//
// See: specs/04-sdk/06-sdk-telemetry.md
package buffer

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gideas/flow/sidecar/internal/proxy"
)

// Priority levels for telemetry events.
const (
	PriorityHigh   = iota // Friction events
	PriorityNormal        // Custom telemetry
)

const (
	// DefaultBufferSize is the default capacity per priority channel.
	DefaultBufferSize = 1000

	// retryBaseDelay is the initial backoff delay for Event Bus retries.
	retryBaseDelay = 500 * time.Millisecond
	// retryMaxDelay caps the exponential backoff.
	retryMaxDelay = 30 * time.Second
)

// Event represents a telemetry event queued for async delivery.
type Event struct {
	// Priority is PriorityHigh for friction or PriorityNormal for telemetry.
	Priority int

	// --- Friction fields (Priority == PriorityHigh) ---
	FlowID     string
	WorkitemID string
	NodeID     string
	LawIDs     []string
	Magnitude  float64

	// --- Telemetry fields (Priority == PriorityNormal) ---
	EventType string
	Payload   []byte
}

// TelemetryBuffer is an async priority buffer for telemetry events.
// It drains events in priority order (HIGH first) and publishes them to
// the Event Bus via the EventBusProxy.
type TelemetryBuffer struct {
	ebProxy *proxy.EventBusProxy

	highCh   chan Event
	normalCh chan Event

	stopCh chan struct{}
	wg     sync.WaitGroup

	// droppedNormal tracks the number of dropped normal-priority events.
	droppedNormal atomic.Int64
	// droppedHigh tracks the number of dropped high-priority events.
	droppedHigh atomic.Int64
}

// NewTelemetryBuffer creates a buffer with the given capacity per channel.
// If size <= 0, DefaultBufferSize is used.
func NewTelemetryBuffer(ebProxy *proxy.EventBusProxy, size int) *TelemetryBuffer {
	if size <= 0 {
		size = DefaultBufferSize
	}
	tb := &TelemetryBuffer{
		ebProxy:  ebProxy,
		highCh:   make(chan Event, size),
		normalCh: make(chan Event, size),
		stopCh:   make(chan struct{}),
	}
	tb.wg.Add(1)
	go tb.drainLoop()
	return tb
}

// Submit enqueues an event for async delivery. Non-blocking: if the
// buffer for the event's priority is full, the event is dropped.
func (tb *TelemetryBuffer) Submit(evt Event) {
	switch evt.Priority {
	case PriorityHigh:
		select {
		case tb.highCh <- evt:
		default:
			tb.droppedHigh.Add(1)
			slog.Warn("High-priority telemetry buffer full, event dropped",
				"flow_id", evt.FlowID,
				"dropped_total", tb.droppedHigh.Load(),
			)
		}
	default:
		select {
		case tb.normalCh <- evt:
		default:
			tb.droppedNormal.Add(1)
			slog.Warn("Normal-priority telemetry buffer full, event dropped",
				"event_type", evt.EventType,
				"dropped_total", tb.droppedNormal.Load(),
			)
		}
	}
}

// Stop signals the drain goroutine to exit and waits for it to finish.
func (tb *TelemetryBuffer) Stop() {
	close(tb.stopCh)
	tb.wg.Wait()
}

// DroppedNormal returns the number of dropped normal-priority events.
func (tb *TelemetryBuffer) DroppedNormal() int64 {
	return tb.droppedNormal.Load()
}

// DroppedHigh returns the number of dropped high-priority events.
func (tb *TelemetryBuffer) DroppedHigh() int64 {
	return tb.droppedHigh.Load()
}

// drainLoop consumes events from both channels, prioritising HIGH,
// and publishes them to the Event Bus. On failure, retries with
// exponential backoff per spec requirement.
func (tb *TelemetryBuffer) drainLoop() {
	defer tb.wg.Done()

	for {
		// Priority drain: exhaust HIGH before NORMAL.
		select {
		case evt := <-tb.highCh:
			tb.publishWithRetry(evt)
			continue
		default:
		}

		select {
		case evt := <-tb.highCh:
			tb.publishWithRetry(evt)
		case evt := <-tb.normalCh:
			tb.publishWithRetry(evt)
		case <-tb.stopCh:
			// Drain remaining events on shutdown.
			tb.drainRemaining()
			return
		}
	}
}

// drainRemaining flushes any queued events before exit.
func (tb *TelemetryBuffer) drainRemaining() {
	for {
		select {
		case evt := <-tb.highCh:
			_ = tb.publish(evt)
		case evt := <-tb.normalCh:
			_ = tb.publish(evt)
		default:
			return
		}
	}
}

// publishWithRetry publishes an event, retrying with exponential backoff
// on failure until success or shutdown.
func (tb *TelemetryBuffer) publishWithRetry(evt Event) {
	delay := retryBaseDelay
	for {
		err := tb.publish(evt)
		if err == nil {
			return
		}

		slog.Warn("Event Bus publish failed, retrying",
			"error", err,
			"retry_delay", delay,
		)

		select {
		case <-time.After(delay):
			delay *= 2
			if delay > retryMaxDelay {
				delay = retryMaxDelay
			}
		case <-tb.stopCh:
			return
		}
	}
}

// publish sends a single event to the Event Bus via the proxy.
func (tb *TelemetryBuffer) publish(evt Event) error {
	ctx := context.Background()
	switch evt.Priority {
	case PriorityHigh:
		return tb.ebProxy.PublishFriction(
			ctx,
			evt.FlowID, evt.WorkitemID, evt.NodeID,
			evt.LawIDs, evt.Magnitude,
		)
	default:
		return tb.ebProxy.PublishTelemetry(
			ctx,
			evt.FlowID, evt.NodeID, evt.WorkitemID,
			evt.EventType, evt.Payload,
		)
	}
}
