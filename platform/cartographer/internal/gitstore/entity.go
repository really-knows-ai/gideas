package gitstore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/foundry/flow/cartographer/internal/uuidutil"
	"github.com/go-git/go-billy/v5"
	"github.com/google/uuid"
)

// WriteEntityFiles batch-writes all entity files for a single type.
// Parses each entity's ID as a canonical RFC4122 §3 UUID v4 and returns
// ErrInvalidUUID if any ID is invalid, including non-canonical spellings of a
// valid UUID (uppercase hex, no-hyphen, braced, urn:uuid:) that would be
// persisted verbatim as <id>.json and split one UUID across two files
// (SPEC:162/:944). PRECONDITION: entityType must match [a-zA-Z_][a-zA-Z0-9_]* (as
// enforced by schema.Validate on ApplySchema) so the type directory stays
// under entities/; a type name containing a path separator would escape the
// tree. Non-existent files for entities missing from the provided slice are
// NOT removed — the caller must separately call RemoveEntityFiles.
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
// PRECONDITION: entityType must match [a-zA-Z_][a-zA-Z0-9_]* (as enforced by
// schema.Validate on ApplySchema) so the type directory stays under entities/;
// a type name containing a path separator would escape the tree.
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
			// Read the entire file and unmarshal it as a single document.
			// json.Unmarshal rejects trailing content after the top-level value,
			// whereas a streaming Decoder.Decode would silently consume only the
			// first of two concatenated documents — a corrupted file must fail
			// loudly like every sibling corruption guard (SPEC R8), not
			// truncate.
			data, err := io.ReadAll(f)
			if err != nil {
				_ = f.Close()
				return EntityFile{}, fmt.Errorf("read entity file %s: %w", fi.Name(), err)
			}
			// A Close error signals an I/O problem reading the file; propagating it
			// prevents a clean-but-corrupt read from silently passing.
			if err := f.Close(); err != nil {
				return EntityFile{}, fmt.Errorf("close entity file %s: %w", fi.Name(), err)
			}
			var ej EntityJSON
			if err := json.Unmarshal(data, &ej); err != nil {
				return EntityFile{}, fmt.Errorf("decode entity file %s: %w", fi.Name(), err)
			}
			// Guard against the embedded id conflicting with the filename. A
			// well-formed file writes <id>.json whose embedded id equals the
			// filename base (writeEntityFile); a file whose embedded id differs
			// (external corruption) would otherwise be loaded under an id that
			// was never written to that path, hiding the intended-UUID file
			// during R8 re-hydration. The raw filename base must equal the
			// canonical spelling (ej.ID.String()): comparing parsed UUIDs would
			// normalise case and admit an uppercase-spelled <id>.json coexisting
			// with the canonical file for one UUID — the two-files-one-UUID
			// hazard SPEC:162/:944 prevent on the write path, so a case-variant
			// file is corruption.
			if base := strings.TrimSuffix(fi.Name(), ".json"); base != ej.ID.String() {
				return EntityFile{}, fmt.Errorf("entity file %s embedded id %s conflicts with filename", fi.Name(), ej.ID)
			}
			// Guard against a zero, non-v4, or non-RFC4122-variant embedded
			// id. writeEntityFile rejects these (ErrInvalidUUID), and recovery
			// reconstruction and refresh snapshots consume this path — a file
			// whose embedded id is uuid.Nil, not version 4, or not an RFC4122
			// variant (external corruption) must surface the same sentinel
			// rather than load an entity under a never-valid UUID. Version() is
			// 0 for uuid.Nil, so the version check covers zero; the variant
			// check matches uuidutil.Validate, which gates the write path on
			// both dimensions.
			if ej.ID.Version() != 4 || ej.ID.Variant() != uuid.RFC4122 {
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
				return EntityFile{}, fmt.Errorf("%w: %q != %q", ErrEntityTypeMismatch, entityType, ej.Type)
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

// writeEntityFile writes a single entity file. It parses the entity's ID as
// a canonical RFC4122 §3 UUID v4 (rejecting the non-canonical spellings that
// uuid.Parse alone would accept — the ID is persisted verbatim as <id>.json),
// creates the directory if needed, and marshals the entity to indented JSON.
// The entityType must match [a-zA-Z_][a-zA-Z0-9_]* (see WriteEntityFiles).
func (g *gitStore) writeEntityFile(entityType string, ent Entity) error {
	if entityType != ent.Type {
		return fmt.Errorf("%w: %q != %q", ErrEntityTypeMismatch, entityType, ent.Type)
	}

	// SPEC:162/SPEC:944 require the canonical RFC4122 §3 UUID v4 string form
	// (the lowercase 8-4-4-4-12 dashed string). uuidutil.Validate — the same
	// gate the store's write path uses — rejects the non-canonical spellings
	// uuid.Parse accepts; a second spelling of one UUID persisted verbatim as
	// <id>.json would split the entity across two files and bypass the
	// CreateEntity ALREADY_EXISTS check (SPEC:162).
	if err := uuidutil.Validate(ent.ID); err != nil {
		return ErrInvalidUUID
	}
	uid := uuid.MustParse(ent.ID)

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
