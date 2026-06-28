package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/nodeutil"
	"github.com/gideas/flow/nodes/internal/tally"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const testWorkitemID = "test-workitem"

type tribunalSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFrictionLedgerServiceServer

	mu sync.Mutex

	Artefacts      map[string][]byte
	ChildArtefacts map[string][]byte
	Law            *flowv1.Law
	Friction       []*flowv1.FrictionAggregate
	RelatedLaws    []*flowv1.Law
	Children       []*flowv1.ChildWorkitemStatus
	nextChildID    int

	GetArtefactErr   error
	StoreArtefactErr error
	CreateChildErr   error
	RouteChildErr    error
	GetChildrenErr   error
	GetLawErr        error
	QueryFrictionErr error
	QueryLawsErr     error
	CompleteErr      error
	RouteToOutputErr error

	GetArtefactRequests  []string
	CompletedReasons     []flowv1.CompletionReason
	RoutedOutputs        []string
	CreatedChildren      []string
	RoutedChildren       []routedChild
	ChildStoredArtefacts map[string][]byte
	PauseTimerCalled     bool
	ResumeTimerCalled    bool
}

type routedChild struct {
	ChildID    string
	TargetNode string
}

//nolint:unparam // Test helper keeps the tier argument for scenario clarity.
func newTribunalSpy(tier flowv1.LawTier) *tribunalSpy {
	return &tribunalSpy{
		Artefacts: map[string][]byte{
			artefactLawReference: []byte("law-under-review-001"),
		},
		ChildArtefacts:       make(map[string][]byte),
		ChildStoredArtefacts: make(map[string][]byte),
		Law: &flowv1.Law{
			Id:        "law-under-review-001",
			Goal:      "Haiku must contain a seasonal reference",
			Tier:      tier,
			AppliesTo: []string{"haiku"},
			Representations: []*flowv1.Representation{
				{Type: "text/markdown", Content: "All haiku must include a kigo (seasonal word)."},
			},
		},
	}
}

func newSpyGRPCServer(spy *tribunalSpy) *grpc.Server {
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	flowv1.RegisterLibrarianServiceServer(srv, spy)
	flowv1.RegisterFrictionLedgerServiceServer(srv, spy)
	return srv
}

func setupTribunalTest(t *testing.T, spy *tribunalSpy) *flow.Client {
	t.Helper()

	lis, err := nodeutil.NewLocalListener()
	if err != nil {
		t.Fatalf("NewLocalListener: %v", err)
	}

	srv := newSpyGRPCServer(spy)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })

	t.Setenv(flow.EnvWorkitemID, testWorkitemID)
	client, err := flow.NewClient(flow.WithSidecarAddress(lis.Addr().String()))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return client
}

func (s *tribunalSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *tribunalSpy) PauseTimer(
	_ context.Context, _ *flowv1.PauseTimerRequest,
) (*flowv1.PauseTimerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PauseTimerCalled = true
	return &flowv1.PauseTimerResponse{Acknowledged: true}, nil
}

func (s *tribunalSpy) ResumeTimer(
	_ context.Context, _ *flowv1.ResumeTimerRequest,
) (*flowv1.ResumeTimerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ResumeTimerCalled = true
	return &flowv1.ResumeTimerResponse{Acknowledged: true}, nil
}

