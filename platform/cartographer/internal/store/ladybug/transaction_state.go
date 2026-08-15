package ladybug

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/foundry/flow/cartographer/internal/store"
)

const branchStateVersion = 2

type branchStateRecord struct {
	Version int                          `json:"version"`
	State   store.BranchTransactionState `json:"state"`
}

func (db *ladybugDB) branchStatePath(txID string) string {
	return filepath.Join(db.path, "branches", txID+".state.json")
}

// SaveBranchTransactionState atomically replaces the branch's sole durable
// transaction lifecycle record.
func (db *ladybugDB) SaveBranchTransactionState(
	_ context.Context, txID string, state store.BranchTransactionState,
) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if filepath.Base(txID) != txID || txID == "." || txID == ".." {
		return fmt.Errorf("invalid branch ID %q", txID)
	}
	if _, err := db.branchLocked(txID); err != nil {
		return err
	}
	if err := validateBranchTransactionState(state); err != nil {
		return err
	}
	if db.path != "" {
		data, err := json.Marshal(branchStateRecord{Version: branchStateVersion, State: state})
		if err != nil {
			return fmt.Errorf("marshal branch state: %w", err)
		}
		if err := writeAtomicBranchState(db.branchStatePath(txID), data); err != nil {
			return err
		}
	}
	db.branchStates[txID] = state
	return nil
}

// LoadBranchTransactionState loads the versioned lifecycle record. Missing,
// malformed, and unsupported records fail closed.
func (db *ladybugDB) LoadBranchTransactionState(_ context.Context, txID string) (store.BranchTransactionState, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed || db.failed {
		return store.BranchTransactionState{}, store.ErrDatabaseNotReady
	}
	if filepath.Base(txID) != txID || txID == "." || txID == ".." {
		return store.BranchTransactionState{}, fmt.Errorf("invalid branch ID %q", txID)
	}
	if state, ok := db.branchStates[txID]; ok {
		return state, nil
	}
	if db.path == "" {
		return store.BranchTransactionState{}, fmt.Errorf("%w", store.ErrBranchStateMissing)
	}
	data, err := os.ReadFile(db.branchStatePath(txID))
	if err != nil {
		if os.IsNotExist(err) {
			return store.BranchTransactionState{}, fmt.Errorf("%w", store.ErrBranchStateMissing)
		}
		return store.BranchTransactionState{}, fmt.Errorf("read branch state: %w", err)
	}
	var record branchStateRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return store.BranchTransactionState{}, fmt.Errorf("parse branch state: %w", err)
	}
	if record.Version != branchStateVersion {
		return store.BranchTransactionState{}, fmt.Errorf("unsupported branch state version %d", record.Version)
	}
	if err := validateBranchTransactionState(record.State); err != nil {
		return store.BranchTransactionState{}, err
	}
	db.branchStates[txID] = record.State
	return record.State, nil
}

func validateBranchTransactionState(state store.BranchTransactionState) error {
	if state.MainHeadAtLastSync == "" || state.SchemaHash == "" {
		return fmt.Errorf("invalid branch state: transaction baselines are required")
	}
	if state.CommitCreated && !state.CommitStarted {
		return fmt.Errorf("invalid branch state: created commit was not started")
	}
	if (state.CommitHydrated || state.MainRehydrated || state.MergeCompleted) && !state.CommitCreated {
		return fmt.Errorf("invalid branch state: commit milestone without created commit")
	}
	if state.MergeCompleted && state.MainRehydrated {
		return fmt.Errorf("invalid branch state: merged transaction still marks main rehydrated")
	}
	return nil
}

// InvalidateBranchState removes the sole lifecycle record. Recovery treats the
// missing record as unsafe and refuses to register the branch.
func (db *ladybugDB) InvalidateBranchState(_ context.Context, txID string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed || db.failed {
		return store.ErrDatabaseNotReady
	}
	if filepath.Base(txID) != txID || txID == "." || txID == ".." {
		return fmt.Errorf("invalid branch ID %q", txID)
	}
	if db.path != "" {
		if err := os.Remove(db.branchStatePath(txID)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("invalidate branch state: %w", err)
		}
		if err := syncDirectory(filepath.Join(db.path, "branches")); err != nil {
			return fmt.Errorf("sync invalidated branch state: %w", err)
		}
	}
	delete(db.branchStates, txID)
	return nil
}

func writeAtomicBranchState(path string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("create branch state temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("chmod branch state: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write branch state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync branch state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close branch state: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace branch state: %w", err)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("sync branch state directory: %w", err)
	}
	return nil
}

// cleanupOrphanedTempFiles removes atomic-write temporaries stranded by a
// crash in a prior process lifetime. The three atomic-write primitives —
// writeAtomicBranchState (.state-*.tmp), stageSchemaMetadata (.schema-*.tmp),
// and probePVCWritable (health-*.tmp) — stage each durable write in a temp
// file that is renamed onto its target (or, for the health probe, removed)
// only after the write is fully synced, so a crash at any point before that
// rename/removal leaks the temp file. Neither existing sweep covers them:
// cleanupOrphanedTempBranches (service/recovery.go) deliberately skips them —
// branchKeyFromFile matches only .lbug/.schema.json/.state.json branch files —
// and the health probe writes to the data root, outside branches/ entirely.
// The leak is pure garbage: every recovery path reads only the renamed target
// (<txID>.state.json, schema.json, or the branch metadata), which a torn temp
// never affects because the rename is atomic, so removing the temp can neither
// lose data nor change what recovery observes.
//
// The match (a .state-/.schema-/health- prefix with a .tmp suffix) cannot
// collide with a live file: live names are <txID>.lbug/.schema.json/.state.json,
// main.lbug, schema.json, and the branches/ and graph-repo/ directories. The
// sweep runs on Open, before any store method can create a new temp, so a
// matched file is never an in-flight write.
//
// ponytail: the sweep is best-effort and discards errors. A leftover temp is
// provably harmless garbage, so failing Open over an unremovable one would
// wedge startup on debris the next Open simply retries; a removal failure
// leaves the temp in place until a later Open, no worse than the pre-fix
// leak. Ceiling: a persistently unremovable temp leaks one small file per
// crash with no signal. Upgrade path: report the sweep outcome through a
// logger or Health once the store carries one.
func cleanupOrphanedTempFiles(dataDir string) {
	for _, dir := range []string{dataDir, filepath.Join(dataDir, "branches")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			for _, pattern := range []string{".state-*.tmp", ".schema-*.tmp", "health-*.tmp"} {
				if matched, _ := filepath.Match(pattern, entry.Name()); matched {
					_ = os.Remove(filepath.Join(dir, entry.Name()))
					break
				}
			}
		}
	}
}
