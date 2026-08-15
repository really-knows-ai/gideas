package ladybug

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

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

	if err := loadExtensionsOnConn(conn, "on branch"); err != nil {
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

// CloseBranchDB closes and deregisters a branch database without removing its
// persisted files. The connection close checkpoints the engine's write-ahead
// log into the branch's `.lbug` file, so the file is fully materialised before
// the service renames it onto a transaction's canonical name (the
// RefreshTransaction branch-DB swap, SPEC R9) — closing the crash window in
// which the swapped-in `.lbug` was missing un-checkpointed rows still held in
// the orphaned WAL. Idempotent: closing an unregistered branch is a no-op.
func (db *ladybugDB) CloseBranchDB(_ context.Context, txID string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if filepath.Base(txID) != txID || txID == "." || txID == ".." {
		return fmt.Errorf("invalid branch ID %q", txID)
	}
	br, ok := db.branches[txID]
	if !ok {
		return nil
	}
	br.mu.Lock()
	if br.conn != nil {
		br.conn.Close()
	}
	if br.db != nil {
		br.db.Close()
	}
	br.mu.Unlock()
	delete(db.branches, txID)
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
		// SPEC R9 recovery point 4 ("If the branch .lbug file itself is absent
		// (e.g., PVC corruption), that transaction is rolled back") covers branch
		// loss; a present-but-corrupt branch .lbug is the same loss mechanism and
		// must not wedge startup either — without this, RecoverOpenTransactions
		// propagates the open failure and main.go exits (a crash loop) until a
		// human deletes the file. Mirror main's R8 corruption classification
		// (corruptionCandidates, ladybug.go): a present, OS-readable file the
		// engine cannot open is corruption → classify as ErrBranchNotFound so
		// recovery rolls the transaction back (cleanupTransaction → DropBranchDB
		// removes the corrupt file). An unreadable file (permission/IO) is an
		// operational failure, not corruption — propagate the hard error instead
		// of touching the file. The readability probe is the same heuristic as
		// main's (see corruptionCandidates' ponytail), with a narrower blast
		// radius: a false positive rolls back one transaction whose uncommitted
		// changes were already unreachable through the unopenable branch DB,
		// never main.
		if corruptionCandidates(path) {
			return nil, fmt.Errorf("%w: branch for tx %q", store.ErrBranchNotFound, txID)
		}
		return nil, fmt.Errorf("open persisted branch %q: %w", txID, err)
	}
	conn, err := lbug.OpenConnection(database)
	if err != nil {
		database.Close()
		return nil, fmt.Errorf("open persisted branch connection %q: %w", txID, err)
	}
	br := &branchDB{db: database, conn: conn}
	if err := loadExtensionsOnConn(conn, "on branch"); err != nil {
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
		// SPEC R9 recovery point 4 rolls back a transaction whose branch .lbug
		// is absent; this closes the sibling crash windows around the branch
		// schema metadata. ReplicateSchemaToBranch writes
		// branches/<txID>.schema.json only after its DDL loop, so a crash at
		// any point before that write leaves the file absent with an empty
		// catalog (crash between CreateBranchDB and ReplicateSchemaToBranch)
		// or a partial catalog (crash inside the DDL loop after ≥1 table was
		// created). In both windows the branch is incomplete and the client
		// never received the txID — the BeginTransaction response is sent only
		// after ReplicateSchemaToBranch's metadata write succeeds — so the
		// transaction is provably harmless and is classified exactly like the
		// absent-.lbug case (ErrBranchNotFound → RecoverOpenTransactions rolls
		// the transaction back via cleanupTransaction/DropBranchDB) instead of
		// surfacing a hard error that bricks startup. A present-but-corrupt
		// metadata file stays a loud failure (genuine state loss, mirroring
		// restoreMainSchemaMetadataLocked): this guard matches only the
		// not-exist read error, so a present file that fails to parse still
		// propagates as a hard error.
		if errors.Is(err, os.ErrNotExist) {
			conn.Close()
			database.Close()
			return nil, fmt.Errorf("%w: branch for tx %q", store.ErrBranchNotFound, txID)
		}
		conn.Close()
		database.Close()
		return nil, fmt.Errorf("restore persisted branch schema %q: %w", txID, err)
	}
	db.branches[txID] = br
	return br, nil
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
		// The CREATE below targets main's rel table, so main's FROM/TO endpoint
		// clauses (SPEC R2, fixed at CREATE time) are the labels the endpoint
		// probe must accept.
		pairs, err := connectionPairsOnConn(db.conn, name)
		if err != nil {
			return fmt.Errorf("read relationship endpoints for %q: %w", name, err)
		}
		edges, err := listEdgesOnConn(br.conn, name)
		if err != nil {
			return fmt.Errorf("query branch edges for %q: %w", name, err)
		}
		for _, edge := range edges {
			if err := insertEdgeOnConn(db.conn, name, pairs, &edge); err != nil {
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
// never observe partially reconstructed state. The load is atomic with respect
// to the source: the entire file tree is validated into a throwaway database
// before the DETACH DELETE below, so a corrupt source (e.g. a corrupt merged
// JSON from the remote) fails with main untouched instead of wiping main first
// and leaving it partially loaded.
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

	// Pre-flight: prove the entire file tree loads cleanly before main is
	// touched. The wipe below precedes the loads, so without this a failure
	// during the loads — the persistent case being a corrupt merged JSON pulled
	// by the sync worker — leaves main.lbug partially wiped, and the caller's
	// cycle returns with main serving a silently-incomplete graph (SPEC
	// error-table row "Sync re-hydration failed" → INTERNAL; its R8
	// "automatic recovery on next startup" escape hatch presupposes a
	// consistent graph to serve, which a wiped main is not). The pre-flight
	// runs the shared loaders against a throwaway in-memory database, so a
	// corrupt source fails here with main untouched and the caller's recovery
	// path (the worker's next-cycle retry, or R8 on the next startup) has the
	// pre-existing graph to keep serving.
	if err := db.validateRehydrateSource(entitiesDir, edgesDir); err != nil {
		return err
	}

	// Wipe everything — use db.conn directly since we hold db.mu.
	result, err := db.conn.Query("MATCH (n) DETACH DELETE n;")
	if err != nil {
		return fmt.Errorf("delete graph data: %w", err)
	}
	result.Close()

	// Read entities from JSON files.
	if err := db.loadEntitiesFromDirOnConn(db.conn, entitiesDir, entDefs); err != nil {
		return err
	}
	// Fail if entities dir exists but edges dir does not (partial wipe).
	if err := checkEdgesDirCompleteness(entitiesDir, edgesDir); err != nil {
		return err
	}
	// Read edges from JSON files.
	if err := db.loadEdgesFromDirOnConn(db.conn, edgesDir, edgeDefs); err != nil {
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
	if err := checkEdgesDirCompleteness(entitiesDir, edgesDir); err != nil {
		return err
	}
	if err := db.loadEdgesFromDirOnConn(br.conn, edgesDir, br.edgeTypeDefs); err != nil {
		return err
	}
	// Persist the branch schema metadata AFTER hydration. ReplicateSchemaToBranch
	// writes branches/<txID>.schema.json before hydration runs, so any types and
	// FROM/TO pairs inferred from the directory structure (SPEC R8) here are absent
	// from that record. Without this rewrite, a crash + restart reopens the branch
	// via branchLocked → restoreBranchSchemaMetadata, whose
	// validateMetadataAgainstCatalog fails hard ("database entity type X is absent
	// from schema metadata") on the inferred types, and RecoverOpenTransactions
	// treats that non-ErrBranchNotFound error as a hard startup failure instead of
	// rolling back the one affected branch. The pairs come from the branch rel
	// tables' actual endpoints (mirroring RehydrateMainFromFiles'
	// rebuildEdgePairsLocked), so rule-less inferred edge types round-trip the
	// catalog comparison on reopen. The block runs only after every load succeeds:
	// a failed hydration leaves the persisted record untouched.
	if db.path != "" {
		pairs, perr := connectionEdgePairs(br.conn, br.edgeTypeDefs)
		if perr != nil {
			br.failed = true
			return fmt.Errorf("capture relationship endpoints: %w", perr)
		}
		metadata := metadataFromDefinitions(br.entityTypeDefs, br.edgeTypeDefs, pairs)
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

// DumpAllEntities returns all entities from a branch (or main if branch empty).
func (db *ladybugDB) DumpAllEntities(ctx context.Context, txID string) ([]store.Entity, error) {
	return dumpAll(db, txID,
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
func (db *ladybugDB) DumpAllEdges(ctx context.Context, txID string) ([]store.Edge, error) {
	return dumpAll(db, txID,
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
