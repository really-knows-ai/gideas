// Test utilities for human-approval node tests.
package main

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	flow "github.com/foundry/flow/sdk/go"
	"google.golang.org/grpc"
)

// newSpyGRPCServer creates a gRPC server with the approvalSpy registered
// for all five Foundry Flow service interfaces.
func newSpyGRPCServer(spy *approvalSpy) *grpc.Server {
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	flowv1.RegisterLibrarianServiceServer(srv, spy)
	flowv1.RegisterFrictionLedgerServiceServer(srv, spy)
	return srv
}

// approvalSpy captures calls to service operations for test assertions.
type approvalSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFrictionLedgerServiceServer

	mu sync.Mutex

	// Operation records.
	ReadArtefacts    []string                  // artefact IDs read via GetArtefact
	StampedArtefacts []stampRecord             // stamps applied
	RoutedOutputs    []string                  // output names routed to
	CompletedReasons []flowv1.CompletionReason // reasons from Complete actions
	PauseTimerCalls  int                       // PauseTimer calls
	ResumeTimerCalls int                       // ResumeTimer calls

	// Configurable responses.
	ArtefactContents map[string]string // artefact ID -> content
}

type stampRecord struct {
	ArtefactID string
	StampName  string
}

// newApprovalSpy returns a spy with haiku and petition artefact contents.
func newApprovalSpy() *approvalSpy {
	return &approvalSpy{
		ArtefactContents: map[string]string{
			"haiku":    "test-haiku-content",
			"petition": "test-petition-content",
		},
	}
}

// ---------------------------------------------------------------------------
// Sidecar methods
// ---------------------------------------------------------------------------

func (s *approvalSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *approvalSpy) PauseTimer(
	_ context.Context, _ *flowv1.PauseTimerRequest,
) (*flowv1.PauseTimerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.PauseTimerCalls++
	return &flowv1.PauseTimerResponse{Acknowledged: true}, nil
}

func (s *approvalSpy) ResumeTimer(
	_ context.Context, _ *flowv1.ResumeTimerRequest,
) (*flowv1.ResumeTimerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ResumeTimerCalls++
	return &flowv1.ResumeTimerResponse{Acknowledged: true}, nil
}

func (s *approvalSpy) SubmitResult(
	_ context.Context, req *flowv1.SubmitResultRequest,
) (*flowv1.SubmitResultResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch a := req.GetAction().(type) {
	case *flowv1.SubmitResultRequest_Route:
		if a.Route != nil {
			s.RoutedOutputs = append(s.RoutedOutputs, a.Route.GetTarget())
		}
	case *flowv1.SubmitResultRequest_Complete:
		reason := flowv1.CompletionReason_COMPLETION_REASON_UNSPECIFIED
		if a.Complete != nil {
			reason = a.Complete.GetReason()
		}
		s.CompletedReasons = append(s.CompletedReasons, reason)
	default:
		// Suspend / nil — nothing to record.
	}
	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

// ---------------------------------------------------------------------------
// Archivist methods
// ---------------------------------------------------------------------------

func (s *approvalSpy) GetArtefact(
	_ context.Context, req *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ReadArtefacts = append(s.ReadArtefacts, req.GetArtefactId())

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

func (s *approvalSpy) StampArtefact(
	_ context.Context, req *flowv1.StampArtefactRequest,
) (*flowv1.StampArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.StampedArtefacts = append(s.StampedArtefacts, stampRecord{
		ArtefactID: req.GetArtefactId(),
		StampName:  req.GetStampName(),
	})
	return &flowv1.StampArtefactResponse{Stamp: &flowv1.Stamp{Name: req.GetStampName()}}, nil
}

// ---------------------------------------------------------------------------
// FrictionLedger methods
// ---------------------------------------------------------------------------

func (s *approvalSpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newSpyClient creates a flow.Client backed by a local gRPC server with
// the approvalSpy registered for all five service interfaces.
func newSpyClient(t *testing.T, spy *approvalSpy) *flow.Client {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
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

// newTestQueueManager creates an in-memory QueueManager for tests.
func newTestQueueManager(t *testing.T) flow.QueueManager {
	t.Helper()
	// The storage-path and API-port knobs are configured via environment
	// variables (the SDK option constructors were deleted as test-only).
	t.Setenv("FLOW_STORAGE_PATH", ":memory:")
	t.Setenv("FLOW_HITL_PORT", "0")
	qm, err := flow.NewQueueManager()
	if err != nil {
		t.Fatalf("NewQueueManager failed: %v", err)
	}
	if err := qm.Start(context.Background()); err != nil {
		t.Fatalf("QueueManager.Start failed: %v", err)
	}
	t.Cleanup(func() { _ = qm.Stop() })
	return qm
}

// waitForEnqueue polls until the given workitem appears in the queue.
func waitForEnqueue(t *testing.T, qm flow.QueueManager, workitemID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := qm.GetItem(context.Background(), workitemID); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s to appear in queue", workitemID)
}

// newWorkitemContext creates a WorkitemContext for testing.
func newWorkitemContext(workitemID string) *flowv1.WorkitemContext {
	return &flowv1.WorkitemContext{
		WorkitemId:    workitemID,
		FlowNamespace: "test-flow",
		NodeId:        "human-approval",
	}
}

// runHandler starts handleApproval in a goroutine and returns an error channel.
// The caller should simulate the human decision then read from errCh.
func runHandler(
	ctx context.Context,
	client *flow.Client,
	qm flow.QueueManager,
	wctx *flowv1.WorkitemContext,
) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		wi, wiErr := client.GetWorkitem(wctx.GetWorkitemId())
		if wiErr != nil {
			errCh <- fmt.Errorf("runHandler: get workitem: %w", wiErr)
			return
		}
		errCh <- handleApproval(ctx, wi, qm, wctx)
	}()
	return errCh
}

// simulateDecision waits for the workitem to appear, claims it, and decides.
func simulateDecision(t *testing.T, ctx context.Context, qm flow.QueueManager, workitemID, choice string) {
	t.Helper()
	waitForEnqueue(t, qm, workitemID)

	if _, err := qm.Claim(ctx, workitemID); err != nil {
		t.Fatalf("Claim failed: %v", err)
	}
	if err := qm.Decide(ctx, workitemID, choice); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}
}
