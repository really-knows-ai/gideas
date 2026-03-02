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

// newSpyGRPCServer creates a gRPC server with the tribunalSpy registered
// for the five Foundry Flow service interfaces the Tribunal depends on.
func newSpyGRPCServer(spy *tribunalSpy) *grpc.Server {
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	flowv1.RegisterLibrarianServiceServer(srv, spy)
	flowv1.RegisterFrictionLedgerServiceServer(srv, spy)
	return srv
}

// tribunalSpy captures calls to service operations for test assertions.
// It supports the fan-out pattern: CreateChildWorkitem, RouteChild,
// GetChildren, PauseTimer/ResumeTimer, and child artefact storage.
type tribunalSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFrictionLedgerServiceServer

	mu sync.Mutex

	// Configurable artefact store: artefactID → content.
	// Used to control which artefacts are "present" (mode detection).
	Artefacts map[string][]byte

	// Configurable child artefacts: "childID:artefactID" → content.
	ChildArtefacts map[string][]byte

	// Configurable law returned by GetLaw.
	Law *flowv1.Law

	// Configurable friction aggregates returned by QueryFriction.
	FrictionAggregates []*flowv1.FrictionAggregate

	// Configurable related laws returned by QueryLaws.
	RelatedLaws []*flowv1.Law

	// Configurable children returned by GetChildren (for AwaitChildren).
	// If nil, auto-generates completed children from CreatedChildren.
	Children []*flowv1.ChildWorkitemStatus

	// Auto-created child IDs (returned by CreateChildWorkitem).
	nextChildID int

	// Configurable error returns.
	GetArtefactErr   error
	GetLawErr        error
	QueryFrictionErr error
	QueryLawsErr     error
	RouteToOutputErr error
	CreateChildErr   error
	RouteChildErr    error
	GetChildrenErr   error
	StoreArtefactErr error

	// Recorded operations for assertions.
	RoutedOutputs        []string
	StoredArtefacts      map[string][]byte // artefactID → content
	ChildStoredArtefacts map[string][]byte // "childID:artefactID" → content
	CreatedChildren      []string
	RoutedChildren       []routedChild
	PauseTimerCalled     bool
	ResumeTimerCalled    bool
}

type routedChild struct {
	ChildID    string
	TargetNode string
}

func newTribunalSpy(tier flowv1.LawTier) *tribunalSpy {
	artefacts := map[string][]byte{
		"law-reference": []byte("law-under-review-001"),
	}

	return &tribunalSpy{
		Artefacts: artefacts,
		Law: &flowv1.Law{
			Id:        "law-under-review-001",
			Goal:      "Haiku must contain a seasonal reference",
			Tier:      tier,
			AppliesTo: []string{"haiku"},
			Representations: []*flowv1.Representation{
				{Type: "text/markdown", Content: "All haiku must include a kigo (seasonal word)."},
			},
		},
		StoredArtefacts:      make(map[string][]byte),
		ChildStoredArtefacts: make(map[string][]byte),
		ChildArtefacts:       make(map[string][]byte),
	}
}

// newReviewModeSpy creates a spy configured for review mode (petition
// artefact present).
func newReviewModeSpy() *tribunalSpy {
	vctx := verdictContext{
		Trigger:   "hearing",
		Goal:      "Haiku must contain a seasonal reference",
		AppliesTo: []string{"haiku"},
		Tier:      1,
		LawID:     "law-001",
		Action:    "create",
	}
	vctxJSON, _ := json.Marshal(vctx)

	petitionContent := `{"petition":{"context":{"trigger":"hearing"},` +
		`"changes":[{"action":"create","goal":"test"}],` +
		`"prose_justification":"test"}}`

	return &tribunalSpy{
		Artefacts: map[string][]byte{
			"petition":        []byte(petitionContent),
			"verdict-context": vctxJSON,
		},
		StoredArtefacts:      make(map[string][]byte),
		ChildStoredArtefacts: make(map[string][]byte),
		ChildArtefacts:       make(map[string][]byte),
	}
}

// setupTribunalTest creates a flow.Client backed by the spy.
func setupTribunalTest(t *testing.T, spy *tribunalSpy) *flow.Client {
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
	case nil:
		// No action set — treat as no-op.
	default:
		// Complete / Suspend — no-op for tribunal spy.
	}
	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

func (s *tribunalSpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

// ---------------------------------------------------------------------------
// Operator methods
// ---------------------------------------------------------------------------

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

func (s *tribunalSpy) GetArtefact(
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

	// Parent artefact request — use the Artefacts map.
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

	// Distinguish between parent and child artefact stores.
	if req.GetWorkitemId() != "" && req.GetWorkitemId() != "test-workitem" {
		key := req.GetWorkitemId() + ":" + req.GetArtefactId()
		s.ChildStoredArtefacts[key] = req.GetContent()
	} else {
		s.StoredArtefacts[req.GetArtefactId()] = req.GetContent()
	}
	return &flowv1.StoreArtefactResponse{
		VersionHash:  "mock-hash",
		IsNewVersion: true,
	}, nil
}

// ---------------------------------------------------------------------------
// Librarian methods
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// FrictionLedger methods
// ---------------------------------------------------------------------------

// Note: RecordTelemetry is defined in the Sidecar methods section above
// and satisfies both interfaces.

func (s *tribunalSpy) QueryFriction(
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
func (s *tribunalSpy) getStoredVerdictContext() *verdictContext {
	content, ok := s.StoredArtefacts[artefactVerdictContext]
	if !ok {
		return nil
	}
	var vctx verdictContext
	if err := json.Unmarshal(content, &vctx); err != nil {
		return nil
	}
	return &vctx
}
