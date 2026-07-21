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

	switch entry.Kind {
	case ChangeAddEntity:
		cl.AddedEntities[entry.ID] = entry.Entity
	case ChangeModEntity:
		cl.ModifiedEntities[entry.ID] = entry.Entity
	case ChangeDelEntity:
		cl.DeletedEntities[entry.ID] = &DeletionInfo{
			Type:      entry.Type,
			Suspected: entry.Suspected,
		}
	case ChangeAddEdge:
		cl.AddedEdges[entry.ID] = entry.Edge
	case ChangeDelEdge:
		cl.DeletedEdges[entry.ID] = &DeletionInfo{
			Type:      entry.Type,
			Suspected: entry.Suspected,
		}
	default:
		return ErrUnknownChangeKind
	}

	cl.count++
	return nil
}

// AddEntity adds an entity creation to the ChangeLog.
// Returns ErrChangeLogFull if the 100K cap would be exceeded.
func (cl *ChangeLog) AddEntity(id, entityType string, props map[string]string, embedding []float32) error {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	if cl.count >= 100000 {
		return ErrChangeLogFull
	}

	cl.AddedEntities[id] = &EntityEntry{
		ID:         id,
		Type:       entityType,
		Properties: props,
		Embedding:  embedding,
	}
	cl.count++
	return nil
}

// ModifyEntity adds an entity modification to the ChangeLog.
// Returns ErrChangeLogFull if the 100K cap would be exceeded.
func (cl *ChangeLog) ModifyEntity(id, entityType string, props map[string]string, embedding []float32) error {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	if cl.count >= 100000 {
		return ErrChangeLogFull
	}

	cl.ModifiedEntities[id] = &EntityEntry{
		ID:         id,
		Type:       entityType,
		Properties: props,
		Embedding:  embedding,
	}
	cl.count++
	return nil
}

// DeleteEntity adds a non-suspected entity deletion to the ChangeLog.
// Returns ErrChangeLogFull if the 100K cap would be exceeded.
func (cl *ChangeLog) DeleteEntity(id, entityType string) error {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	if cl.count >= 100000 {
		return ErrChangeLogFull
	}

	cl.DeletedEntities[id] = &DeletionInfo{
		Type:      entityType,
		Suspected: false,
	}
	cl.count++
	return nil
}

// AddEdge adds an edge creation to the ChangeLog.
// Returns ErrChangeLogFull if the 100K cap would be exceeded.
func (cl *ChangeLog) AddEdge(id, edgeType, fromID, toID string, props map[string]string) error {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	if cl.count >= 100000 {
		return ErrChangeLogFull
	}

	cl.AddedEdges[id] = &EdgeEntry{
		ID:           id,
		Type:         edgeType,
		FromEntityID: fromID,
		ToEntityID:   toID,
		Properties:   props,
	}
	cl.count++
	return nil
}

// DeleteEdge adds a non-suspected edge deletion to the ChangeLog.
// Returns ErrChangeLogFull if the 100K cap would be exceeded.
func (cl *ChangeLog) DeleteEdge(id, edgeType string) error {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	if cl.count >= 100000 {
		return ErrChangeLogFull
	}

	cl.DeletedEdges[id] = &DeletionInfo{
		Type:      edgeType,
		Suspected: false,
	}
	cl.count++
	return nil
}

// Entries returns all entries flattened into a slice.
// Acquires cl.mu for safe concurrent access.
func (cl *ChangeLog) Entries() []ChangeLogEntry {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	var entries []ChangeLogEntry
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
