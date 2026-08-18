package queue

import (
	"context"
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// Tests — HITL QueueManager: lifecycle & basic operations
// ---------------------------------------------------------------------------

func TestQueueManager_Lifecycle(t *testing.T) {
	t.Setenv("FLOW_STORAGE_PATH", ":memory:")
	t.Setenv("FLOW_HITL_PORT", "0")

	qm, err := NewManager()
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	if err := qm.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if err := qm.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}

func TestQueueManager_EnqueueAndList(t *testing.T) {
	qm := newTestManager(t)
	ctx := context.Background()

	if err := qm.Enqueue(ctx, "wi-1"); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	items, _, err := qm.store.getLocal(ctx, QueueFilter{})
	if err != nil {
		t.Fatalf("store.getLocal failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].WorkitemID != "wi-1" {
		t.Fatalf("expected wi-1, got %s", items[0].WorkitemID)
	}
}

func TestQueueManager_FullCycle(t *testing.T) {
	qm := newTestManager(t)
	ctx := context.Background()

	// Enqueue.
	if err := qm.Enqueue(ctx, "wi-cycle"); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Claim.
	item, err := qm.Claim(ctx, "wi-cycle")
	if err != nil {
		t.Fatalf("Claim failed: %v", err)
	}
	if item.Status != QueueStatusClaimed {
		t.Fatalf("expected claimed, got %s", item.Status)
	}

	// Decide.
	if err := qm.Decide(ctx, "wi-cycle", ""); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	// Verify item is gone.
	_, err = qm.GetItem(ctx, "wi-cycle")
	if !errors.Is(err, ErrQueueItemNotFound) {
		t.Fatalf("expected ErrQueueItemNotFound after decide, got %v", err)
	}
}
