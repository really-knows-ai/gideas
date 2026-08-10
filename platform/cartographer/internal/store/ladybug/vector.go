package ladybug

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// IsVectorIndexBootstrapped returns true if the entity type has a non-null
// embedding column and a vector index exists.
func (db *ladybugDB) IsVectorIndexBootstrapped(_ context.Context, entityType, branch string) bool {
	conn, typeDefs, unlock, err := db.lockForRead(branch)
	if err != nil {
		return false
	}
	defer unlock()

	def, ok := typeDefs.entityTypeDefs[entityType]
	if !ok {
		return false
	}

	// Check that the embedding column exists with a dimension > 0.
	dim, err := getEmbeddingDimension(conn, entityType, def.EnableVectorIndex)
	if err != nil {
		return false
	}
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
		if tableName == entityType && strings.EqualFold(indexType, "HNSW") {
			return true
		}
	}
	return false
}

// GetEstablishedDimension returns the dimension of the FLOAT[n] embedding
// column for the given entity type, or 0 if not established.
func (db *ladybugDB) GetEstablishedDimension(_ context.Context, entityType, branch string) (int, error) {
	conn, typeDefs, unlock, err := db.lockForRead(branch)
	if err != nil {
		return 0, err
	}
	defer unlock()

	def, ok := typeDefs.entityTypeDefs[entityType]
	if !ok {
		return 0, fmt.Errorf("%w: %q", store.ErrUnknownEntityType, entityType)
	}

	dim, err := getEmbeddingDimension(conn, entityType, def.EnableVectorIndex)
	if err != nil {
		return 0, fmt.Errorf("read embedding dimension for %q: %w", entityType, err)
	}
	return dim, nil
}

// WipeAll removes all graph data while preserving schema and indexes.
func (db *ladybugDB) WipeAll(ctx context.Context) error {
	conn, _, unlock, err := db.lockForWrite("")
	if err != nil {
		return err
	}
	defer unlock()

	result, err := conn.Query("MATCH (n) DETACH DELETE n;")
	if err != nil {
		return fmt.Errorf("delete graph data: %w", err)
	}
	result.Close()
	return nil
}

// WipeSchema drops all schema tables, indexes, and metadata, leaving the
// database empty for a fresh ApplySchema. Used by WipeGraph before applying
// a destructive schema change.
func (db *ladybugDB) WipeSchema(ctx context.Context) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if db.closed || db.failed {
		return store.ErrDatabaseNotReady
	}

	// Collect all table names to drop, separated by type.
	// LadybugDB requires dropping REL tables before NODE tables.
	var relTables, nodeTables []string
	result, err := db.conn.Query("CALL show_tables() RETURN *;")
	if err != nil {
		return fmt.Errorf("list tables for wipe: %w", err)
	}
	for result.HasNext() {
		tuple, err := result.Next()
		if err != nil {
			result.Close()
			return fmt.Errorf("read table row: %w", err)
		}
		vals, err := tuple.GetAsSlice()
		tuple.Close()
		if err != nil {
			result.Close()
			return fmt.Errorf("get table values: %w", err)
		}
		if len(vals) >= 3 {
			name := fmt.Sprintf("%v", vals[1])
			kind := fmt.Sprintf("%v", vals[2])
			switch strings.ToUpper(kind) {
			case tableTypeRel:
				relTables = append(relTables, name)
			case tableTypeNode:
				nodeTables = append(nodeTables, name)
			}
		}
	}
	result.Close()

	// Drop indexes first, then REL tables, then NODE tables.
	dropTable := func(name string) error {
		// Drop vector/FTS indexes before the table. DROP on an index that does
		// not exist errors in LadybugDB, so guard each drop with an existence
		// probe rather than discarding-or-error-matching. A genuine drop failure
		// (index exists but cannot be dropped) propagates, preventing the
		// residual-index hazard where the drop fails while the subsequent
		// DROP TABLE succeeds — leaving an index pointing at a vanished table
		// that would collide with a later ApplySchema of the same-named type.
		if ok, err := vectorIndexExists(db.conn, name); err != nil {
			return fmt.Errorf("check vector index for %q: %w", name, err)
		} else if ok {
			r, err := db.conn.Query(fmt.Sprintf("CALL DROP_VECTOR_INDEX('%s', '%s_vec');", name, name))
			if err != nil {
				return fmt.Errorf("drop vector index for %q: %w", name, err)
			}
			r.Close()
		}
		if ok, err := ftsIndexExists(db.conn, name); err != nil {
			return fmt.Errorf("check FTS index for %q: %w", name, err)
		} else if ok {
			r, err := db.conn.Query(fmt.Sprintf("CALL DROP_FTS_INDEX('%s', '%s_fts');", name, name))
			if err != nil {
				return fmt.Errorf("drop FTS index for %q: %w", name, err)
			}
			r.Close()
		}
		r, err := db.conn.Query(fmt.Sprintf("DROP TABLE %s;", quoteID(name)))
		if err != nil {
			return fmt.Errorf("drop table %q: %w", name, err)
		}
		r.Close()
		return nil
	}
	for _, name := range relTables {
		if err := dropTable(name); err != nil {
			return err
		}
	}
	for _, name := range nodeTables {
		if err := dropTable(name); err != nil {
			return err
		}
	}

	// Clear in-memory schema cache.
	db.entityTypeDefs = make(map[string]*store.EntityTypeDef)
	db.edgeTypeDefs = make(map[string]*store.EdgeTypeDef)
	db.ruleIndex = make(map[string][]*flowv1.ConnectionRule)
	db.edgePairs = make(map[string][]fromToPair)
	db.schemaApplied = false

	// Drop every open branch connection and its persisted records. A branch's
	// LadybugDB caches the tables dropped above, so leaving the connection open
	// would let a stale branch reference dropped tables (subsequent branch ops
	// error on a vanished schema). And a persisted branches/<txID>.state.json
	// would let SaveBranchTransactionState re-register a branch whose database
	// and schema no longer exist. Enforcing the wipe at the store primitive —
	// closing the branch connections and removing the persisted records — is the
	// defense-in-depth behind the service-layer FAILED_PRECONDITION (SPEC row 915),
	// which only fires for a live transaction; a wipe still must not leave a stale
	// branch dangling.
	for id, br := range db.branches {
		br.mu.Lock()
		if br.conn != nil {
			br.conn.Close()
		}
		if br.db != nil {
			br.db.Close()
		}
		br.mu.Unlock()
		delete(db.branches, id)
	}
	db.branchStates = make(map[string]store.BranchTransactionState)

	// Remove persisted metadata, including the branch durable records.
	if db.path != "" {
		if err := os.Remove(db.mainMetadataPath()); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove schema metadata: %w", err)
		}
		if err := os.RemoveAll(filepath.Join(db.path, "branches")); err != nil {
			return fmt.Errorf("remove branch records: %w", err)
		}
		// Keep the branches container directory so a subsequent CreateBranchDB
		// (which stats/open the branch db file directly) can still create files
		// under it without the prior wipe leaving the path missing.
		if err := os.MkdirAll(filepath.Join(db.path, "branches"), 0o755); err != nil {
			return fmt.Errorf("recreate branches dir: %w", err)
		}
	}
	return nil
}
