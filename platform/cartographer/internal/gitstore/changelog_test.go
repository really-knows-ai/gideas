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
			Kind:   ChangeAddEntity,
			ID:     formatIntID(i),
			Type:   testComponentType,
			Entity: &EntityEntry{ID: formatIntID(i), Type: testComponentType},
		})
	}
	if err := cl3.Add(ChangeLogEntry{
		Kind: ChangeAddEntity, ID: "x", Type: testComponentType,
		Entity: &EntityEntry{ID: "x", Type: testComponentType},
	}); err != ErrChangeLogFull {
		t.Fatalf("Add cap: expected ErrChangeLogFull, got %v", err)
	}
}

// TestChangeLogAtCapSameIDUpdateSucceeds pins the SPEC admission predicate
// (SPEC:891-892): only a mutation that would grow the log past its cap is
// rejected. At cap, a mutation on an already-logged element is admitted —
// it reuses the element's slot. The entry-aware preflight (CheckCapacity)
// mirrors the same predicate.
func TestChangeLogAtCapSameIDUpdateSucceeds(t *testing.T) {
	cl := NewChangeLogWithCap(2)

	// Fill the log to its cap with two distinct entities.
	for _, id := range []string{"e1", "e2"} {
		if err := cl.Add(ChangeLogEntry{
			Kind: ChangeAddEntity, ID: id, Type: testComponentType,
			Entity: &EntityEntry{ID: id, Type: testComponentType},
		}); err != nil {
			t.Fatalf("Add %s failed: %v", id, err)
		}
	}
	if cl.Len() != 2 {
		t.Fatalf("expected Len()=2, got %d", cl.Len())
	}

	// At cap, the preflight admits a same-ID update (it does not grow the log),
	// and Add records it.
	if err := cl.CheckCapacity(ChangeLogEntry{Kind: ChangeModEntity, ID: "e1", Type: testComponentType}); err != nil {
		t.Fatalf("CheckCapacity for same-ID update at cap: %v", err)
	}
	if err := cl.Add(ChangeLogEntry{
		Kind: ChangeModEntity, ID: "e1", Type: testComponentType,
		Entity: &EntityEntry{ID: "e1", Type: testComponentType, Properties: map[string]string{"name": "updated"}},
	}); err != nil {
		t.Fatalf("same-ID update at cap failed: %v", err)
	}
	if cl.Len() != 2 {
		t.Fatalf("expected Len()=2 after slot-reuse update, got %d", cl.Len())
	}
	if _, ok := cl.ModifiedEntities["e1"]; !ok {
		t.Fatal("expected the same-ID update to be recorded in ModifiedEntities")
	}

	// A new-ID insert at cap is rejected by both the preflight and Add.
	if err := cl.CheckCapacity(ChangeLogEntry{
		Kind: ChangeAddEntity, ID: "e3", Type: testComponentType,
	}); err != ErrChangeLogFull {
		t.Fatalf("CheckCapacity for new-ID insert at cap: expected ErrChangeLogFull, got %v", err)
	}
	if err := cl.Add(ChangeLogEntry{
		Kind: ChangeAddEntity, ID: "e3", Type: testComponentType,
		Entity: &EntityEntry{ID: "e3", Type: testComponentType},
	}); err != ErrChangeLogFull {
		t.Fatalf("new-ID insert at cap: expected ErrChangeLogFull, got %v", err)
	}

	// An unknown (auto-generated) ID is treated as a new element — a fresh
	// UUID can never reuse a logged slot.
	if err := cl.CheckCapacity(ChangeLogEntry{Kind: ChangeAddEdge, ID: "", Type: "DEPENDS_ON"}); err != ErrChangeLogFull {
		t.Fatalf("CheckCapacity with unknown ID at cap: expected ErrChangeLogFull, got %v", err)
	}
}

