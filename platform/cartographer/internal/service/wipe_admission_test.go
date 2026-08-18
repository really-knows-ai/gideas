package service

import (
	"context"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestWipeGraph_WithOpenTx(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := context.Background()

	// Create an active transaction.
	srv.dbReady.Store(true)
	applyTestSchema(ctx, t, srv.store)
	err := srv.store.CreateBranchDB(ctx, "test-tx")
	if err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	_, _ = srv.txManager.Create("test-tx", 5*time.Minute, "head")

	_, err = srv.WipeGraph(ctx, &flowv1.WipeGraphRequest{})
	if err == nil {
		t.Fatal("expected error for WipeGraph with open transactions, got nil")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", status.Code(err))
	}
}

// TestWipeGraph_ExpiredTxDoesNotBlock verifies that a transaction whose
// deadline has passed (but which has not yet been garbage-collected) is not
// considered active, so WipeGraph does NOT return FAILED_PRECONDITION
// (SPEC R2 WipeGraph: only transactions that are still open block a wipe).
func TestWipeGraph_ExpiredTxDoesNotBlock(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := context.Background()

	srv.dbReady.Store(true)
	applyTestSchema(ctx, t, srv.store)
	err := srv.store.CreateBranchDB(ctx, "test-tx")
	if err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}

	fc := newFakeClock(time.Now())
	srv.txManager = NewTransactionManager(7*24*time.Hour, 100000)
	srv.txManager.clock = fc
	_, _ = srv.txManager.Create("test-tx", 1*time.Minute, "head")

	// Advance past the transaction deadline without GC: the transaction is
	// still registered but is no longer active.
	fc.Advance(2 * time.Minute)
	if srv.txManager.HasActive() {
		t.Fatal("expired transaction must not be reported as active")
	}

	_, err = srv.WipeGraph(ctx, &flowv1.WipeGraphRequest{})
	if err != nil {
		t.Fatalf("WipeGraph with only expired transaction should succeed, got %v", err)
	}
}

func TestWipeGraph_WaitsForBeginSetupAndSeesRegisteredTransaction(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	blocking := &beginSetupBlockingStore{Store: base, entered: make(chan struct{}), release: make(chan struct{})}
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		blocking, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
	)
	srv.MarkDBReady()
	ctx := testCtx()
	beginDone := make(chan *flowv1.BeginTransactionResponse, 1)
	beginErr := make(chan error, 1)
	go func() {
		response, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
		beginDone <- response
		beginErr <- err
	}()
	<-blocking.entered
	if srv.txAdmission.TryLock() {
		srv.txAdmission.Unlock()
		t.Fatal("WipeGraph admission unexpectedly succeeded during BeginTransaction setup")
	}
	wipeDone := make(chan error, 1)
	wipeStarted := make(chan struct{})
	go func() {
		close(wipeStarted)
		_, err := srv.WipeGraph(context.Background(), &flowv1.WipeGraphRequest{})
		wipeDone <- err
	}()
	<-wipeStarted
	close(blocking.release)
	begin := <-beginDone
	if err := <-beginErr; err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if err := <-wipeDone; status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected WipeGraph to observe registered transaction, got %v", err)
	}
	if _, err := srv.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("RollbackTransaction: %v", err)
	}
}
