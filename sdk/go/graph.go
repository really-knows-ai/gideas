package flow

import (
	"fmt"
	"math"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Domain types
// ---------------------------------------------------------------------------

// Entity represents a graph entity with its ID, type, properties, and embedding.
type Entity struct {
	ID         string
	Type       string
	Properties map[string]string
	Embedding  []float32
	Suspected  bool
}

// Edge represents a directed edge between two entities.
type Edge struct {
	ID           string
	Type         string
	FromEntityID string
	ToEntityID   string
	Properties   map[string]string
	Suspected    bool
}

// SearchResult is a single result from a vector similarity search.
// Identity is exposed as typed fields; properties are carried losslessly.
// Distance is the raw cosine distance to the query embedding — LOWER is more
// similar, and results are ordered ascending by distance. It is a distance,
// not a similarity score: consumers must not sort by Distance descending
// expecting higher-is-better.
type SearchResult struct {
	ID         string
	Type       string
	Properties map[string]string
	Distance   float64
}

// EntityPage is a page of entities returned by ListEntities.
type EntityPage struct {
	Entities      []Entity
	NextPageToken string
}

// ---------------------------------------------------------------------------
// ID-to-type mapping
// ---------------------------------------------------------------------------

// idTypeMapMaxSize and idTypeMapTTL bound the SDK's local ID-to-type cache
// (SPEC R3: "bounded local cache (TTL-bounded)"). The SPEC prescribes the
// bounds but not their values; these pick a generous ceiling for a
// best-effort capability cache.
const (
	idTypeMapMaxSize = 1000
	idTypeMapTTL     = 10 * time.Minute
)

// idTypeMap is a thread-safe, size- and TTL-bounded cache of entity ID to
// entity type (SPEC R3: "bounded local cache (TTL-bounded)"). Entries are
// treated as unknown once they are older than idTypeMapTTL, and once the map
// holds idTypeMapMaxSize IDs a new ID evicts the oldest entry.
//
// ponytail: Eviction is a linear scan for the oldest entry on an
// over-capacity store, and expired entries are only physically removed when
// such an eviction scan runs (they are excluded from resolve/snapshot
// earlier). Both are O(n) in the map size, which is bounded by
// idTypeMapMaxSize (1000), so the cost is acceptable for a best-effort cache
// populated from the SDK's own traffic. Upgrade path: a container/heap keyed
// by insertion time if the cache grows.
type idTypeMap struct {
	mu      sync.RWMutex
	entries map[string]idTypeEntry
	ttl     time.Duration
	maxSize int
}

// idTypeEntry is a single cached mapping with its insertion time for TTL
// eviction.
type idTypeEntry struct {
	entityType string
	insertedAt time.Time
}

func newIDTypeMap() *idTypeMap {
	return &idTypeMap{
		entries: make(map[string]idTypeEntry),
		ttl:     idTypeMapTTL,
		maxSize: idTypeMapMaxSize,
	}
}

// store records the ID→type mapping, stamping the entry with the current
// time for TTL tracking. An empty id or entityType is rejected: an entry
// with an empty type would resolve as ("", true) and annotate
// entity_type="" capability metadata instead of the wildcard (see
// resolveOrWildcard), which fails resolution.
func (m *idTypeMap) store(id, entityType string) {
	if id == "" || entityType == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[id]; !ok && len(m.entries) >= m.maxSize {
		m.evictOldestLocked()
	}
	// Re-storing an existing ID refreshes its TTL: the entity was seen again
	// in the SDK's own traffic.
	m.entries[id] = idTypeEntry{entityType: entityType, insertedAt: time.Now()}
}

// evictOldestLocked removes the entry with the earliest insertion time.
// Caller must hold m.mu for writing.
func (m *idTypeMap) evictOldestLocked() {
	var oldestID string
	var oldestAt time.Time
	first := true
	for id, e := range m.entries {
		if first || e.insertedAt.Before(oldestAt) {
			oldestID, oldestAt = id, e.insertedAt
			first = false
		}
	}
	delete(m.entries, oldestID)
}

func (m *idTypeMap) resolve(id string) (string, bool) {
	m.mu.RLock()
	e, ok := m.entries[id]
	m.mu.RUnlock()
	if !ok {
		return "", false
	}
	if time.Since(e.insertedAt) > m.ttl {
		return "", false // TTL-expired: treat as unknown (lazy eviction).
	}
	return e.entityType, true
}

// resolveOrWildcard returns the entity type for the given ID if present in the
// map, or "*" if not. This ensures capability annotation falls back to the
// wildcard rather than annotating with an empty type (which fails resolution).
func (m *idTypeMap) resolveOrWildcard(id string) string {
	t, ok := m.resolve(id)
	if !ok {
		return "*"
	}
	return t
}

func (m *idTypeMap) remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, id)
}

// snapshot returns a shallow copy of the live (non-expired) entries, safe for
// reading without holding the lock. The caller is responsible for its own
// synchronisation.
func (m *idTypeMap) snapshot() map[string]string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	snap := make(map[string]string, len(m.entries))
	for id, e := range m.entries {
		if time.Since(e.insertedAt) <= m.ttl {
			snap[id] = e.entityType
		}
	}
	return snap
}

// ---------------------------------------------------------------------------
// Functional options
// ---------------------------------------------------------------------------

// ListEntitiesOption configures a ListEntities call.
type ListEntitiesOption func(*listEntitiesConfig)

type listEntitiesConfig struct {
	pageSize  int32
	pageToken string
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// validateEmbedding checks for NaN or infinity values.
func validateEmbedding(embedding []float32) error {
	for _, v := range embedding {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return fmt.Errorf("flow sdk: embedding contains NaN or infinity")
		}
	}
	return nil
}
