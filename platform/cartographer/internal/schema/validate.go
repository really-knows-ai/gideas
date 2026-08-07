package schema

import (
	"fmt"
	"regexp"
	"strings"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// cypherIdentifierRegex matches valid unquoted Cypher identifiers.
var cypherIdentifierRegex = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

const maxNameLength = 255

// Validate validates the given schema and returns the first validation error found.
// Validation runs in this order:
//  1. Duplicate type names (entity types, then edge types)
//  2. Per type, in list order: the type name's format and reserved-word status
//  3. Per property within a type, in declaration order: duplicate property
//     names, property name format, reserved-word status, implicit-column
//     collision (entity: id/embedding when vector index enabled; edge:
//     id/from/to/type), and property type must be "string"
//  4. Rule validation (undeclared references, empty lists), after each entity
//     type's properties pass
func Validate(schema *flowv1.Schema) error {
	// An omitted schema (nil proto3 message field — e.g. an
	// ApplySchemaRequest without a schema, forwarded unguarded by the handler)
	// is equivalent to an empty schema: SPEC:86 permits an empty or omitted
	// entityTypes/edgeTypes array, so a fully omitted schema is valid too.
	// Guard before any field access so Validate never panics on a nil pointer.
	if schema == nil {
		return nil
	}

	// 1. Duplicate type names
	entityTypeNames := make(map[string]bool, len(schema.EntityTypes))
	for i, et := range schema.EntityTypes {
		if et == nil {
			return fmt.Errorf("%w: entity type %d is nil", ErrNilElement, i)
		}
		if entityTypeNames[et.Name] {
			return fmt.Errorf("%w: %q", ErrDuplicateTypeName, et.Name)
		}
		entityTypeNames[et.Name] = true
	}

	edgeTypeNames := make(map[string]bool, len(schema.EdgeTypes))
	for i, et := range schema.EdgeTypes {
		if et == nil {
			return fmt.Errorf("%w: edge type %d is nil", ErrNilElement, i)
		}
		if edgeTypeNames[et.Name] {
			return fmt.Errorf("%w: %q", ErrDuplicateTypeName, et.Name)
		}
		edgeTypeNames[et.Name] = true
	}

	// 2-7. Validate each entity type
	for _, et := range schema.EntityTypes {
		if err := validateEntityType(et, entityTypeNames, edgeTypeNames); err != nil {
			return err
		}
	}

	// 2-7. Validate each edge type
	for _, et := range schema.EdgeTypes {
		if err := validateEdgeType(et); err != nil {
			return err
		}
	}

	return nil
}

func validateEntityType(et *flowv1.EntityType, entityTypeNames, edgeTypeNames map[string]bool) error {
	// 3. Name format and length
	if err := validateName(et.Name); err != nil {
		return err
	}

	// 4. Reserved word check
	if reservedWords[strings.ToUpper(et.Name)] {
		return fmt.Errorf("%w: %q is a reserved word", ErrReservedWord, et.Name)
	}

	// 2. Duplicate property names within this type
	propNames := make(map[string]bool, len(et.Properties))
	for i, p := range et.Properties {
		if p == nil {
			return fmt.Errorf("%w: property %d in entity type %q is nil", ErrNilElement, i, et.Name)
		}
		if propNames[p.Name] {
			return fmt.Errorf("%w: duplicate property %q in entity type %q", ErrDuplicatePropertyName, p.Name, et.Name)
		}
		propNames[p.Name] = true

		// 3. Property name format
		if err := validateName(p.Name); err != nil {
			return fmt.Errorf("%w: property %q in entity type %q", err, p.Name, et.Name)
		}

		// 4. Reserved word check for property name
		if reservedWords[strings.ToUpper(p.Name)] {
			return fmt.Errorf("%w: property %q in entity type %q is a reserved word", ErrReservedWord, p.Name, et.Name)
		}

		// 5. Implicit-column collision (entities)
		if p.Name == "id" {
			return fmt.Errorf("%w: property %q in entity type %q collides with reserved column",
				ErrImplicitColumnCollision, p.Name, et.Name)
		}
		if et.EnableVectorIndex && p.Name == "embedding" {
			return fmt.Errorf("%w: property %q in entity type %q collides with embedding column when vector index is enabled",
				ErrImplicitColumnCollision, p.Name, et.Name)
		}

		// 7. Property type must be "string"
		if p.Type != "string" {
			return fmt.Errorf("%w: property %q in entity type %q has type %q", ErrInvalidPropertyType, p.Name, et.Name, p.Type)
		}
	}

	// 8. Rule validation
	return validateRules(et.Rules, entityTypeNames, edgeTypeNames, et.Name)
}

func validateEdgeType(et *flowv1.EdgeType) error {
	// 3. Name format
	if err := validateName(et.Name); err != nil {
		return err
	}

	// 4. Reserved word check
	if reservedWords[strings.ToUpper(et.Name)] {
		return fmt.Errorf("%w: %q is a reserved word", ErrReservedWord, et.Name)
	}

	// 2. Duplicate property names within this type
	propNames := make(map[string]bool, len(et.Properties))
	for i, p := range et.Properties {
		if p == nil {
			return fmt.Errorf("%w: property %d in edge type %q is nil", ErrNilElement, i, et.Name)
		}
		if propNames[p.Name] {
			return fmt.Errorf("%w: duplicate property %q in edge type %q", ErrDuplicatePropertyName, p.Name, et.Name)
		}
		propNames[p.Name] = true

		// 3. Property name format
		if err := validateName(p.Name); err != nil {
			return fmt.Errorf("%w: property %q in edge type %q", err, p.Name, et.Name)
		}

		// 4. Reserved word check for property name
		if reservedWords[strings.ToUpper(p.Name)] {
			return fmt.Errorf("%w: property %q in edge type %q is a reserved word", ErrReservedWord, p.Name, et.Name)
		}

		// 6. Implicit-column collision (edges)
		if p.Name == "id" || p.Name == "from" || p.Name == "to" || p.Name == "type" {
			return fmt.Errorf("%w: property %q in edge type %q collides with reserved column",
				ErrImplicitColumnCollision, p.Name, et.Name)
		}

		// 7. Property type must be "string"
		if p.Type != "string" {
			return fmt.Errorf("%w: property %q in edge type %q has type %q", ErrInvalidPropertyType, p.Name, et.Name, p.Type)
		}
	}

	return nil
}

func validateRules(rules []*flowv1.ConnectionRule, entityTypeNames,
	edgeTypeNames map[string]bool, entityTypeName string) error {
	for i, rule := range rules {
		if rule == nil {
			return fmt.Errorf("%w: rule %d in entity type %q is nil", ErrNilElement, i, entityTypeName)
		}
		if len(rule.CanConnectTo) == 0 {
			return fmt.Errorf("%w: rule %d in entity type %q has empty canConnectTo", ErrEmptyRuleList, i, entityTypeName)
		}
		if len(rule.Using) == 0 {
			return fmt.Errorf("%w: rule %d in entity type %q has empty using", ErrEmptyRuleList, i, entityTypeName)
		}
		for _, target := range rule.CanConnectTo {
			if !entityTypeNames[target] {
				return fmt.Errorf("%w: canConnectTo references undeclared entity type %q in rule %d of %q",
					ErrUndeclaredTypeRef, target, i, entityTypeName)
			}
		}
		for _, edgeRef := range rule.Using {
			if !edgeTypeNames[edgeRef] {
				return fmt.Errorf("%w: using references undeclared edge type %q in rule %d of %q",
					ErrUndeclaredTypeRef, edgeRef, i, entityTypeName)
			}
		}
	}
	return nil
}

func validateName(name string) error {
	// Length check uses len() (bytes), not utf8.RuneCountInString. Because the
	// Cypher identifier regex is ASCII-only, any name passing it is single-byte
	// (bytes == runes), so the 255-char limit is equivalent. A multi-byte name
	// fails the regex below regardless, so it is never rejected over-length;
	// only its diagnostic reports bytes. This is acceptable, so no rune counting.
	if len(name) == 0 || len(name) > maxNameLength {
		return fmt.Errorf("%w: %q (length %d)", ErrInvalidName, name, len(name))
	}
	if !cypherIdentifierRegex.MatchString(name) {
		return fmt.Errorf("%w: %q does not match [a-zA-Z_][a-zA-Z0-9_]*", ErrInvalidName, name)
	}
	return nil
}
