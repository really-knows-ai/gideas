package schema

import (
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

func TestValidate_UndeclaredEntityInCanConnectTo(t *testing.T) {
	s := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"NonExistentType"}, Using: []string{"DEPENDS_ON"}},
				},
			},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "DEPENDS_ON"},
		},
	}
	if err := Validate(s); err == nil {
		t.Fatal("expected schemaerrors.ErrUndeclaredTypeRef, got nil")
	}
}

func TestValidate_UndeclaredEdgeInUsing(t *testing.T) {
	s := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"Component"}, Using: []string{"NonExistentEdge"}},
				},
			},
		},
	}
	if err := Validate(s); err == nil {
		t.Fatal("expected schemaerrors.ErrUndeclaredTypeRef, got nil")
	}
}

func TestValidate_EmptyCanConnectTo(t *testing.T) {
	s := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: nil, Using: []string{"DEPENDS_ON"}},
				},
			},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "DEPENDS_ON"},
		},
	}
	if err := Validate(s); err == nil {
		t.Fatal("expected schemaerrors.ErrEmptyRuleList, got nil")
	}
}

func TestValidate_EmptyUsing(t *testing.T) {
	s := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"Component"}, Using: nil},
				},
			},
		},
	}
	if err := Validate(s); err == nil {
		t.Fatal("expected schemaerrors.ErrEmptyRuleList, got nil")
	}
}

func TestValidate_SelfReferencingRule(t *testing.T) {
	s := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"Component"}, Using: []string{"DEPENDS_ON"}},
				},
			},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "DEPENDS_ON"},
		},
	}
	if err := Validate(s); err != nil {
		t.Fatalf("expected nil error for self-referencing rule, got: %v", err)
	}
}