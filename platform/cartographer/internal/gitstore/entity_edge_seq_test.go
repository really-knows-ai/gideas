package gitstore

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// ============================================================================
// Sequence test: full write → add → commit → read-back cycle
// ============================================================================

func TestFullWriteAddCommitReadBack(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)

		// Write entity
		e1ID := validUUID(t)
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{
				ID:         e1ID,
				Type:       "Component",
				Properties: map[string]string{"name": "test"},
				CreatedAt:  now,
				UpdatedAt:  now,
			},
		}); err != nil {
			return err
		}

		// Write edge
		edgeID := validUUID(t)
		fromID := validUUID(t)
		toID := validUUID(t)
		if err := gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", []Edge{
			{
				ID:           edgeID,
				Type:         "DEPENDS_ON",
				FromEntityID: fromID,
				ToEntityID:   toID,
				Properties:   map[string]string{"weight": "high"},
				CreatedAt:    now,
				UpdatedAt:    now,
			},
		}); err != nil {
			return err
		}

		// Add and commit
		if err := gs.AddAll(ctx(), "."); err != nil {
			return err
		}
		if err := gs.Commit(ctx(), "transaction:full-cycle"); err != nil {
			return err
		}

		// Read back entity
		entities, err := gs.ReadAllEntityFiles(ctx(), "Component")
		if err != nil {
			return err
		}
		if len(entities) != 1 {
			return fmt.Errorf("expected 1 entity, got %d", len(entities))
		}
		if entities[0].Properties["name"] != "test" {
			return fmt.Errorf("expected name=test, got %q", entities[0].Properties["name"])
		}

		// Read back edge
		edges, err := gs.ReadAllEdgeFiles(ctx(), "DEPENDS_ON")
		if err != nil {
			return err
		}
		if len(edges) != 1 {
			return fmt.Errorf("expected 1 edge, got %d", len(edges))
		}
		if edges[0].Properties["weight"] != "high" {
			return fmt.Errorf("expected weight=high, got %q", edges[0].Properties["weight"])
		}

		// Verify commit exists
		found, err := gs.CommitExistsOnBranch(ctx(), "full-cycle")
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("expected commit to exist")
		}

		return nil
	})
	if err != nil {
		t.Fatalf("FullWriteAddCommitReadBack: %v", err)
	}
}

// TestReadBackTimestampsSurviveRoundTrip pins the storage-layer
// silent-divergence rule for the gitstore read paths: ReadAllEntityFiles and
// ReadAllEdgeFiles must decode the exact persisted created_at/updated_at,
// never fabricate values in place of persisted state. A regression to the
// time.Now() fabrication that previously struck the store load paths would go
// uncaught at this layer, since the sibling read-back tests assert only
// IDs/properties/embeddings.
func TestReadBackTimestampsSurviveRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		createdAt := time.Now().UTC().Round(time.Millisecond)
		updatedAt := createdAt.Add(30 * time.Minute)

		// Write an entity and an edge with distinct CreatedAt/UpdatedAt.
		e1ID := validUUID(t)
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{
				ID:        e1ID,
				Type:      "Component",
				CreatedAt: createdAt,
				UpdatedAt: updatedAt,
			},
		}); err != nil {
			return err
		}
		edgeID := validUUID(t)
		fromID := validUUID(t)
		toID := validUUID(t)
		if err := gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", []Edge{
			{
				ID:           edgeID,
				Type:         "DEPENDS_ON",
				FromEntityID: fromID,
				ToEntityID:   toID,
				CreatedAt:    createdAt,
				UpdatedAt:    updatedAt,
			},
		}); err != nil {
			return err
		}

		// Read back and assert the exact times survive.
		entities, err := gs.ReadAllEntityFiles(ctx(), "Component")
		if err != nil {
			return err
		}
		if len(entities) != 1 {
			return fmt.Errorf("expected 1 entity, got %d", len(entities))
		}
		if !entities[0].CreatedAt.Equal(createdAt) {
			return fmt.Errorf("entity CreatedAt = %v, want %v", entities[0].CreatedAt, createdAt)
		}
		if !entities[0].UpdatedAt.Equal(updatedAt) {
			return fmt.Errorf("entity UpdatedAt = %v, want %v", entities[0].UpdatedAt, updatedAt)
		}
		if entities[0].CreatedAt.Equal(entities[0].UpdatedAt) {
			return fmt.Errorf("entity CreatedAt and UpdatedAt must be distinct")
		}

		edges, err := gs.ReadAllEdgeFiles(ctx(), "DEPENDS_ON")
		if err != nil {
			return err
		}
		if len(edges) != 1 {
			return fmt.Errorf("expected 1 edge, got %d", len(edges))
		}
		if !edges[0].CreatedAt.Equal(createdAt) {
			return fmt.Errorf("edge CreatedAt = %v, want %v", edges[0].CreatedAt, createdAt)
		}
		if !edges[0].UpdatedAt.Equal(updatedAt) {
			return fmt.Errorf("edge UpdatedAt = %v, want %v", edges[0].UpdatedAt, updatedAt)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("TestReadBackTimestampsSurviveRoundTrip: %v", err)
	}
}

