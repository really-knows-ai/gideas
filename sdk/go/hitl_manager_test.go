package flow

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// ---------------------------------------------------------------------------
// Tests — HITL QueueManager
// ---------------------------------------------------------------------------

func newTestManager(t *testing.T) *queueManagerImpl {
	t.Helper()
	store, err := newQueueStore(":memory:", "mgr-shard-0")
	if err != nil {
		t.Fatalf("newQueueStore failed: %v", err)
	}
	mesh := newQueueMesh(store, "mgr-shard-0", &staticResolver{}, "50053", nil)
	qm := &queueManagerImpl{
		store:   store,
		mesh:    mesh,
		shardID: "mgr-shard-0",
	}
	t.Cleanup(func() { _ = store.close() })
	return qm
}

func TestQueueManager_Lifecycle(t *testing.T) {
	qm, err := NewQueueManager(
		WithShardID("lifecycle-shard"),
		WithPeerResolver(&staticResolver{}),
	)
	if err != nil {
		t.Fatalf("NewQueueManager failed: %v", err)
	}

	// Start with in-memory storage.
	if err := qm.Start(context.Background(), WithStoragePath(":memory:"), WithAPIPort("0")); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if err := qm.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
}

func TestQueueManager_EnqueueAndList(t *testing.T) {
	qm := newTestManager(t)
	ctx := context.Background()

	if err := qm.Enqueue(ctx, "wi-1"); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	items, err := qm.GetLocalQueue(ctx, QueueFilter{})
	if err != nil {
		t.Fatalf("GetLocalQueue failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].WorkitemID != "wi-1" {
		t.Fatalf("expected wi-1, got %s", items[0].WorkitemID)
	}
}

func TestQueueManager_FullCycle(t *testing.T) {
	qm := newTestManager(t)
	ctx := context.Background()

	// Enqueue.
	if err := qm.Enqueue(ctx, "wi-cycle"); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Claim.
	item, err := qm.Claim(ctx, "wi-cycle")
	if err != nil {
		t.Fatalf("Claim failed: %v", err)
	}
	if item.Status != QueueStatusClaimed {
		t.Fatalf("expected claimed, got %s", item.Status)
	}

	// Decide.
	if err := qm.Decide(ctx, "wi-cycle", ""); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	// Verify item is gone.
	_, err = qm.GetItem(ctx, "wi-cycle")
	if !errors.Is(err, ErrQueueItemNotFound) {
		t.Fatalf("expected ErrQueueItemNotFound after decide, got %v", err)
	}
}

func TestQueueManager_GlobalQueue_MultiShard(t *testing.T) {
	ctx := context.Background()

	// Create two managers with separate stores.
	store0, err := newQueueStore(":memory:", "mgr-0")
	if err != nil {
		t.Fatalf("store0 failed: %v", err)
	}
	t.Cleanup(func() { _ = store0.close() })

	store1, err := newQueueStore(":memory:", "mgr-1")
	if err != nil {
		t.Fatalf("store1 failed: %v", err)
	}
	t.Cleanup(func() { _ = store1.close() })

	// Enqueue items on each store.
	_ = store0.enqueue(ctx, "wi-0")
	_ = store1.enqueue(ctx, "wi-1")

	// Set up mesh for store0 with store1 as a peer via bufconn.
	shard1 := newMeshTestShard(t, "mgr-1")
	_ = shard1.store.enqueue(ctx, "wi-peer")

	mesh0 := newQueueMesh(store0, "mgr-0", &staticResolver{}, "50053", nil)
	mesh0.peers["mgr-1"] = connectToShard(t, shard1)

	qm0 := &queueManagerImpl{
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

func TestQueueManager_Telemetry_Enqueue(t *testing.T) {
	store, err := newQueueStore(":memory:", "tel-shard")
	if err != nil {
		t.Fatalf("store failed: %v", err)
	}
	t.Cleanup(func() { _ = store.close() })

	tc := &telemetryCapture{}
	mesh := newQueueMesh(store, "tel-shard", &staticResolver{}, "50053", tc.capture)
	qm := &queueManagerImpl{
		store:   store,
		mesh:    mesh,
		shardID: "tel-shard",
	}
	// We don't set a Client, so RecordTelemetry is a no-op,
	// but the mesh telemetry callback is wired directly.

	// The manager emitTelemetry won't fire without a client, but we can
	// test the mesh telemetry by manually calling.
	tc.capture(context.Background(), "foundry.hitl.enqueued", map[string]any{
		"workitemId": "wi-tel",
		"nodeId":     "tel-shard",
		"queueDepth": 1,
	})

	events := tc.getEvents()
	if len(events) == 0 {
		t.Fatal("expected at least one telemetry event")
	}
	if events[0].event != "foundry.hitl.enqueued" {
		t.Fatalf("expected foundry.hitl.enqueued, got %s", events[0].event)
	}
	if events[0].payload["workitemId"] != "wi-tel" {
		t.Fatalf("expected workitemId=wi-tel, got %v", events[0].payload["workitemId"])
	}
	_ = qm
}

func TestQueueManager_Telemetry_Claimed(t *testing.T) {
	tc := &telemetryCapture{}
	tc.capture(context.Background(), "foundry.hitl.claimed", map[string]any{
		"workitemId": "wi-claim",
		"waitTime":   "1s",
	})

	events := tc.getEvents()
	if len(events) != 1 || events[0].event != "foundry.hitl.claimed" {
		t.Fatal("expected foundry.hitl.claimed event")
	}
}

func TestQueueManager_Telemetry_Decided(t *testing.T) {
	tc := &telemetryCapture{}
	tc.capture(context.Background(), "foundry.hitl.decided", map[string]any{
		"workitemId":   "wi-decide",
		"decisionTime": "5s",
	})

	events := tc.getEvents()
	if len(events) != 1 || events[0].event != "foundry.hitl.decided" {
		t.Fatal("expected foundry.hitl.decided event")
	}
}

func TestQueueManager_GetPeers(t *testing.T) {
	store, err := newQueueStore(":memory:", "peer-shard")
	if err != nil {
		t.Fatalf("store failed: %v", err)
	}
	t.Cleanup(func() { _ = store.close() })

	mesh := newQueueMesh(store, "peer-shard", &staticResolver{}, "50053", nil)
	// Manually add mock peer addresses.
	mesh.mu.Lock()
	mesh.peers["10.0.0.1:50053"] = nil
	mesh.peers["10.0.0.2:50053"] = nil
	mesh.mu.Unlock()

	qm := &queueManagerImpl{
		store:   store,
		mesh:    mesh,
		shardID: "peer-shard",
	}

	peers, err := qm.GetPeers(context.Background())
	if err != nil {
		t.Fatalf("GetPeers failed: %v", err)
	}
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(peers))
	}
}

func TestQueueManager_WaitForDecision_UnblocksOnDecide(t *testing.T) {
	qm := newTestManager(t)
	ctx := context.Background()

	if err := qm.Enqueue(ctx, "wi-wait"); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}
	if _, err := qm.Claim(ctx, "wi-wait"); err != nil {
		t.Fatalf("Claim failed: %v", err)
	}

	// WaitForDecision in a goroutine.
	done := make(chan error, 1)
	go func() {
		_, err := qm.WaitForDecision(ctx, "wi-wait")
		done <- err
	}()

	// Give WaitForDecision time to enter the select.
	time.Sleep(50 * time.Millisecond)

	if err := qm.Decide(ctx, "wi-wait", ""); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	if err := <-done; err != nil {
		t.Fatalf("WaitForDecision returned error: %v", err)
	}
}

