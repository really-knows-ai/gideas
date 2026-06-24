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
	t.Cleanup(func() { _ = s.Close() })
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

	if _, err := s.StoreBlob(ctx, "abc123", []byte("hello")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

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
	if _, err := s.StoreBlob(ctx, "hash-v1", []byte("v1")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := s.StoreBlob(ctx, "hash-v2", []byte("v2")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

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

	if _, err := s.StoreBlob(ctx, "hash-v1", []byte("v1")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := s.StoreBlob(ctx, "hash-v2", []byte("v2")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := s.AppendVersion(ctx, "wi-1", "art-1", "hash-v1", "txt"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.AppendVersion(ctx, "wi-1", "art-1", "hash-v2", "txt"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

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

	if _, err := s.StoreBlob(ctx, "h1", []byte("a")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := s.StoreBlob(ctx, "h2", []byte("b")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := s.AppendVersion(ctx, "wi-1", "art-1", "h1", "txt"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.AppendVersion(ctx, "wi-1", "art-2", "h2", "json"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	entries, err := s.ListArtefacts(ctx, "wi-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	found := map[string]string{}
	for _, e := range entries {
		found[e.ID] = e.GovernedArtefact
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

	if _, err := s.StoreBlob(ctx, "h1", []byte("a")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := s.StoreBlob(ctx, "h2", []byte("b")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := s.AppendVersion(ctx, "wi-1", "art-1", "h1", "txt"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.AppendVersion(ctx, "wi-1", "art-1", "h2", "json"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	entries, err := s.ListArtefacts(ctx, "wi-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].GovernedArtefact != "json" {
		t.Fatalf("expected head governed artefact 'json', got %q", entries[0].GovernedArtefact)
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
		if _, err := s.StoreBlob(ctx, hash, []byte("data")); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	var wg sync.WaitGroup

	// Concurrent writes.
	for i := range 100 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			hash := "hash-" + string(rune('a'+i%26))
			_, _ = s.StoreBlob(ctx, hash, []byte("data"))
			_ = s.AppendVersion(ctx, "wi", "art", hash, "txt")
		}(i)
	}

	// Concurrent reads.
	for range 100 {
		wg.Go(func() {
			_, _, _ = s.GetBlob(ctx, "hash-a")
			_, _ = s.GetHead(ctx, "wi", "art")
			_, _ = s.ListArtefacts(ctx, "wi")
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

	if _, err := s.StoreBlob(ctx, "h1", []byte("a")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.AppendVersion(ctx, "wi-1", "art-1", "h1", "txt"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	head, err := s.GetHead(ctx, "wi-1", "art-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if head.CreatedAt.IsZero() {
		t.Fatal("expected non-zero timestamp on version")
	}
}

// ---------------------------------------------------------------------------
// Feedback: can_wont_fix field tests
// ---------------------------------------------------------------------------

func TestAddFeedback_CanWontFix(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Add feedback with canWontFix=true.
	id, err := s.AddFeedback(ctx, "wi-1", "art-1", "quench", true, "test message", "hash-v1")
	if err != nil {
		t.Fatalf("AddFeedback: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty feedback ID")
	}

	// Retrieve and verify.
	f, err := s.GetFeedbackByID(ctx, id)
	if err != nil {
		t.Fatalf("GetFeedbackByID: %v", err)
	}
	if f == nil {
		t.Fatal("expected feedback record")
	}
	if !f.CanWontFix {
		t.Fatal("expected CanWontFix=true")
	}
}

func TestAddFeedback_CanWontFixDefault(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Add feedback with canWontFix=false (the default).
	id, err := s.AddFeedback(ctx, "wi-1", "art-1", "quench", false, "test message", "hash-v1")
	if err != nil {
		t.Fatalf("AddFeedback: %v", err)
	}

	f, err := s.GetFeedbackByID(ctx, id)
	if err != nil {
		t.Fatalf("GetFeedbackByID: %v", err)
	}
	if f == nil {
		t.Fatal("expected feedback record")
	}
	if f.CanWontFix {
		t.Fatal("expected CanWontFix=false")
	}
}

// ---------------------------------------------------------------------------
// ResolveStaleFeedback tests
// ---------------------------------------------------------------------------

func TestResolveStaleFeedback_ResolvesOlderVersion(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Create feedback on version A (canWontFix=false).
	idA, err := s.AddFeedback(ctx, "wi-1", "art-1", "quench", false, "old version msg", "hash-v1")
	if err != nil {
		t.Fatalf("AddFeedback v1: %v", err)
	}

	// Create feedback on version B (current head — should NOT be resolved).
	_, err = s.AddFeedback(ctx, "wi-1", "art-1", "quench", false, "current version msg", "hash-v2")
	if err != nil {
		t.Fatalf("AddFeedback v2: %v", err)
	}

	// Resolve stale feedback for new head hash-v2.
	n, err := s.ResolveStaleFeedback(ctx, "wi-1", "art-1", "hash-v2")
	if err != nil {
		t.Fatalf("ResolveStaleFeedback: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 resolved item, got %d", n)
	}

	// Verify old feedback is now RESOLVED.
	fA, err := s.GetFeedbackByID(ctx, idA)
	if err != nil {
		t.Fatalf("GetFeedbackByID: %v", err)
	}
	if fA == nil {
		t.Fatal("expected feedback record")
	}
	if fA.State != 6 {
		t.Fatalf("expected state=RESOLVED(6), got %d", fA.State)
	}

	// Verify event was appended.
	events, err := s.GetFeedbackEvents(ctx, idA)
	if err != nil {
		t.Fatalf("GetFeedbackEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events (created + resolved), got %d", len(events))
	}
	if events[1].Action != "resolved" {
		t.Fatalf("expected action 'resolved', got %q", events[1].Action)
	}
	if events[1].Actor != "archivist" {
		t.Fatalf("expected actor 'archivist', got %q", events[1].Actor)
	}
}

func TestResolveStaleFeedback_SkipsCanWontFixTrue(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Add feedback with canWontFix=true on an older version.
	id, err := s.AddFeedback(ctx, "wi-1", "art-1", "appraise", true, "subjective review", "hash-v1")
	if err != nil {
		t.Fatalf("AddFeedback: %v", err)
	}

	// Resolve stale feedback for new head hash-v2.
	n, err := s.ResolveStaleFeedback(ctx, "wi-1", "art-1", "hash-v2")
	if err != nil {
		t.Fatalf("ResolveStaleFeedback: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 resolved items (canWontFix=true skipped), got %d", n)
	}

	// Verify feedback is still NEW.
	f, err := s.GetFeedbackByID(ctx, id)
	if err != nil {
		t.Fatalf("GetFeedbackByID: %v", err)
	}
	if f == nil {
		t.Fatal("expected feedback record")
	}
	if f.State != 1 {
		t.Fatalf("expected state=NEW(1), got %d", f.State)
	}
}

func TestResolveStaleFeedback_SkipsTerminalStates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Add feedback with canWontFix=false on old version.
	id, err := s.AddFeedback(ctx, "wi-1", "art-1", "quench", false, "old msg", "hash-v1")
	if err != nil {
		t.Fatalf("AddFeedback: %v", err)
	}
	id2, err := s.AddFeedback(ctx, "wi-1", "art-1", "quench", false, "old msg 2", "hash-v1")
	if err != nil {
		t.Fatalf("AddFeedback: %v", err)
	}

	// Manually set one to RESOLVED (state=6) and one to DEADLOCKED (state=5).
	_, err = s.db.ExecContext(ctx,
		`UPDATE feedback_items SET state = 6 WHERE id = ?`, id)
	if err != nil {
		t.Fatalf("update state to RESOLVED: %v", err)
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE feedback_items SET state = 5 WHERE id = ?`, id2)
	if err != nil {
		t.Fatalf("update state to DEADLOCKED: %v", err)
	}

	// ResolveStaleFeedback should skip both (terminal states).
	n, err := s.ResolveStaleFeedback(ctx, "wi-1", "art-1", "hash-v2")
	if err != nil {
		t.Fatalf("ResolveStaleFeedback: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 resolved (already RESOLVED+DEADLOCKED), got %d", n)
	}
}

func TestResolveStaleFeedback_SkipsLinkedRuling(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Add feedback with canWontFix=false and a linked ruling.
	id, err := s.AddFeedback(ctx, "wi-1", "art-1", "quench", false, "old msg", "hash-v1")
	if err != nil {
		t.Fatalf("AddFeedback: %v", err)
	}

	// Set linked_ruling to simulate judiciary outcome.
	_, err = s.db.ExecContext(ctx,
		`UPDATE feedback_items SET linked_ruling = 'law-001' WHERE id = ?`, id)
	if err != nil {
		t.Fatalf("update linked_ruling: %v", err)
	}

	// ResolveStaleFeedback should skip it (has linked ruling).
	n, err := s.ResolveStaleFeedback(ctx, "wi-1", "art-1", "hash-v2")
	if err != nil {
		t.Fatalf("ResolveStaleFeedback: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 resolved (linked ruling), got %d", n)
	}
}
