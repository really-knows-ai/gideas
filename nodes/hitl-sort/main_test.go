package main

import (
	"context"
	"strings"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

// ---------------------------------------------------------------------------
// Happy path tests
// ---------------------------------------------------------------------------

func TestHITLSort_HappyPath_WithStamp(t *testing.T) {
	spy := newHITLSortSpy()
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)

	cfg := &hitlSortConfig{
		HumanChoices: []choiceMapping{
			{Output: "approve", Label: "Approve"},
			{Output: "reject", Label: "Reject"},
		},
		Stamp: true,
	}

	ctx := context.Background()
	wctx := &flowv1.WorkitemContext{
		WorkitemId:    "wi-sort-1",
		FlowNamespace: "flow-1",
		NodeId:        "hitl-sort",
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- handleSort(ctx, client, qm, cfg, wctx)
	}()

	// Simulate human: wait for item to appear, claim, then decide.
	waitForEnqueue(t, qm, "wi-sort-1")

	_, err := qm.Claim(ctx, "wi-sort-1")
	if err != nil {
		t.Fatalf("Claim failed: %v", err)
	}
	if err := qm.Decide(ctx, "wi-sort-1", "approve"); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	if spy.TopologyCalls != 1 {
		t.Errorf("expected 1 GetFlowTopology call, got %d", spy.TopologyCalls)
	}
	if spy.PauseTimerCalls != 1 {
		t.Errorf("expected 1 PauseTimer call, got %d", spy.PauseTimerCalls)
	}
	if spy.ResumeTimerCalls != 1 {
		t.Errorf("expected 1 ResumeTimer call, got %d", spy.ResumeTimerCalls)
	}

	// Stamp should be applied.
	if len(spy.StampedArtefacts) != 1 {
		t.Fatalf("expected 1 stamp, got %d", len(spy.StampedArtefacts))
	}
	if spy.StampedArtefacts[0].ArtefactID != "haiku" {
		t.Errorf("expected stamp on haiku, got %s", spy.StampedArtefacts[0].ArtefactID)
	}
	if spy.StampedArtefacts[0].StampName != "review" {
		t.Errorf("expected stamp name=review, got %s", spy.StampedArtefacts[0].StampName)
	}

	// Routed to "approve".
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "approve" {
		t.Errorf("expected route to 'approve', got %v", spy.RoutedOutputs)
	}
}

func TestHITLSort_HappyPath_NoStamp(t *testing.T) {
	spy := newHITLSortSpy()
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)

	cfg := &hitlSortConfig{
		HumanChoices: []choiceMapping{
			{Output: "approve", Label: "Approve"},
			{Output: "reject", Label: "Reject"},
		},
		Stamp: false,
	}

	ctx := context.Background()
	wctx := &flowv1.WorkitemContext{WorkitemId: "wi-sort-nostamp"}

	errCh := make(chan error, 1)
	go func() {
		errCh <- handleSort(ctx, client, qm, cfg, wctx)
	}()

	waitForEnqueue(t, qm, "wi-sort-nostamp")
	_, _ = qm.Claim(ctx, "wi-sort-nostamp")
	if err := qm.Decide(ctx, "wi-sort-nostamp", "reject"); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	spy.mu.Lock()
	defer spy.mu.Unlock()

	// No stamps should have been applied.
	if len(spy.StampedArtefacts) != 0 {
		t.Errorf("expected 0 stamps, got %d", len(spy.StampedArtefacts))
	}

	// Routed to "reject".
	if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != "reject" {
		t.Errorf("expected route to 'reject', got %v", spy.RoutedOutputs)
	}
}

// ---------------------------------------------------------------------------
// Multiple choices test
// ---------------------------------------------------------------------------

