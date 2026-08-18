package gitstore

import (
	"testing"
	"time"
)

func TestChangeLogAddEntity(t *testing.T) {
	cl := NewChangeLog()
	if err := cl.Add(ChangeLogEntry{
		Kind: ChangeAddEntity,
		ID:   "id-1",
		Type: testComponentType,
		Entity: &EntityEntry{
			ID:         "id-1",
			Type:       testComponentType,
			Properties: map[string]string{"name": "x"},
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		},
	}); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if cl.Len() != 1 {
		t.Fatalf("expected Len()=1, got %d", cl.Len())
	}
	if len(cl.AddedEntities) != 1 {
		t.Fatalf("expected 1 AddedEntity, got %d", len(cl.AddedEntities))
	}
	if cl.AddedEntities["id-1"].Type != testComponentType {
		t.Fatalf("expected type Component, got %q", cl.AddedEntities["id-1"].Type)
	}
}

func TestChangeLogModifyEntity(t *testing.T) {
	cl := NewChangeLog()
	if err := cl.Add(ChangeLogEntry{
		Kind: ChangeModEntity,
		ID:   "id-1",
		Type: testComponentType,
		Entity: &EntityEntry{
			ID:         "id-1",
			Type:       testComponentType,
			Properties: map[string]string{"name": "updated"},
			UpdatedAt:  time.Now(),
		},
	}); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if cl.Len() != 1 {
		t.Fatalf("expected Len()=1, got %d", cl.Len())
	}
	if len(cl.ModifiedEntities) != 1 {
		t.Fatalf("expected 1 ModifiedEntity, got %d", len(cl.ModifiedEntities))
	}
}

func TestChangeLogDeleteEntity(t *testing.T) {
	cl := NewChangeLog()
	if err := cl.Add(ChangeLogEntry{
		Kind:      ChangeDelEntity,
		ID:        "id-1",
		Type:      testComponentType,
		Suspected: false,
	}); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if cl.Len() != 1 {
		t.Fatalf("expected Len()=1, got %d", cl.Len())
	}
	if len(cl.DeletedEntities) != 1 {
		t.Fatalf("expected 1 DeletedEntity, got %d", len(cl.DeletedEntities))
	}
	if cl.DeletedEntities["id-1"].Type != testComponentType {
		t.Fatalf("expected DeletedEntities type Component, got %q", cl.DeletedEntities["id-1"].Type)
	}
	if cl.DeletedEntities["id-1"].Suspected {
		t.Fatal("expected DeletedEntities Suspected=false")
	}
}

func TestChangeLogAddEdge(t *testing.T) {
	cl := NewChangeLog()
	if err := cl.Add(ChangeLogEntry{
		Kind: ChangeAddEdge,
		ID:   "edge-1",
		Type: "DEPENDS_ON",
		Edge: &EdgeEntry{
			ID:           "edge-1",
			Type:         "DEPENDS_ON",
			FromEntityID: "from-id",
			ToEntityID:   "to-id",
			Properties:   map[string]string{"weight": "high"},
			CreatedAt:    time.Now(),
			UpdatedAt:    time.Now(),
		},
	}); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if cl.Len() != 1 {
		t.Fatalf("expected Len()=1, got %d", cl.Len())
	}
	if len(cl.AddedEdges) != 1 {
		t.Fatalf("expected 1 AddedEdge, got %d", len(cl.AddedEdges))
	}
	if cl.AddedEdges["edge-1"].Type != "DEPENDS_ON" {
		t.Fatalf("expected type DEPENDS_ON, got %q", cl.AddedEdges["edge-1"].Type)
	}
	if cl.AddedEdges["edge-1"].FromEntityID != "from-id" {
		t.Fatalf("expected FromEntityID=from-id, got %q", cl.AddedEdges["edge-1"].FromEntityID)
	}
	if cl.AddedEdges["edge-1"].ToEntityID != "to-id" {
		t.Fatalf("expected ToEntityID=to-id, got %q", cl.AddedEdges["edge-1"].ToEntityID)
	}
}

func TestChangeLogDeleteEdge(t *testing.T) {
	cl := NewChangeLog()
	if err := cl.Add(ChangeLogEntry{
		Kind:      ChangeDelEdge,
		ID:        "edge-1",
		Type:      "DEPENDS_ON",
		Suspected: false,
	}); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if cl.Len() != 1 {
		t.Fatalf("expected Len()=1, got %d", cl.Len())
	}
	if len(cl.DeletedEdges) != 1 {
		t.Fatalf("expected 1 DeletedEdge, got %d", len(cl.DeletedEdges))
	}
}

func TestChangeLogMixed(t *testing.T) {
	cl := NewChangeLog()
	if err := cl.Add(ChangeLogEntry{
		Kind: ChangeAddEntity, ID: "e1", Type: testComponentType,
		Entity: &EntityEntry{ID: "e1", Type: testComponentType, CreatedAt: time.Now(), UpdatedAt: time.Now()},
	}); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if err := cl.Add(ChangeLogEntry{
		Kind: ChangeModEntity, ID: "e2", Type: testComponentType,
		Entity: &EntityEntry{
			ID: "e2", Type: testComponentType,
			Properties: map[string]string{"name": "x"},
			UpdatedAt:  time.Now(),
		},
	}); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if err := cl.Add(ChangeLogEntry{
		Kind: ChangeDelEntity, ID: "e3", Type: testComponentType,
	}); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if err := cl.Add(ChangeLogEntry{
		Kind: ChangeAddEdge, ID: "e4", Type: "DEPENDS_ON",
		Edge: &EdgeEntry{
			ID: "e4", Type: "DEPENDS_ON",
			FromEntityID: "a", ToEntityID: "b",
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		},
	}); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if err := cl.Add(ChangeLogEntry{
		Kind: ChangeDelEdge, ID: "e5", Type: "DEPENDS_ON",
	}); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	expected := 5
	if cl.Len() != expected {
		t.Fatalf("expected Len()=%d, got %d", expected, cl.Len())
	}
	if len(cl.AddedEntities) != 1 {
		t.Fatalf("expected 1 AddedEntity, got %d", len(cl.AddedEntities))
	}
	if len(cl.ModifiedEntities) != 1 {
		t.Fatalf("expected 1 ModifiedEntity, got %d", len(cl.ModifiedEntities))
	}
	if len(cl.DeletedEntities) != 1 {
		t.Fatalf("expected 1 DeletedEntity, got %d", len(cl.DeletedEntities))
	}
	if len(cl.AddedEdges) != 1 {
		t.Fatalf("expected 1 AddedEdge, got %d", len(cl.AddedEdges))
	}
	if len(cl.DeletedEdges) != 1 {
		t.Fatalf("expected 1 DeletedEdge, got %d", len(cl.DeletedEdges))
	}

	entries := cl.Entries()
	if len(entries) != expected {
		t.Fatalf("expected %d entries, got %d", expected, len(entries))
	}
}
