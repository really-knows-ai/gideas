package main

import (
	"context"
	"net"
	"sync"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc"
)

// ---------------------------------------------------------------------------
// Spy: Operator (captures CreateWorkitem calls for entry function tests)
// ---------------------------------------------------------------------------

type spyOperator struct {
	flowv1.UnimplementedOperatorServiceServer

	mu        sync.Mutex
	calls     []*flowv1.CreateWorkitemRequest
	returnID  string
	returnErr error
}

func (s *spyOperator) CreateWorkitem(
	_ context.Context, req *flowv1.CreateWorkitemRequest,
) (*flowv1.CreateWorkitemResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, req)
	if s.returnErr != nil {
		return nil, s.returnErr
	}
	return &flowv1.CreateWorkitemResponse{WorkitemId: s.returnID}, nil
}

func (s *spyOperator) getCalls() []*flowv1.CreateWorkitemRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]*flowv1.CreateWorkitemRequest, len(s.calls))
	copy(cp, s.calls)
	return cp
}

// ---------------------------------------------------------------------------
// Spy: Librarian (returns pre-configured laws for QueryLaws)
// ---------------------------------------------------------------------------

type spyLibrarian struct {
	flowv1.UnimplementedLibrarianServiceServer

	mu         sync.Mutex
	returnLaws []*flowv1.Law
	returnErr  error
}

func (s *spyLibrarian) QueryLaws(
	_ context.Context, _ *flowv1.QueryLawsRequest,
) (*flowv1.QueryLawsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.returnErr != nil {
		return nil, s.returnErr
	}
	return &flowv1.QueryLawsResponse{Laws: s.returnLaws}, nil
}

// ---------------------------------------------------------------------------
// Spy: full Sidecar (all five services for handler tests via flow.Client)
// ---------------------------------------------------------------------------

type handlerSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFrictionLedgerServiceServer

	mu              sync.Mutex
	storedArtefacts []*flowv1.StoreArtefactRequest
	routedOutputs   []string
	heartbeatCount  int
}

func (s *handlerSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heartbeatCount++
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *handlerSpy) StoreArtefact(
	_ context.Context, req *flowv1.StoreArtefactRequest,
) (*flowv1.StoreArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storedArtefacts = append(s.storedArtefacts, req)
	return &flowv1.StoreArtefactResponse{
		VersionHash:  "hash-test",
		IsNewVersion: true,
	}, nil
}

func (s *handlerSpy) SubmitResult(
	_ context.Context, req *flowv1.SubmitResultRequest,
) (*flowv1.SubmitResultResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch a := req.GetAction().(type) {
	case *flowv1.SubmitResultRequest_Route:
		if a.Route != nil {
			s.routedOutputs = append(s.routedOutputs, a.Route.GetTarget())
		}
	default:
		// Complete / Suspend / nil — nothing to record.
	}
	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

func (s *handlerSpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newTestListener creates a TCP listener on an ephemeral localhost port.
func newTestListener(t *testing.T) net.Listener {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	return lis
}

// newHandlerTestClient creates a flow.Client backed by a local gRPC server
// with the handlerSpy providing all five service interfaces.
func newHandlerTestClient(t *testing.T, spy *handlerSpy) *flow.Client {
	t.Helper()

	lis := newTestListener(t)
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	flowv1.RegisterLibrarianServiceServer(srv, spy)
	flowv1.RegisterFrictionLedgerServiceServer(srv, spy)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })

	client, err := flow.NewClient(flow.WithSidecarAddress(lis.Addr().String()))
	if err != nil {
		t.Fatalf("NewClient() failed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return client
}

// setupEntryTestClient creates spy gRPC servers for Operator+Librarian (on
// one listener, mirroring the real Sidecar) and returns an EntryClient.
func setupEntryTestClient(
	t *testing.T,
	operatorSpy *spyOperator,
	librarianSpy *spyLibrarian,
) *flow.EntryClient {
	t.Helper()

	lis := newTestListener(t)
	addr := lis.Addr().String()

	srv := grpc.NewServer()
	if operatorSpy != nil {
		flowv1.RegisterOperatorServiceServer(srv, operatorSpy)
	}
	if librarianSpy != nil {
		flowv1.RegisterLibrarianServiceServer(srv, librarianSpy)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })

	ec, err := flow.NewEntryClientForTest(addr, "")
	if err != nil {
		t.Fatalf("NewEntryClientForTest() failed: %v", err)
	}
	t.Cleanup(func() { _ = ec.Close() })

	return ec
}
