package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store/ladybug"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/google/uuid"
)

// TestSyncWorker_RehydrateFailureKeepsMainConsistent pins the SPEC error-table
// row "Sync re-hydration failed" atomicity from the worker's perspective: a
// post-fetch re-hydration that fails on a corrupt source (e.g. a corrupt merged
// JSON) must leave main serving its pre-fetch graph — the DETACH DELETE must
// not run before the file tree is proven loadable. Without this, the cycle
// returns with main serving a silently-wiped graph and the R8 "automatic
// recovery on next startup" escape hatch has no consistent graph to recover.
func TestSyncWorker_RehydrateFailureKeepsMainConsistent(t *testing.T) {
	ctx := context.Background()
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	// A non-empty repo with a corrupt tracked file: commit a valid entity, then
	// a corrupt JSON file under the same type directory (tracked so the
	// worker's CleanUntracked cannot remove it before re-hydration).
	now := time.Now().UTC().Round(time.Millisecond)
	if err := gs.WithGitLock(func() error {
		if err := gs.WriteEntityFiles(ctx, "Component", []gitstore.Entity{
			{ID: "11111111-1111-4111-8111-111111111111", Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		if err := gs.AddAll(ctx, "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx, "transaction:seed"); err != nil {
			return err
		}
		entitiesDir, _ := gs.HydrationDirs()
		compDir := filepath.Join(entitiesDir, "Component")
		if err := os.WriteFile(filepath.Join(compDir, "corrupt.json"), []byte("not json"), 0644); err != nil {
			return err
		}
		if err := gs.AddAll(ctx, "."); err != nil {
			return err
		}
		return gs.Commit(ctx, "corrupt-merge")
	}); err != nil {
		t.Fatalf("seed git tree: %v", err)
	}

	base, err := ladybug.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	if err := base.ApplySchema(ctx, &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{{
			Name:       "Component",
			Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
		}},
	}); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	// A main-only entity: present in main.lbug but absent from the git tree, so
	// a wipe-then-fail would destroy it and only the validation-first order
	// keeps it.
	mainOnlyID := uuid.NewString()
	if _, err := base.CreateEntity(ctx, "Component", mainOnlyID,
		map[string]string{"name": "main-only"}, nil, "main"); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	preHead, err := gs.BranchHEAD(ctx, "main")
	if err != nil {
		t.Fatalf("BranchHEAD: %v", err)
	}
	fetchHash := "1" + preHead[1:]
	if fetchHash == preHead {
		fetchHash = "2" + preHead[1:]
	}
	syncGit := &syncMockGitStore{GitStore: gs, fetchHash: plumbing.NewHash(fetchHash)}
	sw := NewSyncWorker("https://example.com/repo.git", syncGit, base, RealClock{})
	sw.runSyncCycle()

	sw.cycleMu.Lock()
	cycleErr := sw.cycleErr
	sw.cycleMu.Unlock()
	if cycleErr == nil {
		t.Fatal("expected the re-hydration failure to surface from the cycle")
	}
	got, err := base.GetEntity(ctx, mainOnlyID, "main")
	if err != nil {
		t.Fatalf("failed re-hydration wiped main.lbug: %v", err)
	}
	if got.Properties["name"] != "main-only" {
		t.Fatalf("pre-fetch entity mutated by failed re-hydration: %v", got.Properties)
	}
}
