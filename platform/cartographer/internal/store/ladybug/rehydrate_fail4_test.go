package ladybug

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	uuid "github.com/google/uuid"
)

// A committed file under a type directory absent from the applied schema must
// be inferred from the directory structure (SPEC R8) on every load path
// (main/branch × entity/edge), never silently skipped: the applied schema and
// the git file-per-element representation can diverge (corrupt main.lbug
// recovery, lost schema metadata, partial wipe), and R8 re-hydration must
// "recover the full graph state". Regression: the loaders skipped any type
// directory absent from a non-empty applied schema
// (`if _, ok := defs[typeName]; !ok && len(defs) > 0 { continue }`), dropping
// committed rows with no error and no inference — directory inference only ran
// when the applied schema was entirely empty.
func TestRehydrateFiles_InferredTypeWithAppliedSchema(t *testing.T) {
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
			// Non-empty applied schema (Component/VectorType/Document/DependsOn);
			// the loaded types below are absent from it and must be inferred.
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
			fromID := uuid.NewString()
			toID := uuid.NewString()
			// Endpoint entities under a schema-known type so an inferred edge
			// type's files pass insertEdgeOnConn's endpoint-existence guard.
			writeJSONFile(t, filepath.Join(entitiesDir, "Component", fromID+".json"), map[string]any{
				"id": fromID, "type": "Component",
			})
			writeJSONFile(t, filepath.Join(entitiesDir, "Component", toID+".json"), map[string]any{
				"id": toID, "type": "Component",
			})
			var loadedType, elementID string
			if tc.edge {
				// Edge under a type dir absent from the applied schema.
				loadedType, elementID = "Links", uuid.NewString()
				writeJSONFile(t, filepath.Join(edgesDir, loadedType, elementID+".json"), map[string]any{
					"id": elementID, "type": loadedType, "from": fromID, "to": toID,
					"properties": map[string]string{"strength": strengthValue},
				})
			} else {
				// Entity under a type dir absent from the applied schema.
				loadedType, elementID = "Widget", uuid.NewString()
				writeJSONFile(t, filepath.Join(entitiesDir, loadedType, elementID+".json"), map[string]any{
					"id": elementID, "type": loadedType,
					"properties": map[string]string{"name": inferredValue},
				})
				// Empty edges dir so the main path's completeness check
				// (entities dir exists ⇒ edges dir must exist) passes.
				if err := os.MkdirAll(edgesDir, 0o755); err != nil {
					t.Fatal(err)
				}
			}

			var loadErr error
			if tc.branch {
				loadErr = s.HydrateBranchFromFiles(ctx, branch, entitiesDir, edgesDir)
			} else {
				loadErr = s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
			}
			if loadErr != nil {
				t.Fatalf("re-hydration with schema-absent type dir: %v", loadErr)
			}

			// The schema-absent type was inferred and its elements loaded —
			// nothing was silently dropped. ListEntities/ListEdgesOfType read the
			// branch-scoped type caches and reject unregistered types, proving
			// the inferred type was registered on the load path itself.
			if tc.branch {
				if tc.edge {
					if _, err := s.ListEdgesOfType(ctx, loadedType, branch); err != nil {
						t.Fatalf("inferred edge type %q not registered on branch: %v", loadedType, err)
					}
					edge, err := s.GetEdge(ctx, elementID, branch)
					if err != nil {
						t.Fatalf("inferred edge %q not loaded on branch: %v", elementID, err)
					}
					if edge.Properties["strength"] != strengthValue {
						t.Fatalf("inferred edge property strength = %q, want %q",
							edge.Properties["strength"], strengthValue)
					}
				} else {
					if _, _, err := s.ListEntities(ctx, loadedType, 10, "", branch); err != nil {
						t.Fatalf("inferred entity type %q not registered on branch: %v", loadedType, err)
					}
					ent, err := s.GetEntity(ctx, elementID, branch)
					if err != nil {
						t.Fatalf("inferred entity %q not loaded on branch: %v", elementID, err)
					}
					if ent.Properties["name"] != inferredValue {
						t.Fatalf("inferred entity property name = %q, want %q",
							ent.Properties["name"], inferredValue)
					}
				}
			} else {
				if tc.edge {
					if _, ok := s.EdgeType(loadedType); !ok {
						t.Fatalf("expected edge type %q to be inferred on main", loadedType)
					}
					edge, err := s.GetEdge(ctx, elementID, "main")
					if err != nil {
						t.Fatalf("inferred edge %q not loaded on main: %v", elementID, err)
					}
					if edge.Properties["strength"] != strengthValue {
						t.Fatalf("inferred edge property strength = %q, want %q",
							edge.Properties["strength"], strengthValue)
					}
				} else {
					if _, ok := s.EntityType(loadedType); !ok {
						t.Fatalf("expected entity type %q to be inferred on main", loadedType)
					}
					ent, err := s.GetEntity(ctx, elementID, "main")
					if err != nil {
						t.Fatalf("inferred entity %q not loaded on main: %v", elementID, err)
					}
					if ent.Properties["name"] != inferredValue {
						t.Fatalf("inferred entity property name = %q, want %q",
							ent.Properties["name"], inferredValue)
					}
				}
			}
		})
	}
}
