package ladybug

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	lbug "github.com/LadybugDB/go-ladybug"
	"github.com/foundry/flow/cartographer/internal/store"
)

// --------------------------------------------------------------------------
// Branch lifecycle
// --------------------------------------------------------------------------

// CreateBranchDB opens a new LadybugDB for the given txID. File-backed stores
// persist branches under branches/<txID>.lbug; in-memory stores remain ephemeral.
func (db *ladybugDB) CreateBranchDB(_ context.Context, txID string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if db.closed || db.failed {
		return store.ErrDatabaseNotReady
	}
	if _, ok := db.branches[txID]; ok {
		return fmt.Errorf("%w: branch for tx %q", store.ErrBranchAlreadyExists, txID)
	}

	if filepath.Base(txID) != txID || txID == "." || txID == ".." {
		return fmt.Errorf("invalid branch ID %q", txID)
	}
	var (
		database *lbug.Database
		err      error
		path     string
	)
	if db.path == "" {
		database, err = lbug.OpenInMemoryDatabase(lbug.DefaultSystemConfig())
	} else {
		path = db.branchPath(txID)
		if _, statErr := os.Stat(path); statErr == nil {
			return fmt.Errorf("%w: branch for tx %q", store.ErrBranchAlreadyExists, txID)
		} else if !os.IsNotExist(statErr) {
			return fmt.Errorf("stat branch database: %w", statErr)
		}
		database, err = lbug.OpenDatabase(path, lbug.DefaultSystemConfig())
	}
	if err != nil {
		return fmt.Errorf("open branch database: %w", err)
	}

	conn, err := lbug.OpenConnection(database)
	if err != nil {
		database.Close()
		if path != "" {
			_ = os.RemoveAll(path)
		}
		return fmt.Errorf("open branch connection: %w", err)
	}

	br := &branchDB{
		db:             database,
		conn:           conn,
		entityTypeDefs: make(map[string]*store.EntityTypeDef),
		edgeTypeDefs:   make(map[string]*store.EdgeTypeDef),
	}

	if err := loadBranchExtensions(conn); err != nil {
		conn.Close()
		database.Close()
		if path != "" {
			_ = os.RemoveAll(path)
		}
		return err
	}

	db.branches[txID] = br
	return nil
}

// DropBranchDB closes and removes the branch database.
func (db *ladybugDB) DropBranchDB(_ context.Context, txID string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if filepath.Base(txID) != txID || txID == "." || txID == ".." {
		return fmt.Errorf("invalid branch ID %q", txID)
	}

	br, ok := db.branches[txID]
	if ok {
		br.mu.Lock()
		if br.conn != nil {
			br.conn.Close()
		}
		if br.db != nil {
			br.db.Close()
		}
		br.mu.Unlock()
		delete(db.branches, txID)
	}
	if db.path != "" {
		if err := os.RemoveAll(db.branchPath(txID)); err != nil {
			return fmt.Errorf("remove branch database: %w", err)
		}
		if err := os.Remove(db.branchMetadataPath(txID)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove branch schema metadata: %w", err)
		}
		if err := os.Remove(db.branchStatePath(txID)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove branch state: %w", err)
		}
		if err := syncDirectory(filepath.Join(db.path, "branches")); err != nil {
			return fmt.Errorf("sync branch cleanup: %w", err)
		}
	}
	delete(db.branchStates, txID)
	return nil
}

func (db *ladybugDB) branchPath(txID string) string {
	return filepath.Join(db.path, "branches", txID+".lbug")
}

// branchLocked returns a branch while db.mu is held, lazily reopening a
// persisted branch after process restart.
func (db *ladybugDB) branchLocked(txID string) (*branchDB, error) {
	if db.closed || db.failed {
		return nil, store.ErrDatabaseNotReady
	}
	if filepath.Base(txID) != txID || txID == "." || txID == ".." {
		return nil, fmt.Errorf("invalid branch ID %q", txID)
	}
	if br, ok := db.branches[txID]; ok {
		br.mu.Lock()
		failed := br.failed
		br.mu.Unlock()
		if failed {
			return nil, store.ErrDatabaseNotReady
		}
		return br, nil
	}
	if db.path == "" {
		return nil, fmt.Errorf("%w: branch for tx %q", store.ErrBranchNotFound, txID)
	}
	path := db.branchPath(txID)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: branch for tx %q", store.ErrBranchNotFound, txID)
		}
		return nil, fmt.Errorf("stat branch database: %w", err)
	}
	database, err := lbug.OpenDatabase(path, lbug.DefaultSystemConfig())
	if err != nil {
		return nil, fmt.Errorf("open persisted branch %q: %w", txID, err)
	}
	conn, err := lbug.OpenConnection(database)
	if err != nil {
		database.Close()
		return nil, fmt.Errorf("open persisted branch connection %q: %w", txID, err)
	}
	br := &branchDB{db: database, conn: conn}
	if err := loadBranchExtensions(conn); err != nil {
		conn.Close()
		database.Close()
		return nil, err
	}
	catalogEntities, catalogEdges, err := rebuildBranchSchemaCache(conn)
	if err != nil {
		conn.Close()
		database.Close()
		return nil, fmt.Errorf("rebuild persisted branch schema %q: %w", txID, err)
	}
	br.entityTypeDefs, br.edgeTypeDefs, err = restoreBranchSchemaMetadata(
		conn, db.branchMetadataPath(txID), catalogEntities, catalogEdges,
	)
	if err != nil {
		conn.Close()
		database.Close()
		return nil, fmt.Errorf("restore persisted branch schema %q: %w", txID, err)
	}
	db.branches[txID] = br
	return br, nil
}

func loadBranchExtensions(conn *lbug.Connection) error {
	for _, ext := range []string{"vector", "fts"} {
		// INSTALL is idempotent — it is safe to call on every Open (same as
		// main's loadExtensions). On some configurations the extension may
		// already be installed, so we ignore INSTALL errors and attempt LOAD
		// directly; the LOAD result is checked below.
		_, _ = conn.Query("INSTALL " + ext + ";")
		if _, err := conn.Query("LOAD " + ext + ";"); err != nil {
			return fmt.Errorf("load extension %q on branch: %w", ext, err)
		}
	}
	return nil
}

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

