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

// newLocalListener creates a TCP listener on an ephemeral localhost port.
func newLocalListener() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

// newSpyGRPCServer creates a gRPC server with the arbiterSpy registered
// for the five Foundry Flow service interfaces the Arbiter depends on.
func newSpyGRPCServer(spy *arbiterSpy) *grpc.Server {
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	flowv1.RegisterLibrarianServiceServer(srv, spy)
	flowv1.RegisterFrictionLedgerServiceServer(srv, spy)
	return srv
}

// arbiterSpy captures calls to service operations for test assertions.
// It supports the fan-out pattern: CreateChildWorkitem, RouteChild,
// GetChildren, PauseTimer/ResumeTimer, and child artefact storage.
type arbiterSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFrictionLedgerServiceServer

	mu sync.Mutex

	// Configurable topology response.
	TopologyResponse *flowv1.GetFlowTopologyResponse

	// Configurable feedback items returned by GetFeedback.
	FeedbackItems []*flowv1.FeedbackItem

	// Configurable artefact content returned by GetArtefact.
	ArtefactContent []byte

	// Configurable laws returned by QueryLaws.
	Laws []*flowv1.Law

	// Configurable friction aggregates returned by QueryFriction.
	FrictionAggregates []*flowv1.FrictionAggregate

	// Configurable children returned by GetChildren (for AwaitChildren).
	// If nil, auto-generates completed children from CreatedChildren.
	Children []*flowv1.ChildWorkitemStatus

	// Auto-created child IDs (returned by CreateChildWorkitem).
	nextChildID int

	// Configurable error returns.
	GetFlowTopologyErr error
	GetFeedbackErr     error
	GetArtefactErr     error
	QueryLawsErr       error
	QueryFrictionErr   error
	RouteToOutputErr   error
	CreateChildErr     error
	RouteChildErr      error
	GetChildrenErr     error
	StoreArtefactErr   error

	// Recorded operations for assertions.
	RoutedOutputs        []string
	StoreArtefactCalls   []storeArtefactRecord
	CreatedChildren      []string
	RoutedChildren       []routedChild
	ChildStoredArtefacts map[string][]byte // "childID:artefactID" → content
	PauseTimerCalled     bool
	ResumeTimerCalled    bool
}

type storeArtefactRecord struct {
	ArtefactID string
	Content    []byte
}

type routedChild struct {
	ChildID    string
	TargetNode string
}

func newArbiterSpy() *arbiterSpy {
	return &arbiterSpy{
		TopologyResponse:     defaultArbiterTopology(),
		ArtefactContent:      []byte("sample artefact content"),
		ChildStoredArtefacts: make(map[string][]byte),
	}
}

// defaultArbiterTopology returns a topology appropriate for arbiter tests.
func defaultArbiterTopology() *flowv1.GetFlowTopologyResponse {
	return &flowv1.GetFlowTopologyResponse{
		Self: &flowv1.FlowNode{
			Name: "arbiter",
			Outputs: []*flowv1.FlowOutput{
				{Name: "deliberation-gate", Target: "deliberation-gate"},
			},
		},
		Nodes: map[string]*flowv1.FlowNode{
			"arbiter":           {Name: "arbiter"},
			"deliberation-gate": {Name: "deliberation-gate"},
		},
		ExitContract: map[string]*flowv1.StampRequirements{
			"haiku": {Stamps: []string{"linter", "review", "approval"}},
		},
	}
}

// setupArbiterTest creates a flow.Client backed by the spy.
func setupArbiterTest(t *testing.T, spy *arbiterSpy) *flow.Client {
	t.Helper()

	lis, err := newLocalListener()
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	srv := newSpyGRPCServer(spy)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })

	t.Setenv(flow.EnvWorkitemID, "test-workitem")
	client, err := flow.NewClient(
		flow.WithSidecarAddress(lis.Addr().String()),
	)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return client
}

// ---------------------------------------------------------------------------
// Sidecar methods
// ---------------------------------------------------------------------------

