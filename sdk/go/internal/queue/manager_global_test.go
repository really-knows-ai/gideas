package queue

import (
	"context"
	"net"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// ---------------------------------------------------------------------------
// Tests — HITL QueueManager: global queue & cross-shard behavior
// ---------------------------------------------------------------------------

func TestQueueManager_GlobalQueue_MultiShard(t *testing.T) {
	ctx := context.Background()

	// Create two managers with separate stores.
	store0, err := newQueueStore(":memory:", "mgr-0", "")
	if err != nil {
		t.Fatalf("store0 failed: %v", err)
	}
	t.Cleanup(func() { _ = store0.close() })

	store1, err := newQueueStore(":memory:", "mgr-1", "")
	if err != nil {
		t.Fatalf("store1 failed: %v", err)
	}
	t.Cleanup(func() { _ = store1.close() })

	// Enqueue items on each store.
	_ = store0.enqueue(ctx, "wi-0", "", "")
	_ = store1.enqueue(ctx, "wi-1", "", "")

	// Set up mesh for store0 with store1 as a peer via bufconn.
	shard1 := newMeshTestShard(t, "mgr-1")
	_ = shard1.store.enqueue(ctx, "wi-peer", "", "")

	mesh0 := newQueueMesh(store0, "mgr-0", &staticResolver{}, "50053")
	mesh0.peers[shard1.addr] = connectToShard(t, shard1)

	qm0 := &Manager{
		store:   store0,
		mesh:    mesh0,
		shardID: "mgr-0",
	}

	items, err := qm0.GetGlobalQueue(ctx, QueueFilter{})
	if err != nil {
		t.Fatalf("GetGlobalQueue failed: %v", err)
	}

	// Should have items from local (wi-0) + peer shard.
	if len(items) < 2 {
		t.Fatalf("expected at least 2 items from global queue, got %d", len(items))
	}
}

func TestQueueManager_WaitForDecision_CrossShard(t *testing.T) {
	ctx := context.Background()

	// --- Pod A: the owning shard that enqueues and waits. ---
	storeA, err := newQueueStore(":memory:", "shard-A", "")
	if err != nil {
		t.Fatalf("storeA failed: %v", err)
	}
	t.Cleanup(func() { _ = storeA.close() })

	qmA := &Manager{
		store:   storeA,
		shardID: "shard-A",
	}

	// Create the peer server with the onDecide callback wired to qmA.decisions.
	peerA := &queuePeerServer{
		store: storeA,
		onDecide: func(workitemID, choice string) {
			if ch, ok := qmA.decisions.LoadAndDelete(workitemID); ok {
				ch.(chan string) <- choice
			}
		},
	}

	// Start a bufconn gRPC server for Pod A.
	lisA := bufconn.Listen(1024 * 1024)
	srvA := grpc.NewServer()
	flowv1.RegisterQueuePeerServiceServer(srvA, peerA)
	go func() { _ = srvA.Serve(lisA) }()
	t.Cleanup(func() { srvA.GracefulStop() })

	// Wire qmA's mesh (no peers from A's perspective — it's the local shard).
	meshA := newQueueMesh(storeA, "shard-A", &staticResolver{}, "50053")
	qmA.mesh = meshA

	// --- Pod B: the remote shard that receives the decide request. ---
	storeB, err := newQueueStore(":memory:", "shard-B", "")
	if err != nil {
		t.Fatalf("storeB failed: %v", err)
	}
	t.Cleanup(func() { _ = storeB.close() })

	// Pod B's mesh can reach Pod A via bufconn.
	meshB := newQueueMesh(storeB, "shard-B", &staticResolver{}, "50053")
	connA, err := grpc.NewClient(
		"passthrough:///shard-A",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lisA.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to connect to shard-A: %v", err)
	}
	t.Cleanup(func() { _ = connA.Close() })
	meshB.peers["passthrough:///shard-A"] = flowv1.NewQueuePeerServiceClient(connA)

	qmB := &Manager{
		store:   storeB,
		mesh:    meshB,
		shardID: "shard-B",
	}

	// --- Test: Pod A enqueues, Pod B decides, Pod A's WaitForDecision unblocks. ---

	// Pod A enqueues and claims.
	if err := qmA.Enqueue(ctx, "wi-cross"); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}
	if _, err := qmA.Claim(ctx, "wi-cross"); err != nil {
		t.Fatalf("Claim failed: %v", err)
	}

	// Pod A starts waiting.
	waitErr := make(chan error, 1)
	go func() {
		_, err := qmA.WaitForDecision(ctx, "wi-cross")
		waitErr <- err
	}()

	// Give WaitForDecision time to enter the select.
	time.Sleep(50 * time.Millisecond)

	// Pod B decides — this will route through the mesh to Pod A's gRPC server,
	// which calls onDecide and sends on the channel.
	if err := qmB.Decide(ctx, "wi-cross", ""); err != nil {
		t.Fatalf("remote Decide failed: %v", err)
	}

	// Pod A's WaitForDecision should unblock.
	select {
	case err := <-waitErr:
		if err != nil {
			t.Fatalf("WaitForDecision returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitForDecision did not unblock within 5s after cross-shard Decide")
	}
}