func vectorIndexesOnConn(conn *lbug.Connection) (map[string]bool, error) {
	indexes := make(map[string]bool)
	result, err := conn.Query("CALL show_indexes() RETURN *;")
	if err != nil {
		return indexes, nil
	}
	defer result.Close()
	for result.HasNext() {
		tuple, err := result.Next()
		if err != nil {
			return nil, err
		}
		values, err := tuple.GetAsSlice()
		tuple.Close()
		if err != nil {
			return nil, err
		}
		if len(values) >= 3 && strings.EqualFold(fmt.Sprint(values[2]), "hnsw") {
			indexes[fmt.Sprint(values[0])] = true
		}
	}
	return indexes, nil
}

// tablePropertiesOnConn reads a table's column definitions via table_info.
// Structural column skipping is table-kind-dependent (see getTableProperties):
// REL tables skip their from/to/type endpoint columns; vector-indexed NODE
// tables skip their embedding column. SPEC-valid entity properties named
// from/to/type (or embedding on a non-vector entity type) are real properties
// and are retained. vectorIndexed reports whether the NODE table carries an
// HNSW vector index (the embedding column and its index are bootstrapped
// together, so an index implies a structural embedding column).
func tablePropertiesOnConn(
	conn *lbug.Connection, table, tableType string, vectorIndexed bool,
) ([]store.PropertyDef, error) {
	result, err := conn.Query(fmt.Sprintf("CALL table_info('%s') RETURN *;", table))
	if err != nil {
		return nil, err
	}
	defer result.Close()
	skip := map[string]bool{"id": true}
	switch strings.ToUpper(tableType) {
	case "NODE":
		if vectorIndexed {
			skip["embedding"] = true
		}
	case "REL":
		skip["from"] = true
		skip["to"] = true
		skip["type"] = true
	}
	properties := []store.PropertyDef{}
	for result.HasNext() {
		tuple, err := result.Next()
		if err != nil {
			return nil, err
		}
		values, err := tuple.GetAsSlice()
		tuple.Close()
		if err != nil {
			return nil, err
		}
		if len(values) >= 3 && !skip[fmt.Sprint(values[1])] {
			properties = append(properties, store.PropertyDef{Name: fmt.Sprint(values[1]), Type: fmt.Sprint(values[2])})
		}
	}
	return properties, nil
}

