package ladybug

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	lbug "github.com/LadybugDB/go-ladybug"
	"github.com/foundry/flow/cartographer/internal/store"
	"github.com/google/uuid"
)

// --------------------------------------------------------------------------
// Branch lifecycle
// --------------------------------------------------------------------------

// CreateBranchDB opens a new LadybugDB for the given txID. File-backed stores
// persist branches under branches/<txID>.lbug; in-memory stores remain ephemeral.
func (db *ladybugDB) CreateBranchDB(txID string) error {
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
func (db *ladybugDB) DropBranchDB(txID string) error {
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
		properties, err := tablePropertiesOnConn(conn, name)
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

func tablePropertiesOnConn(conn *lbug.Connection, table string) ([]store.PropertyDef, error) {
	result, err := conn.Query(fmt.Sprintf("CALL table_info('%s') RETURN *;", table))
	if err != nil {
		return nil, err
	}
	defer result.Close()
	skip := map[string]bool{"id": true, "_properties": true, "embedding": true, "from": true, "to": true, "type": true}
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
func (db *ladybugDB) ReplicateSchemaToBranch(txID string) error {
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
		dimension := getEmbeddingDimension(db.conn, name)
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
				continue
			}
			entity := entityFromNode(node, name)
			if err := insertEntityOnConn(db.conn, name, entity); err != nil {
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
				continue
			}
			results = append(results, *entityFromNode(node, name))
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
	cols = append(cols, "_properties STRING")
	// ponytail: embedding column and vector index are bootstrapped lazily
	// on first CreateEntity with an embedding; no FLOAT[n] column or index
	// is created at table creation time.
	ddl := fmt.Sprintf("CREATE NODE TABLE IF NOT EXISTS %s (%s);", quoteID(name), strings.Join(cols, ", "))
	if _, err := conn.Query(ddl); err != nil {
		return err
	}
	// Create FTS index on all string properties.
	if len(stringProps) > 0 {
		propsList := "'" + strings.Join(stringProps, "', '") + "'"
		ftsDDL := fmt.Sprintf("CALL CREATE_FTS_INDEX('%s', '%s_fts', [%s], stemmer := 'porter');",
			name, name, propsList)
		_, _ = conn.Query(ftsDDL) // non-fatal
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
		_, _ = conn.Query("CREATE NODE TABLE IF NOT EXISTS _untyped (id STRING PRIMARY KEY, _properties STRING);")
		clauses = append(clauses, "FROM _untyped TO _untyped")
	}

	cols := make([]string, 0, 2+len(properties)+1)
	cols = append(cols, strings.Join(clauses, ", "))
	cols = append(cols, "id STRING")
	for _, p := range properties {
		cols = append(cols, quoteID(p.Name)+" "+ladybugType(p.Type))
	}
	cols = append(cols, "_properties STRING")
	ddl := fmt.Sprintf("CREATE REL TABLE IF NOT EXISTS %s (%s);", quoteID(name), strings.Join(cols, ", "))
	_, err := conn.Query(ddl)
	return err
}

// --------------------------------------------------------------------------
// Internal helpers — insert/lookup on an arbitrary connection
// --------------------------------------------------------------------------

func insertEntityOnConn(conn *lbug.Connection, entityType string, entity *store.Entity) error {
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
			continue
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
		if _, ok := entDefs[typeName]; !ok && len(entDefs) > 0 {
			continue
		}
		typeDir := filepath.Join(dir, typeName)
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
				je.ID = uuid.New().String()
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
			if err := insertEntityOnConn(db.conn, typeName, entity); err != nil {
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
		if _, ok := edgeDefs[typeName]; !ok && len(edgeDefs) > 0 {
			continue
		}
		typeDir := filepath.Join(dir, typeName)
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
				je.ID = uuid.New().String()
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
		if _, ok := entDefs[typeName]; !ok && len(entDefs) > 0 {
			continue
		}
		typeDir := filepath.Join(dir, typeName)
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
				je.ID = uuid.New().String()
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
			if err := insertEntityOnConn(conn, typeName, entity); err != nil {
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
		if _, ok := edgeDefs[typeName]; !ok && len(edgeDefs) > 0 {
			continue
		}
		typeDir := filepath.Join(dir, typeName)
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
				je.ID = uuid.New().String()
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
