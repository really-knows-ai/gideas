package gitstore

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/object"
)

// ============================================================================
// T2: Entity file operations
// ============================================================================

func TestWriteEntityFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		e1ID := validUUID(t)
		e2ID := validUUID(t)
		now := time.Now().UTC().Round(time.Millisecond)

		entities := []Entity{
			{
				ID:         e1ID,
				Type:       "Component",
				Properties: map[string]string{"name": "auth-service", "version": "2.1.0"},
				CreatedAt:  now,
				UpdatedAt:  now,
			},
			{
				ID:         e2ID,
				Type:       "Component",
				Properties: map[string]string{"name": "db-service"},
				CreatedAt:  now,
				UpdatedAt:  now,
			},
		}

		if err := gs.WriteEntityFiles(ctx(), "Component", entities); err != nil {
			return err
		}

		// Verify files exist
		for _, ent := range entities {
			path := "entities/Component/" + ent.ID + ".json"
			fi, err := gs.fs.Stat(path)
			if err != nil {
				return fmt.Errorf("file %s not found: %w", path, err)
			}
			if fi.IsDir() {
				return fmt.Errorf("%s is a directory", path)
			}
		}

		// Verify content by reading back
		files, err := gs.ReadAllEntityFiles(ctx(), "Component")
		if err != nil {
			return err
		}
		if len(files) != 2 {
			return fmt.Errorf("expected 2 files, got %d", len(files))
		}

		return nil
	})
	if err != nil {
		t.Fatalf("WriteEntityFiles: %v", err)
	}
}

func TestWriteEntityFilesWithEmbedding(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		e1ID := validUUID(t)
		now := time.Now().UTC().Round(time.Millisecond)

		entities := []Entity{
			{
				ID:         e1ID,
				Type:       "Component",
				Properties: map[string]string{"name": "test"},
				Embedding:  []float32{0.12, -0.34, 0.56},
				CreatedAt:  now,
				UpdatedAt:  now,
			},
		}

		if err := gs.WriteEntityFiles(ctx(), "Component", entities); err != nil {
			return err
		}

		files, err := gs.ReadAllEntityFiles(ctx(), "Component")
		if err != nil {
			return err
		}
		if len(files) != 1 {
			return fmt.Errorf("expected 1 file, got %d", len(files))
		}
		if len(files[0].Embedding) != 3 {
			return fmt.Errorf("expected 3 embedding values, got %d", len(files[0].Embedding))
		}
		if files[0].Embedding[0] != 0.12 || files[0].Embedding[1] != -0.34 || files[0].Embedding[2] != 0.56 {
			return fmt.Errorf("unexpected embedding: %v", files[0].Embedding)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WriteEntityFilesWithEmbedding: %v", err)
	}
}

func TestWriteEntityFilesEmpty(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		return gs.WriteEntityFiles(ctx(), "Component", []Entity{})
	})
	if err != nil {
		t.Fatalf("empty write failed: %v", err)
	}
}

func TestRemoveEntityFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		e1ID := validUUID(t)
		e2ID := validUUID(t)
		now := time.Now().UTC().Round(time.Millisecond)

		entities := []Entity{
			{ID: e1ID, Type: "Component", CreatedAt: now, UpdatedAt: now},
			{ID: e2ID, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}

		if err := gs.WriteEntityFiles(ctx(), "Component", entities); err != nil {
			return err
		}
		// Commit the baseline so the committed tree pins both files before
		// removal (the durable-deletion contract is asserted against the
		// committed tree, not only the working tree).
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "pre-remove"); err != nil {
			return err
		}
		if err := committedTreeFile(gs, "entities/Component/"+e1ID+".json"); err != nil {
			return fmt.Errorf("baseline file %s missing from committed tree: %w", e1ID, err)
		}

		// Remove the first entity
		if err := gs.RemoveEntityFiles(ctx(), "Component", []string{e1ID}); err != nil {
			return err
		}

		// Verify e1 is gone, e2 remains (working tree)
		_, err1 := gs.fs.Stat("entities/Component/" + e1ID + ".json")
		if err1 == nil {
			return fmt.Errorf("removed file %s still exists", e1ID)
		}
		_, err2 := gs.fs.Stat("entities/Component/" + e2ID + ".json")
		if err2 != nil {
			return fmt.Errorf("remaining file %s missing: %w", e2ID, err2)
		}

		// AddAll + Commit the staged deletion: the committed tree must no
		// longer contain the removed file while the remaining file stays
		// present (SPEC durable-deletion contract: fs.Remove -> AddAll ->
		// Commit leaves the file gone from the committed tree).
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "post-remove"); err != nil {
			return err
		}
		if err := committedTreeFile(gs, "entities/Component/"+e1ID+".json"); !errors.Is(err, object.ErrFileNotFound) {
			return fmt.Errorf("removed file %s still present in committed tree after commit: %v", e1ID, err)
		}
		if err := committedTreeFile(gs, "entities/Component/"+e2ID+".json"); err != nil {
			return fmt.Errorf("remaining file %s missing from committed tree: %w", e2ID, err)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("RemoveEntityFiles: %v", err)
	}
}

