package ladybug

import (
	"context"
	"fmt"
	"sort"

	lbug "github.com/LadybugDB/go-ladybug"
	"github.com/foundry/flow/cartographer/internal/store"
)

// DumpAllEntities returns all entities from a branch (or main if branch empty).
func (rh *rehydrator) DumpAllEntities(ctx context.Context, txID string) ([]store.Entity, error) {
	return dumpAll(rh.db, txID,
		func(c *branchDBCache) []string { return sortedKeys(c.entityTypeDefs) },
		func(conn *lbug.Connection, c *branchDBCache, name string) ([]store.Entity, error) {
			entities, err := listEntitiesOnConn(conn, name, c.entityTypeDefs[name].EnableVectorIndex)
			if err != nil {
				return nil, fmt.Errorf("query entity type %q: %w", name, err)
			}
			return entities, nil
		})
}

// DumpAllEdges returns all edges from a branch (or main if branch empty).
func (rh *rehydrator) DumpAllEdges(ctx context.Context, txID string) ([]store.Edge, error) {
	return dumpAll(rh.db, txID,
		func(c *branchDBCache) []string { return sortedKeys(c.edgeTypeDefs) },
		func(conn *lbug.Connection, _ *branchDBCache, name string) ([]store.Edge, error) {
			edges, err := listEdgesOnConn(conn, name)
			if err != nil {
				return nil, fmt.Errorf("query edge type %q: %w", name, err)
			}
			return edges, nil
		})
}

// dumpAll is the shared enumeration skeleton behind DumpAllEntities and
// DumpAllEdges: it acquires the branch read lock (or main), iterates the
// given kind's type names in sorted order, aggregates each type's rows, and
// normalizes an empty dump to a non-nil slice. names selects the sorted
// type-name set for the kind; enumerate runs one type's query/parse loop.
func dumpAll[T any](
	db *ladybugDB,
	txID string,
	names func(*branchDBCache) []string,
	enumerate func(conn *lbug.Connection, typeDefs *branchDBCache, name string) ([]T, error),
) ([]T, error) {
	conn, typeDefs, unlock, err := db.lockForRead(txID)
	if err != nil {
		return nil, err
	}
	defer unlock()

	var results []T
	for _, name := range names(typeDefs) {
		rows, err := enumerate(conn, typeDefs, name)
		if err != nil {
			return nil, err
		}
		results = append(results, rows...)
	}
	if results == nil {
		results = []T{}
	}
	return results, nil
}

// listEntitiesOnConn returns every entity of the given type from the
// connection, mirroring listEdgesOnConn (crud.go) on the entity side.
func listEntitiesOnConn(conn *lbug.Connection, entityType string, vectorIndexed bool) ([]store.Entity, error) {
	q := fmt.Sprintf("MATCH (n:%s) RETURN n;", quoteID(entityType))
	result, err := conn.Query(q)
	if err != nil {
		return nil, err
	}
	defer result.Close()

	var entities []store.Entity
	for result.HasNext() {
		tuple, err := result.Next()
		if err != nil {
			return nil, fmt.Errorf("read entity row: %w", err)
		}
		m, err := tuple.GetAsMap()
		tuple.Close()
		if err != nil {
			return nil, fmt.Errorf("parse entity row: %w", err)
		}
		node, ok := m["n"].(lbug.Node)
		if !ok {
			return nil, fmt.Errorf("entity row for %q: unexpected node type %T", entityType, m["n"])
		}
		entities = append(entities, *entityFromNode(node, entityType, vectorIndexed))
	}
	if entities == nil {
		entities = []store.Entity{}
	}
	return entities, nil
}

// sortedKeys returns the sorted keys of a string-keyed map.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
