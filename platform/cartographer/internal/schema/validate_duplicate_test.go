package schema

import (
	"errors"
	"testing"

	"github.com/foundry/flow/cartographer/internal/schemaerrors"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

func TestValidate_DuplicateEntityTypeName(t *testing.T) {
	s := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component"},
			{Name: "Component"},
		},
	}
	if err := Validate(s); err == nil {
		t.Fatal("expected schemaerrors.ErrDuplicateTypeName, got nil")
	} else if !errors.Is(err, schemaerrors.ErrDuplicateTypeName) {
		t.Fatalf("expected schemaerrors.ErrDuplicateTypeName, got: %v", err)
	}
}

func TestValidate_DuplicateEdgeTypeName(t *testing.T) {
	s := &flowv1.Schema{
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "DEPENDS_ON"},
			{Name: "DEPENDS_ON"},
		},
	}
	if err := Validate(s); err == nil {
		t.Fatal("expected schemaerrors.ErrDuplicateTypeName, got nil")
	} else if !errors.Is(err, schemaerrors.ErrDuplicateTypeName) {
		t.Fatalf("expected schemaerrors.ErrDuplicateTypeName, got: %v", err)
	}
}

// Format validation runs before duplicate detection (validate.go), so two
// identically-named but invalid types or properties surface schemaerrors.ErrInvalidName —
// the structural defect — not schemaerrors.ErrDuplicateTypeName/schemaerrors.ErrDuplicatePropertyName.
// Both are INVALID_ARGUMENT at the wire; the ordering only fixes the
// diagnostic.
func TestValidate_InvalidNameBeforeDuplicateDetection(t *testing.T) {
	// Two empty-named entity types: the invalid name, not the duplicate, is
	// the defect reported.
	s := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: ""},
			{Name: ""},
		},
	}
	if err := Validate(s); err == nil {
		t.Fatal("expected schemaerrors.ErrInvalidName, got nil")
	} else if !errors.Is(err, schemaerrors.ErrInvalidName) {
		t.Fatalf("expected schemaerrors.ErrInvalidName for two empty-named entity types, got: %v", err)
	}

	// Two empty-named properties in one entity type: same ordering.
	s = &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{{
			Name: "Component",
			Properties: []*flowv1.Property{
				{Name: "", Type: "string"},
				{Name: "", Type: "string"},
			},
		}},
	}
	if err := Validate(s); err == nil {
		t.Fatal("expected schemaerrors.ErrInvalidName, got nil")
	} else if !errors.Is(err, schemaerrors.ErrInvalidName) {
		t.Fatalf("expected schemaerrors.ErrInvalidName for two empty-named properties, got: %v", err)
	}
}

func TestValidate_DuplicatePropertyNameOnEntity(t *testing.T) {
	s := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "string"},
					{Name: "name", Type: "string"},
				},
			},
		},
	}
	if err := Validate(s); err == nil {
		t.Fatal("expected schemaerrors.ErrDuplicatePropertyName, got nil")
	}
}

func TestValidate_DuplicatePropertyNameOnEdge(t *testing.T) {
	s := &flowv1.Schema{
		EdgeTypes: []*flowv1.EdgeType{
			{
				Name: "DEPENDS_ON",
				Properties: []*flowv1.Property{
					{Name: "weight", Type: "string"},
					{Name: "weight", Type: "string"},
				},
			},
		},
	}
	if err := Validate(s); err == nil {
		t.Fatal("expected schemaerrors.ErrDuplicatePropertyName, got nil")
	}
}
