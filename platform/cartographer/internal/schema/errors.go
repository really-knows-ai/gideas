package schema

import "github.com/foundry/flow/cartographer/internal/schemaerrors"

var (
	ErrDuplicateTypeName       = schemaerrors.ErrDuplicateTypeName
	ErrDuplicatePropertyName   = schemaerrors.ErrDuplicatePropertyName
	ErrInvalidName             = schemaerrors.ErrInvalidName
	ErrReservedWord            = schemaerrors.ErrReservedWord
	ErrImplicitColumnCollision = schemaerrors.ErrImplicitColumnCollision
	ErrInvalidPropertyType     = schemaerrors.ErrInvalidPropertyType
	ErrEmptyRuleList           = schemaerrors.ErrEmptyRuleList
	ErrUndeclaredTypeRef       = schemaerrors.ErrUndeclaredTypeRef
	ErrNilElement              = schemaerrors.ErrNilElement
)
