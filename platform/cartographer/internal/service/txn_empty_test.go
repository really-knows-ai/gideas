package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

func TestTransaction_ExtendTimeout(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	beginResp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{
		Timeout: durationpb.New(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}
	txID := beginResp.TransactionId

	extendResp, err := srv.ExtendTimeout(ctx, &flowv1.ExtendTimeoutRequest{
		TransactionId: txID,
		Duration:      durationpb.New(10 * time.Minute),
	})
	if err != nil {
		t.Fatalf("ExtendTimeout failed: %v", err)
	}
	if extendResp.GetAppliedTimeout() == nil {
		t.Fatal("expected applied_timeout to be set on ExtendTimeout response")
	}
	if extendResp.GetAppliedTimeout().AsDuration() != 10*time.Minute {
		t.Errorf("expected applied timeout 10m, got %v", extendResp.GetAppliedTimeout().AsDuration())
	}
}

func TestTransaction_TimedOut(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	// Replace with fake clock so we can control time.
	fc := newFakeClock(time.Now())
	srv.txManager = NewTransactionManager(7*24*time.Hour, 100000)
	srv.txManager.clock = fc

	beginResp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{
		Timeout: durationpb.New(1 * time.Minute),
	})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}
	txID := beginResp.TransactionId

	// Advance clock past the 1-minute timeout.
	fc.Advance(2 * time.Minute)

	// Any operation with this txID should now return DeadlineExceeded.
	_, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType:    "Component",
		Properties:    map[string]string{"name": "test"},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected DeadlineExceeded for timed-out transaction, got nil")
	}
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("expected DeadlineExceeded, got %v", status.Code(err))
	}
}

func TestTransaction_InvalidTxID(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	_, err := srv.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{
		TransactionId: "not-a-uuid",
	})
	if err == nil {
		t.Fatal("expected error for invalid tx ID, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

// TestTransaction_NonCanonicalTxIDRejected pins the non-canonical-but-parseable
// branch of the SPEC error-table row "Invalid transaction ID format"
// (SPEC:990): spellings that google/uuid parses but that are not the canonical
// RFC4122 §3 lowercase dashed form — uppercase hex, 32-char no-hyphen, braced
// {...}, and urn:uuid: — must be rejected with INVALID_ARGUMENT because IDs are
// persisted verbatim (SPEC:162; uuidutil.Validate's canonical check). The
// existing tests (TestTransaction_InvalidTxID, TestReadPathTransactionID_Rejected,
// TestMutationTransactionID_Rejected) all send "not-a-uuid" and pin only the
// unparseable branch; this test exercises the shared canonical-check branch
// (validateTxID / LockActive → isValidUUID → uuidutil.Validate) from both the
// read path (GetTransactionDiff) and the mutation path (CreateEntity →
// lockTransactionMutation → LockActive).
func TestTransaction_NonCanonicalTxIDRejected(t *testing.T) {
	nonCanonical := []string{
		"550E8400-E29B-41D4-A716-446655440000",          // uppercase hex
		"550e8400e29b41d4a716446655440000",              // no-hyphen 32-char
		"{550e8400-e29b-41d4-a716-446655440000}",        // braced
		"urn:uuid:550e8400-e29b-41d4-a716-446655440000", // urn prefix
	}

	t.Run("read path GetTransactionDiff", func(t *testing.T) {
		srv, _ := newTestServer(t)
		ctx := testCtx()
		for _, txID := range nonCanonical {
			_, err := srv.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{TransactionId: txID})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("expected InvalidArgument for %q, got %v", txID, status.Code(err))
			}
		}
	})

	t.Run("mutation path CreateEntity", func(t *testing.T) {
		srv, _ := newTestServer(t)
		ctx := testCtx()
		for _, txID := range nonCanonical {
			_, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
				EntityType: "Component", Properties: map[string]string{"name": "x"}, TransactionId: txID,
			})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("expected InvalidArgument for %q, got %v", txID, status.Code(err))
			}
		}
	})
}

func TestTransaction_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	_, err := srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: "11111111-1111-4111-8111-111111111111",
	})
	if err == nil {
		t.Fatal("expected error for not-found tx, got nil")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", status.Code(err))
	}
}

