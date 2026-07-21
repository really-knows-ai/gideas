package schema

import "errors"

var (
	// ErrDuplicateTypeName is returned when a schema has duplicate entity or edge type names.
	ErrDuplicateTypeName = errors.New("duplicate type name")

	// ErrDuplicatePropertyName is returned when a type's properties contain duplicate names.
	ErrDuplicatePropertyName = errors.New("duplicate property name")

	// ErrInvalidName is returned when a type or property name fails format or length checks.
	// Names must match [a-zA-Z_][a-zA-Z0-9_]* and be at most 255 characters.
	ErrInvalidName = errors.New("invalid name format or length")

	// ErrReservedWord is returned when a type or property name is a Cypher reserved word.
	ErrReservedWord = errors.New("name is a reserved word")

	// ErrImplicitColumnCollision is returned when a property name collides with an implicit column.
	ErrImplicitColumnCollision = errors.New("property name collides with implicit column")

	// ErrInvalidPropertyType is returned when a property type is not "string".
	ErrInvalidPropertyType = errors.New("property type must be 'string'")

	// ErrEmptyRuleList is returned when a rule entry has an empty canConnectTo or using list.
	ErrEmptyRuleList = errors.New("rule entry has empty canConnectTo or using list")

	// ErrUndeclaredTypeRef is returned when a rule references an undeclared type.
	ErrUndeclaredTypeRef = errors.New("rule references undeclared type")
)
