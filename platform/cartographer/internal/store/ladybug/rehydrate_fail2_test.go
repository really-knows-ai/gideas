package ladybug

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foundry/flow/cartographer/internal/store"
	"github.com/google/uuid"
)

// A JSON element file with a missing `id` key must fail loudly on every load
// path (main/branch × entity/edge) instead of silently assigning a fresh UUID:
// a generated ID changes the element's identity and diverges from its filename,
// so the next serialisation would rewrite the element under a new name,
// orphaning the original file. The sibling checks (missing `type`, type/directory
// mismatch, unparseable content) all fail loudly; the missing `id` must too.
func TestRehydrateFiles_MissingIDFailsLoudly(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed rehydration from working tree")
	}
	for _, tc := range []struct {
		name   string
		branch bool
		edge   bool
		want   error
	}{
		{"main entity", false, false, store.ErrInvalidEntityDir},
		{"main edge", false, true, store.ErrInvalidEdgeDir},
		{"branch entity", true, false, store.ErrInvalidEntityDir},
		{"branch edge", true, true, store.ErrInvalidEdgeDir},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := openInMemory()
			if err != nil {
				t.Fatal(err)
			}
			defer closeStore(t, s)
			applyTestSchema(t, s)
			ctx := context.Background()

			const branch = "tx1"
			if tc.branch {
				if err := s.CreateBranchDB(ctx, branch); err != nil {
					t.Fatalf("CreateBranchDB: %v", err)
				}
				if err := s.ReplicateSchemaToBranch(ctx, branch); err != nil {
					t.Fatalf("ReplicateSchemaToBranch: %v", err)
				}
			}

			root := t.TempDir()
			entitiesDir := filepath.Join(root, "entities")
			edgesDir := filepath.Join(root, "edges")
			if tc.edge {
				// Edge file with every required key except `id`.
				writeJSONFile(t, filepath.Join(edgesDir, "DependsOn", "edge.json"), map[string]any{
					"type": "DependsOn", "from": uuid.NewString(), "to": uuid.NewString(),
				})
			} else {
				// Entity file with every required key except `id`.
				writeJSONFile(t, filepath.Join(entitiesDir, "Component", "ent.json"), map[string]any{
					"type": "Component", "properties": map[string]string{"name": "no-id"},
				})
			}

			var loadErr error
			if tc.branch {
				loadErr = s.HydrateBranchFromFiles(ctx, branch, entitiesDir, edgesDir)
			} else {
				loadErr = s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
			}
			if loadErr == nil {
				t.Fatal("expected loud failure for a file missing the required 'id' key")
			}
			if !errors.Is(loadErr, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, loadErr)
			}
		})
	}
}

// SPEC R8 corruption recovery + SPEC:982 "Sync re-hydration failed → INTERNAL
// (e.g. disk full, filesystem corruption)": the re-hydration loaders must
// surface a filesystem I/O error loudly on every load path — a file the OS
// cannot read (here: permission-denied, mode 000) must fail the whole
// re-hydration, never be silently skipped and reported as success, which would
// re-hydrate an incomplete graph with no signal (LEARNINGS: store-layer I/O
// errors must be propagated, never discarded). This disk-backed coverage closes
// the SPEC-coverage gap that the gitstore setupTestStore ponytail used to admit
// (gitstore_test.go): the sync worker's SPEC:982 INTERNAL row depends on
// RehydrateMainFromFiles failing loudly when the working tree cannot be read.
func TestRehydrateFiles_IOErrorFailsLoudly(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed rehydration from working tree")
	}
	for _, tc := range []struct {
		name   string
		branch bool
		edge   bool
	}{
		{"main entity", false, false},
		{"main edge", false, true},
		{"branch entity", true, false},
		{"branch edge", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := openInMemory()
			if err != nil {
				t.Fatal(err)
			}
			defer closeStore(t, s)
			applyTestSchema(t, s)
			ctx := context.Background()

			const branch = "tx1"
			if tc.branch {
				if err := s.CreateBranchDB(ctx, branch); err != nil {
					t.Fatalf("CreateBranchDB: %v", err)
				}
				if err := s.ReplicateSchemaToBranch(ctx, branch); err != nil {
					t.Fatalf("ReplicateSchemaToBranch: %v", err)
				}
			}

			root := t.TempDir()
			entitiesDir := filepath.Join(root, "entities")
			edgesDir := filepath.Join(root, "edges")
			var blocked string
			if tc.edge {
				blocked = filepath.Join(edgesDir, "DependsOn", "edge.json")
				writeJSONFile(t, blocked, map[string]any{
					"id": uuid.NewString(), "type": "DependsOn", "from": uuid.NewString(), "to": uuid.NewString(),
				})
			} else {
				blocked = filepath.Join(entitiesDir, "Component", "ent.json")
				writeJSONFile(t, blocked, map[string]any{
					"id": uuid.NewString(), "type": "Component", "properties": map[string]string{"name": "io-error"},
				})
			}
			// Make the file unreadable so the loaders' os.ReadFile fails with
			// EACCES. Restore permissions so t.TempDir can clean up.
			if err := os.Chmod(blocked, 0000); err != nil {
				t.Fatalf("chmod 000: %v", err)
			}
			defer func() {
				if err := os.Chmod(blocked, 0600); err != nil {
					t.Errorf("restore chmod: %v", err)
				}
			}()

			var loadErr error
			if tc.branch {
				loadErr = s.HydrateBranchFromFiles(ctx, branch, entitiesDir, edgesDir)
			} else {
				loadErr = s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
			}
			if loadErr == nil {
				t.Fatal("expected loud failure for an unreadable file, got nil")
			}
			want := "read entity file"
			if tc.edge {
				want = "read edge file"
			}
			if !strings.Contains(loadErr.Error(), want) {
				t.Fatalf("expected the file-read error to be propagated, got %v", loadErr)
			}
		})
	}
}

