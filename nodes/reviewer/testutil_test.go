package main

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc"
)

// newLocalListener creates a TCP listener on an ephemeral localhost port.
func newLocalListener() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

// newSpyGRPCServer creates a gRPC server with the reviewerSpy registered
// for the service interfaces the Reviewer's flow.Client needs.
func newSpyGRPCServer(spy *reviewerSpy) *grpc.Server {
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	flowv1.RegisterLibrarianServiceServer(srv, spy)
	flowv1.RegisterFrictionLedgerServiceServer(srv, spy)
	return srv
}

// reviewerSpy captures calls to service operations for test assertions.
// It embeds all unimplemented servers and overrides the methods the
// reviewer handler calls.
type reviewerSpy struct {
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

func newReviewerSpy() *reviewerSpy {
	return &reviewerSpy{
		StoredArtefacts:  make(map[string][]byte),
		ArtefactContents: make(map[string][]byte),
	}
}

// ---------------------------------------------------------------------------
// Sidecar methods
// ---------------------------------------------------------------------------

func (s *reviewerSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *reviewerSpy) SubmitResult(
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

func (s *reviewerSpy) GetArtefact(
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

func (s *reviewerSpy) StoreArtefact(
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

func (s *reviewerSpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newSpyClient creates a flow.Client backed by a local gRPC server with
// the reviewerSpy registered for all service interfaces.
func newSpyClient(t *testing.T, spy *reviewerSpy) *flow.Client {
	t.Helper()

	lis, err := newLocalListener()
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

// defaultTestConfig returns a standard reviewerConfig for tests.
func defaultTestConfig() *reviewerConfig {
	return &reviewerConfig{
		InputArtefact:  "input",
		ReviewArtefact: "review",
	}
}

// mockModel implements flow.Model for test isolation.
type mockModel struct {
	mu sync.Mutex

	output *flow.InferOutput
	err    error

	capturedSystem string
	capturedQuery  []byte
}

func (m *mockModel) Infer(
	_ context.Context, systemPrompt string, queryPrompt []byte,
) (*flow.InferOutput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.capturedSystem = systemPrompt
	m.capturedQuery = queryPrompt
	return m.output, m.err
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

// newTestReviewAgent creates a ReviewAgent with the mock model injected.
func newTestReviewAgent(
	t *testing.T, mm *mockModel, spy *reviewerSpy,
	cfg *reviewerConfig, divisionSuffix string,
) *ReviewAgent {
	t.Helper()
	client := newSpyClient(t, spy)
	agent, err := NewReviewAgent(client, cfg, divisionSuffix)
	if err != nil {
		t.Fatalf("NewReviewAgent() failed: %v", err)
	}
	flow.OverrideModelForTest(agent.agent, mm)
	return agent
}
