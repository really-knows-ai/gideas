package flow

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// T1: CreateTGZ round-trip
func TestCreateTGZ_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "test.tgz")
	files := map[string][]byte{"hello.txt": []byte("world")}

	if err := CreateTGZ(outputPath, files); err != nil {
		t.Fatalf("CreateTGZ: %v", err)
	}

	extracted, err := ExtractTGZ(outputPath)
	if err != nil {
		t.Fatalf("ExtractTGZ: %v", err)
	}
	if v := string(extracted["hello.txt"]); v != "world" {
		t.Fatalf("expected 'world', got %q", v)
	}
}

// T2: CreateTGZ multiple files
func TestCreateTGZ_MultipleFiles(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "test.tgz")
	files := map[string][]byte{
		"a.yaml": []byte("kind: A"),
		"b.yaml": []byte("kind: B"),
		"c.yaml": []byte("kind: C"),
	}

	if err := CreateTGZ(outputPath, files); err != nil {
		t.Fatalf("CreateTGZ: %v", err)
	}

	extracted, err := ExtractTGZ(outputPath)
	if err != nil {
		t.Fatalf("ExtractTGZ: %v", err)
	}
	for _, name := range []string{"a.yaml", "b.yaml", "c.yaml"} {
		if _, ok := extracted[name]; !ok {
			t.Errorf("missing file %q", name)
		}
	}
	if len(extracted) != 3 {
		t.Errorf("expected 3 files, got %d", len(extracted))
	}
}

// T3: CreateTGZ empty files map
func TestCreateTGZ_EmptyFiles(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "test.tgz")

	err := CreateTGZ(outputPath, map[string][]byte{})
	if err == nil {
		t.Fatal("expected error for empty files, got nil")
	}
	if err.Error() != "no files to package" {
		t.Errorf("unexpected error: %v", err)
	}
}

// T4: ExtractTGZ malformed input
func TestExtractTGZ_Malformed(t *testing.T) {
	dir := t.TempDir()
	badPath := filepath.Join(dir, "bad.tgz")
	if err := os.WriteFile(badPath, []byte("not a gzip file"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := ExtractTGZ(badPath)
	if err == nil {
		t.Fatal("expected error for malformed input, got nil")
	}
}

// T5: ExtractTGZ empty archive (valid gzip with no tar entries)
func TestExtractTGZ_Empty(t *testing.T) {
	dir := t.TempDir()
	emptyPath := filepath.Join(dir, "empty.tgz")

	// Create a valid gzip file containing an empty tar archive.
	// An empty tar is 1024 zero bytes (two 512-byte end-of-archive blocks).
	var emptyTarBuf bytes.Buffer
	emptyTarBuf.Write(make([]byte, 1024))

	var gzBuf bytes.Buffer
	gzWriter := gzip.NewWriter(&gzBuf)
	if _, err := gzWriter.Write(emptyTarBuf.Bytes()); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gzWriter.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := os.WriteFile(emptyPath, gzBuf.Bytes(), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := ExtractTGZ(emptyPath)
	if err == nil {
		t.Fatal("expected error for empty archive, got nil")
	}
	if err.Error() != "archive contains no files" {
		t.Errorf("expected 'archive contains no files', got %v", err)
	}
}

// T6: ExtractTGZ skips directories
func TestExtractTGZ_SkipsDirectories(t *testing.T) {
	dir := t.TempDir()
	tgzPath := filepath.Join(dir, "test.tgz")

	// Build a tar with a directory entry and a file entry.
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)

	// Directory entry
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir,
		Name:     "subdir/",
		Mode:     0755,
	}); err != nil {
		t.Fatalf("tar dir header: %v", err)
	}

	// Regular file entry
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     "real.txt",
		Size:     int64(len("content")),
		Mode:     0644,
	}); err != nil {
		t.Fatalf("tar file header: %v", err)
	}
	if _, err := tw.Write([]byte("content")); err != nil {
		t.Fatalf("tar file write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}

	// GZip
	var gzBuf bytes.Buffer
	gzWriter := gzip.NewWriter(&gzBuf)
	if _, err := gzWriter.Write(tarBuf.Bytes()); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gzWriter.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := os.WriteFile(tgzPath, gzBuf.Bytes(), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	extracted, err := ExtractTGZ(tgzPath)
	if err != nil {
		t.Fatalf("ExtractTGZ: %v", err)
	}
	// Only the regular file should be present
	if v := string(extracted["real.txt"]); v != "content" {
		t.Errorf("expected 'content', got %q", v)
	}
	if _, ok := extracted["subdir/"]; ok {
		t.Error("directory entry should be skipped")
	}
}

// T7: CreateTGZ output path invalid (non-existent directory)
func TestCreateTGZ_InvalidOutputPath(t *testing.T) {
	dir := t.TempDir()
	badPath := filepath.Join(dir, "nonexistent", "out.tgz")
	err := CreateTGZ(badPath, map[string][]byte{"a.txt": []byte("data")})
	if err == nil {
		t.Fatal("expected error for invalid output path, got nil")
	}
}

// T8: GZip magic bytes verification
func TestCreateTGZ_GZipMagic(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "test.tgz")
	files := map[string][]byte{"test.txt": []byte("hello")}

	if err := CreateTGZ(outputPath, files); err != nil {
		t.Fatalf("CreateTGZ: %v", err)
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) < 2 || data[0] != 0x1f || data[1] != 0x8b {
		t.Fatal("output does not start with gzip magic bytes [0x1f, 0x8b]")
	}
}

// T9: CreateTGZ partial write error propagation.
// Tests that createTGZTo returns an error from a failing underlying writer,
// and that CreateTGZ cleans up partial output when writing fails.
func TestCreateTGZ_PartialWriteCleanup(t *testing.T) {
	// Verify createTGZTo propagates errors from the underlying writer.
	err := createTGZTo(&failWriter{}, map[string][]byte{
		"a.yaml": []byte("kind: A"),
	})
	if err == nil {
		t.Fatal("expected error from failing writer, got nil")
	}

	// Verify CreateTGZ cleans up partial output by injecting a write failure.
	oldArchive := createArchive
	createArchive = func(w io.Writer, files map[string][]byte) error {
		return errors.New("injected write failure")
	}
	defer func() { createArchive = oldArchive }()

	dir := t.TempDir()
	outputPath := filepath.Join(dir, "test.tgz")
	if err := CreateTGZ(outputPath, map[string][]byte{"a.yaml": []byte("data")}); err == nil {
		t.Fatal("expected error from injected write failure, got nil")
	}
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Error("partial output file should have been removed after write failure")
	}
}

// failWriter is an io.Writer that always returns an error.
type failWriter struct{}

func (w *failWriter) Write(p []byte) (int, error) {
	return 0, errors.New("injected write failure")
}

var _ io.Writer = (*failWriter)(nil)
