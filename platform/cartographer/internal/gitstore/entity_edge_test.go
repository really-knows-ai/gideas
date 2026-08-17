package gitstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/google/uuid"
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

// TestReadAllEntityFilesCorrupt asserts that a malformed .json element under
// entities/<Type>/ surfaces an error from ReadAllEntityFiles (SPEC R8
// corruption recovery). This is exercised via the in-memory memfs: JSON decode
// of garbage bytes must fail rather than be silently ignored.
func TestReadAllEntityFilesCorrupt(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
		eID := validUUID(t)
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: eID, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		path := "entities/Component/" + eID + ".json"
		f, err := gs.fs.Create(path)
		if err != nil {
			return fmt.Errorf("recreate corrupt file: %w", err)
		}
		if _, err := f.Write([]byte("{not valid json")); err != nil {
			return fmt.Errorf("write garbage: %w", err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close garbage file: %w", err)
		}
		if _, err := gs.ReadAllEntityFiles(ctx(), "Component"); err == nil {
			return fmt.Errorf("expected error for corrupt entity file, got nil")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ReadAllEntityFilesCorrupt: %v", err)
	}
}

// TestReadAllEntityFilesTrailingContentRejected: a JSON entity file holding
// two concatenated documents (external corruption) must surface an error, not
// silently load only the first document. The read path decodes the full file
// content (json.Unmarshal), which rejects trailing data after the top-level
// value, whereas a streaming Decode would consume just the first value (SPEC
// R8 corruption recovery).
func TestReadAllEntityFilesTrailingContentRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
		eID := validUUID(t)
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: eID, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		path := "entities/Component/" + eID + ".json"
		// Rewrite the file with two concatenated JSON documents: the first is
		// the valid entity, the second is a trailing document that a streaming
		// decode would silently ignore.
		first := fmt.Appendf(nil,
			`{"id":%q,"type":"Component","created_at":%q,"updated_at":%q}`,
			eID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		second := fmt.Appendf(nil,
			`{"id":%q,"type":"Component","created_at":%q,"updated_at":%q}`,
			validUUID(t), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		concat := append(append([]byte{}, first...), second...)
		f, err := gs.fs.Create(path)
		if err != nil {
			return fmt.Errorf("recreate concatenated file: %w", err)
		}
		if _, err := f.Write(concat); err != nil {
			_ = f.Close()
			return fmt.Errorf("write concatenated entity file: %w", err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close concatenated entity file: %w", err)
		}
		if _, err := gs.ReadAllEntityFiles(ctx(), "Component"); err == nil {
			return fmt.Errorf("expected error for concatenated entity content, got nil")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestReadAllEntityFilesTrailingContentRejected: %v", err)
	}
}

// TestReadAllEntityFilesCaseVariantFilenameRejected: an uppercase-spelled
// <id>.json coexisting with the canonical <id>.json for one UUID (external
// corruption) must surface an error, not load the same entity twice. The write
// path only ever persists the canonical spelling (uuidutil.Validate), so a
// case-variant filename is the two-files-one-UUID hazard SPEC:162/:944 exist
// to prevent; the read path must reject it (SPEC R8 corruption recovery).
func TestReadAllEntityFilesCaseVariantFilenameRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
		eID := validUUID(t)
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: eID, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		// A second file under the uppercase spelling of the same id, with the
		// canonical id embedded: a parsed-UUID comparison would accept it.
		upperPath := "entities/Component/" + strings.ToUpper(eID) + ".json"
		ej := struct {
			ID         uuid.UUID         `json:"id"`
			Type       string            `json:"type"`
			Properties map[string]string `json:"properties,omitempty"`
			CreatedAt  time.Time         `json:"created_at"`
			UpdatedAt  time.Time         `json:"updated_at"`
		}{ID: uuid.MustParse(eID), Type: "Component", CreatedAt: now, UpdatedAt: now}
		data, err := json.Marshal(ej)
		if err != nil {
			return err
		}
		f, err := gs.fs.Create(upperPath)
		if err != nil {
			return fmt.Errorf("create case-variant entity file: %w", err)
		}
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			return fmt.Errorf("write case-variant entity file: %w", err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close case-variant entity file: %w", err)
		}
		if _, err := gs.ReadAllEntityFiles(ctx(), "Component"); err == nil {
			return fmt.Errorf("expected error for case-variant entity filename, got nil")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestReadAllEntityFilesCaseVariantFilenameRejected: %v", err)
	}
}

// TestReadAllEntityFilesIDFilenameConflict: a JSON entity file whose embedded id
// differs from its filename (external corruption) must surface an error, not be
// silently loaded under the conflicting id (SPEC R8 corruption recovery). Without
// the guard, R8 re-hydration would load the entity under a never-written id while
// the intended-UUID file disappears from view.
func TestReadAllEntityFilesIDFilenameConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
		eID := validUUID(t)
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: eID, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		// Rewrite the file's embedded id to a different (still valid) UUID under
		// the original filename, simulating filename/body divergence.
		otherID := validUUID(t)
		path := "entities/Component/" + eID + ".json"
		ej := struct {
			ID         uuid.UUID         `json:"id"`
			Type       string            `json:"type"`
			Properties map[string]string `json:"properties,omitempty"`
			CreatedAt  time.Time         `json:"created_at"`
			UpdatedAt  time.Time         `json:"updated_at"`
		}{ID: uuid.MustParse(otherID), Type: "Component", CreatedAt: now, UpdatedAt: now}
		data, err := json.Marshal(ej)
		if err != nil {
			return err
		}
		f, err := gs.fs.Create(path)
		if err != nil {
			return fmt.Errorf("recreate conflicting entity file: %w", err)
		}
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			return fmt.Errorf("write conflicting entity file: %w", err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close conflicting entity file: %w", err)
		}
		if _, err := gs.ReadAllEntityFiles(ctx(), "Component"); err == nil {
			return fmt.Errorf("expected error for conflicting entity id, got nil")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestReadAllEntityFilesIDFilenameConflict: %v", err)
	}
}

// TestReadAllEntityFilesTypeDirectoryMismatch: a JSON entity file under
// entities/<Type>/ whose embedded type disagrees with the directory (external
// corruption) must surface ErrEntityTypeMismatch, not be silently loaded under
// a never-written type. This mirrors the write path (writeEntityFile rejects
// the mismatch) and the id-vs-filename guard above.
func TestReadAllEntityFilesTypeDirectoryMismatch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
		eID := validUUID(t)
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: eID, Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		// Rewrite the file's embedded type to a different type under the
		// original directory, simulating type/body divergence.
		path := "entities/Component/" + eID + ".json"
		ej := struct {
			ID         uuid.UUID         `json:"id"`
			Type       string            `json:"type"`
			Properties map[string]string `json:"properties,omitempty"`
			CreatedAt  time.Time         `json:"created_at"`
			UpdatedAt  time.Time         `json:"updated_at"`
		}{ID: uuid.MustParse(eID), Type: "Service", CreatedAt: now, UpdatedAt: now}
		data, err := json.Marshal(ej)
		if err != nil {
			return err
		}
		f, err := gs.fs.Create(path)
		if err != nil {
			return fmt.Errorf("recreate type-mismatched entity file: %w", err)
		}
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			return fmt.Errorf("write type-mismatched entity file: %w", err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close type-mismatched entity file: %w", err)
		}
		_, err = gs.ReadAllEntityFiles(ctx(), "Component")
		if !errors.Is(err, ErrEntityTypeMismatch) {
			return fmt.Errorf("expected ErrEntityTypeMismatch, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestReadAllEntityFilesTypeDirectoryMismatch: %v", err)
	}
}

