package uuidutil

import (
	"fmt"

	"github.com/google/uuid"
)

var ErrInvalidUUID = fmt.Errorf("invalid UUID v4")

func Validate(s string) error {
	u, err := uuid.Parse(s)
	if err != nil || u.Version() != 4 {
		return ErrInvalidUUID
	}
	return nil
}
