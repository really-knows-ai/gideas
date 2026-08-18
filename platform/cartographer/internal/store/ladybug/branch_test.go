package ladybug

import (
	"context"
	"errors"
	"testing"

	"github.com/foundry/flow/cartographer/internal/store"
)

func TestBranch_CreateDrop(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)

	if err := s.CreateBranchDB(context.Background(), "tx1"); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.DropBranchDB(context.Background(), "tx1"); err != nil {
		t.Fatalf("DropBranchDB: %v", err)
	}
	// Idempotent drop should not error.
	if err := s.DropBranchDB(context.Background(), "tx1"); err != nil {
		t.Errorf("expected idempotent drop to succeed: %v", err)
	}
}

func TestBranch_IsolatedWrites(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	if err := s.CreateBranchDB(context.Background(), "tx1"); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.ReplicateSchemaToBranch(context.Background(), "tx1"); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}

	// Create entity on branch.
	e, err := s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"name": "branch-only"}, nil, "tx1")
	if err != nil {
		t.Fatalf("CreateEntity on branch: %v", err)
	}

	// Entity should be visible on branch.
	got, err := s.GetEntity(context.Background(), e.Id, "tx1")
	if err != nil {
		t.Fatalf("GetEntity on branch: %v", err)
	}
	if got.Properties["name"] != "branch-only" {
		t.Errorf("name = %q, want %q", got.Properties["name"], "branch-only")
	}

	// Entity should NOT be visible on main.
	_, err = s.GetEntity(context.Background(), e.Id, "")
	if err == nil {
		t.Error("expected entity to NOT exist on main")
	}
	if !errors.Is(err, store.ErrEntityNotFound) {
		t.Errorf("expected ErrEntityNotFound on main, got %v", err)
	}
}

func TestBranch_HydrationRoundTrip(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	if err := s.CreateBranchDB(context.Background(), "tx1"); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.ReplicateSchemaToBranch(context.Background(), "tx1"); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}

	// Create entities and edges on branch.
	src, err := s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"name": "src"}, nil, "tx1")
	if err != nil {
		t.Fatalf("CreateEntity src: %v", err)
	}
	tgt, err := s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"name": "tgt"}, nil, "tx1")
	if err != nil {
		t.Fatalf("CreateEntity tgt: %v", err)
	}
	edge, err := s.CreateEdge(context.Background(), "DependsOn", src.Id, tgt.Id,
		map[string]string{"strength": strengthValue}, "tx1")
	if err != nil {
		t.Fatalf("CreateEdge: %v", err)
	}

	// Snapshot branch state.
	branchEnts, err := s.DumpAllEntities(context.Background(), "tx1")
	if err != nil {
		t.Fatalf("DumpAllEntities on branch: %v", err)
	}
	branchEdges, err := s.DumpAllEdges(context.Background(), "tx1")
	if err != nil {
		t.Fatalf("DumpAllEdges on branch: %v", err)
	}

	// Rehydrate main from branch.
	if err := s.RehydrateFromBranch(context.Background(), "tx1"); err != nil {
		t.Fatalf("RehydrateFromBranch: %v", err)
	}

	// Verify data now exists on main.
	mainEnts, err := s.DumpAllEntities(context.Background(), "")
	if err != nil {
		t.Fatalf("DumpAllEntities on main: %v", err)
	}
	if len(mainEnts) != len(branchEnts) {
		t.Errorf("expected %d entities on main, got %d", len(branchEnts), len(mainEnts))
	}

	mainEdges, err := s.DumpAllEdges(context.Background(), "")
	if err != nil {
		t.Fatalf("DumpAllEdges on main: %v", err)
	}
	if len(mainEdges) != len(branchEdges) {
		t.Errorf("expected %d edges on main, got %d", len(branchEdges), len(mainEdges))
	}

	// Verify individual entity and edge survive the round trip.
	got, err := s.GetEntity(context.Background(), src.Id, "")
	if err != nil {
		t.Fatalf("GetEntity on main after hydration: %v", err)
	}
	if got.Properties["name"] != "src" {
		t.Errorf("name = %q, want %q", got.Properties["name"], "src")
	}

	_, err = s.GetEdge(context.Background(), edge.Id, "")
	if err != nil {
		t.Fatalf("GetEdge on main after hydration: %v", err)
	}
}