// TestChangeLogCountsDistinctElements pins the SPEC error-table row
// "Transaction change log exceeds capacity" (SPEC:968), whose trigger is a
// transaction that "modified more than 100 000 entities/edges": the cap counts
// distinct elements, so add-then-modify of the same entity/edge records two
// mutation entries but counts as one element.
func TestChangeLogCountsDistinctElements(t *testing.T) {
	cl := NewChangeLogWithCap(4)

	addEntity := func(id string) {
		t.Helper()
		if err := cl.Add(ChangeLogEntry{
			Kind: ChangeAddEntity, ID: id, Type: testComponentType,
			Entity: &EntityEntry{ID: id, Type: testComponentType},
		}); err != nil {
			t.Fatalf("Add entity %s failed: %v", id, err)
		}
	}
	modEntity := func(id string) {
		t.Helper()
		if err := cl.Add(ChangeLogEntry{
			Kind: ChangeModEntity, ID: id, Type: testComponentType,
			Entity: &EntityEntry{ID: id, Type: testComponentType, Properties: map[string]string{"name": "x"}},
		}); err != nil {
			t.Fatalf("Mod entity %s failed: %v", id, err)
		}
	}
	addEdge := func(id string) {
		t.Helper()
		if err := cl.Add(ChangeLogEntry{
			Kind: ChangeAddEdge, ID: id, Type: "DEPENDS_ON",
			Edge: &EdgeEntry{ID: id, Type: "DEPENDS_ON", FromEntityID: "a", ToEntityID: "b"},
		}); err != nil {
			t.Fatalf("Add edge %s failed: %v", id, err)
		}
	}
	modEdge := func(id string) {
		t.Helper()
		if err := cl.Add(ChangeLogEntry{
			Kind: ChangeModEdge, ID: id, Type: "DEPENDS_ON",
			Edge: &EdgeEntry{ID: id, Type: "DEPENDS_ON", FromEntityID: "a", ToEntityID: "b"},
		}); err != nil {
			t.Fatalf("Mod edge %s failed: %v", id, err)
		}
	}

	// e1 added-then-modified: two mutation entries, one distinct element.
	addEntity("e1")
	modEntity("e1")
	if cl.Len() != 1 {
		t.Fatalf("expected Len()=1 after add+mod of one entity, got %d", cl.Len())
	}
	// A second entity and an edge are each distinct elements.
	addEntity("e2")
	if cl.Len() != 2 {
		t.Fatalf("expected Len()=2 after two distinct entities, got %d", cl.Len())
	}
	addEdge("edge-1")
	if cl.Len() != 3 {
		t.Fatalf("expected Len()=3 after adding an edge, got %d", cl.Len())
	}
	// Modifying the same edge still counts once.
	modEdge("edge-1")
	if cl.Len() != 3 {
		t.Fatalf("expected Len()=3 after add+mod of one edge, got %d", cl.Len())
	}
	// The fourth distinct element reaches the cap.
	addEntity("e3")
	if cl.Len() != 4 {
		t.Fatalf("expected Len()=4, got %d", cl.Len())
	}
	// A fifth distinct element is rejected — the cap counts distinct elements,
	// so 60K added-then-modified entities fit (120K entries would not).
	if err := cl.Add(ChangeLogEntry{
		Kind: ChangeAddEntity, ID: "e4", Type: testComponentType,
		Entity: &EntityEntry{ID: "e4", Type: testComponentType},
	}); err != ErrChangeLogFull {
		t.Fatalf("5th distinct element: expected ErrChangeLogFull, got %v", err)
	}
	// All six admitted mutations are still recorded as entries.
	if entries := cl.Entries(); len(entries) != 6 {
		t.Fatalf("expected 6 logged mutation entries, got %d", len(entries))
	}
	// An at-cap slot-reuse (re-modifying a tracked element) is still admitted.
	modEntity("e1")
	if cl.Len() != 4 {
		t.Fatalf("expected Len()=4 after at-cap slot reuse, got %d", cl.Len())
	}
}

