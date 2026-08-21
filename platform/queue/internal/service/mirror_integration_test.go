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
//
// PHASE_05 adds the convergence-backstop sub-cases — integration test 1's
// restart-sync + straggler, and integration test 5's partial-broadcast — in the
// same real-composition style: a real Registry + real GatewayServer funnel +
// real peerProxy, state diverged through real broadcast failures (a down dial,
// a transient ApplyItem error), and convergence asserted against the recorded
// freshest mirror (F2). No stubs replace a component that must be real here.

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flowv1api "github.com/foundry/flow/operator/api/v1"
	"google.golang.org/grpc"
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

// ---------------------------------------------------------------------------
// PHASE_05 convergence backstop — integration test 1 (PHASE_05 sub-cases) + 5
// ---------------------------------------------------------------------------

// PHASE_05 integration literals (goconst).
const (
	testWIA = "wi-a"
	testWIB = "wi-b"
	testWI2 = "wi-2"
	testWI3 = "wi-3"
)

// enqueueAck pushes one item through the real gateway funnel and requires the
// quorum ack: GatewayServer.Enqueue -> enqueueBroadcast fans ApplyItem out to
// every living shard over the real bufconn wire.
func enqueueAck(t *testing.T, h *funnelHarness, workitemID string) {
	t.Helper()
	if _, err := h.gateway.Enqueue(context.Background(), &flowv1.EnqueueRequest{
		QueueName: testQueueName, WorkitemId: workitemID, Choices: []string{testChoiceApprove},
	}); err != nil {
		t.Fatalf("Enqueue %s: %v", workitemID, err)
	}
}

// assertIdenticalShardSets returns an error unless shards a and b hold the
// identical item set (workitem_id -> generation_id) — the R-4.3 auditable
// convergence property, checked across two real shard servers.
func assertIdenticalShardSets(a, b *fakeMirrorShard) error {
	as, bs := a.serve(), b.serve()
	if len(as) != len(bs) {
		return fmt.Errorf("shard %s holds %d items, shard %s holds %d: sets differ",
			a.shardID, len(as), b.shardID, len(bs))
	}
	for id, ia := range as {
		ib := bs[id]
		if ib == nil {
			return fmt.Errorf("shard %s missing %s held by shard %s", b.shardID, id, a.shardID)
		}
		if ia.GetGenerationId() != ib.GetGenerationId() {
			return fmt.Errorf("shard %s %s generation %q != shard %s generation %q",
				a.shardID, id, ia.GetGenerationId(), b.shardID, ib.GetGenerationId())
		}
	}
	return nil
}

// appliedIDs records the workitem ids this shard received ApplyItem for — the
// real bufconn wire — so tests asserting "the push really happened" do not
// mistake store state for a wire replay.
func appliedIDs(sh *fakeMirrorShard) map[string]bool {
	out := make(map[string]bool)
	for _, it := range sh.appliedCalls() {
		out[it.GetWorkitemId()] = true
	}
	return out
}

// waitForShardItem polls until the shard's store holds the workitem — the
// convergence the backstop cadence must produce — bounded by a wait window.
// This is a poll for an asynchronous cadence tick, never a wall-clock
// assertion: the injected Run cadence is sub-second, so a working backstop
// converges on the FIRST tick and the poll returns after a few milliseconds.
func waitForShardItem(t *testing.T, sh *fakeMirrorShard, workitemID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if sh.serve()[workitemID] != nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("shard %s did not converge to hold %s within the wait window", sh.shardID, workitemID)
}

