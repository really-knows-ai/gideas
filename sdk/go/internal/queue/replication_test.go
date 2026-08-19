package queue

import (
	"context"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test infrastructure — replicating manager harness (R-C1/R-C2)
// ---------------------------------------------------------------------------

// registeredShard couples a shard's identity to its bufconn test shard.
type registeredShard struct {
	id string
	sh *meshTestShard
}

// buildReplicatingManager wires a Manager for owner id whose mesh has the
// given peers in its static registry, keyed by addr (identity<->addr seam).
// Enqueue-time replication therefore dials peers via registry.AddrFor ->
// peers[addr].
func buildReplicatingManager(t *testing.T, owner string, peers []registeredShard) *Manager {
	t.Helper()
	store, err := newQueueStore(":memory:", owner, testQueueName)
	if err != nil {
		t.Fatalf("newQueueStore(%s) failed: %v", owner, err)
	}
	t.Cleanup(func() { _ = store.close() })

	mesh := newQueueMesh(store, owner, &staticResolver{}, "50053")
	reg := newStaticShardRegistry(nil)
	mesh.registry = reg
	for _, ps := range peers {
		reg.shards = append(reg.shards, Shard{ID: ps.id, Addr: ps.sh.addr})
		reg.byID[ps.id] = ps.sh.addr
		mesh.peers[ps.sh.addr] = connectToShard(t, ps.sh)
	}
	return &Manager{store: store, mesh: mesh, shardID: owner, queueName: testQueueName}
}

// addPeer extends an already-built manager's mesh registry + peers with a new
// shard (used to grow a 2-shard mesh to a 3-shard shape deterministically).
func addPeer(t *testing.T, qm *Manager, id string, sh *meshTestShard) {
	t.Helper()
	reg := qm.mesh.registry.(*staticShardRegistry)
	reg.shards = append(reg.shards, Shard{ID: id, Addr: sh.addr})
	reg.byID[id] = sh.addr
	qm.mesh.peers[sh.addr] = connectToShard(t, sh)
}

// backupRows returns the backup rows on a shard (held for a foreign owner).
func backupRows(t *testing.T, s *queueStore) []QueueItem {
	t.Helper()
	items, _, err := s.listBackups(context.Background(), QueueFilter{})
	if err != nil {
		t.Fatalf("listBackups failed: %v", err)
	}
	return items
}

func TestManager_Enqueue_ReplicatesToOneBackup(t *testing.T) {
	ctx := context.Background()
	s1 := newMeshTestShard(t, testShard1)
	s2 := newMeshTestShard(t, testDeadShard)
	qm := buildReplicatingManager(t, testShard0, []registeredShard{{testShard1, s1}, {testDeadShard, s2}})

	if err := qm.Enqueue(ctx, testWorkitemID); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Local owner row: non-empty generation, backup in {shard-1, shard-2}.
	local, err := qm.store.getByID(ctx, testWorkitemID)
	if err != nil {
		t.Fatalf("getByID local failed: %v", err)
	}
	if local.Generation == "" {
		t.Fatal("expected non-empty generation on owner row")
	}
	if local.BackupShard != testShard1 && local.BackupShard != testDeadShard {
		t.Fatalf("expected backup in {shard-1, shard-2}, got %q", local.BackupShard)
	}
	if local.IsBackup {
		t.Fatal("owner row must not be IsBackup")
	}

	// Exactly one peer holds a backup row for this item (shard_id == shard-0,
	// same generation); the other holds nothing.
	holders := 0
	for _, tbl := range []struct {
		id string
		sh *meshTestShard
	}{{testShard1, s1}, {testDeadShard, s2}} {
		rows := backupRows(t, tbl.sh.store)
		for _, r := range rows {
			if r.WorkitemID == testWorkitemID {
				holders++
				if r.ShardID != testShard0 {
					t.Fatalf("backup on %s has shard_id=%q, want shard-0", tbl.id, r.ShardID)
				}
				if r.Generation != local.Generation {
					t.Fatalf("backup generation=%q, want owner generation %q", r.Generation, local.Generation)
				}
			}
		}
	}
	if holders != 1 {
		t.Fatalf("expected exactly 1 backup holder, got %d", holders)
	}
}

func TestManager_Enqueue_BackupNeverOwner(t *testing.T) {
	ctx := context.Background()
	s1 := newMeshTestShard(t, testShard1)
	s2 := newMeshTestShard(t, testDeadShard)
	qm := buildReplicatingManager(t, testShard0, []registeredShard{{testShard1, s1}, {testDeadShard, s2}})

	for i := range 10 {
		if err := qm.Enqueue(ctx, workitem(i)); err != nil {
			t.Fatalf("Enqueue wi-%d failed: %v", i, err)
		}
		local, err := qm.store.getByID(ctx, workitem(i))
		if err != nil {
			t.Fatalf("getByID failed: %v", err)
		}
		if local.BackupShard == testShard0 {
			t.Fatalf("backup_shard must never equal owner (shard-0), got %q", local.BackupShard)
		}
	}
}

func TestManager_Enqueue_DefersWhenNoLivingPeer(t *testing.T) {
	ctx := context.Background()
	qm := buildReplicatingManager(t, testShard0, nil)

	if err := qm.Enqueue(ctx, testWorkitemID); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}
	local, err := qm.store.getByID(ctx, testWorkitemID)
	if err != nil {
		t.Fatalf("getByID failed: %v", err)
	}
	if local.BackupShard != "" {
		t.Fatalf("expected backup_shard == '' (deferred), got %q", local.BackupShard)
	}
	// Still visible via global queue.
	items, err := qm.GetGlobalQueue(ctx, QueueFilter{})
	if err != nil {
		t.Fatalf("GetGlobalQueue failed: %v", err)
	}
	if len(items) != 1 || items[0].WorkitemID != testWorkitemID {
		t.Fatalf("expected the deferred item visible, got %+v", items)
	}
}

