package service

import (
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestCreateEdge_Valid(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	svc, _ := srv.store.CreateEntity(ctx, "Service", "", map[string]string{"name": "svc"}, nil, txID)
	comp, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "core"}, nil, txID)

	resp, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:      "DEPENDS_ON",
		FromEntityId:  svc.Id,
		ToEntityId:    comp.Id,
		Properties:    map[string]string{"weight": "high"},
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEdge failed: %v", err)
	}
	if resp.EdgeId == "" {
		t.Fatal("expected non-empty edge ID")
	}
}

func TestCreateEdge_SourceNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:      "DEPENDS_ON",
		FromEntityId:  "11111111-1111-4111-8111-111111111111",
		ToEntityId:    "22222222-2222-4222-8222-222222222222",
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for not-found source, got nil")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", status.Code(err))
	}
}

func TestCreateEdge_TargetNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	svc, _ := srv.store.CreateEntity(ctx, "Service", "", map[string]string{"name": "svc"}, nil, txID)

	_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:      "DEPENDS_ON",
		FromEntityId:  svc.Id,
		ToEntityId:    "11111111-1111-4111-8111-111111111111",
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for target not found, got nil")
	}
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", status.Code(err))
	}
}

func TestCreateEdge_InvalidIDFormat(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)

	_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:      "DEPENDS_ON",
		FromEntityId:  "not-a-uuid",
		ToEntityId:    "11111111-1111-4111-8111-111111111111",
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for invalid ID format, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestCreateEdge_UnknownEdgeType(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	ent, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "x"}, nil, txID)

	_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:      "UNKNOWN_EDGE",
		FromEntityId:  ent.Id,
		ToEntityId:    ent.Id,
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for unknown edge type, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

// TestCreateEdge_UnknownEdgeTypeWinsOverMissingCapability asserts the SPEC
// validation order (structural before capability): a caller lacking
// WRITE:graph/entity/* still gets INVALID_ARGUMENT (not PERMISSION_DENIED)
// for an unknown edge type. The transaction is begun with full capabilities;
// the mutation call itself carries only READ capabilities.
func TestCreateEdge_UnknownEdgeTypeWinsOverMissingCapability(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)

	// Only READ capabilities — the caller holds no write capability at all.
	noWriteCtx := capabilityContext("READ:graph/entity/*", testSidecarPriv, "sidecar")
	_, err := srv.CreateEdge(noWriteCtx, &flowv1.CreateEdgeRequest{
		EdgeType:      "UNKNOWN_EDGE",
		FromEntityId:  "11111111-1111-4111-8111-111111111111",
		ToEntityId:    "22222222-2222-4222-8222-222222222222",
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for unknown edge type, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected structural InvalidArgument to win over capability check, got %v (%v)", status.Code(err), err)
	}
}

// TestCreateEdge_SingleTypeSpecificCapabilityPasses pins SPEC R3
// (SPEC:249-250): a caller holding only WRITE:graph/entity/<source-type>
// (plus WRITE:graph/tx) is authorised for a CreateEdge whose source entity is
// of that type, through the Cartographer's authoritative per-type check.
func TestCreateEdge_SingleTypeSpecificCapabilityPasses(t *testing.T) {
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

	resp, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:      "DEPENDS_ON",
		FromEntityId:  svc.Id,
		ToEntityId:    comp.Id,
		TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("CreateEdge with per-type capability failed: %v", err)
	}
	if resp.EdgeId == "" {
		t.Fatal("expected non-empty edge ID")
	}
}
