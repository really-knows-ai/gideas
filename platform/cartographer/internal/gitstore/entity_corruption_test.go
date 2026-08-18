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
