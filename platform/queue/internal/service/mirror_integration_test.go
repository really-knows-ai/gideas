package service

// PHASE_03 mirror-everywhere write funnel + read aggregator: REAL-composition
// integration tests driven end-to-end through the GatewayServer across the real
// in-process gRPC (bufconn) boundary. A Registry (real fake-client CR registry)
// + 3 fakeMirrorShard sidecar store doubles (the sidecar-only mirror shards —
// never the SDK mesh) are composed over bufconn via the Registry's peerDialer.
//
// These cross a real I/O boundary (gRPC messages over the wire to peer shards),
// prove real composition that fakes cannot, and are isolated per test (fresh
// harness, fresh bufconn shards, t.Cleanup teardown). They only run under the
// dedicated integration command (no -short); under `go test -short` they SKIP.

import (
	"context"
	"sync"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flowv1api "github.com/foundry/flow/operator/api/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// killShard evicts a shard from the Registry's Queue CR (marks its phase
// evicted). This is the real lease-eviction mechanism: removing a shard from
// the living set makes the funnel fan out to the remaining shards only — so a
// kill is observable as the surviving set still serving, not as a read failure.
func killShard(t *testing.T, h *funnelHarness, shardID string) {
	t.Helper()
	q := &flowv1api.Queue{}
	key := client.ObjectKey{Namespace: testNamespace, Name: testQueueName}
	if err := h.reg.client.Get(context.Background(), key, q); err != nil {
		t.Fatalf("get queue CR: %v", err)
	}
	changed := false
	for i := range q.Status.Shards {
		if q.Status.Shards[i].ShardID == shardID {
			q.Status.Shards[i].Phase = phaseEvicted
			changed = true
		}
	}
	if !changed {
		t.Fatalf("shard %s not found in CR", shardID)
	}
	if err := h.reg.client.Status().Update(context.Background(), q); err != nil {
		t.Fatalf("evict shard %s: %v", shardID, err)
	}
}

// TestIntegration_Mirror3Shard_KillSurvival pins AC4's mirror-only sub-cases
// (kill-one / kill-two) across the real bufconn gRPC boundary: a 3-shard mirror
// where every write is mirrored everywhere (R-3.1) survives losing shards and
// still serves the full queue from the remaining mirrors.
func TestIntegration_Mirror3Shard_KillSurvival(t *testing.T) {
	if testing.Short() {
		t.Skip("real-I/O queue mesh integration")
	}
	ctx := context.Background()
	h := newFunnelHarness(t, "shard-a", "shard-b", "shard-c")

	const n = 5
	ids := make([]string, n)
	for i := range n {
		ids[i] = "wi-" + string(rune('1'+i))
	}

	// R-3.1: enqueue N items through the gateway funnel — every shard holds
	// every item (mirror-everywhere).
	for _, id := range ids {
		resp, err := h.gateway.Enqueue(ctx, &flowv1.EnqueueRequest{
			QueueName: testQueueName, WorkitemId: id, Choices: []string{"approve", "reject"},
		})
		if err != nil {
			t.Fatalf("Enqueue %s: %v", id, err)
		}
		if !resp.GetAcknowledged() {
			t.Fatalf("Enqueue %s not acknowledged", id)
		}
	}
	for _, addr := range h.addrs {
		serve := h.shards[addr].serve()
		for _, id := range ids {
			it := serve[id]
			if it == nil {
				t.Fatalf("shard %s does not hold %s after enqueue", h.shards[addr].shardID, id)
			}
			if it.GetStatus() != testStatusWaiting {
				t.Fatalf("shard %s item %s status = %q, want waiting", h.shards[addr].shardID, id, it.GetStatus())
			}
		}
	}

	// assertFullQueue reads the deduped global queue and requires every enqueued
	// item present exactly once (the scatter-gather collapsed mirror replicas).
	assertFullQueue := func(step string) {
		t.Helper()
		resp, err := h.gateway.GetGlobalQueue(ctx, &flowv1.GetGlobalQueueRequest{QueueName: testQueueName})
		if err != nil {
			t.Fatalf("%s: GetGlobalQueue: %v", step, err)
		}
		got := make(map[string]bool, len(resp.GetItems()))
		for _, it := range resp.GetItems() {
			got[it.GetWorkitemId()] = true
		}
		if len(got) != n {
			t.Fatalf("%s: deduped global queue has %d items, want %d", step, len(got), n)
		}
		for _, id := range ids {
			if !got[id] {
				t.Fatalf("%s: global queue missing %s", step, id)
			}
		}
	}

	// AC4 kill-one: evict one shard — reads still return the full queue from
	// the two survivors.
	killShard(t, h, "shard-a")
	assertFullQueue("kill-one (2 survivors)")

	// AC4 kill-two: evict a second — the single survivor still answers.
	killShard(t, h, "shard-b")
	assertFullQueue("kill-two (1 survivor)")
}

// TestIntegration_ConcurrentClaims pins AC4's claim race across the real
// bufconn boundary: two concurrent Claims on the same workitem collide on the
// per-item in-flight guard, so exactly one wins (claimed) and the other fails
// with codes.AlreadyExists; the final item state is identical (all claimed) on
// every shard. A decide then removes it from every shard.
func TestIntegration_ConcurrentClaims(t *testing.T) {
	if testing.Short() {
		t.Skip("real-I/O queue mesh integration")
	}
	ctx := context.Background()
	h := newFunnelHarness(t, "shard-a", "shard-b", "shard-c")

	// Enqueue one waiting item across the 3-shard mirror.
	if _, err := h.gateway.Enqueue(ctx, &flowv1.EnqueueRequest{
		QueueName: testQueueName, WorkitemId: testWorkitemID, Choices: []string{testChoiceApprove},
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	type claimResult struct {
		item *flowv1.QueueItem
		err  error
	}
	results := make([]claimResult, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := h.gateway.Claim(ctx, &flowv1.ClaimRequest{
				QueueName: testQueueName, WorkitemId: testWorkitemID,
			})
			results[i] = claimResult{item: resp.GetItem(), err: err}
		}(i)
	}
	wg.Wait()

	// Exactly one claim wins; the other is rejected with AlreadyExists.
	var wins, alreadyExists int
	for _, r := range results {
		switch {
		case r.err == nil:
			wins++
			if r.item.GetStatus() != testStatusClaimed {
				t.Fatalf("winning claim status = %q, want claimed", r.item.GetStatus())
			}
		case status.Code(r.err) == codes.AlreadyExists:
			alreadyExists++
		default:
			t.Fatalf("unexpected claim error code: %v", status.Code(r.err))
		}
	}
	if wins != 1 || alreadyExists != 1 {
		t.Fatalf("wins=%d alreadyExists=%d, want exactly 1 win and 1 AlreadyExists", wins, alreadyExists)
	}

	// The serialized in-flight guard leaves every shard in the identical
	// claimed state.
	for _, addr := range h.addrs {
		got := h.shards[addr].serve()[testWorkitemID]
		if got == nil || got.GetStatus() != testStatusClaimed {
			t.Fatalf("shard %s final state = %+v, want claimed", h.shards[addr].shardID, got)
		}
	}

	// Decide removes the item from every shard.
	if _, err := h.gateway.Decide(ctx, &flowv1.DecideRequest{
		QueueName: testQueueName, WorkitemId: testWorkitemID, Choice: testChoiceApprove,
	}); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	for _, addr := range h.addrs {
		if got := h.shards[addr].serve()[testWorkitemID]; got != nil {
			t.Fatalf("shard %s still holds %s after decide: %+v", h.shards[addr].shardID, testWorkitemID, got)
		}
	}
}
