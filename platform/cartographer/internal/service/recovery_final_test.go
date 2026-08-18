package service

import (
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store/ladybug"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestRecoverOpenTransactionsAbortedRefreshRollsBackLoudly pins SPEC R9 refresh
// step 4's "On ABORTED, the transaction's change log is preserved" guarantee
// together with its crash-durability boundary (SPEC R9 change-log recovery
// point 4): after a refresh returns ABORTED, the in-memory change log is still
// inspectable via GetTransactionDiff (the node can decide how to proceed), but
// it is in-memory only — the branch DB was restored to a clean copy of main. A
// crash in the window before the node rolls back therefore loses the change
// log: recovery reconstructs an empty diff and, because the refresh-in-progress
// marker is still set on the durable record, rolls the transaction back loudly
// — never silently reporting it as committed and never touching another
// transaction's data.
func TestRecoverOpenTransactionsAbortedRefreshRollsBackLoudly(t *testing.T) {
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
	mainA, err := st.CreateEntity(ctx, "Component", "", map[string]string{"name": "main"}, nil, "main")
	if err != nil {
		t.Fatalf("create main entity A: %v", err)
	}
	commitGitEntity(ctx, t, gs, mainA.Id, "main")

	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	// The transaction modifies A; main also modifies A, forcing an ABORTED
	// refresh conflict.
	if _, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id: mainA.Id, Properties: map[string]string{"name": "tx"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("UpdateEntity in transaction: %v", err)
	}
	if err := gs.WithGitLock(func() error {
		if err := gs.RestoreMain(ctx); err != nil {
			return err
		}
		if err := gs.WriteEntityFiles(ctx, "Component", []gitstore.Entity{{
			ID: mainA.Id, Type: "Component", Properties: map[string]string{"name": "main-v2"},
		}}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx, "entities"); err != nil {
			return err
		}
		return gs.Commit(ctx, "main advances")
	}); err != nil {
		t.Fatalf("advance main with conflicting change: %v", err)
	}

	// Refresh returns ABORTED (the transaction modified A while main also
	// changed A).
	if _, err := srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: begin.TransactionId,
	}); status.Code(err) != codes.Aborted {
		t.Fatalf("expected ABORTED refresh, got %v", err)
	}

	// SPEC step 4 (in-process): the change log is preserved after ABORTED — the
	// node can still inspect the diff.
	diff, err := srv.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{TransactionId: begin.TransactionId})
	if err != nil {
		t.Fatalf("GetTransactionDiff after ABORTED: %v", err)
	}
	if len(diff.ModifiedEntities) != 1 || diff.ModifiedEntities[0].Id != mainA.Id {
		t.Fatalf("expected the transaction's change preserved after ABORTED, got %+v", diff.ModifiedEntities)
	}
	durable, err := st.LoadBranchTransactionState(ctx, begin.TransactionId)
	if err != nil {
		t.Fatalf("load branch state: %v", err)
	}
	if !durable.BranchRefreshInProgress {
		t.Fatal("expected refresh-in-progress marker set on the durable record after ABORTED")
	}

	oldLogger := slog.Default()
	defer slog.SetDefault(oldLogger)
	captured := &captureLogHandler{}
	slog.SetDefault(slog.New(captured))

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
	restarted.MarkDBReady()
	if err := restarted.RecoverOpenTransactions(ctx); err != nil {
		t.Fatalf("RecoverOpenTransactions: %v", err)
	}
	// The transaction is rolled back loudly (empty reconstructed diff + marker),
	// never recovered as active.
	if _, err := restarted.txManager.Lookup(begin.TransactionId); err == nil {
		t.Fatal("ABORTED-refresh-crash transaction was recovered as active despite an empty branch diff")
	}
	if !slices.Contains(captured.messages,
		"RecoverOpenTransactions: rolled back transaction interrupted by a mid-refresh crash (never committed)") {
		t.Fatalf("expected loud mid-refresh rollback log, got %v", captured.messages)
	}
}

