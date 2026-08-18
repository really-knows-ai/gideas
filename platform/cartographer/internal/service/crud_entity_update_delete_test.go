package service

import (
	"math"
	"reflect"
	"strings"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestUpdateEntity_SingleTypeSpecificCapabilityPasses pins SPEC R3
// (SPEC:242): a caller holding only WRITE:graph/entity/<type> (plus
// WRITE:graph/tx to begin the transaction) is authorised for an UpdateEntity
// of that type through the Cartographer's authoritative per-type check. The
// positive per-type branch is pinned for CreateEntity elsewhere; without this
// test a handler regression that required the wildcard for UpdateEntity would
// go undetected.
func TestUpdateEntity_SingleTypeSpecificCapabilityPasses(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := narrowCtx("WRITE:graph/entity/Component", "WRITE:graph/tx")

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	ent, err := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "original"}, nil, txID)
	if err != nil {
		t.Fatalf("seed entity: %v", err)
	}

	resp, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id:            ent.Id,
		Properties:    map[string]string{"version": "2"},
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("UpdateEntity with per-type capability failed: %v", err)
	}
	if resp.Properties["version"] != "2" {
		t.Fatalf("expected version=2, got %q", resp.Properties["version"])
	}
}

// TestDeleteEntity_SingleTypeSpecificCapabilityPasses pins SPEC R3
// (SPEC:242): a caller holding only WRITE:graph/entity/<type> (plus
// WRITE:graph/tx) is authorised for a DeleteEntity of that type through the
// Cartographer's authoritative per-type check.
func TestDeleteEntity_SingleTypeSpecificCapabilityPasses(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := narrowCtx("WRITE:graph/entity/Component", "WRITE:graph/tx")

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	ent, err := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "delete-me"}, nil, txID)
	if err != nil {
		t.Fatalf("seed entity: %v", err)
	}

	resp, err := srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{Id: ent.Id, TransactionId: txID})
	if err != nil {
		t.Fatalf("DeleteEntity with per-type capability failed: %v", err)
	}
	if resp.EntityId != ent.Id {
		t.Fatalf("expected deleted entity ID %q, got %q", ent.Id, resp.EntityId)
	}
}

func TestUpdateEntity_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	_, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id:            "11111111-1111-4111-8111-111111111111",
		Properties:    map[string]string{"name": "x"},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for not-found entity, got nil")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", status.Code(err))
	}
}

func TestUpdateEntity_Valid(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	ent, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "original"}, nil, txID)

	resp, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id:            ent.Id,
		Properties:    map[string]string{"version": "2"},
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("UpdateEntity failed: %v", err)
	}
	if resp.Properties["version"] != "2" {
		t.Fatalf("expected version=2, got %q", resp.Properties["version"])
	}
}

func TestDeleteEntity_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	_, err := srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{
		Id:            "11111111-1111-4111-8111-111111111111",
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for not-found entity, got nil")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", status.Code(err))
	}
}

func TestDeleteEntity_InvalidUUID(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	_, err := srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{Id: "not-a-uuid", TransactionId: txID})
	if err == nil {
		t.Fatal("expected error for invalid UUID, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestDeleteEntity_Valid(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	ent, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "delete-me"}, nil, txID)

	resp, err := srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{Id: ent.Id, TransactionId: txID})
	if err != nil {
		t.Fatalf("DeleteEntity failed: %v", err)
	}
	if resp.EntityId != ent.Id {
		t.Fatalf("expected deleted entity ID %q, got %q", ent.Id, resp.EntityId)
	}
}

// TestDeleteEntity_ReturnsEmbedding pins the DeleteEntityResponse wire
// contract (proto/flow/v1/cartographer.proto: DeleteEntityResponse.embedding):
// the handler must populate the embedding field from the store's
// read-before-delete result instead of silently dropping it, so SDK callers
// reading GetEmbedding() receive the deleted entity's stored vector.
func TestDeleteEntity_ReturnsEmbedding(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := testCtx()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VectorType",
				EnableVectorIndex: true,
				Properties:        []*flowv1.Property{{Name: "name", Type: "string"}},
			},
		},
	}
	if err := st.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	txID := beginTestTx(t, srv, ctx)
	ent, _ := srv.store.CreateEntity(ctx, "VectorType", "", map[string]string{"name": "a"}, []float32{0.1, 0.2, 0.3}, txID)

	resp, err := srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{Id: ent.Id, TransactionId: txID})
	if err != nil {
		t.Fatalf("DeleteEntity failed: %v", err)
	}
	if len(resp.Embedding) != 3 ||
		resp.Embedding[0] != 0.1 || resp.Embedding[2] != 0.3 {
		t.Fatalf("expected embedding [0.1 0.2 0.3] on deleted entity, got %v", resp.Embedding)
	}
}

