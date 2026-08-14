package flow

import (
	"context"
	"sync"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// TestTransaction_ConcurrentLifecycle pins that a single Transaction handle is
// safe to share across goroutines: every surviving method reads the
// terminal/lifecycle fields (committed, rolledBack) through checkTerminal,
// which is guarded by a mutex. Run under the race detector (-race) this test
// fails on the unsynchronized-field data race the guard removes.
func TestTransaction_ConcurrentLifecycle(t *testing.T) {
	mock := &mockCartographerClient{
		createEntity: func(ctx context.Context, req *flowv1.CreateEntityRequest) (*flowv1.CreateEntityResponse, error) {
			return &flowv1.CreateEntityResponse{EntityId: "entity-1", EntityType: componentType}, nil
		},
	}
	tx := newMockTx(mock)

	// Concurrent writes through the same handle: every call passes
	// checkTerminal (mutex-guarded) before reaching the wire.
	runConcurrently(t, 8, func(i int) {
		if _, err := tx.CreateEntity(componentType, nil, nil, nil); err != nil {
			t.Errorf("concurrent CreateEntity failed: %v", err)
		}
	})
	if tx.checkTerminal() != nil {
		t.Error("expected the handle to remain non-terminal after concurrent writes")
	}
}

// runConcurrently starts n goroutines calling fn(i) on a start barrier and
// fails the test if they do not all finish within a generous timeout (a
// deadlocked lifecycle lock would wedge the test, not hang the suite).
func runConcurrently(t *testing.T, n int, fn func(i int)) {
	t.Helper()
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			fn(i)
		}(i)
	}
	close(start)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent transaction lifecycle calls deadlocked")
	}
}
