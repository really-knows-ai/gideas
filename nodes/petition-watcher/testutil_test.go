package main

import (
	"context"
	"maps"
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
// Spy: Federation (sends pre-configured petition outcome events)
// ---------------------------------------------------------------------------

type spyFederation struct {
	flowv1.UnimplementedFederationServiceServer

	mu        sync.Mutex
	events    []*flowv1.PetitionOutcomeEvent
	returnErr error
	subCalls  int // number of SubscribePetitionOutcomes calls
}

func (s *spyFederation) SubscribePetitionOutcomes(
	_ *flowv1.SubscribePetitionOutcomesRequest,
	stream grpc.ServerStreamingServer[flowv1.PetitionOutcomeEvent],
) error {
	s.mu.Lock()
	s.subCalls++
	events := make([]*flowv1.PetitionOutcomeEvent, len(s.events))
	copy(events, s.events)
	returnErr := s.returnErr
	s.mu.Unlock()

	if returnErr != nil {
		return returnErr
	}
	for _, evt := range events {
		if err := stream.Send(evt); err != nil {
			return err
		}
	}
	return nil
}

func (s *spyFederation) getSubCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subCalls
}

// ---------------------------------------------------------------------------
// Spy: full Sidecar (all five services for handler tests via flow.Client)
// ---------------------------------------------------------------------------

// handlerSpy captures calls made by handleOutcome through the SDK client.
type handlerSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFrictionLedgerServiceServer

	mu              sync.Mutex
	storedArtefacts []*flowv1.StoreArtefactRequest
	routedOutputs   []string
	heartbeatCount  int
	completedCount  int
}

func (s *handlerSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heartbeatCount++
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *handlerSpy) StoreArtefact(
	_ context.Context, req *flowv1.StoreArtefactRequest,
) (*flowv1.StoreArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storedArtefacts = append(s.storedArtefacts, req)
	return &flowv1.StoreArtefactResponse{
		VersionHash:  "hash-test",
		IsNewVersion: true,
	}, nil
}

func (s *handlerSpy) SubmitResult(
	_ context.Context, req *flowv1.SubmitResultRequest,
) (*flowv1.SubmitResultResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch a := req.GetAction().(type) {
	case *flowv1.SubmitResultRequest_Route:
		if a.Route != nil {
			s.routedOutputs = append(s.routedOutputs, a.Route.GetTarget())
		}
	case *flowv1.SubmitResultRequest_Complete:
		s.completedCount++
	default:
		// Suspend / nil — nothing to record.
	}
	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

func (s *handlerSpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newTestListener creates a TCP listener on an ephemeral localhost port.
func newTestListener(t *testing.T) net.Listener {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	return lis
}

// newHandlerTestClient creates a flow.Client backed by a local gRPC server
// with the handlerSpy providing all five service interfaces.
func newHandlerTestClient(t *testing.T, spy *handlerSpy) *flow.Client {
	t.Helper()

	lis := newTestListener(t)
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	flowv1.RegisterLibrarianServiceServer(srv, spy)
	flowv1.RegisterFrictionLedgerServiceServer(srv, spy)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })

	client, err := flow.NewClient(flow.WithSidecarAddress(lis.Addr().String()))
	if err != nil {
		t.Fatalf("NewClient() failed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return client
}

// startFederationServer creates a gRPC server with the spy Federation and
// returns the listener address.
func startFederationServer(t *testing.T, spy *spyFederation) string {
	t.Helper()
	lis := newTestListener(t)
	srv := grpc.NewServer()
	flowv1.RegisterFederationServiceServer(srv, spy)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })
	return lis.Addr().String()
}

// makeAcceptedEvent creates an ACCEPTED PetitionOutcomeEvent.
func makeAcceptedEvent(petitionID, publishedLawID string) *flowv1.PetitionOutcomeEvent {
	return &flowv1.PetitionOutcomeEvent{
		PetitionId:     petitionID,
		Outcome:        flowv1.PetitionOutcome_PETITION_OUTCOME_ACCEPTED,
		PublishedLawId: publishedLawID,
	}
}

// makeRejectedEvent creates a REJECTED PetitionOutcomeEvent.
func makeRejectedEvent(petitionID string) *flowv1.PetitionOutcomeEvent {
	return &flowv1.PetitionOutcomeEvent{
		PetitionId: petitionID,
		Outcome:    flowv1.PetitionOutcome_PETITION_OUTCOME_REJECTED,
		Rejection: &flowv1.PublicationRejection{
			Reason:            flowv1.PublicationRejectionReason_PUBLICATION_REJECTION_REASON_CONFLICT,
			ConflictingLawIds: []string{"law-A", "law-B"},
			RemediationText:   "Conflicts with existing law",
		},
	}
}

