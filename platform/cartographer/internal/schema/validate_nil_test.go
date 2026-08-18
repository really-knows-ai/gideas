package schema

import (
	"errors"
	"testing"

	"github.com/foundry/flow/cartographer/internal/schemaerrors"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// A nil schema (an unset proto3 message field — an ApplySchemaRequest without
// a schema is forwarded unguarded to Validate by the handler) is a malformed
// request, not an empty schema: SPEC:86 permits empty or omitted
// entityTypes/edgeTypes arrays *within* a schema message, but a fully omitted
// schema would be dereferenced by the store (diffSchemaAgainstCatalog →
// collectFromToPairs read s.EntityTypes) after validation and panic, so
// Validate must reject it with schemaerrors.ErrNilSchema (→ INVALID_ARGUMENT at the gRPC
// boundary via the service's isSchemaError). Nil EntityTypes/EdgeTypes fields
// on a non-nil schema are still treated as empty lists.
func TestValidate_NilSchema(t *testing.T) {
	err := Validate(nil)
	if err == nil {
		t.Fatal("expected schemaerrors.ErrNilSchema for nil schema, got nil")
	} else if !errors.Is(err, schemaerrors.ErrNilSchema) {
		t.Fatalf("expected schemaerrors.ErrNilSchema for nil schema, got: %v", err)
	}

	// Nil EntityTypes / EdgeTypes fields treated as empty lists.
	s := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component", Properties: []*flowv1.Property{{Name: "name", Type: "string"}}},
		},
		EdgeTypes: nil,
	}
	if err := Validate(s); err != nil {
		t.Fatalf("expected nil error for nil EdgeTypes, got: %v", err)
	}

	s = &flowv1.Schema{
		EntityTypes: nil,
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "DEPENDS_ON"},
		},
	}
	if err := Validate(s); err != nil {
		t.Fatalf("expected nil error for nil EntityTypes, got: %v", err)
	}
}

// A nil element inside a repeated field (nil *EntityType, *EdgeType, *Property,
// or *ConnectionRule) must be rejected with schemaerrors.ErrNilElement, never panicked on.
// Validate guards every repeated-field element before dereferencing it
// (validate.go), so a malformed ApplySchema request forwarded to Validate is
// an INVALID_ARGUMENT, not a crash.
func TestValidate_NilElements(t *testing.T) {
	tests := []struct {
		name string
		s    *flowv1.Schema
	}{
		{"nil entity type element", &flowv1.Schema{
			EntityTypes: []*flowv1.EntityType{nil},
		}},
		{"nil edge type element", &flowv1.Schema{
			EdgeTypes: []*flowv1.EdgeType{nil},
		}},
		{"nil property element in entity type", &flowv1.Schema{
			EntityTypes: []*flowv1.EntityType{{
				Name:       "Component",
				Properties: []*flowv1.Property{nil},
			}},
		}},
		{"nil property element in edge type", &flowv1.Schema{
			EdgeTypes: []*flowv1.EdgeType{{
				Name:       "DEPENDS_ON",
				Properties: []*flowv1.Property{nil},
			}},
		}},
		{"nil rule element", &flowv1.Schema{
			EntityTypes: []*flowv1.EntityType{{
				Name:  "Component",
				Rules: []*flowv1.ConnectionRule{nil},
			}},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.s)
			if err == nil {
				t.Fatal("expected schemaerrors.ErrNilElement, got nil")
			} else if !errors.Is(err, schemaerrors.ErrNilElement) {
				t.Fatalf("expected schemaerrors.ErrNilElement, got: %v", err)
			}
		})
	}
}