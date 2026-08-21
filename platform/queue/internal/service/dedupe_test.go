package service

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// These tests pin the R-3.6 read-dedupe selection rules of listItemsDeduped /
// getItemDeduped (the scatter-gather mirror read path). They drive a peerProxy
// directly (newPeerProxy over a funnelHarness Registry whose peerDialer routes
// each shard addr to a distinct fakeMirrorShard), so the single unit under test
// is the dedupe function and its wire collaborators are the in-memory fakes.

// TestDedupe_MaxGenerationWins pins (a): the same workitem on two shards with
// different generations collapses to exactly one copy, and the NEWER generation
// wins (deterministic lexicographic ordering). listItemsDeduped returns one item
// carrying the maximal generation_id, and getItemDeduped agrees.
func TestDedupe_MaxGenerationWins(t *testing.T) {
	h := newFunnelHarness(t, "shard-a", "shard-b")
	// Same workitem on both shards; shard-b holds the newer generation.
	h.shards["addr-shard-a"].setItem(&flowv1.QueueItem{
		WorkitemId: testWorkitemID, QueueName: testQueueName,
		Status: testStatusWaiting, GenerationId: "0000000000000001",
		ShardId: "shard-a",
	})
	h.shards["addr-shard-b"].setItem(&flowv1.QueueItem{
		WorkitemId: testWorkitemID, QueueName: testQueueName,
		Status: testStatusWaiting, GenerationId: "0000000000000002",
		ShardId: "shard-b",
	})

	proxy := newPeerProxy(h.reg)
	defer proxy.close()

	items, err := proxy.listItemsDeduped(context.Background(), testQueueName, "")
	if err != nil {
		t.Fatalf("listItemsDeduped: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("listItemsDeduped returned %d items, want exactly 1 (deduped)", len(items))
	}
	if got := items[0].GetGenerationId(); got != "0000000000000002" {
		t.Fatalf("surviving item generation_id = %q, want the newer 0000000000000002", got)
	}

	item, err := proxy.getItemDeduped(context.Background(), testQueueName, testWorkitemID)
	if err != nil {
		t.Fatalf("getItemDeduped: %v", err)
	}
	if item == nil || item.GetGenerationId() != "0000000000000002" {
		t.Fatalf("getItemDeduped returned %+v, want the newer generation copy", item)
	}
}

// TestDedupe_OwnerTieBreak pins (b): equal generation on two shards, one copy
// served by its owner (item.ShardId == served_by_shard_id of the shard serving
// it) and the other a copy served by a non-owner. The owner copy wins.
func TestDedupe_OwnerTieBreak(t *testing.T) {
	h := newFunnelHarness(t, "shard-a", "shard-b")
	const gen = "0000000000000001"
	// shard-a serves the owner copy: ShardId == its own id ("shard-a"), so on
	// shard-a served_by_shard_id == "shard-a" → candOwner true.
	h.shards["addr-shard-a"].setItem(&flowv1.QueueItem{
		WorkitemId: testWorkitemID, QueueName: testQueueName,
		Status: testStatusWaiting, GenerationId: gen,
		ShardId: "shard-a",
	})
	// shard-b serves a NON-owner copy: ShardId "other-O" != served "shard-b".
	h.shards["addr-shard-b"].setItem(&flowv1.QueueItem{
		WorkitemId: testWorkitemID, QueueName: testQueueName,
		Status: testStatusWaiting, GenerationId: gen,
		ShardId: "other-O",
	})

	proxy := newPeerProxy(h.reg)
	defer proxy.close()

	items, err := proxy.listItemsDeduped(context.Background(), testQueueName, "")
	if err != nil {
		t.Fatalf("listItemsDeduped: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("listItemsDeduped returned %d items, want exactly 1 (deduped)", len(items))
	}
	// The owner copy (ShardId == its serving shard id "shard-a") must win.
	if got := items[0].GetShardId(); got != "shard-a" {
		t.Fatalf("surviving item shard_id = %q, want the owner copy shard-a", got)
	}

	item, err := proxy.getItemDeduped(context.Background(), testQueueName, testWorkitemID)
	if err != nil {
		t.Fatalf("getItemDeduped: %v", err)
	}
	if item == nil || item.GetShardId() != "shard-a" {
		t.Fatalf("getItemDeduped returned %+v, want the owner copy shard-a", item)
	}
}

// TestDedupe_TieNoOwner_FirstWins pins the deterministic fallback: equal
// generation and NEITHER copy served by its owner → the dedupe still returns
// exactly one item without error (the first encountered wins; bestOwner stays
// false so the tie stays non-owner).
func TestDedupe_TieNoOwner_FirstWins(t *testing.T) {
	h := newFunnelHarness(t, "shard-a", "shard-b")
	const gen = "0000000000000001"
	// Both copies carry a shard_id that matches NO serving shard → neither is an
	// owner copy. Equal generation, so the owner tie-break is a no-op.
	h.shards["addr-shard-a"].setItem(&flowv1.QueueItem{
		WorkitemId: testWorkitemID, QueueName: testQueueName,
		Status: testStatusWaiting, GenerationId: gen,
		ShardId: "other-O",
	})
	h.shards["addr-shard-b"].setItem(&flowv1.QueueItem{
		WorkitemId: testWorkitemID, QueueName: testQueueName,
		Status: testStatusWaiting, GenerationId: gen,
		ShardId: "other-O",
	})

	proxy := newPeerProxy(h.reg)
	defer proxy.close()

	items, err := proxy.listItemsDeduped(context.Background(), testQueueName, "")
	if err != nil {
		t.Fatalf("listItemsDeduped: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("listItemsDeduped returned %d items, want exactly 1 deterministic result", len(items))
	}
}
