package schema

import (
	"errors"
	"strings"
	"testing"

	"github.com/foundry/flow/cartographer/internal/schemaerrors"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

func TestValidate_InvalidTypeName(t *testing.T) {
	s := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "1Component"},
		},
	}
	if err := Validate(s); err == nil {
		t.Fatal("expected schemaerrors.ErrInvalidName, got nil")
	}
}

func TestValidate_InvalidPropertyName(t *testing.T) {
	s := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "my-prop", Type: "string"},
				},
			},
		},
	}
	if err := Validate(s); err == nil {
		t.Fatal("expected schemaerrors.ErrInvalidName, got nil")
	}
}

func TestValidate_CypherIdentifierBoundaryCases(t *testing.T) {
	tests := []struct {
		name     string
		typeName string
		valid    bool
	}{
		{"single letter", "a", true},
		{"underscore only", "_", true},
		{"trailing digits", "type42", true},
		{"starts with digit", "1type", false},
		{"contains hyphen", "my-type", false},
		{"empty string", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &flowv1.Schema{
				EntityTypes: []*flowv1.EntityType{
					{Name: tc.typeName},
				},
			}
			err := Validate(s)
			if tc.valid && err != nil {
				t.Fatalf("expected nil error for %q, got: %v", tc.typeName, err)
			}
			if !tc.valid && err == nil {
				t.Fatalf("expected error for %q, got nil", tc.typeName)
			}
		})
	}
}

// The Cypher-identifier regex requirement (SPEC R1, error-table row "Name
// violates Cypher identifier regex" → INVALID_ARGUMENT, SPEC:888) applies to
// edge type names and edge property names exactly as it does to entity type
// names and entity property names (validate.go validateEdgeType). TestValidate_
// CypherIdentifierBoundaryCases above exercises only entity type names, so pin
// the edge-side regex branches here, mirroring that test's shape.
func TestValidate_CypherIdentifierBoundaryCasesEdgeType(t *testing.T) {
	tests := []struct {
		name     string
		typeName string
		valid    bool
	}{
		{"single letter", "a", true},
		{"underscore only", "_", true},
		{"trailing digits", "type42", true},
		{"starts with digit", "1type", false},
		{"contains hyphen", "my-type", false},
		{"empty string", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &flowv1.Schema{
				EdgeTypes: []*flowv1.EdgeType{
					{Name: tc.typeName},
				},
			}
			err := Validate(s)
			if tc.valid && err != nil {
				t.Fatalf("expected nil error for %q, got: %v", tc.typeName, err)
			}
			if !tc.valid && err == nil {
				t.Fatalf("expected error for %q, got nil", tc.typeName)
			}
			if !tc.valid && err != nil && !errors.Is(err, schemaerrors.ErrInvalidName) {
				t.Fatalf("expected schemaerrors.ErrInvalidName for %q, got: %v", tc.typeName, err)
			}
		})
	}
}

// The Cypher-identifier regex requirement (SPEC R1, error-table row "Name
// violates Cypher identifier regex" → INVALID_ARGUMENT, SPEC:932) applies to
// property names on edge types and entity types alike (validate.go
// validateEdgeType / validateEntityType). Pin both property-name regex
// branches here with a full boundary table, mirroring the type-name boundary
// tests above. The two containers share one table-driven test (rather than
// sibling copies) so the boundary set is exercised identically on each.
func TestValidate_CypherIdentifierBoundaryCasesProperty(t *testing.T) {
	containers := []struct {
		name string
		s    func(propName string) *flowv1.Schema
	}{
		{"entity property", func(p string) *flowv1.Schema {
			return &flowv1.Schema{EntityTypes: []*flowv1.EntityType{{
				Name:       "Component",
				Properties: []*flowv1.Property{{Name: p, Type: "string"}},
			}}}
		}},
		{"edge property", func(p string) *flowv1.Schema {
			return &flowv1.Schema{EdgeTypes: []*flowv1.EdgeType{{
				Name:       "DEPENDS_ON",
				Properties: []*flowv1.Property{{Name: p, Type: "string"}},
			}}}
		}},
	}
	boundaries := []struct {
		name  string
		prop  string
		valid bool
	}{
		{"single letter", "a", true},
		{"underscore only", "_", true},
		{"trailing digits", "prop42", true},
		{"starts with digit", "1prop", false},
		{"contains hyphen", "my-prop", false},
		{"empty string", "", false},
	}
	for _, c := range containers {
		for _, b := range boundaries {
			t.Run(c.name+"/"+b.name, func(t *testing.T) {
				err := Validate(c.s(b.prop))
				if b.valid && err != nil {
					t.Fatalf("expected nil error for %q, got: %v", b.prop, err)
				}
				if !b.valid && err == nil {
					t.Fatalf("expected error for %q, got nil", b.prop)
				}
				if !b.valid && err != nil && !errors.Is(err, schemaerrors.ErrInvalidName) {
					t.Fatalf("expected schemaerrors.ErrInvalidName for %q, got: %v", b.prop, err)
				}
			})
		}
	}
}

func TestValidate_NameLengthBoundary(t *testing.T) {
	// 255-char name is the maximum allowed length (SPEC R1, validateName in validate.go).
	long255 := "x" + strings.Repeat("a", 254)
	if len(long255) != 255 {
		t.Fatalf("test string length is %d, expected 255", len(long255))
	}

	// 256-char name should be rejected.
	long256 := "x" + strings.Repeat("a", 255)
	if len(long256) != 256 {
		t.Fatalf("test string length is %d, expected 256", len(long256))
	}

	// The maxNameLength boundary (SPEC R1) applies to entity type names, edge
	// type names, and property names alike, so exercise it on each.
	cases := []struct {
		name string
		s    func(name string) *flowv1.Schema
	}{
		{"entity type name", func(n string) *flowv1.Schema {
			return &flowv1.Schema{EntityTypes: []*flowv1.EntityType{{Name: n}}}
		}},
		{"edge type name", func(n string) *flowv1.Schema {
			return &flowv1.Schema{EdgeTypes: []*flowv1.EdgeType{{Name: n}}}
		}},
		{"entity property name", func(n string) *flowv1.Schema {
			return &flowv1.Schema{EntityTypes: []*flowv1.EntityType{{
				Name:       "Component",
				Properties: []*flowv1.Property{{Name: n, Type: "string"}},
			}}}
		}},
		{"edge property name", func(n string) *flowv1.Schema {
			return &flowv1.Schema{EdgeTypes: []*flowv1.EdgeType{{
				Name:       "DEPENDS_ON",
				Properties: []*flowv1.Property{{Name: n, Type: "string"}},
			}}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/255-char accepted", func(t *testing.T) {
			if err := Validate(tc.s(long255)); err != nil {
				t.Fatalf("expected nil error for 255-char name, got: %v", err)
			}
		})
		t.Run(tc.name+"/256-char rejected", func(t *testing.T) {
			if err := Validate(tc.s(long256)); err == nil {
				t.Fatal("expected schemaerrors.ErrInvalidName for 256-char name, got nil")
			} else if !errors.Is(err, schemaerrors.ErrInvalidName) {
				t.Fatalf("expected schemaerrors.ErrInvalidName, got: %v", err)
			}
		})
	}
}