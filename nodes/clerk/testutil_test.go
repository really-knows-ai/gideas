package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Spy Server
// ---------------------------------------------------------------------------

// clerkSpy implements the gRPC services the Clerk node depends on.
type clerkSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer

	mu sync.Mutex

	// Configurable artefact store: artefactID → content.
	Artefacts map[string][]byte

	// Configurable child artefacts: "childID:artefactID" → content.
	ChildArtefacts map[string][]byte

	// Configurable feedback items returned by GetFeedback.
	FeedbackItems []*flowv1.FeedbackItem

	// Configurable children returned by GetChildren (for AwaitChildren).
	Children []*flowv1.ChildWorkitemStatus

	// Auto-created child IDs (returned by CreateChildWorkitem).
	nextChildID int

	// Configurable error returns.
	GetArtefactErr   error
	StoreArtefactErr error
	RouteToOutputErr error
	CreateChildErr   error
	RouteChildErr    error
	GetChildrenErr   error
	GetFeedbackErr   error

	// Recorded operations.
	StoredArtefacts      map[string][]byte // artefactID → content
	ChildStoredArtefacts map[string][]byte // "childID:artefactID" → content
	RoutedOutputs        []string
	CreatedChildren      []string
	RoutedChildren       []routedChild
}

type routedChild struct {
	ChildID    string
	TargetNode string
}

func newClerkSpy() *clerkSpy {
	return &clerkSpy{
		Artefacts:            make(map[string][]byte),
		ChildArtefacts:       make(map[string][]byte),
		StoredArtefacts:      make(map[string][]byte),
		ChildStoredArtefacts: make(map[string][]byte),
	}
}

// ---------------------------------------------------------------------------
// Sidecar methods
// ---------------------------------------------------------------------------

func (s *clerkSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *clerkSpy) PauseTimer(
	_ context.Context, _ *flowv1.PauseTimerRequest,
) (*flowv1.PauseTimerResponse, error) {
	return &flowv1.PauseTimerResponse{Acknowledged: true}, nil
}

func (s *clerkSpy) ResumeTimer(
	_ context.Context, _ *flowv1.ResumeTimerRequest,
) (*flowv1.ResumeTimerResponse, error) {
	return &flowv1.ResumeTimerResponse{Acknowledged: true}, nil
}

