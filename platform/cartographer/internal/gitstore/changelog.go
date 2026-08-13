package gitstore

import (
	"sort"

	"github.com/foundry/flow/cartographer/internal/store"
)

// NewChangeLog creates a new ChangeLog with all maps initialised, applying the
// store-layer admission cap (store.DefaultChangeLogCap, SPEC.md:888-889).
func NewChangeLog() *ChangeLog {
	return newChangeLog(store.DefaultChangeLogCap)
}

// NewChangeLogWithCap creates a ChangeLog with an explicit admission cap.
func NewChangeLogWithCap(capacity int) *ChangeLog {
	return newChangeLog(capacity)
}

func newChangeLog(capacity int) *ChangeLog {
	return &ChangeLog{
		AddedEntities:    make(map[string]*EntityEntry),
		ModifiedEntities: make(map[string]*EntityEntry),
		DeletedEntities:  make(map[string]*DeletionInfo),
		AddedEdges:       make(map[string]*EdgeEntry),
		ModifiedEdges:    make(map[string]*EdgeEntry),
		DeletedEdges:     make(map[string]*DeletionInfo),
		cap:              capacity,
	}
}

// Add routes a ChangeLogEntry to the correct map based on Kind.
// Enforces the SPEC change-log admission predicate (SPEC:891-892): a mutation
// is rejected with ErrChangeLogFull only when it would grow the log past its
// cap. At cap, a mutation on an element already tracked by the log is admitted
// — it reuses the element's slot and does not exceed the limit. Returns
// ErrUnknownChangeKind for unrecognised ChangeKind values and
// ErrChangeLogNilSnapshot for add/modify entries lacking their required
// Entity/Edge snapshot (Entries() dereferences the snapshot on read-back).
func (cl *ChangeLog) Add(entry ChangeLogEntry) error {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	if cl.checkCapacityLocked(entry) {
		return ErrChangeLogFull
	}

	return cl.add(entry)
}

// CheckCapacity reports whether the given mutation can be admitted without
// growing the change log past its cap. It is the entry-aware preflight used by
// the service layer before a branch mutation, mirroring Add's admission
// predicate. Callers that cannot yet know the entry's ID (CreateEntity or
// CreateEdge with an auto-generated UUID) pass an entry with an empty ID,
// which is treated as a new element — exact, because a fresh UUID can never
// reuse a logged slot.
//
// ponytail: The check-then-add performed by a caller of CheckCapacity followed
// by Add is not atomic — between the two lock acquisitions another goroutine
// could grow the log, so CheckCapacity is advisory only. This is safe in the
// transaction lifecycle context because per-transaction lifecycle locking
// serialises mutations for a given transaction. The Add method itself
// re-checks under the lock with the actual entry and returns ErrChangeLogFull
// only if the entry would exceed the cap.
func (cl *ChangeLog) CheckCapacity(entry ChangeLogEntry) error {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	if cl.checkCapacityLocked(entry) {
		return ErrChangeLogFull
	}
	return nil
}

// checkCapacityLocked reports whether the entry would grow the log past its
// cap. An entry whose element is already tracked reuses its slot and is
// admitted even at cap — the SPEC admission predicate rejects only operations
// "that would cause the log to exceed the limit" (SPEC:891-892). Must be
// called with cl.mu held.
func (cl *ChangeLog) checkCapacityLocked(entry ChangeLogEntry) bool {
	if cl.count < cl.cap {
		return false
	}
	return !cl.hasElement(entry.ID)
}

// AddEntry adds a ChangeLogEntry to the ChangeLog without checking the 100K cap.
// Used during startup recovery reconstruction (RecoverOpenTransactions) where
// entries were already subject to the cap when originally added — per-transaction
// re-checking is unnecessary since the original addition was already gated.
// Returns ErrUnknownChangeKind for unrecognised ChangeKind values and
// ErrChangeLogNilSnapshot for add/modify entries lacking their required
// Entity/Edge snapshot (Entries() dereferences the snapshot on read-back).
//
// ponytail: Recovery bypasses the cap via this method. The per-transaction cap
// means a single recovered transaction cannot exceed 100K distinct
// entities/edges. If the recovery algorithm changes to merge entries across
// transactions into a single ChangeLog, callers must ensure the combined
// distinct-element count does not exceed the cap.
func (cl *ChangeLog) AddEntry(entry ChangeLogEntry) error {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	return cl.add(entry)
}

