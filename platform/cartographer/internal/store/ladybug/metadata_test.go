package ladybug

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

func TestApplySchemaMetadataFailuresFailClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: file-backed LadybugDB store")
	}
	schema := &flowv1.Schema{EntityTypes: []*flowv1.EntityType{{
		Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}},
	}}}
	t.Run("stage before DDL", func(t *testing.T) {
		database, err := Open(t.TempDir())
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer closeStore(t, database)
		db := database.(*ladybugDB)
		stage := db.stageMetadata
		db.stageMetadata = func(string, schemaMetadata) (string, error) {
			return "", errors.New("injected stage failure")
		}
		if err := database.ApplySchema(context.Background(), schema); err == nil {
			t.Fatal("expected stage failure")
		}
		if database.TableExists("Component") {
			t.Fatal("stage failure applied DDL")
		}
		db.stageMetadata = stage
		if err := database.ApplySchema(context.Background(), schema); err != nil {
			t.Fatalf("store was not usable after pre-DDL stage failure: %v", err)
		}
	})

	t.Run("publish before DDL", func(t *testing.T) {
		dir := t.TempDir()
		database, err := Open(dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		db := database.(*ladybugDB)
		db.publishMetadata = func(string, string) error { return errors.New("injected publish failure") }
		if err := database.ApplySchema(context.Background(), schema); err == nil {
			t.Fatal("expected publish failure")
		}
		// The schema metadata is published BEFORE the DDL loop (write-ahead),
		// so a publish failure aborts before any DDL executes: the store is
		// left untouched rather than advanced past the persisted metadata.
		if database.TableExists("Component") {
			t.Fatal("publish failure applied DDL")
		}
		if db.failed {
			t.Fatal("store should not be permanently failed after publish failure")
		}
		if err := database.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		// With no metadata and no DDL, the reopen succeeds on a fresh empty
		// database instead of wedging on an advanced catalog with no metadata.
		reopened, err := Open(dir)
		if err != nil {
			t.Fatalf("reopen after publish failure must succeed: %v", err)
		}
		closeStore(t, reopened)
	})

	t.Run("directory sync after rename", func(t *testing.T) {
		dir := t.TempDir()
		database, err := Open(dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		db := database.(*ladybugDB)
		db.publishMetadata = func(temporaryPath, path string) error {
			if err := publishSchemaMetadata(temporaryPath, path); err != nil {
				return err
			}
			return errors.New("injected post-rename directory sync failure")
		}
		if err := database.ApplySchema(context.Background(), schema); err == nil {
			t.Fatal("expected post-rename failure")
		}
		if database.TableExists("Component") {
			t.Fatal("failed store exposed schema cache after post-rename failure")
		}
		if err := database.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		reopened, err := Open(dir)
		if err != nil {
			t.Fatalf("published metadata did not permit safe restart: %v", err)
		}
		closeStore(t, reopened)
	})
}

func TestWriteSchemaMetadataPublishesDurably(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schema.json")
	metadata := schemaMetadata{
		Version: schemaMetadataVersion, VectorIndexes: map[string]bool{}, VectorDimensions: map[string]int{},
	}
	if err := writeSchemaMetadata(path, metadata); err != nil {
		t.Fatalf("writeSchemaMetadata: %v", err)
	}
	if _, _, err := readSchemaMetadata(path, true); err != nil {
		t.Fatalf("read published metadata: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "schema.json" {
		t.Fatalf("staging files remained after publish: %+v", entries)
	}
}
