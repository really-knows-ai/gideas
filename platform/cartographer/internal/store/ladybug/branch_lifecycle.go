package ladybug

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	lbug "github.com/LadybugDB/go-ladybug"
	"github.com/foundry/flow/cartographer/internal/store"
)

// branchLifecycle owns the branch-DB lifecycle method group of ladybugDB:
// creating, dropping, closing, and lazily reopening transaction branch
// databases. The shared store state (locks, connections, branch registries,
// schema caches) lives on ladybugDB; db is the owner pointer back to it.
type branchLifecycle struct {
	db *ladybugDB
}

// CreateBranchDB opens a new LadybugDB for the given txID. File-backed stores
// persist branches under branches/<txID>.lbug; in-memory stores remain ephemeral.
func (bl *branchLifecycle) CreateBranchDB(_ context.Context, txID string) error {
	db := bl.db
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
func (bl *branchLifecycle) DropBranchDB(_ context.Context, txID string) error {
	db := bl.db
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
func (bl *branchLifecycle) CloseBranchDB(_ context.Context, txID string) error {
	db := bl.db
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

func (bl *branchLifecycle) branchPath(txID string) string {
	db := bl.db
	return filepath.Join(db.path, "branches", txID+".lbug")
}

// branchLocked returns a branch while db.mu is held, lazily reopening a
// persisted branch after process restart.
func (bl *branchLifecycle) branchLocked(txID string) (*branchDB, error) {
	db := bl.db
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
