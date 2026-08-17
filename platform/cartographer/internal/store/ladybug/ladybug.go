// Package ladybug implements the store.Store interface backed by a real
// LadybugDB C-library database via github.com/LadybugDB/go-ladybug.
package ladybug

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	lbug "github.com/LadybugDB/go-ladybug"
	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// ladybugDB is the concrete LadybugDB-backed implementation of store.Store.
//
// Its method set is owned by five cohesive sub-structs — one per responsibility
// — that ladybugDB embeds: schemaManager (schema application/DDL, metadata
// persistence, and vector-state management: schema.go, metadata.go, vector.go),
// crudAccessor (entity/edge CRUD and edge-rule validation: crud.go),
// branchLifecycle (branch-DB lifecycle, schema replication to branches, and
// branch transaction state: branch_lifecycle.go, branch_schema.go,
// transaction_state.go), rehydrator (file-tree re-hydration and dump/scan:
// rehydrate.go, dump.go), and queryEngine (Cypher/search/listing queries:
// query.go). Each sub-struct reaches the shared state (locks, connections,
// type-def caches, branch registries) through its db pointer back to this
// struct; embedded promotion keeps ladybugDB implementing the full store.Store
// interface with public behaviour unchanged.
type ladybugDB struct {
	*schemaManager
	*crudAccessor
	*branchLifecycle
	*rehydrator
	*queryEngine

	mu     sync.Mutex
	path   string
	db     *lbug.Database
	conn   *lbug.Connection
	closed bool
	failed bool

	stageMetadata     func(string, schemaMetadata) (string, error)
	publishMetadata   func(string, string) error
	writeMetadata     func(string, schemaMetadata) error
	createVectorIndex func(*lbug.Connection, string) error
	readDir           func(string) ([]os.DirEntry, error)

	// Schema cache (rebuilt from catalog on Open)
	entityTypeDefs map[string]*store.EntityTypeDef
	edgeTypeDefs   map[string]*store.EdgeTypeDef

	// ruleIndex maps entity type name -> connection rules for edge validation.
	ruleIndex map[string][]*flowv1.ConnectionRule

	// edgePairs stores the FROM/TO pairs for each edge type, populated during
	// ApplySchema and used when rehydrating edge tables.
	edgePairs map[string][]fromToPair

	// schemaApplied records whether a schema has been applied (or restored from
	// persisted metadata). An empty entityTypes/edgeTypes schema is valid (SPEC
	// R1), so the type-count maps are insufficient to distinguish "no schema"
	// from "applied empty schema" — this flag is authoritative for Health.
	schemaApplied bool

	// Branches (txID -> branchDB)
	branches     map[string]*branchDB
	branchStates map[string]store.BranchTransactionState
}

// branchDB holds a real LadybugDB connection for an isolated branch.
type branchDB struct {
	mu             sync.Mutex
	db             *lbug.Database
	conn           *lbug.Connection
	entityTypeDefs map[string]*store.EntityTypeDef
	edgeTypeDefs   map[string]*store.EdgeTypeDef
	failed         bool
}

// Compile-time interface check.
var _ store.Store = (*ladybugDB)(nil)

