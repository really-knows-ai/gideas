package queuemgr_test

import (
	"context"
	"errors"
	"testing"

	"github.com/foundry/flow/sdk/go/internal/queuemgr"
)

// PHASE_04 pins the temporary in-process decision channel semantics of
// WaitForDecision/Decide: Decide stores the choice on the same *Manager
// instance (keyed by workitemID); WaitForDecision reads it back. No EventBus,
// no subscriptions — that arrives in PHASE_06. All cases are deterministic and
// run synchronously: Decide writes before WaitForDecision reads, and the
// cancelled-context case uses a pre-cancelled context so WaitForDecision
// returns immediately with ctx.Err().

func TestManager_WaitForDecision_UnknownID_ErrQueueItemNotFound(t *testing.T) {
	m, _ := newBufconnManager(t)
	_, err := m.WaitForDecision(context.Background(), "never-enqueued")
	if !errors.Is(err, queuemgr.ErrQueueItemNotFound) {
		t.Fatalf("WaitForDecision err = %v, want ErrQueueItemNotFound", err)
	}
}

// Enqueue registers the local decision entry; Decide stores the choice; a
// subsequent WaitForDecision reads it back without blocking.
func TestManager_WaitForDecision_ReturnsChoiceAfterDecide(t *testing.T) {
	m, _ := newBufconnManager(t, queuemgr.WithQueueName("hitl"))
	ctx := context.Background()
	if err := m.Enqueue(ctx, "wi-wd"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if _, err := m.Claim(ctx, "wi-wd"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := m.Decide(ctx, "wi-wd", "approve"); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	choice, err := m.WaitForDecision(ctx, "wi-wd")
	if err != nil {
		t.Fatalf("WaitForDecision: %v", err)
	}
	if choice != "approve" {
		t.Fatalf("choice = %q, want approve", choice)
	}
}

// A pre-cancelled context on a pending (enqueued, undecided) item must make
// WaitForDecision return the context error rather than block.
func TestManager_WaitForDecision_ContextCancelled(t *testing.T) {
	m, _ := newBufconnManager(t, queuemgr.WithQueueName("hitl"))
	ctx := context.Background()
	if err := m.Enqueue(ctx, "wi-pending"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	cancelCtx, cancel := context.WithCancel(ctx)
	cancel() // pre-cancelled before any decision is stored

	_, err := m.WaitForDecision(cancelCtx, "wi-pending")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitForDecision err = %v, want context.Canceled", err)
	}
}