// ReplicateSchemaToBranch applies the main DB's schema DDL to the branch.
func (db *ladybugDB) ReplicateSchemaToBranch(_ context.Context, txID string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	br, err := db.branchLocked(txID)
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
			if _, err := br.conn.Query(alterDDL); err != nil {
				return fmt.Errorf("replicate embedding column %q: %w", name, err)
			}
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
		metadata := metadataFromDefinitions(br.entityTypeDefs, br.edgeTypeDefs)
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

// RehydrateFromBranch replaces main DB data with the branch data.
// For in-memory mode we wipe main and bulk-insert from branch queries.
func (db *ladybugDB) RehydrateFromBranch(ctx context.Context, txID string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	br, err := db.branchLocked(txID)
	if err != nil {
		return err
	}
	br.mu.Lock()
	defer br.mu.Unlock()
	if br.failed {
		return store.ErrDatabaseNotReady
	}
	// Snapshot entity/edge defs before releasing lock for branch work.
	entDefs := make(map[string]*store.EntityTypeDef)
	maps.Copy(entDefs, br.entityTypeDefs)
	edgeDefs := make(map[string]*store.EdgeTypeDef)
	maps.Copy(edgeDefs, br.edgeTypeDefs)
	// Wipe all data from main.
	result, err := db.conn.Query("MATCH (n) DETACH DELETE n;")
	if err != nil {
		return fmt.Errorf("wipe main: %w", err)
	}
	result.Close()

	// Copy all entities from branch to main.
	for _, name := range sortedKeys(entDefs) {
		stmt, err := br.conn.Prepare(fmt.Sprintf("MATCH (n:%s) RETURN n;", quoteID(name)))
		if err != nil {
			return fmt.Errorf("query branch entities for %q: %w", name, err)
		}
		result, err := br.conn.Execute(stmt, map[string]any{})
		stmt.Close()
		if err != nil {
			return fmt.Errorf("execute branch query for %q: %w", name, err)
		}
		for result.HasNext() {
			tuple, err := result.Next()
			if err != nil {
				result.Close()
				return fmt.Errorf("read branch entity: %w", err)
			}
			m, err := tuple.GetAsMap()
			tuple.Close()
			if err != nil {
				result.Close()
				return fmt.Errorf("parse branch entity: %w", err)
			}
			node, ok := m["n"].(lbug.Node)
			if !ok {
				result.Close()
				return fmt.Errorf("branch entity of type %q: unexpected node type %T", name, m["n"])
			}
			entity := entityFromNode(node, name, entDefs[name].EnableVectorIndex)
			// Ensure main's embedding column / vector index exists before
			// inserting an entity that carries an embedding. The branch may have
			// bootstrapped the dimension (SPEC R7 lazy bootstrap on the first
			// embedding write), so main's table need not have the embedding
			// column yet — the copy path leads with an entity that targets it.
			if len(entity.Embedding) > 0 {
				if err := db.ensureEmbeddingLoadSchema(db.conn, name, entity.Embedding, db.entityTypeDefs); err != nil {
					result.Close()
					return fmt.Errorf("promote embedding schema to main for %q: %w", name, err)
				}
			}
			if err := insertEntityOnConn(db.conn, name, entity, entDefs); err != nil {
				result.Close()
				return fmt.Errorf("insert entity into main: %w", err)
			}
		}
		result.Close()
	}

	// Copy all edges from branch to main.
	for _, name := range sortedKeys(edgeDefs) {
		edges, err := listEdgesOnConn(br.conn, name)
		if err != nil {
			return fmt.Errorf("query branch edges for %q: %w", name, err)
		}
		for _, edge := range edges {
			if err := insertEdgeOnConn(db.conn, name, &edge); err != nil {
				return fmt.Errorf("insert edge into main: %w", err)
			}
		}
	}

	// Persist main's schema metadata (capturing the vector index/dimension the
	// copy path promoted to main above) so a reopen's
	// validateMetadataAgainstCatalog does not fail closed. A branch that
	// bootstrapped the embedding column/vector index on its first write (SPEC
	// R7 lazy bootstrap) promoted that vector state to main's catalog here, but
	// main's schema.json (written at the original ApplySchema) still records
	// VectorIndexes=false/VectorDimensions=0 for the type; without rewriting
	// it, restart bricks startup with a catalog/metadata mismatch.
	if err := db.persistMainVectorMetadataLocked(); err != nil {
		return err
	}

	return nil
}

// RehydrateMainFromFiles loads entities/edges from JSON files into main.
// It holds db.mu for the entire wipe-and-load cycle so that concurrent reads
// never observe partially reconstructed state.
func (db *ladybugDB) RehydrateMainFromFiles(ctx context.Context, entitiesDir, edgesDir string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if db.closed || db.failed {
		return store.ErrDatabaseNotReady
	}

	entDefs := make(map[string]*store.EntityTypeDef)
	maps.Copy(entDefs, db.entityTypeDefs)
	edgeDefs := make(map[string]*store.EdgeTypeDef)
	maps.Copy(edgeDefs, db.edgeTypeDefs)

	// Wipe everything — use db.conn directly since we hold db.mu.
	result, err := db.conn.Query("MATCH (n) DETACH DELETE n;")
	if err != nil {
		return fmt.Errorf("delete graph data: %w", err)
	}
	result.Close()

	// Read entities from JSON files.
	if err := db.loadEntitiesFromDir(entitiesDir, entDefs); err != nil {
		return err
	}
	// Fail if entities dir exists but edges dir does not (partial wipe).
	if _, entErr := os.Stat(entitiesDir); entErr == nil {
		if _, edgeErr := os.Stat(edgesDir); os.IsNotExist(edgeErr) {
			return fmt.Errorf("%w: edges directory does not exist but entities directory exists", store.ErrInvalidEdgeDir)
		}
	}
	// Read edges from JSON files.
	if err := db.loadEdgesFromDir(edgesDir, edgeDefs); err != nil {
		return err
	}
	// Promote the schema cache so any types inferred from the directory
	// structure (SPEC R8) are visible to subsequent reads and writes via
	// db.entityTypeDefs / db.edgeTypeDefs. The caches were loaded from a copy
	// above, so this merge also carries the pre-existing compiled defs back.
	db.entityTypeDefs = entDefs
	db.edgeTypeDefs = edgeDefs
	// Re-wire every cached structural handle (stale-structural-pointer rule,
	// LEARNINGS): db.edgePairs is consumed by ReplicateSchemaToBranch to
	// recreate branch rel tables with the same FROM/TO endpoints main's rel
	// tables carry. Types inferred from the directory structure (SPEC R8) have
	// no connection rules, so nothing but the catalog can rebuild this map —
	// without it the next BeginTransaction replicates a `_untyped`-placeholder
	// rel table to the branch (pairs := db.edgePairs[name] is nil) and every
	// branch edge is silently dropped against the mismatched endpoints.
	if err := db.rebuildEdgePairsLocked(); err != nil {
		return err
	}
	// Persist main's schema metadata (capturing the vector index/dimension the
	// file-load path promoted above), so a reopen's validateMetadataAgainstCatalog
	// does not fail closed with a catalog/metadata vector mismatch.
	if err := db.persistMainVectorMetadataLocked(); err != nil {
		return err
	}
	// A successful re-hydration leaves main serving a complete schema (applied
	// or inferred from the directory structure, SPEC R8). In the both-lost
	// recovery corner — corrupt main.lbug AND absent schema.json while the git
	// repo has commits — Open recovers a fresh database and
	// restoreMainSchemaMetadataLocked finds no metadata to restore, leaving
	// schemaApplied false; without this the store serves the recovered graph
	// while Health() reports SchemaApplied=false indefinitely (only ApplySchema
	// and restoreMainSchemaMetadataLocked set the flag elsewhere).
	db.schemaApplied = true
	return nil
}

// rebuildEdgePairsLocked re-wires db.edgePairs from the rel tables' actual
// FROM/TO endpoint pairs in the catalog. It runs on the re-hydration path
// (RehydrateMainFromFiles), which reassigns db.edgeTypeDefs and therefore must
// re-wire every cached structural handle that other paths read. The main load
// paths (ApplySchema, restoreMainSchemaMetadataLocked) derive edgePairs from
// connection rules, but types inferred from the directory structure (SPEC R8)
// carry no rules, so the catalog is the only authoritative source. An edgeless
// edge type's `_untyped` placeholder pair is normalized away (the key stays
// absent) so ReplicateSchemaToBranch's createRelTableOnConn takes its
// placeholder branch, which creates the `_untyped` NODE table the rel table's
// endpoint clause references. Callers must hold db.mu.
func (db *ladybugDB) rebuildEdgePairsLocked() error {
	pairs := make(map[string][]fromToPair)
	untyped := []fromToPair{{From: untypedTableName, To: untypedTableName}}
	for _, name := range sortedKeys(db.edgeTypeDefs) {
		actual, err := connectionPairsOnConn(db.conn, name)
		if err != nil {
			return fmt.Errorf("read relationship endpoints for %q: %w", name, err)
		}
		if equalFromToPairs(actual, untyped) {
			continue
		}
		pairs[name] = actual
	}
	db.edgePairs = pairs
	return nil
}

// HydrateBranchFromFiles loads entities/edges from JSON files into a branch.
func (db *ladybugDB) HydrateBranchFromFiles(ctx context.Context, txID, entitiesDir, edgesDir string) error {
	db.mu.Lock()
	br, err := db.branchLocked(txID)
	if err != nil {
		db.mu.Unlock()
		return err
	}
	br.mu.Lock()
	db.mu.Unlock()
	defer br.mu.Unlock()
	if br.failed {
		return store.ErrDatabaseNotReady
	}

	// Load from files into branch.
	if err := db.loadEntitiesFromDirOnConn(br.conn, entitiesDir, br.entityTypeDefs); err != nil {
		return err
	}
	// Fail if entities dir exists but edges dir does not (partial wipe) —
	// mirrors RehydrateMainFromFiles' completeness guard. On a working tree
	// where entities/ survived but edges/ was removed (SPEC R2 WipeGraph
	// mid-wipe failure → INTERNAL), silently loading entities and skipping
	// every edge would hydrate an incomplete graph with no signal.
	if _, entErr := os.Stat(entitiesDir); entErr == nil {
		if _, edgeErr := os.Stat(edgesDir); os.IsNotExist(edgeErr) {
			return fmt.Errorf("%w: edges directory does not exist but entities directory exists", store.ErrInvalidEdgeDir)
		}
	}
	if err := db.loadEdgesFromDirOnConn(br.conn, edgesDir, br.edgeTypeDefs); err != nil {
		return err
	}
	return nil
}

// DumpAllEntities returns all entities from a branch (or main if branch empty).
func (db *ladybugDB) DumpAllEntities(ctx context.Context, txID string) ([]store.Entity, error) {
	conn, typeDefs, unlock, err := db.lockForRead(txID)
	if err != nil {
		return nil, err
	}
	defer unlock()

	var results []store.Entity
	for _, name := range sortedKeys(typeDefs.entityTypeDefs) {
		q := fmt.Sprintf("MATCH (n:%s) RETURN n;", quoteID(name))
		result, err := conn.Query(q)
		if err != nil {
			return nil, fmt.Errorf("query entity type %q: %w", name, err)
		}
		for result.HasNext() {
			tuple, err := result.Next()
			if err != nil {
				result.Close()
				return nil, fmt.Errorf("read entity: %w", err)
			}
			m, err := tuple.GetAsMap()
			tuple.Close()
			if err != nil {
				result.Close()
				return nil, fmt.Errorf("parse entity: %w", err)
			}
			node, ok := m["n"].(lbug.Node)
			if !ok {
				result.Close()
				return nil, fmt.Errorf("dump entity type %q: unexpected node type %T", name, m["n"])
			}
			results = append(results, *entityFromNode(node, name, typeDefs.entityTypeDefs[name].EnableVectorIndex))
		}
		result.Close()
	}
	if results == nil {
		results = []store.Entity{}
	}
	return results, nil
}

// DumpAllEdges returns all edges from a branch (or main if branch empty).
func (db *ladybugDB) DumpAllEdges(ctx context.Context, txID string) ([]store.Edge, error) {
	conn, typeDefs, unlock, err := db.lockForRead(txID)
	if err != nil {
		return nil, err
	}
	defer unlock()

	var results []store.Edge
	for _, name := range sortedKeys(typeDefs.edgeTypeDefs) {
		edges, err := listEdgesOnConn(conn, name)
		if err != nil {
			return nil, fmt.Errorf("query edge type %q: %w", name, err)
		}
		results = append(results, edges...)
	}
	if results == nil {
		results = []store.Edge{}
	}
	return results, nil
}

// ListEntityTypes returns the entity type names known to a branch (or main).
func (db *ladybugDB) ListEntityTypes(txID string) ([]string, error) {
	_, typeDefs, unlock, err := db.lockForRead(txID)
	if err != nil {
		return nil, err
	}
	defer unlock()

	return sortedKeys(typeDefs.entityTypeDefs), nil
}

// --------------------------------------------------------------------------
// Internal helpers — table DDL on an arbitrary connection
// --------------------------------------------------------------------------

func createNodeTableOnConn(conn *lbug.Connection, name string,
	properties []store.PropertyDef) error {
	cols := make([]string, 0, 1+len(properties)+1)
	cols = append(cols, "id STRING PRIMARY KEY")
	stringProps := make([]string, 0, len(properties))
	for _, p := range properties {
		propertyType := ladybugType(p.Type)
		cols = append(cols, quoteID(p.Name)+" "+propertyType)
		if propertyType == colTypeString || propertyType == "STRING[]" {
			stringProps = append(stringProps, p.Name)
		}
	}
	// ponytail: embedding column and vector index are bootstrapped lazily
	// on first CreateEntity with an embedding; no FLOAT[n] column or index
	// is created at table creation time.
	ddl := fmt.Sprintf("CREATE NODE TABLE IF NOT EXISTS %s (%s);", quoteID(name), strings.Join(cols, ", "))
	if _, err := conn.Query(ddl); err != nil {
		return err
	}
	// Create FTS index on all string properties.
	if len(stringProps) > 0 {
		// Whether the table was freshly created or already existed (this builder
		// runs CREATE NODE TABLE IF NOT EXISTS on every table (re)creation at
		// hydration / schema-load), its FTS index — also not idempotent to
		// create — may already exist. Skip only when the index is known present,
		// so a genuine index-creation error propagates and cannot silently skip
		// the type's FullTextSearch coverage (query.go silently skips index-less
		// types), rather than error-matching the library's "already exists" text.
		if !ftsIndexExists(conn, name) {
			propsList := "'" + strings.Join(stringProps, "', '") + "'"
			ftsDDL := fmt.Sprintf("CALL CREATE_FTS_INDEX('%s', '%s_fts', [%s], stemmer := 'porter');",
				name, name, propsList)
			if _, err := conn.Query(ftsDDL); err != nil {
				return fmt.Errorf("create FTS index for %q: %w", name, err)
			}
		}
	}
	return nil
}

func createRelTableOnConn(conn *lbug.Connection, name string,
	properties []store.PropertyDef, pairs []fromToPair) error {
	var clauses []string
	if len(pairs) > 0 {
		for _, p := range pairs {
			clauses = append(clauses, fmt.Sprintf("FROM %s TO %s", quoteID(p.From), quoteID(p.To)))
		}
	} else {
		// Need at least one FROM/TO pair; create a placeholder _untyped node table.
		// The rel table's endpoint clauses reference this table by name, so a
		// failure here would otherwise surface only second-hand (and unreliably)
		// when the rel-table DDL references the missing table. Propagate it now.
		stmt := "CREATE NODE TABLE IF NOT EXISTS " + untypedTableName + " (id STRING PRIMARY KEY);"
		if _, err := conn.Query(stmt); err != nil {
			return fmt.Errorf("create placeholder %s node table: %w", untypedTableName, err)
		}
		clauses = append(clauses, "FROM "+untypedTableName+" TO "+untypedTableName)
	}

	cols := make([]string, 0, 2+len(properties)+1)
	cols = append(cols, strings.Join(clauses, ", "))
	cols = append(cols, "id STRING")
	for _, p := range properties {
		cols = append(cols, quoteID(p.Name)+" "+ladybugType(p.Type))
	}
	ddl := fmt.Sprintf("CREATE REL TABLE IF NOT EXISTS %s (%s);", quoteID(name), strings.Join(cols, ", "))
	_, err := conn.Query(ddl)
	return err
}

// --------------------------------------------------------------------------
// Internal helpers — insert/lookup on an arbitrary connection
// --------------------------------------------------------------------------

func insertEntityOnConn(
	conn *lbug.Connection, entityType string, entity *store.Entity,
	typeDefs map[string]*store.EntityTypeDef,
) error {
	// Cross-type ID-uniqueness probe, mirroring CreateEntity's data-integrity
	// check (crud.go): the id column is PRIMARY KEY only within each node
	// table, so an entity whose ID already exists under a different type's
	// table would otherwise insert silently on the re-hydration read path —
	// reachable from corrupt/hand-edited git state — after which findEntityByID
	// resolves the ID nondeterministically (map iteration order). Fail loudly
	// instead (never silently produce a wrong result on a read path). The
	// O(#entity_types) scan shares findEntityByID's ponytail ceiling (upgrade
	// path: a global ID→type index).
	if _, perr := findEntityByID(conn, typeDefs, entity.Id); perr == nil {
		return fmt.Errorf("%w: entity with id %q already exists", store.ErrEntityAlreadyExists, entity.Id)
	} else if !errors.Is(perr, store.ErrEntityNotFound) {
		return perr
	}
	var assigns []string
	params := map[string]any{"id": entity.Id}
	assigns = append(assigns, "id: $id")
	for k, v := range entity.Properties {
		pk := "p_" + k
		assigns = append(assigns, quoteID(k)+": $"+pk)
		params[pk] = v
	}
	if len(entity.Embedding) > 0 {
		assigns = append(assigns, "embedding: $embedding")
		embAny := make([]any, len(entity.Embedding))
		for i, v := range entity.Embedding {
			embAny[i] = v
		}
		params["embedding"] = embAny
	}
	q := fmt.Sprintf("CREATE (n:%s {%s});", quoteID(entityType), strings.Join(assigns, ", "))
	stmt, pErr := conn.Prepare(q)
	if pErr != nil {
		return pErr
	}
	_, eErr := conn.Execute(stmt, params)
	stmt.Close()
	return eErr
}

func insertEdgeOnConn(conn *lbug.Connection, edgeType string, edge *store.Edge) error {
	// Verify both endpoints exist before creating the edge. The
	// MATCH (a {id: $from}), (b {id: $to}) CREATE ... statement silently no-ops
	// when an endpoint matches nothing — creating no edge and no error — so an
	// edge whose source or target entity is absent would vanish from the graph
	// with no signal on the re-hydration read path. The load path fails loudly
	// on every other corruption (unparseable files, missing keys, type/directory
	// mismatch); an absent endpoint must fail loudly too (never silently drop a
	// row or swallow a not-exist on a read path).
	for _, endpoint := range []struct {
		id   string
		role string
	}{
		{edge.FromEntityID, "from"},
		{edge.ToEntityID, "to"},
	} {
		stmt, err := conn.Prepare("MATCH (n {id: $id}) RETURN n;")
		if err != nil {
			return fmt.Errorf("prepare edge %s endpoint lookup: %w", endpoint.role, err)
		}
		result, err := conn.Execute(stmt, map[string]any{"id": endpoint.id})
		stmt.Close()
		if err != nil {
			return fmt.Errorf("look up edge %s endpoint %q: %w", endpoint.role, endpoint.id, err)
		}
		found := result.HasNext()
		result.Close()
		if !found {
			return fmt.Errorf("%w: edge %q %s endpoint entity %q not found",
				store.ErrSourceOrTargetNotFound, edge.Id, endpoint.role, endpoint.id)
		}
	}
	relProps := make([]string, 0, 1+len(edge.Properties))
	params := map[string]any{"from": edge.FromEntityID, "to": edge.ToEntityID, "id": edge.Id}
	relProps = append(relProps, "id: $id")
	for k, v := range edge.Properties {
		pk := "p_" + k
		relProps = append(relProps, quoteID(k)+": $"+pk)
		params[pk] = v
	}
	q := fmt.Sprintf("MATCH (a {id: $from}), (b {id: $to}) CREATE (a)-[:%s {%s}]->(b);",
		quoteID(edgeType), strings.Join(relProps, ", "))
	stmt, pErr := conn.Prepare(q)
	if pErr != nil {
		return pErr
	}
	_, eErr := conn.Execute(stmt, params)
	stmt.Close()
	return eErr
}

func listEdgesOnConn(conn *lbug.Connection, edgeType string) ([]store.Edge, error) {
	q := fmt.Sprintf("MATCH (a)-[r:%s]->(b) RETURN a.id, r, b.id;", quoteID(edgeType))
	result, err := conn.Query(q)
	if err != nil {
		return nil, err
	}
	defer result.Close()

	var edges []store.Edge
	for result.HasNext() {
		tuple, err := result.Next()
		if err != nil {
			return nil, fmt.Errorf("read edge row: %w", err)
		}
		m, err := tuple.GetAsMap()
		tuple.Close()
		if err != nil {
			return nil, fmt.Errorf("parse edge row: %w", err)
		}
		fromID := fmt.Sprintf("%v", m["a.id"])
		rel, ok := m["r"].(lbug.Relationship)
		if !ok {
			return nil, fmt.Errorf("edge row for %q: unexpected relationship type %T", edgeType, m["r"])
		}
		toID := fmt.Sprintf("%v", m["b.id"])
		edges = append(edges, *edgeFromRel(rel, edgeType, fromID, toID))
	}
	if edges == nil {
		edges = []store.Edge{}
	}
	return edges, nil
}

// --------------------------------------------------------------------------
// Internal helpers — file loading
// --------------------------------------------------------------------------

// ensureEntityLoadSchema makes the entity table exist before entities are
// loaded from files. When the type is absent from the applied schema (including
// an empty applied schema), the table is inferred on demand from the directory
// structure (SPEC R8). It also (re)creates the FTS index on the type's string
// properties so re-hydration restores the full search state (SPEC R8).
func (db *ladybugDB) ensureEntityLoadSchema(
	conn *lbug.Connection, typeName, typeDir string, entDefs map[string]*store.EntityTypeDef,
) error {
	def, known := entDefs[typeName]
	var props []store.PropertyDef
	if known {
		props = def.Properties
	} else {
		// Infer the schema from the directory structure (SPEC R8): when the
		// type is not in the applied schema, scan its JSON files to collect
		// the union of all property names so the created table has a real
		// column for every property a file may set. Without this, a
		// property-bearing file builds `CREATE (n:Type {col: $v})` against a
		// non-existent column and re-hydration of that type cannot succeed.
		// ponytail: inferred property types are always "string" because the
		// file-per-element representation stores property values as strings.
		// If a future representation carries non-string values, the column
		// type inference here would need corresponding handling.
		names := make(map[string]bool)
		if files, err := db.readDir(typeDir); err == nil {
			for _, f := range files {
				if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
					continue
				}
				data, err := os.ReadFile(filepath.Join(typeDir, f.Name()))
				if err != nil {
					continue
				}
				var je struct {
					Properties map[string]string `json:"properties"`
				}
				if err := json.Unmarshal(data, &je); err != nil {
					continue
				}
				for k := range je.Properties {
					names[k] = true
				}
			}
		}
		for name := range names {
			// Store the proto type ("string"), not the catalog type (colTypeString):
			// PropertyDef.Type is the proto type everywhere else, and schema.json
			// persists it verbatim — validateSchemaMetadata reconstructs a proto
			// schema from it on reopen, and schema.Validate rejects any type other
			// than "string" (ErrInvalidPropertyType), which would brick the next
			// file-backed Open (SPEC R8 corruption-recovery flow). createNodeTableOnConn
			// maps it to the catalog type via ladybugType, so DDL is unaffected.
			props = append(props, store.PropertyDef{Name: name, Type: "string"})
		}
		slices.SortFunc(props, func(a, b store.PropertyDef) int {
			return strings.Compare(a.Name, b.Name)
		})
	}
	if err := createNodeTableOnConn(conn, typeName, props); err != nil {
		return fmt.Errorf("create entity table %q for load: %w", typeName, err)
	}
	if !known {
		// Infer schema from the directory structure: register the type so
		// subsequent files of the same type are treated as known.
		entDefs[typeName] = &store.EntityTypeDef{Name: typeName, Properties: props}
	}
	return nil
}

