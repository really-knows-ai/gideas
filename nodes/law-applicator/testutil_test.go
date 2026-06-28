package main

import (
	"context"
	"sync"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/nodeutil"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// testWorkitemID is the workitem ID used across all law-applicator tests.
const testWorkitemID = "test-workitem"

// newSpyGRPCServer creates a gRPC server with the applicatorSpy registered
// for the Foundry Flow service interfaces the law-applicator depends on.
func newSpyGRPCServer(spy *applicatorSpy) *grpc.Server {
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	flowv1.RegisterLibrarianServiceServer(srv, spy)
	flowv1.RegisterFrictionLedgerServiceServer(srv, spy)
	return srv
}

// ---------------------------------------------------------------------------
// applicatorSpy — configurable inputs, recorded outputs
// ---------------------------------------------------------------------------

// disputeRecordCall records arguments to a CreateDisputeRecord invocation.
type disputeRecordCall struct {
	PetitionID  string
	CitedLawIDs []string
}

// applicatorSpy captures calls to service operations for test assertions.
// The law-applicator is a simple action node: read petition artefact, apply
// each change via Librarian (WriteLaw/RetireLaw/GetLaw+WriteLaw for demote),
// store an approval-stamp artefact, and Complete().
type applicatorSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFrictionLedgerServiceServer

	mu sync.Mutex

	// ── Configurable inputs ─────────────────────────────────────────

	// Artefacts maps artefact IDs to their content for GetArtefact calls.
	Artefacts map[string][]byte

	// LawsByID maps law IDs to their Law objects for GetLaw calls.
	LawsByID map[string]*flowv1.Law

	// WriteLawResponses are returned in sequence by WriteLaw. When
	// exhausted, an auto-generated response is returned.
	WriteLawResponses []*flowv1.WriteLawResponse
	writeLawCallCount int

	// ── Configurable error returns ──────────────────────────────────

	GetArtefactErr   error
	StoreArtefactErr error
	GetLawErr        error
	WriteLawErr      error
	RetireLawErr     error
	CompleteErr      error

	// ── Configurable error returns (dispute) ───────────────────────

	CreateDisputeRecordErr error

	// ── Recorded operations for assertions ──────────────────────────

	// StoredArtefacts records artefact store calls: artefactID -> content.
	StoredArtefacts map[string][]byte

	// Completed is true if a CompleteAction was received.
	Completed bool

	// RoutedTo records the output name from RouteToOutput calls.
	RoutedTo string

	// WrittenLaws records laws passed to WriteLaw.
	WrittenLaws []*flowv1.Law

	// RetiredLawIDs records law IDs passed to RetireLaw.
	RetiredLawIDs []string

	// RequestedLawIDs records law IDs passed to GetLaw.
	RequestedLawIDs []string

	// HeartbeatCount records the number of Heartbeat calls.
	HeartbeatCount int

	// DisputeRecordCalls records CreateDisputeRecord invocations.
	DisputeRecordCalls []disputeRecordCall
}

func newApplicatorSpy() *applicatorSpy {
	return &applicatorSpy{
		Artefacts:       make(map[string][]byte),
		LawsByID:        make(map[string]*flowv1.Law),
		StoredArtefacts: make(map[string][]byte),
	}
}

