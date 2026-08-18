package service

import (
	"context"
	"testing"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

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