func TestEmptyTransaction_CommitNoOp(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	beginResp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}

	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: beginResp.TransactionId,
	})
	if err != nil {
		t.Fatalf("CommitTransaction (no-op) failed: %v", err)
	}
}

// TestEmptyTransaction_CommitNoOpCreatesNoGitCommitAndNoPush pins the SPEC R10
// zero-mutation branch ("zero-mutation commits produce no git commit and
// therefore no remote push"; SPEC R9 step 5: "Commit() — if zero mutations,
// no-op"): a CommitTransaction with an empty change log must succeed without
// creating a git commit and without setting the sync-worker push-needed flag.
// TestEmptyTransaction_CommitNoOp only asserts that the RPC succeeds; nothing
// pins the git/push side of the no-op.
func TestEmptyTransaction_CommitNoOpCreatesNoGitCommitAndNoPush(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	countingGit := &commitCountingGitStore{GitStore: gs}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, countingGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
	)
	srv.MarkDBReady()
	// Wire a (non-running) SyncWorker so the push flag is observable. No remote
	// URL is configured, so BeginTransaction's implicit sync is skipped.
	sw := NewSyncWorker("", gs, base, RealClock{})
	srv.syncWorker = sw
	ctx := testCtx()

	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CommitTransaction (no-op) failed: %v", err)
	}
	if countingGit.commits != 0 {
		t.Fatalf("zero-mutation commit created %d git commits, want 0", countingGit.commits)
	}
	if sw.pushNeeded() {
		t.Fatal("zero-mutation commit set the sync-worker push-needed flag")
	}
}

func TestEmptyTransaction_RollbackNoOp(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	beginResp, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}

	_, err = srv.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{
		TransactionId: beginResp.TransactionId,
	})
	if err != nil {
		t.Fatalf("RollbackTransaction (no-op) failed: %v", err)
	}
}

func TestEmptyTransaction_CommitWaitsForMutationCompletion(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	blocking := &mutationBlockingStore{Store: base, wrote: make(chan struct{}), release: make(chan struct{})}
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
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}

	mutationDone := make(chan error, 1)
	go func() {
		_, mutationErr := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
			EntityType: "Component", Properties: map[string]string{"name": "racing"}, TransactionId: begin.TransactionId,
		})
		mutationDone <- mutationErr
	}()
	<-blocking.wrote

	commitAtLifecycleLock := make(chan struct{})
	srv.txManager.beforeLifecycleLock = func(string) { close(commitAtLifecycleLock) }
	commitDone := make(chan error, 1)
	go func() {
		_, commitErr := srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: begin.TransactionId})
		commitDone <- commitErr
	}()
	<-commitAtLifecycleLock
	srv.txManager.beforeLifecycleLock = nil
	close(blocking.release)
	if err := <-mutationDone; err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if err := <-commitDone; err != nil {
		t.Fatalf("CommitTransaction: %v", err)
	}
	entities, _, err := base.ListEntities(ctx, "Component", 10, "", "main")
	if err != nil {
		t.Fatalf("ListEntities: %v", err)
	}
	if len(entities) != 1 {
		t.Fatalf("expected committed mutation on main, got %d entities", len(entities))
	}
}

func TestEmptyTransaction_CommitCleanupFailureRemainsRetryable(t *testing.T) {
	srv, base := newTestServer(t)
	wrapped := failOnceDropBranchDB(base)
	srv.store = wrapped
	ctx := testCtx()
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}

	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: begin.TransactionId})
	if err == nil {
		t.Fatal("expected cleanup failure")
	}
	if _, unlock, err := srv.txManager.LockActive(begin.TransactionId); err != nil {
		t.Fatalf("transaction was not retryable after cleanup failure: %v", err)
	} else {
		unlock()
	}
	if err = srv.gitstore.WithGitLock(func() error {
		exists, branchErr := srv.gitstore.BranchExists(ctx, begin.TransactionId)
		if branchErr != nil {
			return branchErr
		}
		if !exists {
			return fmt.Errorf("transaction Git branch was deleted before branch DB cleanup")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{TransactionId: begin.TransactionId})
	if err != nil {
		t.Fatalf("retry CommitTransaction: %v", err)
	}
}
