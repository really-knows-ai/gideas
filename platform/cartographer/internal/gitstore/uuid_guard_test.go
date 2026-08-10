package gitstore

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// nonCanonicalSpellings are the four spellings of a single valid RFC4122 v4
// UUID that google/uuid.Parse accepts but the canonical RFC4122 §3 string
// form (the lowercase 8-4-4-4-12 dashed string) does not. SPEC:162/SPEC:944
// reject them all with INVALID_ARGUMENT because the gitstore persists IDs
// verbatim as <id>.json: two spellings of one UUID would become two files for
// one ID and bypass the CreateEntity ALREADY_EXISTS check.
var nonCanonicalSpellings = []string{
	"550E8400-E29B-41D4-A716-446655440000",          // uppercase hex
	"550e8400e29b41d4a716446655440000",              // 32-char no-hyphen
	"{550e8400-e29b-41d4-a716-446655440000}",        // braced {...}
	"urn:uuid:550e8400-e29b-41d4-a716-446655440000", // urn:uuid: prefix
}

// TestWriteEntityFilesRejectsNonCanonicalUUID pins the canonical-form guard
// on the entity write path (writeEntityFile): every non-canonical spelling
// must surface ErrInvalidUUID before anything is persisted, mirroring the
// store's write-path gate (uuidutil.Validate) so the gitstore can never
// create a second file for one UUID.
func TestWriteEntityFilesRejectsNonCanonicalUUID(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)

		// Positive control: the canonical spelling must still pass.
		if err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
			{ID: "550e8400-e29b-41d4-a716-446655440000", Type: "Component", CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return fmt.Errorf("canonical id rejected: %w", err)
		}

		for _, id := range nonCanonicalSpellings {
			err := gs.WriteEntityFiles(ctx(), "Component", []Entity{
				{ID: id, Type: "Component", CreatedAt: now, UpdatedAt: now},
			})
			if !errors.Is(err, ErrInvalidUUID) {
				return fmt.Errorf("WriteEntityFiles(%q) = %v, want ErrInvalidUUID", id, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestWriteEntityFilesRejectsNonCanonicalUUID: %v", err)
	}
}

// TestWriteEdgeFilesRejectsNonCanonicalUUID pins the canonical-form guard on
// the edge write path (writeEdgeFile) for all three UUID fields: the edge ID
// (persisted verbatim as <id>.json) and both from/to endpoints.
func TestWriteEdgeFilesRejectsNonCanonicalUUID(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		now := time.Now().UTC().Round(time.Millisecond)
		canonical := "550e8400-e29b-41d4-a716-446655440000"

		// Positive control: canonical spellings must still pass.
		if err := gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", []Edge{
			{ID: canonical, Type: "DEPENDS_ON", FromEntityID: canonical, ToEntityID: canonical, CreatedAt: now, UpdatedAt: now},
		}); err != nil {
			return fmt.Errorf("canonical ids rejected: %w", err)
		}

		for _, id := range nonCanonicalSpellings {
			// Non-canonical edge ID.
			err := gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", []Edge{
				{ID: id, Type: "DEPENDS_ON", FromEntityID: canonical, ToEntityID: canonical, CreatedAt: now, UpdatedAt: now},
			})
			if !errors.Is(err, ErrInvalidUUID) {
				return fmt.Errorf("WriteEdgeFiles(id=%q) = %v, want ErrInvalidUUID", id, err)
			}
			// Non-canonical from endpoint.
			err = gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", []Edge{
				{ID: canonical, Type: "DEPENDS_ON", FromEntityID: id, ToEntityID: canonical, CreatedAt: now, UpdatedAt: now},
			})
			if !errors.Is(err, ErrInvalidUUID) {
				return fmt.Errorf("WriteEdgeFiles(from=%q) = %v, want ErrInvalidUUID", id, err)
			}
			// Non-canonical to endpoint.
			err = gs.WriteEdgeFiles(ctx(), "DEPENDS_ON", []Edge{
				{ID: canonical, Type: "DEPENDS_ON", FromEntityID: canonical, ToEntityID: id, CreatedAt: now, UpdatedAt: now},
			})
			if !errors.Is(err, ErrInvalidUUID) {
				return fmt.Errorf("WriteEdgeFiles(to=%q) = %v, want ErrInvalidUUID", id, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestWriteEdgeFilesRejectsNonCanonicalUUID: %v", err)
	}
}

// TestCreateBranchRejectsNonCanonicalUUID pins the canonical-form guard on the
// branch-creation path (CreateBranch): every non-canonical spelling must
// surface ErrInvalidUUID before anything is persisted. The txID becomes the
// branch name, so a non-canonical spelling of a valid UUID must be rejected
// per SPEC:978 ("Invalid transaction ID format" error-table row), matching the
// entity/edge write-path gate (uuidutil.Validate).
func TestCreateBranchRejectsNonCanonicalUUID(t *testing.T) {
	gs := setupTestStore(t)
	err := gs.WithGitLock(func() error {
		// Positive control: the canonical spelling must still pass.
		if err := gs.CreateBranch(ctx(), "550e8400-e29b-41d4-a716-446655440000"); err != nil {
			return fmt.Errorf("canonical txID rejected: %w", err)
		}

		for _, id := range nonCanonicalSpellings {
			err := gs.CreateBranch(ctx(), id)
			if !errors.Is(err, ErrInvalidUUID) {
				return fmt.Errorf("CreateBranch(%q) = %v, want ErrInvalidUUID", id, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("TestCreateBranchRejectsNonCanonicalUUID: %v", err)
	}
}
