package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
)

func TestTelemetry_TransactionGC(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())

	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000)
	mockPub := &mockTelemetryPublisher{}
	srv.auditor = mockPub
	srv.dbReady.Store(true)

	fc := newFakeClock(time.Now())
	srv.txManager = NewTransactionManager(7*24*time.Hour, 100000)
	srv.txManager.clock = fc
	_, _ = srv.txManager.Create("test-tx-id", 1*time.Minute, "head")

	fc.Advance(2 * time.Minute)
	srv.gcTick()

	events := mockPub.Events()
	if len(events) == 0 {
		t.Fatal("expected at least one telemetry event")
	}
	found := false
	for _, e := range events {
		if e.Event != nil && e.Event.EventType == "cartographer.transaction_gc" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected telemetry event 'cartographer.transaction_gc'")
	}
}

// TestGC_ExpiredTransaction_Rollback pins the gcTick rollback body (SPEC R9:
// "the branch DB, git branch, and change log are rolled back asynchronously
// within the cleanup grace period"; Design → Versioning Architecture → Garbage
// collection: "expired transactions ... are rolled back automatically. Orphaned
// git branches and branches/<tx-id>.lbug files are deleted."). Unlike
// TestTelemetry_TransactionGC, which only observes the telemetry event, this
// test registers a transaction with real branch resources (git branch + branch
// DB, mirroring BeginTransaction), expires it past the 30-second cleanup grace
// period, runs gcTick, and asserts every rollback observable: the branch DB is
// dropped, the git branch is deleted, and the transaction — whose change log
// is owned by its registration (TransactionState.ChangeLog) — is deregistered.
// Deleting the rollback body of gcTick (cartographer_server.go) would fail
// this test.
func TestGC_ExpiredTransaction_Rollback(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, _ := generateTestKey()
	st, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}

	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000)
	mockPub := &mockTelemetryPublisher{}
	srv.auditor = mockPub
	srv.dbReady.Store(true)

	fc := newFakeClock(time.Now())
	srv.txManager = NewTransactionManager(7*24*time.Hour, 100000)
	srv.txManager.clock = fc

	ctx := context.Background()
	txID := srv.newIDFn()

	// Mirror BeginTransaction's branch-resource creation so gcTick's rollback
	// has real resources to remove: a git branch and a branch DB.
	if err := srv.gitstore.WithGitLock(func() error {
		if err := srv.gitstore.CreateBranch(ctx, txID); err != nil {
			return err
		}
		return srv.gitstore.HardResetToBranch(ctx, txID)
	}); err != nil {
		t.Fatalf("create git branch: %v", err)
	}
	if err := srv.store.CreateBranchDB(ctx, txID); err != nil {
		t.Fatalf("create branch DB: %v", err)
	}
	state, err := srv.txManager.Create(txID, 1*time.Minute, "head")
	if err != nil {
		t.Fatalf("register transaction: %v", err)
	}
	// The change log lives on the registered transaction state; seed it so the
	// rollback has a log to discard along with the registration.
	if err := state.ChangeLog.Add(gitstore.ChangeLogEntry{
		Kind: gitstore.ChangeDelEntity,
		ID:   "22222222-2222-4222-8222-222222222222",
		Type: "Component",
	}); err != nil {
		t.Fatalf("seed change log: %v", err)
	}

	// Sanity: the branch resources exist and the transaction is registered
	// before gcTick runs.
	if exists, err := srv.gitstore.BranchExists(ctx, txID); err != nil || !exists {
		t.Fatalf("git branch should exist before gcTick (exists=%v err=%v)", exists, err)
	}
	if _, err := srv.store.DumpAllEntities(ctx, txID); err != nil {
		t.Fatalf("branch DB should exist before gcTick: %v", err)
	}
	if _, err := srv.txManager.Lookup(txID); err != nil {
		t.Fatalf("transaction should be registered before gcTick: %v", err)
	}

	// Expire the transaction past its timeout plus the 30-second cleanup grace
	// period, then run the GC tick.
	fc.Advance(2 * time.Minute)
	srv.gcTick()

	// Rollback asserts (SPEC R9 + Garbage collection):
	// 1. the branch DB is dropped,
	if _, err := srv.store.DumpAllEntities(ctx, txID); !errors.Is(err, store.ErrBranchNotFound) {
		t.Fatalf("branch DB not rolled back: DumpAllEntities err = %v, want store.ErrBranchNotFound", err)
	}
	// 2. the git branch is deleted,
	if exists, err := srv.gitstore.BranchExists(ctx, txID); err != nil || exists {
		t.Fatalf("git branch not rolled back: BranchExists = %v (err %v), want false", exists, err)
	}
	// 3. the transaction (and its change log) is deregistered.
	if _, err := srv.txManager.Lookup(txID); err == nil {
		t.Fatal("transaction not deregistered after gcTick")
	}
}

// TestGCScan_ConcurrentWithExtendTimeout pins that gcTick's first expiry scan
// reads ExpiresAt under the transaction's lifecycle lock — the same lock
// ExtendTimeout writes it under — not under tm.mu alone. Before the fix, the
// first scan read state.ExpiresAt while holding only tm.mu.RLock, racing the
// lifecycle-locked write; run under -race this test reports that race, and
// without -race it still completes with the unexpired transaction surviving
// (gcTick must not collect a transaction the extension loop keeps alive).
func TestGCScan_ConcurrentWithExtendTimeout(t *testing.T) {
	srv, _ := newTestServer(t)
	txID := srv.newIDFn()
	if _, err := srv.txManager.Create(txID, 10*time.Minute, "head"); err != nil {
		t.Fatalf("register transaction: %v", err)
	}

	done := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 500 {
			if err := srv.txManager.ExtendTimeout(txID, 10*time.Minute, nil); err != nil {
				done <- fmt.Errorf("ExtendTimeout: %w", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range 500 {
			srv.gcTick()
		}
	}()
	wg.Wait()
	close(done)
	for err := range done {
		t.Fatal(err)
	}
	if _, err := srv.txManager.Lookup(txID); err != nil {
		t.Fatalf("unexpired transaction must survive the concurrent GC scan: %v", err)
	}
}

// TestStopGC_ConcurrentCallsDoNotPanic pins the StopGC close-once
// synchronisation: concurrent StopGC calls must not race on the gcStop channel
// close (the select/default idiom is a data race; the fix guards the close with
// a sync.Once).
func TestStopGC_ConcurrentCallsDoNotPanic(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.StartGC()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			srv.StopGC()
		})
	}
	wg.Wait()
	// Sequential idempotency after the concurrent burst.
	srv.StopGC()
}
