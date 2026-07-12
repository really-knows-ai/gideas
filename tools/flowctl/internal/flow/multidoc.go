package flow

import (
	"fmt"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// sepRe matches a YAML document separator line: optional whitespace, ---, optional whitespace.
var sepRe = regexp.MustCompile(`(?m)^\s*---\s*$`)

// SplitYAMLDocuments splits raw bytes on YAML document separator lines (---).
// Leading/trailing whitespace on the separator line is tolerated.
// Consecutive separators produce empty chunks. A file with no separator
// returns a single chunk.
func SplitYAMLDocuments(data []byte) [][]byte {
	if len(data) == 0 {
		return nil
	}
	raw := string(data)
	// Split on the regex. Empty strings between consecutive separators are preserved.
	parts := sepRe.Split(raw, -1)
	var result [][]byte
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			// Empty chunks (from consecutive separators or leading/trailing) are preserved
			// as empty byte slices for the caller to handle.
		}
		result = append(result, []byte(trimmed))
	}
	return result
}

// ParseMultiDocYAML parses a YAML byte slice containing zero or more
// documents separated by `---`. Each document is returned as an
// *unstructured.Unstructured. Empty documents are skipped.
// Returns an error if any document cannot be decoded as valid YAML
// or if a non-empty document does not contain a Kind field.
func ParseMultiDocYAML(data []byte) ([]*unstructured.Unstructured, error) {
	chunks := SplitYAMLDocuments(data)
	var result []*unstructured.Unstructured
	for i, chunk := range chunks {
		if len(strings.TrimSpace(string(chunk))) == 0 {
			continue
		}
		var m map[string]interface{}
		if err := yaml.Unmarshal(chunk, &m); err != nil {
			return nil, fmt.Errorf("multi-doc YAML: document at index %d: %w", i, err)
		}
		kind, _, _ := unstructured.NestedString(m, "kind")
		if kind == "" {
			return nil, fmt.Errorf("multi-doc YAML: document at index %d has no kind", i)
		}
		result = append(result, &unstructured.Unstructured{Object: m})
	}
	return result, nil
}

// AppendResource appends a single unstructured object to an existing
// multi-document YAML byte slice. If the slice is non-empty, a `---\n`
// separator is prepended before the new document.
// Returns the concatenated YAML bytes.
func AppendResource(yamlData []byte, obj *unstructured.Unstructured) ([]byte, error) {
	out, err := yaml.Marshal(obj)
	if err != nil {
		return nil, err
	}
	if len(yamlData) > 0 {
		out = append([]byte("---\n"), out...)
	}
	return append(yamlData, out...), nil
}