// ---------------------------------------------------------------------------
// Spy: entry-client sidecar (Operator + Librarian for acceptance path)
// ---------------------------------------------------------------------------

// entryClientSpy captures RetireDisputeRecord, ResumeWorkitem,
// ListSuspendedWorkitems, and CreateWorkitem calls made via the EntryClient.
// It implements both OperatorServiceServer and LibrarianServiceServer so both
// services can be registered on a single gRPC server (mirroring the real sidecar).
type entryClientSpy struct {
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedLibrarianServiceServer

	mu                 sync.Mutex
	retiredPetitionIDs []string
	retireErr          error
	resumedWorkitemIDs []string
	resumeErr          error

	// ListSuspendedWorkitems tracking.
	listSuspendedResp []*flowv1.SuspendedWorkitemInfo // response workitems
	listSuspendedErr  error                           // error to return
	listSuspendedReqs []string                        // captured condition_contains values

	// CreateWorkitem tracking.
	createdWorkitems []map[string]string // metadata from each CreateWorkitem call
	createWIReturnID string              // workitem ID to return (default: "wi-test-001")
	createWIErr      error               // error to return from CreateWorkitem
}

func (s *entryClientSpy) RetireDisputeRecord(
	_ context.Context, req *flowv1.RetireDisputeRecordRequest,
) (*flowv1.RetireDisputeRecordResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retiredPetitionIDs = append(s.retiredPetitionIDs, req.GetPetitionId())
	if s.retireErr != nil {
		return nil, s.retireErr
	}
	return &flowv1.RetireDisputeRecordResponse{Acknowledged: true}, nil
}

func (s *entryClientSpy) ResumeWorkitem(
	_ context.Context, req *flowv1.ResumeWorkitemRequest,
) (*flowv1.ResumeWorkitemResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resumedWorkitemIDs = append(s.resumedWorkitemIDs, req.GetWorkitemId())
	if s.resumeErr != nil {
		return nil, s.resumeErr
	}
	return &flowv1.ResumeWorkitemResponse{Accepted: true}, nil
}

func (s *entryClientSpy) ListSuspendedWorkitems(
	_ context.Context, req *flowv1.ListSuspendedWorkitemsRequest,
) (*flowv1.ListSuspendedWorkitemsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listSuspendedReqs = append(s.listSuspendedReqs, req.GetConditionContains())
	if s.listSuspendedErr != nil {
		return nil, s.listSuspendedErr
	}
	return &flowv1.ListSuspendedWorkitemsResponse{
		Workitems: s.listSuspendedResp,
	}, nil
}

func (s *entryClientSpy) CreateWorkitem(
	_ context.Context, req *flowv1.CreateWorkitemRequest,
) (*flowv1.CreateWorkitemResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	md := make(map[string]string, len(req.GetMetadata()))
	maps.Copy(md, req.GetMetadata())
	s.createdWorkitems = append(s.createdWorkitems, md)
	if s.createWIErr != nil {
		return nil, s.createWIErr
	}
	id := s.createWIReturnID
	if id == "" {
		id = "wi-test-001"
	}
	return &flowv1.CreateWorkitemResponse{WorkitemId: id}, nil
}

func (s *entryClientSpy) getCreatedWorkitems() []map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]string, len(s.createdWorkitems))
	for i, m := range s.createdWorkitems {
		cp := make(map[string]string, len(m))
		maps.Copy(cp, m)
		out[i] = cp
	}
	return out
}

func (s *entryClientSpy) getRetiredPetitionIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.retiredPetitionIDs))
	copy(out, s.retiredPetitionIDs)
	return out
}

func (s *entryClientSpy) getResumedWorkitemIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.resumedWorkitemIDs))
	copy(out, s.resumedWorkitemIDs)
	return out
}

// newEntryTestClient creates a flow.EntryClient backed by a local gRPC
// server with the entryClientSpy providing Operator + Librarian services.
func newEntryTestClient(t *testing.T, spy *entryClientSpy) *flow.EntryClient {
	t.Helper()

	lis := newTestListener(t)
	srv := grpc.NewServer()
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterLibrarianServiceServer(srv, spy)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })

	ec, err := flow.NewEntryClientForTest(lis.Addr().String(), "")
	if err != nil {
		t.Fatalf("NewEntryClientForTest() failed: %v", err)
	}
	t.Cleanup(func() { _ = ec.Close() })
	return ec
}

// notFoundErr returns a gRPC NotFound status error.
func notFoundErr() error {
	return status.Error(codes.NotFound, "dispute record not found")
}
