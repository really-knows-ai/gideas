package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
	"github.com/foundry/flow/cartographer/internal/store/ladybug"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

func TestRecoverOpenTransactionsPersistsAppliedTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed store + reopen/durability")
	}
	ctx := testCtx()
	dataPath := t.TempDir()
	opPub, _ := generateTestKey()

	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	srv := NewCartographerServer(
		st, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	srv.MarkDBReady()
	applyTestSchema(ctx, t, st)

	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{
		Timeout: durationpb.New(90 * time.Second),
	})
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	if begin.GetAppliedTimeout().AsDuration() != 90*time.Second {
		t.Fatalf("expected applied timeout 90s, got %v", begin.GetAppliedTimeout().AsDuration())
	}
	// A mutation is required so the branch diff is non-empty and recovery does
	// not treat the transaction as already-committed.
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "in-tx"},
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("create entity in transaction: %v", err)
	}
	// Extend the expiry to 2 minutes, then verify the granted value is
	// persisted (mirroring durableTransactionState) so recovery restores it
	// instead of silently defaulting to the 7-day hard maximum.
	if _, err := srv.ExtendTimeout(ctx, &flowv1.ExtendTimeoutRequest{
		TransactionId: begin.TransactionId,
		Duration:      durationpb.New(2 * time.Minute),
	}); err != nil {
		t.Fatalf("extend timeout: %v", err)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedGit, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("reopen git store: %v", err)
	}
	restarted := NewCartographerServer(
		reopened, reopenedGit, opPub, initTestKey(), nil, "", 30*time.Second, "test", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	restarted.MarkDBReady()
	if err := restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("RecoverOpenTransactions: %v", err)
	}
	recovered, err := restarted.txManager.Lookup(begin.TransactionId)
	if err != nil {
		t.Fatalf("lookup recovered transaction: %v", err)
	}
	if recovered.AppliedTimeout != 2*time.Minute {
		t.Fatalf("expected recovered applied timeout 2m, got %v", recovered.AppliedTimeout)
	}
	if time.Until(recovered.ExpiresAt) >= 24*time.Hour {
		t.Fatalf("recovered transaction should not default to the 7-day hard max, ExpiresAt=%v", recovered.ExpiresAt)
	}
}

// TestRecoverOpenTransactionsPreservesAbsoluteLifetime pins SPEC R9's
// "absolute lifetime from BeginTransaction, not an idle timeout" across a
// restart: a recovered transaction must retain its original begin instant
// (CreatedAt) and expiry (ExpiresAt) rather than being re-based from the
// restart instant. Before the fix, RecoverOpenTransactions re-based both via
// txManager.Create, so a transaction could live materially longer than its
// granted lifetime — and beyond the 7-day hard maximum measured from the
// original begin — after each crash/restart, and the ExtendTimeout ceiling
// (computed against the rebased CreatedAt) no longer bounded the true total
// lifetime. The test begins a transaction with a 30-minute timeout, lets its
// absolute lifetime elapse while it is open (fake clock), restarts, and
// asserts the recovered transaction keeps the original CreatedAt/ExpiresAt and
// is already expired (DEADLINE_EXCEEDED) rather than re-armed for a fresh
// 30-minute lease of life from the restart instant.
func TestRecoverOpenTransactionsPreservesAbsoluteLifetime(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed store + reopen/durability")
	}
	ctx := testCtx()
	dataPath := t.TempDir()
	opPub, _ := generateTestKey()

	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	srv := NewCartographerServer(
		st, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	srv.MarkDBReady()
	// Drive the lifetime deterministically with a fake clock shared across the
	// simulated restart.
	fc := newFakeClock(time.Now())
	srv.txManager = NewTransactionManager(7*24*time.Hour, 100000)
	srv.txManager.clock = fc
	applyTestSchema(ctx, t, st)

	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{
		Timeout: durationpb.New(30 * time.Minute),
	})
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	beginInstant := fc.Now()
	// A mutation is required so the branch diff is non-empty and recovery does
	// not treat the transaction as already-committed.
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "in-tx"},
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("create entity in transaction: %v", err)
	}

	// The transaction's absolute lifetime elapses while it is open: the
	// restart happens after the original expiry.
	fc.Advance(40 * time.Minute)

	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedGit, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("reopen git store: %v", err)
	}
	restarted := NewCartographerServer(
		reopened, reopenedGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	restarted.txManager = NewTransactionManager(7*24*time.Hour, 100000)
	restarted.txManager.clock = fc
	restarted.MarkDBReady()
	if err := restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("RecoverOpenTransactions: %v", err)
	}
	recovered, err := restarted.txManager.Lookup(begin.TransactionId)
	if err != nil {
		t.Fatalf("lookup recovered transaction: %v", err)
	}
	// The recovered transaction keeps the original begin instant and expiry:
	// recovery must NOT re-base them from the restart instant (now =
	// begin+40m), which would grant a fresh 30-minute lease of life.
	if !recovered.CreatedAt.Equal(beginInstant) {
		t.Fatalf("recovered CreatedAt = %v, want the original begin %v", recovered.CreatedAt, beginInstant)
	}
	wantExpiry := beginInstant.Add(30 * time.Minute)
	if !recovered.ExpiresAt.Equal(wantExpiry) {
		t.Fatalf("recovered ExpiresAt = %v, want the original absolute expiry %v", recovered.ExpiresAt, wantExpiry)
	}
	// The absolute lifetime has already elapsed at restart (now = begin+40m),
	// so the recovered transaction is expired: an operation on it surfaces
	// DEADLINE_EXCEEDED rather than a re-armed fresh lifetime.
	_, err = restarted.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{TransactionId: begin.TransactionId})
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("expected DeadlineExceeded on the expired recovered transaction, got %v", err)
	}
}

