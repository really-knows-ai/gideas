package eventbus

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
)

// --- test helpers ---

// spyPublisher records all Publish calls and allows injecting errors.
type spyPublisher struct {
	mu    sync.Mutex
	calls []*flowv1.PublishRequest
	err   error

	// publishDelay slows down each Publish call for testing backpressure.
	publishDelay time.Duration
}

func (s *spyPublisher) Publish(_ context.Context, req *flowv1.PublishRequest) (*flowv1.PublishResponse, error) {
	if s.publishDelay > 0 {
		time.Sleep(s.publishDelay)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, req)
	if s.err != nil {
		return nil, s.err
	}
	return &flowv1.PublishResponse{Acknowledged: true, Sequence: uint64(len(s.calls))}, nil
}

func (s *spyPublisher) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *spyPublisher) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *spyPublisher) getCalls() []*flowv1.PublishRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*flowv1.PublishRequest, len(s.calls))
	copy(out, s.calls)
	return out
}

func makeReq(channel, eventType string) *flowv1.PublishRequest {
	return &flowv1.PublishRequest{
		Channel: channel,
		Event: &flowv1.FlowEvent{
			EventId:   "test-id",
			EventType: eventType,
			Channel:   channel,
		},
	}
}

// --- tests ---

func TestAsyncPublisher_SubmitAndDrain(t *testing.T) {
	spy := &spyPublisher{}
	pub := NewAsyncPublisherFromPublisher(spy, WithBufferSize(10))
	defer pub.Stop()

	pub.Submit(makeReq("audit", "audit.test"))

	// Wait for the drain goroutine to publish.
	deadline := time.Now().Add(2 * time.Second)
	for spy.callCount() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	if spy.callCount() != 1 {
		t.Fatalf("expected 1 publish call, got %d", spy.callCount())
	}

	calls := spy.getCalls()
	if calls[0].GetChannel() != "audit" {
		t.Fatalf("expected channel 'audit', got %q", calls[0].GetChannel())
	}
	if calls[0].GetEvent().GetEventType() != "audit.test" {
		t.Fatalf("expected event_type 'audit.test', got %q", calls[0].GetEvent().GetEventType())
	}
}

func TestAsyncPublisher_MultipleEvents(t *testing.T) {
	spy := &spyPublisher{}
	pub := NewAsyncPublisherFromPublisher(spy, WithBufferSize(100))
	defer pub.Stop()

	const n = 50
	for i := range n {
		_ = i
		pub.Submit(makeReq("audit", "audit.batch"))
	}

	deadline := time.Now().Add(5 * time.Second)
	for spy.callCount() < n && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	if spy.callCount() != n {
		t.Fatalf("expected %d publish calls, got %d", n, spy.callCount())
	}
}

func TestAsyncPublisher_DropOnFullBuffer(t *testing.T) {
	spy := &spyPublisher{publishDelay: 50 * time.Millisecond} // slow drain
	pub := NewAsyncPublisherFromPublisher(spy, WithBufferSize(2))

	// Fill the buffer: 2 in channel + 1 being drained = 3 accepted.
	// Then more should be dropped.
	for range 10 {
		pub.Submit(makeReq("audit", "audit.fill"))
	}

	// Give the drain goroutine a moment to start consuming.
	time.Sleep(100 * time.Millisecond)

	dropped := pub.Dropped()
	if dropped == 0 {
		t.Fatal("expected some events to be dropped when buffer is full")
	}

	pub.Stop()
}

func TestAsyncPublisher_DropCallbackInvoked(t *testing.T) {
	spy := &spyPublisher{publishDelay: 100 * time.Millisecond}
	var dropCount atomic.Int64

	pub := NewAsyncPublisherFromPublisher(spy,
		WithBufferSize(1),
		WithOnDrop(func(req *flowv1.PublishRequest) {
			dropCount.Add(1)
		}),
	)

	// Submit enough to guarantee drops.
	for range 20 {
		pub.Submit(makeReq("audit", "audit.drop"))
	}

	time.Sleep(50 * time.Millisecond)
	pub.Stop()

	if dropCount.Load() == 0 {
		t.Fatal("expected OnDrop callback to be invoked at least once")
	}
	if pub.Dropped() != dropCount.Load() {
		t.Fatalf("Dropped() = %d, but OnDrop callback count = %d", pub.Dropped(), dropCount.Load())
	}
}

func TestAsyncPublisher_RetryOnFailure(t *testing.T) {
	spy := &spyPublisher{}
	failErr := errors.New("transient failure")
	spy.setErr(failErr)

	pub := NewAsyncPublisherFromPublisher(spy,
		WithBufferSize(10),
		WithRetry(10*time.Millisecond, 50*time.Millisecond), // fast retry for test
	)

	pub.Submit(makeReq("audit", "audit.retry"))

	// Let it fail a few times.
	time.Sleep(100 * time.Millisecond)
	failedAttempts := spy.callCount()
	if failedAttempts < 2 {
		t.Fatalf("expected at least 2 retry attempts, got %d", failedAttempts)
	}

	// Clear the error — next retry should succeed.
	spy.setErr(nil)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		// Check that no more calls are happening (drained successfully).
		before := spy.callCount()
		time.Sleep(100 * time.Millisecond)
		after := spy.callCount()
		if after == before {
			break
		}
	}

	pub.Stop()

	// Verify dropped count is 0 (retried, not dropped).
	if pub.Dropped() != 0 {
		t.Fatalf("expected 0 drops (retried instead), got %d", pub.Dropped())
	}
}

