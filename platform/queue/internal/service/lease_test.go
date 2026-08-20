package service

import (
	"context"
	"sync"
	"testing"
	"time"

	flowv1api "github.com/foundry/flow/operator/api/v1"
	"google.golang.org/grpc"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestLeaseSweep_EvictsStaleShard(t *testing.T) {
	ctx := context.Background()
	// TTL 1s; seed a shard whose heartbeat is 5s in the past (stale) and one
	// fresh shard.
	now := time.Now().UTC()
	seed := queueCR(
		"hitl-approval",
		shard(testShard0, "10.0.0.1:50053", "active", now.Add(-5*time.Second)),
		shard("shard-1", "10.0.0.2:50053", "active", now),
	)
	c := newFakeClient(t, seed)
	r := NewRegistry(c, time.Second, time.Second)
	r.Namespace = testNamespace
	r.peerDialer = failDialer

	if err := r.sweepEvictions(ctx); err != nil {
		t.Fatalf("sweepEvictions: %v", err)
	}

	q := &flowv1api.Queue{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: testNamespace, Name: "hitl-approval"}, q); err != nil {
		t.Fatalf("get CR: %v", err)
	}
	if len(q.Status.Shards) != 1 {
		t.Fatalf("expected 1 shard to remain, got %d: %+v", len(q.Status.Shards), q.Status.Shards)
	}
	if q.Status.Shards[0].ShardID != "shard-1" {
		t.Fatalf("expected shard-1 to survive, got %+v", q.Status.Shards[0])
	}
}

// recordingClient wraps a client and snapshots every Status().Update payload
// so the two-step eviction transition (mark evicted, then drop) can be pinned.
type recordingClient struct {
	client.Client
	mu           sync.Mutex
	statusWrites []*flowv1api.Queue
}

func (r *recordingClient) Status() client.SubResourceWriter {
	return &recordingSubResource{parent: r}
}

type recordingSubResource struct {
	client.SubResourceWriter
	parent *recordingClient
}

func (s *recordingSubResource) Update(
	ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption,
) error {
	q := obj.(*flowv1api.Queue).DeepCopy()
	s.parent.mu.Lock()
	s.parent.statusWrites = append(s.parent.statusWrites, q)
	s.parent.mu.Unlock()
	return s.parent.Client.Status().Update(ctx, obj, opts...)
}

func TestLeaseSweep_MarksEvictedThenDrops(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	seed := queueCR(
		"hitl-approval",
		shard(testShard0, "10.0.0.1:50053", "active", now.Add(-5*time.Second)),
		shard("shard-1", "10.0.0.2:50053", "active", now),
	)
	base := newFakeClient(t, seed)
	rec := &recordingClient{Client: base}
	r := NewRegistry(rec, time.Second, time.Second)
	r.Namespace = testNamespace
	r.peerDialer = failDialer

	if err := r.sweepEvictions(ctx); err != nil {
		t.Fatalf("sweepEvictions: %v", err)
	}

	if len(rec.statusWrites) < 2 {
		t.Fatalf("expected ≥2 status writes (mark + drop), got %d", len(rec.statusWrites))
	}
	// First status write: the stale shard is marked phase=evicted.
	first := rec.statusWrites[0]
	foundEvicted := false
	for _, s := range first.Status.Shards {
		if s.ShardID == testShard0 {
			if s.Phase != phaseEvicted {
				t.Fatalf("first status write: shard-0 phase = %q, want evicted (template for the enum transition)", s.Phase)
			}
			foundEvicted = true
		}
	}
	if !foundEvicted {
		t.Fatal("first status write did not carry the stale shard as evicted")
	}
	// Final status write no longer contains the stale shard.
	final := rec.statusWrites[len(rec.statusWrites)-1]
	for _, s := range final.Status.Shards {
		if s.ShardID == testShard0 {
			t.Fatalf("final status write still contains evicted shard-0: %+v", final.Status.Shards)
		}
	}
}

func TestLeaseSweep_KeepsFreshShard(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	seed := queueCR(
		"hitl-approval",
		shard(testShard0, "10.0.0.1:50053", "active", now),
		shard("shard-1", "10.0.0.2:50053", "active", now.Add(-200*time.Millisecond)),
	)
	c := newFakeClient(t, seed)
	// A large TTL makes both clearly fresh, immune to metav1 sub-second
	// truncation on the write.
	r := NewRegistry(c, time.Hour, time.Second)
	r.Namespace = testNamespace
	r.peerDialer = failDialer

	if err := r.sweepEvictions(ctx); err != nil {
		t.Fatalf("sweepEvictions: %v", err)
	}
	q := &flowv1api.Queue{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: testNamespace, Name: "hitl-approval"}, q); err != nil {
		t.Fatalf("get CR: %v", err)
	}
	if len(q.Status.Shards) != 2 {
		t.Fatalf("expected both fresh shards to survive, got %d", len(q.Status.Shards))
	}
}

