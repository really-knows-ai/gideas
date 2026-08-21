package service

// PHASE_05 convergence backstop unit tests (R-4.1/R-4.2/R-4.3, F2, F3).
//
// These drive the Sweeper (sweep.go) and the per-queue freshest-shard record
// (the funnel's queueName -> freshestShardID map) through the SAME bufconn
// harness pattern the PHASE_03 funnel tests use: a Registry backed by a
// controller-runtime fake client (no real K8s) whose peerDialer routes each
// shard address to a distinct in-process fakeMirrorShard over bufconn (no real
// network). Collaborators are injected via the existing peerDialer seam; there
// is zero real I/O, no real clock, and every test is deterministic and
// millisecond-fast.
//
// The pure diff under test is planConvergence/planPushToReference: a target
// shard is reconciled against the recorded freshest reference (F2) and the
// OTHER non-reference shards' reports. The reference is repairable — it can
// receive symmetric pushes; it is never a reason to destroy copies. Nothing
// here touches a non-test file.

import (
	"context"
	"fmt"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flowv1api "github.com/foundry/flow/operator/api/v1"
	"google.golang.org/grpc"
)

// PHASE_05 test constant for the repeated freshest-shard literal (goconst).
const testShardA = "shard-a"

// TestDefaultSweepInterval pins the single named cadence constant the plan
// requires (R-4.2: "a single named constant, e.g. DefaultSweepInterval"). Round
// 2 must export this constant in sweep.go with the 60s default.
func TestDefaultSweepInterval(t *testing.T) {
	if DefaultSweepInterval != 60*time.Second {
		t.Fatalf("DefaultSweepInterval = %v, want 60s", DefaultSweepInterval)
	}
}

// sweepHarness composes a Registry (fake-client CR registry with living
// shards) whose peerDialer routes each shard addr to a distinct
// fakeMirrorShard, a Sweeper over that registry, and the funnel's freshest
// record so tests can pin the diff target deterministically (F2).
type sweepHarness struct {
	reg    *Registry
	sw     *Sweeper
	shards map[string]*fakeMirrorShard // addr -> shard
	addrs  []string                    // CR registration order (living shards)
}

// newSweepHarness adds `shardIDs` as living shards, each backed by a distinct
// fakeMirrorShard, and builds a Registry + Sweeper over them. The funnel's
// freshest record is left EMPTY — tests seed it explicitly via
// reg.freshestShardID so the diff target is fully deterministic.
func newSweepHarness(t *testing.T, shardIDs ...string) *sweepHarness {
	t.Helper()
	now := time.Now().UTC()
	h := &sweepHarness{shards: map[string]*fakeMirrorShard{}}
	entries := make([]flowv1api.QueueShardStatus, 0, len(shardIDs))
	for _, id := range shardIDs {
		addr := "addr-" + id
		f := newFakeMirrorShard(t, id)
		h.shards[addr] = f
		h.addrs = append(h.addrs, addr)
		entries = append(entries, shard(id, addr, phaseActive, now))
	}
	seed := queueCR(testQueueName, entries...)
	c := newFakeClient(t, seed)
	r := NewRegistry(c, time.Hour, time.Second)
	r.Namespace = testNamespace
	r.freshestShardID = map[string]string{}
	r.peerDialer = func(ctx context.Context, addr string) (*grpc.ClientConn, error) {
		f, ok := h.shards[addr]
		if !ok {
			return nil, errShardUnavailable
		}
		return f.dialer(ctx, addr)
	}
	h.reg = r
	h.sw = NewSweeper(r, time.Millisecond)
	return h
}

// setFreshest records the per-queue freshest mirror (the funnel's recorded
// last-acked shard, F2) via the seam the round-2 implementer adds. Setting the
// map directly keeps the diff target deterministic without depending on write
// loop order (which shard "acked last" is not part of the contract).
func (h *sweepHarness) setFreshest(shardID string) {
	h.reg.recordFreshest(testQueueName, shardID)
}

