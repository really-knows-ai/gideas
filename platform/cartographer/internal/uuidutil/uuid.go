package uuidutil

import (
	"fmt"

	"github.com/google/uuid"
)

// errInvalidUUID is an internal leaf error returned by Validate. It is
// intentionally unexported: no caller matches it, so its identity would be
// dead contract. Layered classification keys on the store's
// store.ErrInvalidIDFormat, which the store wraps at its ID-format gate.
var errInvalidUUID = fmt.Errorf("invalid UUID v4")

// Validate reports whether s is a canonical RFC4122 §3 UUID v4 string: the
// 8-4-4-4-12 lowercase dashed form, which is what google/uuid's String() and
// uuid.New() produce. SPEC requires entity, edge, and transaction IDs to be
// "a valid UUID v4" (SPEC:161; error-table rows "Invalid entity or edge ID
// format", "Invalid transaction ID format"). The store persists IDs verbatim
// as <id>.json files, so accepting a non-canonical spelling of an already
// valid UUID would let two spellings of one UUID become two distinct entities
// and bypass the CreateEntity ALREADY_EXISTS check (SPEC:942). Non-canonical
// spellings that google/uuid.Parse also accepts (uppercase hex, 32-char
// no-hyphen, braced {...}, urn:uuid:) are therefore rejected here.
func Validate(s string) error {
	u, err := uuid.Parse(s)
	if err != nil || u.Version() != 4 || u.Variant() != uuid.RFC4122 {
		return errInvalidUUID
	}
	// The parsed UUID's String() is the canonical lowercase dashed form, so
	// any spelling that differs from it is not the RFC4122 §3 string
	// representation and is rejected rather than stored verbatim.
	if s != u.String() {
		return errInvalidUUID
	}
	return nil
}
