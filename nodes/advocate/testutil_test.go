package main

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc"
)

// newLocalListener creates a TCP listener on an ephemeral localhost port.
func newLocalListener() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

// newSpyGRPCServer creates a gRPC server with the advocateSpy registered
// for the five Foundry Flow service interfaces the Advocate depends on.
func newSpyGRPCServer(spy *advocateSpy) *grpc.Server {
	srv := grpc.NewServer()
	flowv1.RegisterSidecarServiceServer(srv, spy)
	flowv1.RegisterOperatorServiceServer(srv, spy)
	flowv1.RegisterArchivistServiceServer(srv, spy)
	flowv1.RegisterLibrarianServiceServer(srv, spy)
	flowv1.RegisterFrictionLedgerServiceServer(srv, spy)
	return srv
}

// advocateSpy captures calls to service operations for test assertions.
type advocateSpy struct {
	flowv1.UnimplementedSidecarServiceServer
	flowv1.UnimplementedOperatorServiceServer
	flowv1.UnimplementedArchivistServiceServer
	flowv1.UnimplementedLibrarianServiceServer
	flowv1.UnimplementedFrictionLedgerServiceServer

	mu sync.Mutex

	// Configurable artefact content (for advocate-context).
	ArtefactContent []byte

	// Configurable errors.
	GetArtefactErr   error
	StoreArtefactErr error
	RouteToOutputErr error
	CompleteErr      error
	PauseTimerErr    error
	ResumeTimerErr   error

	// Recorded operations.
	RoutedOutputs      []string
	Completed          bool
	StoreArtefactCalls []storeArtefactRecord
	PauseTimerCalls    int
	ResumeTimerCalls   int
}

type storeArtefactRecord struct {
	ArtefactID string
	Content    []byte
}

func newAdvocateSpy(advCtx *advocateContext) *advocateSpy {
	content, _ := json.Marshal(advCtx)
	return &advocateSpy{
		ArtefactContent: content,
	}
}

// newSpyClient creates a flow.Client backed by the advocateSpy.
func newSpyClient(t *testing.T, spy *advocateSpy) *flow.Client {
	t.Helper()

	lis, err := newLocalListener()
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	srv := newSpyGRPCServer(spy)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() { srv.GracefulStop() })

	t.Setenv(flow.EnvWorkitemID, "test-workitem")
	client, err := flow.NewClient(
		flow.WithSidecarAddress(lis.Addr().String()),
	)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	return client
}

// newTestQueueManager creates an in-memory QueueManager for tests.
func newTestQueueManager(t *testing.T) flow.QueueManager {
	t.Helper()
	qm, err := flow.NewQueueManager(
		flow.WithShardID("test-shard"),
		flow.WithPeerResolver(&staticResolver{}),
	)
	if err != nil {
		t.Fatalf("NewQueueManager failed: %v", err)
	}
	if err := qm.Start(context.Background(), flow.WithStoragePath(":memory:"), flow.WithAPIPort("0")); err != nil {
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

// staticResolver returns no peers (standalone mode for testing).
type staticResolver struct{}

func (r *staticResolver) Resolve(_ context.Context) ([]string, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Sidecar methods
// ---------------------------------------------------------------------------

func (s *advocateSpy) Heartbeat(
	_ context.Context, _ *flowv1.HeartbeatRequest,
) (*flowv1.HeartbeatResponse, error) {
	return &flowv1.HeartbeatResponse{Acknowledged: true}, nil
}

func (s *advocateSpy) PauseTimer(
	_ context.Context, _ *flowv1.PauseTimerRequest,
) (*flowv1.PauseTimerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.PauseTimerErr != nil {
		return nil, s.PauseTimerErr
	}
	s.PauseTimerCalls++
	return &flowv1.PauseTimerResponse{Acknowledged: true}, nil
}

func (s *advocateSpy) ResumeTimer(
	_ context.Context, _ *flowv1.ResumeTimerRequest,
) (*flowv1.ResumeTimerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ResumeTimerErr != nil {
		return nil, s.ResumeTimerErr
	}
	s.ResumeTimerCalls++
	return &flowv1.ResumeTimerResponse{Acknowledged: true}, nil
}

func (s *advocateSpy) SubmitResult(
	_ context.Context, req *flowv1.SubmitResultRequest,
) (*flowv1.SubmitResultResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ri := req.GetRoutingInstruction()
	if ri == nil {
		return &flowv1.SubmitResultResponse{Accepted: true}, nil
	}

	isComplete := ri.GetType() == flowv1.RoutingType_ROUTING_TYPE_COMPLETE

	if isComplete {
		if s.CompleteErr != nil {
			return nil, s.CompleteErr
		}
		s.Completed = true
	} else {
		if s.RouteToOutputErr != nil {
			return nil, s.RouteToOutputErr
		}
		s.RoutedOutputs = append(s.RoutedOutputs, ri.GetTarget())
	}
	return &flowv1.SubmitResultResponse{Accepted: true}, nil
}

func (s *advocateSpy) RecordTelemetry(
	_ context.Context, _ *flowv1.RecordTelemetryRequest,
) (*flowv1.RecordTelemetryResponse, error) {
	return &flowv1.RecordTelemetryResponse{Acknowledged: true}, nil
}

// ---------------------------------------------------------------------------
// Archivist methods
// ---------------------------------------------------------------------------

func (s *advocateSpy) GetArtefact(
	_ context.Context, _ *flowv1.GetArtefactRequest,
) (*flowv1.GetArtefactResponse, error) {
	if s.GetArtefactErr != nil {
		return nil, s.GetArtefactErr
	}
	return &flowv1.GetArtefactResponse{
		Content: s.ArtefactContent,
	}, nil
}

func (s *advocateSpy) StoreArtefact(
	_ context.Context, req *flowv1.StoreArtefactRequest,
) (*flowv1.StoreArtefactResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.StoreArtefactErr != nil {
		return nil, s.StoreArtefactErr
	}
	s.StoreArtefactCalls = append(s.StoreArtefactCalls, storeArtefactRecord{
		ArtefactID: req.GetArtefactId(),
		Content:    req.GetContent(),
	})
	return &flowv1.StoreArtefactResponse{
		VersionHash:  "mock-hash",
		IsNewVersion: true,
	}, nil
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// getStoredArtefact parses a stored artefact from the spy by ID.
// Returns nil if not found.
func (s *advocateSpy) getStoredArtefact(id string) []byte { //nolint:unparam // generic helper
	for _, call := range s.StoreArtefactCalls {
		if call.ArtefactID == id {
			return call.Content
		}
	}
	return nil
}
