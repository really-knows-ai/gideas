package store

import (
	"sync"
	"testing"
)

func TestStoreBlob_NewBlob(t *testing.T) {
	s := NewMemoryStore()
	isNew := s.StoreBlob("abc123", []byte("hello"))
	if !isNew {
		t.Fatal("expected StoreBlob to return true for new blob")
	}
	data, ok := s.GetBlob("abc123")
	if !ok {
		t.Fatal("expected GetBlob to find stored blob")
	}
	if string(data) != "hello" {
		t.Fatalf("expected 'hello', got %q", string(data))
	}
}

func TestStoreBlob_Deduplication(t *testing.T) {
	s := NewMemoryStore()
	s.StoreBlob("abc123", []byte("hello"))
	isNew := s.StoreBlob("abc123", []byte("different"))
	if isNew {
		t.Fatal("expected StoreBlob to return false for duplicate hash")
	}
	// Original content should be preserved.
	data, _ := s.GetBlob("abc123")
	if string(data) != "hello" {
		t.Fatalf("expected original 'hello', got %q", string(data))
	}
}

func TestStoreBlob_ContentIsolation(t *testing.T) {
	s := NewMemoryStore()
	original := []byte("hello")
	s.StoreBlob("abc123", original)
	// Mutate caller's slice.
	original[0] = 'X'
	data, _ := s.GetBlob("abc123")
	if string(data) != "hello" {
		t.Fatal("stored blob was aliased to caller's slice")
	}
}

func TestGetBlob_NotFound(t *testing.T) {
	s := NewMemoryStore()
	_, ok := s.GetBlob("nonexistent")
	if ok {
		t.Fatal("expected GetBlob to return false for missing hash")
	}
}

func TestAppendVersion_And_GetHead(t *testing.T) {
	s := NewMemoryStore()

	s.AppendVersion("wi-1", "art-1", "hash-v1", "txt")
	head := s.GetHead("wi-1", "art-1")
	if head == nil || head.Hash != "hash-v1" {
		t.Fatal("expected head to be hash-v1")
	}

	s.AppendVersion("wi-1", "art-1", "hash-v2", "txt")
	head = s.GetHead("wi-1", "art-1")
	if head == nil || head.Hash != "hash-v2" {
		t.Fatal("expected head to be hash-v2 after append")
	}
}

func TestGetHistory(t *testing.T) {
	s := NewMemoryStore()

	s.AppendVersion("wi-1", "art-1", "hash-v1", "txt")
	s.AppendVersion("wi-1", "art-1", "hash-v2", "txt")

	history := s.GetHistory("wi-1", "art-1")
	if len(history) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(history))
	}
	if history[0].Hash != "hash-v1" || history[1].Hash != "hash-v2" {
		t.Fatal("version order mismatch")
	}
}

func TestGetHead_NotFound(t *testing.T) {
	s := NewMemoryStore()
	head := s.GetHead("missing", "missing")
	if head != nil {
		t.Fatal("expected nil head for missing artefact")
	}
}

func TestListArtefacts(t *testing.T) {
	s := NewMemoryStore()

	s.AppendVersion("wi-1", "art-1", "h1", "txt")
	s.AppendVersion("wi-1", "art-2", "h2", "json")

	entries := s.ListArtefacts("wi-1")
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

func TestListArtefacts_Empty(t *testing.T) {
	s := NewMemoryStore()
	entries := s.ListArtefacts("missing")
	if entries != nil {
		t.Fatal("expected nil for missing workitem")
	}
}

func TestConcurrentAccess(t *testing.T) {
	s := NewMemoryStore()
	var wg sync.WaitGroup

	// Concurrent writes.
	for i := range 100 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			hash := "hash-" + string(rune('a'+i%26))
			s.StoreBlob(hash, []byte("data"))
			s.AppendVersion("wi", "art", hash, "txt")
		}(i)
	}

	// Concurrent reads.
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.GetBlob("hash-a")
			s.GetHead("wi", "art")
			s.ListArtefacts("wi")
		}()
	}

	wg.Wait()
}
