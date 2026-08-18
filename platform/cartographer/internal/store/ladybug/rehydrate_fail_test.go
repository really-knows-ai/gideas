package ladybug

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/google/uuid"
)

// An edge file whose from/to endpoint entities are absent from the graph must
// fail loudly instead of silently vanishing: insertEdgeOnConn's
// MATCH (a {id: $from}), (b {id: $to}) CREATE ... no-ops when an endpoint
// matches nothing, so without the endpoint-existence guard the edge would be
// dropped on the re-hydration read path with no error (learnings rule: never
// silently drop a row or swallow a not-exist on a read path).
func TestRehydrateMainFromFiles_EdgeWithMissingEndpointFailsLoudly(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed rehydration from working tree")
	}
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)
	ctx := context.Background()

	// Load one endpoint entity via files; the edge's `to` endpoint references
	// an ID absent from the graph (orphaned edge file).
	fromID := uuid.NewString()
	root := t.TempDir()
	entitiesDir := filepath.Join(root, "entities")
	edgesDir := filepath.Join(root, "edges")
	writeJSONFile(t, filepath.Join(entitiesDir, "Component", fromID+".json"), map[string]any{
		"id": fromID, "type": "Component",
	})
	edgeID := uuid.NewString()
	writeJSONFile(t, filepath.Join(edgesDir, "DependsOn", edgeID+".json"), map[string]any{
		"id": edgeID, "type": "DependsOn", "from": fromID, "to": uuid.NewString(),
	})

	err = s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
	if err == nil {
		t.Fatal("expected loud failure for an edge whose endpoint entity is absent")
	}
	if !errors.Is(err, store.ErrSourceOrTargetNotFound) {
		t.Fatalf("expected ErrSourceOrTargetNotFound, got %v", err)
	}
}

// An endpoint entity that exists under a label outside the edge type's fixed
// FROM/TO endpoint set (SPEC R2: the endpoint clauses are fixed at rel-table
// CREATE time) must fail loudly on the edge load path instead of silently
// dropping the edge. The former probe MATCH (n {id: $id}) matched by id alone,
// so an id present under a non-endpoint label passed the probe while the
// subsequent CREATE silently no-opped against the rel table's endpoint clauses
// — the wrong-label edge vanished on re-hydration. The label-constrained probe
// surfaces ErrSourceOrTargetNotFound, mirroring the typed write path
// (crud.go CreateEdge's findEntityByID + rule validation).
func TestRehydrateFiles_WrongLabelEndpointFailsLoudly(t *testing.T) {
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

			// The edge's from-endpoint id exists only under the Document label,
			// outside DependsOn's FROM/TO endpoint set (Component→Component), so
			// the id satisfies an untyped probe but not the label-constrained one.
			fromID := uuid.NewString()
			toID := uuid.NewString()
			root := t.TempDir()
			entitiesDir := filepath.Join(root, "entities")
			edgesDir := filepath.Join(root, "edges")
			writeJSONFile(t, filepath.Join(entitiesDir, "Document", fromID+".json"), map[string]any{
				"id": fromID, "type": "Document",
			})
			writeJSONFile(t, filepath.Join(entitiesDir, "Component", toID+".json"), map[string]any{
				"id": toID, "type": "Component",
			})
			edgeID := uuid.NewString()
			writeJSONFile(t, filepath.Join(edgesDir, "DependsOn", edgeID+".json"), map[string]any{
				"id": edgeID, "type": "DependsOn", "from": fromID, "to": toID,
			})

			var loadErr error
			if tc.branch {
				loadErr = s.HydrateBranchFromFiles(ctx, branch, entitiesDir, edgesDir)
			} else {
				loadErr = s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
			}
			if loadErr == nil {
				t.Fatal("expected loud failure for an edge whose from-endpoint id exists only under a non-endpoint label")
			}
			if !errors.Is(loadErr, store.ErrSourceOrTargetNotFound) {
				t.Fatalf("expected ErrSourceOrTargetNotFound, got %v", loadErr)
			}
		})
	}
}