func TestRemoveEntityFilesNonExistent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		// Removing non-existent file should not error
		return gs.RemoveEntityFiles(ctx(), "Component", []string{validUUID(t)})
	})
	if err != nil {
		t.Fatalf("RemoveEntityFiles non-existent: %v", err)
	}
}

func TestReadAllEntityFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		e1ID := validUUID(t)
		e2ID := validUUID(t)
		now := time.Now().UTC().Round(time.Millisecond)

		entities := []Entity{
			{
				ID:         e1ID,
				Type:       "Component",
				Properties: map[string]string{"name": "first"},
				CreatedAt:  now,
				UpdatedAt:  now,
			},
			{
				ID:         e2ID,
				Type:       "Component",
				Properties: map[string]string{"name": "second"},
				CreatedAt:  now,
				UpdatedAt:  now,
			},
		}

		if err := gs.WriteEntityFiles(ctx(), "Component", entities); err != nil {
			return err
		}

		files, err := gs.ReadAllEntityFiles(ctx(), "Component")
		if err != nil {
			return err
		}
		if len(files) != 2 {
			return fmt.Errorf("expected 2 files, got %d", len(files))
		}

		// Check for both names
		names := make(map[string]bool)
		for _, f := range files {
			if f.Properties != nil {
				names[f.Properties["name"]] = true
			}
		}
		if !names["first"] || !names["second"] {
			return fmt.Errorf("missing entity names: %v", names)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("ReadAllEntityFiles: %v", err)
	}
}

func TestReadAllEntityFilesEmptyDir(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		files, err := gs.ReadAllEntityFiles(ctx(), "NonExistent")
		if err != nil {
			return err
		}
		if len(files) != 0 {
			return fmt.Errorf("expected empty slice, got %d", len(files))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ReadAllEntityFilesEmptyDir: %v", err)
	}
}

func TestWriteEntityFilesTypeMismatch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: validUUID(t), Type: "Service"},
		})
		if !errors.Is(err, ErrEntityTypeMismatch) {
			return fmt.Errorf("expected ErrEntityTypeMismatch, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WriteEntityFilesTypeMismatch: %v", err)
	}
}

func TestListEntityTypes(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)

		// Write entities for two types
		entitiesA := []Entity{
			{ID: validUUID(t), Type: "Component", CreatedAt: now, UpdatedAt: now},
		}
		entitiesB := []Entity{
			{ID: validUUID(t), Type: "Service", CreatedAt: now, UpdatedAt: now},
		}

		if err := gs.WriteEntityFiles(ctx(), "Component", entitiesA); err != nil {
			return err
		}
		if err := gs.WriteEntityFiles(ctx(), "Service", entitiesB); err != nil {
			return err
		}

		types, err := gs.ListEntityTypes(ctx())
		if err != nil {
			return err
		}
		if len(types) != 2 {
			return fmt.Errorf("expected 2 types, got %d: %v", len(types), types)
		}
		if types[0] != "Component" || types[1] != "Service" {
			return fmt.Errorf("expected [Component Service], got %v", types)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("ListEntityTypes: %v", err)
	}
}

func TestListEntityTypesEmptyDir(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		types, err := gs.ListEntityTypes(ctx())
		if err != nil {
			return err
		}
		if len(types) != 0 {
			return fmt.Errorf("expected empty, got %v", types)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ListEntityTypesEmptyDir: %v", err)
	}
}

func TestListEntityTypesEmptyTypeDir(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		// Create empty type directory
		if err := gs.fs.MkdirAll("entities/EmptyType", 0755); err != nil {
			return err
		}

		types, err := gs.ListEntityTypes(ctx())
		if err != nil {
			return err
		}
		if len(types) != 0 {
			return fmt.Errorf("expected 0 types (empty dir excluded), got %v", types)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ListEntityTypesEmptyTypeDir: %v", err)
	}
}
