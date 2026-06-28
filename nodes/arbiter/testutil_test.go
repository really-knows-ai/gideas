package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/nodeutil"
	"github.com/gideas/flow/nodes/internal/tally"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Spy Server
// ---------------------------------------------------------------------------

// arbiterSpy implements the gRPC services the Arbiter depends on:
// Sidecar, Operator, and Archivist.
type arbiterSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer

	mu sync.Mutex

	// ── Configurable inputs ─────────────────────────────────────────

	// Artefacts holds parent artefact content keyed by artefact ID.
	Artefacts map[string][]byte

	// ChildArtefacts holds child artefact content keyed as
	// "childID:artefactID".
	ChildArtefacts map[string][]byte

	// Children is returned by GetChildren. When non-nil, overrides
	// auto-generation from CreatedChildren. Use this for post-resume
	// tests where CompletionReason must be set.
	Children []*flowv1.ChildWorkitemStatus

	// nextChildID auto-increments for CreateChildWorkitem.
	nextChildID int

	// ── Configurable error returns ──────────────────────────────────

	GetArtefactErr   error
	StoreArtefactErr error
	CreateChildErr   error
	RouteChildErr    error
	GetChildrenErr   error
	CompleteErr      error
	SuspendErr       error
	RouteToOutputErr error

	// ── Recorded operations ─────────────────────────────────────────

	// CompletedReasons records CompletionReason from Complete actions.
	CompletedReasons []flowv1.CompletionReason

	// SuspendActions records suspend conditions.
	SuspendActions []suspendRecord

	// RoutedOutputs records output names from RouteToOutput actions.
	RoutedOutputs []string

	// CreatedChildren records child workitem IDs.
	CreatedChildren []string

	// RoutedChildren records child routing instructions.
	RoutedChildren []routedChild

	// ChildStoredArtefacts records artefact content stored on child
	// workitems, keyed as "childID:artefactID".
	ChildStoredArtefacts map[string][]byte

	// PauseTimerCalled tracks whether PauseTimer was invoked.
	PauseTimerCalled bool

	// ResumeTimerCalled tracks whether ResumeTimer was invoked.
	ResumeTimerCalled bool
}

type suspendRecord struct {
	Condition string
}

type routedChild struct {
	ChildID    string
	TargetNode string
}

func newArbiterSpy() *arbiterSpy {
	return &arbiterSpy{
		Artefacts:            make(map[string][]byte),
		ChildArtefacts:       make(map[string][]byte),
		ChildStoredArtefacts: make(map[string][]byte),
	}
}

// ---------------------------------------------------------------------------
// Sidecar methods
// ---------------------------------------------------------------------------

func (s *arbiterSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *arbiterSpy) PauseTimer(
	_ context.Context, _ *flowv1.PauseTimerRequest,
) (*flowv1.PauseTimerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PauseTimerCalled = true
	return &flowv1.PauseTimerResponse{Acknowledged: true}, nil
}

func (s *arbiterSpy) ResumeTimer(
	_ context.Context, _ *flowv1.ResumeTimerRequest,
) (*flowv1.ResumeTimerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ResumeTimerCalled = true
	return &flowv1.ResumeTimerResponse{Acknowledged: true}, nil
}

func (s *arbiterSpy) SubmitResult(
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

	case *flowv1.SubmitResultRequest_Complete:
		if s.CompleteErr != nil {
			return nil, s.CompleteErr
		}
		reason := flowv1.CompletionReason_COMPLETION_REASON_UNSPECIFIED
		if a.Complete != nil {
			reason = a.Complete.GetReason()
		}
		s.CompletedReasons = append(s.CompletedReasons, reason)

	case *flowv1.SubmitResultRequest_Suspend:
		if s.SuspendErr != nil {
			return nil, s.SuspendErr
		}
		rec := suspendRecord{}
		if a.Suspend != nil {
			rec.Condition = a.Suspend.GetCondition()
		}
		s.SuspendActions = append(s.SuspendActions, rec)

	case nil:
		// No action set — treat as no-op.
	}
	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

