package flow

import (
	"context"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// ---------------------------------------------------------------------------
// ID-to-type map unit tests
// ---------------------------------------------------------------------------

func TestIDTypeMap_StoreAndResolve(t *testing.T) {
	m := newIDTypeMap()
	m.store("id-1", "Component")
	typ, ok := m.resolve("id-1")
	if !ok || typ != componentType {
		t.Errorf("expected Component, got %q (ok=%v)", typ, ok)
	}
}

func TestIDTypeMap_Remove(t *testing.T) {
	m := newIDTypeMap()
	m.store("id-1", "Component")
	m.remove("id-1")
	_, ok := m.resolve("id-1")
	if ok {
		t.Error("expected id-1 to be removed")
	}
}

// TestIDTypeMap_EvictsOldestAtCapacity verifies the size bound (SPEC R3:
// "bounded local cache"): once the map holds maxSize IDs, storing a new ID
// evicts the oldest entry.
func TestIDTypeMap_EvictsOldestAtCapacity(t *testing.T) {
	m := newIDTypeMap()
	m.maxSize = 3
	m.store("id-1", "Component")
	m.store("id-2", "Service")
	m.store("id-3", "Component")
	m.store("id-4", "Service")
	// id-1 was inserted first and must have been evicted at capacity.
	if _, ok := m.resolve("id-1"); ok {
		t.Error("expected id-1 (oldest) to be evicted at capacity")
	}
	for _, id := range []string{"id-2", "id-3", "id-4"} {
		if _, ok := m.resolve(id); !ok {
			t.Errorf("expected %s to remain in the map", id)
		}
	}
}

// TestIDTypeMap_TTLExpiry verifies the TTL bound (SPEC R3: "TTL-bounded"):
// an entry older than the TTL no longer resolves (lazy expiry via resolve),
// while re-storing an ID refreshes its TTL.
func TestIDTypeMap_TTLExpiry(t *testing.T) {
	m := newIDTypeMap()
	m.ttl = 5 * time.Millisecond
	m.store("id-1", "Component")
	m.store("id-2", "Service")
	time.Sleep(20 * time.Millisecond)
	for _, id := range []string{"id-1", "id-2"} {
		if _, ok := m.resolve(id); ok {
			t.Errorf("expected %s to expire after TTL", id)
		}
	}
	// Re-storing refreshes the TTL.
	m.store("id-1", componentType)
	typ, ok := m.resolve("id-1")
	if !ok || typ != componentType {
		t.Errorf("expected id-1 to resolve again after re-store, got %q (ok=%v)", typ, ok)
	}
}

func TestIDTypeMap_ResolveUnknown(t *testing.T) {
	m := newIDTypeMap()
	_, ok := m.resolve("unknown")
	if ok {
		t.Error("expected unknown id to not be found")
	}
}

// TestDeleteEntity_RemovesFromMap pins tx.DeleteEntity evicting the deleted
// ID from the local ID-to-type map: a deleted entity must not keep resolving
// to a concrete type on a later capability annotation.
func TestDeleteEntity_RemovesFromMap(t *testing.T) {
	mock := &mockCartographerClient{
		deleteEntity: func(ctx context.Context, req *flowv1.DeleteEntityRequest) (*flowv1.DeleteEntityResponse, error) {
			return &flowv1.DeleteEntityResponse{EntityId: req.GetId()}, nil
		},
	}
	tx := newMockTx(mock)
	tx.idTypeMap.store(testUUIDEntity, "Component")
	_, err := tx.DeleteEntity(testUUIDEntity)
	if err != nil {
		t.Fatalf("DeleteEntity returned error: %v", err)
	}
	_, ok := tx.idTypeMap.resolve(testUUIDEntity)
	if ok {
		t.Error("expected e1 to be removed from map")
	}
}

// ---------------------------------------------------------------------------
// resolveOrWildcard tests
// ---------------------------------------------------------------------------

func TestIDTypeMap_ResolveOrWildcard_Found(t *testing.T) {
	m := newIDTypeMap()
	m.store("id-1", componentType)
	typ := m.resolveOrWildcard("id-1")
	if typ != componentType {
		t.Errorf("expected Component, got %q", typ)
	}
}

func TestIDTypeMap_ResolveOrWildcard_NotFound(t *testing.T) {
	m := newIDTypeMap()
	typ := m.resolveOrWildcard("unknown")
	if typ != "*" {
		t.Errorf("expected wildcard *, got %q", typ)
	}
}

// TestIDTypeMap_EmptyTypeNotStored pins resolveOrWildcard's documented
// guarantee: capability annotation falls back to the wildcard rather than
// annotating with an empty type (which fails resolution). An empty
// entity_type from a server response must not be stored, so a stored ID with
// an empty type resolves as unknown and produces "*" — never entity_type="".
func TestIDTypeMap_EmptyTypeNotStored(t *testing.T) {
	m := newIDTypeMap()
	m.store("id-1", "")
	if typ := m.resolveOrWildcard("id-1"); typ != "*" {
		t.Errorf("expected wildcard * for empty stored type, got %q", typ)
	}
	if _, ok := m.resolve("id-1"); ok {
		t.Error("expected empty-type entry not to be stored")
	}
}
