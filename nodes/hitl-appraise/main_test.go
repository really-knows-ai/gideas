package main

import (
	"context"
	"strings"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

func TestHITLAppraise_HappyPath(t *testing.T) {
	spy := newHITLAppraiseSpy()
	client := newSpyClient(t, spy)
	h := newTestQueueManager(t)

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
		wi, _ := client.GetWorkitem(wctx.GetWorkitemId())
		errCh <- handleAppraise(ctx, client, wi, h.qm, cfg, wctx)
	}()

	// Simulate human decision: wait for the item, then claim and decide.
	h.decide(t, ctx, "wi-hitl-1", "")

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

	if len(spy.ReadArtefacts) != 3 {
		t.Fatalf("expected 3 GetArtefact calls (input + governed + stamp), got %d", len(spy.ReadArtefacts))
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
	h := newTestQueueManager(t)

	ctx := context.Background()
	wctx := &flowv1.WorkitemContext{
		WorkitemId:    "wi-no-stamp",
		FlowNamespace: "flow-1",
		NodeId:        "hitl-appraise",
	}

	wi := newTestWorkitem(t, client, wctx.GetWorkitemId())
	err := handleAppraise(ctx, client, wi, h.qm, &hitlAppraiseConfig{InputArtefact: "petition"}, wctx)
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
	h := newTestQueueManager(t)

	ctx, cancel := context.WithCancel(context.Background())
	wctx := &flowv1.WorkitemContext{
		WorkitemId:    "wi-cancel",
		FlowNamespace: "flow-1",
		NodeId:        "hitl-appraise",
	}

	errCh := make(chan error, 1)
	go func() {
		wi, _ := client.GetWorkitem(wctx.GetWorkitemId())
		errCh <- handleAppraise(ctx, client, wi, h.qm, &hitlAppraiseConfig{InputArtefact: "petition"}, wctx)
	}()

	// Cancel while waiting for decision.
	cancel()

	err := <-errCh
	if err == nil {
		t.Fatal("expected error on context cancellation")
	}
}
