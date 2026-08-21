package service

import (
	"context"
	"sync"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flowv1api "github.com/foundry/flow/operator/api/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// These tests drive the PHASE_03 write funnel through the GatewayServer (the
// concrete SDK-facing contract). They are RED today — the funnel methods and
// GatewayServer bodies are stubs that return errShardUnavailable / empty
// responses — and turn green once the implementer wires the broadcast
// funnel over the Registry's per-item in-flight guard. Each test checks real
// mirror-shard state (via the faithful fakeMirrorShard), so a mere "no error"
// from a stub is never enough to pass.

// funnelHarness seeds a Registry (fake client seeded with living shards) whose
// peerDialer routes each shard addr to a distinct fakeMirrorShard, plus a
// GatewayServer over that Registry.
type funnelHarness struct {
	reg     *Registry
	gateway *GatewayServer
	shards  map[string]*fakeMirrorShard // addr -> shard
	addrs   []string
}

func newFunnelHarness(t *testing.T, shardIDs ...string) *funnelHarness {
	t.Helper()
	now := time.Now().UTC()
	h := &funnelHarness{shards: map[string]*fakeMirrorShard{}}
	entries := make([]flowv1api.QueueShardStatus, 0, len(shardIDs))
	for _, id := range shardIDs {
		addr := "addr-" + id
		f := newFakeMirrorShard(t, id)
		h.shards[addr] = f
		h.addrs = append(h.addrs, addr)
		entries = append(entries, shard(id, addr, phaseActive, now))
	}
	seed := queueCR("hitl-approval", entries...)
	c := newFakeClient(t, seed)
	r := NewRegistry(c, time.Minute, time.Second)
	r.Namespace = testNamespace
	r.peerDialer = func(ctx context.Context, addr string) (*grpc.ClientConn, error) {
		f, ok := h.shards[addr]
		if !ok {
			return nil, errShardUnavailable
		}
		return f.dialer(ctx, addr)
	}
	h.reg = r
	h.gateway = NewGatewayServer(r)
	return h
}

// TestEnqueueFansOutToAllLivingShards pins R-3.1/3.2/3.5: the funnel mints a
// generation_id (same across shards), picks one owner shard_id at random, and
// ApplyItem-fans out to EVERY living shard, carrying the caller's choices.
func TestEnqueueFansOutToAllLivingShards(t *testing.T) {
	h := newFunnelHarness(t, "shard-a", "shard-b", "shard-c")

	resp, err := h.gateway.Enqueue(context.Background(), &flowv1.EnqueueRequest{
		QueueName: "hitl-approval", WorkitemId: "wi-1",
		Choices: []string{"approve", "reject"},
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("enqueue not acknowledged")
	}

	gens := make([]string, 0, len(h.addrs))
	owner := ""
	for _, addr := range h.addrs {
		f := h.shards[addr]
		if applied := f.appliedCalls(); len(applied) != 1 {
			t.Fatalf("shard %s received %d ApplyItem calls, want exactly 1", f.shardID, len(applied))
		}
		item := f.serve()[testWorkitemID]
		if item == nil {
			t.Fatalf("shard %s does not hold wi-1 after enqueue", f.shardID)
		}
		if item.GetWorkitemId() != "wi-1" || item.GetQueueName() != "hitl-approval" {
			t.Fatalf("shard %s item = %+v", f.shardID, item)
		}
		if item.GetStatus() != "waiting" {
			t.Fatalf("shard %s item status = %q, want waiting", f.shardID, item.GetStatus())
		}
		if len(item.GetChoices()) != 2 || item.GetChoices()[0] != "approve" || item.GetChoices()[1] != "reject" {
			t.Fatalf("shard %s item choices = %v, want [approve reject]", f.shardID, item.GetChoices())
		}
		gens = append(gens, item.GetGenerationId())
		if owner == "" {
			owner = item.GetShardId()
		}
	}

	// All shards carry the SAME minted, non-empty generation_id.
	if gens[0] == "" {
		t.Fatal("generation_id must be non-empty")
	}
	for _, g := range gens[1:] {
		if g != gens[0] {
			t.Fatalf("generation_id differs across shards: %v", gens)
		}
	}

	// The owner shard_id is non-empty and is one of the living shards.
	if owner == "" {
		t.Fatal("owner shard_id must be non-empty")
	}
	valid := false
	for _, want := range []string{"shard-a", "shard-b", "shard-c"} {
		if owner == want {
			valid = true
		}
	}
	if !valid {
		t.Fatalf("owner shard_id = %q, want one of the 3 living shards", owner)
	}
}

// TestEnqueueQuorumAck pins R-3.2 / Plan F4 quorum accounting. One subtest
// verifies a majority confirm-then-ack (a mid-write shard failure does not
// block the write); the other verifies a minority confirmation yields
// codes.Unavailable.
func TestEnqueueQuorumAck(t *testing.T) {
	t.Run("majority-confirms-acks", func(t *testing.T) {
		// 4 living shards, 1 of them fails ApplyItem mid-write. 3 confirm ⇒
		// quorum (majority of the living set) reached → ack. The non-failed
		// shards all hold the item.
		h := newFunnelHarness(t, "shard-a", "shard-b", "shard-c", "shard-d")
		h.shards["addr-shard-d"].setApplyError(status.Error(codes.Unavailable, "shard d down"))

		resp, err := h.gateway.Enqueue(context.Background(), &flowv1.EnqueueRequest{
			QueueName: "hitl-approval", WorkitemId: "wi-1",
		})
		if err != nil {
			t.Fatalf("Enqueue with 3-of-4 confirmations must ack, got err: %v", err)
		}
		if !resp.GetAcknowledged() {
			t.Fatal("enqueue not acknowledged")
		}
		for _, addr := range []string{"addr-shard-a", "addr-shard-b", "addr-shard-c"} {
			if h.shards[addr].serve()[testWorkitemID] == nil {
				t.Fatalf("shard %s does not hold wi-1 after quorum ack", h.shards[addr].shardID)
			}
		}
	})

	t.Run("minority-confirms-unavailable", func(t *testing.T) {
		// 3 living shards, 2 of them fail ApplyItem mid-write. Only 1 confirms,
		// below the majority of the 3-shard living set → codes.Unavailable.
		h := newFunnelHarness(t, "shard-a", "shard-b", "shard-c")
		h.shards["addr-shard-a"].setApplyError(status.Error(codes.Unavailable, "down"))
		h.shards["addr-shard-b"].setApplyError(status.Error(codes.Unavailable, "down"))

		_, err := h.gateway.Enqueue(context.Background(), &flowv1.EnqueueRequest{
			QueueName: "hitl-approval", WorkitemId: "wi-2",
		})
		if err == nil {
			t.Fatal("Enqueue with 1-of-3 confirmations must fail")
		}
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("code = %v, want Unavailable", status.Code(err))
		}
	})
}

// TestConcurrentClaims_OneWins pins R-3.2's per-item in-flight guard: two
// concurrent Claims on ONE workitem are serialized by Registry.lockItem, so
// exactly one wins (status claimed) and the other is ErrQueueItemAlreadyClaimed
// (gateway → codes.AlreadyExists) on every shard, leaving all shards in the
// identical claimed state.
func TestConcurrentClaims_OneWins(t *testing.T) {
	h := newFunnelHarness(t, "shard-a", "shard-b", "shard-c")
	// Seed the pre-enqueued waiting item on every shard.
	seed := &flowv1.QueueItem{
		WorkitemId: testWorkitemID, QueueName: testQueueName,
		Status: testStatusWaiting, GenerationId: "0000000000000001",
	}
	for _, addr := range h.addrs {
		h.shards[addr].setItem(seed)
	}

	type claimResult struct {
		resp *flowv1.ClaimResponse
		err  error
	}
	results := make([]claimResult, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, err := h.gateway.Claim(context.Background(), &flowv1.ClaimRequest{
				QueueName: "hitl-approval", WorkitemId: "wi-1",
			})
			results[i] = claimResult{resp: resp, err: err}
		}(i)
	}
	wg.Wait()

	var wins, alreadyExists int
	for _, r := range results {
		switch {
		case r.err == nil:
			wins++
			if r.resp.GetItem().GetStatus() != "claimed" {
				t.Fatalf("winning claim item status = %q, want claimed", r.resp.GetItem().GetStatus())
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

	// The in-flight guard serialized the two Claims into one committed
	// transition, so every shard ends in the identical claimed state.
	for _, addr := range h.addrs {
		got := h.shards[addr].serve()[testWorkitemID]
		if got == nil || got.GetStatus() != testStatusClaimed {
			t.Fatalf("shard %s final state = %+v, want claimed", h.shards[addr].shardID, got)
		}
	}
}

// TestGenerationGuard_NoDowngrade pins R-3.3 at the store: a re-delivered /
// OLDER ApplyItem is a no-op and must not downgrade a newer stored copy
// (fixed-width time-ordered hex generation so >= is the correct ordering). This
// is the guard contract the implementer's real shard store must reproduce; the
// fakeMirrorShard already implements it faithfully, so this test documents and
// validates that store behaviour.
func TestGenerationGuard_NoDowngrade(t *testing.T) {
	f := newFakeMirrorShard(t, "shard-a")
	newer := &flowv1.QueueItem{WorkitemId: "wi-1", Status: "waiting", GenerationId: "0000000000000002"}
	older := &flowv1.QueueItem{WorkitemId: "wi-1", Status: "waiting", GenerationId: "0000000000000001"}

	if _, err := f.ApplyItem(context.Background(), &flowv1.ApplyItemRequest{Item: newer}); err != nil {
		t.Fatalf("apply newer: %v", err)
	}
	// Older re-delivery still acks but is a no-op.
	if _, err := f.ApplyItem(context.Background(), &flowv1.ApplyItemRequest{Item: older}); err != nil {
		t.Fatalf("apply older: %v", err)
	}

	got := f.serve()[testWorkitemID]
	if got == nil || got.GetGenerationId() != "0000000000000002" {
		t.Fatalf("after older re-delivery the shard holds generation %q, want "+
			"the newer 0000000000000002", got.GetGenerationId())
	}
}

// TestClaimReleaseDecideBroadcast pins the full write lifecycle through the
// funnel: enqueue (seeded), Claim broadcast leaves every shard claimed, Decide
// broadcast removes the item from EVERY shard with an ack.
func TestClaimReleaseDecideBroadcast(t *testing.T) {
	h := newFunnelHarness(t, "shard-a", "shard-b", "shard-c")
	seed := &flowv1.QueueItem{
		WorkitemId: testWorkitemID, QueueName: testQueueName,
		Status: testStatusWaiting, GenerationId: "0000000000000001",
	}
	for _, addr := range h.addrs {
		h.shards[addr].setItem(seed)
	}

	ctx := context.Background()
	// Claim broadcast: every shard transitions waiting→claimed.
	if _, err := h.gateway.Claim(ctx, &flowv1.ClaimRequest{
		QueueName: testQueueName, WorkitemId: testWorkitemID,
	}); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	for _, addr := range h.addrs {
		got := h.shards[addr].serve()[testWorkitemID]
		if got == nil || got.GetStatus() != testStatusClaimed {
			t.Fatalf("shard %s after claim = %+v, want claimed", h.shards[addr].shardID, got)
		}
	}

	// Decide broadcast: the item is removed from EVERY shard and acked.
	dec, err := h.gateway.Decide(ctx, &flowv1.DecideRequest{
		QueueName: testQueueName, WorkitemId: testWorkitemID, Choice: testChoiceApprove,
	})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !dec.GetAcknowledged() {
		t.Fatal("decide not acknowledged")
	}
	for _, addr := range h.addrs {
		f := h.shards[addr]
		if got := f.serve()[testWorkitemID]; got != nil {
			t.Fatalf("shard %s still holds %s after decide: %+v", f.shardID, testWorkitemID, got)
		}
		if decided := f.decidedCalls(); len(decided) != 1 {
			t.Fatalf("shard %s received %d DecideItem calls, want 1", f.shardID, len(decided))
		}
	}
}