// TestSweepBackstopPushesMissingDropsExtra pins R-4.2: one shard is behind
// (missing an item) — the sweep pushes it via generation-guarded ApplyItem; one
// shard holds an extra/stale item — the sweep drops it via generation-guarded
// DropItem. The authoritative reference is the recorded freshest mirror (F2).
func TestSweepBackstopPushesMissingDropsExtra(t *testing.T) {
	h := newSweepHarness(t, testShardA, "shard-b", "shard-c")
	// shard-a is the recorded freshest mirror (the diff target). It holds the
	// authoritative two-item set.
	h.setFreshest(testShardA)
	a := h.shards["addr-shard-a"]
	a.setItem(&flowv1.QueueItem{
		WorkitemId: testWorkitemID, QueueName: testQueueName, Status: "waiting",
		GenerationId: "0000000000000001",
	})
	a.setItem(&flowv1.QueueItem{
		WorkitemId: "wi-2", QueueName: testQueueName, Status: "waiting",
		GenerationId: "0000000000000002",
	})

	// shard-b is a straggler: missing wi-1 (has only wi-2).
	b := h.shards["addr-shard-b"]
	b.setItem(&flowv1.QueueItem{
		WorkitemId: "wi-2", QueueName: testQueueName, Status: "waiting",
		GenerationId: "0000000000000002",
	})

	// shard-c holds wi-1 + wi-2 plus an EXTRA stale item (wi-3) that is not in
	// the authoritative set — an orphaned backup copy the sweep must drop.
	c := h.shards["addr-shard-c"]
	c.setItem(&flowv1.QueueItem{
		WorkitemId: testWorkitemID, QueueName: testQueueName, Status: "waiting",
		GenerationId: "0000000000000001",
	})
	c.setItem(&flowv1.QueueItem{
		WorkitemId: "wi-2", QueueName: testQueueName, Status: "waiting",
		GenerationId: "0000000000000002",
	})
	c.setItem(&flowv1.QueueItem{
		WorkitemId: "wi-3", QueueName: testQueueName, Status: "waiting",
		GenerationId: "0000000000000009",
	})

	if err := h.sw.Sweep(context.Background(), testQueueName); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	// shard-b was pushed the missing wi-1 with the authoritative generation.
	got := b.serve()[testWorkitemID]
	if got == nil {
		t.Fatal("straggler shard-b does not hold wi-1 after sweep (missing push)")
	}
	if got.GetGenerationId() != "0000000000000001" {
		t.Fatalf("shard-b wi-1 generation = %q, want the authoritative 0000000000000001", got.GetGenerationId())
	}
	if b.serve()["wi-2"] == nil {
		t.Fatal("shard-b lost wi-2 after sweep")
	}

	// The push was issued as a real ApplyItem over the wire (not just a client
	// fake call) — shard-b's applied list records the re-delivered item.
	applied := b.appliedCalls()
	foundPush := false
	for _, it := range applied {
		if it.GetWorkitemId() == testWorkitemID {
			foundPush = true
		}
	}
	if !foundPush {
		t.Fatal("sweep did not ApplyItem wi-1 to shard-b")
	}

	// shard-c's extra stale item was dropped via generation-guarded DropItem.
	if dc := c.serve()["wi-3"]; dc != nil {
		t.Fatalf("shard-c still holds extra wi-3 after sweep: %+v", dc)
	}
	for _, id := range []string{testWorkitemID, "wi-2"} {
		if c.serve()[id] == nil {
			t.Fatalf("shard-c lost authoritative item %s after sweep", id)
		}
	}

	// The reference (shard-a) is untouched by the sweep.
	for _, id := range []string{testWorkitemID, "wi-2"} {
		if a.serve()[id] == nil {
			t.Fatalf("freshest mirror shard-a lost %s after sweep", id)
		}
	}
}

