package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
	"github.com/foundry/flow/cartographer/internal/store/ladybug"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestRefreshTransaction_RehydrationFailureIsInternal pins SPEC error-table
// row "Commit serialisation or re-hydration failed" (INTERNAL, SPEC:987) for
// the RefreshTransaction branch re-hydration path: a refresh whose
// HydrateBranchFromFiles fails with the store's ErrInvalidEntityDir sentinel
// must surface INTERNAL, never the INVALID_ARGUMENT the old mapStoreError
// mapping produced. TestRefreshTransaction_HydrationFailureDoesNotAdvanceSyncHead
// covers the plain-error hydration failure; this test covers the sentinel that
// previously hit the removed ErrInvalidEntityDir/ErrInvalidEdgeDir →
// INVALID_ARGUMENT mappings (errors.go).
func TestRefreshTransaction_RehydrationFailureIsInternal(t *testing.T) {
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
	t.Cleanup(func() { _ = base.Close() })
	failHydration := false
	dirErr := &fakeStore{Store: base, onHydrateBranchFromFiles: func(
		ctx context.Context, txID, entitiesDir, edgesDir string,
	) error {
		if failHydration {
			return fmt.Errorf("%w: entities directory inconsistent", store.ErrInvalidEntityDir)
		}
		return base.HydrateBranchFromFiles(ctx, txID, entitiesDir, edgesDir)
	}}
	gs, err := gitstore.New(dataPath)
	if err != nil {
		t.Fatalf("open git store: %v", err)
	}
	srv := NewCartographerServer(
		dirErr, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(dataPath),
	)
	srv.MarkDBReady()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "tx"}, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	// Main advances while the transaction is open so the refresh re-hydrates.
	interim, err := base.CreateEntity(ctx, "Component", "", map[string]string{"name": "interim"}, nil, "main")
	if err != nil {
		t.Fatalf("create interim entity: %v", err)
	}
	commitGitEntity(ctx, t, gs, interim.Id, "interim")
	// Arm the failure for the refresh's HydrateBranchFromFiles call (the
	// BeginTransaction call has already completed).
	failHydration = true

	_, err = srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{TransactionId: begin.TransactionId})
	if err == nil {
		t.Fatal("expected branch re-hydration failure during refresh")
	}
	if status.Code(err) != codes.Internal {
		t.Fatalf("expected Internal for branch re-hydration failure, got %v (%v)", status.Code(err), err)
	}
}

// TestRefreshTransaction_MissingTxCapability pins SPEC R3 (SPEC:244): a
// caller without WRITE:graph/tx is denied RefreshTransaction with
// PERMISSION_DENIED before any transaction lookup or validation.
func TestRefreshTransaction_MissingTxCapability(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "",
		30*time.Second, "test-ns", 30*time.Minute, 100000)
	srv.MarkDBReady()

	// Only entity capabilities, no WRITE:graph/tx.
	ctx := capabilityContext("READ:graph/entity/*,WRITE:graph/entity/*", scPriv, "sidecar")

	_, err := srv.RefreshTransaction(ctx, &flowv1.RefreshTransactionRequest{
		TransactionId: testMutationEntityID,
	})
	if err == nil {
		t.Fatal("expected PermissionDenied for missing WRITE:graph/tx capability, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

// TestStoreCloseBranchDB_CheckpointsDataBeforeFileRename pins the store
// primitive RefreshTransaction's swap relies on: closing a file-backed branch
// must checkpoint its write-ahead log into the .lbug file (so a renamed .lbug
// is complete) and must not delete the persisted branch files. After the
// close, moving the .lbug to a new name without its WAL companion — exactly
// what a crash between the refresh swap's rename and the (old) close did —
// must still yield every row from the file alone.
func TestStoreCloseBranchDB_CheckpointsDataBeforeFileRename(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed store + reopen/durability")
	}
	dataPath := t.TempDir()
	st, err := ladybug.Open(dataPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	applyTestSchema(ctx, t, st)
	const txID = "11111111-1111-4111-8111-111111111111"
	if err := st.CreateBranchDB(ctx, txID); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := st.ReplicateSchemaToBranch(ctx, txID); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}
	ent, err := st.CreateEntity(ctx, "Component", "", map[string]string{"name": "kept"}, nil, txID)
	if err != nil {
		t.Fatalf("CreateEntity on branch: %v", err)
	}
	// Close the branch: this must checkpoint the WAL into the .lbug and must
	// not remove the persisted files.
	if err := st.CloseBranchDB(ctx, txID); err != nil {
		t.Fatalf("CloseBranchDB: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataPath, "branches", txID+".lbug")); err != nil {
		t.Fatalf("CloseBranchDB deleted the branch file: %v", err)
	}
	// Simulate the refresh swap's crash: rename the branch's files onto a new
	// name WITHOUT the .lbug.wal WAL companion — the engine's path-based WAL
	// recovery cannot find the orphaned <old>.wal, so only data checkpointed
	// into the .lbug before the rename survives.
	const moved = "22222222-2222-4222-8222-222222222222"
	for _, suffix := range []string{".lbug", ".schema.json", ".state.json"} {
		src := filepath.Join(dataPath, "branches", txID+suffix)
		if _, err := os.Stat(src); err != nil {
			continue // no such file (e.g. no state record saved yet)
		}
		if err := os.Rename(src, filepath.Join(dataPath, "branches", moved+suffix)); err != nil {
			t.Fatalf("rename branch file %s: %v", suffix, err)
		}
	}
	got, err := st.GetEntity(ctx, ent.Id, moved)
	if err != nil {
		t.Fatalf("entity lost after close+rename (WAL not checkpointed): %v", err)
	}
	if got.Properties["name"] != "kept" {
		t.Fatalf("entity content = %+v, want name=kept", got.Properties)
	}
}