// ensureEdgeLoadSchema makes the rel table exist before edges are loaded from
// files. When the type is absent from the applied schema (including an empty
// applied schema), the table is inferred on demand from the directory structure
// (SPEC R8), mirroring
// ensureEntityLoadSchema: the union of property names across the type's JSON
// files becomes real columns so a property-bearing file's
// `CREATE (a)-[:T {col: $v}]->(b)` does not target a non-existent column. The
// FROM/TO endpoint pairs are inferred by resolving each edge file's from/to
// entity IDs to their node labels — entities are loaded before edges on both
// the main and branch paths, so the endpoint tables already exist. This is
// required, not cosmetic: a rel table whose FROM/TO clauses do not name the
// endpoint node types silently drops the CREATE (no error, no edge), so the
// _untyped placeholder fallback would lose every inferred edge.
// ponytail: inferred property types are always "string" (same rationale as
// ensureEntityLoadSchema). If a future file representation carries non-string
// property values, inference here would need corresponding handling.
func (db *ladybugDB) ensureEdgeLoadSchema(
	conn *lbug.Connection, typeName, typeDir string, edgeDefs map[string]*store.EdgeTypeDef,
) error {
	if _, known := edgeDefs[typeName]; known {
		// The rel table already exists (created by ApplySchema or restored from
		// schema metadata); only types absent from the applied schema need
		// inference.
		return nil
	}
	names := make(map[string]bool)
	pairs := make(map[string]fromToPair) // "from|to" -> pair
	if files, err := db.readDir(typeDir); err == nil {
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(typeDir, f.Name()))
			if err != nil {
				continue
			}
			var je struct {
				From       string            `json:"from"`
				To         string            `json:"to"`
				Properties map[string]string `json:"properties"`
			}
			if err := json.Unmarshal(data, &je); err != nil {
				continue
			}
			for k := range je.Properties {
				names[k] = true
			}
			if je.From == "" || je.To == "" {
				continue
			}
			fromType, err := nodeLabelOnConn(conn, je.From)
			if err != nil {
				return fmt.Errorf("resolve edge %q from endpoint %q: %w", typeName, je.From, err)
			}
			toType, err := nodeLabelOnConn(conn, je.To)
			if err != nil {
				return fmt.Errorf("resolve edge %q to endpoint %q: %w", typeName, je.To, err)
			}
			pairs[fromType+"|"+toType] = fromToPair{From: fromType, To: toType}
		}
	}
	var props []store.PropertyDef
	for name := range names {
		// Store the proto type ("string"), not the catalog type (colTypeString):
		// PropertyDef.Type is the proto type everywhere else, and schema.json
		// persists it verbatim (see ensureEntityLoadSchema for the full
		// rationale). createRelTableOnConn maps it to the catalog type via
		// ladybugType, so DDL is unaffected.
		props = append(props, store.PropertyDef{Name: name, Type: "string"})
	}
	slices.SortFunc(props, func(a, b store.PropertyDef) int {
		return strings.Compare(a.Name, b.Name)
	})
	var pairList []fromToPair
	for _, p := range pairs {
		pairList = append(pairList, p)
	}
	slices.SortFunc(pairList, func(a, b fromToPair) int {
		if c := strings.Compare(a.From, b.From); c != 0 {
			return c
		}
		return strings.Compare(a.To, b.To)
	})
	if err := createRelTableOnConn(conn, typeName, props, pairList); err != nil {
		return fmt.Errorf("create edge table %q for load: %w", typeName, err)
	}
	// Infer schema from the directory structure: register the type so
	// subsequent files of the same type are treated as known.
	edgeDefs[typeName] = &store.EdgeTypeDef{Name: typeName, Properties: props}
	return nil
}

