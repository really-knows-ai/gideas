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

	// Schema cache (rebuilt from catalog on Open)
	entityTypeDefs map[string]*store.EntityTypeDef
	edgeTypeDefs   map[string]*store.EdgeTypeDef

	// ruleIndex maps entity type name -> connection rules for edge validation.
	ruleIndex map[string][]*flowv1.ConnectionRule

	// edgePairs stores the FROM/TO pairs for each edge type, populated during
	// ApplySchema and used when rehydrating edge tables.
	edgePairs map[string][]fromToPair

	// Branches (txID -> branchDB)
	branches map[string]*branchDB
}

// branchDB holds a real LadybugDB connection for an isolated branch.
type branchDB struct {
	mu             sync.Mutex
	db             *lbug.Database
	conn           *lbug.Connection
	entityTypeDefs map[string]*store.EntityTypeDef
	edgeTypeDefs   map[string]*store.EdgeTypeDef
}

// Compile-time interface check.
var _ store.Store = (*ladybugDB)(nil)

// Open opens a file-backed LadybugDB at the given path directory.
// The database file is <path>/main.lbug. Extensions are loaded.
func Open(path string) (store.Store, error) {
	if err := os.MkdirAll(path, 0755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	dbPath := filepath.Join(path, "main.lbug")
	database, err := lbug.OpenDatabase(dbPath, lbug.DefaultSystemConfig())
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	conn, err := lbug.OpenConnection(database)
	if err != nil {
		database.Close()
		return nil, fmt.Errorf("open connection: %w", err)
	}

	ldb := &ladybugDB{
		path:           path,
		db:             database,
		conn:           conn,
		entityTypeDefs: make(map[string]*store.EntityTypeDef),
		edgeTypeDefs:   make(map[string]*store.EdgeTypeDef),
		ruleIndex:      make(map[string][]*flowv1.ConnectionRule),
		edgePairs:      make(map[string][]fromToPair),
		branches:       make(map[string]*branchDB),
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
		db:             database,
		conn:           conn,
		entityTypeDefs: make(map[string]*store.EntityTypeDef),
		edgeTypeDefs:   make(map[string]*store.EdgeTypeDef),
		ruleIndex:      make(map[string][]*flowv1.ConnectionRule),
		edgePairs:      make(map[string][]fromToPair),
		branches:       make(map[string]*branchDB),
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