func TestRecoverRollbackOnlyTransactionWhenRejectedUpdateDoesNotIncreaseNetDiff(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed store + reopen/durability")
	}
	ctx := testCtx()
	dataPath := t.TempDir()
	opPub, _ := generateTestKey()
	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	failing := failOnceDropBranchDB(st)
	srv := NewCartographerServer(
		failing, gs, opPub, initTestKey(), nil, "", 30*time.Second,
		"test-ns", 30*time.Minute, 1, WithLadybugPath(dataPath),
	)
	applyTestSchema(ctx, t, st)
	mainEntity, err := st.CreateEntity(
		ctx, "Component", "", map[string]string{"name": "before"}, nil, "main",
	)
	if err != nil {
		t.Fatalf("create main entity: %v", err)
	}
	commitGitEntity(ctx, t, gs, mainEntity.Id, "before")
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	if _, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id: mainEntity.Id, Properties: map[string]string{"version": "first"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("first update: %v", err)
	}
	// The rejected mutation is a CreateEntity: under the SPEC change-log
	// admission predicate a same-ID update at cap is admitted (it reuses the
	// element's slot and does not grow the log), so the capacity trigger must
	// be a mutation that adds a new distinct element — a CreateEntity always
	// does.
	_, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "rejected"}, TransactionId: begin.TransactionId,
	})
	if status.Code(err) != codes.ResourceExhausted || !strings.Contains(err.Error(), "rollback failed") {
		t.Fatalf("rejected mutation error = %v", err)
	}
	markerPath := filepath.Join(dataPath, "branches", begin.TransactionId+".state.json")
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("rollback-only marker was not persisted: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedGit, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("reopen git store: %v", err)
	}
	restarted := NewCartographerServer(
		reopened, reopenedGit, opPub, initTestKey(), nil, "", 30*time.Second,
		"test-ns", 30*time.Minute, 1, WithLadybugPath(dataPath),
	)
	if err := restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("recover rollback-only transaction: %v", err)
	}
	state, err := restarted.txManager.Lookup(begin.TransactionId)
	if err != nil || !state.RollbackOnly || state.ChangeLog.Len() != 1 {
		t.Fatalf("recovered state = %+v, err=%v", state, err)
	}
	// A rollback-only transaction is already rolled back, so commit and
	// mutations on it surface NOT_FOUND (SPEC "Transaction not found": "was
	// already committed/rolled back") — the cap-violation outcome is defined
	// solely as RESOURCE_EXHAUSTED with the transaction "rolled back".
	if _, err := restarted.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); status.Code(err) != codes.NotFound {
		t.Fatalf("commit rollback-only transaction: %v", err)
	}
	if _, err := restarted.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id: mainEntity.Id, Properties: map[string]string{"version": "late"}, TransactionId: begin.TransactionId,
	}); status.Code(err) != codes.NotFound {
		t.Fatalf("mutate rollback-only transaction: %v", err)
	}
	if _, err := restarted.RollbackTransaction(ctx, &flowv1.RollbackTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("rollback recovered transaction: %v", err)
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("rollback-only marker remained after cleanup: %v", err)
	}
}

