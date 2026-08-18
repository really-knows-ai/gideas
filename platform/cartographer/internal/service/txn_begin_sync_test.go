package service

import (
	"context"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// TestBeginTransaction_ImplicitSync verifies BeginTransaction's implicit sync
// contract: when a remote is configured, waking the sync worker and waiting
// for its cycle precedes branch creation, so the branch starts from the
// latest remote state.
func TestBeginTransaction_ImplicitSync(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{GitStore: gs}
	srv, fc := newSyncServer(t, syncGit)

	// Wait for the startup cycle so the implicit-sync wake is consumed by a
	// fresh cycle that completes before branch creation.
	waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

	ctx := testCtx()
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if begin.TransactionId == "" {
		t.Fatal("expected a transaction ID")
	}
	if _, unlock, err := srv.txManager.LockActive(begin.TransactionId); err != nil {
		t.Fatalf("transaction not registered: %v", err)
	} else {
		unlock()
	}

	// The implicit-sync fetch must have happened immediately before the branch
	// creation (WakeAndWait completed before branch setup began).
	syncGit.mu.Lock()
	order := append([]string(nil), syncGit.order...)
	syncGit.mu.Unlock()
	if len(order) < 2 || order[len(order)-2] != "fetch" || order[len(order)-1] != "branch" {
		t.Fatalf("expected implicit sync (fetch) immediately before branch creation, got order %v", order)
	}
}

// TestBeginTransaction_ImplicitSyncFailsProceeds verifies that implicit sync
// errors are non-blocking: when the sync cycle fails with a non-recoverable
// error, BeginTransaction still creates the transaction from the current
// local state.
func TestBeginTransaction_ImplicitSyncFailsProceeds(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{GitStore: gs, fetchErr: gitstore.ErrAuthFailed}
	srv, fc := newSyncServer(t, syncGit)

	waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

	ctx := testCtx()
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction must succeed despite implicit-sync failure: %v", err)
	}
	if begin.TransactionId == "" {
		t.Fatal("expected a transaction ID")
	}
	if _, unlock, err := srv.txManager.LockActive(begin.TransactionId); err != nil {
		t.Fatalf("transaction not registered: %v", err)
	} else {
		unlock()
	}
	syncGit.mu.Lock()
	fetchCalls := syncGit.fetchCalls
	syncGit.mu.Unlock()
	if fetchCalls < 2 {
		t.Fatalf("expected the implicit-sync cycle to attempt a fetch, got %d", fetchCalls)
	}
}

// TestBeginTransaction_NoRemote_StillCreatesTransaction pins the SPEC R10
// BeginTransaction implicit-sync contract for the "Remote not configured"
// case: when the gitstore has no remote (a REMOTE_URL whose SetRemote was
// rejected non-fatally at startup with pullOnInit=false, cmd/main.go), the
// implicit-sync cycle fails with ErrNoRemote but the transaction is still
// created from the current local state — sync errors are non-blocking
// (SPEC:624-626 "If the cycle fails, the transaction is still created with the
// current local state").
func TestBeginTransaction_NoRemote_StillCreatesTransaction(t *testing.T) {
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	syncGit := &syncMockGitStore{GitStore: gs, fetchErr: gitstore.ErrNoRemote}
	srv, fc := newSyncServer(t, syncGit)

	waitFor(t, func() bool { return fc.tickers() >= 1 }, "startup cycle")

	begin, err := srv.BeginTransaction(testCtx(), &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction must succeed despite the no-remote implicit-sync failure: %v", err)
	}
	if begin.TransactionId == "" {
		t.Fatal("expected a transaction ID")
	}
	if _, unlock, err := srv.txManager.LockActive(begin.TransactionId); err != nil {
		t.Fatalf("transaction not registered: %v", err)
	} else {
		unlock()
	}
	syncGit.mu.Lock()
	fetchCalls := syncGit.fetchCalls
	syncGit.mu.Unlock()
	if fetchCalls < 2 {
		t.Fatalf("expected the implicit-sync cycle to attempt a fetch, got %d", fetchCalls)
	}
}

func TestBeginTransaction_WaitsUntilWipeGraphCompletes(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	blocking := &wipeBlockingStore{
		Store: base, entered: make(chan struct{}), release: make(chan struct{}), branchSetup: make(chan bool, 1),
	}
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		blocking, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
	)
	srv.MarkDBReady()
	wipeDone := make(chan error, 1)
	go func() {
		_, err := srv.WipeGraph(context.Background(), &flowv1.WipeGraphRequest{})
		wipeDone <- err
	}()
	<-blocking.entered
	if srv.txAdmission.TryRLock() {
		srv.txAdmission.RUnlock()
		t.Fatal("BeginTransaction admission unexpectedly succeeded during WipeGraph")
	}
	beginDone := make(chan *flowv1.BeginTransactionResponse, 1)
	beginErr := make(chan error, 1)
	beginStarted := make(chan struct{})
	ctx := testCtx()
	go func() {
		close(beginStarted)
		response, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
		beginDone <- response
		beginErr <- err
	}()
	<-beginStarted
	close(blocking.release)
	if err := <-wipeDone; err != nil {
		t.Fatalf("WipeGraph: %v", err)
	}
	if wipeCompleted := <-blocking.branchSetup; !wipeCompleted {
		t.Fatal("BeginTransaction reached branch setup before WipeGraph completed its store wipe")
	}
	begin := <-beginDone
	if err := <-beginErr; err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := srv.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("RollbackTransaction: %v", err)
	}
}
