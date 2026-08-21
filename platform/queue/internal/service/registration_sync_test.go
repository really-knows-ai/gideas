package service

// PHASE_05 sync-on-registration (R-4.1) unit tests.
//
// A new / re-registered shard must be replayed the queue's full authoritative
// item set (ApplyItem per item) WITHIN the registration round-trip, sourced
// from the freshest mirror (F2). The seam under test is the
// Registry.syncOnRegistration hook that RegisterQueue / HeartbeatQueue invoke
// when a shard is new or was evicted. All collaborators are injected fakes
// (fake controller client, fakeMirrorShards over bufconn) — zero real I/O, no
// real clock, deterministic.

import (
	"context"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flowv1api "github.com/foundry/flow/operator/api/v1"
	"google.golang.org/grpc"
)

// PHASE_05 test constant for the repeated new-shard literal (goconst).
const testShardNew = "shard-new"

// regSyncHarness starts with an EXISTING queue whose living shard "shard-old"
// is the recorded freshest mirror holding the authoritative item set, plus a
// Registry wired to route addrs to fakeMirrorShards. It lets tests register a
// brand-new shard and then inspect that shard's refilled store.
type regSyncHarness struct {
	reg      *Registry
	shards   map[string]*fakeMirrorShard // addr -> shard
	freshID  string
	freshImg map[string]string // authoritative (id -> gen) snapshot
}

func newRegSyncHarness(t *testing.T) *regSyncHarness {
	t.Helper()
	now := time.Now().UTC()
	h := &regSyncHarness{
		shards:  map[string]*fakeMirrorShard{},
		freshID: "shard-old",
	}
	// The freshest living mirror already holds the authoritative item set.
	old := newFakeMirrorShard(t, h.freshID)
	oldAddr := "addr-" + h.freshID
	h.shards[oldAddr] = old
	old.setItem(&flowv1.QueueItem{
		WorkitemId: "wi-1", QueueName: testQueueName, Status: "waiting",
		GenerationId: "0000000000000001",
	})
	old.setItem(&flowv1.QueueItem{
		WorkitemId: "wi-2", QueueName: testQueueName, Status: "waiting",
		GenerationId: "0000000000000002",
	})
	h.freshImg = map[string]string{"wi-1": "0000000000000001", "wi-2": "0000000000000002"}

	seed := queueCR(testQueueName, shard(h.freshID, oldAddr, phaseActive, now))
	c := newFakeClient(t, seed)
	r := NewRegistry(c, time.Minute, time.Second)
	r.Namespace = testNamespace
	r.freshestShardID = map[string]string{testQueueName: h.freshID}
	r.peerDialer = func(ctx context.Context, addr string) (*grpc.ClientConn, error) {
		f, ok := h.shards[addr]
		if !ok {
			return nil, errShardUnavailable
		}
		return f.dialer(ctx, addr)
	}
	h.reg = r
	return h
}

// registerNew invokes Registry.RegisterQueue for a freshly-arrived shard at a
// new address (a brand-new random shard id / addr not yet in the CR), exactly
// the onboarding path R-4.1 targets.
func (h *regSyncHarness) registerNew(t *testing.T, shardID, shardAddr string) {
	t.Helper()
	h.shards[shardAddr] = newFakeMirrorShard(t, shardID)
	if _, err := h.reg.RegisterQueue(context.Background(), &flowv1.RegisterQueueRequest{
		QueueName: testQueueName, ShardId: shardID, ShardAddr: shardAddr,
	}); err != nil {
		t.Fatalf("RegisterQueue: %v", err)
	}
}

