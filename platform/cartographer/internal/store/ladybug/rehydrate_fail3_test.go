package ladybug

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/foundry/flow/cartographer/internal/store"
	uuid "github.com/google/uuid"
)

// An unparseable JSON element file must fail loudly on every load path
// (branch.go:1109-1112 and 1193-1196 → ErrInvalidEntityDir/ErrInvalidEdgeDir)
// — the file guards treat unparseable content as a corrupt element file, never
// skipping or silently accepting it.
func TestRehydrateFiles_UnparseableJSONFailsLoudly(t *testing.T) {
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
				path := filepath.Join(edgesDir, "DependsOn", "edge.json")
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("{ not json"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				path := filepath.Join(entitiesDir, "Component", "ent.json")
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("{ not json"), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			var loadErr error
			if tc.branch {
				loadErr = s.HydrateBranchFromFiles(ctx, branch, entitiesDir, edgesDir)
			} else {
				loadErr = s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
			}
			if loadErr == nil {
				t.Fatal("expected loud failure for an unparseable JSON element file")
			}
			if !errors.Is(loadErr, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, loadErr)
			}
		})
	}
}

// CreateEntity's data-integrity probe (crud.go) enforces global (cross-type) ID
// uniqueness on the runtime write path; insertEntityOnConn must enforce the
// same invariant on the re-hydration load path. Two element files carrying the
// same ID under different type directories are corrupt/hand-edited git state —
// the id column is PRIMARY KEY only within each node table, so both would
// otherwise insert silently, after which findEntityByID resolves the ID
// nondeterministically (map iteration order). The load must fail loudly with
// ErrEntityAlreadyExists on every load path that inserts entities (main and
// branch).
func TestRehydrateFiles_CrossTypeDuplicateIDFailsLoudly(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed rehydration from working tree")
	}
	for _, tc := range []struct {
		name   string
		branch bool
	}{
		{"main", false},
		{"branch", true},
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

			dupID := uuid.NewString()
			root := t.TempDir()
			entitiesDir := filepath.Join(root, "entities")
			edgesDir := filepath.Join(root, "edges")
			if err := os.MkdirAll(edgesDir, 0o755); err != nil {
				t.Fatal(err)
			}
			// Same ID under two different type directories. Filenames are the
			// canonical <id>.json (as the gitstore write path writes them) so
			// the cross-type duplicate-ID guard is reached — a filename/id
			// mismatch would be rejected by the id↔filename guard instead.
			writeJSONFile(t, filepath.Join(entitiesDir, "Component", dupID+".json"), map[string]any{
				"id": dupID, "type": "Component",
			})
			writeJSONFile(t, filepath.Join(entitiesDir, "Document", dupID+".json"), map[string]any{
				"id": dupID, "type": "Document",
			})

			var loadErr error
			if tc.branch {
				loadErr = s.HydrateBranchFromFiles(ctx, branch, entitiesDir, edgesDir)
			} else {
				loadErr = s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
			}
			if loadErr == nil {
				t.Fatal("expected loud failure for a cross-type duplicate entity ID")
			}
			if !errors.Is(loadErr, store.ErrEntityAlreadyExists) {
				t.Fatalf("expected ErrEntityAlreadyExists, got %v", loadErr)
			}
		})
	}
}

