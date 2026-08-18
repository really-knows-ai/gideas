package gitstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

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