// The endpoint probe must enforce the edge type's FROM/TO endpoint PAIRS, not
// per-role label-union membership. For a multi-pair edge type (rules
// Alpha→Beta and Beta→Alpha both via CONNECTS) the union of FROM labels equals
// the union of TO labels ({Alpha, Beta}), so an edge whose endpoints both
// resolve to Alpha-typed entities passes a union-membership probe for both
// roles while the rel table's per-pair endpoint clauses (FROM Alpha TO Beta,
// FROM Beta TO Alpha) cannot serve an Alpha→Alpha edge — the edge would not be
// loaded (or, on an engine that silently no-ops an unmatched pair, silently
// dropped). The write path rejects the same edge via validateEdgeRulesFor
// (crud.go → ErrEdgeRuleViolation), so the load path must fail loudly with
// ErrSourceOrTargetNotFound instead of relying on coincidental engine errors.
func TestRehydrateFiles_CrossPairEndpointFailsLoudly(t *testing.T) {
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
			ctx := context.Background()

			// Multi-pair edge type: CONNECTS permits Alpha→Beta and Beta→Alpha.
			if err := s.ApplySchema(ctx, &flowv1.Schema{
				EntityTypes: []*flowv1.EntityType{
					{Name: "Alpha", Rules: []*flowv1.ConnectionRule{
						{CanConnectTo: []string{"Beta"}, Using: []string{"CONNECTS"}},
					}},
					{Name: "Beta", Rules: []*flowv1.ConnectionRule{
						{CanConnectTo: []string{"Alpha"}, Using: []string{"CONNECTS"}},
					}},
				},
				EdgeTypes: []*flowv1.EdgeType{{Name: "CONNECTS"}},
			}); err != nil {
				t.Fatalf("ApplySchema: %v", err)
			}

			const branch = "tx1"
			if tc.branch {
				if err := s.CreateBranchDB(ctx, branch); err != nil {
					t.Fatalf("CreateBranchDB: %v", err)
				}
				if err := s.ReplicateSchemaToBranch(ctx, branch); err != nil {
					t.Fatalf("ReplicateSchemaToBranch: %v", err)
				}
			}

			// Both endpoints are Alpha-typed: Alpha→Alpha is NOT one of
			// CONNECTS' FROM/TO pairs (Alpha→Beta, Beta→Alpha).
			fromID := uuid.NewString()
			toID := uuid.NewString()
			root := t.TempDir()
			entitiesDir := filepath.Join(root, "entities")
			edgesDir := filepath.Join(root, "edges")
			writeJSONFile(t, filepath.Join(entitiesDir, "Alpha", fromID+".json"), map[string]any{
				"id": fromID, "type": "Alpha",
			})
			writeJSONFile(t, filepath.Join(entitiesDir, "Alpha", toID+".json"), map[string]any{
				"id": toID, "type": "Alpha",
			})
			edgeID := uuid.NewString()
			writeJSONFile(t, filepath.Join(edgesDir, "CONNECTS", edgeID+".json"), map[string]any{
				"id": edgeID, "type": "CONNECTS", "from": fromID, "to": toID,
			})

			var loadErr error
			if tc.branch {
				loadErr = s.HydrateBranchFromFiles(ctx, branch, entitiesDir, edgesDir)
			} else {
				loadErr = s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
			}
			if loadErr == nil {
				t.Fatal("expected loud failure for an edge whose endpoint pair is not in the edge type's FROM/TO pair set")
			}
			if !errors.Is(loadErr, store.ErrSourceOrTargetNotFound) {
				t.Fatalf("expected ErrSourceOrTargetNotFound, got %v", loadErr)
			}
		})
	}
}

