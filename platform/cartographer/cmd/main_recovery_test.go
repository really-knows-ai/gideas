package main

// SPEC R8 startup corruption-recovery re-hydration (rehydrateMainAfterRecovery)

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
	"github.com/foundry/flow/cartographer/internal/store/ladybug"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/google/uuid"
)

// recoveryGitStore is a gitstore stub for rehydrateMainAfterRecovery that
// reports the repository's commit state and hydration directories, and tracks
// the restore-main/clean-untracked steps the recovery path runs before
// re-hydration (a crash can leave the working tree stranded on a transaction
// branch, so the tree must be switched back to main before files are read).
type recoveryGitStore struct {
	gitstore.GitStore
	isEmpty      bool
	stateErr     error
	dirs         [2]string
	restoreErr   error
	cleanErr     error
	restoreCalls int
	cleanCalls   int
}

func (g *recoveryGitStore) IsEmpty(context.Context) (bool, error) { return g.isEmpty, g.stateErr }
func (g *recoveryGitStore) HydrationDirs() (string, string)       { return g.dirs[0], g.dirs[1] }
func (g *recoveryGitStore) WithGitLock(fn func() error) error     { return fn() }
func (g *recoveryGitStore) RestoreMain(context.Context) error {
	g.restoreCalls++
	return g.restoreErr
}
func (g *recoveryGitStore) CleanUntracked(context.Context) error {
	g.cleanCalls++
	return g.cleanErr
}

// recoveryStore is a store.Store stub that counts RehydrateMainFromFiles
// invocations. The transaction-only write model removed the main-graph-data
// probe (rehydrateMainAfterRecovery re-hydrates unconditionally when the repo
// is non-empty), so the stub no longer models count-query responses.
type recoveryStore struct {
	store.Store
	rehydrateCalls int
	rehydrateErr   error
}

func (s *recoveryStore) RehydrateMainFromFiles(context.Context, string, string) error {
	s.rehydrateCalls++
	return s.rehydrateErr
}

// TestRehydrateMainAfterRecovery pins the SPEC R8 re-hydration behavior: with
// the transaction-only write model there are no local-only writes to protect,
// so whenever the git repository has commits (not empty) main is re-hydrated
// from git unconditionally, and any failure must be surfaced (fail loudly)
// rather than silently serving a vacuous graph. The working tree is switched
// back to main (RestoreMain + CleanUntracked) before files are read, so a
// healthy main.lbug is never rebuilt from a stale transaction-branch snapshot.
func TestRehydrateMainAfterRecovery(t *testing.T) {
	ctx := context.Background()

	t.Run("empty git repo skips re-hydration (fresh install)", func(t *testing.T) {
		gs := &recoveryGitStore{isEmpty: true}
		st := &recoveryStore{}
		if err := rehydrateMainAfterRecovery(ctx, st, gs); err != nil {
			t.Fatalf("rehydrateMainAfterRecovery: %v", err)
		}
		if st.rehydrateCalls != 0 {
			t.Fatalf("re-hydration ran for an empty git repo: %d calls", st.rehydrateCalls)
		}
		if gs.restoreCalls != 0 || gs.cleanCalls != 0 {
			t.Fatalf("restore/clean ran for an empty git repo: restore=%d clean=%d",
				gs.restoreCalls, gs.cleanCalls)
		}
	})

	t.Run("committed git re-hydrates unconditionally", func(t *testing.T) {
		gs := &recoveryGitStore{isEmpty: false, dirs: [2]string{"entities", "edges"}}
		st := &recoveryStore{}
		if err := rehydrateMainAfterRecovery(ctx, st, gs); err != nil {
			t.Fatalf("rehydrateMainAfterRecovery: %v", err)
		}
		if st.rehydrateCalls != 1 {
			t.Fatalf("re-hydration calls = %d, want 1", st.rehydrateCalls)
		}
		if gs.restoreCalls != 1 || gs.cleanCalls != 1 {
			t.Fatalf("restore/clean before re-hydration: restore=%d clean=%d, want 1 each",
				gs.restoreCalls, gs.cleanCalls)
		}
	})

	t.Run("restore-main failure is surfaced (fail loudly)", func(t *testing.T) {
		gs := &recoveryGitStore{isEmpty: false, restoreErr: errors.New("restore boom")}
		st := &recoveryStore{}
		err := rehydrateMainAfterRecovery(ctx, st, gs)
		if err == nil {
			t.Fatal("expected restore-main failure to be surfaced, got nil")
		}
		if !strings.Contains(err.Error(), "restore boom") {
			t.Fatalf("error does not carry the restore-main failure: %v", err)
		}
		if st.rehydrateCalls != 0 {
			t.Fatalf("re-hydration ran after restore-main failure: %d calls", st.rehydrateCalls)
		}
	})

	t.Run("clean-untracked failure is surfaced (fail loudly)", func(t *testing.T) {
		gs := &recoveryGitStore{isEmpty: false, cleanErr: errors.New("clean boom")}
		st := &recoveryStore{}
		err := rehydrateMainAfterRecovery(ctx, st, gs)
		if err == nil {
			t.Fatal("expected clean-untracked failure to be surfaced, got nil")
		}
		if !strings.Contains(err.Error(), "clean boom") {
			t.Fatalf("error does not carry the clean-untracked failure: %v", err)
		}
		if st.rehydrateCalls != 0 {
			t.Fatalf("re-hydration ran after clean-untracked failure: %d calls", st.rehydrateCalls)
		}
	})

	t.Run("re-hydration failure is surfaced (fail loudly)", func(t *testing.T) {
		gs := &recoveryGitStore{isEmpty: false}
		st := &recoveryStore{rehydrateErr: errors.New("rehydrate boom")}
		err := rehydrateMainAfterRecovery(ctx, st, gs)
		if err == nil {
			t.Fatal("expected re-hydration failure to be surfaced, got nil")
		}
		if !strings.Contains(err.Error(), "rehydrate boom") {
			t.Fatalf("error does not carry the re-hydration failure: %v", err)
		}
		if gs.restoreCalls != 1 || gs.cleanCalls != 1 {
			t.Fatalf("restore/clean must run before the re-hydration attempt: restore=%d clean=%d",
				gs.restoreCalls, gs.cleanCalls)
		}
	})

	t.Run("git state check failure is surfaced", func(t *testing.T) {
		gs := &recoveryGitStore{stateErr: errors.New("state boom")}
		st := &recoveryStore{}
		err := rehydrateMainAfterRecovery(ctx, st, gs)
		if err == nil {
			t.Fatal("expected git state-check failure to be surfaced, got nil")
		}
		if st.rehydrateCalls != 0 {
			t.Fatalf("re-hydration ran after git state-check failure: %d calls", st.rehydrateCalls)
		}
		if gs.restoreCalls != 0 || gs.cleanCalls != 0 {
			t.Fatalf("restore/clean ran after git state-check failure: restore=%d clean=%d",
				gs.restoreCalls, gs.cleanCalls)
		}
	})
}