// TestDeleteEntity_TransactionRecordsCascadeEdgeDeletion verifies SPEC R7 §4
// atomicity is preserved across a commit: deleting an entity inside a
// transaction must also record the cascade-removed edges in the change log so
// commit serialisation removes their git files. Without this, committed main
// retains edges pointing at a deleted entity.
func TestDeleteEntity_TransactionRecordsCascadeEdgeDeletion(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)

	begin, err := srv.BeginTransaction(ctx, &flowv1.BeginTransactionRequest{})
	if err != nil {
		t.Fatalf("BeginTransaction: %v", err)
	}
	txID := begin.TransactionId

	// Create the participating entities and edge inside the transaction so they
	// exist on the branch DB (the in-memory branch is not seeded from main).
	svcResp, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Service", Properties: map[string]string{"name": "svc"}, TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEntity svc: %v", err)
	}
	svc := svcResp.EntityId
	compResp, err := srv.CreateEntity(ctx, &flowv1.CreateEntityRequest{
		EntityType: "Component", Properties: map[string]string{"name": "core"}, TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEntity comp: %v", err)
	}
	comp := compResp.EntityId
	edgeResp, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType: "DEPENDS_ON", FromEntityId: svc, ToEntityId: comp, TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEdge: %v", err)
	}

	// Delete the service entity inside the transaction; the DEPENDS_ON edge it
	// participates in is cascade-removed by the store.
	_, err = srv.DeleteEntity(ctx, &flowv1.DeleteEntityRequest{Id: svc, TransactionId: txID})
	if err != nil {
		t.Fatalf("transactional DeleteEntity: %v", err)
	}

	// The deleted edge must be present in the change log as a ChangeDelEdge.
	state, err := srv.txManager.Lookup(txID)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if _, ok := state.ChangeLog.DeletedEdges[edgeResp.EdgeId]; !ok {
		t.Fatalf("expected cascade-deleted edge %q in change log, got %+v", edgeResp.EdgeId, state.ChangeLog.DeletedEdges)
	}
	if _, ok := state.ChangeLog.DeletedEntities[svc]; !ok {
		t.Fatalf("expected deleted entity %q in change log", svc)
	}
}

// TestUpdateEntity_EmbeddingRewriteSuccess drives UpdateEntity on an
// established vector-indexed row and asserts the embedding rewrite succeeds:
// the store drops the vector index, writes the new embedding, and recreates the
// index (SPEC R2/R7 — a dimension-matching, NaN-free embedding update is
// accepted; only dimension mismatch and NaN/Inf are rejections). The response
// carries the persisted embedding.
func TestUpdateEntity_EmbeddingRewriteSuccess(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VecType",
				EnableVectorIndex: true,
				Properties:        []*flowv1.Property{{Name: "name", Type: "string"}},
			},
		},
	}
	if err := srv.store.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	txID := beginTestTx(t, srv, ctx)
	// Bootstrap a 3-dim vector index via CreateEntity with an embedding.
	ent, err := srv.store.CreateEntity(
		ctx, "VecType", "", map[string]string{"name": "seeded"}, []float32{1.0, 0.0, 0.0}, txID,
	)
	if err != nil {
		t.Fatalf("bootstrap CreateEntity: %v", err)
	}
	resp, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id:            ent.Id,
		Embedding:     []float32{0.0, 1.0, 0.0},
		Properties:    map[string]string{"name": "updated"},
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("matching-dimension embedding update must succeed, got %v", err)
	}
	if !reflect.DeepEqual(resp.Embedding, []float32{0.0, 1.0, 0.0}) {
		t.Fatalf("expected rewritten embedding [0 1 0], got %v", resp.Embedding)
	}
	if resp.Properties["name"] != "updated" {
		t.Fatalf("expected name=updated, got %q", resp.Properties["name"])
	}
}

func TestUpdateEntity_UnknownProperty(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	ent, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "test"}, nil, txID)

	_, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id:            ent.Id,
		Properties:    map[string]string{"nonexistent": "value"},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for unknown property, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestUpdateEntity_InvalidIDFormat(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)

	_, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id:            "not-a-uuid",
		Properties:    map[string]string{"name": "x"},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for invalid ID format, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestUpdateEntity_NaNEmbedding(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := testCtx()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VecType",
				EnableVectorIndex: true,
				Properties:        []*flowv1.Property{{Name: "name", Type: "string"}},
			},
		},
	}
	if err := st.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	txID := beginTestTx(t, srv, ctx)
	ent, _ := srv.store.CreateEntity(ctx, "VecType", "", map[string]string{"name": "a"}, []float32{1.0, 0.0, 0.0}, txID)

	_, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id:            ent.Id,
		Properties:    map[string]string{"name": "b"},
		Embedding:     []float32{float32(math.NaN()), 0.0, 0.0},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for NaN embedding, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

// TestUpdateEntity_NaNEmbeddingNonIndexed pins SPEC R7: the NaN/Inf embedding
// rejection applies "regardless of indexing status" — a non-indexed entity
// type must still reject a NaN/Inf embedding at the service layer (the store's
// NaN guard runs unconditionally, before any EnableVectorIndex branching).
func TestUpdateEntity_NaNEmbeddingNonIndexed(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	// Component is a non-indexed type (enableVectorIndex not set).
	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	ent, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "a"}, nil, txID)

	_, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id:            ent.Id,
		Properties:    map[string]string{"name": "b"},
		Embedding:     []float32{float32(math.NaN()), 0.0, 0.0},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for NaN embedding on non-indexed type, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v (%v)", status.Code(err), err)
	}
	if !strings.Contains(err.Error(), "NaN") {
		t.Fatalf("expected the NaN-embedding rejection, got %v", err)
	}
}

func TestUpdateEntity_EmbeddingDimensionMismatch(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := testCtx()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:              "VecType",
				EnableVectorIndex: true,
				Properties:        []*flowv1.Property{{Name: "name", Type: "string"}},
			},
		},
	}
	if err := st.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	txID := beginTestTx(t, srv, ctx)
	ent, _ := srv.store.CreateEntity(ctx, "VecType", "", map[string]string{"name": "a"}, []float32{1.0, 0.0, 0.0}, txID)

	_, err := srv.UpdateEntity(ctx, &flowv1.UpdateEntityRequest{
		Id:            ent.Id,
		Properties:    map[string]string{"name": "b"},
		Embedding:     []float32{1.0, 0.0}, // only 2 dims, expected 3
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for dimension mismatch, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}