// TestSweepUsesRecordedFreshestNotUpdatedAt pins F2: the sweep picks its diff
// target from the funnel's recorded per-queue last-acked shard, NEVER from
// updated_at / heartbeat freshness. shard-a (the recorded freshest) has the
// OLDEST heartbeat, so any updated_at-based selection would choose a different
// shard — which lacks the authoritative item — and nothing would be pushed.
// The item being pushed proves shard-a was the reference.
func TestSweepUsesRecordedFreshestNotUpdatedAt(t *testing.T) {
	h := newSweepHarness(t, testShardA, "shard-b", "shard-c")

	// Give shard-a the OLDEST heartbeat (latest heartbeat = "most recently
	// updated") so an updated_at-arbitrating implementer would pick shard-a
	// LAST. All offsets stay within the lease TTL (1h) so every shard remains
	// living — the contrast is purely about which shard the sweep treats as its
	// diff reference, not about liveness. The recorded freshest remains shard-a.
	h.reg.freshestShardID[testQueueName] = testShardA
	q := &flowv1api.Queue{}
	key := h.reg.key(testQueueName)
	if err := h.reg.client.Get(context.Background(), key, q); err != nil {
		t.Fatalf("get CR: %v", err)
	}
	now := time.Now().UTC()
	for i := range q.Status.Shards {
		switch q.Status.Shards[i].ShardID {
		case testShardA:
			q.Status.Shards[i].LastHeartbeatAt = metav1Now(now.Add(-50 * time.Minute)) // oldest updated
		case "shard-b":
			q.Status.Shards[i].LastHeartbeatAt = metav1Now(now.Add(-30 * time.Minute))
		case "shard-c":
			q.Status.Shards[i].LastHeartbeatAt = metav1Now(now) // newest updated_at
		}
	}
	if err := h.reg.client.Status().Update(context.Background(), q); err != nil {
		t.Fatalf("update CR heartbeats: %v", err)
	}

	// Only shard-a (the recorded freshest, and the OLDEST-updated shard) holds
	// the authoritative item. shard-b and shard-c are missing it.
	a := h.shards["addr-shard-a"]
	a.setItem(&flowv1.QueueItem{
		WorkitemId: "wi-new", QueueName: testQueueName, Status: "waiting",
		GenerationId: "0000000000000007",
	})

	if err := h.sw.Sweep(context.Background(), testQueueName); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	// The item from the recorded freshest (shard-a) reached the other shards.
	// If the sweep had arbitrated by updated_at it would have picked shard-c
	// (newest heartbeat) as the reference — which holds nothing — so the push
	// would not have happened.
	for _, addr := range []string{"addr-shard-b", "addr-shard-c"} {
		got := h.shards[addr].serve()["wi-new"]
		if got == nil {
			t.Fatalf("%s missing wi-new — the sweep did not use the recorded "+
				"freshest shard-a as its diff target (it must not arbitrate by updated_at)",
				h.shards[addr].shardID)
		}
	}
}

// TestSweepConvergesIdenticalItemSets pins R-4.3: after one sweep, any two
// shards have IDENTICAL item sets (the auditable convergence property).
func TestSweepConvergesIdenticalItemSets(t *testing.T) {
	h := newSweepHarness(t, testShardA, "shard-b", "shard-c")
	// Reference (freshest) = shard-a with the full two-item set.
	h.setFreshest(testShardA)
	a := h.shards["addr-shard-a"]
	a.setItem(&flowv1.QueueItem{
		WorkitemId: testWorkitemID, QueueName: testQueueName, Status: "waiting",
		GenerationId: "0000000000000001",
	})
	a.setItem(&flowv1.QueueItem{
		WorkitemId: "wi-2", QueueName: testQueueName, Status: "waiting",
		GenerationId: "0000000000000002",
	})

	// shard-b straggles: missing wi-1, extra stale wi-x.
	b := h.shards["addr-shard-b"]
	b.setItem(&flowv1.QueueItem{
		WorkitemId: "wi-2", QueueName: testQueueName, Status: "waiting",
		GenerationId: "0000000000000002",
	})
	b.setItem(&flowv1.QueueItem{
		WorkitemId: "wi-x", QueueName: testQueueName, Status: "waiting",
		GenerationId: "0000000000000099",
	})

	// shard-c is a fresh empty shard (mirrors a restarted shard pre-sync).

	if err := h.sw.Sweep(context.Background(), testQueueName); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	// Every shard now holds exactly {wi-1: g1, wi-2: g2} — identical item sets.
	want := map[string]string{testWorkitemID: "0000000000000001", "wi-2": "0000000000000002"}
	for _, addr := range h.addrs {
		if err := assertSameItemSet(h.shards[addr].serve(), want); err != nil {
			t.Fatalf("shard %s: %v", h.shards[addr].shardID, err)
		}
	}
}

