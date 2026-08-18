package ladybug

import (
	"context"
	"errors"
	"testing"

	"github.com/foundry/flow/cartographer/internal/store"
	uuid "github.com/google/uuid"
)

func TestGetEntity_Found(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	e, err := s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"name": "findme"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	got, err := s.GetEntity(context.Background(), e.Id, "")
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if got.Id != e.Id {
		t.Errorf("Id = %q, want %q", got.Id, e.Id)
	}
	if got.Properties["name"] != "findme" {
		t.Errorf("Properties[name] = %q, want %q", got.Properties["name"], "findme")
	}
}

func TestGetEntity_NotFound(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.GetEntity(context.Background(), uuid.New().String(), "")
	if err == nil {
		t.Fatal("expected error for non-existent entity")
	}
	if !errors.Is(err, store.ErrEntityNotFound) {
		t.Errorf("expected ErrEntityNotFound, got %v", err)
	}
}

func TestDeleteEntity_Valid(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	e, err := s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"name": "todelete"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	deleted, err := s.DeleteEntity(context.Background(), e.Id, "")
	if err != nil {
		t.Fatalf("DeleteEntity: %v", err)
	}
	if deleted.Id != e.Id {
		t.Errorf("deleted entity Id = %q, want %q", deleted.Id, e.Id)
	}

	_, err = s.GetEntity(context.Background(), e.Id, "")
	if err == nil {
		t.Error("expected error after deletion")
	}
}

func TestDeleteEntity_NotFound(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.DeleteEntity(context.Background(), uuid.New().String(), "")
	if err == nil {
		t.Fatal("expected error for non-existent entity")
	}
	if !errors.Is(err, store.ErrEntityNotFound) {
		t.Errorf("expected ErrEntityNotFound, got %v", err)
	}
}

func TestDeleteEntity_CascadeDeletesEdges(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	src, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity src: %v", err)
	}
	tgt, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity tgt: %v", err)
	}

	edge, err := s.CreateEdge(context.Background(), "DependsOn", src.Id, tgt.Id, nil, "")
	if err != nil {
		t.Fatalf("CreateEdge: %v", err)
	}

	// Delete source entity (cascade deletes edge via DETACH DELETE).
	_, err = s.DeleteEntity(context.Background(), src.Id, "")
	if err != nil {
		t.Fatalf("DeleteEntity: %v", err)
	}

	// Verify edge is gone.
	_, err = s.GetEdge(context.Background(), edge.Id, "")
	if err == nil {
		t.Error("expected edge to be cascade-deleted")
	}
	if !errors.Is(err, store.ErrEdgeNotFound) {
		t.Errorf("expected ErrEdgeNotFound, got %v", err)
	}
}
