package flow

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"sigs.k8s.io/yaml"
)

// Manifest describes a flow package. It is serialised as manifest.yaml
// at the root of a .tgz archive.
type Manifest struct {
	Name        string             `yaml:"name"        json:"name"`
	Version     string             `yaml:"version"     json:"version"`
	Description string             `yaml:"description" json:"description"`
	Schemas     []string           `yaml:"schemas"     json:"schemas"`
	Resources   []ManifestResource `yaml:"resources"   json:"resources"`
}

// ManifestResource names a single resource file in the package.
type ManifestResource struct {
	Path string `yaml:"path" json:"path"`
	Kind string `yaml:"kind" json:"kind"`
}

// schemaRe matches the required format "<group>/v<version>", e.g. "flow.foundry.io/v1".
var schemaRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+/v[0-9]+[a-zA-Z0-9._-]*$`)

// Validate checks the manifest for required fields and valid entries.
// Returns a single error string enumerating all problems, or nil if valid.
func (m *Manifest) Validate() error {
	var errs []string

	if m.Name == "" {
		errs = append(errs, "manifest: name is required")
	}
	if m.Version == "" {
		errs = append(errs, "manifest: version is required")
	}
	if len(m.Schemas) == 0 {
		errs = append(errs, "manifest: at least one schema is required")
	} else {
		for _, s := range m.Schemas {
			if !schemaRe.MatchString(s) {
				errs = append(errs, fmt.Sprintf("manifest: schema %q does not match required format \"<group>/v<version>\"", s))
			}
		}
	}
	if len(m.Resources) == 0 {
		errs = append(errs, "manifest: at least one resource is required")
	} else {
		seen := make(map[string]int) // path -> first index for error reporting
		for i, r := range m.Resources {
			if r.Path == "" {
				errs = append(errs, fmt.Sprintf("manifest: resource at index %d: path is required", i))
			}
			if r.Kind == "" {
				errs = append(errs, fmt.Sprintf("manifest: resource at index %d: kind is required", i))
			}
			if r.Path != "" {
				if firstIdx, ok := seen[r.Path]; ok {
					errs = append(errs, fmt.Sprintf("manifest: duplicate resource path %q (first occurrence at index %d)", r.Path, firstIdx))
				} else {
					seen[r.Path] = i
				}
			}
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return errors.New(strings.Join(errs, "\n"))
}

// Marshal serializes the manifest to YAML.
func (m *Manifest) Marshal() ([]byte, error) {
	return yaml.Marshal(m)
}

// UnmarshalManifest parses YAML data into a Manifest and validates it.
func UnmarshalManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest: failed to parse YAML: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}