// TestChangeLogAddEntryBypassesCap exercises the startup-recovery reconstruct
// path (RecoverOpenTransactions): AddEntry must admit entries beyond the cap
// because the original addition was already gated when the transaction ran.
func TestChangeLogAddEntryBypassesCap(t *testing.T) {
	cl := NewChangeLogWithCap(10)

	// Fill the ChangeLog to its cap via the cap-enforcing Add path.
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

	// The 11th entry would be rejected by Add once the cap is reached.
	if err := cl.Add(ChangeLogEntry{
		Kind:   ChangeAddEntity,
		ID:     "overflow",
		Type:   testComponentType,
		Entity: &EntityEntry{ID: "overflow", Type: testComponentType},
	}); err != ErrChangeLogFull {
		t.Fatalf("Add past cap: expected ErrChangeLogFull, got %v", err)
	}

	// Recovery reconstruction bypasses the cap: AddEntry admits the overflow entry.
	if err := cl.AddEntry(ChangeLogEntry{
		Kind:   ChangeAddEntity,
		ID:     "recovered",
		Type:   testComponentType,
		Entity: &EntityEntry{ID: "recovered", Type: testComponentType},
	}); err != nil {
		t.Fatalf("AddEntry past cap should bypass the cap, got: %v", err)
	}
	if cl.Len() != 11 {
		t.Fatalf("expected Len()=11 after AddEntry, got %d", cl.Len())
	}
	if _, ok := cl.AddedEntities["recovered"]; !ok {
		t.Fatal("expected the recovered entry to be admitted into AddedEntities")
	}

	// The cap remains enforced for the normal Add path after a cap-bypassing AddEntry.
	if err := cl.Add(ChangeLogEntry{
		Kind:   ChangeAddEntity,
		ID:     "overflow2",
		Type:   testComponentType,
		Entity: &EntityEntry{ID: "overflow2", Type: testComponentType},
	}); err != ErrChangeLogFull {
		t.Fatalf("Add after AddEntry: expected ErrChangeLogFull, got %v", err)
	}
}

// TestChangeLogRejectsNilSnapshot pins the ChangeLog invariant that add/modify
// kinds carry the snapshot Entries() dereferences on read-back: a nil Entity
// snapshot for ChangeAddEntity/ChangeModEntity or a nil Edge snapshot for
// ChangeAddEdge/ChangeModEdge must be rejected at the public boundary (both
// Add and the cap-bypassing AddEntry share the add guard), never stored to
// become a latent nil-pointer dereference in Entries(). Deletion kinds store
// DeletionInfo and need no snapshot.
func TestChangeLogRejectsNilSnapshot(t *testing.T) {
	nilSnapshotCases := []struct {
		kind    ChangeKind
		missing string
	}{
		{ChangeAddEntity, "Entity"},
		{ChangeModEntity, "Entity"},
		{ChangeAddEdge, "Edge"},
		{ChangeModEdge, "Edge"},
	}
	for _, tc := range nilSnapshotCases {
		for _, method := range []string{"Add", "AddEntry"} {
			cl := NewChangeLog()
			entry := ChangeLogEntry{Kind: tc.kind, ID: "id-1", Type: testComponentType}
			var err error
			if method == "Add" {
				err = cl.Add(entry)
			} else {
				err = cl.AddEntry(entry)
			}
			if err != ErrChangeLogNilSnapshot {
				t.Fatalf("%s(%v) with nil %s: expected ErrChangeLogNilSnapshot, got %v", method, tc.kind, tc.missing, err)
			}
			if cl.Len() != 0 {
				t.Fatalf("%s(%v) with nil %s: expected nothing stored, got Len()=%d", method, tc.kind, tc.missing, cl.Len())
			}
		}
	}

	// Valid add/modify entries with snapshots, and snapshot-less deletions,
	// still succeed through both public paths.
	cl := NewChangeLog()
	if err := cl.Add(ChangeLogEntry{
		Kind: ChangeAddEntity, ID: "e1", Type: testComponentType,
		Entity: &EntityEntry{ID: "e1", Type: testComponentType},
	}); err != nil {
		t.Fatalf("Add valid entity: %v", err)
	}
	if err := cl.AddEntry(ChangeLogEntry{
		Kind: ChangeAddEdge, ID: "edge-1", Type: "DEPENDS_ON",
		Edge: &EdgeEntry{ID: "edge-1", Type: "DEPENDS_ON", FromEntityID: "a", ToEntityID: "b"},
	}); err != nil {
		t.Fatalf("AddEntry valid edge: %v", err)
	}
	if err := cl.Add(ChangeLogEntry{Kind: ChangeDelEntity, ID: "e2", Type: testComponentType}); err != nil {
		t.Fatalf("Add deletion: %v", err)
	}
	if err := cl.AddEntry(ChangeLogEntry{Kind: ChangeDelEdge, ID: "edge-2", Type: "DEPENDS_ON"}); err != nil {
		t.Fatalf("AddEntry deletion: %v", err)
	}
	if entries := cl.Entries(); len(entries) != 4 {
		t.Fatalf("expected 4 valid entries, got %d", len(entries))
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
