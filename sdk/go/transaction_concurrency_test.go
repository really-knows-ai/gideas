package flow

import (
	"context"
	"sync"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/protobuf/types/known/durationpb"
)

// TestTransaction_ConcurrentLifecycle pins that a single Transaction handle is
// safe to share across goroutines: the terminal/lifecycle fields (committed,
// rolledBack, timeout) are guarded by a mutex while every method reads them
// via checkTerminal/Timeout. Run under the race detector (-race) this test
// fails on the unsynchronized-field data race the guard removes; without the
// guard the deterministic assertions below are also violated.
func TestTransaction_ConcurrentLifecycle(t *testing.T) {
	mock := &mockCartographerClient{
		commitTx: func(
			ctx context.Context,
			req *flowv1.CommitTransactionRequest,
		) (*flowv1.CommitTransactionResponse, error) {
			return &flowv1.CommitTransactionResponse{}, nil
		},
		rollbackTx: func(
			ctx context.Context,
			req *flowv1.RollbackTransactionRequest,
		) (*flowv1.RollbackTransactionResponse, error) {
			return &flowv1.RollbackTransactionResponse{}, nil
		},
		extendTimeout: func(
			ctx context.Context,
			req *flowv1.ExtendTimeoutRequest,
		) (*flowv1.ExtendTimeoutResponse, error) {
			return &flowv1.ExtendTimeoutResponse{AppliedTimeout: durationpb.New(1 * time.Hour)}, nil
		},
	}

	// Phase 1: concurrent ExtendTimeout/Timeout on a non-terminal handle.
	// Every ExtendTimeout succeeds (nothing sets the handle terminal in this
	// phase), so the final timeout is deterministically the applied 1h.
	tx := newMockTx(mock)
	runConcurrently(t, 8, func(i int) {
		if i%2 == 0 {
			if _, err := tx.ExtendTimeout(1 * time.Hour); err != nil {
				t.Errorf("concurrent ExtendTimeout failed: %v", err)
			}
			return
		}
		_ = tx.Timeout()
	})
	if got := tx.Timeout(); got != 1*time.Hour {
		t.Errorf("expected timeout 1h after concurrent ExtendTimeout, got %v", got)
	}

	// Phase 2: concurrent Commit/Rollback on the same handle. At least one
	// transition reaches the wire, so the handle is deterministically terminal.
	runConcurrently(t, 8, func(i int) {
		if i%2 == 0 {
			_ = tx.Commit()
			return
		}
		_ = tx.Rollback()
	})
	if tx.checkTerminal() == nil {
		t.Error("expected the handle to be terminal after concurrent Commit/Rollback")
	}
}

// runConcurrently starts n goroutines calling fn(i) on a start barrier and
// fails the test if they do not all finish within a generous timeout (a
// deadlocked lifecycle lock would wedge the test, not hang the suite).
func runConcurrently(t *testing.T, n int, fn func(i int)) {
	t.Helper()
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			fn(i)
		}(i)
	}
	close(start)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent transaction lifecycle calls deadlocked")
	}
}
