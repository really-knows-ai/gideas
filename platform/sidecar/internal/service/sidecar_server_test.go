package service

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Fake NodeService server for testing
// ---------------------------------------------------------------------------

type fakeNodeServer struct {
	flowv1.UnimplementedNodeServiceServer
	mu        sync.Mutex
	lastReq   *flowv1.AssignWorkRequest
	returnOK  bool
	returnErr error

	// blockCh blocks Process until closed, allowing timer tests.
	blockCh chan struct{}
}

func (f *fakeNodeServer) Process(ctx context.Context, req *flowv1.AssignWorkRequest) (*flowv1.Ack, error) {
	f.mu.Lock()
	f.lastReq = req
	returnErr := f.returnErr
	returnOK := f.returnOK
	blockCh := f.blockCh
	f.mu.Unlock()

	if returnErr != nil {
		return nil, returnErr
	}

	// If blockCh is set, block until it's closed or context is done.
	if blockCh != nil {
		select {
		case <-blockCh:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	return &flowv1.Ack{Accepted: returnOK, Message: "fake"}, nil
}

// ---------------------------------------------------------------------------
// Helper: start a fake node server and return a configured SidecarServer
// ---------------------------------------------------------------------------

func newTestSidecar(t *testing.T, fake *fakeNodeServer) *SidecarServer {
	t.Helper()
	nodeSrv := grpc.NewServer()
	flowv1.RegisterNodeServiceServer(nodeSrv, fake)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	go func() { _ = nodeSrv.Serve(lis) }()
	t.Cleanup(func() { nodeSrv.GracefulStop() })

	sidecar := NewSidecarServer("test-ns", "test-node", lis.Addr().String())
	t.Cleanup(func() { _ = sidecar.Close() })
	return sidecar
}

func testContext() *flowv1.WorkitemContext {
	return &flowv1.WorkitemContext{
		FlowNamespace: "flow-1",
		WorkitemId:    "wi-1",
		NodeId:        "node-1",
	}
}

// ---------------------------------------------------------------------------
// Heartbeat Tests
// ---------------------------------------------------------------------------

func TestSidecarServer_Heartbeat(t *testing.T) {
	srv := NewSidecarServer("test-ns", "test-node", "")

	resp, err := srv.Heartbeat(context.Background(), &flowv1.HeartbeatRequest{
		WorkitemId: "wi-1",
	})
	if err != nil {
		t.Fatalf("Heartbeat() error: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}
}

func TestSidecarServer_Heartbeat_ResetsTimer(t *testing.T) {
	fake := &fakeNodeServer{returnOK: true, blockCh: make(chan struct{})}
	sidecar := newTestSidecar(t, fake)
	sidecar.Timeout = 200 * time.Millisecond

	// Start a long-running assignment.
	done := make(chan error, 1)
	go func() {
		_, err := sidecar.AssignWork(context.Background(), &flowv1.AssignWorkRequest{
			Context: testContext(),
		})
		done <- err
	}()

	// Wait for the session to be created.
	waitForSession(t, sidecar)

	// Send heartbeats every 100ms to keep the 200ms timer alive.
	for i := range 5 {
		time.Sleep(100 * time.Millisecond)
		resp, err := sidecar.Heartbeat(context.Background(), &flowv1.HeartbeatRequest{
			WorkitemId: "wi-1",
		})
		if err != nil {
			t.Fatalf("Heartbeat() error on iteration %d: %v", i, err)
		}
		if !resp.GetAcknowledged() {
			t.Fatalf("Heartbeat() not acknowledged on iteration %d", i)
		}
	}

	// Complete the handler.
	close(fake.blockCh)

	if err := <-done; err != nil {
		t.Fatalf("AssignWork() should succeed with heartbeats: %v", err)
	}
}

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

// ---------------------------------------------------------------------------
// Inactivity Timeout Tests
// ---------------------------------------------------------------------------

func TestSidecarServer_InactivityTimeout(t *testing.T) {
	fake := &fakeNodeServer{returnOK: true, blockCh: make(chan struct{})}
	sidecar := newTestSidecar(t, fake)
	sidecar.Timeout = 100 * time.Millisecond

	_, err := sidecar.AssignWork(context.Background(), &flowv1.AssignWorkRequest{
		Context: testContext(),
	})
	if err == nil {
		t.Fatal("expected error from inactivity timeout")
	}

	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.DeadlineExceeded {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// PauseTimer Tests
// ---------------------------------------------------------------------------

func TestSidecarServer_PauseTimer_NoSession(t *testing.T) {
	srv := NewSidecarServer("test-ns", "test-node", "")

	_, err := srv.PauseTimer(context.Background(), &flowv1.PauseTimerRequest{
		WorkitemId: "nonexistent",
	})
	if err == nil {
		t.Fatal("expected error for no active session")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

func TestSidecarServer_PauseTimer_Success(t *testing.T) {
	fake := &fakeNodeServer{returnOK: true, blockCh: make(chan struct{})}
	sidecar := newTestSidecar(t, fake)
	sidecar.Timeout = 5 * time.Second

	// Start assignment in background.
	done := make(chan error, 1)
	go func() {
		_, err := sidecar.AssignWork(context.Background(), &flowv1.AssignWorkRequest{
			Context: testContext(),
		})
		done <- err
	}()

	waitForSession(t, sidecar)

	resp, err := sidecar.PauseTimer(context.Background(), &flowv1.PauseTimerRequest{
		WorkitemId: "wi-1",
	})
	if err != nil {
		t.Fatalf("PauseTimer() error: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}

	// Verify the session is paused.
	sess := sidecar.getSession("wi-1")
	if sess == nil {
		t.Fatal("session should exist")
	}
	if !sess.isPaused() {
		t.Fatal("session should be paused")
	}

	close(fake.blockCh)
	<-done
}

func TestSidecarServer_PauseTimer_AlreadyPaused(t *testing.T) {
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

	// First pause succeeds.
	_, err := sidecar.PauseTimer(context.Background(), &flowv1.PauseTimerRequest{
		WorkitemId: "wi-1",
	})
	if err != nil {
		t.Fatalf("first PauseTimer() error: %v", err)
	}

	// Second pause fails.
	_, err = sidecar.PauseTimer(context.Background(), &flowv1.PauseTimerRequest{
		WorkitemId: "wi-1",
	})
	if err == nil {
		t.Fatal("expected error on double pause")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}

	close(fake.blockCh)
	<-done
}

func TestSidecarServer_PauseTimer_PreventsTimeout(t *testing.T) {
	fake := &fakeNodeServer{returnOK: true, blockCh: make(chan struct{})}
	sidecar := newTestSidecar(t, fake)
	sidecar.Timeout = 150 * time.Millisecond

	done := make(chan error, 1)
	go func() {
		_, err := sidecar.AssignWork(context.Background(), &flowv1.AssignWorkRequest{
			Context: testContext(),
		})
		done <- err
	}()

	waitForSession(t, sidecar)

	// Pause the timer.
	_, err := sidecar.PauseTimer(context.Background(), &flowv1.PauseTimerRequest{
		WorkitemId: "wi-1",
	})
	if err != nil {
		t.Fatalf("PauseTimer() error: %v", err)
	}

	// Wait longer than the timeout. Should NOT time out because paused.
	time.Sleep(300 * time.Millisecond)

	// Resume the timer.
	_, err = sidecar.ResumeTimer(context.Background(), &flowv1.ResumeTimerRequest{
		WorkitemId: "wi-1",
	})
	if err != nil {
		t.Fatalf("ResumeTimer() error: %v", err)
	}

	// Complete the handler before the new timeout window expires.
	close(fake.blockCh)

	if err := <-done; err != nil {
		t.Fatalf("expected no timeout error after pause/resume, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ResumeTimer Tests
// ---------------------------------------------------------------------------

func TestSidecarServer_ResumeTimer_NoSession(t *testing.T) {
	srv := NewSidecarServer("test-ns", "test-node", "")

	_, err := srv.ResumeTimer(context.Background(), &flowv1.ResumeTimerRequest{
		WorkitemId: "nonexistent",
	})
	if err == nil {
		t.Fatal("expected error for no active session")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

func TestSidecarServer_ResumeTimer_NotPaused(t *testing.T) {
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

	_, err := sidecar.ResumeTimer(context.Background(), &flowv1.ResumeTimerRequest{
		WorkitemId: "wi-1",
	})
	if err == nil {
		t.Fatal("expected error when timer is not paused")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}

	close(fake.blockCh)
	<-done
}

func TestSidecarServer_ResumeTimer_ResetsToFullWindow(t *testing.T) {
	fake := &fakeNodeServer{returnOK: true, blockCh: make(chan struct{})}
	sidecar := newTestSidecar(t, fake)
	sidecar.Timeout = 200 * time.Millisecond

	done := make(chan error, 1)
	go func() {
		_, err := sidecar.AssignWork(context.Background(), &flowv1.AssignWorkRequest{
			Context: testContext(),
		})
		done <- err
	}()

	waitForSession(t, sidecar)

	// Wait ~100ms (half the timeout) to consume some of the window.
	time.Sleep(100 * time.Millisecond)

	// Pause and immediately resume. Timer should reset to full 200ms.
	_, err := sidecar.PauseTimer(context.Background(), &flowv1.PauseTimerRequest{
		WorkitemId: "wi-1",
	})
	if err != nil {
		t.Fatalf("PauseTimer() error: %v", err)
	}
	_, err = sidecar.ResumeTimer(context.Background(), &flowv1.ResumeTimerRequest{
		WorkitemId: "wi-1",
	})
	if err != nil {
		t.Fatalf("ResumeTimer() error: %v", err)
	}

	// Wait another 150ms. Without reset, total would be ~250ms > 200ms
	// and would timeout. With reset, we have 200ms from resume.
	time.Sleep(150 * time.Millisecond)

	// Complete before the new full window expires.
	close(fake.blockCh)

	if err := <-done; err != nil {
		t.Fatalf("expected success after timer reset, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Session Unit Tests
// ---------------------------------------------------------------------------

func TestSession_PauseResumeCycle(t *testing.T) {
	sess, _ := newSession(context.Background(), "w", "n", time.Second)
	defer sess.stop()

	if sess.isPaused() {
		t.Fatal("new session should not be paused")
	}

	if !sess.pause() {
		t.Fatal("first pause should succeed")
	}
	if !sess.isPaused() {
		t.Fatal("should be paused after pause()")
	}
	if sess.pause() {
		t.Fatal("second pause should fail")
	}

	if !sess.resume() {
		t.Fatal("resume should succeed after pause")
	}
	if sess.isPaused() {
		t.Fatal("should not be paused after resume")
	}
	if sess.resume() {
		t.Fatal("resume without pause should fail")
	}
}

func TestSession_TimeoutCancelsContext(t *testing.T) {
	sess, ctx := newSession(context.Background(), "w", "n", 50*time.Millisecond)
	defer sess.stop()

	select {
	case <-ctx.Done():
		// expected
	case <-time.After(time.Second):
		t.Fatal("context should have been cancelled by timeout")
	}

	if !sess.isTimedOut() {
		t.Fatal("session should be timed out")
	}
}

func TestSession_PausePreventsTimeout(t *testing.T) {
	sess, ctx := newSession(context.Background(), "w", "n", 100*time.Millisecond)
	defer sess.stop()

	if !sess.pause() {
		t.Fatal("pause should succeed")
	}

	// Wait longer than timeout.
	time.Sleep(200 * time.Millisecond)

	// Context should NOT be cancelled.
	select {
	case <-ctx.Done():
		t.Fatal("context should not be cancelled while paused")
	default:
		// expected
	}

	if sess.isTimedOut() {
		t.Fatal("session should not be timed out while paused")
	}
}

func TestSession_ResetTimerWhilePaused(t *testing.T) {
	sess, _ := newSession(context.Background(), "w", "n", time.Second)
	defer sess.stop()

	sess.pause()
	// resetTimer should be a no-op while paused.
	sess.resetTimer()
	if !sess.isPaused() {
		t.Fatal("should still be paused after resetTimer")
	}
}

// ---------------------------------------------------------------------------
// Session Child Tracking Unit Tests
// ---------------------------------------------------------------------------

func TestSession_AddChild_HasChild(t *testing.T) {
	sess, _ := newSession(context.Background(), "w", "n", time.Second)
	defer sess.stop()

	if sess.hasChild("child-1") {
		t.Fatal("new session should not have any children")
	}

	sess.addChild("child-1")
	sess.addChild("child-2")

	if !sess.hasChild("child-1") {
		t.Fatal("expected child-1 to be tracked")
	}
	if !sess.hasChild("child-2") {
		t.Fatal("expected child-2 to be tracked")
	}
	if sess.hasChild("child-3") {
		t.Fatal("child-3 was not added")
	}
}

func TestSession_AddChild_Idempotent(t *testing.T) {
	sess, _ := newSession(context.Background(), "w", "n", time.Second)
	defer sess.stop()

	sess.addChild("child-1")
	sess.addChild("child-1") // duplicate add

	if !sess.hasChild("child-1") {
		t.Fatal("expected child-1 to be tracked")
	}
}

// ---------------------------------------------------------------------------
// SidecarServer Child Tracker / Authorizer Tests
// ---------------------------------------------------------------------------

func TestSidecarServer_TrackChild(t *testing.T) {
	srv := NewSidecarServer("test-ns", "node-1", "")

	// Create a session.
	sess, _ := newSession(context.Background(), "wi-1", "node-1", DefaultTimeout)
	defer sess.stop()
	srv.mu.Lock()
	srv.sessions["wi-1"] = sess
	srv.mu.Unlock()

	srv.TrackChild("wi-1", "child-1")
	srv.TrackChild("wi-1", "child-2")

	if !sess.hasChild("child-1") {
		t.Fatal("expected child-1 to be tracked in session")
	}
	if !sess.hasChild("child-2") {
		t.Fatal("expected child-2 to be tracked in session")
	}
}

func TestSidecarServer_TrackChild_NoSession(t *testing.T) {
	srv := NewSidecarServer("test-ns", "node-1", "")

	// Should not panic when no session exists.
	srv.TrackChild("nonexistent", "child-1")
}

func TestSidecarServer_AuthorizeChildAccess_Allowed(t *testing.T) {
	srv := NewSidecarServer("test-ns", "node-1", "")

	sess, _ := newSession(context.Background(), "wi-1", "node-1", DefaultTimeout)
	defer sess.stop()
	srv.mu.Lock()
	srv.sessions["wi-1"] = sess
	srv.mu.Unlock()

	srv.TrackChild("wi-1", "child-1")

	decision := srv.AuthorizeChildAccess("wi-1", "child-1")
	if decision != ChildAccessAllowed {
		t.Fatalf("expected ChildAccessAllowed, got %d", decision)
	}
}

func TestSidecarServer_AuthorizeChildAccess_Denied(t *testing.T) {
	srv := NewSidecarServer("test-ns", "node-1", "")

	sess, _ := newSession(context.Background(), "wi-1", "node-1", DefaultTimeout)
	defer sess.stop()
	srv.mu.Lock()
	srv.sessions["wi-1"] = sess
	srv.mu.Unlock()

	// Session has children but the target is not one of them.
	srv.TrackChild("wi-1", "child-1")

	decision := srv.AuthorizeChildAccess("wi-1", "child-unknown")
	if decision != ChildAccessDenied {
		t.Fatalf("expected ChildAccessDenied, got %d", decision)
	}
}

func TestSidecarServer_AuthorizeChildAccess_Unknown_NoSession(t *testing.T) {
	srv := NewSidecarServer("test-ns", "node-1", "")

	decision := srv.AuthorizeChildAccess("nonexistent", "child-1")
	if decision != ChildAccessUnknown {
		t.Fatalf("expected ChildAccessUnknown, got %d", decision)
	}
}

func TestSidecarServer_AuthorizeChildAccess_Unknown_NoChildren(t *testing.T) {
	srv := NewSidecarServer("test-ns", "node-1", "")

	// Session exists but has no children (collection phase).
	sess, _ := newSession(context.Background(), "wi-1", "node-1", DefaultTimeout)
	defer sess.stop()
	srv.mu.Lock()
	srv.sessions["wi-1"] = sess
	srv.mu.Unlock()

	decision := srv.AuthorizeChildAccess("wi-1", "child-1")
	if decision != ChildAccessUnknown {
		t.Fatalf("expected ChildAccessUnknown for session with no children, got %d", decision)
	}
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

func waitForSession(t *testing.T, s *SidecarServer) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if s.getSession("wi-1") != nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for session")
}
