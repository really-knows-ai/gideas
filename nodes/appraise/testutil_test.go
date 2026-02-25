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
)

// newLocalListener creates a TCP listener on an ephemeral localhost port.
func newLocalListener() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

// newSpyGRPCServer creates a gRPC server with the appraiseSpy registered
// for all five Foundry Flow service interfaces.
func newSpyGRPCServer(spy *appraiseSpy) *grpc.Server {
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	flowv1.RegisterLibrarianServiceServer(srv, spy)
	flowv1.RegisterFrictionLedgerServiceServer(srv, spy)
	return srv
}

// appraiseSpy captures calls to feedback and fan-out operations for test
// assertions. It embeds all unimplemented servers and overrides the methods
// the appraise handler calls.
type appraiseSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFrictionLedgerServiceServer

	mu sync.Mutex

	// Feedback operation records.
	AcceptedFixes    []string            // feedback IDs accepted
	RejectedFixes    map[string]string   // feedback ID -> rejection message
	AcceptedRefusals []string            // feedback IDs accepted
	RejectedRefusals map[string]string   // feedback ID -> rejection message
	AddedFeedback    []addedFeedbackItem // feedback items raised
	StampedArtefacts []string            // artefact stamps applied
	RoutedOutputs    []string            // output names routed to
	CitedLaws        [][]string          // each Cite call's law IDs

	// Librarian operation records.
	RecordedFindings []recordedFinding // findings minted via RecordFinding

	// Configurable responses for artefact reads.
	ArtefactContents map[string]string      // artefact ID -> content
	FeedbackItems    []*flowv1.FeedbackItem // feedback items returned by GetFeedback
	Laws             []*flowv1.Law          // laws returned by QueryLaws

	// Fan-out operation records.
	PauseTimerCalls  int // number of PauseTimer calls
	ResumeTimerCalls int // number of ResumeTimer calls

	// Child workitem tracking.
	childCounter   int                           // auto-incrementing child ID
	ChildArtefacts map[string]map[string][]byte  // childID -> artefactID -> content
	ChildRoutes    map[string]string             // childID -> target node
	ChildStatuses  []*flowv1.ChildWorkitemStatus // configurable GetChildren response
	FanOutTasks    []fanOutRecord                // ordered record of fan-out calls

	// Configurable child review outputs keyed by child workitem ID.
	// If set, GetArtefact with TargetWorkitemId will use these.
	ChildReviewOutputs map[string][]byte // childID -> review-output JSON
}

type addedFeedbackItem struct {
	ArtefactID string
	Severity   flowv1.Severity
	Message    string
}

type recordedFinding struct {
	Goal            string
	AppliesTo       []string
	Representations []*flowv1.Representation
}

// fanOutRecord captures a single CreateChildWorkitem + StoreArtefact +
// RouteChild sequence for assertion.
type fanOutRecord struct {
	ChildID    string
	TargetNode string
	Artefacts  map[string][]byte // artefactID -> content
}

func newAppraiseSpy() *appraiseSpy {
	return &appraiseSpy{
		RejectedFixes:    make(map[string]string),
		RejectedRefusals: make(map[string]string),
		ArtefactContents: map[string]string{
			"petition": "test-petition",
			"haiku":    "test-content",
		},
		ChildArtefacts:     make(map[string]map[string][]byte),
		ChildRoutes:        make(map[string]string),
		ChildReviewOutputs: make(map[string][]byte),
	}
}

// ---------------------------------------------------------------------------
// Sidecar methods
// ---------------------------------------------------------------------------

func (s *appraiseSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *appraiseSpy) PauseTimer(
	_ context.Context, _ *flowv1.PauseTimerRequest,
) (*flowv1.PauseTimerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PauseTimerCalls++
	return &flowv1.PauseTimerResponse{Acknowledged: true}, nil
}

func (s *appraiseSpy) ResumeTimer(
	_ context.Context, _ *flowv1.ResumeTimerRequest,
) (*flowv1.ResumeTimerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ResumeTimerCalls++
	return &flowv1.ResumeTimerResponse{Acknowledged: true}, nil
}

