package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store/ladybug"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// TestRefreshTransaction_MidRefreshCrashPreservesMutations pins the SPEC R9
// change-log-recovery guarantee across the RefreshTransaction branch-DB swap
// (the swap-to-reapply crash window): the swap re-applies the transaction's
// changes onto the replacement branch before the swap, and evicts the old
// branch handle without deleting its files until the atomic rename installs
// the fully re-applied replacement. A crash at any point in the refresh — here
// simulated by failing the final state persist after the swap — leaves the
// durable branch DB carrying the complete change set and the BranchRefreshInProgress
// marker set, so RecoverOpenTransactions reconstructs the FULL change log
// instead of misclassifying the transaction as already committed and deleting
// it (or, in the partial-reapply sub-window, reconstructing a truncated log).
func TestRefreshTransaction_MidRefreshCrashPreservesMutations(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed store + reopen/durability")
	}
	ctx := testCtx()
	dataPath := t.TempDir()
	opPub, _ := generateTestKey()

	base, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	failing := &refreshTailPersistFailingStore{Store: base}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	srv := NewCartographerServer(
		failing, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	srv.MarkDBReady()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	// BeginTransaction already wrote the durable record (the first txID save);
	// the next two txID saves are the refresh's pre-swap in-progress marker and
	// its final persist. Account for the first so the failure fires on the
	// final persist.
	failing.txID = begin.TransactionId
	failing.txSaves = 1
	first, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "tx-one"}, TransactionId: begin.TransactionId,
	})
	if err != nil {
		t.Fatalf("CreateEntity tx-one: %v", err)
	}
	second, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "tx-two"}, TransactionId: begin.TransactionId,
	})
	if err != nil {
		t.Fatalf("CreateEntity tx-two: %v", err)
	}
	// Main advances while the transaction is open, forcing a real refresh.
	mainEntity, err := base.CreateEntity(ctx, "Component", "", map[string]string{"name": "main"}, nil, "main")
	if err != nil {
		t.Fatalf("create main entity: %v", err)
	}
	commitGitEntity(ctx, t, gs, mainEntity.Id, "main")

	// The refresh completes its branch-DB swap (re-applying both transaction
	// changes onto the replacement) and then "crashes" at the final state
	// persist: the durable record keeps the in-progress marker and the
	// swapped-in branch DB carries the full change set.
	if _, err := srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err == nil || !strings.Contains(err.Error(), "simulated crash at refresh tail") {
		t.Fatalf("expected refresh tail persist failure, got %v", err)
	}
	// The durable marker distinguishes this mid-refresh crash from a
	// post-merge crash.
	durable, err := base.LoadBranchTransactionState(ctx, begin.TransactionId)
	if err != nil {
		t.Fatalf("load durable state after refresh crash: %v", err)
	}
	if !durable.BranchRefreshInProgress {
		t.Fatal("mid-refresh crash left no BranchRefreshInProgress marker")
	}
	if err := base.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// Restart: the transaction's uncommitted mutations must be recovered, not
	// misclassified as already committed and deleted.
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
	restarted.MarkDBReady()
	if err := restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("RecoverOpenTransactions: %v", err)
	}
	state, err := restarted.txManager.Lookup(begin.TransactionId)
	if err != nil {
		t.Fatalf("mid-refresh-crash transaction was not recovered (deleted?): %v", err)
	}
	if !state.BranchRefreshInProgress {
		t.Fatal("recovered mid-refresh transaction lost its in-progress marker")
	}
	diff, err := restarted.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{TransactionId: begin.TransactionId})
	if err != nil {
		t.Fatalf("get recovered diff: %v", err)
	}
	if len(diff.AddedEntities) != 2 {
		t.Fatalf("expected both transaction mutations recovered, got %+v", diff.AddedEntities)
	}
	got := map[string]bool{}
	for _, e := range diff.AddedEntities {
		got[e.Id] = true
	}
	if !got[first.EntityId] || !got[second.EntityId] {
		t.Fatalf("recovered diff missing transaction mutations: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dataPath, "branches", begin.TransactionId+".lbug")); err != nil {
		t.Fatalf("recovered transaction branch DB missing: %v", err)
	}
}
