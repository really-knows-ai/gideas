package gitstore

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/foundry/flow/cartographer/internal/uuidutil"
	"github.com/google/uuid"
)

// WriteEdgeFiles batch-writes all edge files for a single type.
// Parses each edge's ID, FromEntityID, and ToEntityID as canonical RFC4122 §3
// UUID v4 and returns ErrInvalidUUID if any is invalid, including
// non-canonical spellings of a valid UUID (uppercase hex, no-hyphen, braced,
// urn:uuid:) that would be persisted verbatim as <id>.json and split one UUID
// across two files (SPEC:162/:944). PRECONDITION: edgeType must match
// [a-zA-Z_][a-zA-Z0-9_]* (as enforced by schema.Validate on ApplySchema) so
// the type directory stays under edges/; a type name containing a path
// separator would escape the tree. Non-existent files for edges absent from
// the provided slice are NOT removed — the caller must separately call
// RemoveEdgeFiles.
func (g *gitStore) WriteEdgeFiles(ctx context.Context, edgeType string, edges []Edge) error {
	for _, edge := range edges {
		if err := g.writeEdgeFile(edgeType, edge); err != nil {
			return err
		}
	}
	return nil
}

// RemoveEdgeFiles batch-removes edge files for a single type by ID.
// Non-existent files are silently skipped (no error).
func (g *gitStore) RemoveEdgeFiles(ctx context.Context, edgeType string, ids []string) error {
	for _, id := range ids {
		if err := g.removeEdgeFile(edgeType, id); err != nil {
			return err
		}
	}
	return nil
}

// ReadAllEdgeFiles reads all JSON files under edges/<edgeType>/,
// unmarshals them, and returns the result ordered by filename (alphabetical).
// Returns an empty slice (not nil) when the directory does not exist or is empty.
// PRECONDITION: edgeType must match [a-zA-Z_][a-zA-Z0-9_]* (as enforced by
// schema.Validate on ApplySchema) so the type directory stays under edges/; a
// type name containing a path separator would escape the tree.
func (g *gitStore) ReadAllEdgeFiles(ctx context.Context, edgeType string) ([]EdgeFile, error) {
	return readAllElementFiles(g, "edges", "edge", edgeType,
		func(elemType, name string, ej *EdgeJSON) (uuid.UUID, error) {
			// Guard against the embedded type conflicting with the directory it
			// is read from. writeEdgeFile rejects this mismatch
			// (ErrEdgeTypeMismatch), and re-hydration enumerates files per type
			// via the directory — a file whose embedded type disagrees with its
			// directory (external corruption) must surface the same sentinel
			// rather than load under a never-written type.
			if elemType != ej.Type {
				return uuid.Nil, fmt.Errorf("%w: %q != %q", ErrEdgeTypeMismatch, elemType, ej.Type)
			}
			// Guard against a zero, non-v4, or non-RFC4122-variant from/to
			// endpoint. writeEdgeFile rejects these (ErrInvalidUUID), and
			// recovery reconstruction and refresh snapshots consume this path
			// — a file whose embedded endpoint is uuid.Nil, not version 4, or
			// not an RFC4122 variant (external corruption) must surface the
			// same sentinel rather than load an edge pointing at a never-valid
			// UUID.
			if !validUUIDv4(ej.FromEntityID) {
				return uuid.Nil, fmt.Errorf(
					"%w: edge file %s embedded from %s is not a valid UUID v4",
					ErrInvalidUUID, name, ej.FromEntityID)
			}
			if !validUUIDv4(ej.ToEntityID) {
				return uuid.Nil, fmt.Errorf(
					"%w: edge file %s embedded to %s is not a valid UUID v4",
					ErrInvalidUUID, name, ej.ToEntityID)
			}
			return ej.ID, nil
		},
		func(elemType, name, dir string, ej *EdgeJSON) (EdgeFile, error) {
			return EdgeFile{
				ID:           ej.ID.String(),
				Type:         ej.Type,
				FromEntityID: ej.FromEntityID.String(),
				ToEntityID:   ej.ToEntityID.String(),
				Properties:   ej.Properties,
				CreatedAt:    ej.CreatedAt,
				UpdatedAt:    ej.UpdatedAt,
				Path:         filepath.Join(dir, name),
			}, nil
		},
		func(a, b EdgeFile) bool { return a.Path < b.Path },
	)
}

// ListEdgeTypes lists edge type directory names under edges/ that contain
// at least one .json file. Empty subdirectories are excluded.
// Returns an empty slice when edges/ does not exist or contains only
// empty subdirectories.
func (g *gitStore) ListEdgeTypes(ctx context.Context) ([]string, error) {
	return listTypesWithJSON(g.fs, "edges")
}

// writeEdgeFile writes a single edge file. It parses the edge's ID,
// FromEntityID, and ToEntityID as canonical RFC4122 §3 UUID v4 (rejecting the
// non-canonical spellings that uuid.Parse alone would accept — the ID is
// persisted verbatim as <id>.json), and delegates the Create→Write→Close
// sequence to writeJSONFile. The edgeType must match
// [a-zA-Z_][a-zA-Z0-9_]* (see WriteEdgeFiles).
func (g *gitStore) writeEdgeFile(edgeType string, edge Edge) error {
	if edgeType != edge.Type {
		return fmt.Errorf("%w: %q != %q", ErrEdgeTypeMismatch, edgeType, edge.Type)
	}

	// SPEC:162/SPEC:944 require the canonical RFC4122 §3 UUID v4 string form
	// (the lowercase 8-4-4-4-12 dashed string) for the edge ID and both
	// endpoints. uuidutil.Validate — the same gate the store's write path
	// uses — rejects the non-canonical spellings uuid.Parse accepts; the edge
	// ID is persisted verbatim as <id>.json, so a second spelling of one UUID
	// would split the edge across two files.
	if err := uuidutil.Validate(edge.ID); err != nil {
		return ErrInvalidUUID
	}
	if err := uuidutil.Validate(edge.FromEntityID); err != nil {
		return ErrInvalidUUID
	}
	if err := uuidutil.Validate(edge.ToEntityID); err != nil {
		return ErrInvalidUUID
	}
	uid := uuid.MustParse(edge.ID)
	fromUID := uuid.MustParse(edge.FromEntityID)
	toUID := uuid.MustParse(edge.ToEntityID)

	ej := EdgeJSON{
		ID:           uid,
		Type:         edge.Type,
		FromEntityID: fromUID,
		ToEntityID:   toUID,
		Properties:   edge.Properties,
		CreatedAt:    edge.CreatedAt,
		UpdatedAt:    edge.UpdatedAt,
	}

	return g.writeJSONFile("edges", "edge", edgeType, edge.ID, ej)
}

// removeEdgeFile deletes a single edge file. If the file does not exist,
// it returns nil (idempotent).
func (g *gitStore) removeEdgeFile(edgeType string, id string) error {
	return g.removeJSONFile("edges", "edge", edgeType, id)
}
