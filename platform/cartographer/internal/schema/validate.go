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
// Validation is performed in the order specified in PHASE_02.md §E:
//  1. Duplicate type names
//  2. Duplicate property names
//  3. Name format and length
//  4. Reserved words
//  5. Implicit-column collision (entities)
//  6. Implicit-column collision (edges)
//  7. Property type must be "string"
//  8. Rule validation (undeclared references, empty lists)
func Validate(schema *flowv1.Schema) error {
	// 1. Duplicate type names
	entityTypeNames := make(map[string]bool, len(schema.EntityTypes))
	for _, et := range schema.EntityTypes {
		if entityTypeNames[et.Name] {
			return fmt.Errorf("%w: %q", ErrDuplicateTypeName, et.Name)
		}
		entityTypeNames[et.Name] = true
	}

	edgeTypeNames := make(map[string]bool, len(schema.EdgeTypes))
	for _, et := range schema.EdgeTypes {
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
	for _, p := range et.Properties {
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
	for _, p := range et.Properties {
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
	if len(name) == 0 || len(name) > maxNameLength {
		return fmt.Errorf("%w: %q (length %d)", ErrInvalidName, name, len(name))
	}
	if !cypherIdentifierRegex.MatchString(name) {
		return fmt.Errorf("%w: %q does not match [a-zA-Z_][a-zA-Z0-9_]*", ErrInvalidName, name)
	}
	return nil
}
