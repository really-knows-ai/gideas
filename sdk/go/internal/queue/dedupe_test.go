package queue

import (
	"context"
	"testing"
)

// ---------------------------------------------------------------------------
// Tests — scatter-gather dedupe (R-C3): exactly one copy per workitem_id,
// max generation wins (time-ordered, deterministic), owner copy preferred for
// identical generations.
// ---------------------------------------------------------------------------

// collectorMesh builds a mesh on a fresh collector store with the given peers
// registered (identity + addr) and peer clients injected. It is the shard that
// runs getGlobalQueue.
func collectorMesh(t *testing.T, peers []registeredShard) *queueMesh {
	t.Helper()
	store, err := newQueueStore(":memory:", "collector", testQueueName)
	if err != nil {
		t.Fatalf("newQueueStore failed: %v", err)
	}
	t.Cleanup(func() { _ = store.close() })

	mesh := newQueueMesh(store, "collector", &staticResolver{}, "50053")
	reg := newStaticShardRegistry(nil)
	mesh.registry = reg
	for _, ps := range peers {
		reg.shards = append(reg.shards, Shard{ID: ps.id, Addr: ps.sh.addr})
		reg.byID[ps.id] = ps.sh.addr
		mesh.peers[ps.sh.addr] = connectToShard(t, ps.sh)
	}
	return mesh
}

func TestGetGlobalQueue_Dedupes_OwnerPreferred(t *testing.T) {
	ctx := context.Background()

	shard0 := newMeshTestShard(t, testShard0)
	shard1 := newMeshTestShard(t, testShard1)
	const gen = "0000000000000001-suffix"
	// Owner row on shard-0, backup row on shard-1 (same parking event).
	if err := shard0.store.enqueue(ctx, testWorkitemID, gen, testShard1); err != nil {
		t.Fatalf("enqueue owner failed: %v", err)
	}
	if err := shard1.store.insertBackup(ctx, testWorkitemID, testShard0, testQueueName, gen); err != nil {
		t.Fatalf("insertBackup failed: %v", err)
	}

	mesh := collectorMesh(t, []registeredShard{{testShard0, shard0}, {testShard1, shard1}})
	items, err := mesh.getGlobalQueue(ctx, QueueFilter{})
	if err != nil {
		t.Fatalf("getGlobalQueue failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected exactly 1 copy, got %+v", items)
	}
	if items[0].Generation != gen {
		t.Fatalf("expected generation %q, got %q", gen, items[0].Generation)
	}
	if items[0].IsBackup {
		t.Fatal("owner copy must win the dedupe (IsBackup == false)")
	}
}

func TestGetGlobalQueue_Dedupes_BackupReturnedWhenOwnerAbsent(t *testing.T) {
	ctx := context.Background()

	shard0 := newMeshTestShard(t, testShard0)
	shard1 := newMeshTestShard(t, testShard1)
	const gen = "0000000000000001-suffix"
	if err := shard0.store.enqueue(ctx, testWorkitemID, gen, testShard1); err != nil {
		t.Fatalf("enqueue owner failed: %v", err)
	}
	if err := shard1.store.insertBackup(ctx, testWorkitemID, testShard0, testQueueName, gen); err != nil {
		t.Fatalf("insertBackup failed: %v", err)
	}

	// Owner shard-0 is stopped: the collector sees only shard-1's backup.
	shard0.srv.GracefulStop()

	mesh := collectorMesh(t, []registeredShard{{testShard0, shard0}, {testShard1, shard1}})
	items, err := mesh.getGlobalQueue(ctx, QueueFilter{})
	if err != nil {
		t.Fatalf("getGlobalQueue failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected exactly 1 copy (backup), got %+v", items)
	}
	if items[0].ShardID != testShard0 {
		t.Fatalf("backup copy must carry the owner identity shard-0, got %q", items[0].ShardID)
	}
	if !items[0].IsBackup {
		t.Fatal("expected the returned copy to be IsBackup (owner absent)")
	}
}

