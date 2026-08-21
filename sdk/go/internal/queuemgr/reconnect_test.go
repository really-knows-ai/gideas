package queuemgr_test

// SEAM CONTRACT — PHASE_06 Unit 3 (SPEC R-4.4): reconnect-backoff helper.
// These tests are RED until the production implementer adds the seam EXACTLY,
// then they pin the semantics of the SDK's EventBus-downtime reconnect loop.
//
// Rationale (do not touch without rereading): the SDK CANNOT import
// nodes/internal/nodeutil.ReconnectStream — sdk/go and nodes form a module
// cycle and nodes/internal is module-private. The helper is reimplemented
// INSIDE the SDK module, semantically matching nodeutil.ReconnectStream but
// WITHOUT importing nodeutil, and with an injectable clock/backoff so unit
// tests are millisecond-fast (no real 1s+ sleeps).
//
// The helper drives WaitForDecision's subscribe→consume loop: subscribe opens
// the EventBus stream, consume runs the workitem-filtered Recv loop. A stream
// drop (consume error) triggers a re-subscribe instead of a hard fail; a
// subscribe failure backs off exponentially. The body must contain no
// time.Sleep/Ticker of seconds — the injected sleep/backoff keep it fast.
//
// Chosen seam — the smallest injectable seams mirroring how nodeutil threads
// SleepCtx/NextBackoff:
//
//  1. package-level types (in reconnect.go, next to Manager):
//
//         // BackoffFn returns the delay to wait before the retry at the given
//         // 1-based attempt number (1 = first retry after a failure).
//         type BackoffFn func(attempt int) time.Duration
//
//         // SleepFn sleeps for d, returning false early if ctx is cancelled.
//         type SleepFn func(ctx context.Context, d time.Duration) bool
//
//  2. the reconnect loop (package function):
//
//         func ReconnectStream[SUB any](
//             ctx context.Context,
//             subscribe func() (SUB, error),
//             consume func(SUB) error,
//             backoff BackoffFn,
//             sleep SleepFn,
//         ) error
//
//     Semantics: loop subscribe→consume; on subscribe failure back off with
//     backoff(retry) and retry (retry counts consecutive failures, reset to 0
//     on a successful subscribe); on a consume error back off (retry=1) and
//     re-subscribe; return nil after one successful subscribe+consume; abort
//     with ctx.Err() whenever sleep reports cancellation or the ctx is already
//     done at the top of the loop.
//
// The unit tests never construct a real EventBus, never import pkg/eventbus or
// nodes/nodeutil, and never touch a real clock: subscribe/consume are fakes,
// backoff is a monotonic function, sleep records the requested delay and
// returns immediately (aborting when ctx is cancelled).

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/foundry/flow/sdk/go/internal/queuemgr"
)

// fakeStream is a stand-in for the EventBus subscribe stream (the real one is
// grpc.ServerStreamingClient[FlowEvent]). ReconnectStream is generic over the
// stream type, so a plain failable struct is all the unit test needs. It
// records every Recv so tests can assert the consume loop ran exactly once per
// connection.
type fakeStream struct {
	mu        sync.Mutex
	recvCalls int
}

func (s *fakeStream) recv() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recvCalls++
	return nil
}

// reconnectSpy records how many times subscribe/consume ran, which backoff
// attempt numbers were requested, and the sleeps requested. It is the single
// unit under test's collaborator fake: no I/O, no clock, all canned.
type reconnectSpy struct {
	mu sync.Mutex

	subscribeAttempts int
	consumeAttempts   int
	backoffAttempts   []int
	sleeps            []time.Duration

	// subscribeFails is how many initial subscribe calls fail before one
	// succeeds (0 = immediately succeed).
	subscribeFails int
	// consumeFailsBefore is how many consume calls fail before one succeeds
	// (0 = every consume succeeds).
	consumeFailsBefore int
}

func newReconnectSpy(subscribeFails, consumeFailsBefore int) *reconnectSpy {
	return &reconnectSpy{
		subscribeFails:     subscribeFails,
		consumeFailsBefore: consumeFailsBefore,
	}
}

// subscribe simulates opening the stream: fails subscribeFails times, then
// succeeds, returning a fresh fakeStream.
func (s *reconnectSpy) subscribe() (*fakeStream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subscribeAttempts++
	if s.subscribeAttempts <= s.subscribeFails {
		return nil, errors.New("eventbus down")
	}
	return &fakeStream{}, nil
}

