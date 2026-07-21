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

func TestValidate_DuplicateEntityTypeName(t *testing.T) {
	s := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "Component"},
			{Name: "Component"},
		},
	}
	if err := Validate(s); err == nil {
		t.Fatal("expected ErrDuplicateTypeName, got nil")
	} else if err.Error() != ErrDuplicateTypeName.Error() && !contains(err.Error(), "duplicate type name") {
		// Check via errors.Is-style prefix matching
		if err.Error() != "duplicate type name: \"Component\"" {
			t.Fatalf("expected duplicate type name error, got: %v", err)
		}
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
		t.Fatal("expected ErrDuplicateTypeName, got nil")
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
		t.Fatal("expected ErrDuplicatePropertyName, got nil")
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
		t.Fatal("expected ErrDuplicatePropertyName, got nil")
	}
}

func TestValidate_InvalidTypeName(t *testing.T) {
	s := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "1Component"},
		},
	}
	if err := Validate(s); err == nil {
		t.Fatal("expected ErrInvalidName, got nil")
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
		t.Fatal("expected ErrInvalidName, got nil")
	}
}

func TestValidate_ReservedWordTypeName(t *testing.T) {
	s := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "MATCH"},
		},
	}
	if err := Validate(s); err == nil {
		t.Fatal("expected ErrReservedWord, got nil")
	}
}

func TestValidate_ReservedWordPropertyName(t *testing.T) {
	s := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "CREATE", Type: "string"},
				},
			},
		},
	}
	if err := Validate(s); err == nil {
		t.Fatal("expected ErrReservedWord, got nil")
	}
}

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
		t.Fatal("expected ErrImplicitColumnCollision, got nil")
	}
}

func TestValidate_EntityPropertyCollidesWithProperties(t *testing.T) {
	s := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name: "Component",
				Properties: []*flowv1.Property{
					{Name: "_properties", Type: "string"},
				},
			},
		},
	}
	if err := Validate(s); err == nil {
		t.Fatal("expected ErrImplicitColumnCollision, got nil")
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
		t.Fatal("expected ErrImplicitColumnCollision, got nil")
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
		t.Fatal("expected ErrImplicitColumnCollision, got nil")
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
		t.Fatal("expected ErrImplicitColumnCollision, got nil")
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
		t.Fatal("expected ErrImplicitColumnCollision, got nil")
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
		t.Fatal("expected ErrImplicitColumnCollision, got nil")
	}
}

func TestValidate_EdgePropertyCollidesWithProperties(t *testing.T) {
	s := &flowv1.Schema{
		EdgeTypes: []*flowv1.EdgeType{
			{
				Name: "DEPENDS_ON",
				Properties: []*flowv1.Property{
					{Name: "_properties", Type: "string"},
				},
			},
		},
	}
	if err := Validate(s); err == nil {
		t.Fatal("expected ErrImplicitColumnCollision, got nil")
	}
}

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
		t.Fatal("expected ErrInvalidPropertyType, got nil")
	}
}

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
		t.Fatal("expected ErrUndeclaredTypeRef, got nil")
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
		t.Fatal("expected ErrUndeclaredTypeRef, got nil")
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
		t.Fatal("expected ErrEmptyRuleList, got nil")
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
		t.Fatal("expected ErrEmptyRuleList, got nil")
	}
}

func TestValidate_EmptySchema(t *testing.T) {
	s := &flowv1.Schema{}
	if err := Validate(s); err != nil {
		t.Fatalf("expected nil error for empty schema, got: %v", err)
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

func TestValidate_ReservedWordEdgeTypeName(t *testing.T) {
	s := &flowv1.Schema{
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "WHERE"},
		},
	}
	if err := Validate(s); err == nil {
		t.Fatal("expected ErrReservedWord, got nil")
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

// contains is a helper for substring matching.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsStr(s, substr)
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
