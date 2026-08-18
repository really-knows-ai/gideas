package ladybug

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

func TestPersistence_AcrossCloseReopen(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	applyTestSchema(t, s)

	e, err := s.CreateEntity(context.Background(), "Component", "",
		map[string]string{"name": "persist"}, nil, "")
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s2)

	got, err := s2.GetEntity(context.Background(), e.Id, "")
	if err != nil {
		t.Fatalf("GetEntity after reopen: %v", err)
	}
	if got.Properties["name"] != "persist" {
		t.Errorf("name = %q, want %q", got.Properties["name"], "persist")
	}
}

func TestPersistence_SchemaSurvivesReopen(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	applyTestSchema(t, s)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s2)

	if !s2.TableExists("Component") {
		t.Error("expected Component table to survive reopen")
	}
	if !s2.TableExists("Document") {
		t.Error("expected Document table to survive reopen")
	}
}

func TestPersistence_CompleteSchemaMetadataSurvivesReopen(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Service", EnableVectorIndex: true,
				Properties: []*flowv1.Property{{Name: "name", Type: "string", Required: true}},
				Rules:      []*flowv1.ConnectionRule{{CanConnectTo: []string{"Component"}, Using: []string{"DEPENDS_ON"}}},
			},
			{Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string", Required: true}}},
		},
		EdgeTypes: []*flowv1.EdgeType{{
			Name: "DEPENDS_ON", Properties: []*flowv1.Property{{Name: "weight", Type: "string", Required: true}},
		}},
	}
	if err := s.ApplySchema(context.Background(), schema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	if err := s.CreateBranchDB(context.Background(), "tx-metadata"); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.ReplicateSchemaToBranch(context.Background(), "tx-metadata"); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer closeStore(t, reopened)
	service, ok := reopened.EntityType("Service")
	if !ok || !service.EnableVectorIndex || len(service.Properties) != 1 || !service.Properties[0].Required {
		t.Fatalf("incomplete reopened entity definition: %+v", service)
	}
	if len(service.Rules) != 1 || len(service.Rules[0].CanConnectTo) != 1 ||
		service.Rules[0].CanConnectTo[0] != "Component" || len(service.Rules[0].Using) != 1 ||
		service.Rules[0].Using[0] != "DEPENDS_ON" {
		t.Fatalf("incomplete reopened rules: %+v", service.Rules)
	}
	edge, ok := reopened.EdgeType("DEPENDS_ON")
	if !ok || len(edge.Properties) != 1 || !edge.Properties[0].Required {
		t.Fatalf("incomplete reopened edge definition: %+v", edge)
	}
	_, err = reopened.CreateEntity(context.Background(), "Component", "", map[string]string{}, nil, "tx-metadata")
	if !errors.Is(err, store.ErrMissingRequiredProperty) {
		t.Fatalf("reopened branch accepted missing required property: %v", err)
	}
}

func TestPersistence_MissingOrCorruptSchemaMetadataFailsClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	for _, test := range []struct {
		name   string
		mutate func(string) error
	}{
		{"missing", os.Remove},
		{"corrupt", func(path string) error { return os.WriteFile(path, []byte("{"), 0600) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			s, err := Open(dir)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			applyTestSchema(t, s)
			if err := s.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if err := test.mutate(filepath.Join(dir, "schema.json")); err != nil {
				t.Fatalf("mutate metadata: %v", err)
			}
			if reopened, err := Open(dir); err == nil {
				_ = reopened.Close()
				t.Fatal("expected schema metadata failure")
			}
		})
	}
}

