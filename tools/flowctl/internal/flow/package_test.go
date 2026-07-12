package flow

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"
)

func TestPackageWriter_RoundTrip(t *testing.T) {
	pw := &PackageWriter{
		Name:        "haiku-flow",
		Version:     "1.0.0",
		Description: "Haiku test flow",
		Resources: map[string][]byte{
			"flow.yaml": []byte("apiVersion: flow.gideas.io/v1\nkind: FoundryFlow\nmetadata:\n  name: haiku-flow\n"),
			"nodes.yaml": []byte("apiVersion: flow.gideas.io/v1\nkind: FoundryNode\nmetadata:\n  name: forge\n"),
		},
		KindIndex: map[string]string{
			"flow.yaml":  "FoundryFlow",
			"nodes.yaml": "FoundryNode",
		},
	}

	var buf bytes.Buffer
	if err := pw.Write(&buf); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Verify gzip magic bytes
	if buf.Len() < 2 {
		t.Fatal("output too short")
	}
	data := buf.Bytes()
	if data[0] != 0x1f || data[1] != 0x8b {
		t.Fatal("output does not start with gzip magic bytes")
	}

	// Extract and verify contents
	gzReader, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)
	found := make(map[string]bool)

	for {
		hdr, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next: %v", err)
		}
		found[hdr.Name] = true

		content, err := io.ReadAll(tarReader)
		if err != nil {
			t.Fatalf("read %q: %v", hdr.Name, err)
		}

		switch hdr.Name {
		case ManifestFile:
			if !strings.Contains(string(content), "haiku-flow") {
				t.Errorf("manifest should contain flow name")
			}
			if !strings.Contains(string(content), "nodes.yaml") {
				t.Errorf("manifest should reference nodes.yaml")
			}
			// Verify it parses correctly
			_, err := UnmarshalManifest(content)
			if err != nil {
				t.Errorf("manifest unmarshal: %v", err)
			}
		case "flow.yaml":
			if !strings.Contains(string(content), "FoundryFlow") {
				t.Errorf("flow.yaml should contain FoundryFlow kind")
			}
		case "nodes.yaml":
			if !strings.Contains(string(content), "FoundryNode") {
				t.Errorf("nodes.yaml should contain FoundryNode kind")
			}
		default:
			t.Errorf("unexpected file in archive: %s", hdr.Name)
		}
	}

	if !found[ManifestFile] {
		t.Error("manifest.yaml not found in archive")
	}
	if !found["flow.yaml"] {
		t.Error("flow.yaml not found in archive")
	}
	if !found["nodes.yaml"] {
		t.Error("nodes.yaml not found in archive")
	}
}

func TestPackageWriter_Directories(t *testing.T) {
	// Verify that all resource files are at root level (no nested directories)
	pw := &PackageWriter{
		Name:    "test",
		Version: "1.0.0",
		Resources: map[string][]byte{
			"flow.yaml": []byte("kind: FoundryFlow\n"),
		},
		KindIndex: map[string]string{
			"flow.yaml": "FoundryFlow",
		},
	}

	var buf bytes.Buffer
	if err := pw.Write(&buf); err != nil {
		t.Fatalf("Write: %v", err)
	}

	gzReader, err := gzip.NewReader(&buf)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)
	for {
		hdr, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next: %v", err)
		}
		if strings.Contains(hdr.Name, "/") {
			t.Errorf("file %q contains directory separator", hdr.Name)
		}
	}
}