// assertSameItemSet checks that a shard's store is exactly the want set
// (workitem_id -> generation_id), the "identical item sets" convergence check.
func assertSameItemSet(store map[string]*flowv1.QueueItem, want map[string]string) error {
	if len(store) != len(want) {
		return fmt.Errorf("store has %d items, want %d", len(store), len(want))
	}
	for id, gen := range want {
		it := store[id]
		if it == nil {
			return fmt.Errorf("store missing %s", id)
		}
		if it.GetGenerationId() != gen {
			return fmt.Errorf("store %s generation = %q, want %q", id, it.GetGenerationId(), gen)
		}
	}
	return nil
}

// TestSweepNeverDowngradesNewerCopy pins the generation-guard safety half of
// R-4.3: the sweep must never push an OLDER copy over a shard's NEWER copy, and
// it must not drop an item that exists (even at an older generation) in the
// authoritative set. A newer parking event on a shard must survive a sweep.
func TestSweepNeverDowngradesNewerCopy(t *testing.T) {
	h := newSweepHarness(t, testShardA, "shard-b")
	// shard-a (freshest reference) holds wi-1 at an OLD generation.
	h.setFreshest(testShardA)
	h.shards["addr-shard-a"].setItem(&flowv1.QueueItem{
		WorkitemId: testWorkitemID, QueueName: testQueueName, Status: "waiting",
		GenerationId: "0000000000000001",
	})
	// shard-b holds wi-1 at a NEWER generation (a newer parking event that has
	// not yet propagated to shard-a — a partial-broadcast race).
	b := h.shards["addr-shard-b"]
	b.setItem(&flowv1.QueueItem{
		WorkitemId: testWorkitemID, QueueName: testQueueName, Status: "waiting",
		GenerationId: "0000000000000005",
	})

	if err := h.sw.Sweep(context.Background(), testQueueName); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	// The newer copy on shard-b must be preserved (no downgrade push, no drop).
	got := b.serve()[testWorkitemID]
	if got == nil {
		t.Fatal("shard-b lost wi-1 during sweep")
	}
	if got.GetGenerationId() != "0000000000000005" {
		t.Fatalf("shard-b wi-1 generation = %q, want the NEWER 0000000000000005 "+
			"(sweep must not downgrade)", got.GetGenerationId())
	}
}

