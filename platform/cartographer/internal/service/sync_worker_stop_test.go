package service

import (
	"sync"
	"testing"
)

// TestSyncWorkerStop_ConcurrentCalls pins that Stop() is safe under concurrent
// callers: the stop signal must be closed exactly once (sync.Once), so racing
// Stop() calls cannot double-close stopCh and panic with "close of closed
// channel" (the select/default close-once idiom is a data race). All callers
// must still block until the worker loop has actually exited.
func TestSyncWorkerStop_ConcurrentCalls(t *testing.T) {
	clk := &intervalRecordingClock{}
	w := NewSyncWorker("", nil, nil, clk)
	go w.Run()
	// Cleanup is a second Stop() after the test's own — exercising the
	// already-stopped path and guarding against a leaked worker if the test
	// fails early.
	t.Cleanup(w.Stop)
	// Wait for the startup no-op cycle to finish and the loop to park in its
	// select, so the concurrent Stop() calls race against a live loop.
	waitFor(t, func() bool {
		_, ok := clk.created()
		return ok
	}, "worker to create its ticker")

	const callers = 16
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			w.Stop()
		}()
	}
	wg.Wait()

	// The stop signal is closed exactly once — a double close would have
	// panicked during the race above — and the loop exited.
	select {
	case <-w.stopCh:
	default:
		t.Fatal("stopCh not closed after Stop()")
	}
	select {
	case <-w.doneCh:
	default:
		t.Fatal("worker loop did not exit after Stop()")
	}
}
