package gitstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-git/go-billy/v5"
	"github.com/google/uuid"
)

// WriteEntityFiles batch-writes all entity files for a single type.
// Parses each entity's ID as a UUID v4 and returns ErrInvalidUUID if any
// ID is invalid. Non-existent files for entities missing from the provided
// slice are NOT removed — the caller must separately call RemoveEntityFiles.
func (g *gitStore) WriteEntityFiles(ctx context.Context, entityType string, entities []Entity) error {
	for _, ent := range entities {
		if err := g.writeEntityFile(entityType, ent); err != nil {
			return err
		}
	}
	return nil
}

// RemoveEntityFiles batch-removes entity files for a single type by ID.
// Non-existent files are silently skipped (no error).
func (g *gitStore) RemoveEntityFiles(ctx context.Context, entityType string, ids []string) error {
	for _, id := range ids {
		if err := g.removeEntityFile(entityType, id); err != nil {
			return err
		}
	}
	return nil
}

// ReadAllEntityFiles reads all JSON files under entities/<entityType>/,
// unmarshals them, and returns the result ordered by filename (alphabetical).
// Returns an empty slice (not nil) when the directory does not exist or is empty.
func (g *gitStore) ReadAllEntityFiles(ctx context.Context, entityType string) ([]EntityFile, error) {
	dir := filepath.Join("entities", entityType)
	entries, err := g.fs.ReadDir(dir)
	if err != nil {
		if isNotExist(err) {
			return []EntityFile{}, nil
		}
		return nil, fmt.Errorf("read entity dir %s: %w", dir, err)
	}

	var files []EntityFile
	for _, fi := range entries {
		if fi.IsDir() || !strings.HasSuffix(fi.Name(), ".json") {
			continue
		}
		ef, err := func() (EntityFile, error) {
			f, err := g.fs.Open(filepath.Join(dir, fi.Name()))
			if err != nil {
				return EntityFile{}, fmt.Errorf("open entity file %s: %w", fi.Name(), err)
			}
			var ej EntityJSON
			if err := json.NewDecoder(f).Decode(&ej); err != nil {
				_ = f.Close()
				return EntityFile{}, fmt.Errorf("decode entity file %s: %w", fi.Name(), err)
			}
			// Guard against the embedded id conflicting with the filename. A
			// well-formed file writes <id>.json whose embedded id equals the
			// filename base (writeEntityFile); a file whose embedded id differs
			// (external corruption) would otherwise be loaded under an id that
			// was never written to that path, hiding the intended-UUID file
			// during R8 re-hydration. Compare parsed UUIDs so a case-only
			// filename/body variation is not treated as corruption.
			fileID, fileErr := uuid.Parse(strings.TrimSuffix(fi.Name(), ".json"))
			if fileErr != nil || fileID != ej.ID {
				_ = f.Close()
				return EntityFile{}, fmt.Errorf("entity file %s embedded id %s conflicts with filename", fi.Name(), ej.ID)
			}
			// Guard against a zero or non-v4 embedded id. writeEntityFile
			// rejects these (ErrInvalidUUID), and recovery reconstruction and
			// refresh snapshots consume this path — a file whose embedded id
			// is uuid.Nil or not version 4 (external corruption) must surface
			// the same sentinel rather than load an entity under a never-valid
			// UUID. Version() is 0 for uuid.Nil, so the single version check
			// covers both.
			if ej.ID.Version() != 4 {
				_ = f.Close()
				return EntityFile{}, fmt.Errorf(
					"%w: entity file %s embedded id %s is not a valid UUID v4",
					ErrInvalidUUID, fi.Name(), ej.ID)
			}
			// Guard against the embedded type conflicting with the directory it
			// is read from. writeEntityFile rejects this mismatch
			// (ErrEntityTypeMismatch), and re-hydration enumerates files per
			// type via the directory — a file whose embedded type disagrees
			// with its directory (external corruption) must surface the same
			// sentinel rather than load under a never-written type.
			if entityType != ej.Type {
				_ = f.Close()
				return EntityFile{}, fmt.Errorf("%w: %q != %q", ErrEntityTypeMismatch, entityType, ej.Type)
			}
			// A Close error signals an I/O problem reading the file; propagating it
			// prevents a clean-but-corrupt read from silently passing.
			if err := f.Close(); err != nil {
				return EntityFile{}, fmt.Errorf("close entity file %s: %w", fi.Name(), err)
			}
			ef := EntityFile{
				ID:         ej.ID.String(),
				Type:       ej.Type,
				Properties: ej.Properties,
				CreatedAt:  ej.CreatedAt,
				UpdatedAt:  ej.UpdatedAt,
				Path:       filepath.Join(dir, fi.Name()),
			}
			if ej.Embedding != nil {
				ef.Embedding = *ej.Embedding
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

// ListEntityTypes lists entity type directory names under entities/ that
// contain at least one .json file. Empty subdirectories are excluded.
// Returns an empty slice when entities/ does not exist or contains only
// empty subdirectories.
func (g *gitStore) ListEntityTypes(ctx context.Context) ([]string, error) {
	return listTypesWithJSON(g.fs, "entities")
}

// writeEntityFile writes a single entity file. It parses the entity's ID
// as a UUID v4, creates the directory if needed, and marshals the entity
// to indented JSON.
func (g *gitStore) writeEntityFile(entityType string, ent Entity) error {
	if entityType != ent.Type {
		return fmt.Errorf("%w: %q != %q", ErrEntityTypeMismatch, entityType, ent.Type)
	}

	uid, err := uuid.Parse(ent.ID)
	if err != nil || uid.Version() != 4 {
		return ErrInvalidUUID
	}

	ej := EntityJSON{
		ID:         uid,
		Type:       ent.Type,
		Properties: ent.Properties,
		CreatedAt:  ent.CreatedAt,
		UpdatedAt:  ent.UpdatedAt,
	}
	if len(ent.Embedding) > 0 {
		emb := make([]float32, len(ent.Embedding))
		copy(emb, ent.Embedding)
		ej.Embedding = &emb
	} else if ent.Embedding != nil {
		emb := []float32{}
		ej.Embedding = &emb
	}

	dir := filepath.Join("entities", entityType)
	if err := g.fs.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("mkdir entity dir %s: %w", dir, err)
	}

	path := filepath.Join(dir, ent.ID+".json")
	data, err := json.MarshalIndent(ej, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal entity %s: %w", ent.ID, err)
	}

	f, err := g.fs.Create(path)
	if err != nil {
		return fmt.Errorf("create entity file %s: %w", path, err)
	}

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write entity file %s: %w", path, err)
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
		return fmt.Errorf("close entity file %s: %w", path, err)
	}
	return nil
}

// removeEntityFile deletes a single entity file. If the file does not
// exist, it returns nil (idempotent).
func (g *gitStore) removeEntityFile(entityType string, id string) error {
	path := filepath.Join("entities", entityType, id+".json")
	if err := g.fs.Remove(path); err != nil {
		if isNotExist(err) {
			return nil
		}
		return fmt.Errorf("remove entity file %s: %w", path, err)
	}
	return nil
}

// listTypesWithJSON is a helper that reads subdirectories under baseDir
// and returns those containing at least one .json file.
func listTypesWithJSON(fs billy.Filesystem, baseDir string) ([]string, error) {
	entries, err := fs.ReadDir(baseDir)
	if err != nil {
		if isNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("read dir %s: %w", baseDir, err)
	}

	var types []string
	for _, fi := range entries {
		if !fi.IsDir() {
			continue
		}
		subEntries, err := fs.ReadDir(filepath.Join(baseDir, fi.Name()))
		if err != nil {
			return nil, fmt.Errorf("read type dir %s: %w", filepath.Join(baseDir, fi.Name()), err)
		}
		hasJSON := false
		for _, se := range subEntries {
			if !se.IsDir() && strings.HasSuffix(se.Name(), ".json") {
				hasJSON = true
				break
			}
		}
		if hasJSON {
			types = append(types, fi.Name())
		}
	}

	sort.Strings(types)
	return types, nil
}

// isNotExist returns true if err indicates a file or directory does not exist.
// It matches os.ErrNotExist (and errors wrapping it), covering OS-backed errors
// (os.PathError / syscall.ENOENT) as well as the go-billy OSFS and memfs
// filesystems, both of which surface os.ErrNotExist for missing paths.
// ponytail: billy v5.9 exposes no billy-native not-exist sentinel distinct from
// os.ErrNotExist, so matching os.IsNotExist is the full surface today; if a
// future billy filesystem returns a non-wrapping not-exist error, extend this
// guard to match it.
func isNotExist(err error) bool {
	return os.IsNotExist(err)
}
