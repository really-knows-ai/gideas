package queue

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// ---------------------------------------------------------------------------
// Promotion-on-shard-death tests (R-C4). Deterministic setup (pinned per the
// determinism rule): enqueue in a 2-shard mesh (owner shard-0, backup = the
// only candidate shard-1), then add shard-2 of the surviving candidate set.
// Promotion is driven by invoking mesh.onShardDead(testShard0) directly.
// ---------------------------------------------------------------------------

// setupPromotion wires: owner shard-0 manager that enqueues the item with its
// backup deterministically on shard-1; shard-1's own mesh (backup holder) with
// shard-2 as the fresh-backup candidate and a peer client for it; and a server
// for shard-0 backed by qm0's store (its stale owner row).
func setupPromotion(t *testing.T) (mesh1 *queueMesh, s1raw, s2 *meshTestShard, gen string) {
	t.Helper()
	ctx := context.Background()

	s1raw = newMeshTestShard(t, testShard1)
	qm0 := buildReplicatingManager(t, testShard0, []registeredShard{{testShard1, s1raw}})
	if err := qm0.Enqueue(ctx, testWorkitemID); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}
	ownerRow, _ := qm0.store.getByID(ctx, testWorkitemID)
	gen = ownerRow.Generation
	if ownerRow.BackupShard != testShard1 {
		t.Fatalf("expected backup on shard-1 (deterministic), got %q", ownerRow.BackupShard)
	}

	// Give shard-0 a server backed by its store so its stale owner row could be
	// surfaced (it is GracefulStop'd before visibility assertions).
	_ = shardServer(t, testShard0, qm0.store, nil)

	// shard-1's own mesh: holds the backup; registry with shard-2 as the fresh
	// candidate; peer client for shard-2.
	s2 = newMeshTestShard(t, testDeadShard)
	mesh1 = newQueueMesh(s1raw.store, testShard1, &staticResolver{}, "50053")
	mesh1.registry = newStaticShardRegistry([]Shard{
		{ID: testShard1, Addr: s1raw.addr},
		{ID: testDeadShard, Addr: s2.addr},
	})
	mesh1.peers[s2.addr] = connectToShard(t, s2)
	// Wire the promotion hook (as Manager.Start does) so tests can drive
	// mesh1.onShardDead directly.
	mesh1.onShardDead = func(dead string) { mesh1.handleShardDead(context.Background(), dead) }

	return mesh1, s1raw, s2, gen
}

func TestPromotion_OwnerShardFlip(t *testing.T) {
	ctx := context.Background()
	mesh1, s1raw, _, gen := setupPromotion(t)

	mesh1.onShardDead(testShard0)

	// The backup row on shard-1 is promoted: shard_id == shard-1.
	row, err := s1raw.store.getByID(ctx, testWorkitemID)
	if err != nil {
		t.Fatalf("getByID failed: %v", err)
	}
	if row.ShardID != testShard1 {
		t.Fatalf("promoted row must be owned by shard-1, got %q", row.ShardID)
	}
	if row.Generation != gen {
		t.Fatalf("promotion must preserve generation %q, got %q", gen, row.Generation)
	}

	// GetGlobalQueue on shard-1's mesh returns one copy owned by shard-1.
	items, err := mesh1.getGlobalQueue(ctx, QueueFilter{})
	if err != nil {
		t.Fatalf("getGlobalQueue failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one copy after promotion, got %+v", items)
	}
	if items[0].ShardID != testShard1 {
		t.Fatalf("expected promoted owner shard-1, got %q", items[0].ShardID)
	}
}

func TestPromotion_SelectsFreshRandomBackup(t *testing.T) {
	ctx := context.Background()
	mesh1, s1raw, s2, _ := setupPromotion(t)
	mesh1.onShardDead(testShard0)

	// Success path records the fresh backup: the promoted shard-1 row's
	// backup_shard == shard-2 (the only living candidate), with a resolvable
	// peer client for it.
	row, err := s1raw.store.getByID(ctx, testWorkitemID)
	if err != nil {
		t.Fatalf("getByID failed: %v", err)
	}
	if row.BackupShard != testDeadShard {
		t.Fatalf("expected fresh backup recorded on shard-2, got %q", row.BackupShard)
	}
	rows := backupRows(t, s2.store)
	if len(rows) != 1 || rows[0].WorkitemID != testWorkitemID {
		t.Fatalf("expected fresh backup replicated to shard-2, got %+v", rows)
	}
}

