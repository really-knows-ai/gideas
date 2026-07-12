package flow

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"sort"
)

const (
	// ManifestFile is the name of the manifest file inside a flow package archive.
	ManifestFile = "manifest.yaml"

	// PackageExt is the file extension for flow package archives.
	PackageExt = ".tgz"
)

// PackageWriter creates flow package archives. Zero dependencies on
// Kubernetes — caller provides already-serialized resource data.
// Phase 03 populates this with live cluster data.
type PackageWriter struct {
	Name        string
	Version     string
	Description string
	Resources   map[string][]byte // filename -> serialized YAML
	KindIndex   map[string]string // filename -> Kubernetes kind
}

// Write writes the flow package archive (`.tgz`) to the given writer.
func (pw *PackageWriter) Write(w io.Writer) error {
	gzWriter := gzip.NewWriter(w)
	defer gzWriter.Close()

	tarWriter := tar.NewWriter(gzWriter)
	defer tarWriter.Close()

	// Build manifest
	manifest := Manifest{
		Name:        pw.Name,
		Version:     pw.Version,
		Description: pw.Description,
		Schemas:     []string{"flow.gideas.io/v1"},
	}

	// Sorted resource keys for deterministic output
	var keys []string
	for k := range pw.Resources {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		kind := pw.KindIndex[key]
		if kind == "" {
			return fmt.Errorf("package: missing kind for resource %q", key)
		}
		manifest.Resources = append(manifest.Resources, ManifestResource{
			Path: key,
			Kind: kind,
		})
	}

	// Marshal manifest
	manifestData, err := manifest.Marshal()
	if err != nil {
		return fmt.Errorf("package: failed to marshal manifest: %w", err)
	}

	// Write manifest.yaml entry
	if err := writeTarEntry(tarWriter, ManifestFile, manifestData); err != nil {
		return fmt.Errorf("package: failed to write manifest: %w", err)
	}

	// Write resource entries in order
	for _, key := range keys {
		if err := writeTarEntry(tarWriter, key, pw.Resources[key]); err != nil {
			return fmt.Errorf("package: failed to write resource %q: %w", key, err)
		}
	}

	return nil
}

// writeTarEntry writes a single file entry to the tar writer.
func writeTarEntry(tw *tar.Writer, name string, data []byte) error {
	hdr := &tar.Header{
		Name:     name,
		Size:     int64(len(data)),
		Mode:     0644,
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}
