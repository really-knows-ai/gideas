package service

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
	"github.com/foundry/flow/cartographer/internal/store/ladybug"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRefreshTransaction_ConflictLeavesCleanRefreshedBranch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed store + reopen/durability")
	}
	ladybugPath := t.TempDir()
	base, err := ladybug.Open(ladybugPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	first, err := base.CreateEntity(ctx, "Component", "", map[string]string{"name": "one"}, nil, "main")
	if err != nil {
		t.Fatalf("create first main entity: %v", err)
	}
	second, err := base.CreateEntity(ctx, "Component", "", map[string]string{"name": "two"}, nil, "main")
	if err != nil {
		t.Fatalf("create second main entity: %v", err)
	}
	commitGitEntity(ctx, t, gs, first.Id, "one")
	commitGitEntity(ctx, t, gs, second.Id, "two")
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id: first.Id, Properties: map[string]string{"name": "tx-one"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("update first transaction entity: %v", err)
	}
	if _, err = srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id: second.Id, Properties: map[string]string{"name": "tx-two"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("update second transaction entity: %v", err)
	}
	if _, err = base.UpdateEntity(ctx, second.Id, map[string]string{"name": "main-two"}, nil, "main"); err != nil {
		t.Fatalf("update second main entity: %v", err)
	}
	commitGitEntity(ctx, t, gs, second.Id, "main-two")

	_, err = srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{TransactionId: begin.TransactionId})
	if status.Code(err) != codes.Aborted {
		t.Fatalf("expected refresh conflict, got %v", err)
	}
	firstAfter, err := base.GetEntity(ctx, first.Id, begin.TransactionId)
	if err != nil {
		t.Fatalf("get first refreshed entity: %v", err)
	}
	secondAfter, err := base.GetEntity(ctx, second.Id, begin.TransactionId)
	if err != nil {
		t.Fatalf("get second refreshed entity: %v", err)
	}
	if firstAfter.Properties["name"] != "one" || secondAfter.Properties["name"] != "main-two" {
		t.Fatalf("conflicted refresh partially reapplied changes: first=%+v second=%+v", firstAfter, secondAfter)
	}
}

// TestRefreshTransaction_AbortedRefreshPreservesDiff pins SPEC R9 step 3
// ("Because Diff() reads the change log rather than querying the branch DB, it
// returns the same result regardless of whether the branch DB reflects the
// transaction's changes or has been re-hydrated to a clean state (e.g. after
// an aborted Refresh())"): after an ABORTED refresh leaves the branch DB
// re-hydrated to a clean state, GetTransactionDiff must return exactly the
// pre-refresh diff — the change log is preserved, only the branch DB is
// rebuilt. The existing aborted-refresh tests assert the branch DB contents but
// never call GetTransactionDiff.
func TestRefreshTransaction_AbortedRefreshPreservesDiff(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed store + reopen/durability")
	}
	ladybugPath := t.TempDir()
	base, err := ladybug.Open(ladybugPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	first, err := base.CreateEntity(ctx, "Component", "", map[string]string{"name": "one"}, nil, "main")
	if err != nil {
		t.Fatalf("create first main entity: %v", err)
	}
	second, err := base.CreateEntity(ctx, "Component", "", map[string]string{"name": "two"}, nil, "main")
	if err != nil {
		t.Fatalf("create second main entity: %v", err)
	}
	commitGitEntity(ctx, t, gs, first.Id, "one")
	commitGitEntity(ctx, t, gs, second.Id, "two")
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id: first.Id, Properties: map[string]string{"name": "tx-one"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("update first transaction entity: %v", err)
	}
	if _, err = srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id: second.Id, Properties: map[string]string{"name": "tx-two"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("update second transaction entity: %v", err)
	}
	before, err := srv.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{TransactionId: begin.TransactionId})
	if err != nil {
		t.Fatalf("GetTransactionDiff before refresh: %v", err)
	}

	// Main advances on the second entity while the transaction is open,
	// forcing the refresh to abort (UUID-overlap conflict).
	if _, err = base.UpdateEntity(ctx, second.Id, map[string]string{"name": "main-two"}, nil, "main"); err != nil {
		t.Fatalf("update second main entity: %v", err)
	}
	commitGitEntity(ctx, t, gs, second.Id, "main-two")

	if _, err = srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: begin.TransactionId,
	}); status.Code(err) != codes.Aborted {
		t.Fatalf("expected refresh conflict, got %v", err)
	}
	after, err := srv.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{TransactionId: begin.TransactionId})
	if err != nil {
		t.Fatalf("GetTransactionDiff after aborted refresh: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("aborted refresh changed the transaction diff: before=%+v after=%+v", before, after)
	}
	if len(after.ModifiedEntities) != 2 {
		t.Fatalf("expected the two modified entities preserved in the diff, got %+v", after.ModifiedEntities)
	}
}

