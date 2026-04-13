package main

import (
	"context"
	"net"
	"sync"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ---------------------------------------------------------------------------
// Spy: Operator (captures CreateWorkitem calls for entry function tests)
// ---------------------------------------------------------------------------

type spyOperator struct {
	flowv1.UnimplementedOperatorServiceServer

	mu          sync.Mutex
	calls       []*flowv1.CreateWorkitemRequest
	submitCalls []*flowv1.SubmitResultRequest
	returnID    string
	returnErr   error
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

func (s *spyOperator) SubmitResult(
	_ context.Context, req *flowv1.SubmitResultRequest,
) (*flowv1.SubmitResultResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.submitCalls = append(s.submitCalls, req)
	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

// ---------------------------------------------------------------------------
// Spy: Event Bus (for entry function tests — Embassy doesn't subscribe, but
// the EntryClient requires it wired)
// ---------------------------------------------------------------------------

type spyEventBus struct {
	flowv1.UnimplementedFlowEventBusServiceServer
}

// ---------------------------------------------------------------------------
// Spy: full Sidecar (all five services for handler tests via flow.Client)
// ---------------------------------------------------------------------------

// handlerSpy captures calls made by handleExport through the SDK client.
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
	completedCount  int
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
	case *flowv1.SubmitResultRequest_Complete:
		s.completedCount++
	default:
		// Suspend / nil — nothing to record.
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

// setupEntryTestClient creates spy gRPC servers for Operator and Event Bus,
// starts them, and returns an EntryClient connected to them.
func setupEntryTestClient(
	t *testing.T,
	operatorSpy *spyOperator,
	eventBusSpy *spyEventBus,
) *flow.EntryClient {
	t.Helper()

	var opAddr, ebAddr string

	if operatorSpy != nil {
		opLis := newTestListener(t)
		opAddr = opLis.Addr().String()
		srv := grpc.NewServer()
		flowv1.RegisterOperatorServiceServer(srv, operatorSpy)
		go func() { _ = srv.Serve(opLis) }()
		t.Cleanup(func() { srv.GracefulStop() })
	}

	if eventBusSpy != nil {
		ebLis := newTestListener(t)
		ebAddr = ebLis.Addr().String()
		srv := grpc.NewServer()
		flowv1.RegisterFlowEventBusServiceServer(srv, eventBusSpy)
		go func() { _ = srv.Serve(ebLis) }()
		t.Cleanup(func() { srv.GracefulStop() })
	}

	ec, err := flow.NewEntryClientForTest(opAddr, ebAddr)
	if err != nil {
		t.Fatalf("NewEntryClientForTest() failed: %v", err)
	}
	t.Cleanup(func() { _ = ec.Close() })

	return ec
}

// ---------------------------------------------------------------------------
// Spy: Archivist (captures StoreArtefact calls for materialisation tests)
// ---------------------------------------------------------------------------

type spyArchivist struct {
	flowv1.UnimplementedArchivistServiceServer

	mu              sync.Mutex
	storedArtefacts []*flowv1.StoreArtefactRequest
	stampedCalls    []*flowv1.StampArtefactRequest
	returnErr       error

	// Export manifest builder support (slice 13.5.1).
	listArtefacts    []*flowv1.ArtefactRef
	artefactContents map[string][]byte          // governed_artefact -> content
	stamps           map[string][]*flowv1.Stamp // governed_artefact -> stamps
}

func (s *spyArchivist) StoreArtefact(
	_ context.Context, req *flowv1.StoreArtefactRequest,
) (*flowv1.StoreArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storedArtefacts = append(s.storedArtefacts, req)
	if s.returnErr != nil {
		return nil, s.returnErr
	}
	return &flowv1.StoreArtefactResponse{
		VersionHash:  "hash-test",
		IsNewVersion: true,
	}, nil
}

func (s *spyArchivist) StampArtefact(
	_ context.Context, req *flowv1.StampArtefactRequest,
) (*flowv1.StampArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stampedCalls = append(s.stampedCalls, req)
	if s.returnErr != nil {
		return nil, s.returnErr
	}
	return &flowv1.StampArtefactResponse{}, nil
}

func (s *spyArchivist) ListArtefacts(
	_ context.Context, _ *flowv1.ListArtefactsRequest,
) (*flowv1.ListArtefactsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.returnErr != nil {
		return nil, s.returnErr
	}
	return &flowv1.ListArtefactsResponse{ArtefactRefs: s.listArtefacts}, nil
}

func (s *spyArchivist) GetArtefact(
	_ context.Context, req *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.returnErr != nil {
		return nil, s.returnErr
	}
	artID := req.GetArtefactId()
	content, ok := s.artefactContents[artID]
	if !ok {
		content = []byte{}
	}
	return &flowv1.GetArtefactResponse{
		Content:          content,
		VersionHash:      "hash-test",
		GovernedArtefact: artID,
	}, nil
}

func (s *spyArchivist) GetStamps(
	_ context.Context, req *flowv1.GetStampsRequest,
) (*flowv1.GetStampsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.returnErr != nil {
		return nil, s.returnErr
	}
	artID := req.GetArtefactId()
	stamps := s.stamps[artID]
	return &flowv1.GetStampsResponse{Stamps: stamps}, nil
}

// ---------------------------------------------------------------------------
// Test handler construction
// ---------------------------------------------------------------------------

// testHandlerOpts provides configuration for creating test embassyHandler
// instances with pre-populated import type registries.
type testHandlerOpts struct {
	systemImportTypes map[string]systemImportType
	flowImportTypes   map[string]flowImportTypeSpec
	treaties          map[string]treatyConfig
	federationID      string
	federationStates  []string
	naturalisation    *naturalisationConfig
	operatorSpy       *spyOperator
	archivistSpy      *spyArchivist
}

// newTestHandler creates an embassyHandler with the given test configuration.
// When operatorSpy and archivistSpy are provided, a local gRPC server is
// started to back the handler's operator and archivist clients.
func newTestHandler(t *testing.T, opts testHandlerOpts) *embassyHandler {
	t.Helper()
	sys := opts.systemImportTypes
	if sys == nil {
		sys = make(map[string]systemImportType)
	}
	fl := opts.flowImportTypes
	if fl == nil {
		fl = make(map[string]flowImportTypeSpec)
	}
	h := &embassyHandler{
		cfg: &embassyConfig{
			SystemImportTypes:  sys,
			FlowImportTypes:    fl,
			Treaties:           opts.treaties,
			FederationIdentity: opts.federationID,
			FederationStates:   opts.federationStates,
			Naturalisation:     opts.naturalisation,
		},
	}

	// Wire operator and archivist spy servers if provided.
	if opts.operatorSpy != nil || opts.archivistSpy != nil {
		lis := newTestListener(t)
		srv := grpc.NewServer()
		if opts.operatorSpy != nil {
			flowv1.RegisterOperatorServiceServer(srv, opts.operatorSpy)
		}
		if opts.archivistSpy != nil {
			flowv1.RegisterArchivistServiceServer(srv, opts.archivistSpy)
		}
		go func() { _ = srv.Serve(lis) }()
		t.Cleanup(func() { srv.GracefulStop() })

		conn, err := grpc.NewClient(
			lis.Addr().String(),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			t.Fatalf("newTestHandler: grpc dial failed: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close() })

		if opts.operatorSpy != nil {
			h.operator = flowv1.NewOperatorServiceClient(conn)
		}
		if opts.archivistSpy != nil {
			h.archivist = flowv1.NewArchivistServiceClient(conn)
		}
	}

	return h
}

// startEmbassyTestServer starts an Embassy gRPC server using the given handler
// on an ephemeral port and returns the listener address.
func startEmbassyTestServer(t *testing.T, handler flow.EmbassyServiceHandler) string {
	t.Helper()

	lis := newTestListener(t)
	srv := grpc.NewServer()
	flowv1.RegisterEmbassyServiceServer(srv, flow.NewEmbassyServer(handler))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })

	return lis.Addr().String()
}

// ---------------------------------------------------------------------------
// Spy: Federation (captures GetPetitionTarget calls for export tests)
// ---------------------------------------------------------------------------

type spyFederation struct {
	flowv1.UnimplementedFederationServiceServer

	mu        sync.Mutex
	calls     []*flowv1.GetPetitionTargetRequest
	returnErr error

	// Configurable response.
	authorityFlowIdentity string
	embassyEndpoint       string
}

func (s *spyFederation) GetPetitionTarget(
	_ context.Context, req *flowv1.GetPetitionTargetRequest,
) (*flowv1.GetPetitionTargetResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, req)
	if s.returnErr != nil {
		return nil, s.returnErr
	}
	return &flowv1.GetPetitionTargetResponse{
		AuthorityFlowIdentity: s.authorityFlowIdentity,
		EmbassyEndpoint:       s.embassyEndpoint,
	}, nil
}

// startFederationSpy starts a spy Federation gRPC server on an ephemeral
// port and returns the listener address.
func startFederationSpy(t *testing.T, spy *spyFederation) string {
	t.Helper()
	lis := newTestListener(t)
	srv := grpc.NewServer()
	flowv1.RegisterFederationServiceServer(srv, spy)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })
	return lis.Addr().String()
}
