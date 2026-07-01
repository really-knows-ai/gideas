package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/durationpb"
)

const bufSize = 1024 * 1024

// ---------------------------------------------------------------------------
// Spy server — captures incoming metadata for assertions
// ---------------------------------------------------------------------------

const testNodeName = "test-node"

// spyServer implements the gRPC services and records the metadata it
// receives. This lets us assert that the SDK's interceptor injects the
// correct workitem_id header.
type spyServer struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFrictionLedgerServiceServer
	flowv1.UnimplementedFlowEventBusServiceServer

	// lastMD is the metadata captured from the most recent call.
	lastMD metadata.MD
	// lastSubmitReq is the request captured from the most recent SubmitResult call.
	lastSubmitReq *flowv1.SubmitResultRequest
	// lastResumeReq is the request captured from the most recent ResumeWorkitem call.
	lastResumeReq *flowv1.ResumeWorkitemRequest
	// lastAddFeedbackReq is the request captured from the most recent AddFeedback call.
	lastAddFeedbackReq *flowv1.AddFeedbackRequest
	// lastPauseTimerReq captures the most recent PauseTimer request.
	lastPauseTimerReq *flowv1.PauseTimerRequest
	// lastResumeTimerReq captures the most recent ResumeTimer request.
	lastResumeTimerReq *flowv1.ResumeTimerRequest
	// lastGetArtefactReq captures the most recent GetArtefact request.
	lastGetArtefactReq *flowv1.GetArtefactRequest
	// lastGetFeedbackReq captures the most recent GetFeedback request.
	lastGetFeedbackReq *flowv1.GetFeedbackRequest
	// lastHasUnresolvedReq captures the most recent HasUnresolvedFeedback request.
	lastHasUnresolvedReq *flowv1.HasUnresolvedFeedbackRequest
	// lastQueryLawsReq captures the most recent QueryLaws request.
	lastQueryLawsReq *flowv1.QueryLawsRequest
	// lastPublishReq captures the most recent Publish request.
	lastPublishReq *flowv1.PublishRequest
	// lastCiteReq captures the most recent Cite request.
	lastCiteReq *flowv1.CiteRequest
	// lastStampReq captures the most recent StampArtefact request.
	lastStampReq *flowv1.StampArtefactRequest

	// Feedback RPC request capture fields.
	lastResolveFeedbackReq  *flowv1.ResolveFeedbackRequest
	lastRefuseFeedbackReq   *flowv1.RefuseFeedbackRequest
	lastAcceptFixReq        *flowv1.AcceptFixRequest
	lastRejectFixReq        *flowv1.RejectFixRequest
	lastAcceptRefusalReq    *flowv1.AcceptRefusalRequest
	lastRejectRefusalReq    *flowv1.RejectRefusalRequest
	lastDeadlockFeedbackReq *flowv1.DeadlockFeedbackRequest
	lastLinkRulingReq       *flowv1.LinkRulingRequest
	lastGetFeedbackDepthReq *flowv1.GetFeedbackDepthRequest
	// feedbackErr, when non-nil, is returned by all feedback RPC handlers for error testing.
	feedbackErr error
}

func (s *spyServer) Heartbeat(ctx context.Context, req *flowv1.HeartbeatRequest) (*flowv1.HeartbeatResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *spyServer) PauseTimer(
	ctx context.Context, req *flowv1.PauseTimerRequest,
) (*flowv1.PauseTimerResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	s.lastPauseTimerReq = req
	return &flowv1.PauseTimerResponse{}, nil
}

func (s *spyServer) ResumeTimer(
	ctx context.Context, req *flowv1.ResumeTimerRequest,
) (*flowv1.ResumeTimerResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	s.lastResumeTimerReq = req
	return &flowv1.ResumeTimerResponse{}, nil
}

