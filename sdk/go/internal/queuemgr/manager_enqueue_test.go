package queuemgr_test

import (
	"context"
	"testing"

	"github.com/foundry/flow/sdk/go/internal/queuemgr"
)

// Enqueue must send the configured queue name and the WithChoices payload on
// the EnqueueRequest (R-5.2). The fake records the choices it received; the
// stored item must also carry the queue name and a "waiting" status.
func TestManager_Enqueue_SendsQueueNameAndChoices(t *testing.T) {
	m, fake := newBufconnManager(t,
		queuemgr.WithQueueName("hitl-approval"),
		queuemgr.WithChoices([]string{"approve", "reject"}),
	)
	ctx := context.Background()

	if err := m.Enqueue(ctx, "wi-1"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	got := fake.enqueueChoices("wi-1")
	if len(got) != 2 || got[0] != "approve" || got[1] != "reject" {
		t.Fatalf("enqueue choices = %v, want [approve reject]", got)
	}

	item := fake.item("wi-1")
	if item == nil {
		t.Fatal("item not stored by fake")
	}
	if item.GetWorkitemId() != "wi-1" {
		t.Fatalf("WorkitemId = %q, want wi-1", item.GetWorkitemId())
	}
	if item.GetQueueName() != "hitl-approval" {
		t.Fatalf("QueueName = %q, want hitl-approval", item.GetQueueName())
	}
	if item.GetStatus() != "waiting" {
		t.Fatalf("Status = %q, want waiting", item.GetStatus())
	}
}

// Without WithChoices the client must send an empty choices payload (nil).
func TestManager_Enqueue_NoChoicesConfigured(t *testing.T) {
	m, fake := newBufconnManager(t, queuemgr.WithQueueName("hitl"))
	ctx := context.Background()

	if err := m.Enqueue(ctx, "wi-nc"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if got := fake.enqueueChoices("wi-nc"); len(got) != 0 {
		t.Fatalf("enqueue choices = %v, want empty when WithChoices unset", got)
	}
}

// The queue name option must be honored; the request must carry it.
func TestManager_Enqueue_QueueNameOption(t *testing.T) {
	m, fake := newBufconnManager(t, queuemgr.WithQueueName("custom-queue"))
	ctx := context.Background()

	if err := m.Enqueue(ctx, "wi-qn"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if item := fake.item("wi-qn"); item == nil || item.GetQueueName() != "custom-queue" {
		t.Fatalf("stored QueueName = %+v, want custom-queue", item)
	}
}
