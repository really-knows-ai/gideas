package ladybug

import (
	"context"
	"errors"
	"testing"

	"github.com/foundry/flow/cartographer/internal/store"
)

func TestBranch_ExecuteCypherScoped(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	setupBranch(t, s)
	ctx := context.Background()

	branchOnly, err := s.CreateEntity(ctx, "Component", "",
		map[string]string{"name": "cypher-branch"}, nil, "tx1")
	if err != nil {
		t.Fatalf("CreateEntity on branch: %v", err)
	}
	mainOnly, err := s.CreateEntity(ctx, "Component", "",
		map[string]string{"name": "cypher-main"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity on main: %v", err)
	}

	// The branch query sees only branch data.
	branchRows, err := s.ExecuteCypher(ctx, "MATCH (n:Component) RETURN n.id AS id", nil, "tx1")
	if err != nil {
		t.Fatalf("ExecuteCypher on branch: %v", err)
	}
	if len(branchRows) != 1 || branchRows[0].Values[0] != branchOnly.Id {
		t.Fatalf("branch ExecuteCypher must see only branch data, got %+v", branchRows)
	}
	// Main's query sees only main data.
	mainRows, err := s.ExecuteCypher(ctx, "MATCH (n:Component) RETURN n.id AS id", nil, "")
	if err != nil {
		t.Fatalf("ExecuteCypher on main: %v", err)
	}
	if len(mainRows) != 1 || mainRows[0].Values[0] != mainOnly.Id {
		t.Fatalf("main ExecuteCypher must see only main data, got %+v", mainRows)
	}
}

func TestBranch_SearchNeighborsScoped(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	setupBranch(t, s)
	ctx := context.Background()

	// Bootstrap VectorType to dimension 3 on the branch (branch-scoped
	// dimension lock, SPEC R7) and on main.
	if _, err := s.CreateEntity(ctx, "VectorType", "",
		map[string]string{"name": "branch-v"}, []float32{1, 2, 3}, "tx1"); err != nil {
		t.Fatalf("bootstrap branch vector: %v", err)
	}
	if _, err := s.CreateEntity(ctx, "VectorType", "",
		map[string]string{"name": "main-v"}, []float32{1, 2, 3}, ""); err != nil {
		t.Fatalf("bootstrap main vector: %v", err)
	}

	// A branch-scoped search sees only the branch's vector entity.
	branchResults, err := s.SearchNeighbors(ctx, []float32{1, 2, 3}, "VectorType", 10, "tx1")
	if err != nil {
		t.Fatalf("SearchNeighbors on branch: %v", err)
	}
	if len(branchResults) != 1 || branchResults[0].Entity.Properties["name"] != "branch-v" {
		t.Fatalf("branch SearchNeighbors must see only branch data, got %+v", branchResults)
	}
	// A main search sees only the main entity.
	mainResults, err := s.SearchNeighbors(ctx, []float32{1, 2, 3}, "VectorType", 10, "")
	if err != nil {
		t.Fatalf("SearchNeighbors on main: %v", err)
	}
	if len(mainResults) != 1 || mainResults[0].Entity.Properties["name"] != "main-v" {
		t.Fatalf("main SearchNeighbors must see only main data, got %+v", mainResults)
	}
}

func TestBranch_FullTextSearchScoped(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	setupBranch(t, s)
	ctx := context.Background()

	if _, err := s.CreateEntity(ctx, "Document", "",
		map[string]string{"title": "needle-branch", "body": "branch body"}, nil, "tx1"); err != nil {
		t.Fatalf("CreateEntity on branch: %v", err)
	}
	if _, err := s.CreateEntity(ctx, "Document", "",
		map[string]string{"title": "needle-main", "body": "main body"}, nil, ""); err != nil {
		t.Fatalf("CreateEntity on main: %v", err)
	}

	branchResults, err := s.FullTextSearch(ctx, "needle", "Document", "tx1")
	if err != nil {
		t.Fatalf("FullTextSearch on branch: %v", err)
	}
	if len(branchResults) != 1 || branchResults[0].Properties["title"] != "needle-branch" {
		t.Fatalf("branch FullTextSearch must see only branch data, got %+v", branchResults)
	}
	mainResults, err := s.FullTextSearch(ctx, "needle", "Document", "")
	if err != nil {
		t.Fatalf("FullTextSearch on main: %v", err)
	}
	if len(mainResults) != 1 || mainResults[0].Properties["title"] != "needle-main" {
		t.Fatalf("main FullTextSearch must see only main data, got %+v", mainResults)
	}
}

func TestBranch_ListEntitiesScoped(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	setupBranch(t, s)
	ctx := context.Background()

	branchOnly, err := s.CreateEntity(ctx, "Component", "",
		map[string]string{"name": "list-branch"}, nil, "tx1")
	if err != nil {
		t.Fatalf("CreateEntity on branch: %v", err)
	}
	mainOnly, err := s.CreateEntity(ctx, "Component", "",
		map[string]string{"name": "list-main"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity on main: %v", err)
	}

	branchEnts, _, err := s.ListEntities(ctx, "Component", 0, "", "tx1")
	if err != nil {
		t.Fatalf("ListEntities on branch: %v", err)
	}
	if len(branchEnts) != 1 || branchEnts[0].Id != branchOnly.Id {
		t.Fatalf("branch ListEntities must see only branch data, got %+v", branchEnts)
	}
	mainEnts, _, err := s.ListEntities(ctx, "Component", 0, "", "")
	if err != nil {
		t.Fatalf("ListEntities on main: %v", err)
	}
	if len(mainEnts) != 1 || mainEnts[0].Id != mainOnly.Id {
		t.Fatalf("main ListEntities must see only main data, got %+v", mainEnts)
	}
}

// TestBranch_UpdateEntityScoped verifies a branch-scoped UpdateEntity mutates
// only the transaction's isolated instance: the change is visible on the branch
// but not on main.
func TestBranch_UpdateEntityScoped(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	setupBranch(t, s)
	ctx := context.Background()

	// Create the entity on both main and the branch (replicated schema, so the
	// same id is valid in both scopes).
	e, err := s.CreateEntity(ctx, "Component", "",
		map[string]string{"name": "shared", "version": "1"}, nil, "tx1")
	if err != nil {
		t.Fatalf("CreateEntity on branch: %v", err)
	}
	if _, err := s.CreateEntity(ctx, "Component", e.Id,
		map[string]string{"name": "shared", "version": "1"}, nil, ""); err != nil {
		t.Fatalf("CreateEntity on main: %v", err)
	}

	updated, err := s.UpdateEntity(ctx, e.Id, map[string]string{"version": "2"}, nil, "tx1")
	if err != nil {
		t.Fatalf("UpdateEntity on branch: %v", err)
	}
	if updated.Properties["version"] != "2" {
		t.Fatalf("branch update version = %q, want %q", updated.Properties["version"], "2")
	}

	// The branch sees the update; main still holds the original value.
	branchGot, err := s.GetEntity(ctx, e.Id, "tx1")
	if err != nil {
		t.Fatalf("GetEntity on branch: %v", err)
	}
	if branchGot.Properties["version"] != "2" {
		t.Fatalf("branch entity version = %q, want %q", branchGot.Properties["version"], "2")
	}
	mainGot, err := s.GetEntity(ctx, e.Id, "")
	if err != nil {
		t.Fatalf("GetEntity on main: %v", err)
	}
	if mainGot.Properties["version"] != "1" {
		t.Fatalf("main entity version = %q, want %q (update must not leak)", mainGot.Properties["version"], "1")
	}
}

// TestBranch_DeleteEntityScoped verifies a branch-scoped DeleteEntity removes
// the entity from the transaction's isolated instance only: gone on the branch,
// still present on main.
func TestBranch_DeleteEntityScoped(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	setupBranch(t, s)
	ctx := context.Background()

	e, err := s.CreateEntity(ctx, "Component", "",
		map[string]string{"name": "to-delete"}, nil, "tx1")
	if err != nil {
		t.Fatalf("CreateEntity on branch: %v", err)
	}
	if _, err := s.CreateEntity(ctx, "Component", e.Id,
		map[string]string{"name": "to-delete"}, nil, ""); err != nil {
		t.Fatalf("CreateEntity on main: %v", err)
	}

	deleted, err := s.DeleteEntity(ctx, e.Id, "tx1")
	if err != nil {
		t.Fatalf("DeleteEntity on branch: %v", err)
	}
	if deleted.Id != e.Id {
		t.Fatalf("deleted entity Id = %q, want %q", deleted.Id, e.Id)
	}

	if _, err := s.GetEntity(ctx, e.Id, "tx1"); !errors.Is(err, store.ErrEntityNotFound) {
		t.Fatalf("branch entity survived branch-scoped delete, want ErrEntityNotFound, got %v", err)
	}
	if _, err := s.GetEntity(ctx, e.Id, ""); err != nil {
		t.Fatalf("main entity must survive a branch-scoped delete: %v", err)
	}
}

// TestBranch_DeleteEdgeScoped verifies a branch-scoped DeleteEdge removes the
// edge from the transaction's isolated instance only: gone on the branch, still
// present on main.
func TestBranch_DeleteEdgeScoped(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	setupBranch(t, s)
	ctx := context.Background()

	// Create endpoints on the branch and an edge between them.
	src, err := s.CreateEntity(ctx, "Component", "", nil, nil, "tx1")
	if err != nil {
		t.Fatalf("CreateEntity src on branch: %v", err)
	}
	tgt, err := s.CreateEntity(ctx, "Component", "", nil, nil, "tx1")
	if err != nil {
		t.Fatalf("CreateEntity tgt on branch: %v", err)
	}
	edge, err := s.CreateEdge(ctx, "DependsOn", src.Id, tgt.Id,
		map[string]string{"strength": strengthValue}, "tx1")
	if err != nil {
		t.Fatalf("CreateEdge on branch: %v", err)
	}
	// Mirror the endpoints and edge on main so the delete targets a live edge.
	if _, err := s.CreateEntity(ctx, "Component", src.Id, nil, nil, ""); err != nil {
		t.Fatalf("CreateEntity src on main: %v", err)
	}
	if _, err := s.CreateEntity(ctx, "Component", tgt.Id, nil, nil, ""); err != nil {
		t.Fatalf("CreateEntity tgt on main: %v", err)
	}
	mainEdge, err := s.CreateEdge(ctx, "DependsOn", src.Id, tgt.Id,
		map[string]string{"strength": strengthValue}, "")
	if err != nil {
		t.Fatalf("CreateEdge on main: %v", err)
	}

	deleted, err := s.DeleteEdge(ctx, edge.Id, "tx1")
	if err != nil {
		t.Fatalf("DeleteEdge on branch: %v", err)
	}
	if deleted.Id != edge.Id {
		t.Fatalf("deleted edge Id = %q, want %q", deleted.Id, edge.Id)
	}

	if _, err := s.GetEdge(ctx, edge.Id, "tx1"); !errors.Is(err, store.ErrEdgeNotFound) {
		t.Fatalf("branch edge survived branch-scoped delete, want ErrEdgeNotFound, got %v", err)
	}
	if _, err := s.GetEdge(ctx, mainEdge.Id, ""); err != nil {
		t.Fatalf("main edge must survive a branch-scoped delete: %v", err)
	}
}

// TestBranch_GetEdgeScoped verifies a branch-scoped GetEdge reads the
// transaction's isolated instance: the branch sees its own edge, and a main
// read of the branch's edge ID fails.
func TestBranch_GetEdgeScoped(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	setupBranch(t, s)
	ctx := context.Background()

	src, err := s.CreateEntity(ctx, "Component", "", nil, nil, "tx1")
	if err != nil {
		t.Fatalf("CreateEntity src on branch: %v", err)
	}
	tgt, err := s.CreateEntity(ctx, "Component", "", nil, nil, "tx1")
	if err != nil {
		t.Fatalf("CreateEntity tgt on branch: %v", err)
	}
	edge, err := s.CreateEdge(ctx, "DependsOn", src.Id, tgt.Id,
		map[string]string{"strength": strengthValue}, "tx1")
	if err != nil {
		t.Fatalf("CreateEdge on branch: %v", err)
	}

	got, err := s.GetEdge(ctx, edge.Id, "tx1")
	if err != nil {
		t.Fatalf("GetEdge on branch: %v", err)
	}
	if got.Id != edge.Id {
		t.Fatalf("edge Id = %q, want %q", got.Id, edge.Id)
	}
	// The branch edge is not visible on main.
	if _, err := s.GetEdge(ctx, edge.Id, ""); !errors.Is(err, store.ErrEdgeNotFound) {
		t.Fatalf("branch edge visible on main, want ErrEdgeNotFound, got %v", err)
	}
}
