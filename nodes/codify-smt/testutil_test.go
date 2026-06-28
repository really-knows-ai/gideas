package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/nodeutil"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Spy Server
// ---------------------------------------------------------------------------

// codifySpy implements the gRPC services the codify-smt node depends on.
type codifySpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer

	mu sync.Mutex

	// Configurable artefact store (artefactID -> content).
	Artefacts map[string][]byte

	// Configurable error returns.
	GetArtefactErr   error
	StoreArtefactErr error
	CompleteErr      error

	// Recorded operations.
	StoredArtefacts map[string][]byte // artefactID -> content
	Completed       bool
	CompletedTarget string
}

func newCodifySpy() *codifySpy {
	return &codifySpy{
		Artefacts:       make(map[string][]byte),
		StoredArtefacts: make(map[string][]byte),
	}
}

// ---------------------------------------------------------------------------
// Sidecar methods
// ---------------------------------------------------------------------------

func (s *codifySpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *codifySpy) SubmitResult(
	_ context.Context, req *flowv1.SubmitResultRequest,
) (*flowv1.SubmitResultResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch req.GetAction().(type) {
	case *flowv1.SubmitResultRequest_Complete:
		if s.CompleteErr != nil {
			return nil, s.CompleteErr
		}
		s.Completed = true
	case nil:
		// No action set — treat as no-op.
	default:
		// Route / Suspend — no-op for codify spy.
	}
	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

func (s *codifySpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

// ---------------------------------------------------------------------------
// Archivist methods
// ---------------------------------------------------------------------------

func (s *codifySpy) GetArtefact(
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

func (s *codifySpy) StoreArtefact(
	_ context.Context, req *flowv1.StoreArtefactRequest,
) (*flowv1.StoreArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.StoreArtefactErr != nil {
		return nil, s.StoreArtefactErr
	}

	s.StoredArtefacts[req.GetArtefactId()] = req.GetContent()
	return &flowv1.StoreArtefactResponse{
		VersionHash:  "test-hash",
		IsNewVersion: true,
	}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newSpyGRPCServer(spy *codifySpy) *grpc.Server {
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	return srv
}

func setupCodifyTest(t *testing.T, spy *codifySpy) *flow.Client {
	t.Helper()

	lis, err := nodeutil.NewLocalListener()
	if err != nil {
		t.Fatalf("NewLocalListener: %v", err)
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

// seedGoal populates the spy with a codification-goal artefact.
func seedGoal(spy *codifySpy, goal string, appliesTo []string, tier int32, action string) {
	g := codificationGoal{
		Goal:      goal,
		AppliesTo: appliesTo,
		Tier:      tier,
		Action:    action,
	}
	data, _ := json.Marshal(g)
	spy.Artefacts[artefactCodificationGoal] = data
}