// Open opens a file-backed LadybugDB at the given path directory.
// The database file is <path>/main.lbug. Extensions are loaded.
func Open(path string) (store.Store, error) {
	if err := os.MkdirAll(path, 0755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(path, "branches"), 0755); err != nil {
		return nil, fmt.Errorf("create branches dir: %w", err)
	}

	// Sweep atomic-write temporaries stranded by a crash in a prior process
	// lifetime (cleanupOrphanedTempFiles). The sweep must run before the
	// database is opened so no in-flight write can be mistaken for an orphan,
	// and so a pre-existing leak never survives into the new process.
	cleanupOrphanedTempFiles(path)

	dbPath := filepath.Join(path, "main.lbug")
	database, err := lbug.OpenDatabase(dbPath, lbug.DefaultSystemConfig())
	if err != nil {
		// Corruption recovery (SPEC R8) applies only to a genuinely corrupted
		// main.lbug, not to any open failure. The library reports only a status
		// integer for database_init and exposes no error detail through the Go
		// wrapper, so we distinguish corruption from an operational (e.g.
		// permission/IO) open failure by probing whether the file itself is
		// readable. If the file is readable it is present but unreadable by the
		// database engine — corruption — and deleting it to rehydrate from git
		// is correct. If the file is NOT clearly readable, the failure is more
		// likely a privilege/IO problem and deleting the file would permanently
		// destroy data that was never corrupt; in that case we classify and
		// fail without touching the file.
		if corruptionCandidates(dbPath) {
			// The engine's write-ahead-log companions (<db>.lbug.wal and
			// <db>.lbug.wal.checkpoint) are the artifacts a crash tears
			// alongside the main file; remove them with it so the fresh
			// re-open below does not replay a still-torn WAL and fail again
			// (the crash loop that would otherwise never reach the SPEC R8 git
			// re-hydration).
			if rmErr := removeCorruptedMain(dbPath); rmErr != nil {
				return nil, fmt.Errorf("remove corrupted database: %w", rmErr)
			}
			database, err = lbug.OpenDatabase(dbPath, lbug.DefaultSystemConfig())
			if err != nil {
				return nil, fmt.Errorf("open database after recovery: %w", err)
			}
		} else {
			return nil, fmt.Errorf("open database: %w", err)
		}
	}

	conn, err := lbug.OpenConnection(database)
	if err != nil {
		database.Close()
		return nil, fmt.Errorf("open connection: %w", err)
	}

	ldb, err := newLadybugDB(path, database, conn)
	if err != nil {
		return nil, err
	}

	ldb.mu.Lock()
	err = ldb.restoreMainSchemaMetadataLocked()
	ldb.mu.Unlock()
	if err != nil {
		_ = ldb.Close()
		return nil, fmt.Errorf("restore schema metadata: %w", err)
	}

	return ldb, nil
}

