package gitstore

import (
	"sync"
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

func TestChangeLogFullCapEnforced(t *testing.T) {
	cl := NewChangeLogWithCap(10)

	// Add 10 entries (the small cap)
	for i := range 10 {
		if err := cl.Add(ChangeLogEntry{
			Kind:   ChangeAddEntity,
			ID:     formatIntID(i),
			Type:   testComponentType,
			Entity: &EntityEntry{ID: formatIntID(i), Type: testComponentType},
		}); err != nil {
			t.Fatalf("Add %d failed: %v", i, err)
		}
	}
	if cl.Len() != 10 {
		t.Fatalf("expected Len()=10, got %d", cl.Len())
	}

	// One more should fail
	if err := cl.Add(ChangeLogEntry{
		Kind:   ChangeAddEntity,
		ID:     "overflow",
		Type:   testComponentType,
		Entity: &EntityEntry{ID: "overflow", Type: testComponentType},
	}); err != ErrChangeLogFull {
		t.Fatalf("expected ErrChangeLogFull, got %v", err)
	}
	if cl.Len() != 10 {
		t.Fatalf("expected Len() still 10, got %d", cl.Len())
	}

	// Verify each typed method enforces the cap
	cl2 := NewChangeLogWithCap(10)
	for i := range 10 {
		_ = cl2.Add(ChangeLogEntry{
			Kind:   ChangeAddEntity,
			ID:     formatIntID(i),
			Type:   testComponentType,
			Entity: &EntityEntry{ID: formatIntID(i), Type: testComponentType},
		})
	}
	if err := cl2.Add(ChangeLogEntry{
		Kind:   ChangeAddEntity,
		ID:     "x",
		Type:   testComponentType,
		Entity: &EntityEntry{ID: "x", Type: testComponentType},
	}); err != ErrChangeLogFull {
		t.Fatalf("AddEntity cap: expected ErrChangeLogFull, got %v", err)
	}
	if err := cl2.Add(ChangeLogEntry{
		Kind:   ChangeModEntity,
		ID:     "x",
		Type:   testComponentType,
		Entity: &EntityEntry{ID: "x", Type: testComponentType},
	}); err != ErrChangeLogFull {
		t.Fatalf("ModifyEntity cap: expected ErrChangeLogFull, got %v", err)
	}
	if err := cl2.Add(ChangeLogEntry{
		Kind: ChangeDelEntity, ID: "x", Type: testComponentType,
	}); err != ErrChangeLogFull {
		t.Fatalf("DeleteEntity cap: expected ErrChangeLogFull, got %v", err)
	}
	if err := cl2.Add(ChangeLogEntry{
		Kind: ChangeAddEdge, ID: "x", Type: "DEPENDS_ON",
		Edge: &EdgeEntry{ID: "x", Type: "DEPENDS_ON", FromEntityID: "a", ToEntityID: "b"},
	}); err != ErrChangeLogFull {
		t.Fatalf("AddEdge cap: expected ErrChangeLogFull, got %v", err)
	}
	if err := cl2.Add(ChangeLogEntry{
		Kind: ChangeDelEdge, ID: "x", Type: "DEPENDS_ON",
	}); err != ErrChangeLogFull {
		t.Fatalf("DeleteEdge cap: expected ErrChangeLogFull, got %v", err)
	}

	// Generic Add method
	cl3 := NewChangeLogWithCap(10)
	for i := range 10 {
		_ = cl3.Add(ChangeLogEntry{
			Kind: ChangeAddEntity,
			ID:   formatIntID(i),
			Type: testComponentType,
		})
	}
	if err := cl3.Add(ChangeLogEntry{Kind: ChangeAddEntity, ID: "x", Type: testComponentType}); err != ErrChangeLogFull {
		t.Fatalf("Add cap: expected ErrChangeLogFull, got %v", err)
	}
}

func TestChangeLogClear(t *testing.T) {
	cl := NewChangeLog()
	_ = cl.Add(ChangeLogEntry{
		Kind: ChangeAddEntity, ID: "e1", Type: testComponentType,
		Entity: &EntityEntry{ID: "e1", Type: testComponentType},
	})
	_ = cl.Add(ChangeLogEntry{
		Kind: ChangeAddEntity, ID: "e2", Type: testComponentType,
		Entity: &EntityEntry{ID: "e2", Type: testComponentType},
	})
	_ = cl.Add(ChangeLogEntry{
		Kind: ChangeAddEdge, ID: "e3", Type: "DEPENDS_ON",
		Edge: &EdgeEntry{ID: "e3", Type: "DEPENDS_ON", FromEntityID: "a", ToEntityID: "b"},
	})

	if cl.Len() != 3 {
		t.Fatalf("expected Len()=3, got %d", cl.Len())
	}

	cl.Clear()
	if cl.Len() != 0 {
		t.Fatalf("expected Len()=0 after Clear, got %d", cl.Len())
	}
	if len(cl.AddedEntities) != 0 {
		t.Fatal("expected empty AddedEntities after Clear")
	}
	if len(cl.ModifiedEntities) != 0 {
		t.Fatal("expected empty ModifiedEntities after Clear")
	}
	if len(cl.DeletedEntities) != 0 {
		t.Fatal("expected empty DeletedEntities after Clear")
	}
	if len(cl.AddedEdges) != 0 {
		t.Fatal("expected empty AddedEdges after Clear")
	}
	if len(cl.DeletedEdges) != 0 {
		t.Fatal("expected empty DeletedEdges after Clear")
	}
}

func TestChangeLogConcurrent(t *testing.T) {
	cl := NewChangeLog()
	var wg sync.WaitGroup
	errCh := make(chan error, 100)

	// Concurrently add entries
	for i := range 100 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := formatIntID(n)
			if err := cl.Add(ChangeLogEntry{
				Kind:   ChangeAddEntity,
				ID:     id,
				Type:   testComponentType,
				Entity: &EntityEntry{ID: id, Type: testComponentType},
			}); err != nil {
				errCh <- err
			}
		}(i)
	}

	// Concurrently read entries
	for range 10 {
		wg.Go(func() {
			_ = cl.Entries()
			_ = cl.Len()
		})
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("Add failed: %v", err)
	}
}

