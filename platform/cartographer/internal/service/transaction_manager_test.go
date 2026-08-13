package service

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestExtendTimeout_SerialisesOnLifecycleLock pins that ExtendTimeout performs
// its ExpiresAt/AppliedTimeout mutation (and persist) under the transaction's
// lifecycle lock: while the lock is held, a concurrent ExtendTimeout must block
// and complete only after the lock is released, exactly like LockActive and the
// GC re-check that read those fields under lifecycle. Before the fix,
// ExtendTimeout wrote them under tm.mu alone, so it did not serialize with the
// lifecycle-locked readers — a data race and a stale-expiry window in the GC.
func TestExtendTimeout_SerialisesOnLifecycleLock(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()
	txID := beginTestTx(t, srv, ctx)

	state, err := srv.txManager.Lookup(txID)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}

	state.lifecycle.Lock()
	extended := make(chan error, 1)
	go func() {
		extended <- srv.txManager.ExtendTimeout(txID, 10*time.Minute, nil)
	}()

	select {
	case err := <-extended:
		t.Fatalf("ExtendTimeout did not block on the lifecycle lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	state.lifecycle.Unlock()
	if err := <-extended; err != nil {
		t.Fatalf("ExtendTimeout after lifecycle release: %v", err)
	}
	if state.AppliedTimeout != 10*time.Minute {
		t.Fatalf("expected applied timeout 10m, got %v", state.AppliedTimeout)
	}
}

// TestExtendTimeout_DirectRejectsRollbackOnly pins the manager-level admission:
// a rollback-only transaction (already rolled back by a change-log capacity
// rejection) is rejected with NOT_FOUND — the SPEC error table defines the
// cap-violation outcome solely as RESOURCE_EXHAUSTED with the transaction
// "rolled back", and "Transaction not found" covers "was already
// committed/rolled back" — and is never extended.
func TestExtendTimeout_DirectRejectsRollbackOnly(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()
	txID := beginTestTx(t, srv, ctx)

	state, err := srv.txManager.Lookup(txID)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	oldExpiresAt := state.ExpiresAt
	oldAppliedTimeout := state.AppliedTimeout
	state.RollbackOnly = true

	err = srv.txManager.ExtendTimeout(txID, 10*time.Minute, nil)
	if status.Code(err) != codes.NotFound {
		t.Fatalf("ExtendTimeout on rollback-only transaction = %v, want NotFound", err)
	}
	if state.ExpiresAt != oldExpiresAt || state.AppliedTimeout != oldAppliedTimeout {
		t.Fatalf("rollback-only transaction was extended: expiresAt=%v applied=%v",
			state.ExpiresAt, state.AppliedTimeout)
	}
}

// TestExtendTimeout_DirectRejectsExpired pins that an expired transaction is
// rejected with DEADLINE_EXCEEDED and left unmutated even when ExtendTimeout is
// invoked directly on the manager (SPEC error table "Transaction timed out":
// "Operations on it are rejected immediately") — the extension must not race
// past the timeout guard by taking a stale, un-extended ExpiresAt as its
// baseline.
func TestExtendTimeout_DirectRejectsExpired(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	fc := newFakeClock(time.Now())
	srv.txManager = NewTransactionManager(7*24*time.Hour, 100000, WithClock(fc))
	txID := beginTestTx(t, srv, ctx)

	state, err := srv.txManager.Lookup(txID)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	oldExpiresAt := state.ExpiresAt
	oldAppliedTimeout := state.AppliedTimeout
	fc.Advance(2 * time.Hour)

	err = srv.txManager.ExtendTimeout(txID, 10*time.Minute, nil)
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("ExtendTimeout on expired transaction = %v, want DeadlineExceeded", err)
	}
	if state.ExpiresAt != oldExpiresAt || state.AppliedTimeout != oldAppliedTimeout {
		t.Fatalf("expired transaction was extended: expiresAt=%v applied=%v",
			state.ExpiresAt, state.AppliedTimeout)
	}
}

// TestHasActive_ConcurrentWithExtendTimeout pins that HasActive reads
// ExpiresAt under the transaction's lifecycle lock — the same lock
// ExtendTimeout writes it under — not under tm.mu alone. Before the fix,
// HasActive read state.ExpiresAt while holding only tm.mu.RLock, racing the
// lifecycle-locked write; run under -race this test reports that race, and
// without -race it still completes with the extended transaction always
// reported active (the reader goroutine never sees a false negative).
func TestHasActive_ConcurrentWithExtendTimeout(t *testing.T) {
	tm := NewTransactionManager(7*24*time.Hour, 100000)
	txID := "test-tx-id"
	if _, err := tm.Create(txID, 10*time.Minute, "head"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	done := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 1000 {
			if err := tm.ExtendTimeout(txID, 10*time.Minute, nil); err != nil {
				done <- fmt.Errorf("ExtendTimeout: %w", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range 1000 {
			if !tm.HasActive() {
				done <- fmt.Errorf("HasActive() = false while a live extension loop holds the transaction active")
				return
			}
		}
	}()
	wg.Wait()
	close(done)
	for err := range done {
		t.Fatal(err)
	}
}