func TestQueue_WithNoShards_SurvivesSweep(t *testing.T) {
	ctx := context.Background()
	seed := queueCR("hitl-approval")
	c := newFakeClient(t, seed)
	r := NewRegistry(c, time.Second, time.Second)
	r.Namespace = testNamespace

	if err := r.sweepEvictions(ctx); err != nil {
		t.Fatalf("sweepEvictions: %v", err)
	}
	// Registry record stays in place.
	q := &flowv1api.Queue{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: testNamespace, Name: "hitl-approval"}, q); err != nil {
		t.Fatalf("empty registry record must survive the sweep, got err: %v", err)
	}
}

func TestEviction_BroadcastsOnShardEvicted(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	seed := queueCR(
		"hitl-approval",
		shard(testShard0, "10.0.0.1:50053", "active", now.Add(-5*time.Second)),
		shard("shard-1", "10.0.0.2:50053", "active", now),
	)
	c := newFakeClient(t, seed)
	r := NewRegistry(c, time.Second, time.Second)
	r.Namespace = testNamespace
	r.peerDialer = failDialer

	var gotQ, gotS string
	var mu sync.Mutex
	r.OnShardEvicted = func(queueName, shardID string) {
		mu.Lock()
		gotQ, gotS = queueName, shardID
		mu.Unlock()
	}

	if err := r.sweepEvictions(ctx); err != nil {
		t.Fatalf("sweepEvictions: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotQ != "hitl-approval" || gotS != testShard0 {
		t.Fatalf("OnShardEvicted fired (%q, %q), want (hitl-approval, shard-0)", gotQ, gotS)
	}
}

func TestEviction_FansOutNotifyShardDead(t *testing.T) {
	ctx := context.Background()
	// Three shards: X stale (⇒ evicted), Y + Z fresh survivors. Wire bufconn
	// fakes for Y and Z that record NotifyShardDead payloads.
	now := time.Now().UTC()
	survivorY := newFakePeerShard(t)
	survivorZ := newFakePeerShard(t)

	seed := queueCR(
		"hitl-approval",
		shard("shard-X", "10.0.0.9:50053", "active", now.Add(-10*time.Second)),
		shard("shard-Y", "say:y", "active", now),
		shard("shard-Z", "saz:z", "active", now),
	)
	c := newFakeClient(t, seed)
	r := NewRegistry(c, time.Second, time.Second)
	r.Namespace = testNamespace

	// Use a dialer that routes bufconn addrs to the right fake. Record which
	// addrs were dialed to assert X's address was never dialed.
	var dialed []string
	var mu sync.Mutex
	r.peerDialer = func(cctx context.Context, addr string) (*grpc.ClientConn, error) {
		mu.Lock()
		dialed = append(dialed, addr)
		mu.Unlock()
		switch addr {
		case "say:y":
			return survivorY.dialer(cctx, addr)
		case "saz:z":
			return survivorZ.dialer(cctx, addr)
		}
		return nil, errShardUnavailable
	}

	if err := r.sweepEvictions(ctx); err != nil {
		t.Fatalf("sweepEvictions: %v", err)
	}

	// Each surviving peer received NotifyShardDead(X) exactly once.
	for _, f := range []*fakePeerShard{survivorY, survivorZ} {
		notified := f.notifiedCalls()
		if len(notified) != 1 || notified[0] != "shard-X" {
			t.Fatalf("survivor received %v, want exactly [shard-X]", notified)
		}
	}
	// The dead shard X's address was never dialed.
	mu.Lock()
	defer mu.Unlock()
	for _, a := range dialed {
		if a == "10.0.0.9:50053" {
			t.Fatal("dead shard X's address was dialed during the fan-out")
		}
	}
}

func TestDefaultLeaseTTL_IsThreeHeartbeatIntervals(t *testing.T) {
	if DefaultQueueLeaseTTL != 3*15*time.Second {
		t.Fatalf("DefaultQueueLeaseTTL = %v, want 45s (3 × 15s heartbeat interval)", DefaultQueueLeaseTTL)
	}
}
