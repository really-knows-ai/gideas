package flow

import (
	"context"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// ---------------------------------------------------------------------------
// Tests — ChildWorkitem Handle Methods (new: no ctx, error-only returns)
// ---------------------------------------------------------------------------

func TestChildWorkitem_StoreArtefact_ReturnsErrorOnly(t *testing.T) {
	const wantID = "workitem-child-store"
	env := setupTestEnv(t, wantID)

	child := &ChildWorkitem{
		id:      "child-store-001",
		session: env.client.session,
	}

	err := child.StoreArtefact("input", "codification-input", []byte("goal text"))
	if err != nil {
		t.Fatalf("child.StoreArtefact() returned error: %v", err)
	}

	// Verify the Archivist was called with the correct artefact ID.
	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}
}

func TestChildWorkitem_StampArtefact_ReturnsErrorOnly(t *testing.T) {
	const wantID = "workitem-child-stamp"
	env := setupTestEnv(t, wantID)

	child := &ChildWorkitem{
		id:      "child-stamp-001",
		session: env.client.session,
	}

	err := child.StampArtefact("input", "validated")
	if err != nil {
		t.Fatalf("child.StampArtefact() returned error: %v", err)
	}

	if env.spy.lastStampReq == nil {
		t.Fatal("expected stamp request to be captured")
	}
	if env.spy.lastStampReq.GetStampName() != "validated" {
		t.Fatalf("expected stamp name=validated, got %q", env.spy.lastStampReq.GetStampName())
	}
}

func TestChildWorkitem_RouteTo_ReturnsErrorOnly(t *testing.T) {
	const wantID = "workitem-child-route"
	env := setupTestEnv(t, wantID)

	child := &ChildWorkitem{
		id:      "child-route-001",
		session: env.client.session,
	}

	err := child.RouteTo("codify-smt")
	if err != nil {
		t.Fatalf("child.RouteTo() returned error: %v", err)
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}
}

func TestChildWorkitem_RouteToOutput_ReturnsErrorOnly(t *testing.T) {
	const wantID = "workitem-child-output"
	env := setupTestEnv(t, wantID)

	child := &ChildWorkitem{
		id:      "child-output-001",
		session: env.client.session,
	}

	err := child.RouteToOutput("codification")
	if err != nil {
		t.Fatalf("child.RouteToOutput() returned error: %v", err)
	}
}

func TestChildWorkitem_Complete_ReturnsErrorOnly(t *testing.T) {
	const wantID = "workitem-child-complete"
	env := setupTestEnv(t, wantID)

	child := &ChildWorkitem{
		id:      "child-complete-001",
		session: env.client.session,
	}

	err := child.Complete()
	if err != nil {
		t.Fatalf("child.Complete() returned error: %v", err)
	}
}

func TestChildWorkitem_ID(t *testing.T) {
	child := &ChildWorkitem{id: "test-child-id"}
	if child.ID() != "test-child-id" {
		t.Fatalf("expected ID=test-child-id, got %q", child.ID())
	}
}

