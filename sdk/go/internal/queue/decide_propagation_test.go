package queue

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// ---------------------------------------------------------------------------
// Test infrastructure — wired shard servers (mesh-enabled DecideItem /
// NotifyShardDead) for decide-propagation tests.
// ---------------------------------------------------------------------------

// shardServer builds a bufconn QueuePeerService server around store with an
// optional mesh (nil => raw). Returns a meshTestShard wired to it.
func shardServer(t *testing.T, id string, store *queueStore, mesh *queueMesh) *meshTestShard {
	t.Helper()
	if store == nil {
		var err error
		store, err = newQueueStore(":memory:", id, testQueueName)
		if err != nil {
			t.Fatalf("newQueueStore(%s): %v", id, err)
		}
		t.Cleanup(func() { _ = store.close() })
	}
	lis := bufconn.Listen(meshBufSize)
	srv := grpc.NewServer()
	flowv1.RegisterQueuePeerServiceServer(srv, &queuePeerServer{store: store, mesh: mesh})
	go func() { _ = srv.Serve(lis) }()
	addr := "bufconn://" + id
	t.Cleanup(func() { srv.GracefulStop() })
	return &meshTestShard{
		store: store, srv: srv, lis: lis, addr: addr,
		dialer: func(context.Context, string) (net.Conn, error) {
			return lis.DialContext(context.TODO())
		},
	}
}

// failingDropShard builds a bufconn shard whose server fails only the DropItem
// method (simulating a partition mid-drop) — ReplicateItem/GetLocalQueue still
// work, so the stale backup copy stays visible.
func failingDropShard(t *testing.T, id string) *meshTestShard {
	t.Helper()
	store, err := newQueueStore(":memory:", id, testQueueName)
	if err != nil {
		t.Fatalf("newQueueStore(%s): %v", id, err)
	}
	t.Cleanup(func() { _ = store.close() })
	lis := bufconn.Listen(meshBufSize)
	srv := grpc.NewServer(grpc.UnaryInterceptor(func(
		ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
	) (any, error) {
		if strings.HasSuffix(info.FullMethod, "/DropItem") {
			return nil, status.Error(codes.FailedPrecondition, "simulated partition drop failure")
		}
		return handler(ctx, req)
	}))
	flowv1.RegisterQueuePeerServiceServer(srv, &queuePeerServer{store: store})
	go func() { _ = srv.Serve(lis) }()
	addr := "bufconn://" + id
	t.Cleanup(func() { srv.GracefulStop() })
	return &meshTestShard{
		store: store, srv: srv, lis: lis, addr: addr,
		dialer: func(context.Context, string) (net.Conn, error) {
			return lis.DialContext(context.TODO())
		},
	}
}

// ---------------------------------------------------------------------------
// Decide propagation (R-C5 / R-C6)
// ---------------------------------------------------------------------------

func TestDecide_DeletesOwnerRowAndDropsBackup(t *testing.T) {
	ctx := context.Background()
	// 2-shard mesh: owner shard-0, backup = the only random candidate shard-1.
	s1 := newMeshTestShard(t, testShard1)
	qm := buildReplicatingManager(t, testShard0, []registeredShard{{testShard1, s1}})

	if err := qm.Enqueue(ctx, testWorkitemID); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}
	if _, err := qm.Claim(ctx, testWorkitemID); err != nil {
		t.Fatalf("Claim failed: %v", err)
	}
	// Local routeDecide path: deletes the owner row AND drops the backup.
	if err := qm.Decide(ctx, testWorkitemID, ""); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}
	if _, err := qm.store.getByID(ctx, testWorkitemID); err == nil {
		t.Fatal("owner row must be deleted")
	}
	if rows := backupRows(t, s1.store); len(rows) != 0 {
		t.Fatalf("backup row must be dropped on the backup shard, got %+v", rows)
	}
}

