package gitstore

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// WriteEdgeFiles batch-writes all edge files for a single type.
// Parses each edge's ID, FromEntityID, and ToEntityID as UUID v4 and
// returns ErrInvalidUUID if any is invalid. Non-existent files for edges
// absent from the provided slice are NOT removed — the caller must separately
// call RemoveEdgeFiles.
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
func (g *gitStore) ReadAllEdgeFiles(ctx context.Context, edgeType string) ([]EdgeFile, error) {
	dir := filepath.Join("edges", edgeType)
	entries, err := g.fs.ReadDir(dir)
	if err != nil {
		if isNotExist(err) {
			return []EdgeFile{}, nil
		}
		return nil, fmt.Errorf("read edge dir %s: %w", dir, err)
	}

	var files []EdgeFile
	for _, fi := range entries {
		if fi.IsDir() || !strings.HasSuffix(fi.Name(), ".json") {
			continue
		}
		ef, err := func() (EdgeFile, error) {
			f, err := g.fs.Open(filepath.Join(dir, fi.Name()))
			if err != nil {
				return EdgeFile{}, fmt.Errorf("open edge file %s: %w", fi.Name(), err)
			}
			var ej EdgeJSON
			if err := json.NewDecoder(f).Decode(&ej); err != nil {
				_ = f.Close()
				return EdgeFile{}, fmt.Errorf("decode edge file %s: %w", fi.Name(), err)
			}
			// Guard against the embedded id conflicting with the filename. A
			// well-formed file writes <id>.json whose embedded id equals the
			// filename base (writeEdgeFile); a file whose embedded id differs
			// (external corruption) would otherwise be loaded under an id that
			// was never written to that path, hiding the intended-UUID file
			// during R8 re-hydration. Compare parsed UUIDs so a case-only
			// filename/body variation is not treated as corruption.
			fileID, fileErr := uuid.Parse(strings.TrimSuffix(fi.Name(), ".json"))
			if fileErr != nil || fileID != ej.ID {
				_ = f.Close()
				return EdgeFile{}, fmt.Errorf("edge file %s embedded id %s conflicts with filename", fi.Name(), ej.ID)
			}
			// Guard against the embedded type conflicting with the directory it
			// is read from. writeEdgeFile rejects this mismatch
			// (ErrEdgeTypeMismatch), and re-hydration enumerates files per type
			// via the directory — a file whose embedded type disagrees with its
			// directory (external corruption) must surface the same sentinel
			// rather than load under a never-written type.
			if edgeType != ej.Type {
				_ = f.Close()
				return EdgeFile{}, fmt.Errorf("%w: %q != %q", ErrEdgeTypeMismatch, edgeType, ej.Type)
			}
			// Guard against a zero or non-v4 from/to endpoint. writeEdgeFile
			// rejects these (ErrInvalidUUID), and recovery reconstruction and
			// refresh snapshots consume this path — a file whose embedded
			// endpoint is uuid.Nil or not version 4 (external corruption) must
			// surface the same sentinel rather than load an edge pointing at a
			// never-valid UUID. Version() is 0 for uuid.Nil, so the single
			// version check covers both.
			if ej.FromEntityID.Version() != 4 {
				_ = f.Close()
				return EdgeFile{}, fmt.Errorf(
					"%w: edge file %s embedded from %s is not a valid UUID v4",
					ErrInvalidUUID, fi.Name(), ej.FromEntityID)
			}
			if ej.ToEntityID.Version() != 4 {
				_ = f.Close()
				return EdgeFile{}, fmt.Errorf(
					"%w: edge file %s embedded to %s is not a valid UUID v4",
					ErrInvalidUUID, fi.Name(), ej.ToEntityID)
			}
			// A Close error signals an I/O problem reading the file; propagating it
			// prevents a clean-but-corrupt read from silently passing.
			if err := f.Close(); err != nil {
				return EdgeFile{}, fmt.Errorf("close edge file %s: %w", fi.Name(), err)
			}
			ef := EdgeFile{
				ID:           ej.ID.String(),
				Type:         ej.Type,
				FromEntityID: ej.FromEntityID.String(),
				ToEntityID:   ej.ToEntityID.String(),
				Properties:   ej.Properties,
				CreatedAt:    ej.CreatedAt,
				UpdatedAt:    ej.UpdatedAt,
				Path:         filepath.Join(dir, fi.Name()),
			}
			return ef, nil
		}()
		if err != nil {
			return nil, err
		}
		files = append(files, ef)
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})
	return files, nil
}

// ListEdgeTypes lists edge type directory names under edges/ that contain
// at least one .json file. Empty subdirectories are excluded.
// Returns an empty slice when edges/ does not exist or contains only
// empty subdirectories.
func (g *gitStore) ListEdgeTypes(ctx context.Context) ([]string, error) {
	return listTypesWithJSON(g.fs, "edges")
}

// writeEdgeFile writes a single edge file. It parses the edge's ID,
// FromEntityID, and ToEntityID as UUID v4, creates the directory if needed,
// and marshals the edge to indented JSON.
func (g *gitStore) writeEdgeFile(edgeType string, edge Edge) error {
	if edgeType != edge.Type {
		return fmt.Errorf("%w: %q != %q", ErrEdgeTypeMismatch, edgeType, edge.Type)
	}

	uid, err := uuid.Parse(edge.ID)
	if err != nil || uid.Version() != 4 {
		return ErrInvalidUUID
	}
	fromUID, err := uuid.Parse(edge.FromEntityID)
	if err != nil || fromUID.Version() != 4 {
		return ErrInvalidUUID
	}
	toUID, err := uuid.Parse(edge.ToEntityID)
	if err != nil || toUID.Version() != 4 {
		return ErrInvalidUUID
	}

	ej := EdgeJSON{
		ID:           uid,
		Type:         edge.Type,
		FromEntityID: fromUID,
		ToEntityID:   toUID,
		Properties:   edge.Properties,
		CreatedAt:    edge.CreatedAt,
		UpdatedAt:    edge.UpdatedAt,
	}

	dir := filepath.Join("edges", edgeType)
	if err := g.fs.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir edge dir %s: %w", dir, err)
	}

	path := filepath.Join(dir, edge.ID+".json")
	data, err := json.MarshalIndent(ej, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal edge %s: %w", edge.ID, err)
	}

	f, err := g.fs.Create(path)
	if err != nil {
		return fmt.Errorf("create edge file %s: %w", path, err)
	}

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write edge file %s: %w", path, err)
	}
	if _, err := f.Write([]byte("\n")); err != nil {
		_ = f.Close()
		return fmt.Errorf("write newline %s: %w", path, err)
	}
	// Closing the file is the point at which a buffered flush failure (and thus
	// data loss) becomes observable, so its error must be propagated, not
	// silently discarded. go-billy's File has no Sync(); Close is the deepest
	// durability boundary the interface exposes.
	if err := f.Close(); err != nil {
		return fmt.Errorf("close edge file %s: %w", path, err)
	}
	return nil
}

// removeEdgeFile deletes a single edge file. If the file does not exist,
// it returns nil (idempotent).
func (g *gitStore) removeEdgeFile(edgeType string, id string) error {
	path := filepath.Join("edges", edgeType, id+".json")
	if err := g.fs.Remove(path); err != nil {
		if isNotExist(err) {
			return nil
		}
		return fmt.Errorf("remove edge file %s: %w", path, err)
	}
	return nil
}