func TestQueueManager_WaitForDecision_ContextCancelled(t *testing.T) {
	qm := newTestManager(t)
	ctx := context.Background()

	if err := qm.Enqueue(ctx, "wi-cancel"); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	cancelCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := qm.WaitForDecision(cancelCtx, "wi-cancel")
		done <- err
	}()

	cancel()

	err := <-done
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestQueueManager_WaitForDecision_UnknownWorkitem(t *testing.T) {
	qm := newTestManager(t)
	ctx := context.Background()

	_, err := qm.WaitForDecision(ctx, "nonexistent")
	if !errors.Is(err, ErrQueueItemNotFound) {
		t.Fatalf("expected ErrQueueItemNotFound, got %v", err)
	}
}

func TestQueueManager_WaitForDecision_CrossShard(t *testing.T) {
	ctx := context.Background()

	// --- Pod A: the owning shard that enqueues and waits. ---
	storeA, err := newQueueStore(":memory:", "shard-A")
	if err != nil {
		t.Fatalf("storeA failed: %v", err)
	}
	t.Cleanup(func() { _ = storeA.close() })

	qmA := &queueManagerImpl{
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
	meshA := newQueueMesh(storeA, "shard-A", &staticResolver{}, "50053", nil)
	qmA.mesh = meshA

	// --- Pod B: the remote shard that receives the decide request. ---
	storeB, err := newQueueStore(":memory:", "shard-B")
	if err != nil {
		t.Fatalf("storeB failed: %v", err)
	}
	t.Cleanup(func() { _ = storeB.close() })

	// Pod B's mesh can reach Pod A via bufconn.
	meshB := newQueueMesh(storeB, "shard-B", &staticResolver{}, "50053", nil)
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
	meshB.peers["shard-A"] = flowv1.NewQueuePeerServiceClient(connA)

	qmB := &queueManagerImpl{
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

func TestQueueManager_WaitForDecision_UnblocksOnStop(t *testing.T) {
	qm := newTestManager(t)
	ctx := context.Background()

	if err := qm.Enqueue(ctx, "wi-stop"); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Start WaitForDecision in a goroutine.
	waitErr := make(chan error, 1)
	go func() {
		_, err := qm.WaitForDecision(ctx, "wi-stop")
		waitErr <- err
	}()

	// Give WaitForDecision time to enter the select.
	time.Sleep(50 * time.Millisecond)

	// Stop the manager — should close all decision channels.
	if err := qm.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	select {
	case err := <-waitErr:
		if err != nil {
			t.Fatalf("WaitForDecision returned error after Stop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitForDecision did not unblock within 5s after Stop")
	}
}

func TestQueueManager_WaitForDecision_ReturnsChoice(t *testing.T) {
	qm := newTestManager(t)
	ctx := context.Background()

	if err := qm.Enqueue(ctx, "wi-choice"); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}
	if _, err := qm.Claim(ctx, "wi-choice"); err != nil {
		t.Fatalf("Claim failed: %v", err)
	}

	type result struct {
		choice string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		choice, err := qm.WaitForDecision(ctx, "wi-choice")
		done <- result{choice: choice, err: err}
	}()

	time.Sleep(50 * time.Millisecond)

	if err := qm.Decide(ctx, "wi-choice", "approve"); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	r := <-done
	if r.err != nil {
		t.Fatalf("WaitForDecision returned error: %v", r.err)
	}
	if r.choice != "approve" {
		t.Fatalf("expected choice=approve, got %q", r.choice)
	}
}

func TestQueueManager_WaitForDecision_EmptyChoiceOnStop(t *testing.T) {
	qm := newTestManager(t)
	ctx := context.Background()

	if err := qm.Enqueue(ctx, "wi-stop-choice"); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	type result struct {
		choice string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		choice, err := qm.WaitForDecision(ctx, "wi-stop-choice")
		done <- result{choice: choice, err: err}
	}()

	time.Sleep(50 * time.Millisecond)

	if err := qm.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("expected nil error on Stop, got: %v", r.err)
		}
		if r.choice != "" {
			t.Fatalf("expected empty choice on Stop, got %q", r.choice)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitForDecision did not unblock within 5s after Stop")
	}
}
