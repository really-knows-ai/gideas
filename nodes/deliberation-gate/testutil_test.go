package main

import (
	"context"
	"encoding/json"
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

// gateSpy implements the gRPC services the Deliberation Gate depends on.
type gateSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer

	mu sync.Mutex

	// Configurable children returned by GetChildren.
	Children []*flowv1.ChildWorkitemStatus

	// Configurable child artefacts: "childID:artefactID" → content.
	ChildArtefacts map[string][]byte

	// Configurable parent artefacts: artefactID → content.
	Artefacts map[string][]byte

	// Configurable error returns.
	GetChildrenErr   error
	GetArtefactErr   error
	StoreArtefactErr error
	RouteToOutputErr error

	// Recorded operations.
	StoredArtefacts map[string][]byte
	RoutedOutputs   []string
}

func newGateSpy() *gateSpy {
	return &gateSpy{
		ChildArtefacts:  make(map[string][]byte),
		Artefacts:       make(map[string][]byte),
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

func (s *gateSpy) SubmitResult(
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

func (s *gateSpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

// ---------------------------------------------------------------------------
// Operator methods
// ---------------------------------------------------------------------------

func (s *gateSpy) GetChildren(
	_ context.Context, _ *flowv1.GetChildrenRequest,
) (*flowv1.GetChildrenResponse, error) {
	if s.GetChildrenErr != nil {
		return nil, s.GetChildrenErr
	}
	return &flowv1.GetChildrenResponse{Children: s.Children}, nil
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

	// Child artefact request (has TargetWorkitemId).
	if target := req.GetTargetWorkitemId(); target != "" {
		key := target + ":" + req.GetArtefactId()
		content, ok := s.ChildArtefacts[key]
		if !ok {
			return nil, status.Errorf(codes.NotFound, "child artefact %q not found", key)
		}
		return &flowv1.GetArtefactResponse{Content: content}, nil
	}

	// Parent artefact request.
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

func newLocalListener() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

func newSpyGRPCServer(spy *gateSpy) *grpc.Server {
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
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

// seedChildren adds completed children with verdict artefacts to the spy.
func seedChildren(spy *gateSpy, verdicts map[string]jurorVerdict) {
	for childID, verdict := range verdicts {
		spy.Children = append(spy.Children, &flowv1.ChildWorkitemStatus{
			WorkitemId: childID,
			Phase:      "Completed",
		})
		verdictJSON, _ := json.Marshal(verdict)
		spy.ChildArtefacts[childID+":verdict"] = verdictJSON
	}
}
