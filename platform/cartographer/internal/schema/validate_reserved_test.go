package schema

import (
	"errors"
	"strings"
	"testing"

	"github.com/foundry/flow/cartographer/internal/schemaerrors"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

func TestValidate_ReservedWordTypeName(t *testing.T) {
	s := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{Name: "MATCH"},
		},
	}
	if err := Validate(s); err == nil {
		t.Fatal("expected schemaerrors.ErrReservedWord, got nil")
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
		t.Fatal("expected schemaerrors.ErrReservedWord, got nil")
	}
}

func TestValidate_ReservedWordEdgeTypeName(t *testing.T) {
	s := &flowv1.Schema{
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "WHERE"},
		},
	}
	if err := Validate(s); err == nil {
		t.Fatal("expected schemaerrors.ErrReservedWord, got nil")
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
				t.Fatal("expected schemaerrors.ErrReservedWord, got nil")
			} else if !errors.Is(err, schemaerrors.ErrReservedWord) {
				t.Fatalf("expected schemaerrors.ErrReservedWord, got: %v", err)
			}
		})
	}
}

// TestValidate_ReservedWordNewlyAdded verifies that keywords added to close
// the gap between the hand-maintained list and the engine's reserved words
// are now rejected at validation time (SPEC R1, error-table row "Name is a
// LadybugDB reserved word" → INVALID_ARGUMENT). The engine never rejects such
// names at table-creation time — the store's DDL is backtick-quoted via
// quoteID and the engine accepts backtick-quoted reserved words — so
// validation is the only guard; a word absent from reservedWords is applied
// silently.
func TestValidate_ReservedWordNewlyAdded(t *testing.T) {
	newlyAdded := []string{
		"CONSTRAINT", "COUNT", "CYPHER", "EXPLAIN", "FILTER",
		"FOR", "FROM", "NODE", "NONE", "PROFILE", "REDUCE", "SHOW", "UNIQUE",
	}
	for _, kw := range newlyAdded {
		t.Run("entity type/"+kw, func(t *testing.T) {
			s := &flowv1.Schema{
				EntityTypes: []*flowv1.EntityType{{Name: kw}},
			}
			if err := Validate(s); err == nil {
				t.Fatalf("expected schemaerrors.ErrReservedWord for %q, got nil", kw)
			} else if !errors.Is(err, schemaerrors.ErrReservedWord) {
				t.Fatalf("expected schemaerrors.ErrReservedWord for %q, got: %v", kw, err)
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
				t.Fatalf("expected schemaerrors.ErrReservedWord for property %q, got nil", kw)
			} else if !errors.Is(err, schemaerrors.ErrReservedWord) {
				t.Fatalf("expected schemaerrors.ErrReservedWord for property %q, got: %v", kw, err)
			}
		})
		t.Run("edge type/"+kw, func(t *testing.T) {
			s := &flowv1.Schema{
				EdgeTypes: []*flowv1.EdgeType{{Name: kw}},
			}
			if err := Validate(s); err == nil {
				t.Fatalf("expected schemaerrors.ErrReservedWord for edge type %q, got nil", kw)
			} else if !errors.Is(err, schemaerrors.ErrReservedWord) {
				t.Fatalf("expected schemaerrors.ErrReservedWord for edge type %q, got: %v", kw, err)
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
				t.Fatalf("expected schemaerrors.ErrReservedWord for edge property %q, got nil", kw)
			} else if !errors.Is(err, schemaerrors.ErrReservedWord) {
				t.Fatalf("expected schemaerrors.ErrReservedWord for edge property %q, got: %v", kw, err)
			}
		})
	}
}

// The store's internal placeholder NODE table for edgeless rel types is named
// `_untyped` (UntypedTableName). It is a valid Cypher identifier (passes the
// regex), so it must be explicitly reserved: a user entity or edge type with
// that name would alias the placeholder table and be silently skipped by the
// store's reopen structural check (validateMetadataAgainstCatalog). Both
// entity and edge type names must be rejected with schemaerrors.ErrReservedWord (→
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
				t.Fatal("expected schemaerrors.ErrReservedWord, got nil")
			} else {
				if !errors.Is(err, schemaerrors.ErrReservedWord) {
					t.Fatalf("expected schemaerrors.ErrReservedWord, got: %v", err)
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