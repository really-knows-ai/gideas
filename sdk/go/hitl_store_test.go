package flow

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// ---------------------------------------------------------------------------
// Tests — HITL Queue Store
// ---------------------------------------------------------------------------

func newTestStore(t *testing.T) *queueStore {
	t.Helper()
	s, err := newQueueStore(":memory:", "test-shard-0")
	if err != nil {
		t.Fatalf("newQueueStore failed: %v", err)
	}
	t.Cleanup(func() { _ = s.close() })
	return s
}

func TestQueueStore_InitSchema(t *testing.T) {
	s := newTestStore(t)
	// Verify the table exists by querying it.
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM hitl_queue").Scan(&count)
	if err != nil {
		t.Fatalf("schema not initialised: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 rows, got %d", count)
	}
}

func TestQueueStore_Enqueue(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.enqueue(ctx, testWorkitemID); err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}

	items, total, err := s.getLocal(ctx, QueueFilter{})
	if err != nil {
		t.Fatalf("getLocal failed: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected total=1, got %d", total)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].WorkitemID != testWorkitemID {
		t.Fatalf("expected workitem_id=%s, got %s", testWorkitemID, items[0].WorkitemID)
	}
	if items[0].Status != QueueStatusWaiting {
		t.Fatalf("expected status=waiting, got %s", items[0].Status)
	}
	if items[0].ShardID != "test-shard-0" {
		t.Fatalf("expected shard_id=test-shard-0, got %s", items[0].ShardID)
	}
}

func TestQueueStore_Enqueue_Duplicate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.enqueue(ctx, testWorkitemID); err != nil {
		t.Fatalf("first enqueue failed: %v", err)
	}
	if err := s.enqueue(ctx, testWorkitemID); err == nil {
		t.Fatal("expected error on duplicate enqueue, got nil")
	}
}

func TestQueueStore_Claim_HappyPath(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.enqueue(ctx, testWorkitemID); err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}

	item, err := s.claim(ctx, testWorkitemID)
	if err != nil {
		t.Fatalf("claim failed: %v", err)
	}
	if item.Status != QueueStatusClaimed {
		t.Fatalf("expected status=claimed, got %s", item.Status)
	}
	if item.ClaimedAt == nil {
		t.Fatal("expected claimed_at to be set")
	}
}

func TestQueueStore_Claim_NotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.claim(ctx, "nonexistent")
	if !errors.Is(err, ErrQueueItemNotFound) {
		t.Fatalf("expected ErrQueueItemNotFound, got %v", err)
	}
}

func TestQueueStore_Claim_AlreadyClaimed(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.enqueue(ctx, testWorkitemID); err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}
	if _, err := s.claim(ctx, testWorkitemID); err != nil {
		t.Fatalf("first claim failed: %v", err)
	}

	_, err := s.claim(ctx, testWorkitemID)
	if !errors.Is(err, ErrQueueItemAlreadyClaimed) {
		t.Fatalf("expected ErrQueueItemAlreadyClaimed, got %v", err)
	}
}

func TestQueueStore_Release_HappyPath(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.enqueue(ctx, testWorkitemID); err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}
	if _, err := s.claim(ctx, testWorkitemID); err != nil {
		t.Fatalf("claim failed: %v", err)
	}

	item, err := s.release(ctx, testWorkitemID)
	if err != nil {
		t.Fatalf("release failed: %v", err)
	}
	if item.Status != QueueStatusWaiting {
		t.Fatalf("expected status=waiting, got %s", item.Status)
	}
	if item.ClaimedAt != nil {
		t.Fatal("expected claimed_at to be nil after release")
	}
}

func TestQueueStore_Release_NotClaimed(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.enqueue(ctx, testWorkitemID); err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}

	_, err := s.release(ctx, testWorkitemID)
	if !errors.Is(err, ErrQueueItemInvalidState) {
		t.Fatalf("expected ErrQueueItemInvalidState, got %v", err)
	}
}

func TestQueueStore_Complete_HappyPath(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.enqueue(ctx, testWorkitemID); err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}
	if _, err := s.claim(ctx, testWorkitemID); err != nil {
		t.Fatalf("claim failed: %v", err)
	}

	if err := s.complete(ctx, testWorkitemID); err != nil {
		t.Fatalf("complete failed: %v", err)
	}

	// Verify item is deleted.
	_, err := s.getByID(ctx, testWorkitemID)
	if !errors.Is(err, ErrQueueItemNotFound) {
		t.Fatalf("expected item to be deleted, got: %v", err)
	}
}

func TestQueueStore_Complete_NotClaimed(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.enqueue(ctx, testWorkitemID); err != nil {
		t.Fatalf("enqueue failed: %v", err)
	}

	err := s.complete(ctx, testWorkitemID)
	if !errors.Is(err, ErrQueueItemInvalidState) {
		t.Fatalf("expected ErrQueueItemInvalidState, got %v", err)
	}
}

func TestQueueStore_GetLocal_StatusFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Enqueue 3 items, claim 1.
	for _, id := range []string{testWorkitemID, "wi-2", "wi-3"} {
		if err := s.enqueue(ctx, id); err != nil {
			t.Fatalf("enqueue %s failed: %v", id, err)
		}
	}
	if _, err := s.claim(ctx, "wi-2"); err != nil {
		t.Fatalf("claim failed: %v", err)
	}

	// Filter waiting.
	waiting := QueueStatusWaiting
	items, total, err := s.getLocal(ctx, QueueFilter{Status: &waiting})
	if err != nil {
		t.Fatalf("getLocal waiting failed: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected 2 waiting, got %d", total)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	// Filter claimed.
	claimed := QueueStatusClaimed
	items, total, err = s.getLocal(ctx, QueueFilter{Status: &claimed})
	if err != nil {
		t.Fatalf("getLocal claimed failed: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected 1 claimed, got %d", total)
	}
	if items[0].WorkitemID != "wi-2" {
		t.Fatalf("expected wi-2, got %s", items[0].WorkitemID)
	}
}

func TestQueueStore_GetLocal_Pagination(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := range 5 {
		if err := s.enqueue(ctx, fmt.Sprintf("wi-%d", i)); err != nil {
			t.Fatalf("enqueue wi-%d failed: %v", i, err)
		}
	}

	// Page 1: limit=2, offset=0.
	items, total, err := s.getLocal(ctx, QueueFilter{Limit: 2, Offset: 0})
	if err != nil {
		t.Fatalf("getLocal page 1 failed: %v", err)
	}
	if total != 5 {
		t.Fatalf("expected total=5, got %d", total)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	// Page 2: limit=2, offset=2.
	items, _, err = s.getLocal(ctx, QueueFilter{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("getLocal page 2 failed: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	// Page 3: limit=2, offset=4.
	items, _, err = s.getLocal(ctx, QueueFilter{Limit: 2, Offset: 4})
	if err != nil {
		t.Fatalf("getLocal page 3 failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
}

func TestQueueStore_GetByID_NotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.getByID(ctx, "nonexistent")
	if !errors.Is(err, ErrQueueItemNotFound) {
		t.Fatalf("expected ErrQueueItemNotFound, got %v", err)
	}
}
