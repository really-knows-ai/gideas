package ladybug

import (
	"context"
	"fmt"
	"strings"

	lbug "github.com/LadybugDB/go-ladybug"
	"github.com/foundry/flow/cartographer/internal/store"
)

func rebuildBranchSchemaCache(conn *lbug.Connection) (
	map[string]*store.EntityTypeDef, map[string]*store.EdgeTypeDef, error,
) {
	entities := make(map[string]*store.EntityTypeDef)
	edges := make(map[string]*store.EdgeTypeDef)
	vectorIndexes, err := vectorIndexesOnConn(conn)
	if err != nil {
		return nil, nil, err
	}
	result, err := conn.Query("CALL show_tables() RETURN *;")
	if err != nil {
		return nil, nil, err
	}
	defer result.Close()
	for result.HasNext() {
		tuple, err := result.Next()
		if err != nil {
			return nil, nil, err
		}
		values, err := tuple.GetAsSlice()
		tuple.Close()
		if err != nil {
			return nil, nil, err
		}
		if len(values) < 3 {
			continue
		}
		name, kind := fmt.Sprint(values[1]), strings.ToUpper(fmt.Sprint(values[2]))
		properties, err := tablePropertiesOnConn(conn, name, kind, vectorIndexes[name])
		if err != nil {
			return nil, nil, err
		}
		switch kind {
		case "NODE":
			entities[name] = &store.EntityTypeDef{
				Name: name, Properties: properties, EnableVectorIndex: vectorIndexes[name],
			}
		case "REL":
			edges[name] = &store.EdgeTypeDef{Name: name, Properties: properties}
		}
	}
	return entities, edges, nil
}

// ReplicateSchemaToBranch applies the main DB's schema DDL to the branch.
func (bl *branchLifecycle) ReplicateSchemaToBranch(_ context.Context, txID string) error {
	db := bl.db
	db.mu.Lock()
	defer db.mu.Unlock()
	br, err := bl.branchLocked(txID)
	if err != nil {
		return err
	}
	br.mu.Lock()
	defer br.mu.Unlock()
	if br.failed {
		return store.ErrDatabaseNotReady
	}
	// Copy type definitions.
	for name, def := range db.entityTypeDefs {
		cloned := cloneEntityTypeDef(def)
		clone := &cloned
		br.entityTypeDefs[name] = clone
	}
	for name, def := range db.edgeTypeDefs {
		cloned := cloneEdgeTypeDef(def)
		clone := &cloned
		br.edgeTypeDefs[name] = clone
	}
	// Replay DDL on the branch connection.
	// Get DDL from main's table definitions.
	// We need to recreate the node and rel tables.
	for _, name := range sortedKeys(db.entityTypeDefs) {
		def := db.entityTypeDefs[name]
		dimension, derr := getEmbeddingDimension(db.conn, name, def.EnableVectorIndex)
		if derr != nil {
			return fmt.Errorf("read embedding dimension for %q: %w", name, derr)
		}
		if err := createNodeTableOnConn(br.conn, name, def.Properties); err != nil {
			return fmt.Errorf("replicate node table %q: %w", name, err)
		}
		if dimension > 0 {
			alterDDL := fmt.Sprintf("ALTER TABLE %s ADD embedding FLOAT[%d];", quoteID(name), dimension)
			r, err := br.conn.Query(alterDDL)
			if err != nil {
				return fmt.Errorf("replicate embedding column %q: %w", name, err)
			}
			r.Close()
			if err := db.createVectorIndex(br.conn, name); err != nil {
				br.failed = true
				return fmt.Errorf("replicate vector index %q: %w", name, err)
			}
		}
	}
	for _, name := range sortedKeys(db.edgeTypeDefs) {
		def := db.edgeTypeDefs[name]
		pairs := db.edgePairs[name]
		if err := createRelTableOnConn(br.conn, name, def.Properties, pairs); err != nil {
			return fmt.Errorf("replicate edge table %q: %w", name, err)
		}
	}
	if db.path != "" {
		// Persist the branch metadata with main's FROM/TO endpoint pairs: the
		// branch rel tables above are created from db.edgePairs, and a reopened
		// branch (branchLocked → restoreBranchSchemaMetadata) validates the
		// persisted pairs against the branch catalog — a lossy write would fail
		// that comparison for rule-less inferred edge types (SPEC R8).
		metadata := metadataFromDefinitions(br.entityTypeDefs, br.edgeTypeDefs, db.edgePairs)
		metadata, err = captureVectorState(br.conn, metadata)
		if err != nil {
			br.failed = true
			return fmt.Errorf("capture branch vector state: %w", err)
		}
		if err := db.writeMetadata(db.branchMetadataPath(txID), metadata); err != nil {
			br.failed = true
			return fmt.Errorf("persist branch schema metadata: %w", err)
		}
	}
	return nil
}