func TestPromotion_DeferredWhenNoOtherShard(t *testing.T) {
	ctx := context.Background()
	// 2-shard mesh only (no shard-2): promotion on shard-1 finds no fresh
	// backup candidate and defers (backup_shard == ''), item stays visible.
	s1raw := newMeshTestShard(t, testShard1)
	qm0 := buildReplicatingManager(t, testShard0, []registeredShard{{testShard1, s1raw}})
	if err := qm0.Enqueue(ctx, testWorkitemID); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}
	_ = shardServer(t, testShard0, qm0.store, nil)

	mesh1 := newQueueMesh(s1raw.store, testShard1, &staticResolver{}, "50053")
	mesh1.registry = newStaticShardRegistry([]Shard{{ID: testShard1, Addr: s1raw.addr}})
	mesh1.onShardDead = func(dead string) { mesh1.handleShardDead(context.Background(), dead) }
	mesh1.onShardDead(testShard0)

	row, err := s1raw.store.getByID(ctx, testWorkitemID)
	if err != nil {
		t.Fatalf("getByID failed: %v", err)
	}
	if row.ShardID != testShard1 {
		t.Fatalf("promoted row must be owned by shard-1, got %q", row.ShardID)
	}
	if row.BackupShard != "" {
		t.Fatalf("expected deferred backup (''), got %q", row.BackupShard)
	}
	// Item still visible.
	items, err := mesh1.getGlobalQueue(ctx, QueueFilter{})
	if err != nil {
		t.Fatalf("getGlobalQueue failed: %v", err)
	}
	if len(items) != 1 || items[0].WorkitemID != testWorkitemID {
		t.Fatalf("item must remain visible after deferred promotion, got %+v", items)
	}

	// A new shard joining later restores the backup via backfillBackups.
	s2 := newMeshTestShard(t, testDeadShard)
	reg := mesh1.registry.(*staticShardRegistry)
	reg.shards = append(reg.shards, Shard{ID: testDeadShard, Addr: s2.addr})
	reg.byID[testDeadShard] = s2.addr
	mesh1.peers[s2.addr] = connectToShard(t, s2)
	mesh1.backfillBackups(ctx)
	row, _ = s1raw.store.getByID(ctx, testWorkitemID)
	if row.BackupShard != testDeadShard {
		t.Fatalf("expected backfill to place backup on shard-2, got %q", row.BackupShard)
	}
}

func TestPromotion_IgnoresNonBackupRows(t *testing.T) {
	ctx := context.Background()
	// Deterministic: shard-1 holds dead shard-0's backup; shard-2 owns its own
	// rows. Promotion must only touch rows with shard_id == shard-0.
	s1raw := newMeshTestShard(t, testShard1)
	qm0 := buildReplicatingManager(t, testShard0, []registeredShard{{testShard1, s1raw}})
	if err := qm0.Enqueue(ctx, testWorkitemID); err != nil { // backup on shard-1
		t.Fatalf("Enqueue failed: %v", err)
	}
	s2 := newMeshTestShard(t, testDeadShard)
	// shard-2 owns its own row.
	if err := s2.store.enqueue(ctx, "wi-s2", "0000000000000001-s2", ""); err != nil {
		t.Fatal(err)
	}

	mesh1 := newQueueMesh(s1raw.store, testShard1, &staticResolver{}, "50053")
	mesh1.registry = newStaticShardRegistry([]Shard{
		{ID: testShard1, Addr: s1raw.addr},
		{ID: testDeadShard, Addr: s2.addr},
	})
	mesh1.peers[s2.addr] = connectToShard(t, s2)
	mesh1.onShardDead = func(dead string) { mesh1.handleShardDead(context.Background(), dead) }
	mesh1.onShardDead(testShard0)

	// shard-0's backup on shard-1 is promoted.
	row, _ := s1raw.store.getByID(ctx, testWorkitemID)
	if row.ShardID != testShard1 {
		t.Fatalf("promoted row must be owned by shard-1, got %q", row.ShardID)
	}
	// shard-2's own row is untouched.
	own, _ := s2.store.getByID(ctx, "wi-s2")
	if own.ShardID != testDeadShard {
		t.Fatalf("non-backup row on shard-2 must be untouched, got shard_id=%q", own.ShardID)
	}
}

func TestPromotion_DoubleFireIsIdempotent(t *testing.T) {
	ctx := context.Background()
	mesh1, s1raw, s2, _ := setupPromotion(t)

	mesh1.onShardDead(testShard0)
	mesh1.onShardDead(testShard0) // second fire: no rows to promote, no error

	// Factor-1 invariant: exactly one owner row (shard-1) and one backup row
	// (shard-2).
	owners, _, err := s1raw.store.listOwnerRows(ctx, QueueFilter{})
	if err != nil {
		t.Fatalf("listOwnerRows failed: %v", err)
	}
	if len(owners) != 1 || owners[0].WorkitemID != testWorkitemID {
		t.Fatalf("expected exactly one owner row on promoted shard-1, got %+v", owners)
	}
	backups := backupRows(t, s2.store)
	if len(backups) != 1 || backups[0].WorkitemID != testWorkitemID {
		t.Fatalf("expected exactly one backup row (factor 1), got %+v", backups)
	}
}

func TestNotifyShardDead_FiresPromotion(t *testing.T) {
	ctx := context.Background()
	// Production entry point: drive the queuePeerServer.NotifyShardDead RPC on
	// shard-1's wired server (mesh set) and assert the backup row is promoted.
	mesh1, s1raw, _, _ := setupPromotion(t)
	shard1Wired := shardServer(t, testShard1, s1raw.store, mesh1)
	client := connectToShard(t, shard1Wired)

	if _, err := client.NotifyShardDead(ctx, &flowv1.NotifyShardDeadRequest{ShardId: testShard0}); err != nil {
		t.Fatalf("NotifyShardDead failed: %v", err)
	}
	row, err := s1raw.store.getByID(ctx, testWorkitemID)
	if err != nil {
		t.Fatalf("getByID failed: %v", err)
	}
	if row.ShardID != testShard1 {
		t.Fatalf("NotifyShardDead must fire promotion: expected owner shard-1, got %q", row.ShardID)
	}
}
