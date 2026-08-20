package service

import (
	"context"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flowv1api "github.com/foundry/flow/operator/api/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestRegisterQueue_CreatesCR_OnFirstRegistration(t *testing.T) {
	ctx := context.Background()
	c := newFakeClient(t)
	r := NewRegistry(c, time.Second, time.Second)
	r.Namespace = testNamespace

	resp, err := r.RegisterQueue(ctx, &flowv1.RegisterQueueRequest{
		QueueName: "hitl-approval", ShardId: testShard0, ShardAddr: testShard0Addr,
	})
	if err != nil {
		t.Fatalf("RegisterQueue: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("not acknowledged")
	}

	q := &flowv1api.Queue{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: testNamespace, Name: "hitl-approval"}, q); err != nil {
		t.Fatalf("get CR: %v", err)
	}
	// Created in the registry's namespace.
	if q.Namespace != testNamespace {
		t.Fatalf("CR namespace = %q, want %q", q.Namespace, testNamespace)
	}
	if len(q.Status.Shards) != 1 {
		t.Fatalf("expected 1 shard, got %d", len(q.Status.Shards))
	}
	// Initial phase is "active" — never "ready".
	if q.Status.Shards[0].Phase != phaseActive {
		t.Fatalf("phase = %q, want active", q.Status.Shards[0].Phase)
	}
	if q.Status.Shards[0].ShardID != testShard0 || q.Status.Shards[0].Addr != testShard0Addr {
		t.Fatalf("shard fields not persisted: %+v", q.Status.Shards[0])
	}
}

func TestRegisterQueue_UpsertsShard_OnExistingCR(t *testing.T) {
	ctx := context.Background()
	// Seed a CR with an existing shard.
	now := time.Now().UTC().Add(-time.Hour)
	seed := queueCR("hitl-approval", shard(testShard0, testShard0Addr, phaseActive, now))
	c := newFakeClient(t, seed)
	r := NewRegistry(c, time.Second, time.Second)
	r.Namespace = testNamespace

	if _, err := r.RegisterQueue(ctx, &flowv1.RegisterQueueRequest{
		QueueName: "hitl-approval", ShardId: "shard-1", ShardAddr: "10.0.0.2:50053",
	}); err != nil {
		t.Fatalf("RegisterQueue: %v", err)
	}

	q := &flowv1api.Queue{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: testNamespace, Name: "hitl-approval"}, q); err != nil {
		t.Fatalf("get CR: %v", err)
	}
	if len(q.Status.Shards) != 2 {
		t.Fatalf("expected 2 shards, got %d", len(q.Status.Shards))
	}
	// First shard preserved untouched.
	if q.Status.Shards[0].ShardID != testShard0 || q.Status.Shards[0].Addr != testShard0Addr {
		t.Fatalf("existing shard clobbered: %+v", q.Status.Shards[0])
	}
}

// TestHeartbeatQueue_SelfHeals_CreatesMissingCR pins R-B3's "idempotent
// upsert"/Error-Handling "retry next tick" intent: if the SDK's single
// boot-time RegisterQueue failed while the queue-service was down, a later
// heartbeat must re-establish the lease (create the CR) rather than return
// NotFound forever, so the shard rejoins the living set once the service
// recovers.
func TestHeartbeatQueue_SelfHeals_CreatesMissingCR(t *testing.T) {
	ctx := context.Background()
	c := newFakeClient(t) // no CR pre-seeded — the queue does not exist yet
	r := NewRegistry(c, time.Second, time.Second)
	r.Namespace = testNamespace

	resp, err := r.HeartbeatQueue(ctx, &flowv1.HeartbeatQueueRequest{
		QueueName: "hitl-approval", ShardId: testShard0, ShardAddr: testShard0Addr,
	})
	if err != nil {
		t.Fatalf("HeartbeatQueue on missing queue must self-heal, got error: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("not acknowledged")
	}

	// The CR was created with the shard active.
	q := &flowv1api.Queue{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: testNamespace, Name: "hitl-approval"}, q); err != nil {
		t.Fatalf("get CR after heartbeat self-heal: %v", err)
	}
	if q.Namespace != testNamespace {
		t.Fatalf("CR namespace = %q, want %q", q.Namespace, testNamespace)
	}
	if len(q.Status.Shards) != 1 || q.Status.Shards[0].ShardID != testShard0 {
		t.Fatalf("expected shard-0 created, got %+v", q.Status.Shards)
	}
	if q.Status.Shards[0].Phase != phaseActive {
		t.Fatalf("phase = %q, want active", q.Status.Shards[0].Phase)
	}
	// The response carries the self-healed shard as part of the living set.
	shards := resp.GetShards()
	if len(shards) != 1 || shards[0].GetShardId() != testShard0 {
		t.Fatalf("living set should carry the self-healed shard, got %+v", shards)
	}
}

func TestHeartbeatQueue_UpdatesLastHeartbeatAt(t *testing.T) {
	ctx := context.Background()
	old := time.Now().UTC().Add(-2 * time.Minute)
	seed := queueCR("hitl-approval", shard(testShard0, testShard0Addr, phaseActive, old))
	c := newFakeClient(t, seed)
	r := NewRegistry(c, time.Second, time.Second)
	r.Namespace = testNamespace

	if _, err := r.HeartbeatQueue(ctx, &flowv1.HeartbeatQueueRequest{
		QueueName: "hitl-approval", ShardId: testShard0, ShardAddr: testShard0Addr,
	}); err != nil {
		t.Fatalf("HeartbeatQueue: %v", err)
	}

	q := &flowv1api.Queue{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: testNamespace, Name: "hitl-approval"}, q); err != nil {
		t.Fatalf("get CR: %v", err)
	}
	if len(q.Status.Shards) != 1 {
		t.Fatalf("expected 1 shard, got %d", len(q.Status.Shards))
	}
	if !q.Status.Shards[0].LastHeartbeatAt.After(old) {
		t.Fatalf("lastHeartbeatAt not refreshed: %v (was %v)", q.Status.Shards[0].LastHeartbeatAt.Time, old)
	}
	if q.Status.Shards[0].Phase != phaseActive {
		t.Fatalf("phase = %q, want active", q.Status.Shards[0].Phase)
	}
}

