package flow

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestParseMultiDoc_SingleDocument(t *testing.T) {
	data := []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: test
`)
	docs, err := ParseMultiDocYAML(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 document, got %d", len(docs))
	}
	if docs[0].GetKind() != "ConfigMap" {
		t.Errorf("kind = %q, want %q", docs[0].GetKind(), "ConfigMap")
	}
}

func TestParseMultiDoc_MultipleDocuments(t *testing.T) {
	data := []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: cm-one
---
apiVersion: v1
kind: Secret
metadata:
  name: secret-one
`)
	docs, err := ParseMultiDocYAML(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 documents, got %d", len(docs))
	}
	if docs[0].GetKind() != "ConfigMap" {
		t.Errorf("doc[0] kind = %q, want %q", docs[0].GetKind(), "ConfigMap")
	}
	if docs[1].GetKind() != "Secret" {
		t.Errorf("doc[1] kind = %q, want %q", docs[1].GetKind(), "Secret")
	}
}

func TestParseMultiDoc_LeadingTrailingSeparators(t *testing.T) {
	data := []byte(`---
apiVersion: v1
kind: ConfigMap
metadata:
  name: cm-one
---
apiVersion: v1
kind: Secret
metadata:
  name: secret-one
---
`)
	docs, err := ParseMultiDocYAML(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 documents, got %d", len(docs))
	}
	if docs[0].GetKind() != "ConfigMap" {
		t.Errorf("doc[0] kind = %q, want %q", docs[0].GetKind(), "ConfigMap")
	}
	if docs[1].GetKind() != "Secret" {
		t.Errorf("doc[1] kind = %q, want %q", docs[1].GetKind(), "Secret")
	}
}

func TestParseMultiDoc_EmptyInput(t *testing.T) {
	docs, err := ParseMultiDocYAML(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 0 {
		t.Fatalf("expected 0 documents, got %d", len(docs))
	}
}

func TestParseMultiDoc_LeadingWhitespace(t *testing.T) {
	data := []byte(`   

---
apiVersion: v1
kind: ConfigMap
metadata:
  name: test
`)
	docs, err := ParseMultiDocYAML(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 document, got %d", len(docs))
	}
	if docs[0].GetKind() != "ConfigMap" {
		t.Errorf("kind = %q, want %q", docs[0].GetKind(), "ConfigMap")
	}
}

func TestParseMultiDoc_InvalidYAML(t *testing.T) {
	data := []byte(`garbage: [[[`)
	_, err := ParseMultiDocYAML(data)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "multi-doc YAML") {
		t.Errorf("error should contain 'multi-doc YAML', got %q", err.Error())
	}
}

func TestParseMultiDoc_MissingKind(t *testing.T) {
	data := []byte(`apiVersion: v1
metadata:
  name: no-kind
`)
	_, err := ParseMultiDocYAML(data)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no kind") {
		t.Errorf("error should contain 'no kind', got %q", err.Error())
	}
}

func TestAppendResource_EmptyBuffer(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]interface{}{"name": "test"},
	}}
	result, err := AppendResource(nil, obj)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) == 0 {
		t.Fatal("result should not be empty")
	}
	if strings.HasPrefix(string(result), "---") {
		t.Error("should not have leading separator for empty buffer")
	}
}

func TestAppendResource_NonEmptyBuffer(t *testing.T) {
	existing := []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: cm-one
`)
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]interface{}{"name": "secret-one"},
	}}
	result, err := AppendResource(existing, obj)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(result), "---") {
		t.Error("result should contain '---' separator")
	}
}

func TestAppendResource_RoundTrip(t *testing.T) {
	doc1 := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]interface{}{"name": "cm-one"},
	}}
	doc2 := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]interface{}{"name": "secret-one"},
	}}
	doc3 := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]interface{}{"name": "cm-two"},
	}}

	data, err := AppendResource(nil, doc1)
	if err != nil {
		t.Fatalf("append doc1: %v", err)
	}
	data, err = AppendResource(data, doc2)
	if err != nil {
		t.Fatalf("append doc2: %v", err)
	}
	data, err = AppendResource(data, doc3)
	if err != nil {
		t.Fatalf("append doc3: %v", err)
	}

	parsed, err := ParseMultiDocYAML(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(parsed) != 3 {
		t.Fatalf("expected 3 documents, got %d", len(parsed))
	}
	if parsed[0].GetName() != "cm-one" {
		t.Errorf("doc[0] name = %q, want %q", parsed[0].GetName(), "cm-one")
	}
	if parsed[1].GetName() != "secret-one" {
		t.Errorf("doc[1] name = %q, want %q", parsed[1].GetName(), "secret-one")
	}
	if parsed[2].GetName() != "cm-two" {
		t.Errorf("doc[2] name = %q, want %q", parsed[2].GetName(), "cm-two")
	}
}

func TestSplitYAMLDocuments_ConsecutiveSeparators(t *testing.T) {
	// Test raw splitting with bare strings
	rawData := []byte("r1\n---\n---\n---\nr2")
	chunks := SplitYAMLDocuments(rawData)
	// Split produces: ["r1", "", "", "r2"] (4 chunks) because the \n
	// before each --- is not consumed by the separator regex.
	if len(chunks) != 4 {
		t.Fatalf("expected 4 raw chunks, got %d", len(chunks))
	}
	if string(chunks[0]) != "r1" {
		t.Errorf("chunk[0] = %q, want %q", string(chunks[0]), "r1")
	}
	if string(chunks[3]) != "r2" {
		t.Errorf("chunk[3] = %q, want %q", string(chunks[3]), "r2")
	}
	// Two empty chunks in the middle
	if string(chunks[1]) != "" {
		t.Errorf("chunk[1] = %q, want empty", string(chunks[1]))
	}
	if string(chunks[2]) != "" {
		t.Errorf("chunk[2] = %q, want empty", string(chunks[2]))
	}

	// Verify that ParseMultiDocYAML skips empties with valid YAML
	yamlData := []byte("kind: ConfigMap\nmetadata:\n  name: cm\n---\n---\n---\nkind: Secret\nmetadata:\n  name: secret\n")
	docs, err := ParseMultiDocYAML(yamlData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 documents, got %d", len(docs))
	}
	if docs[0].GetKind() != "ConfigMap" {
		t.Errorf("doc[0] kind = %q, want %q", docs[0].GetKind(), "ConfigMap")
	}
	if docs[1].GetKind() != "Secret" {
		t.Errorf("doc[1] kind = %q, want %q", docs[1].GetKind(), "Secret")
	}
}
