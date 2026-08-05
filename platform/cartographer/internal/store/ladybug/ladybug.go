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
type ladybugDB struct {
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
			if rmErr := os.Remove(dbPath); rmErr != nil && !os.IsNotExist(rmErr) {
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

	if err := ldb.loadExtensions(); err != nil {
		_ = ldb.Close()
		return nil, fmt.Errorf("load extensions: %w", err)
	}

	if err := ldb.rebuildSchemaCache(); err != nil {
		_ = ldb.Close()
		return nil, fmt.Errorf("rebuild schema cache: %w", err)
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

// OpenInMemory opens an ephemeral in-memory LadybugDB for tests.
func OpenInMemory() (store.Store, error) {
	database, err := lbug.OpenInMemoryDatabase(lbug.DefaultSystemConfig())
	if err != nil {
		return nil, fmt.Errorf("open in-memory database: %w", err)
	}

	conn, err := lbug.OpenConnection(database)
	if err != nil {
		database.Close()
		return nil, fmt.Errorf("open connection: %w", err)
	}

	ldb := &ladybugDB{
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

	if err := ldb.loadExtensions(); err != nil {
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

// loadExtensions installs and loads the vector and fts extensions.
// INSTALL is idempotent — it is safe to call on every Open.
func (db *ladybugDB) loadExtensions() error {
	for _, ext := range []string{"vector", "fts"} {
		// Try INSTALL first; on some configurations the extension may already
		// be installed, so we ignore INSTALL errors and attempt LOAD directly.
		_, _ = db.conn.Query("INSTALL " + ext + ";")
		if _, err := db.conn.Query("LOAD " + ext + ";"); err != nil {
			return fmt.Errorf("load extension %q: %w", ext, err)
		}
	}
	return nil
}