// nodeLabelOnConn resolves an entity ID to its node label (type name). The
// entity tables are created before edge tables on both the main and branch
// file-load paths, so the node is findable here.
func nodeLabelOnConn(conn *lbug.Connection, id string) (string, error) {
	stmt, err := conn.Prepare("MATCH (n {id: $id}) RETURN n;")
	if err != nil {
		return "", err
	}
	result, err := conn.Execute(stmt, map[string]any{"id": id})
	stmt.Close()
	if err != nil {
		return "", err
	}
	defer result.Close()
	if !result.HasNext() {
		return "", fmt.Errorf("endpoint entity %q not found", id)
	}
	tuple, err := result.Next()
	if err != nil {
		return "", err
	}
	m, err := tuple.GetAsMap()
	tuple.Close()
	if err != nil {
		return "", err
	}
	node, ok := m["n"].(lbug.Node)
	if !ok {
		return "", fmt.Errorf("endpoint entity %q: unexpected node type %T", id, m["n"])
	}
	return node.Label, nil
}

// ensureEmbeddingLoadSchema adds the embedding column (and vector index) to the
// entity table when an entity carrying an embedding is loaded. The dimension is
// taken from the first embedding seen for the type. It also marks the type's
// definition as vector-enabled in defs so the in-memory model stays in parity
// with the column/index it creates during re-hydration.
func (db *ladybugDB) ensureEmbeddingLoadSchema(
	conn *lbug.Connection, typeName string, embedding []float32,
	defs map[string]*store.EntityTypeDef,
) error {
	vectorIndexed := false
	if def, ok := defs[typeName]; ok {
		vectorIndexed = def.EnableVectorIndex
	}
	if dim, derr := getEmbeddingDimension(conn, typeName, vectorIndexed); derr != nil {
		return fmt.Errorf("read embedding dimension for %q: %w", typeName, derr)
	} else if dim > 0 {
		return nil
	}
	altDDL := fmt.Sprintf("ALTER TABLE %s ADD embedding FLOAT[%d];", quoteID(typeName), len(embedding))
	if _, err := conn.Query(altDDL); err != nil {
		return fmt.Errorf("ensure embedding column %q: %w", typeName, err)
	}
	if err := db.createVectorIndex(conn, typeName); err != nil {
		return fmt.Errorf("ensure vector index %q: %w", typeName, err)
	}
	// Keep the definition in parity with the column/index just created: a type
	// whose schema def stays EnableVectorIndex=false while the table now carries
	// an embedding column and vector index would diverge from the metadata model
	// (captureVectorState/ValidateMetadataAgainstCatalog require VectorIndexes to
	// match def.EnableVectorIndex) and from the query path (SearchNeighbors reads
	// EnableVectorIndex to decide which types are searchable). Re-hydration is the
	// only path that can create a vector index without a type first declared with
	// EnableVectorIndex=true (inferred directory schema, SPEC R8), so we promote
	// the flag here on the file-load path to keep the def consistent with the
	// database.
	if def, ok := defs[typeName]; ok {
		def.EnableVectorIndex = true
	}
	return nil
}

