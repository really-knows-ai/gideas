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

// ---------------------------------------------------------------------------
// Spy Server
// ---------------------------------------------------------------------------

// codificationSpy implements the gRPC services the Codification node depends
// on: Sidecar, Operator, and Archivist.
type codificationSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer

	mu sync.Mutex

	// Configurable artefact store: artefactID → content.
	Artefacts map[string][]byte

	// Configurable child artefacts: "childID:artefactID" → content.
	ChildArtefacts map[string][]byte

	// Configurable children returned by GetChildren (for AwaitChildren).
	// When nil, auto-generates completed children from CreatedChildren.
	Children []*flowv1.ChildWorkitemStatus

	// Auto-created child IDs (returned by CreateChildWorkitem).
	nextChildID int

	// Configurable error returns.
	GetArtefactErr   error
	StoreArtefactErr error
	RouteToOutputErr error
	CreateChildErr   error
	RouteChildErr    error
	GetChildrenErr   error

	// Recorded operations.
	StoredArtefacts      map[string][]byte // artefactID → content
	ChildStoredArtefacts map[string][]byte // "childID:artefactID" → content
	RoutedOutputs        []string
	CreatedChildren      []string
	RoutedChildren       []routedChild
}

type routedChild struct {
	ChildID    string
	TargetNode string
}

func newCodificationSpy() *codificationSpy {
	return &codificationSpy{
		Artefacts:            make(map[string][]byte),
		ChildArtefacts:       make(map[string][]byte),
		StoredArtefacts:      make(map[string][]byte),
		ChildStoredArtefacts: make(map[string][]byte),
	}
}

// ---------------------------------------------------------------------------
// Sidecar methods
// ---------------------------------------------------------------------------

func (s *codificationSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *codificationSpy) PauseTimer(
	_ context.Context, _ *flowv1.PauseTimerRequest,
) (*flowv1.PauseTimerResponse, error) {
	return &flowv1.PauseTimerResponse{Acknowledged: true}, nil
}

func (s *codificationSpy) ResumeTimer(
	_ context.Context, _ *flowv1.ResumeTimerRequest,
) (*flowv1.ResumeTimerResponse, error) {
	return &flowv1.ResumeTimerResponse{Acknowledged: true}, nil
}

func (s *codificationSpy) SubmitResult(
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
		// Complete / Suspend — no-op.
	}
	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

func (s *codificationSpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

// ---------------------------------------------------------------------------
// Operator methods
// ---------------------------------------------------------------------------

func (s *codificationSpy) CreateChildWorkitem(
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

func (s *codificationSpy) RouteChild(
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

func (s *codificationSpy) GetChildren(
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

func (s *codificationSpy) GetArtefact(
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

func (s *codificationSpy) StoreArtefact(
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

func newSpyGRPCServer(spy *codificationSpy) *grpc.Server {
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	return srv
}

func setupCodificationTest(t *testing.T, spy *codificationSpy) *flow.Client {
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

// ---------------------------------------------------------------------------
// Seed Helpers
// ---------------------------------------------------------------------------

// seedPetition populates the spy with a petition artefact containing the
// given changes.
func seedPetition(spy *codificationSpy, changes ...petitionChange) {
	pet := petition{
		Petition: petitionBody{
			Context: petitionContext{
				Trigger:         "deadlock-resolution",
				VerdictDecision: "favour_refiner",
				Justification:   "Strong argument for change",
			},
			Changes:            changes,
			ProseJustification: "Test prose justification",
		},
	}
	data, _ := json.Marshal(pet)
	spy.Artefacts[defaultPetitionArtefact] = data
}

// seedCodificationResult populates the spy with a codification-result child
// artefact so CollectArtefacts can find it.
func seedCodificationResult(spy *codificationSpy, childID, typ, content string) {
	cr := codificationResult{
		Type:    typ,
		Content: content,
	}
	data, _ := json.Marshal(cr)
	spy.ChildArtefacts[childID+":"+artefactCodificationResult] = data
}

// defaultTestConfig returns a codificationConfig suitable for most tests.
func defaultTestConfig() *codificationConfig {
	return &codificationConfig{
		CodificationNodes: []string{"codify-smt"},
	}
}

// ---------------------------------------------------------------------------
// Assertion Helpers
// ---------------------------------------------------------------------------

// assertRoutedTo verifies the spy recorded exactly one route to the expected
// output name.
func assertRoutedTo(t *testing.T, spy *codificationSpy, expected string) {
	t.Helper()
	if len(spy.RoutedOutputs) != 1 {
		t.Fatalf("expected 1 routed output, got %d: %v", len(spy.RoutedOutputs), spy.RoutedOutputs)
	}
	if spy.RoutedOutputs[0] != expected {
		t.Errorf("routed to %q, want %q", spy.RoutedOutputs[0], expected)
	}
}

// storedPetition extracts and unmarshals the stored petition from the spy.
func storedPetition(t *testing.T, spy *codificationSpy) petition {
	t.Helper()
	raw, ok := spy.StoredArtefacts[defaultPetitionArtefact]
	if !ok {
		t.Fatal("petition artefact was not stored")
	}
	var pet petition
	if err := json.Unmarshal(raw, &pet); err != nil {
		t.Fatalf("unmarshal stored petition: %v", err)
	}
	return pet
}

// containsSubstring checks if s contains substr.
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && contains(s, substr))
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
