package queuemgr_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/foundry/flow/sdk/go/internal/queuemgr"
)

func ptrStatus(s queuemgr.QueueStatus) *queuemgr.QueueStatus { return &s }

func TestManager_GetGlobalQueue_ReturnsMappedItems(t *testing.T) {
	m, _ := newBufconnManager(t, queuemgr.WithQueueName("hitl"))
	ctx := context.Background()

	for _, id := range []string{"wi-a", "wi-b"} {
		if err := m.Enqueue(ctx, id); err != nil {
			t.Fatalf("Enqueue %s: %v", id, err)
		}
	}

	items, err := m.GetGlobalQueue(ctx, queuemgr.QueueFilter{})
	if err != nil {
		t.Fatalf("GetGlobalQueue: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("GetGlobalQueue len = %d, want 2", len(items))
	}

	// The proto RFC3339 enqueued_at must be parsed into time.Time.
	expectedEnqueued, err := time.Parse(time.RFC3339, fakeEnqueuedAt)
	if err != nil {
		t.Fatalf("parse expected enqueued_at: %v", err)
	}
	for _, it := range items {
		if it.Status != queuemgr.QueueStatusWaiting {
			t.Errorf("Status = %q, want waiting", it.Status)
		}
		if !it.EnqueuedAt.Equal(expectedEnqueued) {
			t.Errorf("EnqueuedAt = %v, want %v", it.EnqueuedAt, expectedEnqueued)
		}
		if it.ClaimedAt != nil {
			t.Errorf("ClaimedAt = %v, want nil for waiting item", *it.ClaimedAt)
		}
		if it.ShardID != "shard-0" {
			t.Errorf("ShardID = %q, want shard-0", it.ShardID)
		}
		if it.QueueName != "hitl" {
			t.Errorf("QueueName = %q, want hitl", it.QueueName)
		}
		if it.Generation != "gen-1" {
			t.Errorf("Generation = %q, want gen-1", it.Generation)
		}
	}
}

func TestManager_GetGlobalQueue_StatusFilter(t *testing.T) {
	m, _ := newBufconnManager(t, queuemgr.WithQueueName("hitl"))
	ctx := context.Background()

	for _, id := range []string{"wi-claimed", "wi-waiting"} {
		if err := m.Enqueue(ctx, id); err != nil {
			t.Fatalf("Enqueue %s: %v", id, err)
		}
	}
	if _, err := m.Claim(ctx, "wi-claimed"); err != nil {
		t.Fatalf("Claim wi-claimed: %v", err)
	}

	items, err := m.GetGlobalQueue(ctx, queuemgr.QueueFilter{Status: ptrStatus(queuemgr.QueueStatusClaimed)})
	if err != nil {
		t.Fatalf("GetGlobalQueue: %v", err)
	}
	if len(items) != 1 || items[0].WorkitemID != "wi-claimed" {
		t.Fatalf("claimed-filtered items = %+v, want only wi-claimed", items)
	}
	if items[0].Status != queuemgr.QueueStatusClaimed {
		t.Fatalf("Status = %q, want claimed", items[0].Status)
	}
}

func TestManager_GetItem_ReturnsMappedItem(t *testing.T) {
	m, _ := newBufconnManager(t,
		queuemgr.WithQueueName("hitl"),
		queuemgr.WithChoices([]string{"approve"}),
	)
	ctx := context.Background()
	if err := m.Enqueue(ctx, "wi-get"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	item, err := m.GetItem(ctx, "wi-get")
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.WorkitemID != "wi-get" {
		t.Fatalf("WorkitemID = %q, want wi-get", item.WorkitemID)
	}
	if item.ShardID != "shard-0" {
		t.Fatalf("ShardID = %q, want shard-0", item.ShardID)
	}
	if item.QueueName != "hitl" {
		t.Fatalf("QueueName = %q, want hitl", item.QueueName)
	}
	if item.Status != queuemgr.QueueStatusWaiting {
		t.Fatalf("Status = %q, want waiting", item.Status)
	}
	expectedEnqueued, _ := time.Parse(time.RFC3339, fakeEnqueuedAt)
	if !item.EnqueuedAt.Equal(expectedEnqueued) {
		t.Fatalf("EnqueuedAt = %v, want %v", item.EnqueuedAt, expectedEnqueued)
	}
	if item.ClaimedAt != nil {
		t.Fatalf("ClaimedAt = %v, want nil", *item.ClaimedAt)
	}
}

func TestManager_GetItem_NotFound(t *testing.T) {
	m, _ := newBufconnManager(t)
	ctx := context.Background()

	_, err := m.GetItem(ctx, "nope")
	if !errors.Is(err, queuemgr.ErrQueueItemNotFound) {
		t.Fatalf("GetItem err = %v, want ErrQueueItemNotFound", err)
	}
}

func TestManager_Claim_SuccessAndMapping(t *testing.T) {
	m, _ := newBufconnManager(t, queuemgr.WithQueueName("hitl"))
	ctx := context.Background()
	if err := m.Enqueue(ctx, "wi-claim"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	item, err := m.Claim(ctx, "wi-claim")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if item.Status != queuemgr.QueueStatusClaimed {
		t.Fatalf("Status = %q, want claimed", item.Status)
	}
	if item.ClaimedAt == nil {
		t.Fatal("ClaimedAt = nil, want set after claim")
	}
	expectedClaimed, _ := time.Parse(time.RFC3339, fakeClaimedAt)
	if !item.ClaimedAt.Equal(expectedClaimed) {
		t.Fatalf("ClaimedAt = %v, want %v", *item.ClaimedAt, expectedClaimed)
	}
}

func TestManager_Claim_AlreadyClaimed(t *testing.T) {
	m, _ := newBufconnManager(t)
	ctx := context.Background()
	if err := m.Enqueue(ctx, "wi-2"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := m.Claim(ctx, "wi-2"); err != nil {
		t.Fatalf("first Claim: %v", err)
	}

	_, err := m.Claim(ctx, "wi-2")
	if !errors.Is(err, queuemgr.ErrQueueItemAlreadyClaimed) {
		t.Fatalf("second Claim err = %v, want ErrQueueItemAlreadyClaimed", err)
	}
}

func TestManager_Claim_NotFound(t *testing.T) {
	m, _ := newBufconnManager(t)
	_, err := m.Claim(context.Background(), "nope")
	if !errors.Is(err, queuemgr.ErrQueueItemNotFound) {
		t.Fatalf("Claim err = %v, want ErrQueueItemNotFound", err)
	}
}

func TestManager_Release_Success(t *testing.T) {
	m, _ := newBufconnManager(t)
	ctx := context.Background()
	if err := m.Enqueue(ctx, "wi-rel"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := m.Claim(ctx, "wi-rel"); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	item, err := m.Release(ctx, "wi-rel")
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if item.Status != queuemgr.QueueStatusWaiting {
		t.Fatalf("Status = %q, want waiting after release", item.Status)
	}
}

func TestManager_Release_InvalidState(t *testing.T) {
	m, _ := newBufconnManager(t)
	ctx := context.Background()
	if err := m.Enqueue(ctx, "wi-unclaimed"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	_, err := m.Release(ctx, "wi-unclaimed")
	if !errors.Is(err, queuemgr.ErrQueueItemInvalidState) {
		t.Fatalf("Release err = %v, want ErrQueueItemInvalidState", err)
	}
}

func TestManager_Release_NotFound(t *testing.T) {
	m, _ := newBufconnManager(t)
	_, err := m.Release(context.Background(), "nope")
	if !errors.Is(err, queuemgr.ErrQueueItemNotFound) {
		t.Fatalf("Release err = %v, want ErrQueueItemNotFound", err)
	}
}
