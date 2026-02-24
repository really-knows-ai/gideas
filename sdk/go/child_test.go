package flow

import (
	"context"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// ---------------------------------------------------------------------------
// Tests — CreateChildWorkitem
// ---------------------------------------------------------------------------

func TestCreateChildWorkitem_ReturnsHandle(t *testing.T) {
	const wantID = "workitem-parent-001"
	env := setupTestEnv(t, wantID)

	child, err := env.client.CreateChildWorkitem(context.Background())
	if err != nil {
		t.Fatalf("CreateChildWorkitem() returned error: %v", err)
	}
	if child.ID() != "child-001" {
		t.Fatalf("expected child ID=child-001, got %q", child.ID())
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}
}

// ---------------------------------------------------------------------------
// Tests — GetChildren
// ---------------------------------------------------------------------------

func TestGetChildren_ReturnsStatuses(t *testing.T) {
	const wantID = "workitem-parent-002"
	env := setupTestEnv(t, wantID)

	children, err := env.client.GetChildren(context.Background())
	if err != nil {
		t.Fatalf("GetChildren() returned error: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(children))
	}

	c1 := children[0]
	if c1.WorkitemID != "child-001" {
		t.Fatalf("expected child-001, got %q", c1.WorkitemID)
	}
	if c1.Phase != "Running" {
		t.Fatalf("expected phase Running, got %q", c1.Phase)
	}
	if c1.CurrentAssignee != "codify-smt" {
		t.Fatalf("expected assignee codify-smt, got %q", c1.CurrentAssignee)
	}
	if len(c1.Artefacts) != 1 {
		t.Fatalf("expected 1 artefact, got %d", len(c1.Artefacts))
	}

	c2 := children[1]
	if c2.WorkitemID != "child-002" {
		t.Fatalf("expected child-002, got %q", c2.WorkitemID)
	}
	if c2.Phase != "Completed" {
		t.Fatalf("expected phase Completed, got %q", c2.Phase)
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}
}

// ---------------------------------------------------------------------------
// Tests — GetChildArtefact
// ---------------------------------------------------------------------------

func TestGetChildArtefact_InjectsTargetWorkitemID(t *testing.T) {
	const wantID = "workitem-parent-003"
	env := setupTestEnv(t, wantID)

	resp, err := env.client.GetChildArtefact(context.Background(), "child-002", "output")
	if err != nil {
		t.Fatalf("GetChildArtefact() returned error: %v", err)
	}
	if string(resp.GetContent()) != "test-content" {
		t.Fatalf("unexpected content: %s", resp.GetContent())
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}
}

// ---------------------------------------------------------------------------
// Tests — ListChildArtefacts
// ---------------------------------------------------------------------------

func TestListChildArtefacts_ReturnsRefs(t *testing.T) {
	const wantID = "workitem-parent-004"
	env := setupTestEnv(t, wantID)

	refs, err := env.client.ListChildArtefacts(context.Background(), "child-002")
	if err != nil {
		t.Fatalf("ListChildArtefacts() returned error: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("expected 1 artefact ref, got %d", len(refs))
	}
	if refs[0].GetId() != "output" {
		t.Fatalf("expected artefact id=output, got %q", refs[0].GetId())
	}
	if refs[0].GetGovernedArtefact() != "codification-output" {
		t.Fatalf("expected governed_artefact=codification-output, got %q", refs[0].GetGovernedArtefact())
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}
}

// ---------------------------------------------------------------------------
// Tests — ChildWorkitem Handle Methods
// ---------------------------------------------------------------------------

func TestChildWorkitem_StoreArtefact(t *testing.T) {
	const wantID = "workitem-child-store"
	env := setupTestEnv(t, wantID)

	child := &ChildWorkitem{
		id:        "child-store-001",
		parent:    env.client,
		archivist: env.client.Archivist,
		operator:  env.client.Operator,
	}

	resp, err := child.StoreArtefact(context.Background(), "input", "codification-input", []byte("goal text"))
	if err != nil {
		t.Fatalf("child.StoreArtefact() returned error: %v", err)
	}
	if resp.GetVersionHash() != "hash-001" {
		t.Fatalf("expected version_hash=hash-001, got %q", resp.GetVersionHash())
	}
	if !resp.GetIsNewVersion() {
		t.Fatal("expected is_new_version=true")
	}
}

func TestChildWorkitem_StampArtefact(t *testing.T) {
	const wantID = "workitem-child-stamp"
	env := setupTestEnv(t, wantID)

	child := &ChildWorkitem{
		id:        "child-stamp-001",
		parent:    env.client,
		archivist: env.client.Archivist,
		operator:  env.client.Operator,
	}

	resp, err := child.StampArtefact(context.Background(), "input", "validated")
	if err != nil {
		t.Fatalf("child.StampArtefact() returned error: %v", err)
	}
	if resp.GetStamp().GetName() != "validated" {
		t.Fatalf("expected stamp name=validated, got %q", resp.GetStamp().GetName())
	}
}

func TestChildWorkitem_RouteTo(t *testing.T) {
	const wantID = "workitem-child-route"
	env := setupTestEnv(t, wantID)

	child := &ChildWorkitem{
		id:        "child-route-001",
		parent:    env.client,
		archivist: env.client.Archivist,
		operator:  env.client.Operator,
	}

	accepted, err := child.RouteTo(context.Background(), "codify-smt")
	if err != nil {
		t.Fatalf("child.RouteTo() returned error: %v", err)
	}
	if !accepted {
		t.Fatal("expected accepted=true")
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}
}

func TestChildWorkitem_RouteToOutput(t *testing.T) {
	const wantID = "workitem-child-output"
	env := setupTestEnv(t, wantID)

	child := &ChildWorkitem{
		id:        "child-output-001",
		parent:    env.client,
		archivist: env.client.Archivist,
		operator:  env.client.Operator,
	}

	accepted, err := child.RouteToOutput(context.Background(), "codification")
	if err != nil {
		t.Fatalf("child.RouteToOutput() returned error: %v", err)
	}
	if !accepted {
		t.Fatal("expected accepted=true")
	}
}

func TestChildWorkitem_Complete(t *testing.T) {
	const wantID = "workitem-child-complete"
	env := setupTestEnv(t, wantID)

	child := &ChildWorkitem{
		id:        "child-complete-001",
		parent:    env.client,
		archivist: env.client.Archivist,
		operator:  env.client.Operator,
	}

	accepted, err := child.Complete(context.Background())
	if err != nil {
		t.Fatalf("child.Complete() returned error: %v", err)
	}
	if !accepted {
		t.Fatal("expected accepted=true")
	}
}

// ---------------------------------------------------------------------------
// Tests — WatchChildren
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

func TestWatchChildren_ReceivesEvents(t *testing.T) {
	const parentID = "workitem-watch-parent"

	ebSpy := &spyEventBusServer{
		events: []*flowv1.FlowEvent{
			{
				WorkitemId: "child-w-001",
				EventType:  "workitem.phase_changed",
				Labels: []*flowv1.Label{
					{Key: "parent_workitem_id", Value: parentID},
					{Key: "phase", Value: "Running"},
					{Key: "node_id", Value: "codify-smt"},
				},
			},
			{
				WorkitemId: "child-w-001",
				EventType:  "workitem.phase_changed",
				Labels: []*flowv1.Label{
					{Key: "parent_workitem_id", Value: parentID},
					{Key: "phase", Value: "Completed"},
					{Key: "node_id", Value: "codify-smt"},
				},
			},
		},
	}

	spy := &spyServer{}
	client, _, _ := setupGRPCTestEnvWithEventBus(t, parentID,
		func(s *grpc.Server) {
			flowv1.RegisterSidecarServiceServer(s, spy)
			flowv1.RegisterOperatorServiceServer(s, spy)
			flowv1.RegisterArchivistServiceServer(s, spy)
			flowv1.RegisterLibrarianServiceServer(s, spy)
			flowv1.RegisterFrictionLedgerServiceServer(s, spy)
			flowv1.RegisterJuryServiceServer(s, spy)
			flowv1.RegisterClerkServiceServer(s, spy)
		},
		func(s *grpc.Server) {
			flowv1.RegisterFlowEventBusServiceServer(s, ebSpy)
		},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := client.WatchChildren(ctx)
	if err != nil {
		t.Fatalf("WatchChildren() returned error: %v", err)
	}

	// Collect all events from the channel.
	received := make([]ChildLifecycleEvent, 0, len(ebSpy.events))
	for evt := range ch {
		received = append(received, evt)
	}

	if len(received) != 2 {
		t.Fatalf("expected 2 events, got %d", len(received))
	}

	if received[0].WorkitemID != "child-w-001" {
		t.Fatalf("event[0] workitem_id = %q, want child-w-001", received[0].WorkitemID)
	}
	if received[0].Phase != "Running" {
		t.Fatalf("event[0] phase = %q, want Running", received[0].Phase)
	}
	if received[0].NodeID != "codify-smt" {
		t.Fatalf("event[0] node_id = %q, want codify-smt", received[0].NodeID)
	}

	if received[1].Phase != "Completed" {
		t.Fatalf("event[1] phase = %q, want Completed", received[1].Phase)
	}

	// Verify the subscription request had correct filters.
	if ebSpy.lastRequest == nil {
		t.Fatal("expected subscribe request to be captured")
	}
	if ebSpy.lastRequest.GetChannel() != "workitem" {
		t.Fatalf("subscribe channel = %q, want workitem", ebSpy.lastRequest.GetChannel())
	}
	filter := ebSpy.lastRequest.GetFilter()
	if filter == nil {
		t.Fatal("expected subscribe filter")
	}
	if filter.GetEventType() != "workitem.phase_changed" {
		t.Fatalf("filter event_type = %q, want workitem.phase_changed", filter.GetEventType())
	}
	labels := filter.GetMatchLabels()
	if len(labels) != 1 {
		t.Fatalf("expected 1 match label, got %d", len(labels))
	}
	if labels[0].GetKey() != "parent_workitem_id" || labels[0].GetValue() != parentID {
		t.Fatalf("match label = {%q, %q}, want {parent_workitem_id, %q}",
			labels[0].GetKey(), labels[0].GetValue(), parentID)
	}
}

func TestWatchChildren_NoEventBus_ReturnsError(t *testing.T) {
	env := setupTestEnv(t, "workitem-no-eb")

	// Client has no EventBus wired.
	_, err := env.client.WatchChildren(context.Background())
	if err == nil {
		t.Fatal("expected error when EventBus is nil")
	}
}

// ---------------------------------------------------------------------------
// Tests — CreateChildWorkitem returns handle with working methods
// ---------------------------------------------------------------------------

func TestCreateChildWorkitem_HandleIntegration(t *testing.T) {
	const wantID = "workitem-parent-integration"
	env := setupTestEnv(t, wantID)

	// Create a child via the convenience method.
	child, err := env.client.CreateChildWorkitem(context.Background())
	if err != nil {
		t.Fatalf("CreateChildWorkitem() returned error: %v", err)
	}

	// Verify the handle's fields are correctly wired.
	if child.parent != env.client {
		t.Fatal("child.parent does not point to the expected client")
	}

	// Store an artefact on the child.
	storeResp, err := child.StoreArtefact(context.Background(), "input", "codification-input", []byte("goal"))
	if err != nil {
		t.Fatalf("child.StoreArtefact() returned error: %v", err)
	}
	if storeResp.GetVersionHash() != "hash-001" {
		t.Fatalf("expected version_hash=hash-001, got %q", storeResp.GetVersionHash())
	}

	// Route the child.
	accepted, err := child.RouteTo(context.Background(), "codify-smt")
	if err != nil {
		t.Fatalf("child.RouteTo() returned error: %v", err)
	}
	if !accepted {
		t.Fatal("expected accepted=true")
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
// Tests — Client.WorkitemID accessible from child
// ---------------------------------------------------------------------------

func TestChildWorkitem_ID(t *testing.T) {
	child := &ChildWorkitem{id: "test-child-id"}
	if child.ID() != "test-child-id" {
		t.Fatalf("expected ID=test-child-id, got %q", child.ID())
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
		id:        "child-verify-001",
		parent:    client,
		archivist: client.Archivist,
		operator:  client.Operator,
	}

	_, err := child.RouteTo(context.Background(), "target-node")
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
		id:       "child-complete-verify",
		parent:   client,
		operator: client.Operator,
	}

	_, err := child.Complete(context.Background())
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