// TestRefreshTransaction_EmbeddingDimensionConflict exercises the SPEC R7
// dimension-scope refresh-conflict rule. validateRefresh checks each
// change-log entry carrying an embedding against main's established
// embedding dimension; a mismatch must surface as ABORTED (errRefreshConflict).
// Every pre-existing refresh test only exercises entity/edge-overlap conflicts,
// leaving this dimension path uncovered.
func TestRefreshTransaction_EmbeddingDimensionConflict(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := testCtx()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VecType",
				EnableVectorIndex: true,
				Properties:        []*flowv1.Property{{Name: "name", Type: "string"}},
			},
		},
	}
	if err := st.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	// Main bootstraps a 3-dim embedding column for VecType.
	_, err := st.CreateEntity(ctx, "VecType", "", map[string]string{"name": "main"}, []float32{1.0, 0.0, 0.0}, "main")
	if err != nil {
		t.Fatalf("bootstrap main: %v", err)
	}

	// A transaction whose change log records a 2-dim embedding add.
	txID := testMutationEntityID
	state, err := srv.txManager.Create(txID, time.Minute, "head")
	if err != nil {
		t.Fatalf("Create transaction: %v", err)
	}
	err = state.ChangeLog.Add(gitstore.ChangeLogEntry{
		Kind: gitstore.ChangeAddEntity, ID: "22222222-2222-4222-8222-222222222222", Type: "VecType",
		Entity: &gitstore.EntityEntry{
			ID: "22222222-2222-4222-8222-222222222222", Type: "VecType",
			Embedding: []float32{1.0, 0.0}, // 2 dims vs main's established 3
		},
	})
	if err != nil {
		t.Fatalf("Add change log entry: %v", err)
	}

	// No entity/edge files collide — before and current are empty — so only the
	// dimension check can fire. It must map to an ABORTED refresh conflict.
	err = srv.validateRefresh(ctx, state, gitGraphSnapshot{}, gitGraphSnapshot{})
	if status.Code(err) != codes.Aborted {
		t.Fatalf("expected ABORTED dimension-scope refresh conflict, got %v", err)
	}
	if !strings.Contains(err.Error(), "refresh conflict") {
		t.Fatalf("expected refresh-conflict message, got %q", err.Error())
	}
}

// TestRefreshTransaction_CreatedThenModifiedWithinTransaction pins SPEC R9
// refresh step 3 for a create-then-update of the same entity within one
// transaction. The map-based change log records ChangeAddEntity then
// ChangeModEntity for an ID that has no baseline file on the branch tree (the
// entity was created inside the transaction), and validateRefresh must not
// treat the missing baseline as a UUID-overlap conflict against main when main
// is untouched — the refresh must succeed and re-apply both entries so the
// entity survives with its updated content.
func TestRefreshTransaction_CreatedThenModifiedWithinTransaction(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Id: testMutationEntityID, Properties: map[string]string{"name": "created"},
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if _, err = srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id: testMutationEntityID, Properties: map[string]string{"name": "updated"},
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("UpdateEntity: %v", err)
	}
	if _, err = srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("RefreshTransaction must not conflict on a same-transaction create-then-update: %v", err)
	}
	ent, err := base.GetEntity(ctx, testMutationEntityID, begin.TransactionId)
	if err != nil {
		t.Fatalf("refreshed entity missing: %v", err)
	}
	if ent.Properties["name"] != "updated" {
		t.Fatalf("expected the re-applied update to win, got %+v", ent.Properties)
	}
}

// TestRefreshTransaction_CreatedThenDeletedWithinTransaction pins SPEC R9
// refresh step 3 for a create-then-delete of the same entity within one
// transaction: the ChangeDelEntity entry has no baseline file (the entity was
// created inside the transaction), and with main untouched the refresh must
// succeed and re-apply both entries, leaving the branch without the entity.
func TestRefreshTransaction_CreatedThenDeletedWithinTransaction(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Id: testMutationEntityID, Properties: map[string]string{"name": "created"},
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if _, err = srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{
		Id: testMutationEntityID, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("DeleteEntity: %v", err)
	}
	if _, err = srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("RefreshTransaction must not conflict on a same-transaction create-then-delete: %v", err)
	}
	if _, err := base.GetEntity(ctx, testMutationEntityID, begin.TransactionId); !errors.Is(err, store.ErrEntityNotFound) {
		t.Fatalf("expected the re-applied create-then-delete to leave the entity absent, got %v", err)
	}
}