// TestRecoverOpenTransactionsMidSwapMismatchRollsBack pins the branch-DB swap
// crash window (SPEC R9 change-log recovery): resetBranchStoreFromWorkingTree
// swaps the branch DB via a non-atomic rename loop (.lbug → .schema.json →
// .state.json), so a crash between the .lbug rename and the .schema.json rename
// leaves the swapped-in branch DB (built under main's *current* schema) paired
// with the stale pre-refresh .schema.json. When main gained a non-destructive
// R6 schema change during the transaction's lifetime, reopening that branch
// fails hard in restoreBranchSchemaMetadata → validateMetadataAgainstCatalog
// ("database entity type %q is absent from schema metadata"), a hard error the
// old recovery propagated and cmd/main.go treated as fatal (pod crash loop).
// Recovery must roll the mid-swap casualty back loudly instead.
func TestRecoverOpenTransactionsMidSwapMismatchRollsBack(t *testing.T) {
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
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "tx"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	// Main gains a non-destructive R6 schema change during the transaction's
	// lifetime: a new entity type added to the schema.
	altered := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string", Required: true},
					{Name: "version", Type: "string"},
				},
			},
			{
				Name: "Service",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string", Required: true},
				},
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"Component"}, Using: []string{"DEPENDS_ON"}},
				},
			},
			{Name: "Widget", Properties: []*flowv1.Property{{Name: "label", Type: "string"}}},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "DEPENDS_ON", Properties: []*flowv1.Property{{Name: "weight", Type: "string"}}},
		},
	}
	if err := st.ApplySchema(ctx, altered); err != nil {
		t.Fatalf("apply additive schema change: %v", err)
	}
	// Build the refresh replacement branch (under main's *current* schema, which
	// now includes Widget) exactly as resetBranchStoreFromWorkingTree does, close
	// it so its WAL is checkpointed, and rename only its .lbug onto the
	// transaction's canonical name — leaving the pre-refresh branches/<txID>.
	// schema.json (which does not declare Widget) in place. This is the on-disk
	// state a crash between the swap's .lbug and .schema.json renames leaves
	// behind.
	const tempID = "99999999-9999-4999-8999-999999999999"
	if err := srv.buildBranchStoreFromWorkingTree(ctx, tempID); err != nil {
		t.Fatalf("build replacement branch: %v", err)
	}
	if err := st.CloseBranchDB(ctx, tempID); err != nil {
		t.Fatalf("close replacement branch: %v", err)
	}
	// The durable state record carries the refresh-in-progress marker across the
	// swap (persisted before the renames begin, cleared only by the refresh's
	// final persist).
	durable, err := st.LoadBranchTransactionState(ctx, begin.TransactionId)
	if err != nil {
		t.Fatalf("load durable state: %v", err)
	}
	durable.BranchRefreshInProgress = true
	if err := st.SaveBranchTransactionState(ctx, begin.TransactionId, durable); err != nil {
		t.Fatalf("persist refresh marker: %v", err)
	}
	// Evict the live in-memory handle for the tx branch so a reopen reloads from
	// the (mismatched) files, then swap in the replacement .lbug.
	if err := st.CloseBranchDB(ctx, begin.TransactionId); err != nil {
		t.Fatalf("close tx branch handle: %v", err)
	}
	branchesDir := filepath.Join(dataPath, "branches")
	if err := os.Rename(
		filepath.Join(branchesDir, tempID+".lbug"),
		filepath.Join(branchesDir, begin.TransactionId+".lbug"),
	); err != nil {
		t.Fatalf("swap replacement .lbug onto tx name: %v", err)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	// Restart: the mismatched branch must be rolled back loudly, not crash-loop
	// startup with a hard error.
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
	if _, err := restarted.txManager.Lookup(begin.TransactionId); err == nil {
		t.Fatalf("expected mid-swap-crash transaction rolled back, got a registered transaction")
	}
	if _, err := os.Stat(filepath.Join(branchesDir, begin.TransactionId+".lbug")); !os.IsNotExist(err) {
		t.Fatalf("rollback did not remove the mismatched branch DB: %v", err)
	}
}

