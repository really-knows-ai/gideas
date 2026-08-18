package ladybug

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// WipeSchema drops every schema table and clears the in-memory schema cache,
// but a store primitive must not leave stale branch connections or persisted
// branch records dangling: an open branch connection cached the dropped tables
// (a later branch op would error on a vanished schema), and a persisted
// branches/<txID>.state.json would let SaveBranchTransactionState re-register a
// branch whose database and schema are gone. The store primitive must drop them
// itself — defense-in-depth behind the service-layer FAILED_PRECONDITION
// (SPEC row ~915) which only guards a live transaction.
func TestWipeSchema_ClosesOpenBranchesAndRemovesPersistedState(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	schema := &flowv1.Schema{EntityTypes: []*flowv1.EntityType{{
		Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
	}}}
	if err := s.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	const txID = "00000000-0000-4000-a000-000000000001"
	if err := s.CreateBranchDB(ctx, txID); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.ReplicateSchemaToBranch(ctx, txID); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}
	if err := s.SaveBranchTransactionState(ctx, txID, store.BranchTransactionState{
		MainHeadAtLastSync: "head", SchemaHash: "schema",
	}); err != nil {
		t.Fatalf("SaveBranchTransactionState: %v", err)
	}
	// Confirm the durable branch state file and open connection exist pre-wipe.
	if _, err := os.Stat(filepath.Join(dir, "branches", txID+".state.json")); err != nil {
		t.Fatalf("expected persisted branch state before wipe: %v", err)
	}
	ldb := s.(*ladybugDB)
	if _, ok := ldb.branches[txID]; !ok {
		t.Fatal("expected branch connection registered before wipe")
	}

	if err := s.WipeSchema(ctx); err != nil {
		t.Fatalf("WipeSchema: %v", err)
	}
	// The open branch connection was closed and removed from the registry.
	if _, ok := ldb.branches[txID]; ok {
		t.Fatal("open branch survived WipeSchema")
	}
	// The durable branch state and database records were removed.
	if _, err := os.Stat(filepath.Join(dir, "branches", txID+".state.json")); !os.IsNotExist(err) {
		t.Fatalf("persisted branch state survived WipeSchema: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "branches", txID+".lbug")); !os.IsNotExist(err) {
		t.Fatalf("persisted branch database survived WipeSchema: %v", err)
	}
	// A post-wipe branch operation can no longer be issued against the stale
	// branch (previously it would operate against dropped tables).
	if err := s.ReplicateSchemaToBranch(ctx, txID); !errors.Is(err, store.ErrBranchNotFound) {
		t.Fatalf("expected ErrBranchNotFound after wipe, got %v", err)
	}
	// SaveBranchTransactionState can no longer re-register the stale branch.
	if err := s.SaveBranchTransactionState(ctx, txID, store.BranchTransactionState{
		MainHeadAtLastSync: "head", SchemaHash: "schema",
	}); !errors.Is(err, store.ErrBranchNotFound) {
		t.Fatalf("expected ErrBranchNotFound re-registering wiped branch state, got %v", err)
	}
}

func TestWipeSchema_ThenApplySchema_EntityOnlyTransaction(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Apply initial schema with entity and edge types.
	schema1 := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Service", Rules: []*flowv1.ConnectionRule{
				{CanConnectTo: []string{"Component"}, Using: []string{"DEPENDS_ON"}},
			}},
			{Name: "Component"},
		},
		EdgeTypes: []*flowv1.EdgeType{{Name: "DEPENDS_ON"}},
	}
	if err := s.ApplySchema(ctx, schema1); err != nil {
		t.Fatalf("first ApplySchema: %v", err)
	}

	// Create some data.
	svc, err := s.CreateEntity(ctx, "Service", "", map[string]string{}, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	comp, err := s.CreateEntity(ctx, "Component", "", map[string]string{}, nil, "main")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if _, err := s.CreateEdge(ctx, "DEPENDS_ON", svc.Id, comp.Id, nil, "main"); err != nil {
		t.Fatalf("CreateEdge: %v", err)
	}

	// WipeSchema.
	if err := s.WipeSchema(ctx); err != nil {
		t.Fatalf("WipeSchema: %v", err)
	}

	// Apply new schema with only entity types (no edges).
	schema2 := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{{
			Name: "Document",
			Properties: []*flowv1.Property{
				{Name: "title", Type: "string"},
			},
		}},
	}
	if err := s.ApplySchema(ctx, schema2); err != nil {
		t.Fatalf("second ApplySchema: %v", err)
	}

	// Entity-only transaction: create, commit, restart.
	txID := "00000000-0000-4000-a000-000000000001"
	if err := s.CreateBranchDB(context.Background(), txID); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.ReplicateSchemaToBranch(context.Background(), txID); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}
	doc, err := s.CreateEntity(ctx, "Document", "", map[string]string{"title": "tx-doc"}, nil, txID)
	if err != nil {
		t.Fatalf("CreateEntity on branch: %v", err)
	}
	if err := s.RehydrateFromBranch(ctx, txID); err != nil {
		t.Fatalf("RehydrateFromBranch: %v", err)
	}
	if err := s.DropBranchDB(context.Background(), txID); err != nil {
		t.Fatalf("DropBranchDB: %v", err)
	}

	// Verify entity is in main.
	got, err := s.GetEntity(ctx, doc.Id, "main")
	if err != nil {
		t.Fatalf("GetEntity after commit: %v", err)
	}
	if got.Properties["title"] != "tx-doc" {
		t.Fatalf("expected title=tx-doc, got %v", got.Properties)
	}

	// Close and reopen.
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer closeStore(t, s2)

	// Verify schema and data survived.
	if !s2.TableExists("Document") {
		t.Fatal("Document table missing after reopen")
	}
	got2, err := s2.GetEntity(ctx, doc.Id, "main")
	if err != nil {
		t.Fatalf("GetEntity after reopen: %v", err)
	}
	if got2.Properties["title"] != "tx-doc" {
		t.Fatalf("expected title=tx-doc after reopen, got %v", got2.Properties)
	}
}
