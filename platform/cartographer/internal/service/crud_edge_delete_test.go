package service

import (
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestDeleteEdge_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	_, err := srv.DeleteEdge(ctx, &flowv1.DeleteEdgeRequest{
		Id:            "11111111-1111-4111-8111-111111111111",
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for not-found edge, got nil")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", status.Code(err))
	}
}

func TestDeleteEdge_InvalidUUID(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	_, err := srv.DeleteEdge(ctx, &flowv1.DeleteEdgeRequest{Id: "not-a-uuid", TransactionId: txID})
	if err == nil {
		t.Fatal("expected error for invalid UUID, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestDeleteEdge_Valid(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	svc, _ := srv.store.CreateEntity(ctx, "Service", "", map[string]string{"name": "svc"}, nil, txID)
	comp, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "core"}, nil, txID)

	createResp, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:      "DEPENDS_ON",
		FromEntityId:  svc.Id,
		ToEntityId:    comp.Id,
		Properties:    map[string]string{"weight": "high"},
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEdge failed: %v", err)
	}

	deleteResp, err := srv.DeleteEdge(ctx, &flowv1.DeleteEdgeRequest{Id: createResp.EdgeId, TransactionId: txID})
	if err != nil {
		t.Fatalf("DeleteEdge failed: %v", err)
	}
	if deleteResp.EdgeId != createResp.EdgeId {
		t.Fatalf("expected deleted edge ID %q, got %q", createResp.EdgeId, deleteResp.EdgeId)
	}
}

// TestDeleteEdge_ReturnsFullDeletedEdge pins SPEC R2's "DeleteEdge(id,
// transactionId?) … Returns the deleted edge": the response must carry the
// deleted edge's endpoints and properties (the SDK builds the returned Edge
// from these fields).
func TestDeleteEdge_ReturnsFullDeletedEdge(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	svc, err := srv.store.CreateEntity(ctx, "Service", "", map[string]string{"name": "svc"}, nil, txID)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	comp, err := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "core"}, nil, txID)
	if err != nil {
		t.Fatalf("create component: %v", err)
	}

	createResp, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:      "DEPENDS_ON",
		FromEntityId:  svc.Id,
		ToEntityId:    comp.Id,
		Properties:    map[string]string{"weight": "high"},
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEdge failed: %v", err)
	}

	deleteResp, err := srv.DeleteEdge(ctx, &flowv1.DeleteEdgeRequest{Id: createResp.EdgeId, TransactionId: txID})
	if err != nil {
		t.Fatalf("DeleteEdge failed: %v", err)
	}
	if deleteResp.EdgeId != createResp.EdgeId {
		t.Fatalf("expected deleted edge ID %q, got %q", createResp.EdgeId, deleteResp.EdgeId)
	}
	if deleteResp.FromEntityId != svc.Id {
		t.Fatalf("deleted edge from-entity = %q, want %q", deleteResp.FromEntityId, svc.Id)
	}
	if deleteResp.ToEntityId != comp.Id {
		t.Fatalf("deleted edge to-entity = %q, want %q", deleteResp.ToEntityId, comp.Id)
	}
	if deleteResp.Properties["weight"] != "high" {
		t.Fatalf("deleted edge properties = %v, want weight=high", deleteResp.Properties)
	}
}

// TestDeleteEdge_SingleTypeSpecificCapabilityPasses pins SPEC R3
// (SPEC:249-250): a caller holding only WRITE:graph/entity/<source-type>
// (plus WRITE:graph/tx) is authorised for a DeleteEdge whose source entity is
// of that type, through the Cartographer's authoritative per-type check.
func TestDeleteEdge_SingleTypeSpecificCapabilityPasses(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := narrowCtx("WRITE:graph/entity/Service", "WRITE:graph/tx")

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	svc, err := srv.store.CreateEntity(ctx, "Service", "", map[string]string{"name": "svc"}, nil, txID)
	if err != nil {
		t.Fatalf("seed source entity: %v", err)
	}
	comp, err := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "core"}, nil, txID)
	if err != nil {
		t.Fatalf("seed target entity: %v", err)
	}
	edge, err := srv.store.CreateEdge(ctx, "DEPENDS_ON", svc.Id, comp.Id, nil, txID)
	if err != nil {
		t.Fatalf("seed edge: %v", err)
	}

	resp, err := srv.DeleteEdge(ctx, &flowv1.DeleteEdgeRequest{Id: edge.Id, TransactionId: txID})
	if err != nil {
		t.Fatalf("DeleteEdge with per-type capability failed: %v", err)
	}
	if resp.EdgeId != edge.Id {
		t.Fatalf("expected deleted edge ID %q, got %q", edge.Id, resp.EdgeId)
	}
}