//nolint:dupl // parallel to BackupHolderFallsThrough test; distinct behavior (remote routing vs backup-holder)
func TestDecide_RemoteRouting_StillPropagatesDrop(t *testing.T) {
	ctx := context.Background()
	// Owner shard-0 + backup shard-1. Enqueue replicates to shard-1.
	s1 := newMeshTestShard(t, testShard1)
	qm0 := buildReplicatingManager(t, testShard0, []registeredShard{{testShard1, s1}})
	if err := qm0.Enqueue(ctx, testWorkitemID); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}
	if _, err := qm0.Claim(ctx, testWorkitemID); err != nil {
		t.Fatalf("Claim on owner failed: %v", err)
	}

	// The owner's reachable server must have mesh wired so its DecideItem
	// propagates the drop (remote path).
	shard0Wired := shardServer(t, testShard0, qm0.store, qm0.mesh)

	// Deciding manager shard-2 provably holds no row: clean findOwner -> owner's
	// DecideItem (drop propagation exercised on the queuePeerServer path).
	qm2 := buildReplicatingManager(t, testDeadShard, []registeredShard{{testShard0, shard0Wired}})
	if err := qm2.Decide(ctx, testWorkitemID, ""); err != nil {
		t.Fatalf("remote Decide failed: %v", err)
	}
	if _, err := qm0.store.getByID(ctx, testWorkitemID); err == nil {
		t.Fatal("owner row must be deleted by the remote decide")
	}
	if rows := backupRows(t, s1.store); len(rows) != 0 {
		t.Fatalf("backup row must be dropped, got %+v", rows)
	}
}

// TestGetItem_BackupHolderDoesNotServeBackupCopy pins the R-C6 owner-only read
// rule on the LOCAL branch of routeGetItem: a shard that holds a backup row for
// a workitem must not serve that backup copy via GetItem — it is "not mine" and
// falls through (here to not-found, since the holder has no peers to route to).
func TestGetItem_BackupHolderDoesNotServeBackupCopy(t *testing.T) {
	ctx := context.Background()
	s0 := newMeshTestShard(t, testShard0)
	s1 := newMeshTestShard(t, testShard1)
	const gen = "0000000000000001-gi"
	if err := s0.store.enqueue(ctx, testWorkitemID, gen, testShard1); err != nil {
		t.Fatal(err)
	}
	if err := s1.store.insertBackup(ctx, testWorkitemID, testShard0, testQueueName, gen); err != nil {
		t.Fatal(err)
	}

	// The backup-holder shard-1 has no peers: its local branch must not return
	// its held backup row (ShardID == shard-0, not self). R-C6 -> not found.
	qm1 := buildReplicatingManager(t, testShard1, nil)
	if _, err := qm1.mesh.routeGetItem(ctx, testWorkitemID); !errors.Is(err, ErrQueueItemNotFound) {
		t.Fatalf("backup holder must not serve its backup copy via GetItem; got %v", err)
	}
}

func TestClaim_BackupHolderRoutesToOwner(t *testing.T) {
	ctx := context.Background()
	s0 := newMeshTestShard(t, testShard0)
	s1 := newMeshTestShard(t, testShard1)
	const gen = "0000000000000001-cl"
	if err := s0.store.enqueue(ctx, testWorkitemID, gen, testShard1); err != nil {
		t.Fatal(err)
	}
	if err := s1.store.insertBackup(ctx, testWorkitemID, testShard0, testQueueName, gen); err != nil {
		t.Fatal(err)
	}

	// The backup-holder manager (shard-1) claims: localOwnerRow treats its
	// backup row as "not mine" -> findOwner -> shard-0 ClaimItem.
	qm1 := buildReplicatingManager(t, testShard1, []registeredShard{{testShard0, s0}})
	item, err := qm1.Claim(ctx, testWorkitemID)
	if err != nil {
		t.Fatalf("Claim failed: %v", err)
	}
	if item.ShardID != testShard0 {
		t.Fatalf("claim must land on the owner row (shard-0), got %q", item.ShardID)
	}
	// The local backup row is untouched (still waiting, not double-claimed).
	backup, err := s1.store.getByID(ctx, testWorkitemID)
	if err != nil {
		t.Fatalf("getByID backup failed: %v", err)
	}
	if backup.Status != QueueStatusWaiting {
		t.Fatalf("backup row must remain waiting (no double claim), got %s", backup.Status)
	}
}

//nolint:dupl // parallel to RemoteRouting test; distinct behavior (backup-holder vs remote routing)
func TestDecide_BackupHolderFallsThroughToOwner(t *testing.T) {
	ctx := context.Background()
	s1 := newMeshTestShard(t, testShard1)
	qm0 := buildReplicatingManager(t, testShard0, []registeredShard{{testShard1, s1}})
	if err := qm0.Enqueue(ctx, testWorkitemID); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}
	if _, err := qm0.Claim(ctx, testWorkitemID); err != nil {
		t.Fatalf("Claim on owner failed: %v", err)
	}
	shard0Wired := shardServer(t, testShard0, qm0.store, qm0.mesh)

	// The manager that merely holds the backup row decides: ownership guard
	// (not invalid-state) -> findOwner -> owner's DecideItem.
	qm1 := buildReplicatingManager(t, testShard1, []registeredShard{{testShard0, shard0Wired}})
	if err := qm1.Decide(ctx, testWorkitemID, ""); err != nil {
		t.Fatalf("Decide from backup-holder failed: %v", err)
	}
	if _, err := qm0.store.getByID(ctx, testWorkitemID); err == nil {
		t.Fatal("owner row must be deleted")
	}
	if rows := backupRows(t, s1.store); len(rows) != 0 {
		t.Fatalf("backup row must be dropped, got %+v", rows)
	}
}

