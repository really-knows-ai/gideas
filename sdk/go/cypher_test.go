package flow

import (
	"slices"
	"testing"
)

func TestExtractLabels_SimpleMatch(t *testing.T) {
	labels := extractEntityTypes("MATCH (c:Component) RETURN c")
	if len(labels) != 1 || labels[0] != "Component" {
		t.Errorf("expected [Component], got %v", labels)
	}
}

func TestExtractLabels_MultiLabel(t *testing.T) {
	labels := extractEntityTypes("MATCH (c:Component:Service) RETURN c")
	expected := []string{"Component", "Service"}
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
	expected := []string{"Service", "Component"}
	if !slices.Equal(labels, expected) {
		t.Errorf("expected %v, got %v", expected, labels)
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

func TestExtractLabels_MultiMatch(t *testing.T) {
	labels := extractEntityTypes("MATCH (c:Component) MATCH (s:Service) RETURN c, s")
	expected := []string{"Component", "Service"}
	if !slices.Equal(labels, expected) {
		t.Errorf("expected %v, got %v", expected, labels)
	}
}

func TestExtractLabels_DuplicateTypes(t *testing.T) {
	labels := extractEntityTypes("MATCH (c:Component)-->(s:Component) RETURN c, s")
	expected := []string{"Component"}
	if !slices.Equal(labels, expected) {
		t.Errorf("expected [Component], got %v", labels)
	}
}