// TestBranch_DropBranchDB_LeavesMainUnbootstrapped verifies SPEC R7 "branch
// scope": a vector dimension bootstrapped by a transaction branch, then rolled
// back via DropBranchDB, leaves main un-bootstrapped (GetEstablishedDimension
// == 0). The bootstrap DDL (ALTER TABLE ADD embedding + CREATE_VECTOR_INDEX)
// runs on the branch's own connection; dropping the branch must not leak that
// side-effect into main.
func TestBranch_DropBranchDB_LeavesMainUnbootstrapped(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)
	ctx := context.Background()

	assertVectorIndexState(t, s, "VectorType", "", false, "expected VectorType not bootstrapped on main before branch")

	const branch = "tx-drop-bootstrap"
	if err := s.CreateBranchDB(ctx, branch); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.ReplicateSchemaToBranch(ctx, branch); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}

	// Bootstrap the vector dimension inside the branch.
	if _, err := s.CreateEntity(ctx, "VectorType", "",
		map[string]string{"name": "v"}, []float32{1, 2, 3}, branch); err != nil {
		t.Fatalf("CreateEntity on branch: %v", err)
	}
	assertVectorIndexState(t, s, "VectorType", branch, true, "expected VectorType bootstrapped on branch")

	// Rollback: drop the branch.
	if err := s.DropBranchDB(ctx, branch); err != nil {
		t.Fatalf("DropBranchDB: %v", err)
	}

	// Main must remain un-bootstrapped (SPEC R7 branch scope).
	assertVectorIndexState(t, s, "VectorType", "", false, "main must not be bootstrapped after branch rollback")
	dim, err := s.GetEstablishedDimension(context.Background(), "VectorType", "")
	if err != nil {
		t.Fatalf("GetEstablishedDimension: %v", err)
	}
	if dim != 0 {
		t.Fatalf("expected main dimension 0 after branch rollback, got %d", dim)
	}
}

// TestBranch_InheritsMainVectorDimension_RejectsConflict verifies that a
// branch opened over a pre-bootstrapped main inherits main's established
// vector dimension (via ReplicateSchemaToBranch copying the FLOAT[n] column
// and HNSW index) and rejects a CreateEntity whose embedding dimension
// conflicts. This is the store-layer path that surfaces the ABORTED Refresh
// conflict of SPEC R7 when the branch's dimension disagrees with main's.
func TestBranch_InheritsMainVectorDimension_RejectsConflict(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)
	ctx := context.Background()

	// Bootstrap main to dimension 3.
	if _, err := s.CreateEntity(ctx, "VectorType", "",
		map[string]string{"name": "main-v"}, []float32{1, 2, 3}, ""); err != nil {
		t.Fatalf("bootstrap main vector: %v", err)
	}
	if dim, err := s.GetEstablishedDimension(context.Background(), "VectorType", ""); err != nil || dim != 3 {
		t.Fatalf("main dimension = %d, err = %v", dim, err)
	}

	const branch = "tx-inherit-dim"
	if err := s.CreateBranchDB(ctx, branch); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.ReplicateSchemaToBranch(ctx, branch); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}

	// Matching dimension on branch — should succeed.
	if _, err := s.CreateEntity(ctx, "VectorType", "",
		map[string]string{"name": "branch-v"}, []float32{4, 5, 6}, branch); err != nil {
		t.Fatalf("CreateEntity on branch with matching dimension: %v", err)
	}

	// Conflicting dimension on branch — must fail with ErrEmbeddingDimension.
	_, err = s.CreateEntity(ctx, "VectorType", "",
		map[string]string{"name": "conflict"}, []float32{1, 2, 3, 4, 5}, branch)
	if err == nil {
		t.Fatal("expected dimension mismatch error on branch")
	}
	if !errors.Is(err, store.ErrEmbeddingDimension) {
		t.Errorf("expected ErrEmbeddingDimension, got %v", err)
	}
}
