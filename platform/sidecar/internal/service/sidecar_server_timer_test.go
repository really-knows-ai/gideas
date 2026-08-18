package service

import (
	"context"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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
	if !sess.paused {
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
