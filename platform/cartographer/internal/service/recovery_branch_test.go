package service

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store/ladybug"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestRecoverOpenTransactionsSchemaPushDoesNotBlockCommit pins recovery's
// restoration of the schema baseline together with the SPEC R9 commit flow
// step 1 semantics: a schema push after recovery that is additive (a property
// flag change, a new type) does not make the branch DB incompatible with the
// current schema, so the recovered transaction commits normally instead of
// being wedged in FAILED_PRECONDITION.
func TestRecoverOpenTransactionsSchemaPushDoesNotBlockCommit(t *testing.T) {
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
	applyTestSchema(ctx, t, st)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	created, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "persisted"},
		TransactionId: begin.TransactionId,
	})
	if err != nil {
		t.Fatalf("create transaction entity: %v", err)
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
		reopened, reopenedGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	if err := restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("recover transactions: %v", err)
	}
	// Additive push after recovery: an existing property's required flag and a
	// new entity type. Non-destructive per SPEC R2/R6 (the store's diff ignores
	// the Required flag and new types are additive), so the commit must proceed.
	if err := reopened.ApplySchema(ctx, &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", Properties: []*flowv1.Property{
				{Name: "name", Type: "string", Required: true},
				{Name: "version", Type: "string", Required: true},
			}},
			{
				Name: "Service", Properties: []*flowv1.Property{{Name: "name", Type: "string", Required: true}},
				Rules: []*flowv1.ConnectionRule{{CanConnectTo: []string{"Component"}, Using: []string{"DEPENDS_ON"}}},
			},
			{Name: "Added", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
		},
		EdgeTypes: []*flowv1.EdgeType{{
			Name: "DEPENDS_ON", Properties: []*flowv1.Property{{Name: "weight", Type: "string"}},
		}},
	}); err != nil {
		t.Fatalf("change schema: %v", err)
	}
	if _, err := restarted.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("commit after additive schema push should succeed: %v", err)
	}
	if _, err := reopened.GetEntity(ctx, created.EntityId, "main"); err != nil {
		t.Fatalf("committed entity missing from main after additive schema push: %v", err)
	}
}

