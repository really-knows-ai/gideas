package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestTryRemotePullOnInitCloneFailurePublishesTelemetry verifies SPEC R1: a
// startup clone failure publishes a "cartographer.clone_failed" telemetry
// event on the Event Bus (via the async publisher) while startup stays
// non-blocking. The event must carry the pod's flow namespace and a timestamp,
// matching the server's publishTelemetry and the sync worker's publishFailure
// emitters (the AsyncPublisher forwards requests verbatim, so an event without
// its own attribution is stored un-attributable to a flow).
func TestTryRemotePullOnInitCloneFailurePublishesTelemetry(t *testing.T) {
	gs := &initPullGitStore{isEmpty: true, cloneErr: errors.New("clone boom")}
	spy, pub := newTestAuditPub(t)
	catchUp, err := tryRemotePullOnInit(gs, "https://private.example/repo.git", "remote-auth", "test-ns",
		func(context.Context, string) (map[string]string, error) {
			return map[string]string{"password": "expired"}, nil
		}, pub, nil)
	if err != nil {
		t.Fatalf("clone failure blocked startup: %v", err)
	}
	if catchUp {
		t.Fatal("failed clone path must not flag a catch-up push")
	}
	if gs.cloneCalls != 1 {
		t.Fatalf("clone calls = %d, want 1", gs.cloneCalls)
	}
	req := waitForTelemetry(t, spy, "cartographer.clone_failed")
	if req.GetChannel() != "telemetry" {
		t.Fatalf("telemetry channel = %q, want %q", req.GetChannel(), "telemetry")
	}
	if got := req.GetEvent().GetFlowNamespace(); got != "test-ns" {
		t.Fatalf("telemetry flow namespace = %q, want %q", got, "test-ns")
	}
	if ts := req.GetEvent().GetTimestamp(); ts == nil {
		t.Fatal("telemetry event has no Timestamp, want one (matching the server/sync-worker emitters)")
	} else if ts.AsTime().Before(time.Now().Add(-time.Minute)) {
		t.Fatalf("telemetry timestamp %v is not recent", ts.AsTime())
	}
	if got := req.GetEvent().GetAttributes()["url"]; got != "https://private.example/repo.git" {
		t.Fatalf("telemetry url attribute = %q, want the remote URL", got)
	}
	if got := req.GetEvent().GetAttributes()["error"]; got != "clone boom" {
		t.Fatalf("telemetry error attribute = %q, want %q", got, "clone boom")
	}
}

// TestTryRemotePullOnInitCatchUpPushEmitsNoTelemetry verifies the init path
// publishes no "cartographer.push_failed" telemetry on the catch-up path: the
// catch-up push is deferred to the sync worker's first cycle (SPEC R10 Init),
// which is the sole push-failure emitter, so a startup must
// not report the same push through two emitters.
func TestTryRemotePullOnInitCatchUpPushEmitsNoTelemetry(t *testing.T) {
	gs := &initPullGitStore{isEmpty: false}
	spy, pub := newTestAuditPub(t)
	catchUp, err := tryRemotePullOnInit(gs, "https://public.example/repo.git", "", "", nil, pub, nil)
	if err != nil {
		t.Fatalf("tryRemotePullOnInit: %v", err)
	}
	if !catchUp {
		t.Fatal("expected catch-up push flag on a non-empty repo, got false")
	}
	if gs.pushCalls != 0 {
		t.Fatalf("direct push calls = %d, want 0 (push is deferred to the sync worker's first cycle)", gs.pushCalls)
	}
	// The async publisher gets a beat to drain; the init path must never
	// submit a push-failure event (that telemetry belongs to the worker).
	time.Sleep(50 * time.Millisecond)
	if calls := spy.getCalls(); len(calls) != 0 {
		t.Fatalf("tryRemotePullOnInit published %d telemetry events on the catch-up path, want 0: %+v",
			len(calls), calls)
	}
}