// TestRecoverOpenTransactionsCleansOrphanedRefreshTempBranches pins the
// mid-refresh swap crash-window cleanup (SPEC R9 change-log recovery): the
// refresh branch-DB swap (resetBranchStoreFromWorkingTree) builds the
// replacement branch under a temporary key (tempID := s.newIDFn()) and renames
// branches/<tempID>.{lbug,schema.json,state.json} onto the transaction's
// canonical names, so a crash after some but not all of the swap's renames
// strands the not-yet-renamed temp files under branches/. The temporary key
// never becomes a git branch, so recovery's git-branch enumeration never
// visits it — without the sweep every mid-refresh crash leaks the orphaned
// temp files (plus the engine's write-ahead-log companion) indefinitely. The
// sweep must remove the orphaned temp files while leaving live transaction
// branches (git branch + branch files) untouched.
func TestRecoverOpenTransactionsCleansOrphanedRefreshTempBranches(t *testing.T) {
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
	srv.MarkDBReady()
	applyTestSchema(ctx, t, st)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "tx"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	branchesDir := filepath.Join(dataPath, "branches")

	// Replicate the swap's on-disk state exactly as resetBranchStoreFromWorkingTree
	// builds it, then crash after the first rename: build the replacement under
	// a temp key, mark the refresh in progress, mirror the durable state
	// record, re-apply the transaction's changes, close the replacement (its
	// WAL checkpointed), and rename only the temp .lbug onto the transaction's
	// canonical name — leaving the temp .schema.json and .state.json (and the
	// engine's torn WAL companion) orphaned under the temp key.
	const tempID = "88888888-8888-4888-8888-888888888888"
	if err := srv.buildBranchStoreFromWorkingTree(ctx, tempID); err != nil {
		t.Fatalf("build replacement branch: %v", err)
	}
	state, lookupErr := srv.txManager.Lookup(begin.TransactionId)
	if lookupErr != nil {
		t.Fatalf("look up transaction state: %v", lookupErr)
	}
	state.BranchRefreshInProgress = true
	if err := srv.persistTransactionState(ctx, state); err != nil {
		t.Fatalf("persist refresh marker: %v", err)
	}
	if err := st.SaveBranchTransactionState(ctx, tempID, durableTransactionState(state)); err != nil {
		t.Fatalf("persist temp state record: %v", err)
	}
	if err := srv.reapplyTransactionChanges(ctx, tempID, state.ChangeLog); err != nil {
		t.Fatalf("re-apply transaction changes onto replacement: %v", err)
	}
	if err := st.CloseBranchDB(ctx, tempID); err != nil {
		t.Fatalf("close replacement branch: %v", err)
	}
	// Evict the live in-memory handle for the tx branch (its files stay on
	// disk) so the rename below does not race the engine's handle, mirroring
	// the swap's CloseBranchDB(txID) before its renames.
	if err := st.CloseBranchDB(ctx, begin.TransactionId); err != nil {
		t.Fatalf("close tx branch handle: %v", err)
	}
	if err := os.Rename(
		filepath.Join(branchesDir, tempID+".lbug"),
		filepath.Join(branchesDir, begin.TransactionId+".lbug"),
	); err != nil {
		t.Fatalf("swap replacement .lbug onto tx name: %v", err)
	}
	// The engine's write-ahead-log companion (<key>.lbug.wal) is the artifact a
	// hard crash tears alongside the database file; plant it so the sweep's WAL
	// cleanup is pinned.
	walPath := filepath.Join(branchesDir, tempID+".lbug.wal")
	if err := os.WriteFile(walPath, []byte("torn wal"), 0600); err != nil {
		t.Fatalf("plant torn temp WAL: %v", err)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	// Restart: the orphaned temp files must be swept while the canonical
	// transaction branch survives recovery.
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
	// The orphaned refresh temp files (including the torn WAL) are gone.
	for _, name := range []string{
		tempID + ".lbug", tempID + ".schema.json", tempID + ".state.json", tempID + ".lbug.wal",
	} {
		if _, err := os.Stat(filepath.Join(branchesDir, name)); !os.IsNotExist(err) {
			t.Fatalf("orphaned refresh temp file %q survived recovery: %v", name, err)
		}
	}
	// The canonical transaction branch survives: it is recovered as a live
	// transaction and its branch DB is intact.
	if _, err := restarted.txManager.Lookup(begin.TransactionId); err != nil {
		t.Fatalf("canonical transaction branch was not recovered: %v", err)
	}
	if _, err := os.Stat(filepath.Join(branchesDir, begin.TransactionId+".lbug")); err != nil {
		t.Fatalf("canonical transaction branch DB missing: %v", err)
	}
}
