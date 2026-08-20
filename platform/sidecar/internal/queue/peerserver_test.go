package queue

import (
	"context"
	"reflect"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// wireItem builds a wire flowv1.QueueItem matching the internal item() helper
// shape (same workitem/shard/queue/generation/choices) so round-trips through
// the conversion helpers are stable and deterministic. The shard is always the
// test-serving shard "shard-peersrv", so it is inlined here rather than
// parameterized.
func wireItem(workitemID, generation string) *flowv1.QueueItem {
	return &flowv1.QueueItem{
		WorkitemId:   workitemID,
		ShardId:      "shard-peersrv",
		QueueName:    "queue-a",
		Status:       string(QueueStatusWaiting),
		EnqueuedAt:   time.Unix(1_700_000_000, 0).UTC().Format(time.RFC3339),
		GenerationId: generation,
		Choices:      []string{"approve", "reject"},
	}
}

func TestPeerServerApplyAndGetLocalQueue(t *testing.T) {
	const (
		shardID    = "shard-peersrv"
		queueName  = "queue-a"
		workitemID = "wi-apply"
		generation = "0000000000000001-abc"
	)

	st := newTestStore(t)
	defer func() { _ = st.Close() }()

	p := NewPeerServer(st, shardID, queueName)

	ctx := context.Background()
	resp, err := p.ApplyItem(ctx, &flowv1.ApplyItemRequest{Item: wireItem(workitemID, generation)})
	if err != nil {
		t.Fatalf("ApplyItem() error: %v", err)
	}
	if resp == nil || !resp.Acknowledged {
		t.Fatalf("ApplyItem() response = %+v, want acknowledged", resp)
	}

	// The applied write must have landed in the underlying store.
	stored := getItem(t, st, workitemID)
	if stored.Status != QueueStatusWaiting {
		t.Errorf("stored Status = %q, want waiting", stored.Status)
	}

	// GetLocalQueue must return it as a wire item with the serving shard tag.
	qresp, err := p.GetLocalQueue(ctx, &flowv1.GetLocalQueueRequest{})
	if err != nil {
		t.Fatalf("GetLocalQueue() error: %v", err)
	}
	if qresp == nil {
		t.Fatal("GetLocalQueue() returned nil response")
	}
	if qresp.ServedByShardId != shardID {
		t.Errorf("ServedByShardId = %q, want %q", qresp.ServedByShardId, shardID)
	}
	if qresp.Total != 1 || len(qresp.Items) != 1 {
		t.Fatalf("GetLocalQueue() total=%d items=%d, want total=1 items=1", qresp.Total, len(qresp.Items))
	}
	got := qresp.Items[0]
	if got.WorkitemId != workitemID {
		t.Errorf("WorkitemId = %q, want %q", got.WorkitemId, workitemID)
	}
	if got.Status != string(QueueStatusWaiting) {
		t.Errorf("Status = %q, want %q", got.Status, QueueStatusWaiting)
	}
	if !reflect.DeepEqual(got.Choices, wireItem(workitemID, generation).Choices) {
		t.Errorf("Choices = %v, want round-tripped %v", got.Choices, []string{"approve", "reject"})
	}
}

func TestPeerServerClaimRelease(t *testing.T) {
	const (
		shardID    = "shard-peersrv"
		queueName  = "queue-a"
		workitemID = "wi-claim"
		generation = "0000000000000001-abc"
	)

	st := newTestStore(t)
	defer func() { _ = st.Close() }()

	p := NewPeerServer(st, shardID, queueName)
	ctx := context.Background()

	if _, err := p.ApplyItem(ctx, &flowv1.ApplyItemRequest{Item: wireItem(workitemID, generation)}); err != nil {
		t.Fatalf("ApplyItem() error: %v", err)
	}

	// Claim a waiting item -> response carries a claimed item.
	cresp, err := p.ClaimItem(ctx, &flowv1.ClaimItemRequest{WorkitemId: workitemID})
	if err != nil {
		t.Fatalf("ClaimItem() error: %v", err)
	}
	if cresp == nil || cresp.Item == nil {
		t.Fatal("ClaimItem() response has nil item")
	}
	if cresp.Item.Status != string(QueueStatusClaimed) {
		t.Errorf("ClaimItem() status = %q, want %q", cresp.Item.Status, QueueStatusClaimed)
	}
	if cresp.Item.ClaimedAt == "" {
		t.Error("ClaimItem() ClaimedAt is empty, want set")
	}

	// Release it back to waiting.
	rresp, err := p.ReleaseItem(ctx, &flowv1.ReleaseItemRequest{WorkitemId: workitemID})
	if err != nil {
		t.Fatalf("ReleaseItem() error: %v", err)
	}
	if rresp == nil || rresp.Item == nil {
		t.Fatal("ReleaseItem() response has nil item")
	}
	if rresp.Item.Status != string(QueueStatusWaiting) {
		t.Errorf("ReleaseItem() status = %q, want %q", rresp.Item.Status, QueueStatusWaiting)
	}
}

func TestPeerServerClaimThenDecideRemovesItem(t *testing.T) {
	const (
		shardID    = "shard-peersrv"
		queueName  = "queue-a"
		workitemID = "wi-decide"
		generation = "0000000000000001-abc"
	)

	st := newTestStore(t)
	defer func() { _ = st.Close() }()

	p := NewPeerServer(st, shardID, queueName)
	ctx := context.Background()

	if _, err := p.ApplyItem(ctx, &flowv1.ApplyItemRequest{Item: wireItem(workitemID, generation)}); err != nil {
		t.Fatalf("ApplyItem() error: %v", err)
	}
	if _, err := p.ClaimItem(ctx, &flowv1.ClaimItemRequest{WorkitemId: workitemID}); err != nil {
		t.Fatalf("ClaimItem() error: %v", err)
	}

	dresp, err := p.DecideItem(ctx, &flowv1.DecideItemRequest{WorkitemId: workitemID, Choice: "approve"})
	if err != nil {
		t.Fatalf("DecideItem() error: %v", err)
	}
	if dresp == nil || !dresp.Acknowledged {
		t.Fatalf("DecideItem() response = %+v, want acknowledged", dresp)
	}

	if findItem(t, st, workitemID) {
		t.Errorf("item %q still present after DecideItem; want deleted", workitemID)
	}
}

func TestPeerServerDropItem(t *testing.T) {
	const (
		shardID    = "shard-peersrv"
		queueName  = "queue-a"
		workitemID = "wi-drop"
		generation = "0000000000000002-def"
	)

	st := newTestStore(t)
	defer func() { _ = st.Close() }()

	p := NewPeerServer(st, shardID, queueName)
	ctx := context.Background()

	if _, err := p.ApplyItem(ctx, &flowv1.ApplyItemRequest{Item: wireItem(workitemID, generation)}); err != nil {
		t.Fatalf("ApplyItem() error: %v", err)
	}

	dresp, err := p.DropItem(ctx, &flowv1.DropItemRequest{WorkitemId: workitemID, GenerationId: generation})
	if err != nil {
		t.Fatalf("DropItem() error: %v", err)
	}
	if dresp == nil || !dresp.Acknowledged {
		t.Fatalf("DropItem() response = %+v, want acknowledged", dresp)
	}

	if findItem(t, st, workitemID) {
		t.Errorf("item %q still present after DropItem; want removed", workitemID)
	}
}