func (s *arbiterSpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

// ---------------------------------------------------------------------------
// Operator methods
// ---------------------------------------------------------------------------

func (s *arbiterSpy) CreateChildWorkitem(
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

func (s *arbiterSpy) RouteChild(
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

func (s *arbiterSpy) GetChildren(
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

func (s *arbiterSpy) GetArtefact(
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
			// Also check ChildStoredArtefacts (written during the test run).
			content, ok = s.ChildStoredArtefacts[key]
			if !ok {
				return nil, status.Errorf(codes.NotFound, "child artefact %q not found", key)
			}
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

func (s *arbiterSpy) StoreArtefact(
	_ context.Context, req *flowv1.StoreArtefactRequest,
) (*flowv1.StoreArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.StoreArtefactErr != nil {
		return nil, s.StoreArtefactErr
	}

	// Child artefact store (workitem ID differs from test parent).
	if req.GetWorkitemId() != "" && req.GetWorkitemId() != testWorkitemID {
		key := req.GetWorkitemId() + ":" + req.GetArtefactId()
		s.ChildStoredArtefacts[key] = req.GetContent()
	}
	return &flowv1.StoreArtefactResponse{
		VersionHash:  "test-hash",
		IsNewVersion: true,
	}, nil
}

// ---------------------------------------------------------------------------
// Test setup
// ---------------------------------------------------------------------------

const testWorkitemID = "test-workitem"

func newSpyGRPCServer(spy *arbiterSpy) *grpc.Server {
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	return srv
}

func setupArbiterTest(t *testing.T, spy *arbiterSpy) *flow.Client {
	t.Helper()

	lis, err := nodeutil.NewLocalListener()
	if err != nil {
		t.Fatalf("NewLocalListener: %v", err)
	}

	srv := newSpyGRPCServer(spy)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })

	t.Setenv(flow.EnvWorkitemID, testWorkitemID)

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

// seedEvidence populates the spy with an evidence-bundle artefact.
func seedEvidence(spy *arbiterSpy, content string) {
	spy.Artefacts[artefactEvidenceBundle] = []byte(content)
}

// seedJurorVerdict populates the spy with a verdict artefact on a child
// workitem so that CollectVotes / CollectArtefacts can read it.
func seedJurorVerdict(spy *arbiterSpy, childID, outcome, reasoning string) {
	v := tally.JurorVote{Outcome: outcome, Reasoning: reasoning}
	data, _ := json.Marshal(v)
	spy.ChildArtefacts[childID+":"+tally.ArtefactVerdict] = data
}

// defaultTestConfig returns an arbiterConfig suitable for most tests:
// 3 jurors, 1 round, simple majority.
func defaultTestConfig() *arbiterConfig {
	return &arbiterConfig{
		JurySize:  3,
		MaxRounds: 1,
	}
}

// ---------------------------------------------------------------------------
// Assertion Helpers
// ---------------------------------------------------------------------------

// assertSuspended verifies the spy recorded exactly one suspend action.
func assertSuspended(t *testing.T, spy *arbiterSpy) {
	t.Helper()
	if len(spy.SuspendActions) != 1 {
		t.Fatalf("expected 1 suspend action, got %d", len(spy.SuspendActions))
	}
}

// assertCompleted verifies the spy recorded exactly one completion with the
// expected reason.
func assertCompleted(t *testing.T, spy *arbiterSpy, reason flowv1.CompletionReason) {
	t.Helper()
	if len(spy.CompletedReasons) != 1 {
		t.Fatalf("expected 1 completion, got %d: %v", len(spy.CompletedReasons), spy.CompletedReasons)
	}
	if spy.CompletedReasons[0] != reason {
		t.Errorf("completion reason = %v, want %v", spy.CompletedReasons[0], reason)
	}
}

// assertRoutedTo verifies the spy recorded exactly one route to the expected
// output name.
func assertRoutedTo(t *testing.T, spy *arbiterSpy, expected string) {
	t.Helper()
	if len(spy.RoutedOutputs) != 1 {
		t.Fatalf("expected 1 routed output, got %d: %v", len(spy.RoutedOutputs), spy.RoutedOutputs)
	}
	if spy.RoutedOutputs[0] != expected {
		t.Errorf("routed to %q, want %q", spy.RoutedOutputs[0], expected)
	}
}

// clerkChildVerdictContext extracts and unmarshals the verdict-context
// artefact stored on the clerk child.
func clerkChildVerdictContext(t *testing.T, spy *arbiterSpy) verdictContext {
	t.Helper()

	// Find the clerk child (the last created child after juror children).
	if len(spy.CreatedChildren) == 0 {
		t.Fatal("no children created")
	}
	clerkChildID := spy.CreatedChildren[len(spy.CreatedChildren)-1]

	key := clerkChildID + ":" + artefactVerdictContext
	raw, ok := spy.ChildStoredArtefacts[key]
	if !ok {
		t.Fatalf("verdict-context not stored on clerk child %s", clerkChildID)
	}
	var vctx verdictContext
	if err := json.Unmarshal(raw, &vctx); err != nil {
		t.Fatalf("unmarshal verdict-context: %v", err)
	}
	return vctx
}