// TestSyncOnRegistration_RefillsNewShard pins R-4.1: a shard that was not in
// the living set (fresh random id) registers and, within that single
// RegisterQueue round-trip, is replayed the full authoritative item set from
// the freshest mirror. The freshest mirror itself is untouched.
func TestSyncOnRegistration_RefillsNewShard(t *testing.T) {
	h := newRegSyncHarness(t)

	newShardID := testShardNew
	newAddr := "addr-" + newShardID
	h.registerNew(t, newShardID, newAddr)

	newShard := h.shards[newAddr]
	for id, gen := range h.freshImg {
		it := newShard.serve()[id]
		if it == nil {
			t.Fatalf("new shard %s was not replayed authoritative item %s (R-4.1)", newShardID, id)
		}
		if it.GetGenerationId() != gen {
			t.Fatalf("new shard %s item %s generation = %q, want %q", newShardID, id, it.GetGenerationId(), gen)
		}
	}
	// The new shard holds exactly the authoritative set (no extras).
	if err := assertSameItemSet(newShard.serve(), h.freshImg); err != nil {
		t.Fatalf("new shard %s: %v", newShardID, err)
	}
	// The push happened through the real ApplyItem wire path during the
	// registration round-trip (not a metadata-only registration).
	applied := newShard.appliedCalls()
	seen := map[string]bool{}
	for _, it := range applied {
		seen[it.GetWorkitemId()] = true
	}
	if !seen["wi-1"] || !seen["wi-2"] {
		t.Fatalf("new shard received ApplyItem for %v, want both wi-1 and wi-2", seen)
	}

	// The freshest mirror (the source of truth) is untouched.
	old := h.shards["addr-"+h.freshID]
	for id, gen := range h.freshImg {
		if it := old.serve()[id]; it == nil || it.GetGenerationId() != gen {
			t.Fatalf("freshest mirror %s lost %s during sync", h.freshID, id)
		}
	}
}

// TestSyncOnRegistration_ReplayedFromFreshestMirror pins the F2 half: the
// authoritative set is replayed from the recorded freshest mirror. A stale
// shard holds an EXTRA item that is not part of the authoritative set; the new
// shard must receive exactly the freshest mirror's set (the extra is NOT pushed
// to it, because sync-on-registration is a bounded push of the authoritative
// set, and the stale extra is not authoritative).
func TestSyncOnRegistration_ReplayedFromFreshestMirror(t *testing.T) {
	h := newRegSyncHarness(t)

	// The new shard must be replayed only the freshest mirror's authoritative
	// set — no stale orphans. Register and confirm the exact set.
	newShardID := testShardNew
	newAddr := "addr-" + newShardID
	h.registerNew(t, newShardID, newAddr)

	newShard := h.shards[newAddr]
	if err := assertSameItemSet(newShard.serve(), h.freshImg); err != nil {
		t.Fatalf("new shard %s set not equal to freshest mirror set: %v", newShardID, err)
	}
}

// TestHeartbeatSyncOnRegistration_RefillsNewShard pins R-4.1 for the
// HeartbeatQueue path: a self-healed / newly-heartbeating shard that was absent
// from the living set is refilled within the heartbeat round-trip.
func TestHeartbeatSyncOnRegistration_RefillsNewShard(t *testing.T) {
	h := newRegSyncHarness(t)

	newShardID := testShardNew
	newAddr := "addr-" + newShardID
	h.shards[newAddr] = newFakeMirrorShard(t, newShardID)
	if _, err := h.reg.HeartbeatQueue(context.Background(), &flowv1.HeartbeatQueueRequest{
		QueueName: testQueueName, ShardId: newShardID, ShardAddr: newAddr,
	}); err != nil {
		t.Fatalf("HeartbeatQueue: %v", err)
	}

	newShard := h.shards[newAddr]
	if err := assertSameItemSet(newShard.serve(), h.freshImg); err != nil {
		t.Fatalf("new shard %s set after heartbeat sync: %v", newShardID, err)
	}
}

// TestSyncOnRegistration_BoundedPush pins the "one bounded push, not a scan"
// intent: the sync replays ApplyItem once per authoritative item (exactly the
// authoritative set size), not a repeated/scanning fan-out. The new shard
// receives exactly one ApplyItem per item.
func TestSyncOnRegistration_BoundedPush(t *testing.T) {
	h := newRegSyncHarness(t)

	newShardID := testShardNew
	newAddr := "addr-" + newShardID
	h.registerNew(t, newShardID, newAddr)

	newShard := h.shards[newAddr]
	applied := newShard.appliedCalls()
	if len(applied) != 2 {
		t.Fatalf("new shard received %d ApplyItem calls, want exactly 2 (one per authoritative item)", len(applied))
	}
}

