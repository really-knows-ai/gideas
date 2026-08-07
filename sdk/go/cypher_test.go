package flow

import (
	"slices"
	"testing"
)

const componentType = "Component"

const serviceType = "Service"

func TestExtractLabels_SimpleMatch(t *testing.T) {
	labels := extractEntityTypes("MATCH (c:Component) RETURN c")
	if len(labels) != 1 || labels[0] != componentType {
		t.Errorf("expected [%s], got %v", componentType, labels)
	}
}

func TestExtractLabels_MultiLabel(t *testing.T) {
	labels := extractEntityTypes("MATCH (c:Component:Service) RETURN c")
	expected := []string{componentType, "Service"}
	if !slices.Equal(labels, expected) {
		t.Errorf("expected %v, got %v", expected, labels)
	}
}

func TestExtractLabels_NoLabels(t *testing.T) {
	labels := extractEntityTypes("MATCH (n) RETURN n")
	if labels != nil {
		t.Errorf("expected nil, got %v", labels)
	}
}

func TestExtractLabels_RelationshipPattern(t *testing.T) {
	labels := extractEntityTypes("MATCH (a:Service)-[:DEPENDS_ON]->(b:Component) RETURN a, b")
	expected := []string{"Service", componentType}
	if !slices.Equal(labels, expected) {
		t.Errorf("expected %v, got %v", expected, labels)
	}
}

// TestExtractLabels_PropertyMapPattern pins the property-map node-pattern
// shape (SPEC R3): MATCH (c:Component {name: 'x'}) must still extract its
// labels. If the regex misses the shape, extractEntityTypes returns nil and
// the ExecuteCypher callers collapse to the READ:graph/entity/* wildcard,
// blocking a caller that holds only READ:graph/entity/Component under the
// Sidecar's mode-1 check.
func TestExtractLabels_PropertyMapPattern(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cypher   string
		expected []string
	}{
		{"spaced", "MATCH (c:Component {name: 'x'}) RETURN c", []string{componentType}},
		{"compact", "MATCH (c:Component{name:'x'}) RETURN c", []string{componentType}},
		{"multi-label", "MATCH (c:Component:Service {name: 'x'}) RETURN c", []string{componentType, "Service"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			labels := extractEntityTypes(tc.cypher)
			if !slices.Equal(labels, tc.expected) {
				t.Errorf("expected %v, got %v", tc.expected, labels)
			}
		})
	}
}

func TestExtractLabels_InvalidCypher(t *testing.T) {
	labels := extractEntityTypes("NOT VALID {{ syntax")
	if labels != nil {
		t.Errorf("expected nil for invalid cypher, got %v", labels)
	}
}

func TestExtractLabels_Empty(t *testing.T) {
	labels := extractEntityTypes("")
	if labels != nil {
		t.Errorf("expected nil for empty cypher, got %v", labels)
	}
}

func TestExtractLabels_CreateStatement(t *testing.T) {
	// CREATE is not MATCH, so no labels should be extracted
	labels := extractEntityTypes("CREATE (c:Component {name: 'test'})")
	if labels != nil {
		t.Errorf("expected nil for CREATE, got %v", labels)
	}
}

func TestExtractLabels_NonReadOnlyStatement(t *testing.T) {
	// A successfully-classified mutation (non-read-only) must NOT collapse to
	// the READ:graph/entity/* wildcard. R3's wildcard fallback applies only to
	// genuine parser failure; a mutation that parses still references specific
	// entity types, so extractEntityTypes must return them rather than nil.
	labels := extractEntityTypes("MATCH (c:Component) DELETE c")
	expected := []string{componentType}
	if !slices.Equal(labels, expected) {
		t.Errorf("expected %v for mutation, got %v", expected, labels)
	}
}

func TestExtractLabels_MultiMatch(t *testing.T) {
	labels := extractEntityTypes("MATCH (c:Component) MATCH (s:Service) RETURN c, s")
	expected := []string{componentType, "Service"}
	if !slices.Equal(labels, expected) {
		t.Errorf("expected %v, got %v", expected, labels)
	}
}

func TestExtractLabels_DuplicateTypes(t *testing.T) {
	labels := extractEntityTypes("MATCH (c:Component)-->(s:Component) RETURN c, s")
	expected := []string{componentType}
	if !slices.Equal(labels, expected) {
		t.Errorf("expected [%s], got %v", componentType, labels)
	}
}