// The raw filename base must equal the embedded id on every load path — the
// sibling gitstore read path's invariant (ReadAllEntityFiles/ReadAllEdgeFiles
// reject a file whose embedded id conflicts with its filename base,
// gitstore/entity.go:105-106, edge.go:104-105). A well-formed file is <id>.json
// whose embedded id equals the filename base (writeEntityFile/writeEdgeFile); a
// corrupt/hand-edited file such as wrongname.json containing a canonical id
// would otherwise load silently under an id never written to that path —
// resurrecting a previously deleted element on re-hydration with no signal
// (LEARNINGS: store/gitstore read-path silent divergence). Every main/branch ×
// entity/edge load path must fail loudly with the sentinel.
func TestRehydrateFiles_EmbeddedIDConflictsWithFilenameFailsLoudly(t *testing.T) {
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
				// Edge file with a canonical embedded id under a filename that
				// does not match it.
				writeJSONFile(t, filepath.Join(edgesDir, "DependsOn", "wrongname.json"), map[string]any{
					"id": uuid.NewString(), "type": "DependsOn",
					"from": uuid.NewString(), "to": uuid.NewString(),
				})
			} else {
				// Entity file with a canonical embedded id under a filename that
				// does not match it.
				writeJSONFile(t, filepath.Join(entitiesDir, "Component", "wrongname.json"), map[string]any{
					"id": uuid.NewString(), "type": "Component",
					"properties": map[string]string{"name": "filename-mismatch"},
				})
			}

			var loadErr error
			if tc.branch {
				loadErr = s.HydrateBranchFromFiles(ctx, branch, entitiesDir, edgesDir)
			} else {
				loadErr = s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
			}
			if loadErr == nil {
				t.Fatal("expected loud failure for a file whose filename conflicts with its embedded id")
			}
			if !errors.Is(loadErr, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, loadErr)
			}
		})
	}
}

// nonCanonicalUUIDSpellings are the four spellings of a single valid RFC4122
// v4 UUID that google/uuid.Parse accepts but the canonical RFC4122 §3 string
// form (the lowercase 8-4-4-4-12 dashed string) does not. SPEC:162 requires
// the canonical form; the gitstore sibling tests the same spellings against
// its read/write paths (uuid_guard_test.go).
var nonCanonicalUUIDSpellings = []string{
	"550E8400-E29B-41D4-A716-446655440000",          // uppercase hex
	"550e8400e29b41d4a716446655440000",              // 32-char no-hyphen
	"{550e8400-e29b-41d4-a716-446655440000}",        // braced {...}
	"urn:uuid:550e8400-e29b-41d4-a716-446655440000", // urn:uuid: prefix
}

// SPEC R2 requires entity and edge IDs to be the canonical RFC4122 §3 UUID v4
// string form: the store's write path gates every ID through validateUUID
// (store.ErrInvalidIDFormat), and the sibling gitstore read path rejects a
// non-canonical embedded id with ErrInvalidUUID (entity.go/edge.go). The
// re-hydration loaders must reject the same shape on every load path
// (main/branch × entity/edge): a corrupt/hand-edited file whose embedded id is
// a non-canonical spelling of a valid UUID (uppercase hex, no-hyphen, braced,
// urn:uuid:) would otherwise load silently under that spelling — a second row
// for one UUID the write path would never produce. Fail loudly instead (SPEC
// R8). Each file's filename matches its embedded (non-canonical) id so the
// filename↔id guard passes and the canonical-form guard is the one that fires.
func TestRehydrateFiles_NonCanonicalUUIDFailsLoudly(t *testing.T) {
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
			for _, id := range nonCanonicalUUIDSpellings {
				t.Run(id, func(t *testing.T) {
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
						writeJSONFile(t, filepath.Join(edgesDir, "DependsOn", id+".json"), map[string]any{
							"id": id, "type": "DependsOn",
							"from": uuid.NewString(), "to": uuid.NewString(),
						})
					} else {
						writeJSONFile(t, filepath.Join(entitiesDir, "Component", id+".json"), map[string]any{
							"id": id, "type": "Component",
							"properties": map[string]string{"name": "non-canonical"},
						})
					}

					var loadErr error
					if tc.branch {
						loadErr = s.HydrateBranchFromFiles(ctx, branch, entitiesDir, edgesDir)
					} else {
						loadErr = s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
					}
					if loadErr == nil {
						t.Fatal("expected loud failure for a file whose embedded id is a non-canonical UUID v4 spelling")
					}
					if !errors.Is(loadErr, store.ErrInvalidIDFormat) {
						t.Fatalf("expected ErrInvalidIDFormat, got %v", loadErr)
					}
				})
			}
		})
	}
}