// corruptionCandidates reports whether an OpenDatabase failure is consistent
// with a corrupted main.lbug rather than an operational open failure. The
// go-ladybug wrapper surfaces only a status integer for database_init and does
// not expose the underlying reason, so we classify by file accessibility: if
// the database file is present but cannot be opened at the OS File layer
// (e.g. permission denied or a path/IO problem), removing it would permanently
// destroy data that was never corrupt, so those opens must fail/classify
// instead of recovering. If the file is readable, the database engine could
// not parse it — genuine corruption — and the SPEC R8 recovery path applies.
//
// ponytail: the readability probe is a classification heuristic, not a
// corruption diagnosis, and it has a genuine data-loss ceiling: a
// readable-but-not-corrupt main.lbug that the engine fails to open for a
// transient/operational reason — another process holding the file, a library
// version skew between the binary that wrote it and this build — is classified
// as a corruption candidate. Open then removes the file irreversibly (it is
// deleted before the re-open and never backed up) and the R8 recovery
// re-hydration reconstructs only graph state that was committed to git, so any
// engine state not yet reflected in the git tree (an in-flight write, a
// partially-applied schema) is lost with the file. Deployment risk: a pod
// restart racing a write, or a version-skewed rollback opening a PVC written
// by a newer build, silently triggers the destructive path instead of failing
// loudly. Upgrade path: gate the deletion on an engine-level corruption
// verdict — request an explicit error detail/status from the library (or
// attempt a read-only open before deleting) — rather than a file-accessibility
// proxy.
func corruptionCandidates(dbPath string) bool {
	// A missing file is not a corruption candidate: OpenDatabase creates a
	// fresh database for a missing path, so a failure with no file present is
	// an operational error (e.g. the directory is not writable).
	if _, err := os.Stat(dbPath); err != nil {
		return false
	}
	// Prove the file is readable at the OS level before assuming the engine
	// failed because of its contents.
	f, err := os.OpenFile(dbPath, os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// removeCorruptedMain deletes a corrupted main.lbug together with the engine's
// write-ahead-log companions — <db>.lbug.wal and <db>.lbug.wal.checkpoint (the
// library's WAL_FILE_SUFFIX/CHECKPOINT_WAL_FILE_SUFFIX, lbug.hpp). The open
// failure that triggered SPEC R8 recovery can originate from a torn WAL rather
// than the main file itself (the WAL is replayed on open), so removing only
// main.lbug would leave the torn WAL to be replayed by the fresh re-open —
// failing again into a permanent crash loop that never reaches the R8 git
// re-hydration. Missing companions are not an error (a clean close leaves no
// WAL behind).
func removeCorruptedMain(dbPath string) error {
	for _, p := range []string{
		dbPath,
		dbPath + ".wal",
		dbPath + ".wal.checkpoint",
	} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// newLadybugDB builds the ladybugDB struct around an opened database and
// connection and runs the shared post-open sequence (extension load and
// schema-cache rebuild) that Open and the in-memory test constructor require.
// On any failure the partially-built store is closed before the error is
// returned.
func newLadybugDB(path string, database *lbug.Database, conn *lbug.Connection) (*ladybugDB, error) {
	ldb := &ladybugDB{
		path:              path,
		db:                database,
		conn:              conn,
		entityTypeDefs:    make(map[string]*store.EntityTypeDef),
		edgeTypeDefs:      make(map[string]*store.EdgeTypeDef),
		ruleIndex:         make(map[string][]*flowv1.ConnectionRule),
		edgePairs:         make(map[string][]fromToPair),
		branches:          make(map[string]*branchDB),
		branchStates:      make(map[string]store.BranchTransactionState),
		stageMetadata:     stageSchemaMetadata,
		publishMetadata:   publishSchemaMetadata,
		writeMetadata:     writeSchemaMetadata,
		createVectorIndex: createVectorIndexOnConn,
		readDir:           os.ReadDir,
	}
	// Wire the five cohesive method-group owners to the shared store state.
	ldb.schemaManager = &schemaManager{db: ldb}
	ldb.crudAccessor = &crudAccessor{db: ldb}
	ldb.branchLifecycle = &branchLifecycle{db: ldb}
	ldb.rehydrator = &rehydrator{db: ldb}
	ldb.queryEngine = &queryEngine{db: ldb}
	if err := loadExtensionsOnConn(ldb.conn, ""); err != nil {
		_ = ldb.Close()
		return nil, fmt.Errorf("load extensions: %w", err)
	}
	if err := ldb.rebuildSchemaCache(); err != nil {
		_ = ldb.Close()
		return nil, fmt.Errorf("rebuild schema cache: %w", err)
	}
	return ldb, nil
}

// Close releases the database connection and database handle.
func (db *ladybugDB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()

	if db.closed {
		return nil
	}
	db.closed = true

	for id, branch := range db.branches {
		branch.mu.Lock()
		if branch.conn != nil {
			branch.conn.Close()
		}
		if branch.db != nil {
			branch.db.Close()
		}
		branch.mu.Unlock()
		delete(db.branches, id)
	}
	if db.conn != nil {
		db.conn.Close()
	}
	if db.db != nil {
		db.db.Close()
	}
	return nil
}

// loadExtensionsOnConn installs and loads the vector and fts extensions on an
// arbitrary connection. INSTALL is idempotent — it is safe to call on every
// Open. On some configurations the extension may already be installed, so
// INSTALL errors are ignored and LOAD is attempted directly. contextLabel
// names the database in the error message ("" for main, "on branch" for
// branch connections) so the failing connection is identifiable.
func loadExtensionsOnConn(conn *lbug.Connection, contextLabel string) error {
	for _, ext := range []string{"vector", "fts"} {
		// Try INSTALL first; on some configurations the extension may already
		// be installed, so we ignore INSTALL errors and attempt LOAD directly.
		r, _ := conn.Query("INSTALL " + ext + ";")
		if r != nil {
			r.Close()
		}
		r, err := conn.Query("LOAD " + ext + ";")
		if err != nil {
			if contextLabel == "" {
				return fmt.Errorf("load extension %q: %w", ext, err)
			}
			return fmt.Errorf("load extension %q %s: %w", ext, contextLabel, err)
		}
		r.Close()
	}
	return nil
}
