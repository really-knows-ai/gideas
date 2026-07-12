package flow

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// CreateTGZ creates a gzipped tar archive at outputPath containing the entries
// in files (filename → content bytes). Filenames are placed at the archive root
// (no directory prefix). Returns an error if the file cannot be created or the
// archive cannot be written.
func CreateTGZ(outputPath string, files map[string][]byte) error {
	if len(files) == 0 {
		return errors.New("no files to package")
	}

	out, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer out.Close()

	if err := createArchive(out, files); err != nil {
		// Attempt to clean up partial output on write failure
		out.Close()
		os.Remove(outputPath)
		return err
	}
	return nil
}

// createArchive is a variable so tests can inject write failures.
var createArchive = createTGZTo

// createTGZTo writes a gzipped tar archive to w containing the entries in files.
// Exported for testing with failing io.Writers.
func createTGZTo(w io.Writer, files map[string][]byte) error {
	gzWriter := gzip.NewWriter(w)
	defer gzWriter.Close()

	tarWriter := tar.NewWriter(gzWriter)
	defer tarWriter.Close()

	// Deterministic order: sort filenames
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, name := range keys {
		data := files[name]
		hdr := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     name,
			Size:     int64(len(data)),
			Mode:     0644,
		}
		if err := tarWriter.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tarWriter.Write(data); err != nil {
			return err
		}
	}
	return nil
}

// ExtractTGZ reads a gzipped tar archive from tgzPath and returns a map of
// filename → raw bytes for all regular file entries. Directory entries are
// skipped. Returns an error if the archive is malformed or contains no files.
func ExtractTGZ(tgzPath string) (map[string][]byte, error) {
	f, err := os.Open(tgzPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	gzReader, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)
	result := make(map[string][]byte)

	for {
		hdr, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
			// ponytail: flat extraction uses filepath.Base to strip any directory
			// prefix. Production packages use flat layout (no nested directories).
			// If nested paths are needed in the future, remove filepath.Base and
			// preserve the full path.
			name := filepath.Base(hdr.Name)
			data, err := io.ReadAll(tarReader)
			if err != nil {
				return nil, err
			}
			result[name] = data
		default:
			// Symlinks, block/char devices, etc.: skip
			continue
		}
	}

	if len(result) == 0 {
		return nil, errors.New("archive contains no files")
	}
	return result, nil
}