func (db *ladybugDB) loadEntitiesFromDir(dir string, entDefs map[string]*store.EntityTypeDef) error {
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %q is not a directory", store.ErrInvalidEntityDir, dir)
	}
	entries, err := db.readDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		typeName := entry.Name()
		// No schema-absent skip: a type directory present in the git
		// file-per-element representation but absent from the applied schema is
		// inferred from the directory structure by ensureEntityLoadSchema so
		// re-hydration recovers the full graph state (SPEC R8). Silently
		// skipping committed files would drop rows on the read path.
		typeDir := filepath.Join(dir, typeName)
		if err := db.ensureEntityLoadSchema(db.conn, typeName, typeDir, entDefs); err != nil {
			return err
		}
		files, err := db.readDir(typeDir)
		if err != nil {
			return fmt.Errorf("read entities dir %q: %w", typeDir, err)
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(typeDir, f.Name()))
			if err != nil {
				return fmt.Errorf("read entity file %q: %w", filepath.Join(typeDir, f.Name()), err)
			}
			var je struct {
				ID         string            `json:"id"`
				Type       string            `json:"type"`
				Properties map[string]string `json:"properties"`
				Embedding  []float32         `json:"embedding"`
			}
			if err := json.Unmarshal(data, &je); err != nil {
				return fmt.Errorf("%w: unparseable entity file %q: %v",
					store.ErrInvalidEntityDir, filepath.Join(typeDir, f.Name()), err)
			}
			if je.Type == "" {
				return fmt.Errorf("%w: entity file %q is missing required key 'type'",
					store.ErrInvalidEntityDir, filepath.Join(typeDir, f.Name()))
			}
			if je.ID == "" {
				return fmt.Errorf("%w: entity file %q is missing required key 'id'",
					store.ErrInvalidEntityDir, filepath.Join(typeDir, f.Name()))
			}
			if len(je.Embedding) > 0 {
				if err := db.ensureEmbeddingLoadSchema(db.conn, typeName, je.Embedding, entDefs); err != nil {
					return err
				}
			}
			if je.Type != typeName {
				return fmt.Errorf("%w: entity file %q declares type %q but is stored under directory %q",
					store.ErrInvalidEntityDir, filepath.Join(typeDir, f.Name()), je.Type, typeName)
			}
			props := je.Properties
			if props == nil {
				props = make(map[string]string)
			}
			entity := &store.Entity{
				Id: je.ID, Type: je.Type, Properties: props,
				Embedding: je.Embedding,
				CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			}
			if err := insertEntityOnConn(db.conn, typeName, entity, entDefs); err != nil {
				return fmt.Errorf("insert entity %q: %w", je.ID, err)
			}
		}
	}
	return nil
}