func (s *appraiseSpy) SubmitResult(
	_ context.Context, req *flowv1.SubmitResultRequest,
) (*flowv1.SubmitResultResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ri := req.GetRoutingInstruction(); ri != nil {
		s.RoutedOutputs = append(s.RoutedOutputs, ri.GetTarget())
	}
	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

// ---------------------------------------------------------------------------
// Operator methods (fan-out support)
// ---------------------------------------------------------------------------

func (s *appraiseSpy) CreateChildWorkitem(
	_ context.Context, _ *flowv1.CreateChildWorkitemRequest,
) (*flowv1.CreateChildWorkitemResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.childCounter++
	childID := fmt.Sprintf("child-%04d", s.childCounter)
	s.ChildArtefacts[childID] = make(map[string][]byte)
	return &flowv1.CreateChildWorkitemResponse{
		ChildWorkitemId: childID,
	}, nil
}

func (s *appraiseSpy) RouteChild(
	_ context.Context, req *flowv1.RouteChildRequest,
) (*flowv1.RouteChildResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	childID := req.GetChildWorkitemId()
	target := req.GetRoutingInstruction().GetTarget()
	s.ChildRoutes[childID] = target

	// Record the fan-out task.
	s.FanOutTasks = append(s.FanOutTasks, fanOutRecord{
		ChildID:    childID,
		TargetNode: target,
		Artefacts:  s.ChildArtefacts[childID],
	})
	return &flowv1.RouteChildResponse{Accepted: true}, nil
}

func (s *appraiseSpy) GetChildren(
	_ context.Context, _ *flowv1.GetChildrenRequest,
) (*flowv1.GetChildrenResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// If ChildStatuses is configured, use it.
	if s.ChildStatuses != nil {
		return &flowv1.GetChildrenResponse{Children: s.ChildStatuses}, nil
	}

	// Default: all created children are Completed.
	children := make([]*flowv1.ChildWorkitemStatus, 0, len(s.ChildArtefacts))
	for childID := range s.ChildArtefacts {
		children = append(children, &flowv1.ChildWorkitemStatus{
			WorkitemId: childID,
			Phase:      "Completed",
		})
	}
	return &flowv1.GetChildrenResponse{Children: children}, nil
}

// ---------------------------------------------------------------------------
// Archivist methods
// ---------------------------------------------------------------------------

func (s *appraiseSpy) GetArtefact(
	_ context.Context, req *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	// If requesting from a child workitem, serve from child artefacts or
	// ChildReviewOutputs.
	if targetID := req.GetTargetWorkitemId(); targetID != "" {
		s.mu.Lock()
		defer s.mu.Unlock()

		// Check ChildReviewOutputs first (pre-configured responses).
		if raw, ok := s.ChildReviewOutputs[targetID]; ok && req.GetArtefactId() == artefactReviewOutput {
			return &flowv1.GetArtefactResponse{
				Content:     raw,
				VersionHash: "child-hash",
			}, nil
		}

		// Fall back to stored child artefacts.
		if arts, ok := s.ChildArtefacts[targetID]; ok {
			if content, ok := arts[req.GetArtefactId()]; ok {
				return &flowv1.GetArtefactResponse{
					Content:     content,
					VersionHash: "child-hash",
				}, nil
			}
		}
		return &flowv1.GetArtefactResponse{
			Content:     nil,
			VersionHash: "",
		}, nil
	}

	// Parent artefact read.
	content := "test-content"
	if s.ArtefactContents != nil {
		if c, ok := s.ArtefactContents[req.GetArtefactId()]; ok {
			content = c
		}
	}
	return &flowv1.GetArtefactResponse{
		Content:     []byte(content),
		VersionHash: "test-hash",
	}, nil
}

func (s *appraiseSpy) StoreArtefact(
	_ context.Context, req *flowv1.StoreArtefactRequest,
) (*flowv1.StoreArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// If storing on a child workitem (via FanOut), track it.
	wid := req.GetWorkitemId()
	if arts, ok := s.ChildArtefacts[wid]; ok {
		arts[req.GetArtefactId()] = req.GetContent()
	}

	return &flowv1.StoreArtefactResponse{
		VersionHash:  "new-hash",
		IsNewVersion: true,
	}, nil
}

func (s *appraiseSpy) StampArtefact(
	_ context.Context, req *flowv1.StampArtefactRequest,
) (*flowv1.StampArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.StampedArtefacts = append(s.StampedArtefacts, req.GetStampName())
	return &flowv1.StampArtefactResponse{Stamp: &flowv1.Stamp{Name: req.GetStampName()}}, nil
}

func (s *appraiseSpy) GetFeedback(
	_ context.Context, _ *flowv1.GetFeedbackRequest,
) (*flowv1.GetFeedbackResponse, error) {
	return &flowv1.GetFeedbackResponse{FeedbackItems: s.FeedbackItems}, nil
}

func (s *appraiseSpy) AddFeedback(
	_ context.Context, req *flowv1.AddFeedbackRequest,
) (*flowv1.AddFeedbackResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.AddedFeedback = append(s.AddedFeedback, addedFeedbackItem{
		ArtefactID: req.GetArtefactId(),
		Severity:   req.GetSeverity(),
		Message:    req.GetMessage(),
	})
	return &flowv1.AddFeedbackResponse{
		FeedbackId: fmt.Sprintf("fb-%d", len(s.AddedFeedback)),
	}, nil
}

func (s *appraiseSpy) AcceptFix(
	_ context.Context, req *flowv1.AcceptFixRequest,
) (*flowv1.AcceptFixResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.AcceptedFixes = append(s.AcceptedFixes, req.GetFeedbackId())
	return &flowv1.AcceptFixResponse{UpdatedItem: &flowv1.FeedbackItem{
		Id: req.GetFeedbackId(), State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED,
	}}, nil
}

func (s *appraiseSpy) RejectFix(
	_ context.Context, req *flowv1.RejectFixRequest,
) (*flowv1.RejectFixResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RejectedFixes[req.GetFeedbackId()] = req.GetMessage()
	return &flowv1.RejectFixResponse{UpdatedItem: &flowv1.FeedbackItem{
		Id: req.GetFeedbackId(), State: flowv1.FeedbackState_FEEDBACK_STATE_REJECTED,
	}}, nil
}

func (s *appraiseSpy) AcceptRefusal(
	_ context.Context, req *flowv1.AcceptRefusalRequest,
) (*flowv1.AcceptRefusalResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.AcceptedRefusals = append(s.AcceptedRefusals, req.GetFeedbackId())
	return &flowv1.AcceptRefusalResponse{UpdatedItem: &flowv1.FeedbackItem{
		Id: req.GetFeedbackId(), State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED,
	}}, nil
}

func (s *appraiseSpy) RejectRefusal(
	_ context.Context, req *flowv1.RejectRefusalRequest,
) (*flowv1.RejectRefusalResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RejectedRefusals[req.GetFeedbackId()] = req.GetMessage()
	return &flowv1.RejectRefusalResponse{UpdatedItem: &flowv1.FeedbackItem{
		Id: req.GetFeedbackId(), State: flowv1.FeedbackState_FEEDBACK_STATE_REJECTED,
	}}, nil
}

// ---------------------------------------------------------------------------
// Librarian methods
// ---------------------------------------------------------------------------

func (s *appraiseSpy) QueryLaws(
	_ context.Context, _ *flowv1.QueryLawsRequest,
) (*flowv1.QueryLawsResponse, error) {
	return &flowv1.QueryLawsResponse{Laws: s.Laws}, nil
}

func (s *appraiseSpy) Cite(
	_ context.Context, req *flowv1.CiteRequest,
) (*flowv1.CiteResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.CitedLaws = append(s.CitedLaws, req.GetLawIds())
	return &flowv1.CiteResponse{Acknowledged: true}, nil
}

func (s *appraiseSpy) RecordFinding(
	_ context.Context, req *flowv1.RecordFindingRequest,
) (*flowv1.RecordFindingResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RecordedFindings = append(s.RecordedFindings, recordedFinding{
		Goal:            req.GetGoal(),
		AppliesTo:       req.GetAppliesTo(),
		Representations: req.GetRepresentations(),
	})
	return &flowv1.RecordFindingResponse{
		LawId: fmt.Sprintf("law-%d", len(s.RecordedFindings)),
	}, nil
}

// ---------------------------------------------------------------------------
// FrictionLedger methods
// ---------------------------------------------------------------------------

func (s *appraiseSpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newSpyClient creates a flow.Client backed by a local gRPC server with
// the appraiseSpy registered for all five service interfaces.
func newSpyClient(t *testing.T, spy *appraiseSpy) *flow.Client {
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

// defaultTestConfig returns a standard appraiseConfig for tests.
func defaultTestConfig() *appraiseConfig {
	return &appraiseConfig{
		InputArtefact:    "petition",
		ReviewArtefact:   "haiku",
		GovernedArtefact: "haiku",
		StampName:        "review",
		ReviewerNode:     "reviewer",
		DivisionPrompts:  map[string]string{},
	}
}

// setupChildReviewOutputs pre-configures the spy to return review-output
// artefacts for child workitems. The outputs are keyed by the order children
// are created (child-0001, child-0002, ...).
func setupChildReviewOutputs(spy *appraiseSpy, outputs ...reviewOutput) {
	for i, out := range outputs {
		childID := fmt.Sprintf("child-%04d", i+1)
		raw, _ := json.Marshal(out)
		spy.ChildReviewOutputs[childID] = raw
	}
}

// mockModel implements flow.Model for test isolation.
// It supports both single-call and multi-call (parallel) test patterns.
type mockModel struct {
	mu sync.Mutex

	output *flow.InferOutput
	err    error

	capturedSystem string
	capturedQuery  []byte

	// For parallel tests: per-call responses keyed by call index.
	outputs []*flow.InferOutput
	callIdx int
}

func (m *mockModel) Infer(
	_ context.Context, systemPrompt string, queryPrompt []byte,
) (*flow.InferOutput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.capturedSystem = systemPrompt
	m.capturedQuery = queryPrompt

	if m.outputs != nil && m.callIdx < len(m.outputs) {
		out := m.outputs[m.callIdx]
		m.callIdx++
		return out, m.err
	}
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
