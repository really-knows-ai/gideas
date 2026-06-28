package main

import (
	"context"
	"fmt"
	"sync"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
)

// newSpyGRPCServer creates a gRPC server with the quenchSpy registered
// for all five Foundry Flow service interfaces.
func newSpyGRPCServer(spy *quenchSpy) *grpc.Server {
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	flowv1.RegisterLibrarianServiceServer(srv, spy)
	flowv1.RegisterFrictionLedgerServiceServer(srv, spy)
	return srv
}

// quenchSpy captures calls to feedback, stamp, and routing operations
// for test assertions. It embeds all unimplemented servers and overrides
// the methods the quench handler calls.
type quenchSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFrictionLedgerServiceServer

	mu sync.Mutex

	// Configurable state returned by GetArtefact.
	HaikuContent string

	// Configurable state returned by GetFeedback.
	FeedbackItems []*flowv1.FeedbackItem

	// Feedback operation records.
	AcceptedFixes []string          // feedback IDs accepted
	RejectedFixes map[string]string // feedback ID → rejection message
	AddedFeedback []addedFeedbackItem
	StampedNames  []string // stamp names applied
	RoutedOutputs []string // output names routed to
}

type addedFeedbackItem struct {
	ArtefactID string
	CanWontFix bool
	Message    string
}

func newQuenchSpy(haiku string) *quenchSpy {
	return &quenchSpy{
		HaikuContent:  haiku,
		RejectedFixes: make(map[string]string),
	}
}

// ---------------------------------------------------------------------------
// Sidecar methods
// ---------------------------------------------------------------------------

func (s *quenchSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *quenchSpy) SubmitResult(
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

func (s *quenchSpy) GetArtefact(
	_ context.Context, _ *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	return &flowv1.GetArtefactResponse{
		Content:          []byte(s.HaikuContent),
		VersionHash:      "test-hash",
		GovernedArtefact: "haiku",
	}, nil
}

func (s *quenchSpy) GetFeedback(
	_ context.Context, _ *flowv1.GetFeedbackRequest,
) (*flowv1.GetFeedbackResponse, error) {
	return &flowv1.GetFeedbackResponse{
		FeedbackItems: s.FeedbackItems,
	}, nil
}

func (s *quenchSpy) AddFeedback(
	_ context.Context, req *flowv1.AddFeedbackRequest,
) (*flowv1.AddFeedbackResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.AddedFeedback = append(s.AddedFeedback, addedFeedbackItem{
		ArtefactID: req.GetArtefactId(),
		CanWontFix: req.GetCanWontFix(),
		Message:    req.GetMessage(),
	})
	return &flowv1.AddFeedbackResponse{
		FeedbackId: fmt.Sprintf("fb-%d", len(s.AddedFeedback)),
	}, nil
}

func (s *quenchSpy) StampArtefact(
	_ context.Context, req *flowv1.StampArtefactRequest,
) (*flowv1.StampArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.StampedNames = append(s.StampedNames, req.GetStampName())
	return &flowv1.StampArtefactResponse{
		Stamp: &flowv1.Stamp{Name: req.GetStampName()},
	}, nil
}

func (s *quenchSpy) AcceptFix(
	_ context.Context, req *flowv1.AcceptFixRequest,
) (*flowv1.AcceptFixResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.AcceptedFixes = append(s.AcceptedFixes, req.GetFeedbackId())
	return &flowv1.AcceptFixResponse{UpdatedItem: &flowv1.FeedbackItem{
		Id:    req.GetFeedbackId(),
		State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED,
	}}, nil
}

func (s *quenchSpy) RejectFix(
	_ context.Context, req *flowv1.RejectFixRequest,
) (*flowv1.RejectFixResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.RejectedFixes[req.GetFeedbackId()] = req.GetMessage()
	return &flowv1.RejectFixResponse{UpdatedItem: &flowv1.FeedbackItem{
		Id:    req.GetFeedbackId(),
		State: flowv1.FeedbackState_FEEDBACK_STATE_REJECTED,
	}}, nil
}

// ---------------------------------------------------------------------------
// FrictionLedger methods
// ---------------------------------------------------------------------------

func (s *quenchSpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}
