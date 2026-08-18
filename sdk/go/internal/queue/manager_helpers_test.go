package queue

import (
	"testing"
)

// newTestManager creates an in-memory QueueManager wired for manager tests.
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	store, err := newQueueStore(":memory:", "mgr-shard-0", "")
	if err != nil {
		t.Fatalf("newQueueStore failed: %v", err)
	}
	mesh := newQueueMesh(store, "mgr-shard-0", &staticResolver{}, "50053")
	qm := &Manager{
		store:   store,
		mesh:    mesh,
		shardID: "mgr-shard-0",
	}
	t.Cleanup(func() { _ = store.close() })
	return qm
}
