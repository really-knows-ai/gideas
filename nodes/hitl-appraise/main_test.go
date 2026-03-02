package main

import (
	"context"
	"strings"
	"testing"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
)

func TestHITLAppraise_HappyPath(t *testing.T) {
	spy := newHITLAppraiseSpy()
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)

	ctx := context.Background()
	wctx := &flowv1.WorkitemContext{
		WorkitemId:    "wi-hitl-1",
		FlowNamespace: "flow-1",
		NodeId:        "hitl-appraise",
	}

	// Run handler in a goroutine — it will block on WaitForDecision.
	cfg := &hitlAppraiseConfig{InputArtefact: "petition"}
	errCh := make(chan error, 1)
	go func() {
		errCh <- handleAppraise(ctx, client, qm, cfg, wctx)
	}()

	// Simulate human: wait for item to appear, then claim and decide.
	waitForEnqueue(t, qm, "wi-hitl-1")

	_, err := qm.Claim(ctx, "wi-hitl-1")
	if err != nil {
		t.Fatalf("Claim failed: %v", err)
	}
	if err := qm.Decide(ctx, "wi-hitl-1", ""); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	// Wait for handler to complete.
	if err := <-errCh; err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	// Verify operations.
	spy.mu.Lock()
	defer spy.mu.Unlock()

	if spy.TopologyCalls != 1 {
		t.Errorf("expected 1 GetFlowTopology call, got %d", spy.TopologyCalls)
	}

	if len(spy.ReadArtefacts) != 2 {
		t.Fatalf("expected 2 GetArtefact calls, got %d", len(spy.ReadArtefacts))
	}
	if spy.ReadArtefacts[0] != "petition" {
		t.Errorf("expected first read=petition, got %s", spy.ReadArtefacts[0])
	}
	if spy.ReadArtefacts[1] != "haiku" {
		t.Errorf("expected second read=haiku, got %s", spy.ReadArtefacts[1])
	}

	if spy.PauseTimerCalls != 1 {
		t.Errorf("expected 1 PauseTimer call, got %d", spy.PauseTimerCalls)
	}
	if spy.ResumeTimerCalls != 1 {
		t.Errorf("expected 1 ResumeTimer call, got %d", spy.ResumeTimerCalls)
	}

	if len(spy.StampedArtefacts) != 1 {
		t.Fatalf("expected 1 stamp, got %d", len(spy.StampedArtefacts))
	}
	if spy.StampedArtefacts[0].ArtefactID != "haiku" {
		t.Errorf("expected stamp on haiku, got %s", spy.StampedArtefacts[0].ArtefactID)
	}
	if spy.StampedArtefacts[0].StampName != "review" {
		t.Errorf("expected stamp name=review, got %s", spy.StampedArtefacts[0].StampName)
	}

	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "default" {
		t.Errorf("expected route to 'default', got %v", spy.RoutedOutputs)
	}
}

func TestHITLAppraise_NoStampCapability(t *testing.T) {
	spy := newHITLAppraiseSpy()
	spy.Topology = &flowv1.GetFlowTopologyResponse{
		Self: &flowv1.FlowNode{
			Name:         "hitl-appraise",
			Capabilities: []string{"READ:flow", "READ:artefact"},
		},
	}
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)

	ctx := context.Background()
	wctx := &flowv1.WorkitemContext{
		WorkitemId:    "wi-no-stamp",
		FlowNamespace: "flow-1",
		NodeId:        "hitl-appraise",
	}

	err := handleAppraise(ctx, client, qm, &hitlAppraiseConfig{InputArtefact: "petition"}, wctx)
	if err == nil {
		t.Fatal("expected error when no stamp capability")
	}
	if got := err.Error(); !strings.Contains(got, "no STAMP:artefact capability") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestHITLAppraise_ContextCancellation(t *testing.T) {
	spy := newHITLAppraiseSpy()
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)

	ctx, cancel := context.WithCancel(context.Background())
	wctx := &flowv1.WorkitemContext{
		WorkitemId:    "wi-cancel",
		FlowNamespace: "flow-1",
		NodeId:        "hitl-appraise",
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- handleAppraise(ctx, client, qm, &hitlAppraiseConfig{InputArtefact: "petition"}, wctx)
	}()

	// Cancel while waiting for decision.
	cancel()

	err := <-errCh
	if err == nil {
		t.Fatal("expected error on context cancellation")
	}
}

func TestDiscoverStamp(t *testing.T) {
	tests := []struct {
		name         string
		capabilities []string
		wantKind     string
		wantStamp    string
		wantErr      bool
	}{
		{
			name:         "single stamp capability",
			capabilities: []string{"READ:flow", "STAMP:artefact/haiku/review"},
			wantKind:     "haiku",
			wantStamp:    "review",
		},
		{
			name:         "multiple stamps uses first",
			capabilities: []string{"STAMP:artefact/haiku/review", "STAMP:artefact/doc/linter"},
			wantKind:     "haiku",
			wantStamp:    "review",
		},
		{
			name:         "no stamp capability",
			capabilities: []string{"READ:flow", "WRITE:feedback/new"},
			wantErr:      true,
		},
		{
			name:         "empty capabilities",
			capabilities: nil,
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := newHITLAppraiseSpy()
			spy.Topology = &flowv1.GetFlowTopologyResponse{
				Self: &flowv1.FlowNode{
					Name:         "test-node",
					Capabilities: tt.capabilities,
				},
			}
			client := newSpyClient(t, spy)

			kind, stamp, err := discoverStamp(context.Background(), client)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if kind != tt.wantKind {
				t.Errorf("kind=%q, want %q", kind, tt.wantKind)
			}
			if stamp != tt.wantStamp {
				t.Errorf("stamp=%q, want %q", stamp, tt.wantStamp)
			}
		})
	}
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