// TestRehydrateMainAfterRecoveryRestoresCommittedGraph pins SPEC R8 with real
// components: a file-backed git repository holding a committed file-per-element
// entity, and a freshly-opened (empty) file-backed main.lbug — the exact state
// ladybug.Open's corruption recovery produces (delete main.lbug, re-open
// empty). rehydrateMainAfterRecovery must restore the committed entity into
// main so the service does not serve a vacuous graph. This also pins that the
// emptiness probe (count queries) succeeds on a fresh, table-less database.
func TestRehydrateMainAfterRecoveryRestoresCommittedGraph(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	gs, err := gitstore.New(root)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	entityID := uuid.NewString()
	now := time.Now().UTC().Round(time.Millisecond)
	if err := gs.WithGitLock(func() error {
		if err := gs.WriteEntityFiles(ctx, "Component", []gitstore.Entity{
			{ID: entityID, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx, "."); err != nil {
			return err
		}
		return gs.Commit(ctx, "transaction:recovery-test")
	}); err != nil {
		t.Fatalf("commit entity: %v", err)
	}
	empty, err := gs.IsEmpty(ctx)
	if err != nil || empty {
		t.Fatalf("fixture: expected non-empty git repo, empty=%v err=%v", empty, err)
	}

	dbStore, err := ladybug.Open(root)
	if err != nil {
		t.Fatalf("ladybug.Open: %v", err)
	}
	t.Cleanup(func() { _ = dbStore.Close() })

	if err := rehydrateMainAfterRecovery(ctx, dbStore, gs); err != nil {
		t.Fatalf("rehydrateMainAfterRecovery: %v", err)
	}

	ent, err := dbStore.GetEntity(ctx, entityID, "")
	if err != nil {
		t.Fatalf("committed entity was not restored into main: %v", err)
	}
	if ent.Type != "Component" {
		t.Fatalf("restored entity type = %q, want %q", ent.Type, "Component")
	}
}

// TestStartupRehydrateFailureFatal pins the startup fatality gate for a failed
// re-hydration (main.go's call site after rehydrateMainAfterRecovery): the
// failure is fatal only when main.lbug holds no graph data. A healthy main.lbug
// holding a complete graph must keep the process alive so the SPEC error-table
// row "Sync re-hydration failed" escape hatch ("see R8 for automatic recovery
// on next startup") stays reachable — the old unconditional os.Exit(1)
// crash-looped a pod whose main.lbug was fine on the same corrupt files.
func TestStartupRehydrateFailureFatal(t *testing.T) {
	ctx := context.Background()

	t.Run("non-fatal when main holds graph data", func(t *testing.T) {
		s, err := ladybug.Open(t.TempDir())
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		if err := s.ApplySchema(ctx, &flowv1.Schema{
			EntityTypes: []*flowv1.EntityType{{
				Name:       "Component",
				Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
			}},
		}); err != nil {
			t.Fatalf("ApplySchema: %v", err)
		}
		if _, err := s.CreateEntity(ctx, "Component", uuid.NewString(),
			map[string]string{"name": "served"}, nil, "main"); err != nil {
			t.Fatalf("CreateEntity: %v", err)
		}
		if startupRehydrateFailureFatal(ctx, s) {
			t.Fatal("startup re-hydration failure must not be fatal while main.lbug holds a complete graph")
		}
	})

	t.Run("fatal when main is empty", func(t *testing.T) {
		s, err := ladybug.Open(t.TempDir())
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		// Fresh database — the state ladybug.Open's corruption recovery
		// produces (delete main.lbug, re-open empty).
		if !startupRehydrateFailureFatal(ctx, s) {
			t.Fatal("startup re-hydration failure must stay fatal when main.lbug holds no graph data")
		}
	})
}

// TestRehydrateMainAfterRecoveryRestoresCurrentMainNotStaleBranch pins SPEC R8
// with real components in the crash scenario that motivates the
// restore-main-before-re-hydration step: a pod killed mid-transaction leaves
// the working tree checked out on the transaction branch (BeginTransaction's
// HardResetToBranch), while main has advanced via a concurrent commit. The
// recovery path must switch the tree back to main before re-hydrating, so
// main.lbug is rebuilt from main's current files — not the stale
// transaction-branch snapshot — and committed data that landed on main after
// the transaction began survives.
func TestRehydrateMainAfterRecoveryRestoresCurrentMainNotStaleBranch(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	gs, err := gitstore.New(root)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}

	now := time.Now().UTC().Round(time.Millisecond)
	commitEntity := func(id string) error {
		return gs.WithGitLock(func() error {
			if err := gs.WriteEntityFiles(ctx, "Component", []gitstore.Entity{
				{ID: id, Type: "Component", CreatedAt: now, UpdatedAt: now},
			}); err != nil {
				return err
			}
			if err := gs.AddAll(ctx, "."); err != nil {
				return err
			}
			return gs.Commit(ctx, "transaction:recovery-"+id)
		})
	}

	// 1. Commit entity A to main — the graph state when the transaction began.
	entityA := uuid.NewString()
	if err := commitEntity(entityA); err != nil {
		t.Fatalf("commit entity A to main: %v", err)
	}

	// 2. A transaction begins: branch tx1 is created from main and the working
	// tree is hard-reset onto it (BeginTransaction's HardResetToBranch), so the
	// tree now shows only entity A.
	txID := uuid.NewString()
	if err := gs.WithGitLock(func() error {
		if err := gs.CreateBranch(ctx, txID); err != nil {
			return err
		}
		return gs.HardResetToBranch(ctx, txID)
	}); err != nil {
		t.Fatalf("begin transaction (create branch + hard reset): %v", err)
	}

	// 3. main advances via a concurrent commit of entity B: restore main, write
	// B, commit — then hard-reset the tree back onto tx1, simulating the crash
	// state where the tree sits on the stale transaction-branch snapshot while
	// main's ref points at a commit containing both A and B.
	entityB := uuid.NewString()
	if err := gs.WithGitLock(func() error {
		if err := gs.RestoreMain(ctx); err != nil {
			return err
		}
		if err := gs.WriteEntityFiles(ctx, "Component", []gitstore.Entity{
			{ID: entityB, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx, "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx, "transaction:recovery-"+entityB); err != nil {
			return err
		}
		return gs.HardResetToBranch(ctx, txID)
	}); err != nil {
		t.Fatalf("advance main then strand tree on tx1: %v", err)
	}

	// Guard: the working tree must still be on the stale snapshot (only A
	// visible) while main's ref has advanced, or this test is not exercising
	// the restore-before-read scenario it claims to.
	var treeFiles []gitstore.EntityFile
	if err := gs.WithGitLock(func() error {
		var err error
		treeFiles, err = gs.ReadAllEntityFiles(ctx, "Component")
		return err
	}); err != nil {
		t.Fatalf("read tree entities for fixture guard: %v", err)
	}
	if len(treeFiles) != 1 || treeFiles[0].ID != entityA {
		t.Fatalf("fixture: expected the tree to show only entity A (stale snapshot), got %+v", treeFiles)
	}

	dbStore, err := ladybug.Open(root)
	if err != nil {
		t.Fatalf("ladybug.Open: %v", err)
	}
	t.Cleanup(func() { _ = dbStore.Close() })

	if err := rehydrateMainAfterRecovery(ctx, dbStore, gs); err != nil {
		t.Fatalf("rehydrateMainAfterRecovery: %v", err)
	}

	// Both entities must be present: B survives because the tree was restored
	// to main before files were read; without that step main.lbug would be
	// rebuilt from the stale tx1 snapshot and B would be silently lost.
	for _, id := range []string{entityA, entityB} {
		if _, err := dbStore.GetEntity(ctx, id, ""); err != nil {
			t.Fatalf("entity %s missing after recovery (main.lbug rebuilt from stale branch snapshot?): %v", id, err)
		}
	}
}