// A corrupt branch .lbug is rolled back during recovery, mirroring the
// absent-.lbug case (SPEC R9 recovery point 4): the R8-corruption
// classification (branchLocked → ErrBranchNotFound) turns the transaction into
// a rollback (DropBranchDB removes the corrupt file, the git branch is
// deleted) so startup proceeds instead of wedging on a hard open error until a
// human deletes the file (the pre-fix behavior).
func TestRecoverOpenTransactionsRollsBackCorruptBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed store + reopen/durability")
	}
	ctx := testCtx()
	dataPath := t.TempDir()
	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	txID := "33333333-3333-4333-8333-333333333333"
	if err := gs.WithGitLock(func() error { return gs.CreateBranch(ctx, txID) }); err != nil {
		t.Fatalf("create git branch: %v", err)
	}
	branchPath := filepath.Join(dataPath, "branches", txID+".lbug")
	if err := os.WriteFile(branchPath, []byte("not a ladybug database"), 0600); err != nil {
		t.Fatalf("write corrupt branch: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		st, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	// Startup must not wedge: recovery classifies the corrupt .lbug as a lost
	// branch and rolls it back instead of returning a hard error.
	if err := srv.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("recover transactions with corrupt branch should roll back, got %v", err)
	}
	// The rollback removed the corrupt branch .lbug.
	if _, err := os.Stat(branchPath); !os.IsNotExist(err) {
		t.Fatalf("corrupt branch .lbug was not removed by rollback: %v", err)
	}
	// The rollback deleted the git branch.
	if err := gs.WithGitLock(func() error {
		exists, err := gs.BranchExists(ctx, txID)
		if err != nil {
			return err
		}
		if exists {
			return errors.New("git branch was not removed by rollback")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverOpenTransactionsMissingBranchRestoresMain(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed store + reopen/durability")
	}
	ctx := testCtx()
	dataPath := t.TempDir()
	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		st, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	applyTestSchema(ctx, t, st)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(dataPath, "branches", begin.TransactionId+".lbug")); err != nil {
		t.Fatalf("remove branch DB: %v", err)
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
	if err := restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("recover transactions: %v", err)
	}
	if _, err := restarted.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{}); err != nil {
		t.Fatalf("begin transaction after missing-branch cleanup: %v", err)
	}
}

func TestRecoverOpenTransactionsMainLookupFailuresAbort(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git refresh/recovery")
	}
	for _, operation := range []string{
		"lookup lock", "restore", "clean", "list entities", "read entities", "list edges", "read edges",
	} {
		t.Run(operation, func(t *testing.T) {
			ctx := testCtx()
			st, err := openTestStore(t)
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })
			gs, err := gitstore.New(t.TempDir())
			if err != nil {
				t.Fatalf("open git store: %v", err)
			}
			opPub, _ := generateTestKey()
			setup := NewCartographerServer(
				st, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
			)
			applyTestSchema(ctx, t, st)
			if err := gs.WithGitLock(func() error {
				if err := gs.WriteEntityFiles(ctx, "Component", []gitstore.Entity{{
					ID: "11111111-1111-4111-8111-111111111111", Type: "Component",
					Properties: map[string]string{"name": "main"},
				}}); err != nil {
					return err
				}
				if err := gs.WriteEdgeFiles(ctx, "DEPENDS_ON", []gitstore.Edge{{
					ID: "22222222-2222-4222-8222-222222222222", Type: "DEPENDS_ON",
					FromEntityID: "33333333-3333-4333-8333-333333333333",
					ToEntityID:   "44444444-4444-4444-8444-444444444444",
				}}); err != nil {
					return err
				}
				if err := gs.AddAll(ctx, "."); err != nil {
					return err
				}
				return gs.Commit(ctx, "recovery fixtures")
			}); err != nil {
				t.Fatalf("write git fixtures: %v", err)
			}
			begin, err := setup.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
			if err != nil {
				t.Fatalf("begin transaction: %v", err)
			}
			failing := newRecoveryFailingGitStore(gs, operation)
			restarted := NewCartographerServer(
				st, failing, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
			)
			if err := restarted.RecoverOpenTransactions(ctx); err == nil {
				t.Fatalf("expected %s failure", operation)
			}
			if _, unlock, err := restarted.txManager.LockActive(begin.TransactionId); status.Code(err) != codes.NotFound {
				t.Fatalf("recovery registered transaction after lookup failure: %v", err)
			} else if unlock != nil {
				unlock()
			}
		})
	}
}

func TestRecoverOpenTransactionsIdenticalCleanupIsRetryable(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git refresh/recovery")
	}
	for _, operation := range []string{"restore", "clean", "drop", "delete"} {
		t.Run(operation, func(t *testing.T) {
			ctx := testCtx()
			st, err := openTestStore(t)
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })
			gs, err := gitstore.New(t.TempDir())
			if err != nil {
				t.Fatalf("open git store: %v", err)
			}
			opPub, _ := generateTestKey()
			setup := NewCartographerServer(
				st, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
			)
			begin, err := setup.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
			if err != nil {
				t.Fatalf("begin transaction: %v", err)
			}
			failingGit := newRecoveryFailingGitStore(gs, "")
			var recoveryStore = st
			if operation == "drop" {
				recoveryStore = failOnceDropBranchDB(st)
			} else {
				failingGit = newRecoveryFailingGitStore(gs, operation)
			}
			restarted := NewCartographerServer(
				recoveryStore, failingGit, opPub, initTestKey(), nil, "", 30*time.Second,
				"test-ns", 30*time.Minute, 100000,
			)
			if err := restarted.RecoverOpenTransactions(ctx); err == nil {
				t.Fatalf("expected %s cleanup failure", operation)
			}
			if operation == "drop" {
				exists, branchErr := gs.BranchExists(ctx, begin.TransactionId)
				if branchErr != nil || !exists {
					t.Fatalf("drop failure lost recovery anchor: exists=%v err=%v", exists, branchErr)
				}
				restarted = NewCartographerServer(
					st, gs, opPub, initTestKey(), nil, "", 30*time.Second,
					"test-ns", 30*time.Minute, 100000,
				)
			}
			if err := restarted.RecoverOpenTransactions(ctx); err != nil {
				t.Fatalf("retry cleanup after %s failure: %v", operation, err)
			}
			if err := gs.WithGitLock(func() error {
				exists, err := gs.BranchExists(ctx, begin.TransactionId)
				if err != nil {
					return err
				}
				if exists {
					return errors.New("transaction branch still exists")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