func (db *ladybugDB) loadEdgesFromDir(dir string, edgeDefs map[string]*store.EdgeTypeDef) error {
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %q is not a directory", store.ErrInvalidEdgeDir, dir)
	}
	entries, err := db.readDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		typeName := entry.Name()
		// No schema-absent skip: a type directory present in the git
		// file-per-element representation but absent from the applied schema is
		// inferred from the directory structure by ensureEdgeLoadSchema so
		// re-hydration recovers the full graph state (SPEC R8). Silently
		// skipping committed files would drop rows on the read path.
		typeDir := filepath.Join(dir, typeName)
		if err := db.ensureEdgeLoadSchema(db.conn, typeName, typeDir, edgeDefs); err != nil {
			return err
		}
		files, err := db.readDir(typeDir)
		if err != nil {
			return fmt.Errorf("read edges dir %q: %w", typeDir, err)
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(typeDir, f.Name()))
			if err != nil {
				return fmt.Errorf("read edge file %q: %w", filepath.Join(typeDir, f.Name()), err)
			}
			var je struct {
				ID         string            `json:"id"`
				Type       string            `json:"type"`
				From       string            `json:"from"`
				To         string            `json:"to"`
				Properties map[string]string `json:"properties"`
			}
			if err := json.Unmarshal(data, &je); err != nil {
				return fmt.Errorf("%w: unparseable edge file %q: %v",
					store.ErrInvalidEdgeDir, filepath.Join(typeDir, f.Name()), err)
			}
			if je.Type == "" || je.From == "" || je.To == "" {
				return fmt.Errorf("%w: edge file %q is missing required keys (type, from, to)",
					store.ErrInvalidEdgeDir, filepath.Join(typeDir, f.Name()))
			}
			if je.ID == "" {
				return fmt.Errorf("%w: edge file %q is missing required key 'id'",
					store.ErrInvalidEdgeDir, filepath.Join(typeDir, f.Name()))
			}
			if je.Type != typeName {
				return fmt.Errorf("%w: edge file %q declares type %q but is stored under directory %q",
					store.ErrInvalidEdgeDir, filepath.Join(typeDir, f.Name()), je.Type, typeName)
			}
			props := je.Properties
			if props == nil {
				props = make(map[string]string)
			}
			edge := &store.Edge{
				Id: je.ID, Type: je.Type,
				FromEntityID: je.From, ToEntityID: je.To,
				Properties: props,
				CreatedAt:  time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			}
			if err := insertEdgeOnConn(db.conn, typeName, edge); err != nil {
				return fmt.Errorf("insert edge %q: %w", je.ID, err)
			}
		}
	}
	return nil
}

