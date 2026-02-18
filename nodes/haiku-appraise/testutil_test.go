package main

import (
	"context"
	"fmt"
	"net"
	"sync"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
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
	RejectedFixes    map[string]string   // feedback ID → rejection message
	AcceptedRefusals []string            // feedback IDs accepted
	RejectedRefusals map[string]string   // feedback ID → rejection message
	AddedFeedback    []addedFeedbackItem // feedback items raised
	StampedArtefacts []string            // artefact stamps applied
	RoutedOutputs    []string            // output names routed to
	CitedLaws        [][]string          // each Cite call's law IDs

	// Librarian operation records.
	RecordedFindings []recordedFinding // findings minted via RecordFinding
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
	return &flowv1.GetArtefactResponse{
		Content:          []byte("test-content"),
		VersionHash:      "test-hash",
		GovernedArtefact: "haiku",
	}, nil
}

func (s *appraiseSpy) StoreArtefact(
	_ context.Context, req *flowv1.StoreArtefactRequest,
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
	// Default: no existing feedback. Tests can override via a custom spy
	// or by injecting feedback items into the handler flow directly.
	return &flowv1.GetFeedbackResponse{FeedbackItems: nil}, nil
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
	return &flowv1.QueryLawsResponse{Laws: nil}, nil
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
