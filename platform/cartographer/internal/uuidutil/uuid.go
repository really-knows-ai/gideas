package uuidutil

import (
	"fmt"
	"regexp"

	"github.com/google/uuid"
)

var ErrInvalidUUID = fmt.Errorf("invalid UUID v4")

// canonicalV4 matches the canonical 8-4-4-4-12 lowercase dashed form produced
// by uuid.New().String() / uuid.NewString. google/uuid's Parse also accepts the
// 32-char no-hyphen, braced {...}, and urn:uuid: forms, but SPEC requires the
// dashed form the generator emits downstream.
var canonicalV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func Validate(s string) error {
	u, err := uuid.Parse(s)
	if err != nil || u.Version() != 4 || u.Variant() != uuid.RFC4122 || !canonicalV4.MatchString(s) {
		return ErrInvalidUUID
	}
	return nil
}
