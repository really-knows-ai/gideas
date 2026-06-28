// Package eventbus provides a shared async publisher for the Flow Event Bus.
//
// The AsyncPublisher buffers [flowv1.PublishRequest] messages in a channel and
// drains them in a background goroutine with exponential-backoff retry. Callers
// use the non-blocking [AsyncPublisher.Submit] method so that Event Bus latency
// is never on the critical path of RPC handlers or reconcile loops.
//
// This package replaces the ad-hoc synchronous publishAudit() helpers found in
// the operator, archivist, librarian, and hearing-trigger modules, and provides
// the generic core that the sidecar's TelemetryBuffer composes for
// priority-queue telemetry publishing.
package eventbus

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

const (
	// DefaultBufferSize is the default capacity of the internal channel.
	DefaultBufferSize = 1024

	// defaultRetryBase is the initial backoff delay on publish failure.
	defaultRetryBase = 100 * time.Millisecond

	// defaultRetryMax caps the exponential backoff.
	defaultRetryMax = 5 * time.Second
)

// Option configures an [AsyncPublisher].
type Option func(*AsyncPublisher)

// WithBufferSize sets the capacity of the internal buffered channel.
// If size <= 0, [DefaultBufferSize] is used.
func WithBufferSize(size int) Option {
	return func(p *AsyncPublisher) {
		if size > 0 {
			p.bufSize = size
		}
	}
}

// AsyncPublisher buffers [flowv1.PublishRequest] messages and drains them
// asynchronously via a single background goroutine with exponential-backoff
// retry.
//
// Submit is non-blocking: if the buffer is full the event is dropped and
// the drop counter is incremented.
//
// Stop signals the drain goroutine to exit and performs a best-effort flush
// of remaining buffered events (without retry).
type AsyncPublisher struct {
	pub flowv1.FlowEventBusServiceClient

	bufSize int

	ch     chan *flowv1.PublishRequest
	stopCh chan struct{}
	wg     sync.WaitGroup

	dropped atomic.Int64
}

// NewAsyncPublisher creates and starts a new AsyncPublisher. The background
// drain goroutine begins immediately.
func NewAsyncPublisher(client flowv1.FlowEventBusServiceClient, opts ...Option) *AsyncPublisher {
	p := &AsyncPublisher{
		pub:     client,
		bufSize: DefaultBufferSize,
	}
	for _, o := range opts {
		o(p)
	}

	p.ch = make(chan *flowv1.PublishRequest, p.bufSize)
	p.stopCh = make(chan struct{})

	p.wg.Add(1)
	go p.drainLoop()

	return p
}

// Submit enqueues a publish request for async delivery. Non-blocking: if
// the buffer is full, the event is dropped and the drop counter is
// incremented.
func (p *AsyncPublisher) Submit(req *flowv1.PublishRequest) {
	select {
	case p.ch <- req:
	default:
		p.dropped.Add(1)
		slog.Warn("AsyncPublisher buffer full, event dropped",
			"channel", req.GetChannel(),
			"event_type", req.GetEvent().GetEventType(),
			"dropped_total", p.dropped.Load(),
		)
	}
}

// Stop signals the drain goroutine to exit and waits for it to finish.
// Remaining buffered events are flushed on a best-effort basis (no retry).
// Stop is safe to call multiple times but only the first call has effect.
func (p *AsyncPublisher) Stop() {
	// close is not idempotent — guard with sync.Once semantics via
	// select on the already-closed channel.
	select {
	case <-p.stopCh:
		// Already stopped.
	default:
		close(p.stopCh)
	}
	p.wg.Wait()
}

// Dropped returns the total number of events dropped due to a full buffer.
func (p *AsyncPublisher) Dropped() int64 {
	return p.dropped.Load()
}

// drainLoop consumes events from the buffered channel and publishes them
// with retry. It exits when stopCh is closed, after draining remaining
// events best-effort.
func (p *AsyncPublisher) drainLoop() {
	defer p.wg.Done()

	for {
		select {
		case req := <-p.ch:
			p.publishWithRetry(req)
		case <-p.stopCh:
			p.drainRemaining()
			return
		}
	}
}

// drainRemaining flushes any events left in the buffer after Stop is
// called. Each event gets a single publish attempt (no retry).
func (p *AsyncPublisher) drainRemaining() {
	for {
		select {
		case req := <-p.ch:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if _, err := p.pub.Publish(ctx, req); err != nil {
				slog.Warn("AsyncPublisher drain-remaining publish failed",
					"error", err,
					"channel", req.GetChannel(),
				)
			}
			cancel()
		default:
			return
		}
	}
}

// publishWithRetry publishes a single request, retrying with exponential
// backoff on failure until success or shutdown.
func (p *AsyncPublisher) publishWithRetry(req *flowv1.PublishRequest) {
	delay := defaultRetryBase
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := p.pub.Publish(ctx, req)
		cancel()

		if err == nil {
			return
		}

		slog.Warn("AsyncPublisher publish failed, retrying",
			"error", err,
			"channel", req.GetChannel(),
			"retry_delay", delay,
		)

		select {
		case <-time.After(delay):
			delay *= 2
			if delay > defaultRetryMax {
				delay = defaultRetryMax
			}
		case <-p.stopCh:
			return
		}
	}
}
