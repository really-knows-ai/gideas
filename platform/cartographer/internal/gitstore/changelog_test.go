package gitstore

import (
	"sync"
	"testing"
)

func TestChangeLogAddEntity(t *testing.T) {
	cl := NewChangeLog()
	if err := cl.AddEntity("id-1", "Component", map[string]string{"name": "x"}, nil); err != nil {
		t.Fatalf("AddEntity failed: %v", err)
	}
	if cl.Len() != 1 {
		t.Fatalf("expected Len()=1, got %d", cl.Len())
	}
	if len(cl.AddedEntities) != 1 {
		t.Fatalf("expected 1 AddedEntity, got %d", len(cl.AddedEntities))
	}
	if cl.AddedEntities["id-1"].Type != "Component" {
		t.Fatalf("expected type Component, got %q", cl.AddedEntities["id-1"].Type)
	}
}

func TestChangeLogModifyEntity(t *testing.T) {
	cl := NewChangeLog()
	if err := cl.ModifyEntity("id-1", "Component", map[string]string{"name": "updated"}, nil); err != nil {
		t.Fatalf("ModifyEntity failed: %v", err)
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
	if err := cl.DeleteEntity("id-1", "Component"); err != nil {
		t.Fatalf("DeleteEntity failed: %v", err)
	}
	if cl.Len() != 1 {
		t.Fatalf("expected Len()=1, got %d", cl.Len())
	}
	if len(cl.DeletedEntities) != 1 {
		t.Fatalf("expected 1 DeletedEntity, got %d", len(cl.DeletedEntities))
	}
	if cl.DeletedEntities["id-1"].Type != "Component" {
		t.Fatalf("expected DeletedEntities type Component, got %q", cl.DeletedEntities["id-1"].Type)
	}
	if cl.DeletedEntities["id-1"].Suspected {
		t.Fatal("expected DeletedEntities Suspected=false")
	}
}

func TestChangeLogAddEdge(t *testing.T) {
	cl := NewChangeLog()
	if err := cl.AddEdge("edge-1", "DEPENDS_ON", "from-id", "to-id", map[string]string{"weight": "high"}); err != nil {
		t.Fatalf("AddEdge failed: %v", err)
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
	if err := cl.DeleteEdge("edge-1", "DEPENDS_ON"); err != nil {
		t.Fatalf("DeleteEdge failed: %v", err)
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
	if err := cl.AddEntity("e1", "Component", nil, nil); err != nil {
		t.Fatalf("AddEntity failed: %v", err)
	}
	if err := cl.ModifyEntity("e2", "Component", map[string]string{"name": "x"}, nil); err != nil {
		t.Fatalf("ModifyEntity failed: %v", err)
	}
	if err := cl.DeleteEntity("e3", "Component"); err != nil {
		t.Fatalf("DeleteEntity failed: %v", err)
	}
	if err := cl.AddEdge("e4", "DEPENDS_ON", "a", "b", nil); err != nil {
		t.Fatalf("AddEdge failed: %v", err)
	}
	if err := cl.DeleteEdge("e5", "DEPENDS_ON"); err != nil {
		t.Fatalf("DeleteEdge failed: %v", err)
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
	cl := NewChangeLog()

	// Add 100K entries (the cap)
	for i := range 100000 {
		if err := cl.AddEntity(formatIntID(i), "Component", nil, nil); err != nil {
			t.Fatalf("AddEntity %d failed: %v", i, err)
		}
	}
	if cl.Len() != 100000 {
		t.Fatalf("expected Len()=100000, got %d", cl.Len())
	}

	// One more should fail
	if err := cl.AddEntity("overflow", "Component", nil, nil); err != ErrChangeLogFull {
		t.Fatalf("expected ErrChangeLogFull, got %v", err)
	}
	if cl.Len() != 100000 {
		t.Fatalf("expected Len() still 100000, got %d", cl.Len())
	}

	// Verify each typed method enforces the cap
	cl2 := NewChangeLog()
	for i := range 100000 {
		_ = cl2.AddEntity(formatIntID(i), "Component", nil, nil)
	}
	if err := cl2.AddEntity("x", "Component", nil, nil); err != ErrChangeLogFull {
		t.Fatalf("AddEntity cap: expected ErrChangeLogFull, got %v", err)
	}
	if err := cl2.ModifyEntity("x", "Component", nil, nil); err != ErrChangeLogFull {
		t.Fatalf("ModifyEntity cap: expected ErrChangeLogFull, got %v", err)
	}
	if err := cl2.DeleteEntity("x", "Component"); err != ErrChangeLogFull {
		t.Fatalf("DeleteEntity cap: expected ErrChangeLogFull, got %v", err)
	}
	if err := cl2.AddEdge("x", "DEPENDS_ON", "a", "b", nil); err != ErrChangeLogFull {
		t.Fatalf("AddEdge cap: expected ErrChangeLogFull, got %v", err)
	}
	if err := cl2.DeleteEdge("x", "DEPENDS_ON"); err != ErrChangeLogFull {
		t.Fatalf("DeleteEdge cap: expected ErrChangeLogFull, got %v", err)
	}

	// Generic Add method
	cl3 := NewChangeLog()
	for i := range 100000 {
		_ = cl3.Add(ChangeLogEntry{
			Kind: ChangeAddEntity,
			ID:   formatIntID(i),
			Type: "Component",
		})
	}
	if err := cl3.Add(ChangeLogEntry{Kind: ChangeAddEntity, ID: "x", Type: "Component"}); err != ErrChangeLogFull {
		t.Fatalf("Add cap: expected ErrChangeLogFull, got %v", err)
	}
}

func TestChangeLogClear(t *testing.T) {
	cl := NewChangeLog()
	_ = cl.AddEntity("e1", "Component", nil, nil)
	_ = cl.AddEntity("e2", "Component", nil, nil)
	_ = cl.AddEdge("e3", "DEPENDS_ON", "a", "b", nil)

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

	// Concurrently add entries
	for i := range 100 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := formatIntID(n)
			if err := cl.AddEntity(id, "Component", nil, nil); err != nil {
				t.Errorf("AddEntity failed: %v", err)
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
}

func TestChangeLogGenericAdd(t *testing.T) {
	cl := NewChangeLog()

	// Add via generic Add method
	entry := ChangeLogEntry{
		Kind: ChangeAddEntity,
		ID:   "e1",
		Type: "Component",
		Entity: &EntityEntry{
			ID:         "e1",
			Type:       "Component",
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

// formatIntID formats an int as a padded string ID for test use.
func formatIntID(n int) string {
	buf := make([]byte, 36)
	for i := range buf {
		buf[i] = '0'
	}
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