// setupApplicatorTest creates a flow.Client backed by the spy, suitable for
// calling handleLawApplicator directly.
func setupApplicatorTest(t *testing.T, spy *applicatorSpy) *flow.Client {
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
// Sidecar methods
// ---------------------------------------------------------------------------

func (s *applicatorSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.HeartbeatCount++
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *applicatorSpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

func (s *applicatorSpy) SubmitResult(
	_ context.Context, req *flowv1.SubmitResultRequest,
) (*flowv1.SubmitResultResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch a := req.GetAction().(type) {
	case *flowv1.SubmitResultRequest_Complete:
		if s.CompleteErr != nil {
			return nil, s.CompleteErr
		}
		_ = a
		s.Completed = true
	case nil:
		// No action set — treat as complete.
		if s.CompleteErr != nil {
			return nil, s.CompleteErr
		}
		s.Completed = true
	case *flowv1.SubmitResultRequest_Route:
		s.RoutedTo = a.Route.GetTarget()
	default:
		// Suspend — law-applicator should never call this.
	}
	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

// ---------------------------------------------------------------------------
// Archivist methods
// ---------------------------------------------------------------------------

func (s *applicatorSpy) GetArtefact(
	_ context.Context, req *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	if s.GetArtefactErr != nil {
		return nil, s.GetArtefactErr
	}

	content, ok := s.Artefacts[req.GetArtefactId()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "artefact %q not found", req.GetArtefactId())
	}
	return &flowv1.GetArtefactResponse{Content: content}, nil
}

func (s *applicatorSpy) StoreArtefact(
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
// Librarian methods
// ---------------------------------------------------------------------------

func (s *applicatorSpy) GetLaw(
	_ context.Context, req *flowv1.GetLawRequest,
) (*flowv1.GetLawResponse, error) {
	s.mu.Lock()
	s.RequestedLawIDs = append(s.RequestedLawIDs, req.GetLawId())
	s.mu.Unlock()

	if s.GetLawErr != nil {
		return nil, s.GetLawErr
	}

	law, ok := s.LawsByID[req.GetLawId()]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "law %q not found", req.GetLawId())
	}
	return &flowv1.GetLawResponse{Law: law}, nil
}

func (s *applicatorSpy) WriteLaw(
	_ context.Context, req *flowv1.WriteLawRequest,
) (*flowv1.WriteLawResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.WriteLawErr != nil {
		return nil, s.WriteLawErr
	}

	s.WrittenLaws = append(s.WrittenLaws, req.GetLaw())

	// Return preconfigured response or auto-generate one.
	if s.writeLawCallCount < len(s.WriteLawResponses) {
		resp := s.WriteLawResponses[s.writeLawCallCount]
		s.writeLawCallCount++
		return resp, nil
	}

	s.writeLawCallCount++
	return &flowv1.WriteLawResponse{
		LawId:       "new-law-id",
		VersionHash: "v1",
	}, nil
}

func (s *applicatorSpy) RetireLaw(
	_ context.Context, req *flowv1.RetireLawRequest,
) (*flowv1.RetireLawResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.RetireLawErr != nil {
		return nil, s.RetireLawErr
	}

	s.RetiredLawIDs = append(s.RetiredLawIDs, req.GetLawId())
	return &flowv1.RetireLawResponse{Acknowledged: true}, nil
}

func (s *applicatorSpy) CreateDisputeRecord(
	_ context.Context, req *flowv1.CreateDisputeRecordRequest,
) (*flowv1.CreateDisputeRecordResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.DisputeRecordCalls = append(s.DisputeRecordCalls, disputeRecordCall{
		PetitionID:  req.GetPetitionId(),
		CitedLawIDs: req.GetCitedLawIds(),
	})

	if s.CreateDisputeRecordErr != nil {
		return nil, s.CreateDisputeRecordErr
	}

	return &flowv1.CreateDisputeRecordResponse{}, nil
}

// ---------------------------------------------------------------------------
// Test assertion helpers
// ---------------------------------------------------------------------------

// assertCompleted verifies the spy recorded a Complete() call.
func assertCompleted(t *testing.T, spy *applicatorSpy) {
	t.Helper()
	if !spy.Completed {
		t.Fatal("expected Complete() to be called")
	}
}

// assertNotCompleted verifies the spy did NOT record a Complete() call.
func assertNotCompleted(t *testing.T, spy *applicatorSpy) {
	t.Helper()
	if spy.Completed {
		t.Fatal("expected Complete() NOT to be called")
	}
}

// assertStampStored verifies the spy recorded an approval-stamp artefact store.
// Returns the raw content for further inspection.
func assertStampStored(t *testing.T, spy *applicatorSpy) []byte {
	t.Helper()
	content, ok := spy.StoredArtefacts["approval-stamp"]
	if !ok {
		t.Fatal("expected approval-stamp artefact to be stored")
	}
	return content
}

// assertNoStampStored verifies no approval-stamp artefact was stored.
func assertNoStampStored(t *testing.T, spy *applicatorSpy) {
	t.Helper()
	if _, ok := spy.StoredArtefacts["approval-stamp"]; ok {
		t.Fatal("expected no approval-stamp artefact to be stored")
	}
}

// assertRoutedTo verifies the spy recorded a Route to the given output.
//
//nolint:unparam // target is currently always "embassy" but the helper is general-purpose.
func assertRoutedTo(t *testing.T, spy *applicatorSpy, target string) {
	t.Helper()
	if spy.RoutedTo != target {
		t.Fatalf("expected RouteToOutput(%q), got %q", target, spy.RoutedTo)
	}
}

// assertNotRouted verifies the spy did NOT record any Route call.
func assertNotRouted(t *testing.T, spy *applicatorSpy) {
	t.Helper()
	if spy.RoutedTo != "" {
		t.Fatalf("expected no RouteToOutput call, got %q", spy.RoutedTo)
	}
}