// TestSweepReproducer_QuorumAckedSurvivesIncompleteFreshest is the data-loss
// reproducer (n=3, X,Y,Z):
//
//	Enqueue(A): Z fails → X,Y confirm → freshest=Y; A on {X,Y}.
//	Enqueue(B): Y fails → X,Z confirm → freshest=Z; B on {X,Z}.
//
// Recorded freshest = Z = {B} — incomplete (misses A). A single sweep must
// converge EVERY shard to {A,B} and must NOT destroy the only remaining copy
// of A. This FAILS against the old presence-only "drop anything absent from
// fresh" logic (A vanishes everywhere); it passes only with corroborated drops
// + symmetric push onto the reference.
func TestSweepReproducer_QuorumAckedSurvivesIncompleteFreshest(t *testing.T) {
	// shard-a (X) holds A and B; shard-b (Y) holds only A; shard-c (Z) holds
	// only B and is the recorded freshest reference.
	h := newSweepHarness(t, "shard-a", "shard-b", "shard-c")
	h.setFreshest("shard-c")

	h.shards["addr-shard-a"].setItem(&flowv1.QueueItem{
		WorkitemId: "A", QueueName: testQueueName, Status: "waiting",
		GenerationId: "0000000000000001",
	})
	h.shards["addr-shard-a"].setItem(&flowv1.QueueItem{
		WorkitemId: "B", QueueName: testQueueName, Status: "waiting",
		GenerationId: "0000000000000002",
	})
	h.shards["addr-shard-b"].setItem(&flowv1.QueueItem{
		WorkitemId: "A", QueueName: testQueueName, Status: "waiting",
		GenerationId: "0000000000000001",
	})
	h.shards["addr-shard-c"].setItem(&flowv1.QueueItem{
		WorkitemId: "B", QueueName: testQueueName, Status: "waiting",
		GenerationId: "0000000000000002",
	})

	if err := h.sw.Sweep(context.Background(), testQueueName); err != nil {
		t.Fatalf("Sweep: %v", err)
	}

	// The reference (shard-c) must be repaired with the A it lacks (symmetric
	// push), and no corroborated copy may be dropped. After one sweep EVERY
	// shard must hold BOTH A and B — all quorum-acked items survive everywhere.
	for _, addr := range h.addrs {
		store := h.shards[addr].serve()
		if store["A"] == nil {
			t.Fatalf("shard %s lost A — quorum-acked item destroyed by an incomplete "+
				"freshest (data-loss bug); every shard must hold A", h.shards[addr].shardID)
		}
		if store["B"] == nil {
			t.Fatalf("shard %s lost B — every shard must hold B", h.shards[addr].shardID)
		}
	}
}

// TestTrimReport_PayloadFree pins F3 (per-shard payload-free report): mapping
// a GetLocalQueue response's items to {workitem_id -> generation_id} only,
// discarding payloads (choices, timestamps, status, shard_id).
func TestTrimReport_PayloadFree(t *testing.T) {
	f := newFakeMirrorShard(t, testShardA)
	f.setItem(&flowv1.QueueItem{
		WorkitemId: testWorkitemID, QueueName: testQueueName, Status: "waiting",
		GenerationId: "0000000000000001", ShardId: testShardA, EnqueuedAt: "2026-08-21T00:00:00Z",
		Choices: []string{"approve", "reject"},
	})
	f.setItem(&flowv1.QueueItem{
		WorkitemId: "wi-2", QueueName: testQueueName, Status: "claimed",
		GenerationId: "0000000000000002", ShardId: testShardA, EnqueuedAt: "2026-08-21T01:00:00Z",
	})

	resp, err := f.GetLocalQueue(context.Background(), &flowv1.GetLocalQueueRequest{})
	if err != nil {
		t.Fatalf("GetLocalQueue: %v", err)
	}
	report := trimReport(resp.GetItems())

	if len(report) != 2 {
		t.Fatalf("trimmed report has %d entries, want 2", len(report))
	}
	if report[testWorkitemID] != "0000000000000001" || report["wi-2"] != "0000000000000002" {
		t.Fatalf("trimmed report = %v, want {wi-1: g1, wi-2: g2}", report)
	}
}

// TestTrimReportEmpty pins the degenerate case: an empty GetLocalQueue response
// yields an empty report.
func TestTrimReportEmpty(t *testing.T) {
	if r := trimReport(nil); len(r) != 0 {
		t.Fatalf("trimReport(nil) = %v, want empty", r)
	}
	if r := trimReport([]*flowv1.QueueItem{}); len(r) != 0 {
		t.Fatalf("trimReport(empty) = %v, want empty", r)
	}
}

