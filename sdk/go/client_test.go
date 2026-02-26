package flow

import (
	"context"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const bufSize = 1024 * 1024

// ---------------------------------------------------------------------------
// Spy server — captures incoming metadata for assertions
// ---------------------------------------------------------------------------

// spyServer implements the gRPC services and records the metadata it
// receives. This lets us assert that the SDK's interceptor injects the
// correct workitem_id header.
type spyServer struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFrictionLedgerServiceServer

	// lastMD is the metadata captured from the most recent call.
	lastMD metadata.MD
}

func (s *spyServer) Heartbeat(ctx context.Context, req *flowv1.HeartbeatRequest) (*flowv1.HeartbeatResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *spyServer) SubmitResult(
	ctx context.Context, req *flowv1.SubmitResultRequest,
) (*flowv1.SubmitResultResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

func (s *spyServer) GetFlowTopology(
	ctx context.Context, _ *flowv1.GetFlowTopologyRequest,
) (*flowv1.GetFlowTopologyResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.GetFlowTopologyResponse{
		Self: &flowv1.FlowNode{
			Name:         "test-node",
			Capabilities: []string{"READ:flow"},
			Outputs:      []*flowv1.FlowOutput{{Name: "next", Target: "other"}},
		},
		Nodes: map[string]*flowv1.FlowNode{
			"test-node": {Name: "test-node"},
			"other":     {Name: "other"},
		},
		ExitContract: map[string]*flowv1.StampRequirements{
			"doc": {Stamps: []string{"linter", "approval"}},
		},
	}, nil
}

func (s *spyServer) GetArtefact(
	ctx context.Context, req *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.GetArtefactResponse{
		Content:          []byte("test-content"),
		VersionHash:      "test-hash",
		GovernedArtefact: "test-artefact",
	}, nil
}

func (s *spyServer) QueryLaws(ctx context.Context, req *flowv1.QueryLawsRequest) (*flowv1.QueryLawsResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.QueryLawsResponse{Laws: []*flowv1.Law{{Id: "law-1"}}}, nil
}

func (s *spyServer) Cite(ctx context.Context, req *flowv1.CiteRequest) (*flowv1.CiteResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.CiteResponse{Acknowledged: true}, nil
}

func (s *spyServer) RecordFinding(
	ctx context.Context, req *flowv1.RecordFindingRequest,
) (*flowv1.RecordFindingResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.RecordFindingResponse{LawId: "finding-001"}, nil
}

func (s *spyServer) RecordTelemetry(
	ctx context.Context, req *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

func (s *spyServer) AddFriction(
	ctx context.Context, req *flowv1.AddFrictionRequest,
) (*flowv1.AddFrictionResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.AddFrictionResponse{Acknowledged: true}, nil
}

func (s *spyServer) RefuseFeedback(
	ctx context.Context, req *flowv1.RefuseFeedbackRequest,
) (*flowv1.RefuseFeedbackResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.RefuseFeedbackResponse{UpdatedItem: &flowv1.FeedbackItem{
		Id: req.GetFeedbackId(), State: flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
	}}, nil
}

func (s *spyServer) RejectFix(
	ctx context.Context, req *flowv1.RejectFixRequest,
) (*flowv1.RejectFixResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.RejectFixResponse{UpdatedItem: &flowv1.FeedbackItem{
		Id: req.GetFeedbackId(), State: flowv1.FeedbackState_FEEDBACK_STATE_REJECTED,
	}}, nil
}

func (s *spyServer) AcceptRefusal(
	ctx context.Context, req *flowv1.AcceptRefusalRequest,
) (*flowv1.AcceptRefusalResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.AcceptRefusalResponse{UpdatedItem: &flowv1.FeedbackItem{
		Id: req.GetFeedbackId(), State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED,
	}}, nil
}

func (s *spyServer) RejectRefusal(
	ctx context.Context, req *flowv1.RejectRefusalRequest,
) (*flowv1.RejectRefusalResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.RejectRefusalResponse{UpdatedItem: &flowv1.FeedbackItem{
		Id: req.GetFeedbackId(), State: flowv1.FeedbackState_FEEDBACK_STATE_REJECTED,
	}}, nil
}

func (s *spyServer) GetFeedbackDepth(
	ctx context.Context, req *flowv1.GetFeedbackDepthRequest,
) (*flowv1.GetFeedbackDepthResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.GetFeedbackDepthResponse{Depth: 5}, nil
}

func (s *spyServer) DeadlockFeedback(
	ctx context.Context, req *flowv1.DeadlockFeedbackRequest,
) (*flowv1.DeadlockFeedbackResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.DeadlockFeedbackResponse{UpdatedItem: &flowv1.FeedbackItem{
		Id:    req.GetFeedbackId(),
		State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
	}}, nil
}

func (s *spyServer) LinkRuling(
	ctx context.Context, req *flowv1.LinkRulingRequest,
) (*flowv1.LinkRulingResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.LinkRulingResponse{UpdatedItem: &flowv1.FeedbackItem{
		Id:           req.GetFeedbackId(),
		State:        req.GetTargetState(),
		LinkedRuling: req.GetLawId(),
	}}, nil
}

func (s *spyServer) QueryFriction(
	ctx context.Context, req *flowv1.QueryFrictionRequest,
) (*flowv1.QueryFrictionResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.QueryFrictionResponse{
		FrictionAggregates: []*flowv1.FrictionAggregate{{LawId: "law-friction-001"}},
	}, nil
}

func (s *spyServer) GetLaw(
	ctx context.Context, req *flowv1.GetLawRequest,
) (*flowv1.GetLawResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.GetLawResponse{
		Law: &flowv1.Law{Id: req.GetLawId(), Goal: "test goal"},
	}, nil
}

func (s *spyServer) CreateChildWorkitem(
	ctx context.Context, _ *flowv1.CreateChildWorkitemRequest,
) (*flowv1.CreateChildWorkitemResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.CreateChildWorkitemResponse{
		ChildWorkitemId: "child-001",
	}, nil
}

func (s *spyServer) RouteChild(
	ctx context.Context, req *flowv1.RouteChildRequest,
) (*flowv1.RouteChildResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.RouteChildResponse{Accepted: true}, nil
}

func (s *spyServer) GetChildren(
	ctx context.Context, _ *flowv1.GetChildrenRequest,
) (*flowv1.GetChildrenResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.GetChildrenResponse{
		Children: []*flowv1.ChildWorkitemStatus{
			{
				WorkitemId:      "child-001",
				Phase:           "Running",
				CurrentAssignee: "codify-smt",
				Artefacts: []*flowv1.ArtefactRef{
					{Id: "input", GovernedArtefact: "codification-input"},
				},
			},
			{
				WorkitemId:      "child-002",
				Phase:           "Completed",
				CurrentAssignee: "codify-smt",
			},
		},
	}, nil
}

func (s *spyServer) StoreArtefact(
	ctx context.Context, req *flowv1.StoreArtefactRequest,
) (*flowv1.StoreArtefactResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.StoreArtefactResponse{
		VersionHash:  "hash-001",
		IsNewVersion: true,
	}, nil
}

func (s *spyServer) StampArtefact(
	ctx context.Context, req *flowv1.StampArtefactRequest,
) (*flowv1.StampArtefactResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.StampArtefactResponse{
		Stamp: &flowv1.Stamp{Name: req.GetStampName()},
	}, nil
}

func (s *spyServer) ListArtefacts(
	ctx context.Context, req *flowv1.ListArtefactsRequest,
) (*flowv1.ListArtefactsResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.ListArtefactsResponse{
		ArtefactRefs: []*flowv1.ArtefactRef{
			{Id: "output", GovernedArtefact: "codification-output"},
		},
	}, nil
}

// ---------------------------------------------------------------------------
// Test helper — starts a bufconn gRPC server and returns a connected Client
// ---------------------------------------------------------------------------

// testEnv bundles a live Client wired to an in-process gRPC server via
// bufconn so tests never touch the real network.
type testEnv struct {
	client *Client
	spy    *spyServer
	srv    *grpc.Server
}

func setupTestEnv(t *testing.T, workitemID string) *testEnv {
	t.Helper()

	spy := &spyServer{}
	client, srv := setupGRPCTestEnv(t, workitemID, func(s *grpc.Server) {
		flowv1.RegisterSidecarServiceServer(s, spy)
		flowv1.RegisterOperatorServiceServer(s, spy)
		flowv1.RegisterArchivistServiceServer(s, spy)
		flowv1.RegisterLibrarianServiceServer(s, spy)
		flowv1.RegisterFrictionLedgerServiceServer(s, spy)
	})

	return &testEnv{client: client, spy: spy, srv: srv}
}

// ---------------------------------------------------------------------------
// Tests — Configuration
// ---------------------------------------------------------------------------

func TestNewClient_DefaultAddress(t *testing.T) {
	cfg := &clientConfig{sidecarAddr: DefaultSidecarAddress}

	// Apply zero options — the default should remain.
	opts := []ClientOption{}
	for _, o := range opts {
		o(cfg)
	}

	if cfg.sidecarAddr != "localhost:50051" {
		t.Fatalf("expected default address localhost:50051, got %s", cfg.sidecarAddr)
	}
}

func TestNewClient_CustomAddress(t *testing.T) {
	cfg := &clientConfig{sidecarAddr: DefaultSidecarAddress}
	WithSidecarAddress("custom:9090")(cfg)

	if cfg.sidecarAddr != "custom:9090" {
		t.Fatalf("expected custom address custom:9090, got %s", cfg.sidecarAddr)
	}
}

func TestClient_WorkitemID(t *testing.T) {
	env := setupTestEnv(t, "wid-123")
	if env.client.WorkitemID() != "wid-123" {
		t.Fatalf("expected WorkitemID wid-123, got %s", env.client.WorkitemID())
	}
}

// ---------------------------------------------------------------------------
// Tests — Metadata Injection (The Critical Path)
// ---------------------------------------------------------------------------

func TestHeartbeat_InjectsWorkitemMetadata(t *testing.T) {
	const wantID = "workitem-abc-789"
	env := setupTestEnv(t, wantID)

	ack, err := env.client.Heartbeat(context.Background())
	if err != nil {
		t.Fatalf("Heartbeat() returned error: %v", err)
	}
	if !ack {
		t.Fatal("Heartbeat() was not acknowledged")
	}

	// THE critical assertion: the interceptor must inject the workitem_id
	// into the gRPC metadata that the server sees.
	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 {
		t.Fatal("metadata x-flow-workitem-id was NOT present in the server-side context — interceptor is broken")
	}
	if got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %q, want %q", got[0], wantID)
	}
}

func TestComplete_InjectsWorkitemMetadata(t *testing.T) {
	const wantID = "workitem-complete-456"
	env := setupTestEnv(t, wantID)

	accepted, err := env.client.Complete(context.Background(), "next-node")
	if err != nil {
		t.Fatalf("Complete() returned error: %v", err)
	}
	if !accepted {
		t.Fatal("Complete() was not accepted")
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 {
		t.Fatal("metadata x-flow-workitem-id was NOT present on SubmitResult call")
	}
	if got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %q, want %q", got[0], wantID)
	}
}

func TestGetArtefact_InjectsWorkitemMetadata(t *testing.T) {
	const wantID = "workitem-artefact-001"
	env := setupTestEnv(t, wantID)

	resp, err := env.client.GetArtefact(context.Background(), "doc-draft")
	if err != nil {
		t.Fatalf("GetArtefact() returned error: %v", err)
	}
	if string(resp.GetContent()) != "test-content" {
		t.Fatalf("unexpected content: %s", resp.GetContent())
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 {
		t.Fatal("metadata x-flow-workitem-id was NOT present on GetArtefact call")
	}
	if got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %q, want %q", got[0], wantID)
	}
}

func TestHeartbeat_EmptyWorkitemID_NoMetadataInjected(t *testing.T) {
	// When workitem ID is empty, the interceptor should NOT inject the header.
	env := setupTestEnv(t, "")

	ack, err := env.client.Heartbeat(context.Background())
	if err != nil {
		t.Fatalf("Heartbeat() returned error: %v", err)
	}
	if !ack {
		t.Fatal("Heartbeat() was not acknowledged")
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) != 0 {
		t.Fatalf("expected no x-flow-workitem-id metadata when workitem ID is empty, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Tests — Librarian Convenience Methods
// ---------------------------------------------------------------------------

func TestQueryLaws_InjectsWorkitemMetadata(t *testing.T) {
	const wantID = "workitem-laws-001"
	env := setupTestEnv(t, wantID)

	laws, err := env.client.QueryLaws(context.Background(), "", "")
	if err != nil {
		t.Fatalf("QueryLaws() returned error: %v", err)
	}
	if len(laws) != 1 {
		t.Fatalf("expected 1 law, got %d", len(laws))
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}
}

func TestCite_InjectsWorkitemMetadata(t *testing.T) {
	const wantID = "workitem-cite-001"
	env := setupTestEnv(t, wantID)

	err := env.client.Cite(context.Background(), "law-1", "law-2")
	if err != nil {
		t.Fatalf("Cite() returned error: %v", err)
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}
}

func TestRecordFinding_InjectsWorkitemMetadata(t *testing.T) {
	const wantID = "workitem-finding-001"
	env := setupTestEnv(t, wantID)

	lawID, err := env.client.RecordFinding(context.Background(), "test goal", []string{"docs"}, []*flowv1.Representation{
		{Type: "text/plain", Content: "test"},
	})
	if err != nil {
		t.Fatalf("RecordFinding() returned error: %v", err)
	}
	if lawID != "finding-001" {
		t.Fatalf("expected law_id=finding-001, got %q", lawID)
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}
}

// ---------------------------------------------------------------------------
// Tests — Monitor Convenience Methods
// ---------------------------------------------------------------------------

func TestRecordTelemetry_InjectsWorkitemMetadata(t *testing.T) {
	const wantID = "workitem-telemetry-001"
	env := setupTestEnv(t, wantID)

	err := env.client.RecordTelemetry(context.Background(), "foundry.cost.llm", []byte(`{"model":"gpt-4"}`))
	if err != nil {
		t.Fatalf("RecordTelemetry() returned error: %v", err)
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}
}

// ---------------------------------------------------------------------------
// Tests — Feedback Reviewer Methods
// ---------------------------------------------------------------------------

func TestRejectFix_InjectsWorkitemMetadata(t *testing.T) {
	const wantID = "workitem-reject-fix-001"
	env := setupTestEnv(t, wantID)

	err := env.client.RejectFix(context.Background(), "fb-001", "fix is incomplete")
	if err != nil {
		t.Fatalf("RejectFix() returned error: %v", err)
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}
}

func TestAcceptRefusal_InjectsWorkitemMetadata(t *testing.T) {
	const wantID = "workitem-accept-refusal-001"
	env := setupTestEnv(t, wantID)

	err := env.client.AcceptRefusal(context.Background(), "fb-002")
	if err != nil {
		t.Fatalf("AcceptRefusal() returned error: %v", err)
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}
}

func TestRejectRefusal_InjectsWorkitemMetadata(t *testing.T) {
	const wantID = "workitem-reject-refusal-001"
	env := setupTestEnv(t, wantID)

	err := env.client.RejectRefusal(context.Background(), "fb-003", "justification is weak")
	if err != nil {
		t.Fatalf("RejectRefusal() returned error: %v", err)
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}
}

func TestRefuseFeedback_InjectsWorkitemMetadata(t *testing.T) {
	const wantID = "workitem-refuse-fb-001"
	env := setupTestEnv(t, wantID)

	justification := &flowv1.Justification{
		Kind: &flowv1.Justification_Citation{
			Citation: &flowv1.Citation{CitationIds: []string{"law-42"}},
		},
	}
	err := env.client.RefuseFeedback(context.Background(), "fb-004", justification)
	if err != nil {
		t.Fatalf("RefuseFeedback() returned error: %v", err)
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}
}

// ---------------------------------------------------------------------------
// Tests — Feedback Deadlock Methods
// ---------------------------------------------------------------------------

func TestGetFeedbackDepth_InjectsWorkitemMetadata(t *testing.T) {
	const wantID = "workitem-depth-001"
	env := setupTestEnv(t, wantID)

	depth, err := env.client.GetFeedbackDepth(context.Background(), "fb-010")
	if err != nil {
		t.Fatalf("GetFeedbackDepth() returned error: %v", err)
	}
	if depth != 5 {
		t.Fatalf("expected depth=5, got %d", depth)
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}
}

func TestDeadlockFeedback_InjectsWorkitemMetadata(t *testing.T) {
	const wantID = "workitem-deadlock-001"
	env := setupTestEnv(t, wantID)

	item, err := env.client.DeadlockFeedback(context.Background(), "fb-011")
	if err != nil {
		t.Fatalf("DeadlockFeedback() returned error: %v", err)
	}
	if item.GetId() != "fb-011" {
		t.Fatalf("expected feedback_id=fb-011, got %q", item.GetId())
	}
	wantState := flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED
	if item.GetState() != wantState {
		t.Fatalf("expected state=%v, got %v", wantState, item.GetState())
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}
}

// ---------------------------------------------------------------------------
// Tests — Topology Convenience Methods
// ---------------------------------------------------------------------------

func TestGetFlowTopology_InjectsWorkitemMetadata(t *testing.T) {
	const wantID = "workitem-topology-001"
	env := setupTestEnv(t, wantID)

	resp, err := env.client.GetFlowTopology(context.Background())
	if err != nil {
		t.Fatalf("GetFlowTopology() returned error: %v", err)
	}

	// Verify response content.
	if resp.GetSelf().GetName() != "test-node" {
		t.Fatalf("expected self.name=test-node, got %s", resp.GetSelf().GetName())
	}
	if len(resp.GetNodes()) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(resp.GetNodes()))
	}
	if len(resp.GetExitContract()) != 1 {
		t.Fatalf("expected 1 exit contract kind, got %d", len(resp.GetExitContract()))
	}
	docStamps := resp.GetExitContract()["doc"]
	if docStamps == nil {
		t.Fatal("expected doc in exit contract")
	}
	if len(docStamps.GetStamps()) != 2 {
		t.Fatalf("expected 2 stamps in doc exit contract, got %d", len(docStamps.GetStamps()))
	}

	// Verify metadata injection.
	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}
}

// ---------------------------------------------------------------------------
// Tests — LinkRuling Convenience Method
// ---------------------------------------------------------------------------

func TestLinkRuling_InjectsWorkitemMetadata(t *testing.T) {
	const wantID = "workitem-linkruling-001"
	env := setupTestEnv(t, wantID)

	item, err := env.client.LinkRuling(
		context.Background(), "fb-dead-001", "law-ruling-001",
		flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
	)
	if err != nil {
		t.Fatalf("LinkRuling() returned error: %v", err)
	}
	if item.GetId() != "fb-dead-001" {
		t.Fatalf("expected feedback_id=fb-dead-001, got %q", item.GetId())
	}
	if item.GetLinkedRuling() != "law-ruling-001" {
		t.Fatalf("expected linked_ruling=law-ruling-001, got %q", item.GetLinkedRuling())
	}
	wantState := flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX
	if item.GetState() != wantState {
		t.Fatalf("expected state=%v, got %v", wantState, item.GetState())
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}
}

// ---------------------------------------------------------------------------
// Tests — QueryFriction Convenience Method
// ---------------------------------------------------------------------------

func TestQueryFriction_InjectsWorkitemMetadata(t *testing.T) {
	const wantID = "workitem-friction-001"
	env := setupTestEnv(t, wantID)

	aggregates, err := env.client.QueryFriction(
		context.Background(), &flowv1.FrictionFilter{LawId: "law-friction-001"},
	)
	if err != nil {
		t.Fatalf("QueryFriction() returned error: %v", err)
	}
	if len(aggregates) != 1 {
		t.Fatalf("expected 1 aggregate, got %d", len(aggregates))
	}
	if aggregates[0].GetLawId() != "law-friction-001" {
		t.Fatalf("expected law_id=law-friction-001, got %q", aggregates[0].GetLawId())
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}
}

// ---------------------------------------------------------------------------
// Tests — GetLaw Convenience Method
// ---------------------------------------------------------------------------

func TestGetLaw_InjectsWorkitemMetadata(t *testing.T) {
	const wantID = "workitem-getlaw-001"
	env := setupTestEnv(t, wantID)

	law, err := env.client.GetLaw(context.Background(), "law-getlaw-001")
	if err != nil {
		t.Fatalf("GetLaw() returned error: %v", err)
	}
	if law.GetId() != "law-getlaw-001" {
		t.Fatalf("expected law_id=law-getlaw-001, got %q", law.GetId())
	}
	if law.GetGoal() != "test goal" {
		t.Fatalf("expected goal=test goal, got %q", law.GetGoal())
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}
}