// add is the shared insertion logic used by both Add and AddEntry.
// Must be called with cl.mu held.
//
// cl.count counts distinct entities/edges touched by the transaction, matching
// the SPEC error-table row "Transaction change log exceeds capacity"
// (SPEC:968): the trigger is a transaction that "modified more than 100 000
// entities/edges". An element is distinct by ID across all six category maps —
// adding then modifying the same entity records two mutation entries but
// counts as one element. IDs are UUID v4s, unique across entities and edges,
// so raw-ID membership across the maps is the distinct-element test.
func (cl *ChangeLog) add(entry ChangeLogEntry) error {
	switch entry.Kind {
	case ChangeAddEntity:
		if entry.Entity == nil {
			return ErrChangeLogNilSnapshot
		}
		if !cl.hasElement(entry.ID) {
			cl.count++
		}
		cl.AddedEntities[entry.ID] = entry.Entity
	case ChangeModEntity:
		if entry.Entity == nil {
			return ErrChangeLogNilSnapshot
		}
		if !cl.hasElement(entry.ID) {
			cl.count++
		}
		cl.ModifiedEntities[entry.ID] = entry.Entity
	case ChangeDelEntity:
		if !cl.hasElement(entry.ID) {
			cl.count++
		}
		info := &DeletionInfo{
			Type:      entry.Type,
			Suspected: entry.Suspected,
		}
		// The deleted entity's snapshot is optional: it is present for live
		// deletions (recorded with the data in hand) and absent for suspected
		// recovery deletions (the element is absent from the branch DB).
		if entry.Entity != nil {
			info.Properties = entry.Entity.Properties
			info.Embedding = entry.Entity.Embedding
		}
		cl.DeletedEntities[entry.ID] = info
	case ChangeAddEdge:
		if entry.Edge == nil {
			return ErrChangeLogNilSnapshot
		}
		if !cl.hasElement(entry.ID) {
			cl.count++
		}
		cl.AddedEdges[entry.ID] = entry.Edge
	case ChangeModEdge:
		if entry.Edge == nil {
			return ErrChangeLogNilSnapshot
		}
		if !cl.hasElement(entry.ID) {
			cl.count++
		}
		cl.ModifiedEdges[entry.ID] = entry.Edge
	case ChangeDelEdge:
		if !cl.hasElement(entry.ID) {
			cl.count++
		}
		info := &DeletionInfo{
			Type:      entry.Type,
			Suspected: entry.Suspected,
		}
		// The deleted edge's snapshot is optional (see ChangeDelEntity above).
		if entry.Edge != nil {
			info.Properties = entry.Edge.Properties
			info.FromEntityID = entry.Edge.FromEntityID
			info.ToEntityID = entry.Edge.ToEntityID
		}
		cl.DeletedEdges[entry.ID] = info
	default:
		return ErrUnknownChangeKind
	}

	return nil
}

// hasElement reports whether an element with the given ID is already tracked
// in any of the six category maps — the distinct-element test. IDs are UUID
// v4s, unique across entities and edges, so an ID identifies exactly one
// element regardless of category. Must be called with cl.mu held.
func (cl *ChangeLog) hasElement(id string) bool {
	if _, ok := cl.AddedEntities[id]; ok {
		return true
	}
	if _, ok := cl.ModifiedEntities[id]; ok {
		return true
	}
	if _, ok := cl.DeletedEntities[id]; ok {
		return true
	}
	if _, ok := cl.AddedEdges[id]; ok {
		return true
	}
	if _, ok := cl.ModifiedEdges[id]; ok {
		return true
	}
	_, ok := cl.DeletedEdges[id]
	return ok
}

// Entries returns all entries flattened into a slice.
// Acquires cl.mu for safe concurrent access.
func (cl *ChangeLog) Entries() []ChangeLogEntry {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	entries := make([]ChangeLogEntry, 0, cl.count)
	for id, ent := range cl.AddedEntities {
		entries = append(entries, ChangeLogEntry{
			Kind:   ChangeAddEntity,
			ID:     id,
			Type:   ent.Type,
			Entity: ent,
		})
	}
	for id, ent := range cl.ModifiedEntities {
		entries = append(entries, ChangeLogEntry{
			Kind:   ChangeModEntity,
			ID:     id,
			Type:   ent.Type,
			Entity: ent,
		})
	}
	for id, info := range cl.DeletedEntities {
		entries = append(entries, ChangeLogEntry{
			Kind:      ChangeDelEntity,
			ID:        id,
			Type:      info.Type,
			Suspected: info.Suspected,
			// Carry the captured payload so GetTransactionDiff can populate the
			// wire fields for deleted entities (nil snapshot for suspected
			// recoveries, whose payload is inherently absent).
			Entity: &EntityEntry{
				ID: id, Type: info.Type, Properties: info.Properties, Embedding: info.Embedding,
			},
		})
	}
	for id, edge := range cl.AddedEdges {
		entries = append(entries, ChangeLogEntry{
			Kind: ChangeAddEdge,
			ID:   id,
			Type: edge.Type,
			Edge: edge,
		})
	}
	for id, edge := range cl.ModifiedEdges {
		entries = append(entries, ChangeLogEntry{
			Kind: ChangeModEdge,
			ID:   id,
			Type: edge.Type,
			Edge: edge,
		})
	}
	for id, info := range cl.DeletedEdges {
		entries = append(entries, ChangeLogEntry{
			Kind:      ChangeDelEdge,
			ID:        id,
			Type:      info.Type,
			Suspected: info.Suspected,
			// Carry the captured payload so GetTransactionDiff can populate the
			// wire fields for deleted edges (nil snapshot for suspected
			// recoveries, whose payload is inherently absent).
			Edge: &EdgeEntry{
				ID: id, Type: info.Type, FromEntityID: info.FromEntityID,
				ToEntityID: info.ToEntityID, Properties: info.Properties,
			},
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Kind < entries[j].Kind ||
			(entries[i].Kind == entries[j].Kind && entries[i].ID < entries[j].ID)
	})
	return entries
}

// Len returns the number of distinct entities/edges touched by the
// transaction — the metric the 100K cap counts (SPEC:968).
// Acquires cl.mu for safe concurrent access.
func (cl *ChangeLog) Len() int {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return cl.count
}