func TestChangeLogMarkerFailureStillCleansRejectedTransaction(t *testing.T) {
	srv, base := newTestServer(t)
	srv.store = newMarkerFailingStore(base, true, false)
	srv.txManager.changeLogCap = 1
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "first"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("first CreateEntity: %v", err)
	}
	_, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "rejected"}, TransactionId: begin.TransactionId,
	})
	if status.Code(err) != codes.ResourceExhausted ||
		!strings.Contains(err.Error(), "simulated rollback-only marker failure") {
		t.Fatalf("cap rejection error = %v", err)
	}
	if _, err := srv.txManager.Lookup(begin.TransactionId); err == nil {
		t.Fatal("transaction remained after successful cleanup")
	}
	if _, err := base.DumpAllEntities(ctx, begin.TransactionId); !errors.Is(err, store.ErrBranchNotFound) {
		t.Fatalf("branch DB remained after cleanup: %v", err)
	}
	exists, err := srv.gitstore.BranchExists(ctx, begin.TransactionId)
	if err != nil || exists {
		t.Fatalf("Git branch remained after cleanup: exists=%v err=%v", exists, err)
	}
}

func TestChangeLogMarkerAndCleanupFailureCannotRecoverAsActive(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed store + reopen/durability")
	}
	ctx := testCtx()
	dataPath := t.TempDir()
	opPub, _ := generateTestKey()
	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	failing := newMarkerFailingStore(st, true, true)
	srv := NewCartographerServer(
		failing, gs, opPub, initTestKey(), nil, "", 30*time.Second,
		"test-ns", 30*time.Minute, 1, WithLadybugPath(dataPath),
	)
	applyTestSchema(ctx, t, st)
	mainEntity, err := st.CreateEntity(ctx, "Component", "", map[string]string{"name": "before"}, nil, "main")
	if err != nil {
		t.Fatalf("create main entity: %v", err)
	}
	commitGitEntity(ctx, t, gs, mainEntity.Id, "before")
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id: mainEntity.Id, Properties: map[string]string{"version": "first"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("first update: %v", err)
	}
	// The rejected mutation is a CreateEntity: under the SPEC change-log
	// admission predicate a same-ID update at cap is admitted (it reuses the
	// element's slot and does not grow the log), so the capacity trigger must
	// be a mutation that adds a new distinct element — a CreateEntity always
	// does.
	_, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "rejected"}, TransactionId: begin.TransactionId,
	})
	if status.Code(err) != codes.ResourceExhausted ||
		!strings.Contains(err.Error(), "simulated rollback-only marker failure") ||
		!strings.Contains(err.Error(), "simulated marker cleanup drop failure") {
		t.Fatalf("combined failure error = %v", err)
	}
	branchEntity, err := st.GetEntity(ctx, mainEntity.Id, begin.TransactionId)
	if err != nil {
		t.Fatalf("read branch entity: %v", err)
	}
	if branchEntity.Properties["version"] != "first" {
		t.Fatalf("rejected mutation reached branch: %+v", branchEntity.Properties)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	reopened, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedGit, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("reopen git store: %v", err)
	}
	restarted := NewCartographerServer(
		reopened, reopenedGit, opPub, initTestKey(), nil, "", 30*time.Second,
		"test-ns", 30*time.Minute, 1, WithLadybugPath(dataPath),
	)
	// The rollback-only marker persist failed, so the store invalidated the
	// state record; on restart the branch has no lifecycle record. Recovery
	// must not wedge startup and must not register the rejected transaction as
	// active — it finishes the aborted rollback instead (SPEC R9 recovery:
	// a branch with no persisted state record cannot be recovered).
	if err := restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("recovery wedged startup on an invalidated branch: %v", err)
	}
	if _, err := restarted.txManager.Lookup(begin.TransactionId); err == nil {
		t.Fatal("failed-closed transaction was registered as active")
	}
	// The rollback finished the aborted cleanup: the git branch is removed.
	if err := reopenedGit.WithGitLock(func() error {
		exists, err := reopenedGit.BranchExists(ctx, begin.TransactionId)
		if err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("invalidated transaction git branch still exists")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