func TestChildWorkitem_StoreArtefact_Error_Propagates(t *testing.T) {
	env := setupTestEnv(t, "workitem-child-error")

	child := &ChildWorkitem{
		id:      "child-error-001",
		session: env.client.session,
	}

	err := child.StoreArtefact("input", "ga", []byte("data"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests — Workitem.CreateChild returns handle with working methods
// ---------------------------------------------------------------------------

func TestCreateChildWorkitem_HandleIntegration(t *testing.T) {
	const wantID = "workitem-parent-integration"
	env := setupTestEnv(t, wantID)

	wi, err := env.client.GetWorkitem()
	if err != nil {
		t.Fatalf("GetWorkitem() returned error: %v", err)
	}

	// Create a child via the workitem domain method.
	child, err := wi.CreateChild()
	if err != nil {
		t.Fatalf("wi.CreateChild() returned error: %v", err)
	}

	// Verify the handle's session is correctly wired.
	if child.session != env.client.session {
		t.Fatal("child.session does not point to the expected session")
	}

	// Store an artefact on the child (no ctx, error-only).
	if err := child.StoreArtefact("input", "codification-input", []byte("goal")); err != nil {
		t.Fatalf("child.StoreArtefact() returned error: %v", err)
	}

	// Route the child (no ctx, error-only).
	if err := child.RouteTo("codify-smt"); err != nil {
		t.Fatalf("child.RouteTo() returned error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests — EventBus option wiring
// ---------------------------------------------------------------------------

func TestWithEventBusAddress(t *testing.T) {
	cfg := &clientConfig{sidecarAddr: DefaultSidecarAddress}
	WithEventBusAddress("eventbus:50056")(cfg)

	if cfg.eventBusAddr != "eventbus:50056" {
		t.Fatalf("expected eventBusAddr=eventbus:50056, got %s", cfg.eventBusAddr)
	}
}

// ---------------------------------------------------------------------------
// Tests — Verify metadata injection on child RPCs
// ---------------------------------------------------------------------------

// captureRouteChildServer captures the RouteChild request for assertions.
type captureRouteChildServer struct {
	flowv1.UnimplementedOperatorServiceServer
	lastReq *flowv1.RouteChildRequest
	lastMD  metadata.MD
}

func (s *captureRouteChildServer) RouteChild(
	ctx context.Context, req *flowv1.RouteChildRequest,
) (*flowv1.RouteChildResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	s.lastReq = req
	return &flowv1.RouteChildResponse{Accepted: true}, nil
}

func TestChildWorkitem_RouteTo_SendsCorrectRequest(t *testing.T) {
	const wantParentID = "workitem-route-verify"
	captureSpy := &captureRouteChildServer{}

	client, _ := setupGRPCTestEnv(t, wantParentID, func(s *grpc.Server) {
		flowv1.RegisterOperatorServiceServer(s, captureSpy)
	})

	child := &ChildWorkitem{
		id:      "child-verify-001",
		session: client.session,
	}

	err := child.RouteTo("target-node")
	if err != nil {
		t.Fatalf("child.RouteTo() error: %v", err)
	}

	if captureSpy.lastReq == nil {
		t.Fatal("expected request to be captured")
	}
	if captureSpy.lastReq.GetChildWorkitemId() != "child-verify-001" {
		t.Fatalf("expected child_workitem_id=child-verify-001, got %q",
			captureSpy.lastReq.GetChildWorkitemId())
	}
	ri := captureSpy.lastReq.GetRoutingInstruction()
	if ri == nil {
		t.Fatal("expected routing instruction")
	}
	if ri.GetType() != flowv1.RoutingType_ROUTING_TYPE_ROUTE_TO {
		t.Fatalf("expected ROUTE_TO, got %v", ri.GetType())
	}
	if ri.GetTarget() != "target-node" {
		t.Fatalf("expected target=target-node, got %q", ri.GetTarget())
	}

	// Verify workitem metadata was injected.
	got := captureSpy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantParentID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantParentID)
	}
}

func TestChildWorkitem_Complete_SendsCorrectRequest(t *testing.T) {
	captureSpy := &captureRouteChildServer{}

	client, _ := setupGRPCTestEnv(t, "workitem-complete-verify", func(s *grpc.Server) {
		flowv1.RegisterOperatorServiceServer(s, captureSpy)
	})

	child := &ChildWorkitem{
		id:      "child-complete-verify",
		session: client.session,
	}

	err := child.Complete()
	if err != nil {
		t.Fatalf("child.Complete() error: %v", err)
	}

	if captureSpy.lastReq == nil {
		t.Fatal("expected request to be captured")
	}
	if captureSpy.lastReq.GetChildWorkitemId() != "child-complete-verify" {
		t.Fatalf("expected child_workitem_id=child-complete-verify, got %q",
			captureSpy.lastReq.GetChildWorkitemId())
	}
	ri := captureSpy.lastReq.GetRoutingInstruction()
	if ri.GetType() != flowv1.RoutingType_ROUTING_TYPE_COMPLETE {
		t.Fatalf("expected COMPLETE, got %v", ri.GetType())
	}
}

// ---------------------------------------------------------------------------
// spyEventBusServer — used by childwatcher_test.go
// ---------------------------------------------------------------------------

// spyEventBusServer implements FlowEventBusServiceServer for testing
// WatchChildren. It sends a fixed set of events and then closes the stream.
type spyEventBusServer struct {
	flowv1.UnimplementedFlowEventBusServiceServer

	// events to send on Subscribe.
	events []*flowv1.FlowEvent
	// captured request for assertions.
	lastRequest *flowv1.SubscribeRequest
}

func (s *spyEventBusServer) Subscribe(
	req *flowv1.SubscribeRequest, stream grpc.ServerStreamingServer[flowv1.FlowEvent],
) error {
	s.lastRequest = req
	for _, evt := range s.events {
		if err := stream.Send(evt); err != nil {
			return err
		}
	}
	return nil // closes the stream
}
