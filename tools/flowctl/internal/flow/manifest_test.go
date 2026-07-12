package flow

import (
	"strings"
	"testing"
)

func TestManifest_Valid(t *testing.T) {
	m := &Manifest{
		Name:        "haiku-flow",
		Version:     "1.0.0",
		Description: "Haiku test flow",
		Schemas:     []string{"flow.foundry.io/v1"},
		Resources: []ManifestResource{
			{Path: "flow.yaml", Kind: "FoundryFlow"},
			{Path: "nodes.yaml", Kind: "FoundryNode"},
		},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestManifest_MissingName(t *testing.T) {
	m := &Manifest{
		Version:     "1.0.0",
		Description: "test",
		Schemas:     []string{"flow.foundry.io/v1"},
		Resources:   []ManifestResource{{Path: "flow.yaml", Kind: "FoundryFlow"}},
	}
	err := m.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !contains(err.Error(), "name is required") {
		t.Fatalf("expected 'name is required', got %q", err.Error())
	}
}

func TestManifest_MissingVersion(t *testing.T) {
	m := &Manifest{
		Name:        "test",
		Description: "test",
		Schemas:     []string{"flow.foundry.io/v1"},
		Resources:   []ManifestResource{{Path: "flow.yaml", Kind: "FoundryFlow"}},
	}
	err := m.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !contains(err.Error(), "version is required") {
		t.Fatalf("expected 'version is required', got %q", err.Error())
	}
}

func TestManifest_MissingSchemas(t *testing.T) {
	m := &Manifest{
		Name:        "test",
		Version:     "1.0.0",
		Description: "test",
		Schemas:     nil,
		Resources:   []ManifestResource{{Path: "flow.yaml", Kind: "FoundryFlow"}},
	}
	err := m.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !contains(err.Error(), "at least one schema") {
		t.Fatalf("expected 'at least one schema', got %q", err.Error())
	}
}

func TestManifest_MissingResources(t *testing.T) {
	m := &Manifest{
		Name:    "test",
		Version: "1.0.0",
		Schemas: []string{"flow.foundry.io/v1"},
	}
	err := m.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !contains(err.Error(), "at least one resource") {
		t.Fatalf("expected 'at least one resource', got %q", err.Error())
	}
}

func TestManifest_MissingResourcePath(t *testing.T) {
	m := &Manifest{
		Name:    "test",
		Version: "1.0.0",
		Schemas: []string{"flow.foundry.io/v1"},
		Resources: []ManifestResource{
			{Path: "", Kind: "FoundryFlow"},
		},
	}
	err := m.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !contains(err.Error(), "path is required") {
		t.Fatalf("expected 'path is required', got %q", err.Error())
	}
}

func TestManifest_MissingResourceKind(t *testing.T) {
	m := &Manifest{
		Name:    "test",
		Version: "1.0.0",
		Schemas: []string{"flow.foundry.io/v1"},
		Resources: []ManifestResource{
			{Path: "flow.yaml", Kind: ""},
		},
	}
	err := m.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !contains(err.Error(), "kind is required") {
		t.Fatalf("expected 'kind is required', got %q", err.Error())
	}
}

func TestManifest_DuplicateResourcePath(t *testing.T) {
	m := &Manifest{
		Name:    "test",
		Version: "1.0.0",
		Schemas: []string{"flow.foundry.io/v1"},
		Resources: []ManifestResource{
			{Path: "flow.yaml", Kind: "FoundryFlow"},
			{Path: "flow.yaml", Kind: "FoundryNode"},
		},
	}
	err := m.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !contains(err.Error(), "duplicate resource path") {
		t.Fatalf("expected 'duplicate resource path', got %q", err.Error())
	}
}

func TestManifest_InvalidSchemaFormat(t *testing.T) {
	m := &Manifest{
		Name:    "test",
		Version: "1.0.0",
		Schemas: []string{"invalid"},
		Resources: []ManifestResource{
			{Path: "flow.yaml", Kind: "FoundryFlow"},
		},
	}
	err := m.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !contains(err.Error(), "does not match required format") {
		t.Fatalf("expected 'does not match required format', got %q", err.Error())
	}
}

func TestManifest_ValidSchemaFormats(t *testing.T) {
	m := &Manifest{
		Name:    "test",
		Version: "1.0.0",
		Schemas: []string{"flow.foundry.io/v1", "example.io/v2", "my-group/v10"},
		Resources: []ManifestResource{
			{Path: "flow.yaml", Kind: "FoundryFlow"},
		},
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestManifest_MultipleErrors(t *testing.T) {
	m := &Manifest{
		Schemas: []string{""},
		Resources: []ManifestResource{
			{Path: "", Kind: "FoundryFlow"},
		},
	}
	err := m.Validate()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !contains(err.Error(), "name is required") {
		t.Fatalf("expected 'name is required', got %q", err.Error())
	}
	if !contains(err.Error(), "version is required") {
		t.Fatalf("expected 'version is required', got %q", err.Error())
	}
}

func TestManifest_MarshalRoundTrip(t *testing.T) {
	original := &Manifest{
		Name:        "haiku-flow",
		Version:     "1.0.0",
		Description: "Haiku test flow",
		Schemas:     []string{"flow.foundry.io/v1"},
		Resources: []ManifestResource{
			{Path: "flow.yaml", Kind: "FoundryFlow"},
		},
	}
	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	restored, err := UnmarshalManifest(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if restored.Name != original.Name {
		t.Fatalf("name: got %q, want %q", restored.Name, original.Name)
	}
	if restored.Version != original.Version {
		t.Fatalf("version: got %q, want %q", restored.Version, original.Version)
	}
	if len(restored.Resources) != len(original.Resources) {
		t.Fatalf("resources: got %d, want %d", len(restored.Resources), len(original.Resources))
	}
}

func TestManifest_UnmarshalInvalidYAML(t *testing.T) {
	_, err := UnmarshalManifest([]byte("garbage: [[["))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestManifest_UnmarshalWithValidationFailure(t *testing.T) {
	// Missing name should fail validation
	data := []byte("version: 1.0.0\nresources:\n  - path: flow.yaml\n    kind: FoundryFlow\nschemas:\n  - flow.foundry.io/v1\n")
	_, err := UnmarshalManifest(data)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !contains(err.Error(), "name is required") {
		t.Fatalf("expected 'name is required', got %q", err.Error())
	}
}

// contains reports whether substr is within s.
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}