// TestEmptyEdgeWriteAndRead verifies empty edge write/read cycle.
func TestEmptyEdgeWriteAndRead(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		// Empty write should not error
		if err := gs.WriteEdgeFiles(ctx(), "EMPTY_TYPE", []Edge{}); err != nil {
			return err
		}
		// Reading from non-existent dir returns empty slice
		files, err := gs.ReadAllEdgeFiles(ctx(), "EMPTY_TYPE")
		if err != nil {
			return err
		}
		if len(files) != 0 {
			return fmt.Errorf("expected empty slice for non-existent edge type")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestEmptyEdgeWriteAndRead: %v", err)
	}
}

// TestReadAllEdgeFilesEmptyTypeDir tests reading an existing-but-empty type
// directory returns an empty slice (not an error), mirroring the entity-side
// TestReadAllEntityFilesEmptyTypeDir for the edge load path.
func TestReadAllEdgeFilesEmptyTypeDir(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		// Create an empty type directory (no JSON files)
		if err := gs.fs.MkdirAll("edges/EmptyType", 0755); err != nil {
			return err
		}
		files, err := gs.ReadAllEdgeFiles(ctx(), "EmptyType")
		if err != nil {
			return err
		}
		if len(files) != 0 {
			return fmt.Errorf("expected 0 files for empty type dir")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestReadAllEdgeFilesEmptyTypeDir: %v", err)
	}
}

// TestWriteEntityInvalidUUID verifies ErrInvalidUUID is returned.
func TestWriteEntityInvalidUUID(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
		entities := []Entity{
			{ID: "not-a-uuid", Type: "Component", CreatedAt: now, UpdatedAt: now},
		}
		err := gs.WriteEntityFiles(ctx(), "Component", entities)
		if !errors.Is(err, ErrInvalidUUID) {
			return fmt.Errorf("expected ErrInvalidUUID, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestWriteEntityInvalidUUID: %v", err)
	}
}

// TestWriteEdgeInvalidUUID verifies ErrInvalidUUID is returned.
func TestWriteEdgeInvalidUUID(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
		edges := []Edge{
			{
				ID: "not-a-uuid", Type: "DEPENDS_ON",
				FromEntityID: validUUID(t), ToEntityID: validUUID(t),
				CreatedAt: now, UpdatedAt: now,
			},
		}
		err := gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", edges)
		if !errors.Is(err, ErrInvalidUUID) {
			return fmt.Errorf("expected ErrInvalidUUID, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestWriteEdgeInvalidUUID: %v", err)
	}
}

// TestReadAllEntityFilesEmptyTypeDir tests reading an empty type directory
// returns an empty slice (not an error).
func TestReadAllEntityFilesEmptyTypeDir(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		// Create an empty type directory (no JSON files)
		if err := gs.fs.MkdirAll("entities/EmptyType", 0755); err != nil {
			return err
		}
		files, err := gs.ReadAllEntityFiles(ctx(), "EmptyType")
		if err != nil {
			return err
		}
		if len(files) != 0 {
			return fmt.Errorf("expected 0 files for empty type dir")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestReadAllEntityFilesEmptyTypeDir: %v", err)
	}
}

// TestWriteEntityNilEmbedding tests that nil embedding is handled correctly.
func TestWriteEntityNilEmbedding(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		e1ID := validUUID(t)
		now := time.Now().UTC().Round(time.Millisecond)

		entities := []Entity{
			{
				ID:        e1ID,
				Type:      "Component",
				Embedding: nil, // nil embedding
				CreatedAt: now,
				UpdatedAt: now,
			},
		}

		if err := gs.WriteEntityFiles(ctx(), "Component", entities); err != nil {
			return err
		}

		// Read back and verify embedding is nil
		files, err := gs.ReadAllEntityFiles(ctx(), "Component")
		if err != nil {
			return err
		}
		if len(files) != 1 {
			return fmt.Errorf("expected 1 file, got %d", len(files))
		}
		if files[0].Embedding != nil {
			return fmt.Errorf("expected nil embedding, got %v", files[0].Embedding)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestWriteEntityNilEmbedding: %v", err)
	}
}

// TestWriteEntityEmptyEmbeddingSlice tests that empty embedding slice is handled.
func TestWriteEntityEmptyEmbeddingSlice(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		e1ID := validUUID(t)
		now := time.Now().UTC().Round(time.Millisecond)

		entities := []Entity{
			{
				ID:        e1ID,
				Type:      "Component",
				Embedding: []float32{}, // empty, non-nil slice
				CreatedAt: now,
				UpdatedAt: now,
			},
		}

		if err := gs.WriteEntityFiles(ctx(), "Component", entities); err != nil {
			return err
		}

		// Read back and verify embedding is empty but not nil
		files, err := gs.ReadAllEntityFiles(ctx(), "Component")
		if err != nil {
			return err
		}
		if len(files) != 1 {
			return fmt.Errorf("expected 1 file, got %d", len(files))
		}
		if files[0].Embedding == nil {
			return fmt.Errorf("expected non-nil empty embedding")
		}
		if len(files[0].Embedding) != 0 {
			return fmt.Errorf("expected 0-length embedding, got %d", len(files[0].Embedding))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestWriteEntityEmptyEmbeddingSlice: %v", err)
	}
}
