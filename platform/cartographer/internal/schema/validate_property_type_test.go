package schema

import (
	"errors"
	"testing"

	"github.com/foundry/flow/cartographer/internal/schemaerrors"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

func TestValidate_InvalidPropertyType(t *testing.T) {
	s := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "name", Type: "int"},
				},
			},
		},
	}
	if err := Validate(s); err == nil {
		t.Fatal("expected schemaerrors.ErrInvalidPropertyType, got nil")
	}
}

// SPEC:890 "Invalid property type in schema" applies to edge properties just
// as it does to entity properties (validate.go validateEdgeType §7), so a
// non-string edge property must be rejected and a string-typed one accepted.
func TestValidate_EdgePropertyType(t *testing.T) {
	t.Run("non-string edge property rejected", func(t *testing.T) {
		s := &flowv1.Schema{
			EdgeTypes: []*flowv1.EdgeType{
				{
					Name: "DEPENDS_ON",
					Properties: []*flowv1.Property{
						{Name: "weight", Type: "int"},
					},
				},
			},
		}
		if err := Validate(s); err == nil {
			t.Fatal("expected schemaerrors.ErrInvalidPropertyType, got nil")
		} else if !errors.Is(err, schemaerrors.ErrInvalidPropertyType) {
			t.Fatalf("expected schemaerrors.ErrInvalidPropertyType, got: %v", err)
		}
	})

	t.Run("string-typed edge property accepted", func(t *testing.T) {
		s := &flowv1.Schema{
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
			t.Fatalf("expected nil error for string-typed edge property, got: %v", err)
		}
	})
}