package main

import (
	"context"
	"fmt"
	"sync"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/nodeutil"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc"
)

// newSpyGRPCServer creates a gRPC server with the appraiserSpy registered
// for the service interfaces the Appraiser's flow.Client needs.
func newSpyGRPCServer(spy *appraiserSpy) *grpc.Server {
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	flowv1.RegisterLibrarianServiceServer(srv, spy)
	flowv1.RegisterFrictionLedgerServiceServer(srv, spy)
	return srv
}

// appraiserSpy captures calls to service operations for test assertions.
// It embeds all unimplemented servers and overrides the methods the
// appraiser handler calls.
type appraiserSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFrictionLedgerServiceServer

	mu sync.Mutex

	// Artefact storage records.
	StoredArtefacts map[string][]byte // artefact ID -> content
	CompleteCalls   int               // number of Complete calls

	// Configurable responses for artefact reads.
	ArtefactContents map[string][]byte // artefact ID -> content
}

func newAppraiserSpy() *appraiserSpy {
	return &appraiserSpy{
		StoredArtefacts:  make(map[string][]byte),
		ArtefactContents: make(map[string][]byte),
	}
}

// ---------------------------------------------------------------------------
// Sidecar methods
// ---------------------------------------------------------------------------

func (s *appraiserSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *appraiserSpy) SubmitResult(
	_ context.Context, req *flowv1.SubmitResultRequest,
) (*flowv1.SubmitResultResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ri := req.GetAction()
	if _, ok := ri.(*flowv1.SubmitResultRequest_Complete); ok {
		s.CompleteCalls++
	}
	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

// ---------------------------------------------------------------------------
// Archivist methods
// ---------------------------------------------------------------------------

func (s *appraiserSpy) GetArtefact(
	_ context.Context, req *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	content, ok := s.ArtefactContents[req.GetArtefactId()]
	if !ok {
		return nil, fmt.Errorf("artefact %q not found", req.GetArtefactId())
	}
	return &flowv1.GetArtefactResponse{
		Content:     content,
		VersionHash: "test-hash",
	}, nil
}

func (s *appraiserSpy) StoreArtefact(
	_ context.Context, req *flowv1.StoreArtefactRequest,
) (*flowv1.StoreArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.StoredArtefacts[req.GetArtefactId()] = req.GetContent()
	return &flowv1.StoreArtefactResponse{
		VersionHash:  "new-hash",
		IsNewVersion: true,
	}, nil
}

// ---------------------------------------------------------------------------
// FrictionLedger methods
// ---------------------------------------------------------------------------

func (s *appraiserSpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newSpyClient creates a flow.Client backed by a local gRPC server with
// the appraiserSpy registered for all service interfaces.
func newSpyClient(t *testing.T, spy *appraiserSpy) *flow.Client {
	t.Helper()

	lis, err := nodeutil.NewLocalListener()
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	srv := newSpyGRPCServer(spy)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })

	client, err := flow.NewClient(flow.WithSidecarAddress(lis.Addr().String()))
	if err != nil {
		t.Fatalf("NewClient() failed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return client
}

// defaultTestConfig returns a standard appraiserNodeConfig for tests.
func defaultTestConfig() *appraiserNodeConfig {
	return &appraiserNodeConfig{
		InputArtefacts: []string{"input"},
		ReviewArtefact: "review",
	}
}

// defaultCost returns a standard CostMetadata for tests.
func defaultCost() *flow.CostMetadata {
	return &flow.CostMetadata{
		Model:        "test-model",
		InputTokens:  10,
		OutputTokens: 5,
		DurationMs:   100,
	}
}

// newTestAppraiserAgent creates an AppraiserAgent with a custom InferFunc.
// opts may be nil to use baked-in defaults.
// personality is the optional appraiser personality string for the system prompt.
func newTestAppraiserAgent(
	t *testing.T, inferFn flow.InferFunc, spy *appraiserSpy,
	cfg *appraiserNodeConfig, personality string, opts *AppraiserAgentOpts,
) *AppraiserAgent {
	t.Helper()
	client := newSpyClient(t, spy)
	agent, err := NewAppraiserAgent(client, cfg, personality, opts)
	if err != nil {
		t.Fatalf("NewAppraiserAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(agent.agent, inferFn)
	return agent
}