// A JSON entity file whose `id` key is present but whose required `type` key
// is absent must fail loudly on every load path (branch.go:1113-1116 main,
// branch.go:1277-1280 branch → ErrInvalidEntityDir): a type-less file cannot
// be tied to its directory label, so the sibling missing-`id` guard's
// protection would be incomplete without it.
func TestRehydrateFiles_MissingTypeFailsLoudly(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed rehydration from working tree")
	}
	for _, tc := range []struct {
		name   string
		branch bool
	}{
		{"main entity", false},
		{"branch entity", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := openInMemory()
			if err != nil {
				t.Fatal(err)
			}
			defer closeStore(t, s)
			applyTestSchema(t, s)
			ctx := context.Background()

			const branch = "tx1"
			if tc.branch {
				if err := s.CreateBranchDB(ctx, branch); err != nil {
					t.Fatalf("CreateBranchDB: %v", err)
				}
				if err := s.ReplicateSchemaToBranch(ctx, branch); err != nil {
					t.Fatalf("ReplicateSchemaToBranch: %v", err)
				}
			}

			root := t.TempDir()
			entitiesDir := filepath.Join(root, "entities")
			edgesDir := filepath.Join(root, "edges")
			// Entity file with every required key except `type`.
			writeJSONFile(t, filepath.Join(entitiesDir, "Component", "ent.json"), map[string]any{
				"id": uuid.NewString(), "properties": map[string]string{"name": "no-type"},
			})

			var loadErr error
			if tc.branch {
				loadErr = s.HydrateBranchFromFiles(ctx, branch, entitiesDir, edgesDir)
			} else {
				loadErr = s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
			}
			if loadErr == nil {
				t.Fatal("expected loud failure for a file missing the required 'type' key")
			}
			if !errors.Is(loadErr, store.ErrInvalidEntityDir) {
				t.Fatalf("expected ErrInvalidEntityDir, got %v", loadErr)
			}
		})
	}
}

// A JSON edge file missing any of the required `type`/`from`/`to` keys must
// fail loudly on every load path (branch.go:1197-1200 main,
// branch.go:1362-1365 branch → ErrInvalidEdgeDir), even when the `id` key is
// present.
func TestRehydrateFiles_MissingEdgeKeysFailsLoudly(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed rehydration from working tree")
	}
	for _, tc := range []struct {
		name   string
		branch bool
		file   map[string]any
	}{
		{"main missing type", false, map[string]any{
			"id": uuid.NewString(), "from": uuid.NewString(), "to": uuid.NewString(),
		}},
		{"main missing endpoints", false, map[string]any{
			"id": uuid.NewString(), "type": "DependsOn",
		}},
		{"branch missing type", true, map[string]any{
			"id": uuid.NewString(), "from": uuid.NewString(), "to": uuid.NewString(),
		}},
		{"branch missing endpoints", true, map[string]any{
			"id": uuid.NewString(), "type": "DependsOn",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := openInMemory()
			if err != nil {
				t.Fatal(err)
			}
			defer closeStore(t, s)
			applyTestSchema(t, s)
			ctx := context.Background()

			const branch = "tx1"
			if tc.branch {
				if err := s.CreateBranchDB(ctx, branch); err != nil {
					t.Fatalf("CreateBranchDB: %v", err)
				}
				if err := s.ReplicateSchemaToBranch(ctx, branch); err != nil {
					t.Fatalf("ReplicateSchemaToBranch: %v", err)
				}
			}

			root := t.TempDir()
			entitiesDir := filepath.Join(root, "entities")
			edgesDir := filepath.Join(root, "edges")
			writeJSONFile(t, filepath.Join(edgesDir, "DependsOn", "edge.json"), tc.file)

			var loadErr error
			if tc.branch {
				loadErr = s.HydrateBranchFromFiles(ctx, branch, entitiesDir, edgesDir)
			} else {
				loadErr = s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
			}
			if loadErr == nil {
				t.Fatal("expected loud failure for an edge file missing type/from/to keys")
			}
			if !errors.Is(loadErr, store.ErrInvalidEdgeDir) {
				t.Fatalf("expected ErrInvalidEdgeDir, got %v", loadErr)
			}
		})
	}
}