// planConvergence is the pure diff pinned by the tests below. Its signature is
// declared in sweep.go; the tests exercise it against the still-2-arg RED
// declaration until the implementer adds the corroboration parameter. The
// corrected contract it must implement:
//
//	func planConvergence(
//		fresh []*flowv1.QueueItem, targetGen map[string]string,
//		otherReports []map[string]string,
//	) (push []*flowv1.QueueItem, drops []dropRef)
//
//   - push: every item in F missing from the target (full items from F,
//     generation-guarded ApplyItem).
//   - drops: an item the target holds that is ABSENT from F is a genuine orphan
//     only when it is ALSO absent from EVERY other non-reference shard's report
//     — then it is dropped (generation-guarded, S's recorded gen). If it is
//     present on ANY other non-reference shard it is a corroborated copy that
//     must survive and is NOT dropped (the data-loss guard). Presence on another
//     non-reference shard always wins: absence from the reference ALONE never
//     justifies a drop.
//
// targetGen: the target shard's trimmed {workitem_id -> generation_id}
// report. otherReports: the trimmed reports of every OTHER non-reference
// shard. Returns the ApplyItem pushes and the generation-guarded DropItem
// refs.

// planPushToReference is the pure symmetric-push-onto-reference helper pinned
// by TestPlanPushToReference. It must be added by the sweeping implementer
// alongside the corrected planConvergence:
//
//	func planPushToReference(freshestGen map[string]string, otherReports []map[string]string) []*flowv1.QueueItem
//
// It computes the symmetric push onto the reference shard: every item present
// on some non-reference shard but ABSENT from the recorded freshest reference
// F (full items from the reporting shards' reports) is pushed onto the
// reference. The reference is a repairable straggler, never a reason to destroy
// copies (R-4.2 corrected). `freshestGen` is the reference's current trimmed
// report ({workitem_id -> generation_id}).

// TestPlanConvergence_PushAndDrop pins the pure diff (R-4.2/R-4.3 corrected):
// against the freshest mirror's item set, a shard missing an item gets it
// pushed, and a shard holding an EXTRA item absent from the reference AND from
// every other shard's report (a corroborated orphan) gets it dropped.
func TestPlanConvergence_PushAndDrop(t *testing.T) {
	fresh := []*flowv1.QueueItem{
		{WorkitemId: testWorkitemID, GenerationId: "0000000000000001"},
		{WorkitemId: "wi-2", GenerationId: "0000000000000002"},
	}
	// Target shard holds wi-2 (same generation) and an EXTRA wi-9 that is not
	// in the authoritative set, but is MISSING wi-1.
	shard := map[string]string{
		"wi-2": "0000000000000002",
		"wi-9": "0000000000000099",
	}
	// Other non-reference shards hold ONLY authoritative items — wi-9 appears
	// nowhere else, so it is a genuine orphan and is dropped.
	others := []map[string]string{
		{"wi-2": "0000000000000002"},
	}

	push, drops := planConvergence(fresh, shard, others)

	// Missing authoritative item wi-1 is pushed (with its full item).
	if len(push) != 1 || push[0].GetWorkitemId() != testWorkitemID || push[0].GetGenerationId() != "0000000000000001" {
		t.Fatalf("push = %+v, want exactly [wi-1@0000000000000001]", push)
	}
	// Extra wi-9 is dropped (with the shard's recorded generation) because it
	// is absent from the reference AND from every other shard's report.
	if len(drops) != 1 || drops[0].WorkitemID != "wi-9" || drops[0].GenerationID != "0000000000000099" {
		t.Fatalf("drops = %+v, want exactly [wi-9@0000000000000099]", drops)
	}
}