func TestHITLSort_MultipleChoices_AllRoute(t *testing.T) {
	choices := []struct {
		output string
		label  string
	}{
		{"approve", "Approve"},
		{"reject", "Reject"},
		{"escalate", "Escalate"},
	}

	for _, tc := range choices {
		t.Run("choice="+tc.output, func(t *testing.T) {
			spy := newHITLSortSpy()
			client := newSpyClient(t, spy)
			qm := newTestQueueManager(t)

			cfg := &hitlSortConfig{
				HumanChoices: []choiceMapping{
					{Output: "approve", Label: "Approve"},
					{Output: "reject", Label: "Reject"},
					{Output: "escalate", Label: "Escalate"},
				},
			}

			ctx := context.Background()
			wctx := &flowv1.WorkitemContext{WorkitemId: "wi-multi-" + tc.output}

			errCh := make(chan error, 1)
			go func() {
				errCh <- handleSort(ctx, client, qm, cfg, wctx)
			}()

			waitForEnqueue(t, qm, "wi-multi-"+tc.output)
			_, _ = qm.Claim(ctx, "wi-multi-"+tc.output)
			if err := qm.Decide(ctx, "wi-multi-"+tc.output, tc.output); err != nil {
				t.Fatalf("Decide failed: %v", err)
			}

			if err := <-errCh; err != nil {
				t.Fatalf("handler returned error: %v", err)
			}

			spy.mu.Lock()
			defer spy.mu.Unlock()

			if len(spy.RoutedOutputs) != 1 || spy.RoutedOutputs[0] != tc.output {
				t.Errorf("expected route to %q, got %v", tc.output, spy.RoutedOutputs)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Error cases
// ---------------------------------------------------------------------------

func TestHITLSort_InvalidChoice(t *testing.T) {
	spy := newHITLSortSpy()
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)

	cfg := &hitlSortConfig{
		HumanChoices: []choiceMapping{
			{Output: "approve", Label: "Approve"},
			{Output: "reject", Label: "Reject"},
		},
	}

	ctx := context.Background()
	wctx := &flowv1.WorkitemContext{WorkitemId: "wi-invalid-choice"}

	errCh := make(chan error, 1)
	go func() {
		errCh <- handleSort(ctx, client, qm, cfg, wctx)
	}()

	waitForEnqueue(t, qm, "wi-invalid-choice")
	_, _ = qm.Claim(ctx, "wi-invalid-choice")
	// Human picks an output not in humanChoices.
	if err := qm.Decide(ctx, "wi-invalid-choice", "escalate"); err != nil {
		t.Fatalf("Decide failed: %v", err)
	}

	err := <-errCh
	if err == nil {
		t.Fatal("expected error for invalid choice, got nil")
	}
	if !strings.Contains(err.Error(), "invalid choice") {
		t.Errorf("expected 'invalid choice' in error, got: %v", err)
	}
}

func TestHITLSort_ContextCancellation(t *testing.T) {
	spy := newHITLSortSpy()
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)

	cfg := &hitlSortConfig{
		HumanChoices: []choiceMapping{
			{Output: "approve", Label: "Approve"},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	wctx := &flowv1.WorkitemContext{WorkitemId: "wi-ctx-cancel"}

	errCh := make(chan error, 1)
	go func() {
		errCh <- handleSort(ctx, client, qm, cfg, wctx)
	}()

	// Wait for enqueue, then cancel context while waiting for decision.
	waitForEnqueue(t, qm, "wi-ctx-cancel")
	cancel()

	err := <-errCh
	if err == nil {
		t.Fatal("expected error on context cancellation, got nil")
	}
}

func TestHITLSort_NoStampCapabilityWithStampEnabled(t *testing.T) {
	spy := newSpyNoStamp()
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)

	cfg := &hitlSortConfig{
		HumanChoices: []choiceMapping{
			{Output: "approve", Label: "Approve"},
			{Output: "reject", Label: "Reject"},
		},
		Stamp: true, // wants to stamp but topology has no STAMP capability
	}

	ctx := context.Background()
	wctx := &flowv1.WorkitemContext{WorkitemId: "wi-no-stamp-cap"}

	err := handleSort(ctx, client, qm, cfg, wctx)
	if err == nil {
		t.Fatal("expected error when stamp=true but no STAMP capability in topology")
	}
	if !strings.Contains(err.Error(), "STAMP:artefact") {
		t.Errorf("expected 'STAMP:artefact' in error, got: %v", err)
	}
}

func TestHITLSort_ConfigChoiceNotInTopology(t *testing.T) {
	spy := newHITLSortSpy() // topology has: approve, reject, escalate
	client := newSpyClient(t, spy)
	qm := newTestQueueManager(t)

	cfg := &hitlSortConfig{
		HumanChoices: []choiceMapping{
			{Output: "approve", Label: "Approve"},
			{Output: "publish", Label: "Publish"}, // NOT in topology
		},
	}

	ctx := context.Background()
	wctx := &flowv1.WorkitemContext{WorkitemId: "wi-bad-config"}

	err := handleSort(ctx, client, qm, cfg, wctx)
	if err == nil {
		t.Fatal("expected error when config output not in topology")
	}
	if !strings.Contains(err.Error(), "publish") {
		t.Errorf("expected 'publish' in error, got: %v", err)
	}
}
