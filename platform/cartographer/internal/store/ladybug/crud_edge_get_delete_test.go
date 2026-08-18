package ladybug

import (
	"context"
	"errors"
	"testing"

	"github.com/foundry/flow/cartographer/internal/store"
	uuid "github.com/google/uuid"
)

func TestDeleteEdge_Valid(t *testing.T) {
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

	deleted, err := s.DeleteEdge(context.Background(), edge.Id, "")
	if err != nil {
		t.Fatalf("DeleteEdge: %v", err)
	}
	if deleted.Id != edge.Id {
		t.Errorf("deleted edge Id = %q, want %q", deleted.Id, edge.Id)
	}

	_, err = s.GetEdge(context.Background(), edge.Id, "")
	if err == nil {
		t.Error("expected error after edge deletion")
	}

	// SPEC R7 point 3: "Edge deletion does not cascade to any entity" — both
	// endpoints must survive the edge's removal.
	if _, err := s.GetEntity(context.Background(), src.Id, ""); err != nil {
		t.Fatalf("source entity must survive edge deletion: %v", err)
	}
	if _, err := s.GetEntity(context.Background(), tgt.Id, ""); err != nil {
		t.Fatalf("target entity must survive edge deletion: %v", err)
	}
}

func TestDeleteEdge_NotFound(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.DeleteEdge(context.Background(), uuid.New().String(), "")
	if err == nil {
		t.Fatal("expected error for non-existent edge")
	}
	if !errors.Is(err, store.ErrEdgeNotFound) {
		t.Errorf("expected ErrEdgeNotFound, got %v", err)
	}
}

func TestGetEdge_NotFound(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.GetEdge(context.Background(), uuid.New().String(), "")
	if err == nil {
		t.Fatal("expected error for non-existent edge")
	}
	if !errors.Is(err, store.ErrEdgeNotFound) {
		t.Errorf("expected ErrEdgeNotFound, got %v", err)
	}
}

func TestDeleteEdge_InvalidUUID(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.DeleteEdge(context.Background(), "not-a-uuid", "")
	if err == nil {
		t.Fatal("expected error for invalid edge UUID")
	}
	if !errors.Is(err, store.ErrInvalidIDFormat) {
		t.Errorf("expected ErrInvalidIDFormat, got %v", err)
	}
}

func TestListEdgesOfType(t *testing.T) {
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
	tgt1, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity tgt1: %v", err)
	}
	tgt2, err := s.CreateEntity(context.Background(), "Component", "", nil, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity tgt2: %v", err)
	}

	_, err = s.CreateEdge(context.Background(), "DependsOn", src.Id, tgt1.Id, nil, "")
	if err != nil {
		t.Fatalf("CreateEdge 1: %v", err)
	}
	_, err = s.CreateEdge(context.Background(), "DependsOn", src.Id, tgt2.Id, nil, "")
	if err != nil {
		t.Fatalf("CreateEdge 2: %v", err)
	}

	edges, err := s.ListEdgesOfType(context.Background(), "DependsOn", "")
	if err != nil {
		t.Fatalf("ListEdgesOfType: %v", err)
	}
	if len(edges) != 2 {
		t.Fatalf("expected 2 edges, got %d", len(edges))
	}
}

func TestListEdgesOfType_UnknownEdgeType(t *testing.T) {
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)

	_, err = s.ListEdgesOfType(context.Background(), "DoesNotExist", "")
	if err == nil {
		t.Fatal("expected error for unknown edge type")
	}
	if !errors.Is(err, store.ErrUnknownEdgeType) {
		t.Errorf("expected ErrUnknownEdgeType, got %v", err)
	}
}
