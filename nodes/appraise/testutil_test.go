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

// newSpyGRPCServer creates a gRPC server with the appraiseSpy registered
// for all five Foundry Flow service interfaces.
func newSpyGRPCServer(spy *appraiseSpy) *grpc.Server {
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	flowv1.RegisterLibrarianServiceServer(srv, spy)
	flowv1.RegisterFlowMonitorServiceServer(srv, spy)
	return srv
}

// appraiseSpy captures calls to feedback operations for test assertions.
// It embeds all unimplemented servers and overrides the methods the
// appraise handler calls.
type appraiseSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFlowMonitorServiceServer

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

func newAppraiseSpy() *appraiseSpy {
	return &appraiseSpy{
		RejectedFixes:    make(map[string]string),
		RejectedRefusals: make(map[string]string),
		ArtefactContents: map[string]string{
			"petition": "test-petition",
			"haiku":    "test-content",
		},
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
// Archivist methods
// ---------------------------------------------------------------------------

func (s *appraiseSpy) GetArtefact(
	_ context.Context, req *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
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
	_ context.Context, _ *flowv1.StoreArtefactRequest,
) (*flowv1.StoreArtefactResponse, error) {
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
// Monitor methods
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
		Model:            "test-model",
	}
}

// mockProvider implements flow.Provider for test isolation.
type mockProvider struct {
	mu sync.Mutex

	output *flow.InferOutput
	err    error

	capturedModel  string
	capturedSystem string
	capturedQuery  []byte

	// For parallel tests: per-call responses keyed by call index.
	outputs []*flow.InferOutput
	callIdx int
}

func (m *mockProvider) Infer(
	_ context.Context, model, systemPrompt string, queryPrompt []byte,
) (*flow.InferOutput, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.capturedModel = model
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