// SPEC R9 recovery point 4 rolls back a transaction whose branch .lbug is
// absent; this pins the sibling crash window inside ReplicateSchemaToBranch.
// The branch schema metadata (branches/<txID>.schema.json) is written only
// after ReplicateSchemaToBranch's DDL loop, so a crash after ≥1 table is
// created but before the metadata write leaves a branch .lbug with a
// non-empty catalog and no schema metadata. The client never received the
// txID (the BeginTransaction response is sent only after the metadata write
// succeeds), so the transaction is provably harmless and the reopen must
// classify the partial branch exactly like the absent-.lbug case —
// ErrBranchNotFound, which RecoverOpenTransactions turns into a rollback via
// cleanupTransaction/DropBranchDB — instead of surfacing a hard error that
// bricks startup. A present-but-corrupt metadata file stays a loud failure
// (the guard matches only the not-exist read error).
func TestPersistence_MissingBranchSchemaMetadataRollsBackOnReopen(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	dir := t.TempDir()
	ctx := context.Background()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	applyTestSchema(t, s)
	if err := s.CreateBranchDB(ctx, "tx-missing-metadata"); err != nil {
		t.Fatalf("CreateBranchDB: %v", err)
	}
	if err := s.ReplicateSchemaToBranch(ctx, "tx-missing-metadata"); err != nil {
		t.Fatalf("ReplicateSchemaToBranch: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Remove the branch schema metadata: the crash residue of a replication
	// interrupted before the metadata write is a non-empty catalog with an
	// absent branches/<txID>.schema.json (on disk indistinguishable from a
	// metadata file lost after the fact).
	metadataPath := filepath.Join(dir, "branches", "tx-missing-metadata.schema.json")
	if err := os.Remove(metadataPath); err != nil {
		t.Fatalf("remove branch metadata: %v", err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen main: %v", err)
	}
	defer closeStore(t, reopened)
	// The partial branch must be classified for rollback, not hard-failed.
	if _, err := reopened.DumpAllEntities(ctx, "tx-missing-metadata"); !errors.Is(err, store.ErrBranchNotFound) {
		t.Fatalf("DumpAllEntities = %v, want ErrBranchNotFound (rollback classification)", err)
	}
	// The rollback path (RecoverOpenTransactions' cleanup) drops the branch via
	// DropBranchDB; it must succeed and remove the persisted .lbug.
	if err := reopened.DropBranchDB(ctx, "tx-missing-metadata"); err != nil {
		t.Fatalf("DropBranchDB: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "branches", "tx-missing-metadata.lbug")); !os.IsNotExist(err) {
		t.Fatalf("branch .lbug was not removed by rollback: %v", err)
	}
	// After the rollback the branch is fully absent: classification stays
	// ErrBranchNotFound, never a resurrected partial branch.
	if _, err := reopened.DumpAllEntities(ctx, "tx-missing-metadata"); !errors.Is(err, store.ErrBranchNotFound) {
		t.Fatalf("DumpAllEntities after rollback = %v, want ErrBranchNotFound", err)
	}
}

func TestPersistence_CatalogMetadataMismatchFailsClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	for _, test := range []struct {
		name   string
		mutate func(*schemaMetadata)
	}{
		{"property name", func(metadata *schemaMetadata) {
			metadata.EntityTypes[0].Properties[0].Name = "renamed"
		}},
		{"property type", func(metadata *schemaMetadata) {
			metadata.EntityTypes[0].Properties[0].Type = driftedColumnType
		}},
		{"relationship endpoint", func(metadata *schemaMetadata) {
			metadata.EntityTypes[1].Rules[0].CanConnectTo = []string{"Service"}
		}},
		{"vector state", func(metadata *schemaMetadata) {
			metadata.VectorIndexes["Service"] = true
			metadata.VectorDimensions["Service"] = 3
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			s, err := Open(dir)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			schema := &flowv1.Schema{
				EntityTypes: []*flowv1.EntityType{
					{Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
					{
						Name: "Service", EnableVectorIndex: true,
						Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
						Rules: []*flowv1.ConnectionRule{{
							CanConnectTo: []string{"Component"}, Using: []string{"DEPENDS_ON"},
						}},
					},
				},
				EdgeTypes: []*flowv1.EdgeType{{
					Name: "DEPENDS_ON", Properties: []*flowv1.Property{{Name: "weight", Type: "string"}},
				}},
			}
			if err := s.ApplySchema(context.Background(), schema); err != nil {
				t.Fatalf("ApplySchema: %v", err)
			}
			if err := s.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			metadataPath := filepath.Join(dir, "schema.json")
			data, err := os.ReadFile(metadataPath)
			if err != nil {
				t.Fatalf("read metadata: %v", err)
			}
			var metadata schemaMetadata
			if err := json.Unmarshal(data, &metadata); err != nil {
				t.Fatalf("parse metadata: %v", err)
			}
			test.mutate(&metadata)
			data, err = json.Marshal(metadata)
			if err != nil {
				t.Fatalf("marshal changed metadata: %v", err)
			}
			if err := os.WriteFile(metadataPath, data, 0600); err != nil {
				t.Fatalf("write changed metadata: %v", err)
			}
			if reopened, err := Open(dir); err == nil {
				_ = reopened.Close()
				t.Fatal("expected catalog mismatch")
			}
		})
	}
}

func TestPersistence_ValidMetadataRestoresEmptyCatalog(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	applyTestSchema(t, s)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "main.lbug")); err != nil {
		t.Fatalf("remove main database: %v", err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("restore empty catalog from metadata: %v", err)
	}
	defer closeStore(t, reopened)
	if !reopened.TableExists("Component") {
		t.Fatal("metadata did not restore Component table")
	}
}
