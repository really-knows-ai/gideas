package service

import (
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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

func TestCreateEdge_RuleViolation(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	// Service has rules permitting connection to Component via DEPENDS_ON,
	// but Component has no rules defined.
	comp, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "comp"}, nil, txID)
	svc, _ := srv.store.CreateEntity(ctx, "Service", "", map[string]string{"name": "svc"}, nil, txID)

	// Attempt edge FROM Component (no rules) TO Service.
	_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:      "DEPENDS_ON",
		FromEntityId:  comp.Id,
		ToEntityId:    svc.Id,
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for rule violation, got nil")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", status.Code(err))
	}
}

// TestCreateEdge_SelfReferencing verifies SPEC R1's self-referencing allowance
// at the service layer: an entity type appearing in its own canConnectTo list
// (Component → Component) must admit an edge, with the membership check treating
// the declaring type the same as any other. Only Service → Component rule tests
// were previously exercised at the service layer.
func TestCreateEdge_SelfReferencing(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := testCtx()

	// Component declares a self-referencing rule via DEPENDS_ON.
	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:       "Component",
				Properties: []*flowv1.Property{{Name: "name", Type: "string", Required: true}},
				Rules: []*flowv1.ConnectionRule{
					{CanConnectTo: []string{"Component"}, Using: []string{"DEPENDS_ON"}},
				},
			},
		},
		EdgeTypes: []*flowv1.EdgeType{{Name: "DEPENDS_ON"}},
	}
	if err := st.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	txID := beginTestTx(t, srv, ctx)
	from, _ := st.CreateEntity(ctx, "Component", "", map[string]string{"name": "from"}, nil, txID)
	to, _ := st.CreateEntity(ctx, "Component", "", map[string]string{"name": "to"}, nil, txID)

	resp, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType: "DEPENDS_ON", FromEntityId: from.Id, ToEntityId: to.Id, TransactionId: txID,
	})
	if err != nil {
		t.Fatalf("self-referencing Component->Component edge must be allowed: %v", err)
	}
	if resp.EdgeId == "" {
		t.Fatal("expected non-empty edge ID")
	}
}

func TestCreateEdge_MissingRequiredProperty(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := testCtx()

	// Apply schema with a required edge property.
	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:       "Source",
				Properties: []*flowv1.Property{{Name: "name", Type: "string", Required: true}},
			},
			{
				Name:       "Target",
				Properties: []*flowv1.Property{{Name: "name", Type: "string", Required: true}},
				Rules:      []*flowv1.ConnectionRule{{CanConnectTo: []string{"Source"}, Using: []string{"LINKED"}}},
			},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "LINKED", Properties: []*flowv1.Property{{Name: "label", Type: "string", Required: true}}},
		},
	}
	if err := st.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	txID := beginTestTx(t, srv, ctx)
	src, _ := srv.store.CreateEntity(ctx, "Source", "", map[string]string{"name": "src"}, nil, txID)
	tgt, _ := srv.store.CreateEntity(ctx, "Target", "", map[string]string{"name": "tgt"}, nil, txID)

	// Missing required property "label".
	_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:      "LINKED",
		FromEntityId:  tgt.Id,
		ToEntityId:    src.Id,
		Properties:    map[string]string{},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for missing required property, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

func TestCreateEdge_UnknownProperty(t *testing.T) {
	srv, _ := newTestServer(t)
	ctx := testCtx()

	applyTestSchema(ctx, t, srv.store)
	txID := beginTestTx(t, srv, ctx)
	svc, _ := srv.store.CreateEntity(ctx, "Service", "", map[string]string{"name": "svc"}, nil, txID)
	comp, _ := srv.store.CreateEntity(ctx, "Component", "", map[string]string{"name": "core"}, nil, txID)

	_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:      "DEPENDS_ON",
		FromEntityId:  svc.Id,
		ToEntityId:    comp.Id,
		Properties:    map[string]string{"unknownprop": "x"},
		TransactionId: txID,
	})
	if err == nil {
		t.Fatal("expected error for unknown property, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", status.Code(err))
	}
}

// TestCreateEdge_StructuralErrorBeforeEntityExistence asserts the SPEC RPC
// check-order (CreateEdge: structural → entity existence): a request carrying
// BOTH a missing source entity AND a structurally invalid edge property surfaces
// INVALID_ARGUMENT (structural), not the NOT_FOUND the entity-existence probe
// would otherwise return.
func TestCreateEdge_StructuralErrorBeforeEntityExistence(t *testing.T) {
	srv, st := newTestServer(t)
	ctx := testCtx()

	schema := &flowv1.Schema{
		EntityTypes: []*flowv1.EntityType{
			{
				Name:       "Source",
				Properties: []*flowv1.Property{{Name: "name", Type: "string", Required: true}},
			},
			{
				Name:       "Target",
				Properties: []*flowv1.Property{{Name: "name", Type: "string", Required: true}},
				Rules:      []*flowv1.ConnectionRule{{CanConnectTo: []string{"Source"}, Using: []string{"LINKED"}}},
			},
		},
		EdgeTypes: []*flowv1.EdgeType{
			{Name: "LINKED", Properties: []*flowv1.Property{{Name: "label", Type: "string", Required: true}}},
		},
	}
	if err := st.ApplySchema(ctx, schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	txID := beginTestTx(t, srv, ctx)
	tgt, _ := srv.store.CreateEntity(ctx, "Target", "", map[string]string{"name": "tgt"}, nil, txID)

	missingSource := "11111111-1111-4111-8111-111111111111"

	// Missing required property + missing source → INVALID_ARGUMENT, not NOT_FOUND.
	_, err := srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:      "LINKED",
		FromEntityId:  missingSource,
		ToEntityId:    tgt.Id,
		TransactionId: txID,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for missing required property + missing source, got %v", status.Code(err))
	}

	// Unknown property + missing source → INVALID_ARGUMENT, not NOT_FOUND.
	_, err = srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:      "LINKED",
		FromEntityId:  missingSource,
		ToEntityId:    tgt.Id,
		Properties:    map[string]string{"label": "x", "bogus": "y"},
		TransactionId: txID,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for unknown property + missing source, got %v", status.Code(err))
	}

	// Structurally valid + missing source → NOT_FOUND (entity existence is the
	// next check in order).
	_, err = srv.CreateEdge(ctx, &flowv1.CreateEdgeRequest{
		EdgeType:      "LINKED",
		FromEntityId:  missingSource,
		ToEntityId:    tgt.Id,
		Properties:    map[string]string{"label": "x"},
		TransactionId: txID,
	})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound for structurally-valid missing source, got %v", status.Code(err))
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
