package schemaerrors

import "errors"

var (
	ErrDuplicateTypeName       = errors.New("duplicate type name")
	ErrDuplicatePropertyName   = errors.New("duplicate property name")
	ErrInvalidName             = errors.New("invalid name format or length")
	ErrReservedWord            = errors.New("name is a reserved word")
	ErrImplicitColumnCollision = errors.New("property name collides with implicit column")
	ErrInvalidPropertyType     = errors.New("property type must be 'string'")
	ErrEmptyRuleList           = errors.New("rule entry has empty canConnectTo or using list")
	ErrUndeclaredTypeRef       = errors.New("rule references undeclared type")
	ErrNilElement              = errors.New("schema contains a nil element")
	ErrNilSchema               = errors.New("schema is nil")
)