// TestPlanConvergence_CorroboratedDropNotDestroyed pins the corrected drop
// semantics: an item in the target absent from the (incomplete) reference is a
// corroborated copy, NOT a genuine orphan, when it is present on ANY other
// non-reference shard — it must survive and is NOT dropped (the data-loss
// guard). The presence of a corroborating report wins even if a different
// non-reference shard also lacks the item: "present on another non-reference
// shard → do NOT drop" (fix contract).
func TestPlanConvergence_CorroboratedDropNotDestroyed(t *testing.T) {
	// Reference is INCOMPLETE — the recorded freshest is shard-c missing A
	// (the reproduced partial-broadcast row).
	ref := []*flowv1.QueueItem{
		{WorkitemId: "B", GenerationId: "0000000000000002"},
	}
	// Target shard-a holds A and B; A is absent from the reference but is a
	// quorum-acked item that ALSO lives on shard-b. A THIRD non-reference shard
	// (shard-x) does not hold A — but shard-b's corroboration makes A survive.
	target := map[string]string{"A": "0000000000000001", "B": "0000000000000002"}
	others := []map[string]string{
		{"A": "0000000000000001"}, // shard-b corroborates A → A survives
		{"B": "0000000000000002"}, // shard-x does not hold A; must NOT cause a drop
	}

	push, drops := planConvergence(ref, target, others)

	// A is corroborated by shard-b, so it must NOT be dropped — even though
	// shard-x lacks it. Absence from the reference alone never justifies a drop.
	if len(drops) != 0 {
		t.Fatalf("drops = %+v, want NONE — A is corroborated by shard-b and must survive; "+
			"a genuine orphan is absent from the reference AND from EVERY other non-reference report", drops)
	}
	// Nothing in the reference is missing from the target, so no push (B is
	// already there).
	if len(push) != 0 {
		t.Fatalf("push = %+v, want none (target already holds B)", push)
	}
}

// TestPlanConvergence_GenuineOrphanDropped pins the OTHER side of the drop
// guard: an item in the target that is absent from the reference AND absent
// from EVERY other non-reference shard's report is a genuine orphan and IS
// dropped (generation-guarded).
func TestPlanConvergence_GenuineOrphanDropped(t *testing.T) {
	ref := []*flowv1.QueueItem{
		{WorkitemId: "B", GenerationId: "0000000000000002"},
	}
	// Target shard-a holds a stale extra "orphan" that no other shard holds —
	// a real orphaned backup copy.
	target := map[string]string{"B": "0000000000000002", "orphan": "0000000000000099"}
	others := []map[string]string{
		{"A": "0000000000000001"}, // other shards have A, but NOT "orphan"
	}

	push, drops := planConvergence(ref, target, others)

	// "orphan" is absent from the reference and absent from every other
	// non-reference report → genuine orphan → dropped (generation-guarded).
	if len(drops) != 1 || drops[0].WorkitemID != "orphan" || drops[0].GenerationID != "0000000000000099" {
		t.Fatalf("drops = %+v, want exactly [orphan@0000000000000099]", drops)
	}
	if len(push) != 0 {
		t.Fatalf("push = %+v, want none (target already holds B)", push)
	}
}

// TestPlanConvergence_NoDowngrade pins the generation-guard in the pure diff:
// a shard holding a NEWER copy of an authoritative item must not be offered an
// older push nor have the item dropped.
func TestPlanConvergence_NoDowngrade(t *testing.T) {
	fresh := []*flowv1.QueueItem{
		{WorkitemId: testWorkitemID, GenerationId: "0000000000000001"}, // authoritative but old
	}
	// Target shard holds a NEWER copy of wi-1 (generation 5 > authoritative 1).
	shard := map[string]string{testWorkitemID: "0000000000000005"}
	var others []map[string]string

	push, drops := planConvergence(fresh, shard, others)

	if len(push) != 0 {
		t.Fatalf("push = %+v, want no downgrade push for a newer local copy", push)
	}
	if len(drops) != 0 {
		t.Fatalf("drops = %+v, want no drop for an item present in the authoritative set", drops)
	}
}

