package gitstore

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/object"
)

// ============================================================================
// T3: Edge file operations
// ============================================================================

func TestWriteEdgeFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		e1ID := validUUID(t)
		e2ID := validUUID(t)
		fromID := validUUID(t)
		toID := validUUID(t)
		now := time.Now().UTC().Round(time.Millisecond)

		edges := []Edge{
			{
				ID:           e1ID,
				Type:         "DEPENDS_ON",
				FromEntityID: fromID,
				ToEntityID:   toID,
				Properties:   map[string]string{"weight": "high"},
				CreatedAt:    now,
				UpdatedAt:    now,
			},
			{
				ID:           e2ID,
				Type:         "DEPENDS_ON",
				FromEntityID: toID,
				ToEntityID:   fromID,
				CreatedAt:    now,
				UpdatedAt:    now,
			},
		}

		if err := gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", edges); err != nil {
			return err
		}

		for _, edge := range edges {
			path := "edges/DEPENDS_ON/" + edge.ID + ".json"
			if _, err := gs.fs.Stat(path); err != nil {
				return fmt.Errorf("file %s not found: %w", path, err)
			}
		}

		return nil
	})
	if err != nil {
		t.Fatalf("WriteEdgeFiles: %v", err)
	}
}

func TestWriteEdgeFilesEmpty(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		return gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", []Edge{})
	})
	if err != nil {
		t.Fatalf("empty edge write failed: %v", err)
	}
}

func TestRemoveEdgeFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		e1ID := validUUID(t)
		e2ID := validUUID(t)
		now := time.Now().UTC().Round(time.Millisecond)

		edges := []Edge{
			{
				ID: e1ID, Type: "DEPENDS_ON",
				FromEntityID: validUUID(t), ToEntityID: validUUID(t),
				CreatedAt: now, UpdatedAt: now,
			},
			{
				ID: e2ID, Type: "DEPENDS_ON",
				FromEntityID: validUUID(t), ToEntityID: validUUID(t),
				CreatedAt: now, UpdatedAt: now,
			},
		}

		if err := gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", edges); err != nil {
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
		if err := committedTreeFile(gs, "edges/DEPENDS_ON/"+e1ID+".json"); err != nil {
			return fmt.Errorf("baseline file %s missing from committed tree: %w", e1ID, err)
		}

		if err := gs.RemoveEdgeFiles(ctx(), "DEPENDS_ON", []string{e1ID}); err != nil {
			return err
		}

		if _, err := gs.fs.Stat("edges/DEPENDS_ON/" + e1ID + ".json"); err == nil {
			return fmt.Errorf("removed edge file still exists")
		}
		if _, err := gs.fs.Stat("edges/DEPENDS_ON/" + e2ID + ".json"); err != nil {
			return fmt.Errorf("remaining edge file missing")
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
		if err := committedTreeFile(gs, "edges/DEPENDS_ON/"+e1ID+".json"); !errors.Is(err, object.ErrFileNotFound) {
			return fmt.Errorf("removed edge file %s still present in committed tree after commit: %v", e1ID, err)
		}
		if err := committedTreeFile(gs, "edges/DEPENDS_ON/"+e2ID+".json"); err != nil {
			return fmt.Errorf("remaining edge file %s missing from committed tree: %w", e2ID, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("RemoveEdgeFiles: %v", err)
	}
}

func TestRemoveEdgeFilesNonExistent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		return gs.RemoveEdgeFiles(ctx(), "DEPENDS_ON", []string{validUUID(t)})
	})
	if err != nil {
		t.Fatalf("RemoveEdgeFiles non-existent: %v", err)
	}
}

func TestReadAllEdgeFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		e1ID := validUUID(t)
		now := time.Now().UTC().Round(time.Millisecond)
		fromID := validUUID(t)
		toID := validUUID(t)

		edges := []Edge{
			{
				ID: e1ID, Type: "DEPENDS_ON",
				FromEntityID: fromID, ToEntityID: toID,
				Properties: map[string]string{"weight": "low"},
				CreatedAt:  now, UpdatedAt: now,
			},
		}

		if err := gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", edges); err != nil {
			return err
		}

		files, err := gs.ReadAllEdgeFiles(ctx(), "DEPENDS_ON")
		if err != nil {
			return err
		}
		if len(files) != 1 {
			return fmt.Errorf("expected 1 file, got %d", len(files))
		}
		if files[0].Properties["weight"] != "low" {
			return fmt.Errorf("expected weight=low, got %q", files[0].Properties["weight"])
		}
		if files[0].FromEntityID != fromID {
			return fmt.Errorf("expected FromEntityID=%s, got %s", fromID, files[0].FromEntityID)
		}
		if files[0].ToEntityID != toID {
			return fmt.Errorf("expected ToEntityID=%s, got %s", toID, files[0].ToEntityID)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("ReadAllEdgeFiles: %v", err)
	}
}

// TestWriteEdgeFilesTypeMismatch asserts that writing an edge whose Type
// differs from the batch type returns ErrEdgeTypeMismatch (edge.go:109).
func TestWriteEdgeFilesTypeMismatch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		err := gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", []Edge{
			{
				ID:           validUUID(t),
				Type:         "CONNECTS_TO",
				FromEntityID: validUUID(t),
				ToEntityID:   validUUID(t),
			},
		})
		if !errors.Is(err, ErrEdgeTypeMismatch) {
			return fmt.Errorf("expected ErrEdgeTypeMismatch, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WriteEdgeFilesTypeMismatch: %v", err)
	}
}

func TestListEdgeTypes(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
		fromID := validUUID(t)
		toID := validUUID(t)

		edgesA := []Edge{
			{ID: validUUID(t), Type: "DEPENDS_ON", FromEntityID: fromID, ToEntityID: toID, CreatedAt: now, UpdatedAt: now},
		}
		edgesB := []Edge{
			{ID: validUUID(t), Type: "CONNECTS_TO", FromEntityID: fromID, ToEntityID: toID, CreatedAt: now, UpdatedAt: now},
		}

		if err := gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", edgesA); err != nil {
			return err
		}
		if err := gs.WriteEdgeFiles(ctx(), "CONNECTS_TO", edgesB); err != nil {
			return err
		}

		types, err := gs.ListEdgeTypes(ctx())
		if err != nil {
			return err
		}
		if len(types) != 2 {
			return fmt.Errorf("expected 2 edge types, got %d", len(types))
		}

		return nil
	})
	if err != nil {
		t.Fatalf("ListEdgeTypes: %v", err)
	}
}

func TestListEdgeTypesEmptyDir(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		types, err := gs.ListEdgeTypes(ctx())
		if err != nil {
			return err
		}
		if len(types) != 0 {
			return fmt.Errorf("expected empty, got %v", types)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ListEdgeTypesEmptyDir: %v", err)
	}
}

func TestListEdgeTypesEmptyTypeDir(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		if err := gs.fs.MkdirAll("edges/EmptyType", 0755); err != nil {
			return err
		}
		types, err := gs.ListEdgeTypes(ctx())
		if err != nil {
			return err
		}
		if len(types) != 0 {
			return fmt.Errorf("expected 0 types (empty dir excluded), got %v", types)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ListEdgeTypesEmptyTypeDir: %v", err)
	}
}
