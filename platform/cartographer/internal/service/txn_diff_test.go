package service

import (
	"errors"
	"testing"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestCommitTransaction_DeleteThenRecreateSameID_Survives pins the commit
// path's final-state semantics for delete-then-recreate of the same explicit
// entity ID within one transaction. The map-based change log records both a
// ChangeDelEntity and a ChangeAddEntity for the ID; the commit path writes
// added files before removing deleted ones, so without same-ID resolution the
// recreated <id>.json would be written then removed — the committed tree (and
// main, which is re-hydrated from the tree per SPEC R9 commit step 8) would
// lack the entity while the branch LadybugDB's final state still holds it:
// silent data loss. The recreated entity must survive both the git tree and
// main.
func TestCommitTransaction_DeleteThenRecreateSameID_Survives(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	// Main holds the entity at begin — a delete requires an existing entity.
	commitGitEntity(ctx, t, gs, testMutationEntityID, "original")
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{
		Id: testMutationEntityID, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("DeleteEntity: %v", err)
	}
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Id: testMutationEntityID, Properties: map[string]string{"name": "recreated"},
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("recreate entity: %v", err)
	}
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CommitTransaction: %v", err)
	}
	mainEnt, err := base.GetEntity(ctx, testMutationEntityID, "main")
	if err != nil {
		t.Fatalf("recreated entity lost from main after commit: %v", err)
	}
	if mainEnt.Properties["name"] != "recreated" {
		t.Fatalf("expected the recreated entity content on main, got %+v", mainEnt.Properties)
	}
	files, err := gs.ReadAllEntityFiles(ctx, "Component")
	if err != nil {
		t.Fatalf("ReadAllEntityFiles: %v", err)
	}
	if len(files) != 1 || files[0].ID != testMutationEntityID || files[0].Properties["name"] != "recreated" {
		t.Fatalf("expected the recreated <id>.json to survive in the committed tree, got %+v", files)
	}
}

// TestCommitTransaction_CreateThenDeleteSameID_LeavesNoTrace pins the other
// half of the same-ID ambiguity: create-then-delete within one transaction is
// a net no-op and must commit with no entity on main and no file in the
// committed tree — the same-ID resolution must not resurrect the deleted
// entity.
func TestCommitTransaction_CreateThenDeleteSameID_LeavesNoTrace(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Id: testMutationEntityID, Properties: map[string]string{"name": "created"},
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if _, err = srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{
		Id: testMutationEntityID, TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("DeleteEntity: %v", err)
	}
	if _, err = srv.CommitTransaction(ctx, &flowv1.CommitTransactionRequest{
		TransactionId: begin.TransactionId,
	}); err != nil {
		t.Fatalf("CommitTransaction: %v", err)
	}
	if _, err := base.GetEntity(ctx, testMutationEntityID, "main"); !errors.Is(err, store.ErrEntityNotFound) {
		t.Fatalf("expected create-then-delete to leave no entity on main, got %v", err)
	}
	files, err := gs.ReadAllEntityFiles(ctx, "Component")
	if err != nil {
		t.Fatalf("ReadAllEntityFiles: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected no entity files in the committed tree after create-then-delete, got %+v", files)
	}
}

// TestGetTransactionDiff_NetZeroElementNotBothAddedAndDeleted pins the SPEC R9
// "Diff() reads the change log" wire consistency with the commit path's
// final-state semantics (resolveSameIDSequences, conversions.go). The change
// log is operation-based and keeps an ID that a transaction both creates and
// deletes in both the added and deleted buckets; without the same same-ID
// final-state reduction, GetTransactionDiff would report a net-zero element in
// both added_entities and deleted_entities. It must appear in at most one
// bucket: create-then-delete (absent final state) in deleted only,
// delete-then-recreate (present final state) in added only.
func TestGetTransactionDiff_NetZeroElementNotBothAddedAndDeleted(t *testing.T) {
	base, err := openTestStore(t)
	if err != nil {
		t.Fatalf("openTestStore: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	ladybugPath := t.TempDir()
	gs, err := gitstore.New(ladybugPath)
	if err != nil {
		t.Fatalf("gitstore.New: %v", err)
	}
	opPub, _ := generateTestKey()
	srv := NewCartographerServer(
		base, gs, opPub, initTestKey(), nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000,
		WithLadybugPath(ladybugPath),
	)
	srv.MarkDBReady()
	ctx := testCtx()
	applyTestSchema(ctx, t, base)
	// Main holds an entity so the delete-then-recreate branch resolves present.
	commitGitEntity(ctx, t, gs, testMutationEntityID, "original")

	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	txID := begin.TransactionId

	// Create-then-delete a fresh entity: net absent.
	createdID := "11111111-2222-4333-8444-555555555555"
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Id: createdID, Properties: map[string]string{"name": "created"},
		TransactionId: txID,
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if _, err = srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{
		Id: createdID, TransactionId: txID,
	}); err != nil {
		t.Fatalf("DeleteEntity: %v", err)
	}

	// Delete-then-recreate an existing entity: net present.
	if _, err = srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{
		Id: testMutationEntityID, TransactionId: txID,
	}); err != nil {
		t.Fatalf("DeleteEntity: %v", err)
	}
	if _, err = srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Id: testMutationEntityID, Properties: map[string]string{"name": "recreated"},
		TransactionId: txID,
	}); err != nil {
		t.Fatalf("recreate entity: %v", err)
	}

	diff, err := srv.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{TransactionId: txID})
	if err != nil {
		t.Fatalf("GetTransactionDiff: %v", err)
	}
	added := make(map[string]bool, len(diff.AddedEntities))
	for _, e := range diff.AddedEntities {
		added[e.Id] = true
	}
	for _, e := range diff.DeletedEntities {
		if added[e.Id] {
			t.Fatalf("net-zero entity %q reported as both added and deleted", e.Id)
		}
	}
	// Create-then-delete resolves to absent: reported deleted only.
	var createdDeleted bool
	for _, e := range diff.DeletedEntities {
		if e.Id == createdID {
			createdDeleted = true
		}
	}
	if !createdDeleted {
		t.Fatalf("expected create-then-delete entity %q in deleted_entities, added=%v", createdID, added)
	}
	if added[createdID] {
		t.Fatalf("create-then-delete entity %q must not be in added_entities", createdID)
	}
	// Delete-then-recreate resolves to present: reported added only.
	if !added[testMutationEntityID] {
		t.Fatalf("expected delete-then-recreate entity %q in added_entities, added=%v", testMutationEntityID, added)
	}
	for _, e := range diff.DeletedEntities {
		if e.Id == testMutationEntityID {
			t.Fatalf("delete-then-recreate entity %q must not be in deleted_entities", testMutationEntityID)
		}
	}
}

