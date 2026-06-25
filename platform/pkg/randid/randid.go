package randid

import (
	"crypto/rand"
	"fmt"
)

// NewRandomID returns a random hex-encoded identifier.
func NewRandomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return fmt.Sprintf("%x", b)
}
