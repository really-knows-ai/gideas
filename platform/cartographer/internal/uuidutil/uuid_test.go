package uuidutil

import (
	"testing"

	"github.com/google/uuid"
)

func TestValidate(t *testing.T) {
	// Canonical dashed v4 form produced by the generator must pass.
	if err := Validate(uuid.New().String()); err != nil {
		t.Fatalf("generator output rejected: %v", err)
	}
	good := "550e8400-e29b-41d4-a716-446655440000"
	if err := Validate(good); err != nil {
		t.Fatalf("canonical dashed v4 rejected: %v", err)
	}

	// Every spelling google/uuid.Parse accepts of a valid RFC4122 v4 UUID
	// is rejected — uppercase hex, no-hyphen, braced, and urn:uuid: all
	// decode to the same UUID as `good`, but none is the canonical RFC4122
	// §3 string representation. The store persists IDs verbatim as
	// <id>.json files, so accepting a second spelling of an existing UUID
	// would let two entities exist for one UUID and bypass the
	// CreateEntity ALREADY_EXISTS check (SPEC:942).
	invalid := []struct {
		name string
		in   string
	}{
		{"non-canonical uppercase hex", "550E8400-E29B-41D4-A716-446655440000"},
		{"non-canonical no-hyphen 32-char", "550e8400e29b41d4a716446655440000"},
		{"non-canonical braced", "{550e8400-e29b-41d4-a716-446655440000}"},
		{"non-canonical urn prefix", "urn:uuid:550e8400-e29b-41d4-a716-446655440000"},
		{"wrong version", "550e8400-e29b-31d4-a716-446655440000"},
		// Version nibble left at 4, canonical dashed form, but variant nibble
		// (first hex of 4th group) set to 1100 (Microsoft variant) instead of
		// RFC4122 10xx — isolates the u.Variant() != uuid.RFC4122 branch.
		{"wrong variant (non-RFC4122)", "550e8400-e29b-41d4-c716-446655440000"},
		{"non-uuid", "not-a-uuid"},
		{"empty", ""},
	}
	for _, tc := range invalid {
		if err := Validate(tc.in); err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
		}
	}
}
