package main

import (
	"context"
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

// gateSpy implements the gRPC services the Judiciary Gate depends on:
// Sidecar, Operator (for RouteToOutput/Complete), Archivist, and Librarian.
type gateSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer

	mu sync.Mutex

	// Configurable artefact store: artefactID -> content.
	Artefacts map[string][]byte

	// Configurable laws: lawID -> *flowv1.Law.
	Laws map[string]*flowv1.Law

	// Configurable feedback items for the petition artefact.
	FeedbackItems []*flowv1.FeedbackItem

	// Configurable error returns.
	GetArtefactErr   error
	GetLawErr        error
	GetFeedbackErr   error
	RouteToOutputErr error
	CompleteErr      error
	WriteLawErr      error
	RetireLawErr     error

	// Recorded operations.
	StoredArtefacts map[string][]byte // artefactID -> content
	RoutedOutputs   []string
	Completed       bool
	CompletedTarget string

	// Recorded Librarian calls.
	WrittenLaws     []*flowv1.Law
	RetiredLawIDs   []string
	RequestedLawIDs []string

	// WriteLaw responses (auto-generated if nil).
	WriteLawResponses []*flowv1.WriteLawResponse
	writeLawCallCount int
}

func newGateSpy() *gateSpy {
	return &gateSpy{
		Artefacts:       make(map[string][]byte),
		Laws:            make(map[string]*flowv1.Law),
		StoredArtefacts: make(map[string][]byte),
	}
}

// ---------------------------------------------------------------------------
// Sidecar methods
// ---------------------------------------------------------------------------

func (s *gateSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *gateSpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

// ---------------------------------------------------------------------------
// Operator methods (RouteToOutput and Complete both go through SubmitResult)
// ---------------------------------------------------------------------------

func (s *gateSpy) SubmitResult(
	_ context.Context, req *flowv1.SubmitResultRequest,
) (*flowv1.SubmitResultResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch a := req.GetAction().(type) {
	case *flowv1.SubmitResultRequest_Complete:
		if s.CompleteErr != nil {
			return nil, s.CompleteErr
		}
		_ = a // suppress unused warning
		s.Completed = true
	case *flowv1.SubmitResultRequest_Route:
		if a.Route != nil {
			target := a.Route.GetTarget()
			if s.RouteToOutputErr != nil {
				return nil, s.RouteToOutputErr
			}
			s.RoutedOutputs = append(s.RoutedOutputs, target)
		}
	case nil:
		// No action set — treat as complete.
		if s.CompleteErr != nil {
			return nil, s.CompleteErr
		}
		s.Completed = true
	default:
		// Suspend — no-op for gate spy.
	}
	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

// ---------------------------------------------------------------------------
// Archivist methods
// ---------------------------------------------------------------------------

func (s *gateSpy) GetArtefact(
	_ context.Context, req *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	if s.GetArtefactErr != nil {
		return nil, s.GetArtefactErr
	}

	content, ok := s.Artefacts[req.GetArtefactId()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "artefact %q not found", req.GetArtefactId())
	}
	return &flowv1.GetArtefactResponse{Content: content}, nil
}

func (s *gateSpy) StoreArtefact(
	_ context.Context, req *flowv1.StoreArtefactRequest,
) (*flowv1.StoreArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.StoredArtefacts[req.GetArtefactId()] = req.GetContent()
	return &flowv1.StoreArtefactResponse{
		VersionHash:  "test-hash",
		IsNewVersion: true,
	}, nil
}

func (s *gateSpy) GetFeedback(
	_ context.Context, _ *flowv1.GetFeedbackRequest,
) (*flowv1.GetFeedbackResponse, error) {
	if s.GetFeedbackErr != nil {
		return nil, s.GetFeedbackErr
	}
	return &flowv1.GetFeedbackResponse{FeedbackItems: s.FeedbackItems}, nil
}

// ---------------------------------------------------------------------------
// Librarian methods
// ---------------------------------------------------------------------------

func (s *gateSpy) GetLaw(
	_ context.Context, req *flowv1.GetLawRequest,
) (*flowv1.GetLawResponse, error) {
	s.mu.Lock()
	s.RequestedLawIDs = append(s.RequestedLawIDs, req.GetLawId())
	s.mu.Unlock()

	if s.GetLawErr != nil {
		return nil, s.GetLawErr
	}

	law, ok := s.Laws[req.GetLawId()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "law %q not found", req.GetLawId())
	}
	return &flowv1.GetLawResponse{Law: law}, nil
}

func (s *gateSpy) WriteLaw(
	_ context.Context, req *flowv1.WriteLawRequest,
) (*flowv1.WriteLawResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.WriteLawErr != nil {
		return nil, s.WriteLawErr
	}

	s.WrittenLaws = append(s.WrittenLaws, req.GetLaw())

	// Return preconfigured response or auto-generate one.
	if s.writeLawCallCount < len(s.WriteLawResponses) {
		resp := s.WriteLawResponses[s.writeLawCallCount]
		s.writeLawCallCount++
		return resp, nil
	}

	s.writeLawCallCount++
	return &flowv1.WriteLawResponse{
		LawId:       "new-law-id",
		VersionHash: "v1",
	}, nil
}

func (s *gateSpy) RetireLaw(
	_ context.Context, req *flowv1.RetireLawRequest,
) (*flowv1.RetireLawResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.RetireLawErr != nil {
		return nil, s.RetireLawErr
	}

	s.RetiredLawIDs = append(s.RetiredLawIDs, req.GetLawId())
	return &flowv1.RetireLawResponse{Acknowledged: true}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newLocalListener() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

func newSpyGRPCServer(spy *gateSpy) *grpc.Server {
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	flowv1.RegisterLibrarianServiceServer(srv, spy)
	return srv
}

func setupGateTest(t *testing.T, spy *gateSpy) *flow.Client {
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

// assertRoutedTo verifies the spy recorded exactly one routing decision.
func assertRoutedTo(t *testing.T, spy *gateSpy, expected string) {
	t.Helper()
	if len(spy.RoutedOutputs) != 1 {
		t.Fatalf("expected 1 routed output, got %d: %v", len(spy.RoutedOutputs), spy.RoutedOutputs)
	}
	if spy.RoutedOutputs[0] != expected {
		t.Errorf("routed to %q, want %q", spy.RoutedOutputs[0], expected)
	}
}

// assertCompleted verifies the spy recorded a Complete() call.
func assertCompleted(t *testing.T, spy *gateSpy) {
	t.Helper()
	if !spy.Completed {
		t.Fatal("expected Complete() to be called")
	}
}