// TestSyncOnRegistration_FirstRegistrationIsNoop pins the degenerate R-4.1
// case: the very first registration of a brand-new queue creates the CR with
// only that one shard — there is no other mirror to replay from, so the fresh
// shard simply starts empty (nothing is pushed, no error).
func TestSyncOnRegistration_FirstRegistrationIsNoop(t *testing.T) {
	ctx := context.Background()
	// Empty fake client: no queue exists yet.
	c := newFakeClient(t)
	r := NewRegistry(c, time.Minute, time.Second)
	r.Namespace = testNamespace
	r.freshestShardID = map[string]string{}

	solo := newFakeMirrorShard(t, "shard-solo")
	r.peerDialer = func(ctx context.Context, addr string) (*grpc.ClientConn, error) {
		if addr != "addr-shard-solo" {
			return nil, errShardUnavailable
		}
		return solo.dialer(ctx, addr)
	}

	if _, err := r.RegisterQueue(ctx, &flowv1.RegisterQueueRequest{
		QueueName: testQueueName, ShardId: "shard-solo", ShardAddr: "addr-shard-solo",
	}); err != nil {
		t.Fatalf("RegisterQueue (first): %v", err)
	}
	// Solo shard holds nothing — a fresh, empty store.
	if len(solo.serve()) != 0 {
		t.Fatalf("solo shard store = %v, want empty after first registration", solo.serve())
	}
}

// TestSyncOnRegistration_ReRegisterAfterEviction pins R-4.1's "re-registration
// after eviction" sub-case: a shard whose CR entry was previously evicted
// (removed from the living set) is treated as needing a fresh sync on
// re-registration.
func TestSyncOnRegistration_ReRegisterAfterEviction(t *testing.T) {
	h := newRegSyncHarness(t)
	ctx := context.Background()

	// Register the new shard once (it gets the set), then evict it from the CR,
	// then empty its store to simulate a restart, then re-register — R-4.1 says
	// it must be replayed the authoritative set again on re-registration.
	newShardID := "shard-rejoin"
	newAddr := "addr-" + newShardID
	h.shards[newAddr] = newFakeMirrorShard(t, newShardID)
	if _, err := h.reg.RegisterQueue(ctx, &flowv1.RegisterQueueRequest{
		QueueName: testQueueName, ShardId: newShardID, ShardAddr: newAddr,
	}); err != nil {
		t.Fatalf("RegisterQueue (join): %v", err)
	}

	// Evict it from the living set via the CR (as the lease sweep would).
	q := &flowv1api.Queue{}
	key := h.reg.key(testQueueName)
	if err := h.reg.client.Get(ctx, key, q); err != nil {
		t.Fatalf("get CR: %v", err)
	}
	var remaining []flowv1api.QueueShardStatus
	for _, s := range q.Status.Shards {
		if s.ShardID != newShardID {
			remaining = append(remaining, s)
		}
	}
	q.Status.Shards = remaining
	if err := h.reg.client.Status().Update(ctx, q); err != nil {
		t.Fatalf("evict shard: %v", err)
	}

	// Wipe the shard's store (a crash-restart makes it empty again).
	rejoined := h.shards[newAddr]
	rejoined.mu.Lock()
	rejoined.store = map[string]*flowv1.QueueItem{}
	rejoined.mu.Unlock()

	// Re-register: sync-on-registration must refill it from the freshest mirror.
	if _, err := h.reg.RegisterQueue(ctx, &flowv1.RegisterQueueRequest{
		QueueName: testQueueName, ShardId: newShardID, ShardAddr: newAddr,
	}); err != nil {
		t.Fatalf("RegisterQueue (re-join): %v", err)
	}
	if err := assertSameItemSet(rejoined.serve(), h.freshImg); err != nil {
		t.Fatalf("re-registered (evicted) shard %s: %v", newShardID, err)
	}
}