func TestRelease_BackupHolderRoutesToOwner(t *testing.T) {
	ctx := context.Background()
	s0 := newMeshTestShard(t, testShard0)
	s1 := newMeshTestShard(t, testShard1)
	const gen = "0000000000000001-rl"
	if err := s0.store.enqueue(ctx, testWorkitemID, gen, testShard1); err != nil {
		t.Fatal(err)
	}
	if err := s1.store.insertBackup(ctx, testWorkitemID, testShard0, testQueueName, gen); err != nil {
		t.Fatal(err)
	}
	// Owner claims its row.
	if _, err := s0.store.claim(ctx, testWorkitemID); err != nil {
		t.Fatal(err)
	}

	// Backup-holder manager releases: ownership guard -> owner's ReleaseItem.
	qm1 := buildReplicatingManager(t, testShard1, []registeredShard{{testShard0, s0}})
	item, err := qm1.Release(ctx, testWorkitemID)
	if err != nil {
		t.Fatalf("Release failed: %v", err)
	}
	if item.Status != QueueStatusWaiting {
		t.Fatalf("owner row must return to waiting, got %s", item.Status)
	}
	backup, err := s1.store.getByID(ctx, testWorkitemID)
	if err != nil {
		t.Fatalf("getByID backup failed: %v", err)
	}
	if backup.Status != QueueStatusWaiting {
		t.Fatalf("backup row must stay untouched (waiting), got %s", backup.Status)
	}
}

func TestDecide_MissedDrop_LeavesStaleCopyPutIgnored(t *testing.T) {
	ctx := context.Background()
	// shard-1's server errors only DropItem (partition); ReplicateItem works.
	s1 := failingDropShard(t, testShard1)
	qm := buildReplicatingManager(t, testShard0, []registeredShard{{testShard1, s1}})

	// Enqueue G1, claim, decide on the owner -> owner row deleted; the
	// propagateDrop -> DropItem fails (warn); G1 stays on shard-1 AND visible.
	if err := qm.Enqueue(ctx, testWorkitemID); err != nil {
		t.Fatalf("Enqueue G1 failed: %v", err)
	}
	g1, _ := qm.store.getByID(ctx, testWorkitemID)
	if g1.BackupShard != testShard1 {
		t.Fatalf("expected G1 backup on shard-1, got %q", g1.BackupShard)
	}
	if _, err := qm.Claim(ctx, testWorkitemID); err != nil {
		t.Fatalf("Claim failed: %v", err)
	}
	if err := qm.Decide(ctx, testWorkitemID, ""); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}
	// G1 owner row gone; G1 stale backup still on shard-1 (drop failed).
	if _, err := qm.store.getByID(ctx, testWorkitemID); err == nil {
		t.Fatal("owner row must be deleted")
	}
	if rows := backupRows(t, s1.store); len(rows) != 1 || rows[0].WorkitemID != testWorkitemID {
		t.Fatalf("stale G1 backup must remain (missed drop), got %+v", rows)
	}

	// Re-park at G2 on a grown mesh: add shard-2 and mark shard-1 dead (its
	// server stays UP so the stale G1 stays collectible, but chooseBackup now
	// excludes it). shard-2 is the only candidate => ReplicateItem succeeds and
	// G2 backs up on a DIFFERENT shard than stale G1.
	s2 := newMeshTestShard(t, testDeadShard)
	addPeer(t, qm, testDeadShard, s2)
	qm.mesh.markDead(testShard1)
	if err := qm.Enqueue(ctx, testWorkitemID); err != nil {
		t.Fatalf("re-Enqueue G2 failed: %v", err)
	}
	g2, _ := qm.store.getByID(ctx, testWorkitemID)
	if g2.BackupShard != testDeadShard {
		t.Fatalf("expected G2 backup on shard-2, got %q", g2.BackupShard)
	}

	items, err := qm.GetGlobalQueue(ctx, QueueFilter{})
	if err != nil {
		t.Fatalf("GetGlobalQueue failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected only G2 visible (stale G1 superseded by generation), got %+v", items)
	}
	if items[0].Generation != g2.Generation {
		t.Fatalf("expected surviving copy generation %q (G2), got %q", g2.Generation, items[0].Generation)
	}
}

