package schema

import (
	"errors"
	"testing"

	"github.com/foundry/flow/cartographer/internal/schemaerrors"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

func TestValidate_EntityPropertyCollidesWithID(t *testing.T) {
	s := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "id", Type: "string"},
				},
			},
		},
	}
	if err := Validate(s); err == nil {
		t.Fatal("expected schemaerrors.ErrImplicitColumnCollision, got nil")
	}
}

func TestValidate_EntityPropertyCollidesWithEmbeddingIndexed(t *testing.T) {
	s := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "Component",
				EnableVectorIndex: true,
				Properties: []*flowv1.Property{
					{Name: "embedding", Type: "string"},
				},
			},
		},
	}
	if err := Validate(s); err == nil {
		t.Fatal("expected schemaerrors.ErrImplicitColumnCollision, got nil")
	}
}

func TestValidate_EntityPropertyEmbeddingOKWhenNotIndexed(t *testing.T) {
	s := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "Component",
				EnableVectorIndex: false,
				Properties: []*flowv1.Property{
					{Name: "embedding", Type: "string"},
				},
			},
		},
	}
	if err := Validate(s); err != nil {
		t.Fatalf("expected nil error for non-indexed type with 'embedding' property, got: %v", err)
	}
}

// An edge property named "from" is rejected — in practice by the
// reserved-word check (FROM is in reservedWords, and that check runs before
// the collision branch), not the implicit-column collision branch. The
// collision branch's `p.Name == "from"` disjunct is retained so the SPEC R1
// implicit-column-collision guarantee ("Edge properties entries must not use
// the names id, from, to, type") is self-contained and survives any future
// trim of FROM from reservedWords. A property named "from" surfaces
// schemaerrors.ErrReservedWord today — also pinned by TestValidate_ReservedWordNewlyAdded.
// Both rows are INVALID_ARGUMENT
// at the wire (SPEC R1 error-table rows "Name is a LadybugDB reserved word" and
// implicit-column collision).
func TestValidate_EdgePropertyCollidesWithFrom(t *testing.T) {
	s := &flowv1.Schema{
		EdgeTypes: []*flowv1.EdgeType{
			{
				Name: "DEPENDS_ON",
				Properties: []*flowv1.Property{
					{Name: "from", Type: "string"},
				},
			},
		},
	}
	if err := Validate(s); err == nil {
		t.Fatal("expected schemaerrors.ErrReservedWord for edge property \"from\", got nil")
	} else if !errors.Is(err, schemaerrors.ErrReservedWord) {
		t.Fatalf("expected schemaerrors.ErrReservedWord for edge property \"from\", got: %v", err)
	}
}

func TestValidate_EdgePropertyCollidesWithTo(t *testing.T) {
	s := &flowv1.Schema{
		EdgeTypes: []*flowv1.EdgeType{
			{
				Name: "DEPENDS_ON",
				Properties: []*flowv1.Property{
					{Name: "to", Type: "string"},
				},
			},
		},
	}
	if err := Validate(s); err == nil {
		t.Fatal("expected schemaerrors.ErrImplicitColumnCollision, got nil")
	}
}

func TestValidate_EdgePropertyCollidesWithType(t *testing.T) {
	s := &flowv1.Schema{
		EdgeTypes: []*flowv1.EdgeType{
			{
				Name: "DEPENDS_ON",
				Properties: []*flowv1.Property{
					{Name: "type", Type: "string"},
				},
			},
		},
	}
	if err := Validate(s); err == nil {
		t.Fatal("expected schemaerrors.ErrImplicitColumnCollision, got nil")
	}
}

func TestValidate_EdgePropertyCollidesWithID(t *testing.T) {
	s := &flowv1.Schema{
		EdgeTypes: []*flowv1.EdgeType{
			{
				Name: "DEPENDS_ON",
				Properties: []*flowv1.Property{
					{Name: "id", Type: "string"},
				},
			},
		},
	}
	if err := Validate(s); err == nil {
		t.Fatal("expected schemaerrors.ErrImplicitColumnCollision, got nil")
	}
}