func (s *tribunalSpy) SubmitResult(
	_ context.Context, req *flowv1.SubmitResultRequest,
) (*flowv1.SubmitResultResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch a := req.GetAction().(type) {
	case *flowv1.SubmitResultRequest_Route:
		if s.RouteToOutputErr != nil {
			return nil, s.RouteToOutputErr
		}
		if a.Route != nil {
			s.RoutedOutputs = append(s.RoutedOutputs, a.Route.GetTarget())
		}
	case *flowv1.SubmitResultRequest_Complete:
		if s.CompleteErr != nil {
			return nil, s.CompleteErr
		}
		reason := flowv1.CompletionReason_COMPLETION_REASON_UNSPECIFIED
		if a.Complete != nil {
			reason = a.Complete.GetReason()
		}
		s.CompletedReasons = append(s.CompletedReasons, reason)
	}

	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

func (s *tribunalSpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

func (s *tribunalSpy) CreateChildWorkitem(
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

func (s *tribunalSpy) RouteChild(
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

func (s *tribunalSpy) GetChildren(
	_ context.Context, _ *flowv1.GetChildrenRequest,
) (*flowv1.GetChildrenResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.GetChildrenErr != nil {
		return nil, s.GetChildrenErr
	}
	if len(s.Children) > 0 {
		return &flowv1.GetChildrenResponse{Children: s.Children}, nil
	}

	children := make([]*flowv1.ChildWorkitemStatus, len(s.CreatedChildren))
	for i, id := range s.CreatedChildren {
		children[i] = &flowv1.ChildWorkitemStatus{
			WorkitemId: id,
			Phase:      flow.PhaseCompleted,
		}
	}
	return &flowv1.GetChildrenResponse{Children: children}, nil
}

func (s *tribunalSpy) GetArtefact(
	_ context.Context, req *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	s.mu.Lock()
	s.GetArtefactRequests = append(s.GetArtefactRequests, req.GetArtefactId())
	s.mu.Unlock()

	if s.GetArtefactErr != nil {
		return nil, s.GetArtefactErr
	}

	if target := req.GetTargetWorkitemId(); target != "" {
		key := target + ":" + req.GetArtefactId()
		if content, ok := s.ChildArtefacts[key]; ok {
			return &flowv1.GetArtefactResponse{Content: content}, nil
		}
		if content, ok := s.ChildStoredArtefacts[key]; ok {
			return &flowv1.GetArtefactResponse{Content: content}, nil
		}
		return nil, status.Errorf(codes.NotFound, "child artefact %q not found", key)
	}

	content, ok := s.Artefacts[req.GetArtefactId()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "artefact %q not found", req.GetArtefactId())
	}
	return &flowv1.GetArtefactResponse{Content: content}, nil
}

func (s *tribunalSpy) StoreArtefact(
	_ context.Context, req *flowv1.StoreArtefactRequest,
) (*flowv1.StoreArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.StoreArtefactErr != nil {
		return nil, s.StoreArtefactErr
	}

	if req.GetWorkitemId() != "" && req.GetWorkitemId() != testWorkitemID {
		key := req.GetWorkitemId() + ":" + req.GetArtefactId()
		s.ChildStoredArtefacts[key] = req.GetContent()
	}

	return &flowv1.StoreArtefactResponse{VersionHash: "test-hash", IsNewVersion: true}, nil
}

func (s *tribunalSpy) GetLaw(
	_ context.Context, _ *flowv1.GetLawRequest,
) (*flowv1.GetLawResponse, error) {
	if s.GetLawErr != nil {
		return nil, s.GetLawErr
	}
	return &flowv1.GetLawResponse{Law: s.Law}, nil
}

func (s *tribunalSpy) QueryLaws(
	_ context.Context, _ *flowv1.QueryLawsRequest,
) (*flowv1.QueryLawsResponse, error) {
	if s.QueryLawsErr != nil {
		return nil, s.QueryLawsErr
	}
	return &flowv1.QueryLawsResponse{Laws: s.RelatedLaws}, nil
}

func (s *tribunalSpy) QueryFriction(
	_ context.Context, _ *flowv1.QueryFrictionRequest,
) (*flowv1.QueryFrictionResponse, error) {
	if s.QueryFrictionErr != nil {
		return nil, s.QueryFrictionErr
	}
	return &flowv1.QueryFrictionResponse{FrictionAggregates: s.Friction}, nil
}

func seedJurorVerdict(spy *tribunalSpy, childID, outcome, reasoning string) {
	v := tally.JurorVote{Outcome: outcome, Reasoning: reasoning}
	data, _ := json.Marshal(v)
	spy.ChildArtefacts[childID+":"+tally.ArtefactVerdict] = data
}

func defaultTestConfig() *tribunalConfig {
	return &tribunalConfig{JurySize: 3, MaxRounds: 1}
}

func assertCompleted(t *testing.T, spy *tribunalSpy) {
	t.Helper()
	if len(spy.CompletedReasons) != 1 {
		t.Fatalf("expected 1 completion, got %d: %v", len(spy.CompletedReasons), spy.CompletedReasons)
	}
	if spy.CompletedReasons[0] != flowv1.CompletionReason_COMPLETION_REASON_UNSPECIFIED {
		t.Fatalf("completion reason = %v, want %v",
			spy.CompletedReasons[0], flowv1.CompletionReason_COMPLETION_REASON_UNSPECIFIED)
	}
}

func assertRoutedTo(t *testing.T, spy *tribunalSpy, output string) {
	t.Helper()
	if len(spy.RoutedOutputs) != 1 {
		t.Fatalf("expected 1 routed output, got %d: %v", len(spy.RoutedOutputs), spy.RoutedOutputs)
	}
	if spy.RoutedOutputs[0] != output {
		t.Fatalf("routed to %q, want %q", spy.RoutedOutputs[0], output)
	}
}

func clerkChildVerdictContext(t *testing.T, spy *tribunalSpy) (verdictContext, string) {
	t.Helper()

	if len(spy.CreatedChildren) == 0 {
		t.Fatal("no children created")
	}
	childID := spy.CreatedChildren[len(spy.CreatedChildren)-1]
	raw, ok := spy.ChildStoredArtefacts[childID+":"+artefactVerdictContext]
	if !ok {
		t.Fatalf("verdict-context not stored on child %s", childID)
	}

	var vctx verdictContext
	if err := json.Unmarshal(raw, &vctx); err != nil {
		t.Fatalf("unmarshal verdict-context: %v", err)
	}
	return vctx, string(raw)
}