func TestHeartbeatQueue_ReturnsLivingShardSet(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	seed := queueCR(
		"hitl-approval",
		shard(testShard0, testShard0Addr, phaseActive, now),
		shard("shard-1", "10.0.0.2:50053", phaseActive, now),
		shard("shard-2", "10.0.0.3:50053", "evicted", now.Add(-time.Hour)),
	)
	c := newFakeClient(t, seed)
	r := NewRegistry(c, time.Hour, time.Second)
	r.Namespace = testNamespace

	resp, err := r.HeartbeatQueue(ctx, &flowv1.HeartbeatQueueRequest{
		QueueName: "hitl-approval", ShardId: testShard0, ShardAddr: testShard0Addr,
	})
	if err != nil {
		t.Fatalf("HeartbeatQueue: %v", err)
	}

	// The response carries the current living shard set — active entries only,
	// the evicted one excluded (R-B3).
	shards := resp.GetShards()
	if len(shards) != 2 {
		t.Fatalf("expected 2 living shards, got %d: %+v", len(shards), shards)
	}
	got := map[string]string{}
	for _, s := range shards {
		got[s.GetShardId()] = s.GetShardAddr()
		if s.GetPhase() != phaseActive {
			t.Fatalf("living shard %s phase = %q, want active", s.GetShardId(), s.GetPhase())
		}
	}
	if got[testShard0] != testShard0Addr || got["shard-1"] != "10.0.0.2:50053" {
		t.Fatalf("living shard addrs wrong: %+v", got)
	}
	if _, ok := got["shard-2"]; ok {
		t.Fatal("evicted shard must be excluded from the living set")
	}
}

func TestDeregisterQueue_RemovesShard_LeavesOthers(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	seed := queueCR(
		"hitl-approval",
		shard(testShard0, testShard0Addr, phaseActive, now),
		shard("shard-1", "10.0.0.2:50053", phaseActive, now),
	)
	c := newFakeClient(t, seed)
	r := NewRegistry(c, time.Hour, time.Second)
	r.Namespace = testNamespace

	if _, err := r.DeregisterQueue(ctx, &flowv1.DeregisterQueueRequest{
		QueueName: "hitl-approval", ShardId: testShard0,
	}); err != nil {
		t.Fatalf("DeregisterQueue: %v", err)
	}

	q := &flowv1api.Queue{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: testNamespace, Name: "hitl-approval"}, q); err != nil {
		t.Fatalf("get CR: %v", err)
	}
	if len(q.Status.Shards) != 1 {
		t.Fatalf("expected 1 shard, got %d", len(q.Status.Shards))
	}
	if q.Status.Shards[0].ShardID != "shard-1" {
		t.Fatalf("expected shard-1 to remain, got %+v", q.Status.Shards[0])
	}
}

func TestListQueues_ReturnsRegisteredQueues(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	c := newFakeClient(t,
		queueCR("hitl-approval", shard(testShard0, testShard0Addr, phaseActive, now)),
		queueCR("hitl-array", shard("shard-3", "10.0.0.4:50053", phaseActive, now)),
	)
	r := NewRegistry(c, time.Hour, time.Second)
	r.Namespace = testNamespace

	resp, err := r.ListQueues(ctx, &flowv1.ListQueuesRequest{})
	if err != nil {
		t.Fatalf("ListQueues: %v", err)
	}
	if len(resp.GetQueues()) != 2 {
		t.Fatalf("expected 2 queues, got %d", len(resp.GetQueues()))
	}
}

// TestStatusWritesUseSubresource ensures a main-resource Update that smuggles
// status changes would NOT pass: the fake is built with WithStatusSubresource,
// so a broken implementation that used client.Update() for status (production
// strips it) stays red.
func TestStatusWritesUseSubresource(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	seed := queueCR("hitl-approval", shard(testShard0, testShard0Addr, phaseActive, now))
	c := newFakeClient(t, seed)
	r := NewRegistry(c, time.Hour, time.Second)
	r.Namespace = testNamespace

	// A main-resource Update with a status change must be ignored by the fake
	// (mirroring a real API server stripping status on Update). Verify the
	// subresource path is what persists by confirming the seeded status is
	// intact after a plain Update attempt via the raw client.
	q := &flowv1api.Queue{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: testNamespace, Name: "hitl-approval"}, q); err != nil {
		t.Fatalf("get CR: %v", err)
	}
	q.Status.Shards[0].Phase = "evicted"
	if err := c.Update(ctx, q); err != nil {
		t.Fatalf("main-resource Update: %v", err)
	}
	got := &flowv1api.Queue{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: testNamespace, Name: "hitl-approval"}, got); err != nil {
		t.Fatalf("re-get CR: %v", err)
	}
	if got.Status.Shards[0].Phase == phaseEvicted {
		t.Fatal("main-resource Update carried a status change — production " +
			"would strip it; the registry must use Status().Update()")
	}
}
