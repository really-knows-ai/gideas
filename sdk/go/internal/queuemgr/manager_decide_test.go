package queuemgr_test

import (
	"context"
	"errors"
	"testing"

	"github.com/foundry/flow/sdk/go/internal/queuemgr"
)

func TestManager_Decide_Success(t *testing.T) {
	m, fake := newBufconnManager(t, queuemgr.WithQueueName("hitl"))
	ctx := context.Background()
	if err := m.Enqueue(ctx, "wi-dec"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := m.Claim(ctx, "wi-dec"); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	if err := m.Decide(ctx, "wi-dec", choiceApprove); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if fake.item("wi-dec") != nil {
		t.Fatal("item should be deleted from the queue after Decide")
	}
	if got := fake.decidedChoice("wi-dec"); got != choiceApprove {
		t.Fatalf("decided choice = %q, want approve", got)
	}
}

func TestManager_Decide_NotFound(t *testing.T) {
	m, _ := newBufconnManager(t)
	err := m.Decide(context.Background(), "nope", "")
	if !errors.Is(err, queuemgr.ErrQueueItemNotFound) {
		t.Fatalf("Decide err = %v, want ErrQueueItemNotFound", err)
	}
}

func TestManager_Decide_InvalidState(t *testing.T) {
	m, _ := newBufconnManager(t)
	ctx := context.Background()
	if err := m.Enqueue(ctx, "wi-notclaimed"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	err := m.Decide(ctx, "wi-notclaimed", "")
	if !errors.Is(err, queuemgr.ErrQueueItemInvalidState) {
		t.Fatalf("Decide err = %v, want ErrQueueItemInvalidState", err)
	}
}