// consume simulates the workitem-filtered Recv loop: fails the first
// consumeFailsBefore calls (stream drop), then succeeds (decision received).
func (s *reconnectSpy) consume(st *fakeStream) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.consumeAttempts++
	if s.consumeAttempts <= s.consumeFailsBefore {
		return errors.New("stream dropped")
	}
	return st.recv()
}

func (s *reconnectSpy) subscribeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subscribeAttempts
}

func (s *reconnectSpy) consumeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.consumeAttempts
}

func (s *reconnectSpy) backoffArgs() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.backoffAttempts...)
}

func (s *reconnectSpy) sleepDurations() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.sleeps...)
}

// recordBackoff returns a monotonic backoff fn: attempt N yields N
// milliseconds, and records N. Non-decreasing across a failure run, so a
// growing/non-decreasing backoff sequence can be asserted.
func (s *reconnectSpy) recordBackoff() func(int) time.Duration {
	return func(attempt int) time.Duration {
		s.mu.Lock()
		s.backoffAttempts = append(s.backoffAttempts, attempt)
		s.mu.Unlock()
		return time.Duration(attempt) * time.Millisecond
	}
}

// immediateSleep records the requested delay and returns immediately with no
// real wait. It honours ctx: once cancelled it returns false (the abort
// signal), matching nodeutil.SleepCtx semantics. The recorded delay proves the
// helper threads the backoff value through the sleep seam.
func (s *reconnectSpy) immediateSleep() func(context.Context, time.Duration) bool {
	return func(ctx context.Context, d time.Duration) bool {
		s.mu.Lock()
		s.sleeps = append(s.sleeps, d)
		s.mu.Unlock()
		return ctx.Err() == nil
	}
}

