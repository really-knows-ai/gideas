package service

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// AssignWork Tests
// ---------------------------------------------------------------------------

func TestSidecarServer_AssignWork_MissingContext(t *testing.T) {
	srv := NewSidecarServer("test-ns", "test-node", "")

	_, err := srv.AssignWork(context.Background(), &flowv1.AssignWorkRequest{})
	if err == nil {
		t.Fatal("expected error for missing context")
	}
	if st, ok := status.FromError(err); !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestSidecarServer_AssignWork_ForwardsToNode(t *testing.T) {
	fake := &fakeNodeServer{returnOK: true}
	sidecar := newTestSidecar(t, fake)

	ack, err := sidecar.AssignWork(context.Background(), &flowv1.AssignWorkRequest{
		Context: testContext(),
	})
	if err != nil {
		t.Fatalf("AssignWork() error: %v", err)
	}
	if !ack.GetAccepted() {
		t.Fatal("expected accepted=true")
	}

	// Verify the fake node received the correct context.
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.lastReq == nil {
		t.Fatal("fake node server did not receive request")
	}
	if fake.lastReq.GetContext().GetWorkitemId() != "wi-1" {
		t.Fatalf("expected workitem_id=wi-1, got %s", fake.lastReq.GetContext().GetWorkitemId())
	}
}

func TestSidecarServer_AssignWork_NodeFailure(t *testing.T) {
	fake := &fakeNodeServer{
		returnErr: status.Error(codes.Internal, "boom"),
	}
	sidecar := newTestSidecar(t, fake)

	_, err := sidecar.AssignWork(context.Background(), &flowv1.AssignWorkRequest{
		Context: testContext(),
	})
	if err == nil {
		t.Fatal("expected error when node fails")
	}
}

func TestSidecarServer_AssignWork_UnreachableNode(t *testing.T) {
	// Point at an address where nothing is listening.
	sidecar := NewSidecarServer("test-ns", "test-node", "127.0.0.1:1")

	// Force connection with a real address that's refused.
	conn, err := grpc.NewClient(
		"127.0.0.1:1",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	sidecar.nodeConn = conn
	sidecar.nodeClient = flowv1.NewNodeServiceClient(conn)
	t.Cleanup(func() { _ = sidecar.Close() })

	_, err = sidecar.AssignWork(context.Background(), &flowv1.AssignWorkRequest{
		Context: &flowv1.WorkitemContext{
			FlowNamespace: "flow-1",
			WorkitemId:    "wi-unreachable",
			NodeId:        "node-1",
		},
	})
	if err == nil {
		t.Fatal("expected error when node is unreachable")
	}
}

func TestSidecarServer_AssignWork_SessionCleanup(t *testing.T) {
	fake := &fakeNodeServer{returnOK: true}
	sidecar := newTestSidecar(t, fake)

	_, err := sidecar.AssignWork(context.Background(), &flowv1.AssignWorkRequest{
		Context: testContext(),
	})
	if err != nil {
		t.Fatalf("AssignWork() error: %v", err)
	}

	// Session should be cleaned up after handler completes.
	sidecar.mu.Lock()
	count := len(sidecar.sessions)
	sidecar.mu.Unlock()
	if count != 0 {
		t.Fatalf("expected 0 sessions after completion, got %d", count)
	}
}

func TestSidecarServer_AssignWork_DuplicateRejected(t *testing.T) {
	fake := &fakeNodeServer{returnOK: true, blockCh: make(chan struct{})}
	sidecar := newTestSidecar(t, fake)
	sidecar.Timeout = 5 * time.Second

	// Start the first assignment (blocked inside the fake node's Process).
	done := make(chan error, 1)
	go func() {
		_, err := sidecar.AssignWork(context.Background(), &flowv1.AssignWorkRequest{
			Context: testContext(),
		})
		done <- err
	}()

	waitForSession(t, sidecar)

	// A second assignment for the same workitem must be rejected while the
	// first handler is still live, not silently overwrite its session.
	_, err := sidecar.AssignWork(context.Background(), &flowv1.AssignWorkRequest{
		Context: testContext(),
	})
	if err == nil {
		t.Fatal("expected duplicate assignment to be rejected")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.AlreadyExists {
		t.Fatalf("expected AlreadyExists, got %v", err)
	}

	// The first handler's session must still be live.
	if s := sidecar.getSession("wi-1"); s == nil {
		t.Fatal("first assignment's session was destroyed by the rejected duplicate")
	}

	// Complete the first handler.
	close(fake.blockCh)
	if err := <-done; err != nil {
		t.Fatalf("first AssignWork() error: %v", err)
	}

	// Cleanup must still remove the first handler's session.
	if s := sidecar.getSession("wi-1"); s != nil {
		t.Fatal("session not cleaned up after first assignment completed")
	}
}

func TestSidecarServer_AssignWork_CleanupKeepsReplacedSession(t *testing.T) {
	fake := &fakeNodeServer{returnOK: true, blockCh: make(chan struct{})}
	sidecar := newTestSidecar(t, fake)
	sidecar.Timeout = 5 * time.Second

	done := make(chan error, 1)
	go func() {
		_, err := sidecar.AssignWork(context.Background(), &flowv1.AssignWorkRequest{
			Context: testContext(),
		})
		done <- err
	}()

	waitForSession(t, sidecar)
	first := sidecar.getSession("wi-1")

	// A newer session for the same workitem replaces the first handler's
	// entry (via a direct session injection that bypasses AssignWork's
	// conflict check).
	injected, _ := newSession(context.Background(), "wi-1", "node-2", sidecar.timeout())
	injected.stop() // Prevent timer goroutine from leaking.
	sidecar.mu.Lock()
	sidecar.sessions["wi-1"] = injected
	sidecar.mu.Unlock()
	second := sidecar.getSession("wi-1")
	if first == second {
		t.Fatal("test setup broken: expected the injected session to replace the first")
	}

	// Complete the first handler. Its cleanup must not destroy the newer
	// session that replaced its entry.
	close(fake.blockCh)
	if err := <-done; err != nil {
		t.Fatalf("first AssignWork() error: %v", err)
	}

	if s := sidecar.getSession("wi-1"); s != second {
		t.Fatal("first handler's cleanup deleted the session that replaced it")
	}
}

func TestSidecarServer_ConcurrentAssignWorkAndClose(t *testing.T) {
	fake := &fakeNodeServer{returnOK: true}
	sidecar := newTestSidecar(t, fake)
	sidecar.Timeout = 5 * time.Second

	// Run several AssignWork RPCs concurrently (as gRPC does per-request) so
	// they share the lazily-initialised node connection, then Close the
	// server. The lazy-init and close must be synchronised — without the
	// guard this races on nodeConn/nodeClient and leaks every connection but
	// the last.
	var wg sync.WaitGroup
	for i := range 5 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx := &flowv1.WorkitemContext{
				FlowNamespace: "flow-1",
				WorkitemId:    fmt.Sprintf("wi-%d", i),
				NodeId:        "node-1",
			}
			if _, err := sidecar.AssignWork(context.Background(), &flowv1.AssignWorkRequest{Context: ctx}); err != nil {
				t.Errorf("AssignWork(%d) error: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	// All sessions must be cleaned up after completion.
	sidecar.mu.Lock()
	count := len(sidecar.sessions)
	sidecar.mu.Unlock()
	if count != 0 {
		t.Fatalf("expected 0 sessions after concurrent assignments, got %d", count)
	}

	// Close must release the single shared connection without error.
	if err := sidecar.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
}
