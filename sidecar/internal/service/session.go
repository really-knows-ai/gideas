// Package service implements the Sidecar's gRPC service handlers.
package service

import (
	"context"
	"sync"
	"time"
)

// DefaultTimeout is the system fallback inactivity timeout for a Workitem
// assignment when no node-level or flow-level timeout is configured.
const DefaultTimeout = 5 * time.Minute

// session tracks the state of a single active Workitem assignment.
// Each assignment has an independent session with its own inactivity
// timer, pause state, and cancellable handler context.
//
// See: specs/03-node/01-sidecar.md#heartbeat-and-activity-tracking
type session struct {
	mu sync.Mutex

	flowID     string
	workitemID string
	nodeID     string

	timeout time.Duration

	// timer is the inactivity timer. When it fires, cancelFn is called
	// to signal the handler to terminate gracefully.
	timer *time.Timer

	// paused indicates whether the inactivity timer is suspended.
	paused bool

	// timedOut is set to true when the inactivity timer fires.
	timedOut bool

	// cancelFn cancels the handler context when inactivity timeout fires.
	cancelFn context.CancelFunc
}

// newSession creates a session for an active Workitem assignment and starts
// the inactivity timer. The returned context is derived from the parent and
// will be cancelled when the inactivity timer fires.
func newSession(
	parent context.Context, flowID, workitemID, nodeID string, timeout time.Duration,
) (*session, context.Context) {
	ctx, cancel := context.WithCancel(parent)
	s := &session{
		flowID:     flowID,
		workitemID: workitemID,
		nodeID:     nodeID,
		timeout:    timeout,
		cancelFn:   cancel,
	}
	s.timer = time.AfterFunc(timeout, func() {
		s.mu.Lock()
		s.timedOut = true
		s.mu.Unlock()
		cancel()
	})
	return s, ctx
}

// resetTimer resets the inactivity timer to the full timeout window.
// No-op if the session is paused or already timed out.
func (s *session) resetTimer() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paused || s.timedOut {
		return
	}
	s.timer.Stop()
	s.timer.Reset(s.timeout)
}

// pause suspends the inactivity timer. While paused, no heartbeat signals
// are required. Returns false if the session is already paused or timed out.
func (s *session) pause() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paused || s.timedOut {
		return false
	}
	s.paused = true
	s.timer.Stop()
	return true
}

// resume resumes the inactivity timer after a pause, resetting to the full
// timeout window. Returns false if the session is not paused or timed out.
func (s *session) resume() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.paused || s.timedOut {
		return false
	}
	s.paused = false
	s.timer.Reset(s.timeout)
	return true
}

// stop cancels the inactivity timer without triggering timeout. Called when
// the handler completes normally.
func (s *session) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timer.Stop()
}

// isPaused returns whether the timer is currently paused.
func (s *session) isPaused() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paused
}

// isTimedOut returns whether the inactivity timer has fired.
func (s *session) isTimedOut() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.timedOut
}