func TestAsyncPublisher_StopDrainsRemaining(t *testing.T) {
	spy := &spyPublisher{publishDelay: 20 * time.Millisecond}
	pub := NewAsyncPublisherFromPublisher(spy, WithBufferSize(100))

	// Submit a bunch of events.
	for range 10 {
		pub.Submit(makeReq("audit", "audit.drain"))
	}

	// Stop should drain remaining events.
	pub.Stop()

	// All non-dropped events should have been published.
	total := spy.callCount() + int(pub.Dropped())
	if total != 10 {
		t.Fatalf("expected published + dropped = 10, got published=%d dropped=%d",
			spy.callCount(), pub.Dropped())
	}
}

func TestAsyncPublisher_NonBlocking(t *testing.T) {
	spy := &spyPublisher{publishDelay: 100 * time.Millisecond}
	// Tiny buffer, slow drain — should still never block.
	pub := NewAsyncPublisherFromPublisher(spy, WithBufferSize(1))

	done := make(chan struct{})
	go func() {
		for range 200 {
			pub.Submit(makeReq("audit", "audit.nb"))
		}
		close(done)
	}()

	select {
	case <-done:
		// Non-blocking: all submits completed quickly.
	case <-time.After(time.Second):
		t.Fatal("Submit blocked — buffer is not non-blocking")
	}

	pub.Stop()
}

func TestAsyncPublisher_ConcurrentSubmitSafety(t *testing.T) {
	spy := &spyPublisher{}
	pub := NewAsyncPublisherFromPublisher(spy, WithBufferSize(1000))

	var wg sync.WaitGroup
	const goroutines = 10
	const perGoroutine = 100

	for range goroutines {
		wg.Go(func() {
			for range perGoroutine {
				pub.Submit(makeReq("audit", "audit.concurrent"))
			}
		})
	}

	wg.Wait()

	// Wait for drain.
	deadline := time.Now().Add(5 * time.Second)
	expected := goroutines * perGoroutine
	for spy.callCount()+int(pub.Dropped()) < expected && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	pub.Stop()

	total := spy.callCount() + int(pub.Dropped())
	if total != expected {
		t.Fatalf("expected total (published + dropped) = %d, got published=%d dropped=%d",
			expected, spy.callCount(), pub.Dropped())
	}
}

func TestAsyncPublisher_StopIdempotent(t *testing.T) {
	spy := &spyPublisher{}
	pub := NewAsyncPublisherFromPublisher(spy, WithBufferSize(10))

	pub.Submit(makeReq("audit", "audit.stop"))
	pub.Stop()
	pub.Stop() // Second stop should not panic.
}

func TestAsyncPublisher_ZeroBufferSize_UsesDefault(t *testing.T) {
	spy := &spyPublisher{}
	pub := NewAsyncPublisherFromPublisher(spy, WithBufferSize(0))
	defer pub.Stop()

	// Should use DefaultBufferSize. Just verify it works.
	pub.Submit(makeReq("audit", "audit.default"))

	deadline := time.Now().Add(2 * time.Second)
	for spy.callCount() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	if spy.callCount() != 1 {
		t.Fatalf("expected 1 publish call, got %d", spy.callCount())
	}
}

func TestAsyncPublisher_RetryBackoffRespected(t *testing.T) {
	spy := &spyPublisher{}
	failErr := errors.New("always fail")
	spy.setErr(failErr)

	base := 20 * time.Millisecond

	pub := NewAsyncPublisherFromPublisher(spy,
		WithBufferSize(10),
		WithRetry(base, 100*time.Millisecond),
	)

	pub.Submit(makeReq("audit", "audit.backoff"))

	// After ~150ms with 20ms base and doubling: attempts at 0ms, 20ms, 60ms, 120ms ≈ 4 attempts.
	time.Sleep(150 * time.Millisecond)
	attempts := spy.callCount()

	// Should be 3-5 attempts with exponential backoff, not many more
	// (which would indicate no backoff).
	if attempts < 2 || attempts > 8 {
		t.Fatalf("expected 2-8 retry attempts with exponential backoff, got %d", attempts)
	}

	pub.Stop()
}

func TestNewAsyncPublisher_WithGRPCClient(t *testing.T) {
	// Test that NewAsyncPublisher (taking FlowEventBusServiceClient) works
	// through the adapter. We use a minimal implementation.
	spy := &grpcClientSpy{}
	pub := NewAsyncPublisher(spy, WithBufferSize(10))
	defer pub.Stop()

	pub.Submit(makeReq("telemetry", "test.grpc"))

	deadline := time.Now().Add(2 * time.Second)
	for spy.callCount() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	if spy.callCount() != 1 {
		t.Fatalf("expected 1 publish call via gRPC client adapter, got %d", spy.callCount())
	}
}

// grpcClientSpy implements flowv1.FlowEventBusServiceClient for testing the
// NewAsyncPublisher constructor path.
type grpcClientSpy struct {
	flowv1.FlowEventBusServiceClient

	mu    sync.Mutex
	calls int
}

func (s *grpcClientSpy) Publish(
	_ context.Context, _ *flowv1.PublishRequest, _ ...grpc.CallOption,
) (*flowv1.PublishResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return &flowv1.PublishResponse{Acknowledged: true}, nil
}

func (s *grpcClientSpy) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}
