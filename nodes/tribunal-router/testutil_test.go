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

// routerSpy implements the gRPC services the Tribunal Router depends on.
type routerSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer

	mu sync.Mutex

	// Configurable artefacts: artefactID -> content.
	Artefacts map[string][]byte

	// Configurable laws: lawID -> *flowv1.Law.
	Laws map[string]*flowv1.Law

	// Configurable error returns.
	GetArtefactErr   error
	GetLawErr        error
	RouteToOutputErr error

	// Recorded operations.
	RoutedOutputs  []string
	RequestedLawID string
}

func newRouterSpy() *routerSpy {
	return &routerSpy{
		Artefacts: make(map[string][]byte),
		Laws:      make(map[string]*flowv1.Law),
	}
}

// ---------------------------------------------------------------------------
// Sidecar methods
// ---------------------------------------------------------------------------

func (s *routerSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *routerSpy) SubmitResult(
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
		// Complete / Suspend — no-op for router spy.
	}
	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

func (s *routerSpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

// ---------------------------------------------------------------------------
// Archivist methods
// ---------------------------------------------------------------------------

func (s *routerSpy) GetArtefact(
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

// ---------------------------------------------------------------------------
// Librarian methods
// ---------------------------------------------------------------------------

func (s *routerSpy) GetLaw(
	_ context.Context, req *flowv1.GetLawRequest,
) (*flowv1.GetLawResponse, error) {
	s.mu.Lock()
	s.RequestedLawID = req.GetLawId()
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

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newLocalListener() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

func newSpyGRPCServer(spy *routerSpy) *grpc.Server {
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	flowv1.RegisterLibrarianServiceServer(srv, spy)
	return srv
}

func setupRouterTest(t *testing.T, spy *routerSpy) *flow.Client {
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

// assertRoutedTo verifies the spy recorded exactly one routing decision to
// the expected output.
func assertRoutedTo(t *testing.T, spy *routerSpy, expected string) {
	t.Helper()
	if len(spy.RoutedOutputs) != 1 {
		t.Fatalf("expected 1 routed output, got %d: %v", len(spy.RoutedOutputs), spy.RoutedOutputs)
	}
	if spy.RoutedOutputs[0] != expected {
		t.Errorf("routed to %q, want %q", spy.RoutedOutputs[0], expected)
	}
}