func TestGetGlobalQueue_StaleGenerationSuperseded(t *testing.T) {
	ctx := context.Background()

	// 2-shard mesh: owner shard-0, backup = the only candidate shard-1. G1.
	s1 := newMeshTestShard(t, testShard1)
	qm := buildReplicatingManager(t, testShard0, []registeredShard{{testShard1, s1}})
	if err := qm.Enqueue(ctx, testWorkitemID); err != nil {
		t.Fatalf("Enqueue G1 failed: %v", err)
	}
	g1, _ := qm.store.getByID(ctx, testWorkitemID)

	// Free the PK at the store level (bypasses the mesh, so no DropItem is
	// sent and shard-1's G1 backup copy stays).
	if _, err := qm.store.claim(ctx, testWorkitemID); err != nil {
		t.Fatalf("claim failed: %v", err)
	}
	if _, err := qm.store.decideWithRow(ctx, testWorkitemID); err != nil {
		t.Fatalf("decide failed: %v", err)
	}

	// Grow to a 3-shard shape: add shard-2, then mark shard-1 dead (its server
	// STAYS UP so the stale G1 stays collectible, but chooseBackup excludes it).
	s2 := newMeshTestShard(t, testDeadShard)
	addPeer(t, qm, testDeadShard, s2)
	qm.mesh.markDead(testShard1)

	// Re-park at G2 (deterministic: shard-2 is the only candidate; G2 > G1 by
	// time-ordering). The G2 backup lands on shard-2 (a DIFFERENT shard than
	// the stale G1 on shard-1 — insertBackup is not an upsert).
	if err := qm.Enqueue(ctx, testWorkitemID); err != nil {
		t.Fatalf("re-Enqueue G2 failed: %v", err)
	}
	g2, _ := qm.store.getByID(ctx, testWorkitemID)
	if g2.Generation <= g1.Generation {
		t.Fatalf("G2=%q must be strictly greater than G1=%q", g2.Generation, g1.Generation)
	}
	if g2.BackupShard != testDeadShard {
		t.Fatalf("expected G2 backup on shard-2, got %q", g2.BackupShard)
	}

	// Scatter-gather sees three copies; exactly one survives with generation G2.
	items, err := qm.GetGlobalQueue(ctx, QueueFilter{})
	if err != nil {
		t.Fatalf("GetGlobalQueue failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected exactly 1 copy after stale-supersede, got %+v", items)
	}
	if items[0].Generation != g2.Generation {
		t.Fatalf("expected surviving copy generation %q (G2), got %q", g2.Generation, items[0].Generation)
	}
}

func TestGetGlobalQueue_OneCopyPerWorkitem(t *testing.T) {
	ctx := context.Background()

	shard0 := newMeshTestShard(t, testShard0)
	shard1 := newMeshTestShard(t, testShard1)
	shard2 := newMeshTestShard(t, testDeadShard)

	// workitem A: owner on shard-0 (new gen), stale backup on shard-1 (old gen),
	// second backup on shard-2 (new gen).
	const genA = "0000000000000002-old"
	const genAnew = "0000000000000003-new"
	if err := shard0.store.enqueue(ctx, "wi-a", genA, testShard1); err != nil {
		t.Fatal(err)
	}
	if err := shard1.store.insertBackup(ctx, "wi-a", testShard0, testQueueName, genA); err != nil {
		t.Fatal(err)
	}
	if err := shard2.store.insertBackup(ctx, "wi-a", testShard0, testQueueName, genAnew); err != nil {
		t.Fatal(err)
	}
	// workitem B: single owner on shard-1.
	if err := shard1.store.enqueue(ctx, "wi-b", "0000000000000001-b", ""); err != nil {
		t.Fatal(err)
	}

	mesh := collectorMesh(t, []registeredShard{
		{testShard0, shard0}, {testShard1, shard1}, {testDeadShard, shard2},
	})
	items, err := mesh.getGlobalQueue(ctx, QueueFilter{})
	if err != nil {
		t.Fatalf("getGlobalQueue failed: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected exactly 2 copies (one per workitem), got %+v", items)
	}
	got := map[string]string{}
	for _, it := range items {
		got[it.WorkitemID] = it.Generation
	}
	if got["wi-a"] != genAnew {
		t.Fatalf("wi-a should keep the max generation %q, got %q", genAnew, got["wi-a"])
	}
	if got["wi-b"] != "0000000000000001-b" {
		t.Fatalf("wi-b should keep its lone copy, got %q", got["wi-b"])
	}
}
