package service

import (
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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