func (s *clerkSpy) SubmitResult(
	_ context.Context, req *flowv1.SubmitResultRequest,
) (*flowv1.SubmitResultResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ri := req.GetRoutingInstruction()
	if ri == nil {
		return &flowv1.SubmitResultResponse{Accepted: true}, nil
	}

	if s.RouteToOutputErr != nil {
		return nil, s.RouteToOutputErr
	}

	s.RoutedOutputs = append(s.RoutedOutputs, ri.GetTarget())
	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

func (s *clerkSpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

// ---------------------------------------------------------------------------
// Operator methods
// ---------------------------------------------------------------------------

func (s *clerkSpy) CreateChildWorkitem(
	_ context.Context, _ *flowv1.CreateChildWorkitemRequest,
) (*flowv1.CreateChildWorkitemResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.CreateChildErr != nil {
		return nil, s.CreateChildErr
	}

	s.nextChildID++
	childID := fmt.Sprintf("child-%d", s.nextChildID)
	s.CreatedChildren = append(s.CreatedChildren, childID)
	return &flowv1.CreateChildWorkitemResponse{ChildWorkitemId: childID}, nil
}

func (s *clerkSpy) RouteChild(
	_ context.Context, req *flowv1.RouteChildRequest,
) (*flowv1.RouteChildResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.RouteChildErr != nil {
		return nil, s.RouteChildErr
	}

	s.RoutedChildren = append(s.RoutedChildren, routedChild{
		ChildID:    req.GetChildWorkitemId(),
		TargetNode: req.GetRoutingInstruction().GetTarget(),
	})
	return &flowv1.RouteChildResponse{Accepted: true}, nil
}

func (s *clerkSpy) GetChildren(
	_ context.Context, _ *flowv1.GetChildrenRequest,
) (*flowv1.GetChildrenResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.GetChildrenErr != nil {
		return nil, s.GetChildrenErr
	}

	// If explicit children are configured, return them.
	if len(s.Children) > 0 {
		return &flowv1.GetChildrenResponse{Children: s.Children}, nil
	}

	// Auto-generate completed children from created list.
	children := make([]*flowv1.ChildWorkitemStatus, len(s.CreatedChildren))
	for i, id := range s.CreatedChildren {
		children[i] = &flowv1.ChildWorkitemStatus{
			WorkitemId: id,
			Phase:      "Completed",
		}
	}
	return &flowv1.GetChildrenResponse{Children: children}, nil
}

// ---------------------------------------------------------------------------
// Archivist methods
// ---------------------------------------------------------------------------

func (s *clerkSpy) GetArtefact(
	_ context.Context, req *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	if s.GetArtefactErr != nil {
		return nil, s.GetArtefactErr
	}

	// Child artefact request (has TargetWorkitemId).
	if target := req.GetTargetWorkitemId(); target != "" {
		key := target + ":" + req.GetArtefactId()
		content, ok := s.ChildArtefacts[key]
		if !ok {
			return nil, status.Errorf(codes.NotFound, "child artefact %q not found", key)
		}
		return &flowv1.GetArtefactResponse{Content: content}, nil
	}

	// Parent artefact request.
	content, ok := s.Artefacts[req.GetArtefactId()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "artefact %q not found", req.GetArtefactId())
	}
	return &flowv1.GetArtefactResponse{Content: content}, nil
}

func (s *clerkSpy) StoreArtefact(
	_ context.Context, req *flowv1.StoreArtefactRequest,
) (*flowv1.StoreArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.StoreArtefactErr != nil {
		return nil, s.StoreArtefactErr
	}

	// Distinguish between parent and child artefact stores.
	if req.GetWorkitemId() != "" && req.GetWorkitemId() != "test-workitem" {
		key := req.GetWorkitemId() + ":" + req.GetArtefactId()
		s.ChildStoredArtefacts[key] = req.GetContent()
	} else {
		s.StoredArtefacts[req.GetArtefactId()] = req.GetContent()
	}
	return &flowv1.StoreArtefactResponse{
		VersionHash:  "test-hash",
		IsNewVersion: true,
	}, nil
}

func (s *clerkSpy) GetFeedback(
	_ context.Context, _ *flowv1.GetFeedbackRequest,
) (*flowv1.GetFeedbackResponse, error) {
	if s.GetFeedbackErr != nil {
		return nil, s.GetFeedbackErr
	}
	return &flowv1.GetFeedbackResponse{FeedbackItems: s.FeedbackItems}, nil
}

func (s *clerkSpy) ListArtefacts(
	_ context.Context, _ *flowv1.ListArtefactsRequest,
) (*flowv1.ListArtefactsResponse, error) {
	return &flowv1.ListArtefactsResponse{}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newLocalListener() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

func newSpyGRPCServer(spy *clerkSpy) *grpc.Server {
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	return srv
}

func setupClerkTest(t *testing.T, spy *clerkSpy) *flow.Client {
	t.Helper()

	lis, err := newLocalListener()
	if err != nil {
		t.Fatalf("newLocalListener: %v", err)
	}

	srv := newSpyGRPCServer(spy)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })

	t.Setenv(flow.EnvWorkitemID, "test-workitem")

	client, err := flow.NewClient(
		flow.WithSidecarAddress(lis.Addr().String()),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return client
}

// seedClerkArtefacts populates the spy with standard Clerk input artefacts.
func seedClerkArtefacts(spy *clerkSpy, deliberation *deliberationResult, vctx *verdictContext) {
	deliberationJSON, _ := json.Marshal(deliberation)
	spy.Artefacts[artefactDeliberationResult] = deliberationJSON

	vctxJSON, _ := json.Marshal(vctx)
	spy.Artefacts[artefactVerdictContext] = vctxJSON
}

// seedCodificationResults populates the spy with codification child
// artefacts so CollectArtefacts can find them.
func seedCodificationResults(spy *clerkSpy, childID string, rep petitionRep) {
	repJSON, _ := json.Marshal(rep)
	spy.ChildArtefacts[childID+":"+artefactCodificationResult] = repJSON
}