// TestPlanConvergence_StaleGenerationPushed pins the generation-aware
// convergence half of the re-park supersede rule (SPEC integration test 3): a
// shard holding an OLDER generation of a workitem whose NEWER generation is the
// recorded freshest reference must be pushed the newer re-parked copy. The
// presence-based plan (a target holding ANY copy is never pushed onto) would
// leave the stale copy in place forever.
func TestPlanConvergence_StaleGenerationPushed(t *testing.T) {
	// The recorded freshest reference holds the NEWER re-parked copy.
	fresh := []*flowv1.QueueItem{
		{WorkitemId: testWorkitemID, GenerationId: "0000000000000002"},
	}
	// Target shard holds an OLDER stale copy of the same workitem — the re-park
	// supersede case: presence-based convergence sees wi-1 in the target and
	// never pushes the authoritative newer generation onto it.
	shard := map[string]string{testWorkitemID: "0000000000000001"}
	// An unrelated other shard: it neither corroborates the stale copy nor holds
	// the newer one, keeping the fixture shape realistic (the target's stale
	// wi-1 is not corroborated elsewhere).
	others := []map[string]string{
		{"wi-2": "0000000000000099"},
	}

	push, drops := planConvergence(fresh, shard, others)

	// The NEWER authoritative generation must be pushed onto the stale copy.
	if len(push) != 1 || push[0].GetWorkitemId() != testWorkitemID || push[0].GetGenerationId() != "0000000000000002" {
		t.Fatalf("push = %+v, want the newer authoritative copy 0000000000000002 "+
			"pushed onto the stale 0000000000000001", push)
	}
	// The id is present in the authoritative set → never a drop candidate.
	if len(drops) != 0 {
		t.Fatalf("drops = %+v, want none — wi-1 is in the authoritative set, never a drop candidate", drops)
	}
}

// TestPlanPushToReference pins the symmetric push onto the reference: an item
// present on some non-reference shard but ABSENT from the recorded freshest
// reference is pushed onto the reference. The reference is a repairable
// straggler, never a reason to destroy copies.
func TestPlanPushToReference(t *testing.T) {
	// The recorded freshest reference (shard-c) is INCOMPLETE: it holds only B,
	// missing A (the quorum-acked partial-broadcast row).
	freshestGen := map[string]string{"B": "0000000000000002"}
	// Non-reference shards hold A (shard-a) and B (shard-b, shard-c-like).
	others := []map[string]string{
		{"A": "0000000000000001", "B": "0000000000000002"}, // shard-a has A+B
		{"B": "0000000000000002"},                          // shard-b has only B
	}

	push := planPushToReference(freshestGen, others)

	// A is absent from the reference but present on another non-reference shard,
	// so the reference must be repaired with it.
	if len(push) != 1 || push[0].GetWorkitemId() != "A" {
		t.Fatalf("push onto reference = %+v, want exactly [A] (reference is a repairable straggler)", push)
	}
}

// TestFunnelRecordsFreshestOnAck pins seam A: a quorum-acked enqueue records
// the per-queue freshest-mirror (the funnel's queueName -> freshestShardID map)
// on ack, so the sweep has a recorded diff target (F2). The recorded shard must
// be one of the living shards that confirmed the write.
func TestFunnelRecordsFreshestOnAck(t *testing.T) {
	ctx := context.Background()
	h := newFunnelHarness(t, testShardA, "shard-b")

	if resp, err := h.gateway.Enqueue(ctx, &flowv1.EnqueueRequest{
		QueueName: testQueueName, WorkitemId: testWorkitemID, Choices: []string{testChoiceApprove},
	}); err != nil || !resp.GetAcknowledged() {
		t.Fatalf("Enqueue ack: ack=%v err=%v", resp.GetAcknowledged(), err)
	}

	fresh := h.reg.FreshestShardID(testQueueName)
	if fresh == "" {
		t.Fatal("enqueue ack did not record a per-queue freshest shard")
	}
	if fresh != testShardA && fresh != "shard-b" {
		t.Fatalf("freshest shard = %q, want one of the confirming living shards", fresh)
	}
}