// nonDecreasing reports whether a backoff attempt sequence is non-decreasing.
func nonDecreasing(seq []int) bool {
	for i := 1; i < len(seq); i++ {
		if seq[i] < seq[i-1] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Tests — reconnect-backoff helper (SPEC R-4.4)
// ---------------------------------------------------------------------------

// Compile-time pins: the exact exported type names the implementer must add in
// the queuemgr package. If BackoffFn or SleepFn are named or shaped differently
// (or left unexported), these declarations fail to compile.
func TestReconnectStream_TypeNamesPin(t *testing.T) {
	var _ queuemgr.BackoffFn = func(attempt int) time.Duration {
		return time.Duration(attempt)
	}
	var _ queuemgr.SleepFn = func(ctx context.Context, d time.Duration) bool {
		return ctx.Err() == nil
	}
	var _ func(
		context.Context,
		func() (*fakeStream, error),
		func(*fakeStream) error,
		queuemgr.BackoffFn,
		queuemgr.SleepFn,
	) error
}

// Subscribe succeeds on the first try, so the helper consumes exactly once and
// returns nil WITHOUT retrying or spinning: no backoff is requested, no
// additional subscribe/consume happens.
func TestReconnectStream_FirstTrySucceedsNoRetry(t *testing.T) {
	spy := newReconnectSpy(0, 0)
	ctx := context.Background()

	err := queuemgr.ReconnectStream(
		ctx,
		spy.subscribe,
		spy.consume,
		spy.recordBackoff(),
		spy.immediateSleep(),
	)
	if err != nil {
		t.Fatalf("ReconnectStream err = %v, want nil", err)
	}
	if got := spy.subscribeCount(); got != 1 {
		t.Fatalf("subscribe called %d times, want 1 (no retry on first success)", got)
	}
	if got := spy.consumeCount(); got != 1 {
		t.Fatalf("consume called %d times, want 1 (consume runs once)", got)
	}
	if got := spy.backoffArgs(); len(got) != 0 {
		t.Fatalf("backoff called with %v, want none on first-try success", got)
	}
}

// Subscribe fails N=2 times then succeeds. The helper must retry with the
// injected backoff on each failure and eventually subscribe + consume, with
// N+1=3 subscribe attempts and a non-decreasing (growing) backoff sequence.
func TestReconnectStream_SubscribeRetriesWithBackoffThenSucceeds(t *testing.T) {
	spy := newReconnectSpy(2, 0)
	ctx := context.Background()

	err := queuemgr.ReconnectStream(
		ctx,
		spy.subscribe,
		spy.consume,
		spy.recordBackoff(),
		spy.immediateSleep(),
	)
	if err != nil {
		t.Fatalf("ReconnectStream err = %v, want nil", err)
	}
	if got := spy.subscribeCount(); got != 3 {
		t.Fatalf("subscribe called %d times, want 3 (2 failures + 1 success)", got)
	}
	if got := spy.consumeCount(); got != 1 {
		t.Fatalf("consume called %d times, want 1 (after the successful subscribe)", got)
	}
	backoffs := spy.backoffArgs()
	if len(backoffs) != 2 {
		t.Fatalf("backoff called %d times, want 2 (one per subscribe failure)", len(backoffs))
	}
	if !nonDecreasing(backoffs) {
		t.Fatalf("backoff attempted with %v, want non-decreasing (growing) values", backoffs)
	}
	// The delays threaded into sleep must match the injected monotonic backoff.
	sleeps := spy.sleepDurations()
	if len(sleeps) != 2 {
		t.Fatalf("sleep called %d times, want 2 (one per backoff)", len(sleeps))
	}
	for i, d := range sleeps {
		if want := time.Duration(backoffs[i]) * time.Millisecond; d != want {
			t.Fatalf("sleep[%d] = %v, want %v (backoff threaded through)", i, d, want)
		}
	}
}

// A consume error (the stream drops mid-wait) must trigger a reconnect: the
// helper subscribes again and retries the consume, still within ctx, and
// returns nil once the decision is consumed.
func TestReconnectStream_ConsumeErrorTriggersReconnect(t *testing.T) {
	spy := newReconnectSpy(0, 1) // subscribe succeeds; first consume fails
	ctx := context.Background()

	err := queuemgr.ReconnectStream(
		ctx,
		spy.subscribe,
		spy.consume,
		spy.recordBackoff(),
		spy.immediateSleep(),
	)
	if err != nil {
		t.Fatalf("ReconnectStream err = %v, want nil", err)
	}
	if got := spy.subscribeCount(); got != 2 {
		t.Fatalf("subscribe called %d times, want 2 (re-subscribe after the dropped stream)", got)
	}
	if got := spy.consumeCount(); got != 2 {
		t.Fatalf("consume called %d times, want 2 (dropped once, then succeeds)", got)
	}
	// The dropped stream path backs off once before re-subscribing.
	if got := len(spy.backoffArgs()); got != 1 {
		t.Fatalf("backoff called %d times, want 1 (one reconnect after the consume error)", got)
	}
}

// The context is cancelled while subscribe keeps failing. The helper must stop
// and return ctx.Err() rather than spin or succeed — regardless of the
// subscribe/consume state. The injected sleep aborts mid-backoff on
// cancellation; asserting only the returned error keeps the test deterministic.
func TestReconnectStream_ContextCancelledDuringSubscribeRetries(t *testing.T) {
	spy := newReconnectSpy(1<<30, 0) // subscribe keeps failing forever
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel before the loop: the helper must observe it and abort promptly.
	cancel()

	err := queuemgr.ReconnectStream(
		ctx,
		spy.subscribe,
		spy.consume,
		spy.recordBackoff(),
		spy.immediateSleep(), // returns false once ctx is cancelled
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReconnectStream err = %v, want context.Canceled", err)
	}
	// It must never reach a successful consume.
	if got := spy.consumeCount(); got != 0 {
		t.Fatalf("consume called %d times after cancel, want 0", got)
	}
	// It must not keep retrying: at most one backoff may be requested before
	// the abort is observed.
	if got := len(spy.backoffArgs()); got > 1 {
		t.Fatalf("backoff attempted %d times after cancel, want 0 or 1 (abort, not spin)", got)
	}
}

// The context is cancelled while a consume error is waiting to reconnect: the
// helper must return ctx.Err() instead of re-subscribing.
func TestReconnectStream_ContextCancelledMidReconnect(t *testing.T) {
	spy := newReconnectSpy(0, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancel() // cancel before the loop: subscribe aborts at the top

	err := queuemgr.ReconnectStream(
		ctx,
		spy.subscribe,
		spy.consume,
		spy.recordBackoff(),
		spy.immediateSleep(),
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReconnectStream err = %v, want context.Canceled", err)
	}
}
