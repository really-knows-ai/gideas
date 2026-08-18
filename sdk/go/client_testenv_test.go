package flow

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
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
	// lastListLawGroupsReq captures the most recent ListLawGroups request.
	lastListLawGroupsReq *flowv1.ListLawGroupsRequest

	// queryLawsResp, when non-nil, overrides the default QueryLaws response.
	queryLawsResp *flowv1.QueryLawsResponse

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
			"doc": {Stamps: []string{stampLinter, "approval"}},
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
	if s.queryLawsResp != nil {
		return s.queryLawsResp, nil
	}
	return &flowv1.QueryLawsResponse{Laws: []*flowv1.Law{{
		Id:              "law-1",
		Representations: []*flowv1.Representation{{Type: "text/markdown"}},
	}}}, nil
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
	ctx context.Context, req *flowv1.ListLawGroupsRequest,
) (*flowv1.ListLawGroupsResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	s.lastListLawGroupsReq = req
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
			{Name: stampLinter, ApplyingNode: "node-a", ContentHash: "ch-1"},
			{Name: "approval", ApplyingNode: "node-b", ContentHash: "ch-1"},
		},
	}, nil
}

func (s *spyServer) HasStamp(
	ctx context.Context, req *flowv1.HasStampRequest,
) (*flowv1.HasStampResponse, error) {
	s.lastMD, _ = metadata.FromIncomingContext(ctx)
	return &flowv1.HasStampResponse{Exists: req.GetStampName() == stampLinter}, nil
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
