package ladybug

import (
	"context"
	"errors"
	"testing"

	"github.com/foundry/flow/cartographer/internal/store"
	"github.com/google/uuid"
)

// ResolveEntityType is the store primitive backing SPEC R3's authoritative
// source-entity-type lookup for the DeleteEdge/UpdateEntity/DeleteEntity
// capability checks (cartographer_server.go:1108/1163/1249/1345): the server
// resolves the entity's type, then checks the caller's capabilities against
// that type. Pins the found branch: an existing entity resolves to its type.
func TestResolveEntityType_Found(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	e, err := s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"name": "resolvable"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	entityType, err := s.ResolveEntityType(context.Background(), e.Id, "")
	if err != nil {
		t.Fatalf("ResolveEntityType: %v", err)
	}
	if entityType != e.Type {
		t.Errorf("resolved type = %q, want %q", entityType, e.Type)
	}
}

// Pins the not-found branch: an absent entity must surface the ErrEntityNotFound
// sentinel (learnings rule "Sentinel errors over zero-value returns") rather
// than a zero-value ("", nil).
func TestResolveEntityType_NotFound(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.ResolveEntityType(context.Background(), uuid.NewString(), "")
	if err == nil {
		t.Fatal("expected error for non-existent entity")
	}
	if !errors.Is(err, store.ErrEntityNotFound) {
		t.Errorf("expected ErrEntityNotFound, got %v", err)
	}
}

// Pins the branch-scoped path: with a txID argument the lookup resolves against
// that transaction's isolated LadybugDB instance (SPEC R2), never main. An
// entity created on the branch is resolvable on the branch and NOT on main; an
// entity on main is resolvable on main and NOT on the branch.
func TestResolveEntityType_BranchScoped(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)
	ctx := context.Background()

	const branch = "tx1"
	if err := s.CreateBranchDB(ctx, branch); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.ReplicateSchemaToBranch(ctx, branch); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}

	mainEntity, err := s.CreateEntity(ctx, "Document", "",
		map[string]string{"title": "main-only"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity on main: %v", err)
	}
	branchEntity, err := s.CreateEntity(ctx, "Component", "",
		map[string]string{"name": "branch-only"}, nil, branch)
	if err != nil {
		t.Fatalf("CreateEntity on branch: %v", err)
	}

	// Each scope resolves its own data.
	mainType, err := s.ResolveEntityType(ctx, mainEntity.Id, "")
	if err != nil || mainType != "Document" {
		t.Fatalf("resolve main entity on main: type=%q err=%v", mainType, err)
	}
	branchType, err := s.ResolveEntityType(ctx, branchEntity.Id, branch)
	if err != nil || branchType != branchEntity.Type {
		t.Fatalf("resolve branch entity on branch: type=%q err=%v", branchType, err)
	}

	// Isolation: branch data is invisible to main and vice versa.
	if _, err := s.ResolveEntityType(ctx, branchEntity.Id, ""); !errors.Is(err, store.ErrEntityNotFound) {
		t.Fatalf("branch entity resolvable on main, want ErrEntityNotFound, got %v", err)
	}
	if _, err := s.ResolveEntityType(ctx, mainEntity.Id, branch); !errors.Is(err, store.ErrEntityNotFound) {
		t.Fatalf("main entity resolvable on branch, want ErrEntityNotFound, got %v", err)
	}
}