// The store load path must decode the created_at/updated_at fields the gitstore
// write path persists on every entity/edge file (gitstore EntityJSON/EdgeJSON)
// instead of fabricating time.Now() at the re-hydration moment (LEARNINGS: "a
// read path must decode every field the write path persists (never fabricate a
// value in place of persisted state)"). The load structs previously omitted the
// fields, so json.Unmarshal dropped them and the load stamped "now" — persisted
// timestamps silently shifted to the re-hydration moment. This test writes
// entity/edge files through the gitstore write path, re-hydrates them through
// the store load path, and asserts the persisted timestamps survive untouched.
func TestRehydrateMainFromFiles_PersistedTimestampsSurvive(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed rehydration from working tree")
	}
	s, err := openInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	applyTestSchema(t, s)
	ctx := context.Background()

	// Write the graph through the gitstore write path, which persists
	// created_at/updated_at on every file.
	gs, err := gitstore.New(t.TempDir())
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}

	created := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	updated := time.Date(2024, 6, 7, 8, 9, 10, 0, time.UTC)
	fromID := uuid.NewString()
	toID := uuid.NewString()
	edgeID := uuid.NewString()
	if err := gs.WriteEntityFiles(ctx, "Component", []gitstore.Entity{
		{ID: fromID, Type: "Component", Properties: map[string]string{"name": "a"}, CreatedAt: created, UpdatedAt: updated},
		{ID: toID, Type: "Component", Properties: map[string]string{"name": "b"}, CreatedAt: created, UpdatedAt: updated},
	}); err != nil {
		t.Fatalf("WriteEntityFiles: %v", err)
	}
	if err := gs.WriteEdgeFiles(ctx, "DependsOn", []gitstore.Edge{
		{ID: edgeID, Type: "DependsOn", FromEntityID: fromID, ToEntityID: toID,
			Properties: map[string]string{"strength": strengthValue}, CreatedAt: created, UpdatedAt: updated},
	}); err != nil {
		t.Fatalf("WriteEdgeFiles: %v", err)
	}

	entitiesDir, edgesDir := gs.HydrationDirs()
	if err := s.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir); err != nil {
		t.Fatalf("RehydrateMainFromFiles: %v", err)
	}

	// The load path must have accepted the timestamp-bearing files and loaded
	// the graph content.
	if _, err := s.GetEntity(ctx, fromID, ""); err != nil {
		t.Fatalf("GetEntity(from): %v", err)
	}
	if _, err := s.GetEntity(ctx, toID, ""); err != nil {
		t.Fatalf("GetEntity(to): %v", err)
	}
	if _, err := s.GetEdge(ctx, edgeID, ""); err != nil {
		t.Fatalf("GetEdge: %v", err)
	}

	// The persisted timestamps must survive the store round-trip unchanged:
	// re-reading the files via the gitstore read path (the sibling read path
	// that already decodes them) must return the exact values written, not a
	// re-hydration-time "now".
	entities, err := gs.ReadAllEntityFiles(ctx, "Component")
	if err != nil {
		t.Fatalf("ReadAllEntityFiles: %v", err)
	}
	if len(entities) != 2 {
		t.Fatalf("ReadAllEntityFiles returned %d entities, want 2", len(entities))
	}
	for _, ent := range entities {
		if !ent.CreatedAt.Equal(created) {
			t.Errorf("entity %s created_at = %v, want %v", ent.ID, ent.CreatedAt, created)
		}
		if !ent.UpdatedAt.Equal(updated) {
			t.Errorf("entity %s updated_at = %v, want %v", ent.ID, ent.UpdatedAt, updated)
		}
	}
	edges, err := gs.ReadAllEdgeFiles(ctx, "DependsOn")
	if err != nil {
		t.Fatalf("ReadAllEdgeFiles: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("ReadAllEdgeFiles returned %d edges, want 1", len(edges))
	}
	for _, edge := range edges {
		if !edge.CreatedAt.Equal(created) {
			t.Errorf("edge %s created_at = %v, want %v", edge.ID, edge.CreatedAt, created)
		}
		if !edge.UpdatedAt.Equal(updated) {
			t.Errorf("edge %s updated_at = %v, want %v", edge.ID, edge.UpdatedAt, updated)
		}
	}
}