func TestManager_Enqueue_BackfillsWhenPeerJoins(t *testing.T) {
	ctx := context.Background()
	qm := buildReplicatingManager(t, testShard0, nil)
	if err := qm.Enqueue(ctx, testWorkitemID); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// New peer joins: backfill places the backup and records backup_shard.
	s1 := newMeshTestShard(t, testShard1)
	addPeer(t, qm, testShard1, s1)
	qm.mesh.backfillBackups(ctx)

	local, err := qm.store.getByID(ctx, testWorkitemID)
	if err != nil {
		t.Fatalf("getByID failed: %v", err)
	}
	if local.BackupShard != testShard1 {
		t.Fatalf("expected backup_shard == shard-1 after backfill, got %q", local.BackupShard)
	}
	rows := backupRows(t, s1.store)
	if len(rows) != 1 || rows[0].WorkitemID != testWorkitemID {
		t.Fatalf("expected backup placed on shard-1, got %+v", rows)
	}
}

func TestManager_Enqueue_ReplicateFailureContinues(t *testing.T) {
	ctx := context.Background()
	// 2-shard mesh: owner shard-0 + the only candidate shard-1. Deterministic:
	// chooseBackup selects shard-1. GracefulStop its server so the random pick
	// deterministically selects the dead shard-1.
	s1 := newMeshTestShard(t, testShard1)
	qm := buildReplicatingManager(t, testShard0, []registeredShard{{testShard1, s1}})
	s1.srv.GracefulStop()

	if err := qm.Enqueue(ctx, testWorkitemID); err != nil {
		t.Fatalf("Enqueue must succeed even when replicate fails: %v", err)
	}
	local, err := qm.store.getByID(ctx, testWorkitemID)
	if err != nil {
		t.Fatalf("getByID failed: %v", err)
	}
	// Pinned: failed replicate resets backup_shard to '' (backfill-eligible).
	if local.BackupShard != "" {
		t.Fatalf("expected backup_shard == '' after failed replicate, got %q", local.BackupShard)
	}
	// No backup row exists anywhere.
	if rows := backupRows(t, s1.store); len(rows) != 0 {
		t.Fatalf("expected no backup row on shard-1 after failed replicate, got %+v", rows)
	}

	// Restore: a fresh peer (new shard-2) + backfill places the backup.
	// shard-1's server was GracefulStop'd (dead) — mark it dead so backfill's
	// chooseBackup deterministically selects the only living candidate shard-2
	// (per the phase determinism rule; in production the lease-eviction
	// NotifyShardDead path marks it dead).
	qm.mesh.markDead(testShard1)
	s2 := newMeshTestShard(t, testDeadShard)
	addPeer(t, qm, testDeadShard, s2)
	qm.mesh.backfillBackups(ctx)
	local, err = qm.store.getByID(ctx, testWorkitemID)
	if err != nil {
		t.Fatalf("getByID failed: %v", err)
	}
	if local.BackupShard != testDeadShard {
		t.Fatalf("expected backup_shard == shard-2 after backfill, got %q", local.BackupShard)
	}
}

func TestManager_Enqueue_UniqueGenerationPerParkingEvent(t *testing.T) {
	ctx := context.Background()
	s1 := newMeshTestShard(t, testShard1)
	qm := buildReplicatingManager(t, testShard0, []registeredShard{{testShard1, s1}})

	if err := qm.Enqueue(ctx, testWorkitemID); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}
	g1, _ := qm.store.getByID(ctx, testWorkitemID)
	if _, err := qm.store.claim(ctx, testWorkitemID); err != nil {
		t.Fatalf("claim failed: %v", err)
	}
	if _, err := qm.store.decideWithRow(ctx, testWorkitemID); err != nil {
		t.Fatalf("decide failed: %v", err)
	}

	// Re-park the same workitem (2-shard mesh keeps the claim/decide claimed).
	if err := qm.Enqueue(ctx, testWorkitemID); err != nil {
		t.Fatalf("re-Enqueue failed: %v", err)
	}
	g2, _ := qm.store.getByID(ctx, testWorkitemID)
	if g1.Generation == g2.Generation {
		t.Fatal("re-park must produce a different generation")
	}
	if g2.Generation <= g1.Generation {
		t.Fatalf("re-park generation %q must be strictly greater than %q", g2.Generation, g1.Generation)
	}
}

func TestNewGenerationID_IsTimeOrdered(t *testing.T) {
	// ≥1ms sleep between calls -> strictly increasing (lexicographic) strings.
	time.Sleep(2 * time.Millisecond)
	g1 := newGenerationID()
	time.Sleep(2 * time.Millisecond)
	g2 := newGenerationID()
	time.Sleep(2 * time.Millisecond)
	g3 := newGenerationID()
	if g1 >= g2 || g2 >= g3 {
		t.Fatalf("time-ordered generations must increase lexicographically: %q < %q < %q wanted", g1, g2, g3)
	}
	// Fixed-width prefix: the suffix after the hyphen must be 32 hex chars.
	if _, after, ok := strings.Cut(g1, "-"); !ok || len(after) != 32 {
		t.Fatalf("generation %q must carry 32-hex suffix after '-', got %q", g1, after)
	}
}

func workitem(i int) string {
	return "wi-repl-" + string(rune('0'+i))
}