// TestReadAllEntityFilesZeroIDGuard: a JSON entity file whose embedded id is a
// zero UUID matching its zero-UUID filename (external corruption) must surface
// ErrInvalidUUID, not be silently loaded under the nil UUID. This mirrors the
// write path (writeEntityFile rejects zero/non-v4 ids via ErrInvalidUUID) and
// the sibling id-vs-filename, type-vs-directory, and edge from/to guards
// (SPEC R8 corruption recovery).
func TestReadAllEntityFilesZeroIDGuard(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		path := "entities/Component/00000000-0000-0000-0000-000000000000.json"
		ej := struct {
			ID         uuid.UUID         `json:"id"`
			Type       string            `json:"type"`
			Properties map[string]string `json:"properties,omitempty"`
			CreatedAt  time.Time         `json:"created_at"`
			UpdatedAt  time.Time         `json:"updated_at"`
		}{
			ID:        uuid.Nil,
			Type:      "Component",
			CreatedAt: time.Now().UTC().Round(time.Millisecond),
			UpdatedAt: time.Now().UTC().Round(time.Millisecond),
		}
		data, err := json.Marshal(ej)
		if err != nil {
			return err
		}
		f, err := gs.fs.Create(path)
		if err != nil {
			return fmt.Errorf("create zero-id entity file: %w", err)
		}
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			return fmt.Errorf("write zero-id entity file: %w", err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close zero-id entity file: %w", err)
		}
		if _, err := gs.ReadAllEntityFiles(ctx(), "Component"); !errors.Is(err, ErrInvalidUUID) {
			return fmt.Errorf("expected ErrInvalidUUID for zero entity id, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestReadAllEntityFilesZeroIDGuard: %v", err)
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

// TestReadAllEdgeFilesCorrupt verifies that a malformed .json element under
// edges/<Type>/ surfaces an error from ReadAllEdgeFiles (SPEC R8 corruption
// recovery). JSON decode of garbage bytes must fail rather than be silently
// ignored.
func TestReadAllEdgeFilesCorrupt(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		eID := validUUID(t)
		fromID := validUUID(t)
		toID := validUUID(t)
		now := time.Now().UTC().Round(time.Millisecond)
		if err := gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", []Edge{
			{
				ID: eID, Type: "DEPENDS_ON",
				FromEntityID: fromID, ToEntityID: toID,
				CreatedAt: now, UpdatedAt: now,
			},
		}); err != nil {
			return err
		}
		path := "edges/DEPENDS_ON/" + eID + ".json"
		f, err := gs.fs.Create(path)
		if err != nil {
			return fmt.Errorf("recreate corrupt file: %w", err)
		}
		if _, err := f.Write([]byte("{not valid json")); err != nil {
			return fmt.Errorf("write garbage: %w", err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close garbage file: %w", err)
		}
		if _, err := gs.ReadAllEdgeFiles(ctx(), "DEPENDS_ON"); err == nil {
			return fmt.Errorf("expected error for corrupt edge file, got nil")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ReadAllEdgeFilesCorrupt: %v", err)
	}
}

// TestReadAllEdgeFilesTrailingContentRejected: a JSON edge file holding two
// concatenated documents (external corruption) must surface an error, not
// silently load only the first document. The read path decodes the full file
// content (json.Unmarshal), which rejects trailing data after the top-level
// value, whereas a streaming Decode would consume just the first value (SPEC
// R8 corruption recovery).
func TestReadAllEdgeFilesTrailingContentRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
		eID := validUUID(t)
		fromID := validUUID(t)
		toID := validUUID(t)
		if err := gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", []Edge{
			{ID: eID, Type: "DEPENDS_ON", FromEntityID: fromID, ToEntityID: toID, CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		path := "edges/DEPENDS_ON/" + eID + ".json"
		// Rewrite the file with two concatenated JSON documents: the first is
		// the valid edge, the second is a trailing document that a streaming
		// decode would silently ignore.
		first := fmt.Appendf(nil,
			`{"id":%q,"type":"DEPENDS_ON","from":%q,"to":%q,"created_at":%q,"updated_at":%q}`,
			eID, fromID, toID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		second := fmt.Appendf(nil,
			`{"id":%q,"type":"DEPENDS_ON","from":%q,"to":%q,"created_at":%q,"updated_at":%q}`,
			validUUID(t), fromID, toID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		concat := append(append([]byte{}, first...), second...)
		f, err := gs.fs.Create(path)
		if err != nil {
			return fmt.Errorf("recreate concatenated file: %w", err)
		}
		if _, err := f.Write(concat); err != nil {
			_ = f.Close()
			return fmt.Errorf("write concatenated edge file: %w", err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close concatenated edge file: %w", err)
		}
		if _, err := gs.ReadAllEdgeFiles(ctx(), "DEPENDS_ON"); err == nil {
			return fmt.Errorf("expected error for concatenated edge content, got nil")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestReadAllEdgeFilesTrailingContentRejected: %v", err)
	}
}

// TestReadAllEdgeFilesCaseVariantFilenameRejected: an uppercase-spelled
// <id>.json coexisting with the canonical <id>.json for one UUID (external
// corruption) must surface an error, not load the same edge twice. The write
// path only ever persists the canonical spelling (uuidutil.Validate), so a
// case-variant filename is the two-files-one-UUID hazard SPEC:162/:944 exist
// to prevent; the read path must reject it (SPEC R8 corruption recovery).
func TestReadAllEdgeFilesCaseVariantFilenameRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
		eID := validUUID(t)
		fromID := validUUID(t)
		toID := validUUID(t)
		if err := gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", []Edge{
			{ID: eID, Type: "DEPENDS_ON", FromEntityID: fromID, ToEntityID: toID, CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		// A second file under the uppercase spelling of the same id, with the
		// canonical id embedded: a parsed-UUID comparison would accept it.
		upperPath := "edges/DEPENDS_ON/" + strings.ToUpper(eID) + ".json"
		ej := struct {
			ID           uuid.UUID         `json:"id"`
			Type         string            `json:"type"`
			FromEntityID uuid.UUID         `json:"from"`
			ToEntityID   uuid.UUID         `json:"to"`
			Properties   map[string]string `json:"properties,omitempty"`
			CreatedAt    time.Time         `json:"created_at"`
			UpdatedAt    time.Time         `json:"updated_at"`
		}{
			ID:           uuid.MustParse(eID),
			Type:         "DEPENDS_ON",
			FromEntityID: uuid.MustParse(fromID),
			ToEntityID:   uuid.MustParse(toID),
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		data, err := json.Marshal(ej)
		if err != nil {
			return err
		}
		f, err := gs.fs.Create(upperPath)
		if err != nil {
			return fmt.Errorf("create case-variant edge file: %w", err)
		}
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			return fmt.Errorf("write case-variant edge file: %w", err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close case-variant edge file: %w", err)
		}
		if _, err := gs.ReadAllEdgeFiles(ctx(), "DEPENDS_ON"); err == nil {
			return fmt.Errorf("expected error for case-variant edge filename, got nil")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestReadAllEdgeFilesCaseVariantFilenameRejected: %v", err)
	}
}

// TestReadAllEdgeFilesIDFilenameConflict: a JSON edge file whose embedded id
// differs from its filename (external corruption) must surface an error, not be
// silently loaded under the conflicting id (SPEC R8 corruption recovery).
func TestReadAllEdgeFilesIDFilenameConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		eID := validUUID(t)
		fromID := validUUID(t)
		toID := validUUID(t)
		now := time.Now().UTC().Round(time.Millisecond)
		if err := gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", []Edge{
			{ID: eID, Type: "DEPENDS_ON", FromEntityID: fromID, ToEntityID: toID, CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		// Rewrite the file's embedded id to a different (still valid) UUID under
		// the original filename, simulating filename/body divergence.
		otherID := validUUID(t)
		path := "edges/DEPENDS_ON/" + eID + ".json"
		ej := struct {
			ID           uuid.UUID         `json:"id"`
			Type         string            `json:"type"`
			FromEntityID uuid.UUID         `json:"from_entity_id"`
			ToEntityID   uuid.UUID         `json:"to_entity_id"`
			Properties   map[string]string `json:"properties,omitempty"`
			CreatedAt    time.Time         `json:"created_at"`
			UpdatedAt    time.Time         `json:"updated_at"`
		}{
			ID:           uuid.MustParse(otherID),
			Type:         "DEPENDS_ON",
			FromEntityID: uuid.MustParse(fromID),
			ToEntityID:   uuid.MustParse(toID),
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		data, err := json.Marshal(ej)
		if err != nil {
			return err
		}
		f, err := gs.fs.Create(path)
		if err != nil {
			return fmt.Errorf("recreate conflicting edge file: %w", err)
		}
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			return fmt.Errorf("write conflicting edge file: %w", err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close conflicting edge file: %w", err)
		}
		if _, err := gs.ReadAllEdgeFiles(ctx(), "DEPENDS_ON"); err == nil {
			return fmt.Errorf("expected error for conflicting edge id, got nil")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestReadAllEdgeFilesIDFilenameConflict: %v", err)
	}
}

// TestReadAllEdgeFilesTypeDirectoryMismatch: a JSON edge file under
// edges/<Type>/ whose embedded type disagrees with the directory (external
// corruption) must surface ErrEdgeTypeMismatch, not be silently loaded under a
// never-written type. This mirrors the write path (writeEdgeFile rejects the
// mismatch) and the id-vs-filename guard above.
func TestReadAllEdgeFilesTypeDirectoryMismatch(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		eID := validUUID(t)
		fromID := validUUID(t)
		toID := validUUID(t)
		now := time.Now().UTC().Round(time.Millisecond)
		if err := gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", []Edge{
			{ID: eID, Type: "DEPENDS_ON", FromEntityID: fromID, ToEntityID: toID, CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		// Rewrite the file's embedded type to a different type under the
		// original directory, simulating type/body divergence.
		path := "edges/DEPENDS_ON/" + eID + ".json"
		ej := struct {
			ID           uuid.UUID         `json:"id"`
			Type         string            `json:"type"`
			FromEntityID uuid.UUID         `json:"from_entity_id"`
			ToEntityID   uuid.UUID         `json:"to_entity_id"`
			Properties   map[string]string `json:"properties,omitempty"`
			CreatedAt    time.Time         `json:"created_at"`
			UpdatedAt    time.Time         `json:"updated_at"`
		}{
			ID:           uuid.MustParse(eID),
			Type:         "CONNECTS_TO",
			FromEntityID: uuid.MustParse(fromID),
			ToEntityID:   uuid.MustParse(toID),
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		data, err := json.Marshal(ej)
		if err != nil {
			return err
		}
		f, err := gs.fs.Create(path)
		if err != nil {
			return fmt.Errorf("recreate type-mismatched edge file: %w", err)
		}
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			return fmt.Errorf("write type-mismatched edge file: %w", err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close type-mismatched edge file: %w", err)
		}
		_, err = gs.ReadAllEdgeFiles(ctx(), "DEPENDS_ON")
		if !errors.Is(err, ErrEdgeTypeMismatch) {
			return fmt.Errorf("expected ErrEdgeTypeMismatch, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestReadAllEdgeFilesTypeDirectoryMismatch: %v", err)
	}
}

// TestReadAllEdgeFilesZeroFromToGuard: a JSON edge file whose embedded from
// or to endpoint is a zero UUID (external corruption) must surface
// ErrInvalidUUID, not be silently loaded with a nil-UUID endpoint. This
// mirrors the write path (writeEdgeFile rejects zero/non-v4 endpoints via
// ErrInvalidUUID) and the sibling id-vs-filename and type-vs-directory
// guards (SPEC R8 corruption recovery).
func TestReadAllEdgeFilesZeroFromToGuard(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		eID := validUUID(t)
		fromID := validUUID(t)
		toID := validUUID(t)
		now := time.Now().UTC().Round(time.Millisecond)
		if err := gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", []Edge{
			{ID: eID, Type: "DEPENDS_ON", FromEntityID: fromID, ToEntityID: toID, CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return err
		}
		path := "edges/DEPENDS_ON/" + eID + ".json"
		// The real EdgeJSON unmarshals from json:"from"/json:"to" keys, so the
		// rewrite must emit those exact keys for the zero UUID to genuinely
		// exercise the decode path.
		rewrite := func(from, to uuid.UUID) error {
			ej := struct {
				ID           uuid.UUID         `json:"id"`
				Type         string            `json:"type"`
				FromEntityID uuid.UUID         `json:"from"`
				ToEntityID   uuid.UUID         `json:"to"`
				Properties   map[string]string `json:"properties,omitempty"`
				CreatedAt    time.Time         `json:"created_at"`
				UpdatedAt    time.Time         `json:"updated_at"`
			}{
				ID:           uuid.MustParse(eID),
				Type:         "DEPENDS_ON",
				FromEntityID: from,
				ToEntityID:   to,
				CreatedAt:    now,
				UpdatedAt:    now,
			}
			data, err := json.Marshal(ej)
			if err != nil {
				return err
			}
			f, err := gs.fs.Create(path)
			if err != nil {
				return fmt.Errorf("recreate zero-endpoint edge file: %w", err)
			}
			if _, err := f.Write(data); err != nil {
				_ = f.Close()
				return fmt.Errorf("write zero-endpoint edge file: %w", err)
			}
			return f.Close()
		}
		// (a) Zero from endpoint.
		if err := rewrite(uuid.Nil, uuid.MustParse(toID)); err != nil {
			return err
		}
		if _, err := gs.ReadAllEdgeFiles(ctx(), "DEPENDS_ON"); !errors.Is(err, ErrInvalidUUID) {
			return fmt.Errorf("expected ErrInvalidUUID for zero from endpoint, got %v", err)
		}
		// (b) Zero to endpoint.
		if err := rewrite(uuid.MustParse(fromID), uuid.Nil); err != nil {
			return err
		}
		if _, err := gs.ReadAllEdgeFiles(ctx(), "DEPENDS_ON"); !errors.Is(err, ErrInvalidUUID) {
			return fmt.Errorf("expected ErrInvalidUUID for zero to endpoint, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestReadAllEdgeFilesZeroFromToGuard: %v", err)
	}
}

// TestReadAllEdgeFilesZeroIDGuard: a JSON edge file whose embedded id is a
// zero UUID matching its zero-UUID filename (external corruption) must surface
// ErrInvalidUUID, not be silently loaded under the nil UUID. This mirrors the
// write path (writeEdgeFile rejects zero/non-v4 ids via ErrInvalidUUID) and
// the sibling id-vs-filename, type-vs-directory, and from/to guards (SPEC R8
// corruption recovery).
func TestReadAllEdgeFilesZeroIDGuard(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		fromID := validUUID(t)
		toID := validUUID(t)
		now := time.Now().UTC().Round(time.Millisecond)
		path := "edges/DEPENDS_ON/00000000-0000-0000-0000-000000000000.json"
		ej := struct {
			ID           uuid.UUID         `json:"id"`
			Type         string            `json:"type"`
			FromEntityID uuid.UUID         `json:"from"`
			ToEntityID   uuid.UUID         `json:"to"`
			Properties   map[string]string `json:"properties,omitempty"`
			CreatedAt    time.Time         `json:"created_at"`
			UpdatedAt    time.Time         `json:"updated_at"`
		}{
			ID:           uuid.Nil,
			Type:         "DEPENDS_ON",
			FromEntityID: uuid.MustParse(fromID),
			ToEntityID:   uuid.MustParse(toID),
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		data, err := json.Marshal(ej)
		if err != nil {
			return err
		}
		f, err := gs.fs.Create(path)
		if err != nil {
			return fmt.Errorf("create zero-id edge file: %w", err)
		}
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			return fmt.Errorf("write zero-id edge file: %w", err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close zero-id edge file: %w", err)
		}
		if _, err := gs.ReadAllEdgeFiles(ctx(), "DEPENDS_ON"); !errors.Is(err, ErrInvalidUUID) {
			return fmt.Errorf("expected ErrInvalidUUID for zero edge id, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestReadAllEdgeFilesZeroIDGuard: %v", err)
	}
}

// TestReadAllFilesNonRFC4122VariantGuard: a JSON entity or edge file whose
// embedded id (or edge endpoint) is a version-4 UUID with a non-RFC4122
// variant nibble (external corruption) must surface ErrInvalidUUID, not be
// silently loaded. uuidutil.Validate gates the write path on both Version()==4
// and Variant()==uuid.RFC4122, so a canonical-spelled v4 UUID whose variant
// nibble falls outside RFC4122's 8-b would otherwise load on the read path
// while being rejected on write — a read/write validation divergence from the
// SPEC:162 canonical RFC4122 §3 form. The fixture's variant nibble c
// (Microsoft variant, 110x) keeps Version()==4 while failing the variant
// check, isolating that dimension from the sibling zero-id guard tests.
func TestReadAllFilesNonRFC4122VariantGuard(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: real go-git repository (memfs)")
	}
	gs := setupTestStore(t)
	// 550e8400-e29b-41d4-c716-446655440000: version nibble 4, variant nibble c.
	badID := "550e8400-e29b-41d4-c716-446655440000"
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)

		// (a) Entity file whose embedded id is a v4 non-RFC4122-variant UUID
		// matching its filename (so only the variant dimension can trip).
		eej := struct {
			ID         uuid.UUID         `json:"id"`
			Type       string            `json:"type"`
			Properties map[string]string `json:"properties,omitempty"`
			CreatedAt  time.Time         `json:"created_at"`
			UpdatedAt  time.Time         `json:"updated_at"`
		}{
			ID:        uuid.MustParse(badID),
			Type:      "Component",
			CreatedAt: now,
			UpdatedAt: now,
		}
		data, err := json.Marshal(eej)
		if err != nil {
			return err
		}
		f, err := gs.fs.Create("entities/Component/" + badID + ".json")
		if err != nil {
			return fmt.Errorf("create bad-variant entity file: %w", err)
		}
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			return fmt.Errorf("write bad-variant entity file: %w", err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("close bad-variant entity file: %w", err)
		}
		if _, err := gs.ReadAllEntityFiles(ctx(), "Component"); !errors.Is(err, ErrInvalidUUID) {
			return fmt.Errorf("expected ErrInvalidUUID for bad-variant entity id, got %v", err)
		}

		// (b-d) Edge files: bad-variant id / from / to endpoints must each
		// surface ErrInvalidUUID.
		eID := validUUID(t)
		fromID := validUUID(t)
		toID := validUUID(t)
		eUUID := uuid.MustParse(eID)
		fromUUID := uuid.MustParse(fromID)
		toUUID := uuid.MustParse(toID)
		badUUID := uuid.MustParse(badID)
		rewrite := func(path string, id, from, to uuid.UUID) error {
			ej := struct {
				ID           uuid.UUID         `json:"id"`
				Type         string            `json:"type"`
				FromEntityID uuid.UUID         `json:"from"`
				ToEntityID   uuid.UUID         `json:"to"`
				Properties   map[string]string `json:"properties,omitempty"`
				CreatedAt    time.Time         `json:"created_at"`
				UpdatedAt    time.Time         `json:"updated_at"`
			}{
				ID:           id,
				Type:         "DEPENDS_ON",
				FromEntityID: from,
				ToEntityID:   to,
				CreatedAt:    now,
				UpdatedAt:    now,
			}
			data, err := json.Marshal(ej)
			if err != nil {
				return err
			}
			f, err := gs.fs.Create(path)
			if err != nil {
				return fmt.Errorf("recreate edge file: %w", err)
			}
			if _, err := f.Write(data); err != nil {
				_ = f.Close()
				return fmt.Errorf("write edge file: %w", err)
			}
			return f.Close()
		}
		// (b) Bad-variant edge id (filename must match the embedded id so the
		// variant check, not the id-vs-filename guard, is exercised).
		if err := rewrite("edges/DEPENDS_ON/"+badID+".json", badUUID, fromUUID, toUUID); err != nil {
			return err
		}
		if _, err := gs.ReadAllEdgeFiles(ctx(), "DEPENDS_ON"); !errors.Is(err, ErrInvalidUUID) {
			return fmt.Errorf("expected ErrInvalidUUID for bad-variant edge id, got %v", err)
		}
		// (c) Bad-variant from endpoint.
		if err := rewrite("edges/DEPENDS_ON/"+eID+".json", eUUID, badUUID, toUUID); err != nil {
			return err
		}
		if _, err := gs.ReadAllEdgeFiles(ctx(), "DEPENDS_ON"); !errors.Is(err, ErrInvalidUUID) {
			return fmt.Errorf("expected ErrInvalidUUID for bad-variant from endpoint, got %v", err)
		}
		// (d) Bad-variant to endpoint.
		if err := rewrite("edges/DEPENDS_ON/"+eID+".json", eUUID, fromUUID, badUUID); err != nil {
			return err
		}
		if _, err := gs.ReadAllEdgeFiles(ctx(), "DEPENDS_ON"); !errors.Is(err, ErrInvalidUUID) {
			return fmt.Errorf("expected ErrInvalidUUID for bad-variant to endpoint, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestReadAllFilesNonRFC4122VariantGuard: %v", err)
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
