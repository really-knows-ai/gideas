package service

import (
	"strings"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestBeginTransaction_PersistStateFailure_CleanupSuccess(t *testing.T) {
	// Path C1: persistTransactionState fails, cleanupTransaction succeeds.
	// Only the persist error should be returned.
	opPub, _ := generateTestKey()
	scPub := initTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Fail on the first SaveBranchTransactionState call.
	srv.store = newTxStateFailingStore(st, func(store.BranchTransactionState) bool { return true })

	ctx := testCtx()
	_, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got %v", status.Code(err))
	}
	if !strings.Contains(err.Error(), "persist transaction state") {
		t.Fatalf("error should contain persist failure, got: %v", err)
	}
	if strings.Contains(err.Error(), "cleanup") {
		t.Fatalf("error should NOT contain cleanup failure when cleanup succeeds, got: %v", err)
	}
}

func TestBeginTransaction_PersistStateFailure_CleanupFails(t *testing.T) {
	// Path C2: persistTransactionState fails, cleanupTransaction also fails.
	// Both errors should be aggregated.
	opPub, _ := generateTestKey()
	scPub := initTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Fail on the first SaveBranchTransactionState call.
	failingStore := newTxStateFailingStore(st, func(store.BranchTransactionState) bool { return true })
	// Also make DropBranchDB fail so cleanupTransaction fails.
	srv.store = failOnceDropBranchDB(failingStore)

	ctx := testCtx()
	_, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("expected ResourceExhausted, got %v", status.Code(err))
	}
	if !strings.Contains(err.Error(), "persist transaction state") {
		t.Fatalf("error should contain persist failure, got: %v", err)
	}
	if !strings.Contains(err.Error(), "cleanup") {
		t.Fatalf("error should contain cleanup failure when cleanup fails, got: %v", err)
	}
}