// TestIntegration_RestartSyncRefillsEmptyShard pins R-4.1 end-to-end: a shard
// restarts with a NEW random identity (R-2.3) and an empty store. The moment
// it calls RegisterQueue, the queue-service replays the authoritative item set
// from the recorded freshest mirror WITHIN that registration round-trip — no
// sweep cadence involved. A subsequent GetLocalQueue on the refilled shard
// returns the authoritative item set, identical to the freshest mirror's.
func TestIntegration_RestartSyncRefillsEmptyShard(t *testing.T) {
	if testing.Short() {
		t.Skip("real-I/O queue mesh integration")
	}
	ctx := context.Background()
	h := newFunnelHarness(t, testShardA, "shard-b")

	// Real write path: two enqueues land on both shards and the funnel records
	// the per-queue freshest mirror on ack (F2).
	enqueueAck(t, h, testWIA)
	enqueueAck(t, h, testWIB)
	freshID := h.reg.FreshestShardID(testQueueName)
	if freshID == "" {
		t.Fatal("funnel did not record a per-queue freshest mirror after ack")
	}

	// The restarted shard: brand-new random identity, empty store, joining the
	// queue through the real RegisterQueue round-trip.
	rebornID := "shard-reborn"
	rebornAddr := "addr-shard-reborn"
	h.shards[rebornAddr] = newFakeMirrorShard(t, rebornID)
	if _, err := h.reg.RegisterQueue(ctx, &flowv1.RegisterQueueRequest{
		QueueName: testQueueName, ShardId: rebornID, ShardAddr: rebornAddr,
	}); err != nil {
		t.Fatalf("RegisterQueue (reborn shard): %v", err)
	}

	// Without ANY sweep, the registration round-trip itself refilled the new
	// shard: a GetLocalQueue on it returns the full authoritative item set.
	reborn := h.shards[rebornAddr]
	resp, err := reborn.GetLocalQueue(ctx, &flowv1.GetLocalQueueRequest{})
	if err != nil {
		t.Fatalf("reborn shard GetLocalQueue: %v", err)
	}
	got := make(map[string]bool, len(resp.GetItems()))
	for _, it := range resp.GetItems() {
		got[it.GetWorkitemId()] = true
	}
	for _, id := range []string{testWIA, testWIB} {
		if !got[id] {
			t.Fatalf("reborn shard %s missing %s after sync-on-registration (R-4.1)", rebornID, id)
		}
	}

	// The refilled set is exactly the recorded freshest mirror's set (the
	// auditable convergence property), and the replay arrived over the real
	// ApplyItem wire path (recorded per ApplyItem call).
	var freshAddr string
	for _, addr := range h.addrs {
		if h.shards[addr].shardID == freshID {
			freshAddr = addr
		}
	}
	if err := assertIdenticalShardSets(reborn, h.shards[freshAddr]); err != nil {
		t.Fatalf("reborn shard not converged with freshest mirror %s: %v", freshID, err)
	}
	applied := appliedIDs(reborn)
	if !applied[testWIA] || !applied[testWIB] {
		t.Fatalf("reborn shard received ApplyItem for %v, want %s and %s", applied, testWIA, testWIB)
	}
}

// TestIntegration_StragglerConvergesWithinOneCadence pins R-4.2 end-to-end: a
// straggler is hard-down during a write (the registry dial to it fails —
// exactly what a dead peer produces on the real wire), so it simply does not
// confirm and the enqueue acks via quorum from the survivors. Once the
// straggler is back, the backstop Run loop (sub-second injected cadence via
// the NewSweeper interval seam) converges it within ONE cadence: the missing
// item is pushed via the real ApplyItem wire path and every shard holds the
// identical item set.
func TestIntegration_StragglerConvergesWithinOneCadence(t *testing.T) {
	if testing.Short() {
		t.Skip("real-I/O queue mesh integration")
	}
	ctx := context.Background()
	h := newFunnelHarness(t, testShardA, "shard-b", "shard-c")
	stragglerAddr := h.addrs[2]
	straggler := h.shards[stragglerAddr]

	// The straggler is hard-down during the write: the registry dialer now
	// refuses its address, so the funnel's fan-out simply does not count it
	// and quorum (2 of 3) still acks.
	baseDialer := h.reg.peerDialer
	h.reg.peerDialer = func(ctx context.Context, addr string) (*grpc.ClientConn, error) {
		if addr == stragglerAddr {
			return nil, errShardUnavailable
		}
		return baseDialer(ctx, addr)
	}
	enqueueAck(t, h, testWorkitemID)
	h.reg.peerDialer = baseDialer // the straggler recovers

	// While it was down it missed the write; the survivors hold the item.
	if it := straggler.serve()[testWorkitemID]; it != nil {
		t.Fatalf("straggler %s holds %s while hard-down: %+v", straggler.shardID, testWorkitemID, it)
	}
	for _, addr := range h.addrs {
		if addr == stragglerAddr {
			continue
		}
		if h.shards[addr].serve()[testWorkitemID] == nil {
			t.Fatalf("surviving shard %s missing the quorum-acked %s", h.shards[addr].shardID, testWorkitemID)
		}
	}

	// The backstop cadence loop converges the straggler on its first tick (the
	// exact Run shape production wires in cmd/main.go).
	sw := NewSweeper(h.reg, 10*time.Millisecond)
	runCtx, cancelRun := context.WithCancel(ctx)
	t.Cleanup(cancelRun)
	sw.Run(runCtx)

	waitForShardItem(t, straggler, testWorkitemID)

	if !appliedIDs(straggler)[testWorkitemID] {
		t.Fatal("straggler did not receive the missing item via the real ApplyItem wire path")
	}
	for _, addr := range h.addrs {
		if err := assertIdenticalShardSets(h.shards[addr], h.shards[h.addrs[0]]); err != nil {
			t.Fatalf("post-sweep shard %s: %v", h.shards[addr].shardID, err)
		}
	}
}

