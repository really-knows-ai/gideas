package service

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/foundry/flow/archivist/internal/store/sqlite"
)

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h[:])
}

func newTestServer(t *testing.T) *ArchivistServer {
	t.Helper()
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewArchivistServer(store)
}
