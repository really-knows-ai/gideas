package gitstore

import (
	"sort"
)

// DefaultChangeLogCap is the admission cap applied when NewChangeLog is used.
// Exported so the store/service layer can import it rather than hardcoding the
// 100 000 literal.
const DefaultChangeLogCap = 100000

// NewChangeLog creates a new ChangeLog with all maps initialised.
func NewChangeLog() *ChangeLog {
	return newChangeLog(DefaultChangeLogCap)
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
		DeletedEdges:     make(map[string]*DeletionInfo),
		cap:              capacity,
	}
}

// Add routes a ChangeLogEntry to the correct map based on Kind.
// Returns ErrChangeLogFull if the 100K cap would be exceeded.
// Returns ErrUnknownChangeKind for unrecognised ChangeKind values.
func (cl *ChangeLog) Add(entry ChangeLogEntry) error {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	if cl.checkCapacityLocked() {
		return ErrChangeLogFull
	}

	return cl.add(entry)
}

// CheckCapacity reports whether another mutation can be admitted. Transaction
// lifecycle locking serialises this preflight with the subsequent Add call.
func (cl *ChangeLog) CheckCapacity() error {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	if cl.checkCapacityLocked() {
		return ErrChangeLogFull
	}
	return nil
}

// checkCapacityLocked reports whether the ChangeLog has reached its cap.
// Must be called with cl.mu held.
//
// ponytail: The check-then-add performed by a caller of CheckCapacity followed
// by Add is not atomic — between the two lock acquisitions another goroutine
// could fill the final slot, so CheckCapacity is advisory only. This is safe in
// the transaction lifecycle context because per-transaction lifecycle locking
// serialises mutations for a given transaction, making the preflight reliable
// there. The Add method itself re-checks under the lock and returns
// ErrChangeLogFull if the cap was reached.
func (cl *ChangeLog) checkCapacityLocked() bool {
	return cl.count >= cl.cap
}

// AddEntry adds a ChangeLogEntry to the ChangeLog without checking the 100K cap.
// Used during startup recovery reconstruction (RecoverOpenTransactions) where
// entries were already subject to the cap when originally added — per-transaction
// re-checking is unnecessary since the original addition was already gated.
// Returns ErrUnknownChangeKind for unrecognised ChangeKind values.
//
// ponytail: Recovery bypasses the cap via this method. The per-transaction cap
// means a single recovered transaction cannot exceed 100K entries. If the recovery
// algorithm changes to merge entries across transactions into a single ChangeLog,
// callers must ensure the combined count does not exceed the cap.
func (cl *ChangeLog) AddEntry(entry ChangeLogEntry) error {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	return cl.add(entry)
}

// add is the shared insertion logic used by both Add and AddEntry.
// Must be called with cl.mu held.
//
// ponytail: cl.count counts per map slot, not per distinct element. The same
// entityID modified twice (Add then Mod, or Add/Mod coordinated with Del)
// occupies distinct slots in different category maps and so counts twice toward
// the 100K cap, even though only one distinct element was touched. SPEC error
// row "Transaction change log exceeds capacity" (line 917) is framed per
// entity/edge, so a transaction touching 60K distinct entities each
// added-then-modified fails at 120K slots despite only 60K distinct elements.
// A distinct-element count would need a cross-map de-dup set keyed by ID. The
// slot-count is retained because the cap is a memory guard and worst-case (all
// 100K slots are distinct elements) admits at most 100K entities; double-count
// only errs toward rejection. Upgrade path: track distinct IDs in a
// map[string]struct{} across categories and count that set if SPEC-per-entity
// semantics become a requirement.
func (cl *ChangeLog) add(entry ChangeLogEntry) error {
	switch entry.Kind {
	case ChangeAddEntity:
		if _, exists := cl.AddedEntities[entry.ID]; !exists {
			cl.count++
		}
		cl.AddedEntities[entry.ID] = entry.Entity
	case ChangeModEntity:
		if _, exists := cl.ModifiedEntities[entry.ID]; !exists {
			cl.count++
		}
		cl.ModifiedEntities[entry.ID] = entry.Entity
	case ChangeDelEntity:
		if _, exists := cl.DeletedEntities[entry.ID]; !exists {
			cl.count++
		}
		cl.DeletedEntities[entry.ID] = &DeletionInfo{
			Type:      entry.Type,
			Suspected: entry.Suspected,
		}
	case ChangeAddEdge:
		if _, exists := cl.AddedEdges[entry.ID]; !exists {
			cl.count++
		}
		cl.AddedEdges[entry.ID] = entry.Edge
	case ChangeDelEdge:
		if _, exists := cl.DeletedEdges[entry.ID]; !exists {
			cl.count++
		}
		cl.DeletedEdges[entry.ID] = &DeletionInfo{
			Type:      entry.Type,
			Suspected: entry.Suspected,
		}
	default:
		return ErrUnknownChangeKind
	}

	return nil
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
	for id, info := range cl.DeletedEdges {
		entries = append(entries, ChangeLogEntry{
			Kind:      ChangeDelEdge,
			ID:        id,
			Type:      info.Type,
			Suspected: info.Suspected,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Kind < entries[j].Kind ||
			(entries[i].Kind == entries[j].Kind && entries[i].ID < entries[j].ID)
	})
	return entries
}

// Clear resets all maps and resets count to zero.
// Acquires cl.mu for safe concurrent access.
func (cl *ChangeLog) Clear() {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	cl.AddedEntities = make(map[string]*EntityEntry)
	cl.ModifiedEntities = make(map[string]*EntityEntry)
	cl.DeletedEntities = make(map[string]*DeletionInfo)
	cl.AddedEdges = make(map[string]*EdgeEntry)
	cl.DeletedEdges = make(map[string]*DeletionInfo)
	cl.count = 0
}

// Len returns the total number of tracked changes across all maps.
// Acquires cl.mu for safe concurrent access.
func (cl *ChangeLog) Len() int {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return cl.count
}