// TestIntegration_PartialBroadcastConvergesOnNextSweep pins integration test 5
// and the "ack barrier is load-bearing" constraint end-to-end: a shard has a
// TRANSIENT failure during one enqueue — it accepts the earlier writes, then a
// single ApplyItem fails (gRPC Unavailable), then it recovers. The write acks
// via quorum (the failing shard just does not confirm). Reads through the
// scatter-gather GetGlobalQueue never observe divergence once that ack
// returned (the dedupe collapses the straggler's absence). The next sweep
// pushes the missed item and the straggler holds the identical set.
func TestIntegration_PartialBroadcastConvergesOnNextSweep(t *testing.T) {
	if testing.Short() {
		t.Skip("real-I/O queue mesh integration")
	}
	ctx := context.Background()
	h := newFunnelHarness(t, testShardA, "shard-b", "shard-c")
	straggler := h.shards[h.addrs[2]]

	// The straggler accepts the first two writes...
	enqueueAck(t, h, testWorkitemID)
	enqueueAck(t, h, testWI2)

	// ...then fails for ONE write (transient apply failure), then recovers.
	straggler.setApplyError(status.Error(codes.Unavailable, "transient shard failure"))
	enqueueAck(t, h, testWI3) // acks via quorum: the failing shard does not confirm
	straggler.setApplyError(nil)

	// Reads never show divergence once the quorum ack returned: the
	// scatter-gather GetGlobalQueue returns every item exactly once even
	// though the straggler does not yet hold the partially-broadcast item.
	resp, err := h.gateway.GetGlobalQueue(ctx, &flowv1.GetGlobalQueueRequest{QueueName: testQueueName})
	if err != nil {
		t.Fatalf("GetGlobalQueue: %v", err)
	}
	got := make(map[string]bool, len(resp.GetItems()))
	for _, it := range resp.GetItems() {
		got[it.GetWorkitemId()] = true
	}
	if len(got) != 3 {
		t.Fatalf("global queue has %d items, want 3 (reads must never diverge once acked)", len(got))
	}
	for _, id := range []string{testWorkitemID, testWI2, testWI3} {
		if !got[id] {
			t.Fatalf("global queue missing %s after quorum-acked enqueue", id)
		}
	}
	if straggler.serve()[testWI3] != nil {
		t.Fatal("precondition broken: straggler already holds the partially-broadcast item")
	}

	// One sweep pass (driven directly — the exact work one cadence tick
	// performs) pushes the missed item and converges the straggler.
	if err := NewSweeper(h.reg, time.Millisecond).Sweep(ctx, testQueueName); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if it := straggler.serve()[testWI3]; it == nil {
		t.Fatal("straggler still missing the partially-broadcast item after the next sweep (R-4.2)")
	}
	if !appliedIDs(straggler)[testWI3] {
		t.Fatal("straggler did not receive the missed item via the real ApplyItem wire path")
	}
	for _, addr := range h.addrs {
		if err := assertIdenticalShardSets(h.shards[addr], h.shards[h.addrs[0]]); err != nil {
			t.Fatalf("post-sweep shard %s: %v", h.shards[addr].shardID, err)
		}
	}
}
