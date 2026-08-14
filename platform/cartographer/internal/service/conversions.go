package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
)

// groupChanges organises change log entries by type and operation.
type typeChanges struct {
	addedEntities    map[string][]*gitstore.EntityEntry
	modifiedEntities map[string][]*gitstore.EntityEntry
	deletedEntities  map[string][]string
	addedEdges       map[string][]*gitstore.EdgeEntry
	modifiedEdges    map[string][]*gitstore.EdgeEntry
	deletedEdges     map[string][]string
}

func groupChanges(cl *gitstore.ChangeLog) typeChanges {
	tc := typeChanges{
		addedEntities:    make(map[string][]*gitstore.EntityEntry),
		modifiedEntities: make(map[string][]*gitstore.EntityEntry),
		deletedEntities:  make(map[string][]string),
		addedEdges:       make(map[string][]*gitstore.EdgeEntry),
		modifiedEdges:    make(map[string][]*gitstore.EdgeEntry),
		deletedEdges:     make(map[string][]string),
	}
	for _, entry := range cl.Entries() {
		switch entry.Kind {
		case gitstore.ChangeAddEntity:
			tc.addedEntities[entry.Type] = append(tc.addedEntities[entry.Type], entry.Entity)
		case gitstore.ChangeModEntity:
			tc.modifiedEntities[entry.Type] = append(tc.modifiedEntities[entry.Type], entry.Entity)
		case gitstore.ChangeDelEntity:
			tc.deletedEntities[entry.Type] = append(tc.deletedEntities[entry.Type], entry.ID)
		case gitstore.ChangeAddEdge:
			tc.addedEdges[entry.Type] = append(tc.addedEdges[entry.Type], entry.Edge)
		case gitstore.ChangeModEdge:
			tc.modifiedEdges[entry.Type] = append(tc.modifiedEdges[entry.Type], entry.Edge)
		case gitstore.ChangeDelEdge:
			tc.deletedEdges[entry.Type] = append(tc.deletedEdges[entry.Type], entry.ID)
		}
	}
	return tc
}

// resolveSameIDSequences applies final-state semantics to change-log entries
// whose entity ID appears in both the added and deleted buckets. The change log
// is map-based and loses operation order, so DeleteEntity followed by
// CreateEntity with the same explicit ID (delete-then-recreate) is
// indistinguishable from CreateEntity followed by DeleteEntity
// (create-then-delete) — both record the ID in AddedEntities and
// DeletedEntities. The commit path writes added files before removing deleted
// ones, so a naive write-then-remove would drop the recreated <id>.json from
// the committed tree while the branch LadybugDB's final state still contains
// the entity; main is then re-hydrated from the committed tree (SPEC R9 commit
// step 8), silently losing it. The branch DB holds the authoritative final
// state, so each same-ID add/delete pair is resolved against it: a present
// entity keeps its file (dropped from the removal bucket, so the removal does
// not undo the write), an absent one is dropped from the write bucket (the
// removal is then a no-op on the absent file). Edges are not resolved:
// CreateEdge always generates a fresh UUID (the RPC takes no ID), so an edge
// ID can only appear in both buckets via create-then-delete, where the
// write-then-remove ordering already produces the correct absent final state.
func (s *CartographerServer) resolveSameIDSequences(
	ctx context.Context, branch string, tc *typeChanges,
) error {
	for et, deletes := range tc.deletedEntities {
		adds, ok := tc.addedEntities[et]
		if !ok {
			continue
		}
		addByID := make(map[string]*gitstore.EntityEntry, len(adds))
		for _, e := range adds {
			addByID[e.ID] = e
		}
		kept := deletes[:0]
		for _, id := range deletes {
			if _, overlapped := addByID[id]; !overlapped {
				kept = append(kept, id)
				continue
			}
			if _, err := s.store.GetEntity(ctx, id, branch); err != nil {
				if errors.Is(err, store.ErrEntityNotFound) {
					// create-then-delete: the final state is absent — drop the
					// write; the removal is a no-op on the absent file.
					tc.addedEntities[et] = withoutEntityEntry(tc.addedEntities[et], id)
					kept = append(kept, id)
					continue
				}
				return fmt.Errorf("resolve final state of entity %q on branch %q: %w", id, branch, err)
			}
			// delete-then-recreate: the final state is present — keep the
			// write and drop the removal so the recreated file survives.
		}
		tc.deletedEntities[et] = kept
	}
	return nil
}

// withoutEntityEntry returns entries with the entry whose ID matches id removed.
func withoutEntityEntry(entries []*gitstore.EntityEntry, id string) []*gitstore.EntityEntry {
	out := entries[:0]
	for _, e := range entries {
		if e.ID != id {
			out = append(out, e)
		}
	}
	return out
}

// toGitEntities converts change-log entity entries to gitstore.Entity values
// for file serialisation. Embeddings are stripped for entity types that are
// not vector-indexed (SPEC R7: embedding data is discarded — not persisted),
// so that no embedding data leaks into the git repository for non-indexed
// types. For indexed types the embedding is retained so corruption recovery
// (SPEC R8) can restore the full graph state including vector data.
func (s *CartographerServer) toGitEntities(entries []*gitstore.EntityEntry) []gitstore.Entity {
	r := make([]gitstore.Entity, len(entries))
	for i, e := range entries {
		emb := e.Embedding
		if def, ok := s.store.EntityType(e.Type); ok && !def.EnableVectorIndex {
			emb = nil
		}
		r[i] = gitstore.Entity{
			ID: e.ID, Type: e.Type, Properties: e.Properties,
			Embedding: emb, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
		}
	}
	return r
}

func toGitEdges(entries []*gitstore.EdgeEntry) []gitstore.Edge {
	r := make([]gitstore.Edge, len(entries))
	for i, e := range entries {
		r[i] = gitstore.Edge{
			ID: e.ID, Type: e.Type, FromEntityID: e.FromEntityID,
			ToEntityID: e.ToEntityID, Properties: e.Properties,
			CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
		}
	}
	return r
}

// contentEqual compares the shared data content of a store element against its
// gitstore file form — ID, type, and the properties map — then delegates the
// type-specific fields (edge endpoints, entity embedding) to extra. Timestamps
// are ignored, since they may differ due to JSON round-trip precision or
// re-hydration timing.
func contentEqual(
	idA, typeA string, propsA map[string]string,
	idB, typeB string, propsB map[string]string,
	extra func() bool,
) bool {
	if idA != idB || typeA != typeB {
		return false
	}
	if len(propsA) != len(propsB) {
		return false
	}
	for k, v := range propsA {
		if propsB[k] != v {
			return false
		}
	}
	return extra()
}

// entityContentEqual returns true when the data content of a store.Entity
// matches a gitstore.EntityFile (the entity-specific embedding compare).
func entityContentEqual(a store.Entity, b gitstore.EntityFile) bool {
	return contentEqual(a.Id, a.Type, a.Properties, b.ID, b.Type, b.Properties, func() bool {
		if len(a.Embedding) != len(b.Embedding) {
			return false
		}
		for i := range a.Embedding {
			if a.Embedding[i] != b.Embedding[i] {
				return false
			}
		}
		return true
	})
}

// edgeContentEqual returns true when the data content of a store.Edge matches
// a gitstore.EdgeFile (the edge-specific endpoint compare).
func edgeContentEqual(a store.Edge, b gitstore.EdgeFile) bool {
	return contentEqual(a.Id, a.Type, a.Properties, b.ID, b.Type, b.Properties, func() bool {
		return a.FromEntityID == b.FromEntityID && a.ToEntityID == b.ToEntityID
	})
}
