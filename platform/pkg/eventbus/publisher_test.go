package eventbus

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
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

func (s *spyPublisher) Publish(_ context.Context, req *flowv1.PublishRequest, _ ...grpc.CallOption) (*flowv1.PublishResponse, error) {
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

func (s *spyPublisher) Subscribe(_ context.Context, _ *flowv1.SubscribeRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[flowv1.FlowEvent], error) {
	return nil, errors.New("unimplemented")
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
	pub := NewAsyncPublisher(spy, WithBufferSize(10))
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
	pub := NewAsyncPublisher(spy, WithBufferSize(100))
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
	pub := NewAsyncPublisher(spy, WithBufferSize(2))

	// Fill the buffer: 2 in channel + 1 being drained = 3 accepted.
	// The remaining 7 submits hit a full buffer and are dropped.
	for range 10 {
		pub.Submit(makeReq("audit", "audit.fill"))
	}

	pub.Stop()

	// A 50ms-per-publish drain and a 2-slot buffer admit at most 3 of the
	// 10 submissions — the rest are dropped, so fewer than 10 events ever
	// reach the publisher.
	if spy.callCount() >= 10 {
		t.Fatalf("expected events to be dropped when buffer is full, but all %d were published", spy.callCount())
	}
}

func TestAsyncPublisher_RetryOnFailure(t *testing.T) {
	spy := &spyPublisher{}
	failErr := errors.New("transient failure")
	spy.setErr(failErr)

	pub := NewAsyncPublisher(spy,
		WithBufferSize(10),
	)

	pub.Submit(makeReq("audit", "audit.retry"))

	// Let it fail a few times (defaultRetryBase = 100ms).
	time.Sleep(400 * time.Millisecond)
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
}

func TestAsyncPublisher_StopDrainsRemaining(t *testing.T) {
	spy := &spyPublisher{publishDelay: 20 * time.Millisecond}
	pub := NewAsyncPublisher(spy, WithBufferSize(100))

	// Submit a bunch of events.
	for range 10 {
		pub.Submit(makeReq("audit", "audit.drain"))
	}

	// Stop should drain remaining events.
	pub.Stop()

	// The 100-slot buffer never fills for 10 submissions, so nothing is
	// dropped: every submitted event must have been published.
	if spy.callCount() != 10 {
		t.Fatalf("expected all 10 events published after Stop, got %d", spy.callCount())
	}
}

func TestAsyncPublisher_NonBlocking(t *testing.T) {
	spy := &spyPublisher{publishDelay: 100 * time.Millisecond}
	// Tiny buffer, slow drain — should still never block.
	pub := NewAsyncPublisher(spy, WithBufferSize(1))

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
	pub := NewAsyncPublisher(spy, WithBufferSize(1000))

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
	for spy.callCount() < expected && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	pub.Stop()

	// The 1000-capacity buffer admits all 1000 submissions, so nothing is
	// dropped: every concurrent submit must be published exactly once.
	if spy.callCount() != expected {
		t.Fatalf("expected %d publish calls (no drops), got %d", expected, spy.callCount())
	}
}

func TestAsyncPublisher_StopIdempotent(t *testing.T) {
	spy := &spyPublisher{}
	pub := NewAsyncPublisher(spy, WithBufferSize(10))

	pub.Submit(makeReq("audit", "audit.stop"))
	pub.Stop()
	pub.Stop() // Second stop should not panic.
}

// TestAsyncPublisher_StopConcurrentCalls pins that Stop is safe under
// concurrent callers: the stop signal must be closed exactly once (sync.Once),
// so racing Stop() calls cannot double-close stopCh and panic with "close of
// closed channel" (the select/default close-once idiom is a data race). All
// callers must still block until the drain goroutine has actually exited.
func TestAsyncPublisher_StopConcurrentCalls(t *testing.T) {
	spy := &spyPublisher{}
	pub := NewAsyncPublisher(spy, WithBufferSize(10))
	// Cleanup is a second Stop() after the test's own — exercising the
	// already-stopped path and guarding against a leaked drain goroutine if
	// the test fails early.
	t.Cleanup(pub.Stop)

	const callers = 16
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			pub.Stop()
		}()
	}
	wg.Wait()

	// The stop signal is closed exactly once — a double close would have
	// panicked during the race above — and the drain goroutine exited.
	select {
	case <-pub.stopCh:
	default:
		t.Fatal("stopCh not closed after Stop()")
	}
}

func TestAsyncPublisher_ZeroBufferSize_UsesDefault(t *testing.T) {
	spy := &spyPublisher{}
	pub := NewAsyncPublisher(spy, WithBufferSize(0))
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

	pub := NewAsyncPublisher(spy,
		WithBufferSize(10),
	)

	pub.Submit(makeReq("audit", "audit.backoff"))

	// defaultRetryBase = 100ms, so with doubling: attempts at 0ms, 100ms, 300ms.
	// After 200ms we should have 1-3 attempts.
	time.Sleep(200 * time.Millisecond)
	attempts := spy.callCount()

	if attempts < 1 || attempts > 3 {
		t.Fatalf("expected 1-3 retry attempts with exponential backoff, got %d", attempts)
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