func (db *ladybugDB) loadEntitiesFromDirOnConn(conn *lbug.Connection, dir string,
	entDefs map[string]*store.EntityTypeDef) error {
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %q is not a directory", store.ErrInvalidEntityDir, dir)
	}
	entries, err := db.readDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		typeName := entry.Name()
		// No schema-absent skip: a type directory present in the git
		// file-per-element representation but absent from the applied schema is
		// inferred from the directory structure by ensureEntityLoadSchema so
		// re-hydration recovers the full graph state (SPEC R8). Silently
		// skipping committed files would drop rows on the read path.
		typeDir := filepath.Join(dir, typeName)
		if err := db.ensureEntityLoadSchema(conn, typeName, typeDir, entDefs); err != nil {
			return err
		}
		files, err := db.readDir(typeDir)
		if err != nil {
			return fmt.Errorf("read entities dir %q: %w", typeDir, err)
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(typeDir, f.Name()))
			if err != nil {
				return fmt.Errorf("read entity file %q: %w", filepath.Join(typeDir, f.Name()), err)
			}
			var je struct {
				ID         string            `json:"id"`
				Type       string            `json:"type"`
				Properties map[string]string `json:"properties"`
				Embedding  []float32         `json:"embedding"`
			}
			if err := json.Unmarshal(data, &je); err != nil {
				return fmt.Errorf("%w: unparseable entity file %q: %v",
					store.ErrInvalidEntityDir, filepath.Join(typeDir, f.Name()), err)
			}
			if je.Type == "" {
				return fmt.Errorf("%w: entity file %q is missing required key 'type'",
					store.ErrInvalidEntityDir, filepath.Join(typeDir, f.Name()))
			}
			if je.ID == "" {
				return fmt.Errorf("%w: entity file %q is missing required key 'id'",
					store.ErrInvalidEntityDir, filepath.Join(typeDir, f.Name()))
			}
			if len(je.Embedding) > 0 {
				if err := db.ensureEmbeddingLoadSchema(conn, typeName, je.Embedding, entDefs); err != nil {
					return err
				}
			}
			if je.Type != typeName {
				return fmt.Errorf("%w: entity file %q declares type %q but is stored under directory %q",
					store.ErrInvalidEntityDir, filepath.Join(typeDir, f.Name()), je.Type, typeName)
			}
			props := je.Properties
			if props == nil {
				props = make(map[string]string)
			}
			entity := &store.Entity{
				Id: je.ID, Type: je.Type, Properties: props,
				Embedding: je.Embedding,
				CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			}
			if err := insertEntityOnConn(conn, typeName, entity, entDefs); err != nil {
				return fmt.Errorf("insert entity %q on branch: %w", je.ID, err)
			}
		}
	}
	return nil
}

func (db *ladybugDB) loadEdgesFromDirOnConn(conn *lbug.Connection, dir string,
	edgeDefs map[string]*store.EdgeTypeDef) error {
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %q is not a directory", store.ErrInvalidEdgeDir, dir)
	}
	entries, err := db.readDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		typeName := entry.Name()
		// No schema-absent skip: a type directory present in the git
		// file-per-element representation but absent from the applied schema is
		// inferred from the directory structure by ensureEdgeLoadSchema so
		// re-hydration recovers the full graph state (SPEC R8). Silently
		// skipping committed files would drop rows on the read path.
		typeDir := filepath.Join(dir, typeName)
		if err := db.ensureEdgeLoadSchema(conn, typeName, typeDir, edgeDefs); err != nil {
			return err
		}
		files, err := db.readDir(typeDir)
		if err != nil {
			return fmt.Errorf("read edges dir %q: %w", typeDir, err)
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(typeDir, f.Name()))
			if err != nil {
				return fmt.Errorf("read edge file %q: %w", filepath.Join(typeDir, f.Name()), err)
			}
			var je struct {
				ID         string            `json:"id"`
				Type       string            `json:"type"`
				From       string            `json:"from"`
				To         string            `json:"to"`
				Properties map[string]string `json:"properties"`
			}
			if err := json.Unmarshal(data, &je); err != nil {
				return fmt.Errorf("%w: unparseable edge file %q: %v",
					store.ErrInvalidEdgeDir, filepath.Join(typeDir, f.Name()), err)
			}
			if je.Type == "" || je.From == "" || je.To == "" {
				return fmt.Errorf("%w: edge file %q is missing required keys",
					store.ErrInvalidEdgeDir, filepath.Join(typeDir, f.Name()))
			}
			if je.ID == "" {
				return fmt.Errorf("%w: edge file %q is missing required key 'id'",
					store.ErrInvalidEdgeDir, filepath.Join(typeDir, f.Name()))
			}
			if je.Type != typeName {
				return fmt.Errorf("%w: edge file %q declares type %q but is stored under directory %q",
					store.ErrInvalidEdgeDir, filepath.Join(typeDir, f.Name()), je.Type, typeName)
			}
			props := je.Properties
			if props == nil {
				props = make(map[string]string)
			}
			edge := &store.Edge{
				Id: je.ID, Type: je.Type,
				FromEntityID: je.From, ToEntityID: je.To,
				Properties: props,
				CreatedAt:  time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			}
			if err := insertEdgeOnConn(conn, typeName, edge); err != nil {
				return fmt.Errorf("insert edge %q on branch: %w", je.ID, err)
			}
		}
	}
	return nil
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