func (s *arbiterSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *arbiterSpy) PauseTimer(
	_ context.Context, _ *flowv1.PauseTimerRequest,
) (*flowv1.PauseTimerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PauseTimerCalled = true
	return &flowv1.PauseTimerResponse{Acknowledged: true}, nil
}

func (s *arbiterSpy) ResumeTimer(
	_ context.Context, _ *flowv1.ResumeTimerRequest,
) (*flowv1.ResumeTimerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ResumeTimerCalled = true
	return &flowv1.ResumeTimerResponse{Acknowledged: true}, nil
}

func (s *arbiterSpy) SubmitResult(
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

func (s *arbiterSpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

// ---------------------------------------------------------------------------
// Operator methods
// ---------------------------------------------------------------------------

func (s *arbiterSpy) GetFlowTopology(
	_ context.Context, _ *flowv1.GetFlowTopologyRequest,
) (*flowv1.GetFlowTopologyResponse, error) {
	if s.GetFlowTopologyErr != nil {
		return nil, s.GetFlowTopologyErr
	}
	return s.TopologyResponse, nil
}

func (s *arbiterSpy) CreateChildWorkitem(
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

func (s *arbiterSpy) RouteChild(
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

func (s *arbiterSpy) GetChildren(
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

func (s *arbiterSpy) GetArtefact(
	_ context.Context, req *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	if s.GetArtefactErr != nil {
		return nil, s.GetArtefactErr
	}

	// Child artefact request (has TargetWorkitemId).
	if target := req.GetTargetWorkitemId(); target != "" {
		key := target + ":" + req.GetArtefactId()
		content, ok := s.ChildStoredArtefacts[key]
		if !ok {
			return nil, status.Errorf(codes.NotFound, "child artefact %q not found", key)
		}
		return &flowv1.GetArtefactResponse{Content: content}, nil
	}

	return &flowv1.GetArtefactResponse{
		Content: s.ArtefactContent,
	}, nil
}

func (s *arbiterSpy) StoreArtefact(
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
		s.StoreArtefactCalls = append(s.StoreArtefactCalls, storeArtefactRecord{
			ArtefactID: req.GetArtefactId(),
			Content:    req.GetContent(),
		})
	}
	return &flowv1.StoreArtefactResponse{
		VersionHash:  "mock-hash",
		IsNewVersion: true,
	}, nil
}

func (s *arbiterSpy) GetFeedback(
	_ context.Context, _ *flowv1.GetFeedbackRequest,
) (*flowv1.GetFeedbackResponse, error) {
	if s.GetFeedbackErr != nil {
		return nil, s.GetFeedbackErr
	}
	return &flowv1.GetFeedbackResponse{
		FeedbackItems: s.FeedbackItems,
	}, nil
}

// ---------------------------------------------------------------------------
// Librarian methods
// ---------------------------------------------------------------------------

func (s *arbiterSpy) QueryLaws(
	_ context.Context, _ *flowv1.QueryLawsRequest,
) (*flowv1.QueryLawsResponse, error) {
	if s.QueryLawsErr != nil {
		return nil, s.QueryLawsErr
	}
	return &flowv1.QueryLawsResponse{Laws: s.Laws}, nil
}

// ---------------------------------------------------------------------------
// FrictionLedger methods
// ---------------------------------------------------------------------------

func (s *arbiterSpy) QueryFriction(
	_ context.Context, _ *flowv1.QueryFrictionRequest,
) (*flowv1.QueryFrictionResponse, error) {
	if s.QueryFrictionErr != nil {
		return nil, s.QueryFrictionErr
	}
	return &flowv1.QueryFrictionResponse{
		FrictionAggregates: s.FrictionAggregates,
	}, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// getStoredVerdictContext parses the verdict-context artefact from the spy's
// recorded store calls. Returns nil if not found.
func (s *arbiterSpy) getStoredVerdictContext() *verdictContext {
	for _, call := range s.StoreArtefactCalls {
		if call.ArtefactID == artefactVerdictContext {
			var vctx verdictContext
			if err := json.Unmarshal(call.Content, &vctx); err != nil {
				return nil
			}
			return &vctx
		}
	}
	return nil
}