func TestChangeLogGenericAdd(t *testing.T) {
	cl := NewChangeLog()

	// Add via generic Add method
	entry := ChangeLogEntry{
		Kind: ChangeAddEntity,
		ID:   "e1",
		Type: testComponentType,
		Entity: &EntityEntry{
			ID:         "e1",
			Type:       testComponentType,
			Properties: map[string]string{"name": "test"},
		},
	}
	if err := cl.Add(entry); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if cl.Len() != 1 {
		t.Fatalf("expected Len()=1, got %d", cl.Len())
	}

	// Unknown kind
	err := cl.Add(ChangeLogEntry{Kind: ChangeKind(999), ID: "x"})
	if err != ErrUnknownChangeKind {
		t.Fatalf("expected ErrUnknownChangeKind, got %v", err)
	}
}

func TestChangeLogAddPreservesCallerTimestamps(t *testing.T) {
	cl := NewChangeLog()
	created := time.Unix(100, 0).UTC()
	updated := time.Unix(200, 0).UTC()
	if err := cl.Add(ChangeLogEntry{
		Kind: ChangeAddEntity,
		ID:   "e1",
		Type: testComponentType,
		Entity: &EntityEntry{
			ID: "e1", Type: testComponentType, CreatedAt: created, UpdatedAt: updated,
		},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	entries := cl.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected one entry, got %d", len(entries))
	}
	if !entries[0].Entity.CreatedAt.Equal(created) || !entries[0].Entity.UpdatedAt.Equal(updated) {
		t.Fatalf("timestamps changed: %+v", entries[0].Entity)
	}
}

// formatIntID formats an int as a padded string ID for test use.
func formatIntID(n int) string {
	s := ""
	for n >= 0 {
		s = string(rune('a'+n%26)) + s
		n = n/26 - 1
		if n < 0 {
			break
		}
	}
	return s
}