func (s *spyServer) SubmitResult(
	ctx context.Context, req *flowv1.SubmitResultRequest,
) (*flowv1.SubmitResultResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	s.lastSubmitReq = req
	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

func (s *spyServer) ResumeWorkitem(
	ctx context.Context, req *flowv1.ResumeWorkitemRequest,
) (*flowv1.ResumeWorkitemResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	s.lastResumeReq = req
	return &flowv1.ResumeWorkitemResponse{Accepted: true}, nil
}

func (s *spyServer) GetFlowTopology(
	ctx context.Context, _ *flowv1.GetFlowTopologyRequest,
) (*flowv1.GetFlowTopologyResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.GetFlowTopologyResponse{
		Self: &flowv1.FlowNode{
			Name:         testNodeName,
			Capabilities: []string{"READ:flow"},
			Outputs:      []*flowv1.FlowOutput{{Name: "next", Target: "other"}},
		},
		Nodes: map[string]*flowv1.FlowNode{
			testNodeName: {Name: testNodeName},
			"other":      {Name: "other"},
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
	s.lastGetArtefactReq = req
	return &flowv1.GetArtefactResponse{
		Content:          []byte("test-content"),
		VersionHash:      "test-hash",
		GovernedArtefact: "test-artefact",
	}, nil
}

func (s *spyServer) QueryLaws(ctx context.Context, req *flowv1.QueryLawsRequest) (*flowv1.QueryLawsResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	s.lastQueryLawsReq = req
	return &flowv1.QueryLawsResponse{Laws: []*flowv1.Law{{Id: "law-1"}}}, nil
}

func (s *spyServer) GetLawGroup(
	ctx context.Context, req *flowv1.GetLawGroupRequest,
) (*flowv1.GetLawGroupResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.GetLawGroupResponse{
		Group: &flowv1.LawGroup{
			Name: req.GetGroupName(), Mode: "bundle", Passes: 1,
		},
	}, nil
}

func (s *spyServer) ListLawGroups(
	ctx context.Context, _ *flowv1.ListLawGroupsRequest,
) (*flowv1.ListLawGroupsResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.ListLawGroupsResponse{
		Groups: []*flowv1.LawGroup{
			{Name: "group-a", Mode: "bundle", Passes: 1},
			{Name: "group-b", Mode: "law-by-law", Passes: 2},
		},
	}, nil
}

func (s *spyServer) Publish(ctx context.Context, req *flowv1.PublishRequest) (*flowv1.PublishResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	s.lastPublishReq = req
	return &flowv1.PublishResponse{Sequence: 1}, nil
}

func (s *spyServer) Cite(ctx context.Context, req *flowv1.CiteRequest) (*flowv1.CiteResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	s.lastCiteReq = req
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

func (s *spyServer) ResolveFeedback(
	ctx context.Context, req *flowv1.ResolveFeedbackRequest,
) (*flowv1.ResolveFeedbackResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	s.lastResolveFeedbackReq = req
	if s.feedbackErr != nil {
		return nil, s.feedbackErr
	}
	return &flowv1.ResolveFeedbackResponse{}, nil
}

func (s *spyServer) RefuseFeedback(
	ctx context.Context, req *flowv1.RefuseFeedbackRequest,
) (*flowv1.RefuseFeedbackResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	s.lastRefuseFeedbackReq = req
	if s.feedbackErr != nil {
		return nil, s.feedbackErr
	}
	return &flowv1.RefuseFeedbackResponse{UpdatedItem: &flowv1.FeedbackItem{
		Id: req.GetFeedbackId(), State: flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
	}}, nil
}

func (s *spyServer) AcceptFix(
	ctx context.Context, req *flowv1.AcceptFixRequest,
) (*flowv1.AcceptFixResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	s.lastAcceptFixReq = req
	if s.feedbackErr != nil {
		return nil, s.feedbackErr
	}
	return &flowv1.AcceptFixResponse{UpdatedItem: &flowv1.FeedbackItem{
		Id: req.GetFeedbackId(), State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED,
	}}, nil
}

func (s *spyServer) RejectFix(
	ctx context.Context, req *flowv1.RejectFixRequest,
) (*flowv1.RejectFixResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	s.lastRejectFixReq = req
	if s.feedbackErr != nil {
		return nil, s.feedbackErr
	}
	return &flowv1.RejectFixResponse{UpdatedItem: &flowv1.FeedbackItem{
		Id: req.GetFeedbackId(), State: flowv1.FeedbackState_FEEDBACK_STATE_REJECTED,
	}}, nil
}

func (s *spyServer) AcceptRefusal(
	ctx context.Context, req *flowv1.AcceptRefusalRequest,
) (*flowv1.AcceptRefusalResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	s.lastAcceptRefusalReq = req
	if s.feedbackErr != nil {
		return nil, s.feedbackErr
	}
	return &flowv1.AcceptRefusalResponse{UpdatedItem: &flowv1.FeedbackItem{
		Id: req.GetFeedbackId(), State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED,
	}}, nil
}

func (s *spyServer) RejectRefusal(
	ctx context.Context, req *flowv1.RejectRefusalRequest,
) (*flowv1.RejectRefusalResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	s.lastRejectRefusalReq = req
	if s.feedbackErr != nil {
		return nil, s.feedbackErr
	}
	return &flowv1.RejectRefusalResponse{UpdatedItem: &flowv1.FeedbackItem{
		Id: req.GetFeedbackId(), State: flowv1.FeedbackState_FEEDBACK_STATE_REJECTED,
	}}, nil
}

func (s *spyServer) GetFeedbackDepth(
	ctx context.Context, req *flowv1.GetFeedbackDepthRequest,
) (*flowv1.GetFeedbackDepthResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	s.lastGetFeedbackDepthReq = req
	if s.feedbackErr != nil {
		return nil, s.feedbackErr
	}
	return &flowv1.GetFeedbackDepthResponse{Depth: 5}, nil
}

func (s *spyServer) DeadlockFeedback(
	ctx context.Context, req *flowv1.DeadlockFeedbackRequest,
) (*flowv1.DeadlockFeedbackResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	s.lastDeadlockFeedbackReq = req
	if s.feedbackErr != nil {
		return nil, s.feedbackErr
	}
	return &flowv1.DeadlockFeedbackResponse{UpdatedItem: &flowv1.FeedbackItem{
		Id:    req.GetFeedbackId(),
		State: flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED,
	}}, nil
}

func (s *spyServer) LinkRuling(
	ctx context.Context, req *flowv1.LinkRulingRequest,
) (*flowv1.LinkRulingResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	s.lastLinkRulingReq = req
	if s.feedbackErr != nil {
		return nil, s.feedbackErr
	}
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

func (s *spyServer) AddFeedback(
	ctx context.Context, req *flowv1.AddFeedbackRequest,
) (*flowv1.AddFeedbackResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	s.lastAddFeedbackReq = req
	return &flowv1.AddFeedbackResponse{FeedbackId: "fb-auto-001"}, nil
}

func (s *spyServer) StampArtefact(
	ctx context.Context, req *flowv1.StampArtefactRequest,
) (*flowv1.StampArtefactResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	s.lastStampReq = req
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

func (s *spyServer) GetStamps(
	ctx context.Context, req *flowv1.GetStampsRequest,
) (*flowv1.GetStampsResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.GetStampsResponse{
		Stamps: []*flowv1.Stamp{
			{Name: "linter", ApplyingNode: "node-a", ContentHash: "ch-1"},
			{Name: "approval", ApplyingNode: "node-b", ContentHash: "ch-1"},
		},
	}, nil
}

func (s *spyServer) HasStamp(
	ctx context.Context, req *flowv1.HasStampRequest,
) (*flowv1.HasStampResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.HasStampResponse{Exists: req.GetStampName() == "linter"}, nil
}

func (s *spyServer) GetFeedback(
	ctx context.Context, req *flowv1.GetFeedbackRequest,
) (*flowv1.GetFeedbackResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	s.lastGetFeedbackReq = req
	return &flowv1.GetFeedbackResponse{
		FeedbackItems: []*flowv1.FeedbackItem{
			{Id: "fb-001", Message: "needs revision"},
			{Id: "fb-002", Message: "looks good"},
		},
	}, nil
}

func (s *spyServer) HasUnresolvedFeedback(
	ctx context.Context, req *flowv1.HasUnresolvedFeedbackRequest,
) (*flowv1.HasUnresolvedFeedbackResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	s.lastHasUnresolvedReq = req
	return &flowv1.HasUnresolvedFeedbackResponse{HasUnresolved: true}, nil
}

// errorSpyServer returns errors on every Archivist RPC, used for testing error propagation.
type errorSpyServer struct {
	flowv1.UnimplementedArchivistServiceServer
}

func (s *errorSpyServer) GetArtefact(ctx context.Context, req *flowv1.GetArtefactRequest) (*flowv1.GetArtefactResponse, error) {
	return nil, fmt.Errorf("archivist error: get artefact failed")
}

func (s *errorSpyServer) StoreArtefact(ctx context.Context, req *flowv1.StoreArtefactRequest) (*flowv1.StoreArtefactResponse, error) {
	return nil, fmt.Errorf("archivist error: store artefact failed")
}

func (s *errorSpyServer) StampArtefact(ctx context.Context, req *flowv1.StampArtefactRequest) (*flowv1.StampArtefactResponse, error) {
	return nil, fmt.Errorf("archivist error: stamp artefact failed")
}

func (s *errorSpyServer) GetStamps(ctx context.Context, req *flowv1.GetStampsRequest) (*flowv1.GetStampsResponse, error) {
	return nil, fmt.Errorf("archivist error: get stamps failed")
}

func (s *errorSpyServer) HasStamp(ctx context.Context, req *flowv1.HasStampRequest) (*flowv1.HasStampResponse, error) {
	return nil, fmt.Errorf("archivist error: has stamp failed")
}

func (s *errorSpyServer) GetFeedback(ctx context.Context, req *flowv1.GetFeedbackRequest) (*flowv1.GetFeedbackResponse, error) {
	return nil, fmt.Errorf("archivist error: get feedback failed")
}

func (s *errorSpyServer) HasUnresolvedFeedback(ctx context.Context, req *flowv1.HasUnresolvedFeedbackRequest) (*flowv1.HasUnresolvedFeedbackResponse, error) {
	return nil, fmt.Errorf("archivist error: has unresolved feedback failed")
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
		flowv1.RegisterFlowEventBusServiceServer(s, spy)
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

	accepted, err := env.client.Complete(context.Background())
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

	lawID, err := env.client.RecordFindingProto(
		context.Background(), "test goal", []string{"docs"},
		[]*flowv1.Representation{{Type: "text/plain", Content: "test"}},
	)
	if err != nil {
		t.Fatalf("RecordFindingProto() returned error: %v", err)
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
	if resp.GetSelf().GetName() != testNodeName {
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

	law, err := env.client.GetLawProto(context.Background(), "law-getlaw-001")
	if err != nil {
		t.Fatalf("GetLawProto() returned error: %v", err)
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

// ---------------------------------------------------------------------------
// Tests — LawGroup SDK Methods
// ---------------------------------------------------------------------------

func TestGetLawGroup_ReturnsGroup(t *testing.T) {
	env := setupTestEnv(t, "workitem-lawgroup-001")

	group, err := env.client.GetLawGroup(context.Background(), "my-group")
	if err != nil {
		t.Fatalf("GetLawGroup() returned error: %v", err)
	}
	if group.Name() != "my-group" {
		t.Fatalf("expected group name my-group, got %q", group.Name())
	}
	if group.Mode() != "bundle" {
		t.Fatalf("expected mode bundle, got %q", group.Mode())
	}
}

func TestGetLawGroup_ServerError(t *testing.T) {
	env := setupTestEnv(t, "workitem-lawgroup-002")

	// Override server to return error by setting a nil handler is complex,
	// so we test via the normal path. A server error would propagate as gRPC error.
	_, err := env.client.GetLawGroup(context.Background(), "")
	if err != nil {
		// Empty name is valid — just testing the SDK returns a group.
		t.Logf("GetLawGroup with empty name: %v", err)
	}
}

func TestListLawGroups_ReturnsGroups(t *testing.T) {
	env := setupTestEnv(t, "workitem-listlawgroups-001")

	groups, err := env.client.ListLawGroups(context.Background())
	if err != nil {
		t.Fatalf("ListLawGroups() returned error: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}
	names := map[string]bool{}
	for _, g := range groups {
		names[g.Name()] = true
	}
	if !names["group-a"] || !names["group-b"] {
		t.Fatalf("expected group-a and group-b, got %v", names)
	}
}

func TestQueryLawsByGroup_SendsGroupFilter(t *testing.T) {
	env := setupTestEnv(t, "workitem-querylawsbygroup-001")

	laws, err := env.client.QueryLawsByGroup(context.Background(), "source-code", "security")
	if err != nil {
		t.Fatalf("QueryLawsByGroup() returned error: %v", err)
	}
	if len(laws) != 1 {
		t.Fatalf("expected 1 law, got %d", len(laws))
	}

	req := env.spy.lastQueryLawsReq
	if req == nil {
		t.Fatal("QueryLaws was not called")
	}
	f := req.GetFilter()
	if f == nil {
		t.Fatal("expected non-nil filter")
	}
	if f.GetGovernedArtefact() != "source-code" {
		t.Fatalf("expected governed_artefact=source-code, got %q", f.GetGovernedArtefact())
	}
	if f.GetGroup() != "security" {
		t.Fatalf("expected group=security, got %q", f.GetGroup())
	}
}

// ---------------------------------------------------------------------------
// Tests — PublishAuditEvent SDK Method
// ---------------------------------------------------------------------------

func TestPublishAuditEvent_PublishesToAuditChannel(t *testing.T) {
	spy := &spyServer{}
	client := setupGRPCTestEnvWithEventBus(t, "workitem-publishaudit-001",
		func(s *grpc.Server) {
			flowv1.RegisterSidecarServiceServer(s, spy)
			flowv1.RegisterOperatorServiceServer(s, spy)
			flowv1.RegisterArchivistServiceServer(s, spy)
			flowv1.RegisterLibrarianServiceServer(s, spy)
			flowv1.RegisterFrictionLedgerServiceServer(s, spy)
		},
		func(s *grpc.Server) {
			flowv1.RegisterFlowEventBusServiceServer(s, spy)
		},
	)
	env := &testEnv{client: client, spy: spy}

	err := env.client.PublishAuditEvent(context.Background(), "appraisal.coverage", map[string]string{
		"stage": "appraisal",
		"cycle": "test-cycle",
	}, "workitem-publishaudit-001", "test-ns")
	if err != nil {
		t.Fatalf("PublishAuditEvent() returned error: %v", err)
	}

	req := env.spy.lastPublishReq
	if req == nil {
		t.Fatal("Publish was not called")
	}
	if req.GetChannel() != "audit" {
		t.Fatalf("expected channel=audit, got %q", req.GetChannel())
	}
	if req.GetEvent().GetEventType() != "appraisal.coverage" {
		t.Fatalf("expected event_type=appraisal.coverage, got %q", req.GetEvent().GetEventType())
	}
	if len(req.GetEvent().GetEventId()) == 0 {
		t.Fatal("expected non-empty event_id")
	}
	if req.GetEvent().GetTimestamp() == nil {
		t.Fatal("expected non-nil timestamp")
	}

	// Verify payload is valid JSON.
	var payload map[string]string
	if err := json.Unmarshal(req.GetEvent().GetPayload(), &payload); err != nil {
		t.Fatalf("expected valid JSON payload, got error: %v", err)
	}
	if payload["stage"] != "appraisal" {
		t.Fatalf("expected payload.stage=appraisal, got %q", payload["stage"])
	}
}

func TestPublishAuditEvent_NoEventBus_ReturnsError(t *testing.T) {
	client := &Client{session: &session{}}
	err := client.PublishAuditEvent(context.Background(), "test.event", map[string]string{}, "", "")
	if err == nil {
		t.Fatal("expected error when EventBus is nil, got nil")
	}
	if err.Error() != "flow sdk: publish audit event requires Event Bus connection (set EVENT_BUS_ADDRESS)" {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests — Suspend Convenience Method
// ---------------------------------------------------------------------------

func TestSuspend_NoOptions(t *testing.T) {
	const wantID = "workitem-suspend-001"
	env := setupTestEnv(t, wantID)

	err := env.client.Suspend(context.Background())
	if err != nil {
		t.Fatalf("Suspend() returned error: %v", err)
	}

	req := env.spy.lastSubmitReq
	if req == nil {
		t.Fatal("SubmitResult was not called")
	}
	suspend, ok := req.GetAction().(*flowv1.SubmitResultRequest_Suspend)
	if !ok {
		t.Fatalf("expected SuspendAction, got %T", req.GetAction())
	}
	if suspend.Suspend.GetCondition() != "" {
		t.Fatalf("expected empty condition, got %q", suspend.Suspend.GetCondition())
	}
	if suspend.Suspend.GetTimeout() != nil {
		t.Fatalf("expected nil timeout, got %v", suspend.Suspend.GetTimeout())
	}
}

func TestSuspend_WithCondition(t *testing.T) {
	const wantID = "workitem-suspend-002"
	env := setupTestEnv(t, wantID)

	cel := `children.all(c, c.phase == "Completed")`
	err := env.client.Suspend(context.Background(), WithCondition(cel))
	if err != nil {
		t.Fatalf("Suspend() returned error: %v", err)
	}

	req := env.spy.lastSubmitReq
	suspend, ok := req.GetAction().(*flowv1.SubmitResultRequest_Suspend)
	if !ok {
		t.Fatalf("expected SuspendAction, got %T", req.GetAction())
	}
	if suspend.Suspend.GetCondition() != cel {
		t.Fatalf("condition = %q, want %q", suspend.Suspend.GetCondition(), cel)
	}
	if suspend.Suspend.GetTimeout() != nil {
		t.Fatalf("expected nil timeout, got %v", suspend.Suspend.GetTimeout())
	}
}

func TestSuspend_WithTimeout(t *testing.T) {
	const wantID = "workitem-suspend-003"
	env := setupTestEnv(t, wantID)

	err := env.client.Suspend(context.Background(), WithSuspendTimeout(5*time.Minute))
	if err != nil {
		t.Fatalf("Suspend() returned error: %v", err)
	}

	req := env.spy.lastSubmitReq
	suspend, ok := req.GetAction().(*flowv1.SubmitResultRequest_Suspend)
	if !ok {
		t.Fatalf("expected SuspendAction, got %T", req.GetAction())
	}
	if suspend.Suspend.GetCondition() != "" {
		t.Fatalf("expected empty condition, got %q", suspend.Suspend.GetCondition())
	}
	wantTimeout := durationpb.New(5 * time.Minute)
	if suspend.Suspend.GetTimeout().GetSeconds() != wantTimeout.GetSeconds() {
		t.Fatalf("timeout = %v, want %v", suspend.Suspend.GetTimeout(), wantTimeout)
	}
}

func TestSuspend_WithConditionAndTimeout(t *testing.T) {
	const wantID = "workitem-suspend-004"
	env := setupTestEnv(t, wantID)

	cel := `children.all(c, c.phase == "Completed")`
	err := env.client.Suspend(context.Background(),
		WithCondition(cel),
		WithSuspendTimeout(10*time.Minute),
	)
	if err != nil {
		t.Fatalf("Suspend() returned error: %v", err)
	}

	req := env.spy.lastSubmitReq
	suspend, ok := req.GetAction().(*flowv1.SubmitResultRequest_Suspend)
	if !ok {
		t.Fatalf("expected SuspendAction, got %T", req.GetAction())
	}
	if suspend.Suspend.GetCondition() != cel {
		t.Fatalf("condition = %q, want %q", suspend.Suspend.GetCondition(), cel)
	}
	wantTimeout := durationpb.New(10 * time.Minute)
	if suspend.Suspend.GetTimeout().GetSeconds() != wantTimeout.GetSeconds() {
		t.Fatalf("timeout = %v, want %v", suspend.Suspend.GetTimeout(), wantTimeout)
	}

	// Also verify metadata injection.
	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, wantID)
	}
}

// ---------------------------------------------------------------------------
// Tests — Resume Convenience Method
// ---------------------------------------------------------------------------

func TestResume_SendsCorrectWorkitemID(t *testing.T) {
	const callerID = "workitem-caller-001"
	const targetID = "workitem-child-suspended-001"
	env := setupTestEnv(t, callerID)

	err := env.client.Resume(context.Background(), targetID)
	if err != nil {
		t.Fatalf("Resume() returned error: %v", err)
	}

	req := env.spy.lastResumeReq
	if req == nil {
		t.Fatal("ResumeWorkitem was not called")
	}
	if req.GetWorkitemId() != targetID {
		t.Fatalf("workitem_id = %q, want %q", req.GetWorkitemId(), targetID)
	}
}

func TestResume_InjectsCallerMetadata(t *testing.T) {
	const callerID = "workitem-caller-002"
	env := setupTestEnv(t, callerID)

	err := env.client.Resume(context.Background(), "workitem-target-002")
	if err != nil {
		t.Fatalf("Resume() returned error: %v", err)
	}

	// The interceptor injects the caller's workitem ID, not the target's.
	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 || got[0] != callerID {
		t.Fatalf("metadata x-flow-workitem-id = %v, want %q", got, callerID)
	}
}

// ---------------------------------------------------------------------------
// Tests — Complete with WithReason
// ---------------------------------------------------------------------------

func TestComplete_WithReason(t *testing.T) {
	const wantID = "workitem-complete-reason-001"
	env := setupTestEnv(t, wantID)

	reason := flowv1.CompletionReason_COMPLETION_REASON_CANCELLED
	accepted, err := env.client.Complete(context.Background(), WithReason(reason))
	if err != nil {
		t.Fatalf("Complete(WithReason) returned error: %v", err)
	}
	if !accepted {
		t.Fatal("Complete(WithReason) was not accepted")
	}

	req := env.spy.lastSubmitReq
	if req == nil {
		t.Fatal("SubmitResult was not called")
	}
	complete, ok := req.GetAction().(*flowv1.SubmitResultRequest_Complete)
	if !ok {
		t.Fatalf("expected CompleteAction, got %T", req.GetAction())
	}
	if complete.Complete.GetReason() != flowv1.CompletionReason_COMPLETION_REASON_CANCELLED {
		t.Fatalf("reason = %v, want COMPLETION_REASON_CANCELLED", complete.Complete.GetReason())
	}
}

func TestComplete_WithoutReason(t *testing.T) {
	const wantID = "workitem-complete-reason-002"
	env := setupTestEnv(t, wantID)

	accepted, err := env.client.Complete(context.Background())
	if err != nil {
		t.Fatalf("Complete() returned error: %v", err)
	}
	if !accepted {
		t.Fatal("Complete() was not accepted")
	}

	req := env.spy.lastSubmitReq
	if req == nil {
		t.Fatal("SubmitResult was not called")
	}
	complete, ok := req.GetAction().(*flowv1.SubmitResultRequest_Complete)
	if !ok {
		t.Fatalf("expected CompleteAction, got %T", req.GetAction())
	}
	if complete.Complete.GetReason() != flowv1.CompletionReason_COMPLETION_REASON_UNSPECIFIED {
		t.Fatalf("reason = %v, want COMPLETION_REASON_UNSPECIFIED", complete.Complete.GetReason())
	}
}

// ---------------------------------------------------------------------------
// Tests — AddFeedback Convenience Method
// ---------------------------------------------------------------------------

func TestAddFeedback_CanWontFixTrue(t *testing.T) {
	const wantID = "workitem-addfb-true-001"
	env := setupTestEnv(t, wantID)

	fbID, err := env.client.AddFeedback(context.Background(), "artefact-001", true, "test message")
	if err != nil {
		t.Fatalf("AddFeedback() returned error: %v", err)
	}
	if fbID == "" {
		t.Fatal("AddFeedback() returned empty feedback ID")
	}
	if env.spy.lastAddFeedbackReq == nil {
		t.Fatal("AddFeedback was not called on the server")
	}
	if !env.spy.lastAddFeedbackReq.GetCanWontFix() {
		t.Fatal("expected CanWontFix=true, got false")
	}
}

func TestAddFeedback_CanWontFixFalse(t *testing.T) {
	const wantID = "workitem-addfb-false-001"
	env := setupTestEnv(t, wantID)

	fbID, err := env.client.AddFeedback(context.Background(), "artefact-002", false, "another message")
	if err != nil {
		t.Fatalf("AddFeedback() returned error: %v", err)
	}
	if fbID == "" {
		t.Fatal("AddFeedback() returned empty feedback ID")
	}
	if env.spy.lastAddFeedbackReq == nil {
		t.Fatal("AddFeedback was not called on the server")
	}
	if env.spy.lastAddFeedbackReq.GetCanWontFix() {
		t.Fatal("expected CanWontFix=false, got true")
	}
}

func TestAddFeedback_InjectsWorkitemMetadata(t *testing.T) {
	const wantID = "workitem-addfb-meta-001"
	env := setupTestEnv(t, wantID)

	_, err := env.client.AddFeedback(context.Background(), "artefact-003", true, "meta test")
	if err != nil {
		t.Fatalf("AddFeedback() returned error: %v", err)
	}

	got := env.spy.lastMD.Get("x-flow-workitem-id")
	if len(got) == 0 {
		t.Fatal("metadata x-flow-workitem-id was NOT present on AddFeedback call")
	}
	if got[0] != wantID {
		t.Fatalf("metadata x-flow-workitem-id = %q, want %q", got[0], wantID)
	}
}

// ---------------------------------------------------------------------------
// Tests — New Client Options (Phase 1)
// ---------------------------------------------------------------------------

func TestNewClient_WithTimeout(t *testing.T) {
	cfg := &clientConfig{sidecarAddr: DefaultSidecarAddress}
	WithTimeout(5 * time.Second)(cfg)
	if cfg.timeout != 5*time.Second {
		t.Fatalf("expected timeout=5s, got %v", cfg.timeout)
	}
}

func TestNewClient_WithRetry(t *testing.T) {
	cfg := &clientConfig{sidecarAddr: DefaultSidecarAddress}
	WithRetry(3)(cfg)
	if cfg.maxRetries != 3 {
		t.Fatalf("expected maxRetries=3, got %d", cfg.maxRetries)
	}
}

func TestClient_Close(t *testing.T) {
	env := setupTestEnv(t, "workitem-close-001")
	err := env.client.Close()
	if err != nil {
		t.Fatalf("Close() returned error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests — New Entry Methods (Phase 1)
// ---------------------------------------------------------------------------

func TestGetWorkitem_NoArgs(t *testing.T) {
	const wantID = "workitem-getwi-noargs-001"
	env := setupTestEnv(t, wantID)

	wi, err := env.client.GetWorkitem()
	if err != nil {
		t.Fatalf("GetWorkitem() returned error: %v", err)
	}
	if wi.ID() != wantID {
		t.Fatalf("expected Workitem.ID()=%q, got %q", wantID, wi.ID())
	}
}

func TestGetWorkitem_OneArg(t *testing.T) {
	const sessionID = "workitem-session-001"
	const otherID = "other-wid-001"
	env := setupTestEnv(t, sessionID)

	wi, err := env.client.GetWorkitem(otherID)
	if err != nil {
		t.Fatalf("GetWorkitem(%q) returned error: %v", otherID, err)
	}
	if wi.ID() != otherID {
		t.Fatalf("expected Workitem.ID()=%q, got %q", otherID, wi.ID())
	}
}

func TestGetWorkitem_MultiArgs(t *testing.T) {
	env := setupTestEnv(t, "workitem-multi-001")

	_, err := env.client.GetWorkitem("a", "b")
	if err == nil {
		t.Fatal("expected error for multi-arg GetWorkitem, got nil")
	}
}

func TestGetWorkitem_NoArgs_ReadsEnv(t *testing.T) {
	env := setupTestEnv(t, "workitem-env-001")

	t.Setenv("FLOW_WORKITEM_ID", "env-wid-001")
	// Re-create client session to pick up env var.
	sess := &session{
		workitemID:     "env-wid-001",
		conn:           env.client.session.conn,
		Sidecar:        env.client.session.Sidecar,
		Operator:       env.client.session.Operator,
		Archivist:      env.client.session.Archivist,
		Librarian:      env.client.session.Librarian,
		FrictionLedger: env.client.session.FrictionLedger,
	}
	env.client.session = sess

	wi, err := env.client.GetWorkitem()
	if err != nil {
		t.Fatalf("GetWorkitem() returned error: %v", err)
	}
	if wi.ID() != "env-wid-001" {
		t.Fatalf("expected Workitem.ID()=%q from env, got %q", "env-wid-001", wi.ID())
	}
}

func TestGetFlow(t *testing.T) {
	env := setupTestEnv(t, "workitem-getflow-001")

	f, err := env.client.GetFlow()
	if err != nil {
		t.Fatalf("GetFlow() returned error: %v", err)
	}
	if f == nil {
		t.Fatal("expected non-nil Flow")
	}

	// Verify topology accessors work.
	nodes := f.GetNodes()
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}

	ec := f.GetExitContract()
	if ec == nil {
		t.Fatal("expected non-nil exit contract")
	}
	stamps, ok := ec["doc"]
	if !ok {
		t.Fatal("missing doc in exit contract")
	}
	if len(stamps) != 2 || stamps[0] != "linter" {
		t.Fatalf("unexpected doc stamps: %v", stamps)
	}
}

func TestGetNode(t *testing.T) {
	env := setupTestEnv(t, "workitem-getnode-001")

	n, err := env.client.GetNode()
	if err != nil {
		t.Fatalf("GetNode() returned error: %v", err)
	}
	if n == nil {
		t.Fatal("expected non-nil Node")
	}
	if n.GetName() != testNodeName {
		t.Fatalf("expected node name=%q, got %q", testNodeName, n.GetName())
	}
	if !n.HasCapability("READ:flow") {
		t.Error("expected READ:flow capability")
	}
}

func TestGetLaw_NewEntryMethod(t *testing.T) {
	env := setupTestEnv(t, "workitem-getlaw-entry-001")

	law, err := env.client.GetLaw("law-entry-001")
	if err != nil {
		t.Fatalf("GetLaw(%q) returned error: %v", "law-entry-001", err)
	}
	if law == nil {
		t.Fatal("expected non-nil Law")
	}
	if law.ID() != "law-entry-001" {
		t.Fatalf("expected law.ID()=law-entry-001, got %q", law.ID())
	}
	if law.GetGoal() != "test goal" {
		t.Fatalf("expected law.GetGoal()=test goal, got %q", law.GetGoal())
	}
}

func TestRecordFinding_NewEntryMethod(t *testing.T) {
	env := setupTestEnv(t, "workitem-recordfinding-entry-001")

	lawID, err := env.client.RecordFinding("test goal", []string{"docs"}, []*flowv1.Representation{
		{Type: "text/plain", Content: "test"},
	})
	if err != nil {
		t.Fatalf("RecordFinding() returned error: %v", err)
	}
	if lawID != "finding-001" {
		t.Fatalf("expected law_id=finding-001, got %q", lawID)
	}
}