func TestDecide_RoutesToPromotedOwner(t *testing.T) {
	ctx := context.Background()
	// Deterministic: enqueue in 2-shard (owner shard-0, backup = shard-1).
	s1raw := newMeshTestShard(t, testShard1)
	qm0 := buildReplicatingManager(t, testShard0, []registeredShard{{testShard1, s1raw}})
	if err := qm0.Enqueue(ctx, testWorkitemID); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}
	g1, _ := qm0.store.getByID(ctx, testWorkitemID)
	if g1.BackupShard != testShard1 {
		t.Fatalf("expected backup on shard-1, got %q", g1.BackupShard)
	}

	// shard-1's own mesh: it holds the backup and, once promoted, becomes owner.
	// Give it a registry with shard-2 as the fresh-backup candidate and a peer
	// client for shard-2 so replicateAndRecord + dropBackup work.
	s2 := newMeshTestShard(t, testDeadShard)
	mesh1 := newQueueMesh(s1raw.store, testShard1, &staticResolver{}, "50053")
	mesh1.registry = newStaticShardRegistry([]Shard{
		{ID: testShard1, Addr: s1raw.addr},
		{ID: testDeadShard, Addr: s2.addr},
	})
	mesh1.peers[s2.addr] = connectToShard(t, s2)
	mesh1.onShardDead = func(dead string) { mesh1.handleShardDead(context.Background(), dead) }
	mesh1.onShardDead(testShard0) // promote shard-0's backup on shard-1

	// shard-1's reachable server with mesh wired so DecideItem drops the fresh
	// backup (remote propagation path).
	shard1Wired := shardServer(t, testShard1, s1raw.store, mesh1)

	// Claim on the promoted owner (shard-1). The promoted row inherits
	// status=='waiting' from the backup row.
	qm1 := &Manager{store: s1raw.store, mesh: mesh1, shardID: testShard1}
	if _, err := qm1.Claim(ctx, testWorkitemID); err != nil {
		t.Fatalf("Claim on promoted owner failed: %v", err)
	}

	// Decide from shard-2 routes (owner-row-only findOwner) to promoted shard-1.
	qm2 := buildReplicatingManager(t, testDeadShard, []registeredShard{{testShard1, shard1Wired}})
	if err := qm2.Decide(ctx, testWorkitemID, ""); err != nil {
		t.Fatalf("Decide to promoted owner failed: %v", err)
	}
	// Promoted owner's row deleted.
	if _, err := s1raw.store.getByID(ctx, testWorkitemID); err == nil {
		t.Fatal("promoted owner row must be deleted")
	}
	// Fresh backup on shard-2 dropped via the promoted owner's DecideItem.
	if rows := backupRows(t, s2.store); len(rows) != 0 {
		t.Fatalf("fresh backup on shard-2 must be dropped, got %+v", rows)
	}
}

func TestProxyWrite_ToStoppedOwner_ErrShardUnavailable(t *testing.T) {
	ctx := context.Background()
	s0 := newMeshTestShard(t, testShard0)
	const gen = "0000000000000001-pw"
	if err := s0.store.enqueue(ctx, testWorkitemID, gen, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s0.store.claim(ctx, testWorkitemID); err != nil {
		t.Fatal(err)
	}

	// Caller shard-1 resolves the owner's peer client via findOwner while shard-0
	// is still live.
	qm1 := buildReplicatingManager(t, testShard1, []registeredShard{{testShard0, s0}})
	client, err := qm1.mesh.findOwner(ctx, testWorkitemID)
	if err != nil {
		t.Fatalf("findOwner against live owner failed: %v", err)
	}

	// The owner dies MID-request: stop its server, then proxy the write on the
	// already-resolved client.
	s0.srv.GracefulStop()
	_, err = client.ClaimItem(ctx, &flowv1.ClaimItemRequest{WorkitemId: testWorkitemID})
	if err == nil {
		t.Fatal("expected error proxying to a stopped owner")
	}
	if got := mapGRPCError(err); !errors.Is(got, ErrShardUnavailable) {
		t.Fatalf("expected ErrShardUnavailable (503), got %v", got)
	}
}
