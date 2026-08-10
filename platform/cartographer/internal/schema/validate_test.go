package schema

import (
	"errors"
	"strings"
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
	} else if !errors.Is(err, ErrDuplicateTypeName) {
		t.Fatalf("expected ErrDuplicateTypeName, got: %v", err)
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
			t.Fatal("expected ErrInvalidPropertyType, got nil")
		} else if !errors.Is(err, ErrInvalidPropertyType) {
			t.Fatalf("expected ErrInvalidPropertyType, got: %v", err)
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

// A nil schema (unset proto3 message field — an ApplySchemaRequest without a
// schema is forwarded unguarded to Validate by the handler) is equivalent to
// an empty schema: SPEC:86 permits empty or omitted entityTypes/edgeTypes
// arrays, so a fully omitted schema must validate, not panic. Nil
// EntityTypes/EdgeTypes fields on a non-nil schema must likewise be treated
// as empty lists.
func TestValidate_NilSchema(t *testing.T) {
	if err := Validate(nil); err != nil {
		t.Fatalf("expected nil error for nil schema, got: %v", err)
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
// or *ConnectionRule) must be rejected with ErrNilElement, never panicked on.
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
				t.Fatal("expected ErrNilElement, got nil")
			} else if !errors.Is(err, ErrNilElement) {
				t.Fatalf("expected ErrNilElement, got: %v", err)
			}
		})
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

// reservedWords are matched case-insensitively (validate.go uppercases names
// before lookup), so lowercase reserved words must be rejected too.
func TestValidate_ReservedWordCaseInsensitive(t *testing.T) {
	tests := []struct {
		name string
		s    *flowv1.Schema
	}{
		{"entity type name lowercased", &flowv1.Schema{
			EntityTypes: []*flowv1.EntityType{{Name: "match"}},
		}},
		{"entity property name lowercased", &flowv1.Schema{
			EntityTypes: []*flowv1.EntityType{{
				Name:       "Component",
				Properties: []*flowv1.Property{{Name: "create", Type: "string"}},
			}},
		}},
		{"edge type name lowercased", &flowv1.Schema{
			EdgeTypes: []*flowv1.EdgeType{{Name: "where"}},
		}},
		{"edge property name lowercased", &flowv1.Schema{
			EdgeTypes: []*flowv1.EdgeType{{
				Name:       "DEPENDS_ON",
				Properties: []*flowv1.Property{{Name: "using", Type: "string"}},
			}},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := Validate(tc.s); err == nil {
				t.Fatal("expected ErrReservedWord, got nil")
			} else if !errors.Is(err, ErrReservedWord) {
				t.Fatalf("expected ErrReservedWord, got: %v", err)
			}
		})
	}
}

// TestValidate_ReservedWordNewlyAdded verifies that keywords added to close
// the gap between the hand-maintained list and the engine's reserved words
// are now rejected at validation time (SPEC R1, error-table row "Name is a
// LadybugDB reserved word" → INVALID_ARGUMENT), not at table-creation time
// (FAILED_PRECONDITION).
func TestValidate_ReservedWordNewlyAdded(t *testing.T) {
	newlyAdded := []string{
		"CONSTRAINT", "COUNT", "CYPHER", "EXPLAIN", "FILTER",
		"FROM", "NODE", "NONE", "PROFILE", "REDUCE", "SHOW", "UNIQUE",
	}
	for _, kw := range newlyAdded {
		t.Run("entity type/"+kw, func(t *testing.T) {
			s := &flowv1.Schema{
				EntityTypes: []*flowv1.EntityType{{Name: kw}},
			}
			if err := Validate(s); err == nil {
				t.Fatalf("expected ErrReservedWord for %q, got nil", kw)
			} else if !errors.Is(err, ErrReservedWord) {
				t.Fatalf("expected ErrReservedWord for %q, got: %v", kw, err)
			}
		})
		t.Run("entity property/"+kw, func(t *testing.T) {
			s := &flowv1.Schema{
				EntityTypes: []*flowv1.EntityType{{
					Name:       "Component",
					Properties: []*flowv1.Property{{Name: kw, Type: "string"}},
				}},
			}
			if err := Validate(s); err == nil {
				t.Fatalf("expected ErrReservedWord for property %q, got nil", kw)
			} else if !errors.Is(err, ErrReservedWord) {
				t.Fatalf("expected ErrReservedWord for property %q, got: %v", kw, err)
			}
		})
		t.Run("edge type/"+kw, func(t *testing.T) {
			s := &flowv1.Schema{
				EdgeTypes: []*flowv1.EdgeType{{Name: kw}},
			}
			if err := Validate(s); err == nil {
				t.Fatalf("expected ErrReservedWord for edge type %q, got nil", kw)
			} else if !errors.Is(err, ErrReservedWord) {
				t.Fatalf("expected ErrReservedWord for edge type %q, got: %v", kw, err)
			}
		})
		t.Run("edge property/"+kw, func(t *testing.T) {
			s := &flowv1.Schema{
				EdgeTypes: []*flowv1.EdgeType{{
					Name:       "DEPENDS_ON",
					Properties: []*flowv1.Property{{Name: kw, Type: "string"}},
				}},
			}
			if err := Validate(s); err == nil {
				t.Fatalf("expected ErrReservedWord for edge property %q, got nil", kw)
			} else if !errors.Is(err, ErrReservedWord) {
				t.Fatalf("expected ErrReservedWord for edge property %q, got: %v", kw, err)
			}
		})
	}
}

// The store's internal placeholder NODE table for edgeless rel types is named
// `_untyped` (UntypedTableName). It is a valid Cypher identifier (passes the
// regex), so it must be explicitly reserved: a user entity or edge type with
// that name would alias the placeholder table and be silently skipped by the
// store's reopen structural check (validateMetadataAgainstCatalog). Both
// entity and edge type names must be rejected with ErrReservedWord (→
// INVALID_ARGUMENT at the gRPC boundary).
func TestValidate_ReservedUntypedPlaceholderName(t *testing.T) {
	tests := []struct {
		name string
		s    *flowv1.Schema
	}{
		{"entity type name", &flowv1.Schema{
			EntityTypes: []*flowv1.EntityType{{Name: UntypedTableName}},
		}},
		{"edge type name", &flowv1.Schema{
			EdgeTypes: []*flowv1.EdgeType{{Name: UntypedTableName}},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := Validate(tc.s); err == nil {
				t.Fatal("expected ErrReservedWord, got nil")
			} else {
				if !errors.Is(err, ErrReservedWord) {
					t.Fatalf("expected ErrReservedWord, got: %v", err)
				}
				// SPEC:937 has its own error-table row ("Name is the reserved
				// internal placeholder") distinct from SPEC:936 ("Name is a
				// LadybugDB reserved word"), so the wire message must
				// identify the placeholder, not read as a plain reserved word.
				if !strings.Contains(err.Error(), "reserved internal placeholder") {
					t.Fatalf("expected placeholder-distinguishing message, got: %v", err)
				}
			}
		})
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
			if !tc.valid && err != nil && !errors.Is(err, ErrInvalidName) {
				t.Fatalf("expected ErrInvalidName for %q, got: %v", tc.typeName, err)
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
				if !b.valid && err != nil && !errors.Is(err, ErrInvalidName) {
					t.Fatalf("expected ErrInvalidName for %q, got: %v", b.prop, err)
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
				t.Fatal("expected ErrInvalidName for 256-char name, got nil")
			} else if !errors.Is(err, ErrInvalidName) {
				t.Fatalf("expected ErrInvalidName, got: %v", err)
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
