package gitstore

import (
	"sort"
)

// NewChangeLog creates a new ChangeLog with all maps initialised.
func NewChangeLog() *ChangeLog {
	return &ChangeLog{
		AddedEntities:    make(map[string]*EntityEntry),
		ModifiedEntities: make(map[string]*EntityEntry),
		DeletedEntities:  make(map[string]*DeletionInfo),
		AddedEdges:       make(map[string]*EdgeEntry),
		DeletedEdges:     make(map[string]*DeletionInfo),
	}
}

// Add routes a ChangeLogEntry to the correct map based on Kind.
// Returns ErrChangeLogFull if the 100K cap would be exceeded.
// Returns ErrUnknownChangeKind for unrecognised ChangeKind values.
func (cl *ChangeLog) Add(entry ChangeLogEntry) error {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	if cl.count >= 100000 {
		return ErrChangeLogFull
	}

	return cl.add(entry)
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