// TestRefreshTransaction_CreatedThenDeleted_ConflictsWhenMainHasUUID is the
// positive control for the create-then-delete fix: the missing baseline must
// not conflict when main is untouched, but the same change MUST still abort
// when main advances with an entity holding the same UUID (SPEC R9 refresh
// step 3 UUID-overlap rule). The ChangeAddEntity entry is what detects the
// overlap.
func TestRefreshTransaction_CreatedThenDeleted_ConflictsWhenMainHasUUID(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed store + reopen/durability")
	}
	ladybugPath := t.TempDir()
	base, err := ladybug.Open(ladybugPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Id: testMutationEntityID, Properties: map[string]string{"name": "created"},
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if _, err = srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{
		Id: testMutationEntityID, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("DeleteEntity: %v", err)
	}
	// Main advances with an entity holding the same UUID while the transaction
	// is open.
	commitGitEntity(ctx, t, gs, testMutationEntityID, "main-owned")
	if _, err = srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: begin.TransactionId,
	}); status.Code(err) != codes.Aborted {
		t.Fatalf("expected ABORTED refresh conflict when main holds the same UUID, got %v", err)
	}
}

// TestRefreshTransaction_CommitMergeFailedThenMainAdvancedUnwedges pins the
// SPEC serialisation-flow retry contract for a transaction whose step-6 git
// commit was created but whose step-10 merge failed, after main advanced: the
// retried commit surfaces step 5's FAILED_PRECONDITION ("Commit not up-to-date
// with main", SPEC error table, whose prescription is "Call Refresh() before
// Commit()"), and the prescribed Refresh() → Commit() path must now succeed
// without losing the transaction's changes. Previously RefreshTransaction
// rejected any CommitStarted transaction with NOT_FOUND, so the baseline could
// never advance again and the transaction was permanently wedged — its changes
// recoverable only by Rollback, i.e. loss. The fix keeps the FAILED_PRECONDITION
// guard intact for genuinely conflicting changes: the wedge here is the
// baseline-advance path, not the conflict detection.
func TestRefreshTransaction_CommitMergeFailedThenMainAdvancedUnwedges(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real git refresh/recovery")
	}
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	failingGit := failOnceMerge(gs)
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, failingGit, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)

	// Transaction A: the step-6 git commit is created, the step-10 merge fails.
	beginA, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction A: %v", err)
	}
	createdA, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "a"}, TransactionId: beginA.TransactionId,
	})
	if err != nil {
		t.Fatalf("CreateEntity A: %v", err)
	}
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: beginA.TransactionId,
	}); err == nil {
		t.Fatal("expected commit A merge failure")
	}
	stateA, lookupErr := srv.txManager.Lookup(beginA.TransactionId)
	if lookupErr != nil || !stateA.CommitStarted || !stateA.CommitCreated || stateA.MergeCompleted {
		t.Fatalf("commit A did not retain the created-commit milestone: state=%+v error=%v", stateA, lookupErr)
	}

	// Main advances: transaction B commits on top.
	beginB, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction B: %v", err)
	}
	createdB, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "b"}, TransactionId: beginB.TransactionId,
	})
	if err != nil {
		t.Fatalf("CreateEntity B: %v", err)
	}
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: beginB.TransactionId,
	}); err != nil {
		t.Fatalf("CommitTransaction B: %v", err)
	}

	// The retried commit A now hits step 5's divergence check and surfaces the
	// SPEC-prescribed FAILED_PRECONDITION ("Commit not up-to-date with main").
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: beginA.TransactionId,
	}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected retried commit A to surface FAILED_PRECONDITION, got %v (%v)", status.Code(err), err)
	}

	// The SPEC error-table prescription for that condition: Refresh() then
	// Commit(). The refresh must succeed (it resets the branch, discarding the
	// orphaned commit, and re-applies A's change) and clear the commit-in-flight
	// milestones so the retried commit re-enters the serialisation flow.
	if _, err = srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: beginA.TransactionId,
	}); err != nil {
		t.Fatalf("RefreshTransaction after failed merge + main advance: %v", err)
	}
	refreshedA, lookupErr := srv.txManager.Lookup(beginA.TransactionId)
	if lookupErr != nil || refreshedA.CommitStarted || refreshedA.CommitCreated || refreshedA.CommitHydrated {
		t.Fatalf("refresh did not clear commit-in-flight milestones: state=%+v error=%v", refreshedA, lookupErr)
	}
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: beginA.TransactionId,
	}); err != nil {
		t.Fatalf("retried CommitTransaction after refresh: %v", err)
	}

	// No data loss: A's entity is committed to main, and B's survives too.
	if _, err = base.GetEntity(ctx, createdA.EntityId, "main"); err != nil {
		t.Fatalf("transaction A entity lost after un-wedged commit: %v", err)
	}
	if _, err = base.GetEntity(ctx, createdB.EntityId, "main"); err != nil {
		t.Fatalf("transaction B entity lost after A's un-wedged commit: %v", err)
	}
}
