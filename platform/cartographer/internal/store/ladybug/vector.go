package ladybug

import (
	"context"
	"fmt"

	"github.com/foundry/flow/cartographer/internal/store"
)

// IsVectorIndexBootstrapped returns true if the entity type has a non-null
// embedding column and a vector index exists.
func (db *ladybugDB) IsVectorIndexBootstrapped(entityType, branch string) bool {
	conn, typeDefs, unlock, err := db.lockForRead(branch)
	if err != nil {
		return false
	}
	defer unlock()

	if _, ok := typeDefs.entityTypeDefs[entityType]; !ok {
		return false
	}

	// Check that the embedding column exists with a dimension > 0.
	dim := getEmbeddingDimension(conn, entityType)
	if dim == 0 {
		return false
	}

	// Check that a vector index exists.
	q := "CALL show_indexes() RETURN *;"
	result, err := conn.Query(q)
	if err != nil {
		return false
	}
	defer result.Close()

	for result.HasNext() {
		tuple, err := result.Next()
		if err != nil {
			return false
		}
		vals, err := tuple.GetAsSlice()
		tuple.Close()
		if err != nil || len(vals) < 3 {
			continue
		}
		tableName := fmt.Sprintf("%v", vals[0])
		indexType := fmt.Sprintf("%v", vals[2])
		if tableName == entityType && indexType == "HNSW" {
			return true
		}
	}
	return false
}

// GetEstablishedDimension returns the dimension of the FLOAT[n] embedding
// column for the given entity type, or 0 if not established.
func (db *ladybugDB) GetEstablishedDimension(entityType, branch string) (int, error) {
	conn, typeDefs, unlock, err := db.lockForRead(branch)
	if err != nil {
		return 0, err
	}
	defer unlock()

	if _, ok := typeDefs.entityTypeDefs[entityType]; !ok {
		return 0, fmt.Errorf("%w: %q", store.ErrUnknownEntityType, entityType)
	}

	dim := getEmbeddingDimension(conn, entityType)
	return dim, nil
}

// WipeAll drops all node and rel tables (resetting the database).
func (db *ladybugDB) WipeAll(ctx context.Context) error {
	conn, typeDefs, unlock, err := db.lockForWrite("")
	if err != nil {
		return err
	}
	defer unlock()

	// Drop all rel tables first (must drop before their referenced node tables).
	for name := range typeDefs.edgeTypeDefs {
		q := fmt.Sprintf("DROP TABLE %s;", quoteID(name))
		// Ignore errors — table may not exist or already be dropped.
		_, _ = conn.Query(q)
	}
	// Drop all node tables.
	for name := range typeDefs.entityTypeDefs {
		q := fmt.Sprintf("DROP TABLE %s;", quoteID(name))
		_, _ = conn.Query(q)
	}

	// Rebuild cache (will be empty). Lock already held from lockForWrite.
	db.entityTypeDefs = make(map[string]*store.EntityTypeDef)
	db.edgeTypeDefs = make(map[string]*store.EdgeTypeDef)

	return nil
}
