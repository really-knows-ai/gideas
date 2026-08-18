package schema

import (
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

func TestValidate_ValidSchema(t *testing.T) {
	s := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string", Required: true},
				},
				EnableVectorIndex: false,
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"Component"}, Using: []string{"DEPENDS_ON"}},
				},
			},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{
				Name: "DEPENDS_ON",
				Properties: []*flowv1.Property{
					{Name: "weight", Type: "string"},
				},
			},
		},
	}
	if err := Validate(s); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
}

// Names are case-sensitive (SPEC R1, "Case-sensitivity — Names are case-sensitive"):
// type/property names differing only by case are distinct, not duplicates. Reserved
// words are matched case-insensitively (validate.go uppercases before lookup), but
// duplicate detection is exact-match, so "Component" and "component" are distinct
// types/properties and must both be accepted.
func TestValidate_NamesCaseSensitive(t *testing.T) {
	// Entity types differing only by case are distinct.
	s := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component"},
			{Name: "component"},
		},
	}
	if err := Validate(s); err != nil {
		t.Fatalf("expected nil error for case-differing entity type names, got: %v", err)
	}

	// Edge types differing only by case are distinct.
	s = &flowv1.Schema{
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "DEPENDS_ON"},
			{Name: "depends_on"},
		},
	}
	if err := Validate(s); err != nil {
		t.Fatalf("expected nil error for case-differing edge type names, got: %v", err)
	}

	// Property names differing only by case within a type are distinct.
	s = &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
					{Name: "Name", Type: "string"},
				},
			},
		},
	}
	if err := Validate(s); err != nil {
		t.Fatalf("expected nil error for case-differing property names, got: %v", err)
	}
}

func TestValidate_EmptySchema(t *testing.T) {
	s := &flowv1.Schema{}
	if err := Validate(s); err != nil {
		t.Fatalf("expected nil error for empty schema, got: %v", err)
	}
}

func TestValidate_CrossTypeNameDuplicationAllowed(t *testing.T) {
	s := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Review"},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "Review"},
		},
	}
	if err := Validate(s); err != nil {
		t.Fatalf("expected nil error for cross-list name overlap, got: %v", err)
	}
}