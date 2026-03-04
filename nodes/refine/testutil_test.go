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

// newSpyGRPCServer creates a gRPC server with the refineSpy registered
// for all five Foundry Flow service interfaces.
func newSpyGRPCServer(spy *refineSpy) *grpc.Server {
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	flowv1.RegisterLibrarianServiceServer(srv, spy)
	flowv1.RegisterFrictionLedgerServiceServer(srv, spy)
	return srv
}

// refineSpy captures calls to feedback and artefact operations for test
// assertions. It embeds all unimplemented servers and overrides the methods
// the refine handler calls.
type refineSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFrictionLedgerServiceServer

	mu sync.Mutex

	// Configurable responses for artefact reads.
	ArtefactContents map[string]string      // artefact ID → content
	FeedbackItems    []*flowv1.FeedbackItem // feedback items returned by GetFeedback
	Laws             []*flowv1.Law          // laws returned by QueryLaws

	// Feedback operation records.
	ResolvedFeedback map[string]string                // feedback ID → resolve message
	RefusedFeedback  map[string]*flowv1.Justification // feedback ID → justification

	// Artefact operation records.
	StoredArtefacts []storedArtefact // artefacts stored
	RoutedOutputs   []string         // output names routed to
}

type storedArtefact struct {
	ArtefactID       string
	GovernedArtefact string
	Content          []byte
}

func newRefineSpy() *refineSpy {
	return &refineSpy{
		ArtefactContents: map[string]string{
			"petition": "test-petition",
			"haiku":    "test-content",
		},
		ResolvedFeedback: make(map[string]string),
		RefusedFeedback:  make(map[string]*flowv1.Justification),
	}
}

// ---------------------------------------------------------------------------
// Sidecar methods
// ---------------------------------------------------------------------------

func (s *refineSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *refineSpy) SubmitResult(
	_ context.Context, req *flowv1.SubmitResultRequest,
) (*flowv1.SubmitResultResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch a := req.GetAction().(type) {
	case *flowv1.SubmitResultRequest_Route:
		if a.Route != nil {
			s.RoutedOutputs = append(s.RoutedOutputs, a.Route.GetTarget())
		}
	default:
		// Complete / Suspend / nil — nothing to record.
	}
	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

// ---------------------------------------------------------------------------
// Archivist methods
// ---------------------------------------------------------------------------

const testContent = "test-content"

func (s *refineSpy) GetArtefact(
	_ context.Context, req *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	content := testContent
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

func (s *refineSpy) StoreArtefact(
	_ context.Context, req *flowv1.StoreArtefactRequest,
) (*flowv1.StoreArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.StoredArtefacts = append(s.StoredArtefacts, storedArtefact{
		ArtefactID:       req.GetArtefactId(),
		GovernedArtefact: req.GetGovernedArtefact(),
		Content:          req.GetContent(),
	})
	return &flowv1.StoreArtefactResponse{
		VersionHash:  "new-hash",
		IsNewVersion: true,
	}, nil
}

func (s *refineSpy) GetFeedback(
	_ context.Context, _ *flowv1.GetFeedbackRequest,
) (*flowv1.GetFeedbackResponse, error) {
	return &flowv1.GetFeedbackResponse{FeedbackItems: s.FeedbackItems}, nil
}

func (s *refineSpy) ResolveFeedback(
	_ context.Context, req *flowv1.ResolveFeedbackRequest,
) (*flowv1.ResolveFeedbackResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ResolvedFeedback[req.GetFeedbackId()] = req.GetMessage()
	return &flowv1.ResolveFeedbackResponse{UpdatedItem: &flowv1.FeedbackItem{
		Id: req.GetFeedbackId(), State: flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED,
	}}, nil
}

func (s *refineSpy) RefuseFeedback(
	_ context.Context, req *flowv1.RefuseFeedbackRequest,
) (*flowv1.RefuseFeedbackResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RefusedFeedback[req.GetFeedbackId()] = req.GetJustification()
	return &flowv1.RefuseFeedbackResponse{UpdatedItem: &flowv1.FeedbackItem{
		Id:    req.GetFeedbackId(),
		State: flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
	}}, nil
}

func (s *refineSpy) HasUnresolvedFeedback(
	_ context.Context, _ *flowv1.HasUnresolvedFeedbackRequest,
) (*flowv1.HasUnresolvedFeedbackResponse, error) {
	return &flowv1.HasUnresolvedFeedbackResponse{HasUnresolved: false}, nil
}

// ---------------------------------------------------------------------------
// Librarian methods
// ---------------------------------------------------------------------------

func (s *refineSpy) QueryLaws(
	_ context.Context, _ *flowv1.QueryLawsRequest,
) (*flowv1.QueryLawsResponse, error) {
	return &flowv1.QueryLawsResponse{Laws: s.Laws}, nil
}

func (s *refineSpy) Cite(
	_ context.Context, _ *flowv1.CiteRequest,
) (*flowv1.CiteResponse, error) {
	return &flowv1.CiteResponse{Acknowledged: true}, nil
}

func (s *refineSpy) RecordFinding(
	_ context.Context, _ *flowv1.RecordFindingRequest,
) (*flowv1.RecordFindingResponse, error) {
	return &flowv1.RecordFindingResponse{LawId: fmt.Sprintf("finding-%d", 1)}, nil
}

// ---------------------------------------------------------------------------
// FrictionLedger methods
// ---------------------------------------------------------------------------

func (s *refineSpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newSpyClient creates a flow.Client backed by a local gRPC server with
// the refineSpy registered for all five service interfaces.
func newSpyClient(t *testing.T, spy *refineSpy) *flow.Client {
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

// defaultTestConfig returns a standard refineConfig for tests.
func defaultTestConfig() *refineConfig {
	return &refineConfig{
		InputArtefacts:   []string{"petition"},
		OutputArtefact:   "haiku",
		GovernedArtefact: "haiku",
		OutputField:      "haiku",
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
