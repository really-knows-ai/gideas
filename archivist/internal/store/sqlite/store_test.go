package sqlite

import (
	"context"
	"sync"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestStoreBlob_NewBlob(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	isNew, err := s.StoreBlob(ctx, "abc123", []byte("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isNew {
		t.Fatal("expected StoreBlob to return true for new blob")
	}

	data, ok, err := s.GetBlob(ctx, "abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected GetBlob to find stored blob")
	}
	if string(data) != "hello" {
		t.Fatalf("expected 'hello', got %q", string(data))
	}
}

func TestStoreBlob_Deduplication(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.StoreBlob(ctx, "abc123", []byte("hello"))

	isNew, err := s.StoreBlob(ctx, "abc123", []byte("different"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isNew {
		t.Fatal("expected StoreBlob to return false for duplicate hash")
	}

	// Original content should be preserved.
	data, _, _ := s.GetBlob(ctx, "abc123")
	if string(data) != "hello" {
		t.Fatalf("expected original 'hello', got %q", string(data))
	}
}

func TestGetBlob_NotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, ok, err := s.GetBlob(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected GetBlob to return false for missing hash")
	}
}

func TestAppendVersion_And_GetHead(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Must store blobs first (foreign key constraint).
	s.StoreBlob(ctx, "hash-v1", []byte("v1"))
	s.StoreBlob(ctx, "hash-v2", []byte("v2"))

	if err := s.AppendVersion(ctx, "wi-1", "art-1", "hash-v1", "txt"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	head, err := s.GetHead(ctx, "wi-1", "art-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if head == nil || head.Hash != "hash-v1" {
		t.Fatal("expected head to be hash-v1")
	}

	if err := s.AppendVersion(ctx, "wi-1", "art-1", "hash-v2", "txt"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	head, err = s.GetHead(ctx, "wi-1", "art-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if head == nil || head.Hash != "hash-v2" {
		t.Fatal("expected head to be hash-v2 after append")
	}
}

func TestGetHistory(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.StoreBlob(ctx, "hash-v1", []byte("v1"))
	s.StoreBlob(ctx, "hash-v2", []byte("v2"))

	s.AppendVersion(ctx, "wi-1", "art-1", "hash-v1", "txt")
	s.AppendVersion(ctx, "wi-1", "art-1", "hash-v2", "txt")

	history, err := s.GetHistory(ctx, "wi-1", "art-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(history))
	}
	if history[0].Hash != "hash-v1" || history[1].Hash != "hash-v2" {
		t.Fatal("version order mismatch")
	}
}

func TestGetHead_NotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	head, err := s.GetHead(ctx, "missing", "missing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if head != nil {
		t.Fatal("expected nil head for missing artefact")
	}
}

func TestGetHistory_NotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	history, err := s.GetHistory(ctx, "missing", "missing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if history != nil {
		t.Fatal("expected nil history for missing artefact")
	}
}

func TestListArtefacts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.StoreBlob(ctx, "h1", []byte("a"))
	s.StoreBlob(ctx, "h2", []byte("b"))

	s.AppendVersion(ctx, "wi-1", "art-1", "h1", "txt")
	s.AppendVersion(ctx, "wi-1", "art-2", "h2", "json")

	entries, err := s.ListArtefacts(ctx, "wi-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	found := map[string]string{}
	for _, e := range entries {
		found[e.ID] = e.Kind
	}
	if found["art-1"] != "txt" || found["art-2"] != "json" {
		t.Fatalf("unexpected entries: %v", found)
	}
}

func TestListArtefacts_HeadKind(t *testing.T) {
	// When an artefact's kind changes across versions, ListArtefacts should
	// return the kind from the head (most recent) version.
	s := newTestStore(t)
	ctx := context.Background()

	s.StoreBlob(ctx, "h1", []byte("a"))
	s.StoreBlob(ctx, "h2", []byte("b"))

	s.AppendVersion(ctx, "wi-1", "art-1", "h1", "txt")
	s.AppendVersion(ctx, "wi-1", "art-1", "h2", "json")

	entries, err := s.ListArtefacts(ctx, "wi-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Kind != "json" {
		t.Fatalf("expected head kind 'json', got %q", entries[0].Kind)
	}
}

func TestListArtefacts_Empty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	entries, err := s.ListArtefacts(ctx, "missing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty list, got %d entries", len(entries))
	}
}

func TestConcurrentAccess(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Pre-populate blobs for the concurrent writes.
	for i := range 26 {
		hash := "hash-" + string(rune('a'+i))
		s.StoreBlob(ctx, hash, []byte("data"))
	}

	var wg sync.WaitGroup

	// Concurrent writes.
	for i := range 100 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			hash := "hash-" + string(rune('a'+i%26))
			s.StoreBlob(ctx, hash, []byte("data"))
			s.AppendVersion(ctx, "wi", "art", hash, "txt")
		}(i)
	}

	// Concurrent reads.
	for range 100 {
		wg.Go(func() {
			s.GetBlob(ctx, "hash-a")
			s.GetHead(ctx, "wi", "art")
			s.ListArtefacts(ctx, "wi")
		})
	}

	wg.Wait()
}

func TestForeignKeyEnforcement(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Attempting to append a version referencing a non-existent blob should
	// fail due to foreign key constraint.
	err := s.AppendVersion(ctx, "wi-1", "art-1", "nonexistent-hash", "txt")
	if err == nil {
		t.Fatal("expected foreign key error when referencing non-existent blob")
	}
}

func TestVersionTimestamps(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	s.StoreBlob(ctx, "h1", []byte("a"))
	s.AppendVersion(ctx, "wi-1", "art-1", "h1", "txt")

	head, err := s.GetHead(ctx, "wi-1", "art-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if head.CreatedAt.IsZero() {
		t.Fatal("expected non-zero timestamp on version")
	}
}
