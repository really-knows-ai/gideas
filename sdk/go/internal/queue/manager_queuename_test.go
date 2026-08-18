package queue

import (
	"context"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Tests — HITL QueueManager: queue name configuration
// ---------------------------------------------------------------------------

func TestQueueManager_WithQueueName_Stored(t *testing.T) {
	t.Setenv("FLOW_STORAGE_PATH", ":memory:")
	t.Setenv("FLOW_HITL_PORT", "0")

	qm, err := NewManager(
		WithQueueName("my-queue"),
	)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	if err := qm.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { _ = qm.Stop() })

	ctx := context.Background()
	if err := qm.Enqueue(ctx, "wi-qn-1"); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	items, _, err := qm.store.getLocal(ctx, QueueFilter{})
	if err != nil {
		t.Fatalf("store.getLocal failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].QueueName != "my-queue" {
		t.Fatalf("expected QueueName=my-queue, got %q", items[0].QueueName)
	}
}

func TestQueueManager_QueueName_DefaultsToFLOW_NODE_ID(t *testing.T) {
	t.Setenv("FLOW_NODE_ID", "test-node-id")
	t.Setenv("FLOW_STORAGE_PATH", ":memory:")
	t.Setenv("FLOW_HITL_PORT", "0")

	qm, err := NewManager()
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	if qm.queueName != "test-node-id" {
		t.Fatalf("expected queueName=test-node-id, got %q", qm.queueName)
	}

	if err := qm.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { _ = qm.Stop() })

	ctx := context.Background()
	if err := qm.Enqueue(ctx, "wi-default"); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	items, _, err := qm.store.getLocal(ctx, QueueFilter{})
	if err != nil {
		t.Fatalf("store.getLocal failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].QueueName != "test-node-id" {
		t.Fatalf("expected QueueName=test-node-id, got %q", items[0].QueueName)
	}
}

func TestQueueManager_QueueName_EnqueueDecideWaitCycle(t *testing.T) {
	t.Setenv("FLOW_STORAGE_PATH", ":memory:")
	t.Setenv("FLOW_HITL_PORT", "0")

	qm, err := NewManager(
		WithQueueName("test-queue"),
	)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	if err := qm.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { _ = qm.Stop() })

	ctx := context.Background()

	if err := qm.Enqueue(ctx, "wi-cycle-qn"); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}
	if _, err := qm.Claim(ctx, "wi-cycle-qn"); err != nil {
		t.Fatalf("Claim failed: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := qm.WaitForDecision(ctx, "wi-cycle-qn")
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)

	if err := qm.Decide(ctx, "wi-cycle-qn", ""); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	if err := <-done; err != nil {
		t.Fatalf("WaitForDecision returned error: %v", err)
	}

	items, _, err := qm.store.getLocal(ctx, QueueFilter{})
	if err != nil {
		t.Fatalf("store.getLocal failed: %v", err)
	}
	for _, item := range items {
		if item.QueueName != "test-queue" {
			t.Errorf("expected QueueName=test-queue on item %s, got %q", item.WorkitemID, item.QueueName)
		}
	}
}