func TestGetTransactionDiff_WrongCapability(t *testing.T) {
	opPub, _ := generateTestKey()
	scPub, scPriv := generateTestKey()
	st, _ := openTestStore(t)
	t.Cleanup(func() { _ = st.Close() })
	gs, _ := gitstore.New(t.TempDir())
	srv := NewCartographerServer(st, gs, opPub, scPub, nil, "", 30*time.Second, "test-ns", 30*time.Minute, 100000)

	// Capabilities missing READ:graph/tx.
	ctx := capabilityContext("WRITE:graph/entity/*,WRITE:graph/tx", scPriv, "sidecar")

	_, err := srv.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{
		TransactionId: "11111111-1111-4111-8111-111111111111",
	})
	if err == nil {
		t.Fatal("expected error for wrong capability, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

// TestGetTransactionDiff_LiveDeletePopulatesDeletionBuckets pins SPEC R2
// GetTransactionDiff's deleted_entities / deleted_edges wire fields for a live
// (non-recovery) transaction: after a DeleteEntity and a DeleteEdge inside an
// open transaction, the RPC response must carry the deleted entities and edges
// in their respective buckets, populated with the payload captured at deletion
// time (properties for the entity, endpoints for the edge). All prior
// assertions on these buckets came from the recovery path (suspected
// deletions), so a regression that stopped populating them for a normal delete
// would previously fail no test.
func TestGetTransactionDiff_LiveDeletePopulatesDeletionBuckets(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()
	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	svc, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Service", Properties: map[string]string{"name": "svc"}, TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEntity svc: %v", err)
	}
	comp, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "core"}, TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEntity comp: %v", err)
	}
	edge, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType: "DEPENDS_ON", FromEntityId: svc.EntityId, ToEntityId: comp.EntityId,
		Properties: map[string]string{"weight": "medium"}, TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEdge: %v", err)
	}
	// A standalone DeleteEdge must populate deleted_edges with its endpoints.
	if _, err := srv.DeleteEdge(ctx, &flowv1.DeleteEdgeRequest{Id: edge.EdgeId, TransactionId: txID}); err != nil {
		t.Fatalf("DeleteEdge: %v", err)
	}
	// Deleting the entity populates deleted_entities with its payload.
	if _, err := srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{Id: svc.EntityId, TransactionId: txID}); err != nil {
		t.Fatalf("DeleteEntity: %v", err)
	}

	diff, err := srv.GetTransactionDiff(ctx, &flowv1.GetTransactionDiffRequest{TransactionId: txID})
	if err != nil {
		t.Fatalf("GetTransactionDiff: %v", err)
	}
	if len(diff.DeletedEntities) != 1 || diff.DeletedEntities[0].Id != svc.EntityId {
		t.Fatalf("expected the deleted entity in deleted_entities, got %+v", diff.DeletedEntities)
	}
	if diff.DeletedEntities[0].Suspected {
		t.Fatal("live deletion must not be marked suspected")
	}
	if diff.DeletedEntities[0].Properties["name"] != "svc" {
		t.Fatalf("deleted entity payload dropped: %+v", diff.DeletedEntities[0].Properties)
	}
	if len(diff.DeletedEdges) != 1 || diff.DeletedEdges[0].Id != edge.EdgeId {
		t.Fatalf("expected the deleted edge in deleted_edges, got %+v", diff.DeletedEdges)
	}
	if diff.DeletedEdges[0].Suspected {
		t.Fatal("live deletion must not be marked suspected")
	}
	if diff.DeletedEdges[0].FromEntityId != svc.EntityId || diff.DeletedEdges[0].ToEntityId != comp.EntityId {
		t.Fatalf(
			"deleted edge endpoints dropped: from=%q to=%q",
			diff.DeletedEdges[0].FromEntityId, diff.DeletedEdges[0].ToEntityId,
		)
	}
	if diff.DeletedEdges[0].Properties["weight"] != "medium" {
		t.Fatalf("deleted edge payload dropped: %+v", diff.DeletedEdges[0].Properties)
	}
}
