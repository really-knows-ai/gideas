package uuidutil

import (
	"fmt"

	"github.com/google/uuid"
)

var ErrInvalidUUID = fmt.Errorf("invalid UUID v4")

// Validate reports whether s is a valid RFC4122 UUID v4. google/uuid.Parse
// accepts the canonical 8-4-4-4-12 lowercase dashed form as well as valid
// spellings in uppercase hex, 32-char no-hyphen, braced {...}, and urn:uuid:
// forms; all decode to the same UUID, so the version/variant checks below
// treat them identically and every parseable v4 RFC4122 UUID passes. SPEC
// (SPEC:161; error-table rows "Invalid entity or edge ID format", "Invalid
// transaction ID format") requires only "a valid UUID v4", so no spelling pin
// is applied. Callers that store the ID can normalize any accepted spelling
// to the canonical form via the parsed uuid's String().
func Validate(s string) error {
	u, err := uuid.Parse(s)
	if err != nil || u.Version() != 4 || u.Variant() != uuid.RFC4122 {
		return ErrInvalidUUID
	}
	return nil
}
