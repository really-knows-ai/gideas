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

// newSpyGRPCServer creates a gRPC server with the assaySpy registered
// for all five Foundry Flow service interfaces.
func newSpyGRPCServer(spy *assaySpy) *grpc.Server {
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	flowv1.RegisterLibrarianServiceServer(srv, spy)
	flowv1.RegisterFlowMonitorServiceServer(srv, spy)
	return srv
}

// assaySpy captures calls to judicial operations for test assertions.
// It embeds all unimplemented servers and overrides the methods the
// assay handler calls.
type assaySpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFlowMonitorServiceServer

	mu sync.Mutex

	// Feedback operation records.
	ResolvedFeedback []string // feedback IDs resolved
	RoutedOutputs    []string // output names routed to

	// Librarian operation records.
	WrittenLaws []writtenLaw // laws minted via WriteLaw
}

type writtenLaw struct {
	Goal            string
	Tier            flowv1.LawTier
	AppliesTo       []string
	Representations []*flowv1.Representation
}

func newAssaySpy() *assaySpy {
	return &assaySpy{}
}

// ---------------------------------------------------------------------------
// Sidecar methods
// ---------------------------------------------------------------------------

func (s *assaySpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *assaySpy) SubmitResult(
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

func (s *assaySpy) GetArtefact(
	_ context.Context, req *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	return &flowv1.GetArtefactResponse{
		Content:          []byte("test-haiku\nline two\nline three"),
		VersionHash:      "test-hash",
		GovernedArtefact: "haiku",
	}, nil
}

func (s *assaySpy) GetFeedback(
	_ context.Context, _ *flowv1.GetFeedbackRequest,
) (*flowv1.GetFeedbackResponse, error) {
	// Default: no existing feedback. Tests can override via custom spy.
	return &flowv1.GetFeedbackResponse{FeedbackItems: nil}, nil
}

func (s *assaySpy) ResolveFeedback(
	_ context.Context, req *flowv1.ResolveFeedbackRequest,
) (*flowv1.ResolveFeedbackResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ResolvedFeedback = append(s.ResolvedFeedback, req.GetFeedbackId())
	return &flowv1.ResolveFeedbackResponse{
		UpdatedItem: &flowv1.FeedbackItem{
			Id:    req.GetFeedbackId(),
			State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED,
		},
	}, nil
}

// ---------------------------------------------------------------------------
// Librarian methods
// ---------------------------------------------------------------------------

func (s *assaySpy) QueryLaws(
	_ context.Context, _ *flowv1.QueryLawsRequest,
) (*flowv1.QueryLawsResponse, error) {
	return &flowv1.QueryLawsResponse{Laws: nil}, nil
}

func (s *assaySpy) WriteLaw(
	_ context.Context, req *flowv1.WriteLawRequest,
) (*flowv1.WriteLawResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	law := req.GetLaw()
	s.WrittenLaws = append(s.WrittenLaws, writtenLaw{
		Goal:            law.GetGoal(),
		Tier:            law.GetTier(),
		AppliesTo:       law.GetAppliesTo(),
		Representations: law.GetRepresentations(),
	})

	return &flowv1.WriteLawResponse{
		LawId:       fmt.Sprintf("law-%d", len(s.WrittenLaws)),
		VersionHash: "law-hash",
	}, nil
}

// ---------------------------------------------------------------------------
// Monitor methods
// ---------------------------------------------------------------------------

func (s *assaySpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}
