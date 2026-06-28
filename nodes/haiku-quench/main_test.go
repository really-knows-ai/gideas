package main

import (
	"context"
	"strings"
	"testing"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal/nodeutil"
	flow "github.com/gideas/flow/sdk/go"
)

const (
	stampLinter  = "linter"
	routeDefault = "default"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newSpyClientWithSpy creates a flow.Client backed by the given quenchSpy.
func newSpyClientWithSpy(
	t *testing.T, spy *quenchSpy,
) (*flow.Client, func()) {
	t.Helper()

	lis, err := nodeutil.NewLocalListener()
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}

	srv := newSpyGRPCServer(spy)
	go func() { _ = srv.Serve(lis) }()

	client, err := flow.NewClient(
		flow.WithSidecarAddress(lis.Addr().String()))
	if err != nil {
		t.Fatalf("NewClient() failed: %v", err)
	}

	cleanup := func() {
		_ = client.Close()
		srv.GracefulStop()
	}
	return client, cleanup
}

// validHaiku is a 5-7-5 haiku used by multiple tests. Syllable counts:
//
//	Line 1: An(1) old(1) si-lent(2) pond(1) = 5
//	Line 2: A(1) frog(1) leaps(1) in(1) to(1) the(1) pond(1) = 7
//	Line 3: Sound(1) of(1) the(1) wa-ter(2) = 5
const validHaiku = "An old silent pond\nA frog leaps in to the pond\nSound of the water"

// invalidHaiku is a 3-4-2 haiku (wrong syllable counts).
const invalidHaiku = "Hello world\nThis is not right\nAt all"

// ---------------------------------------------------------------------------
// Tests: handleQuench — valid haiku, no prior feedback
// ---------------------------------------------------------------------------

func TestHandleQuench_ValidHaiku_NoFeedback(t *testing.T) {
	spy := newQuenchSpy(validHaiku)
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	err := handleQuench(context.Background(), client)
	if err != nil {
		t.Fatalf("handleQuench() error: %v", err)
	}

	// Should NOT raise any feedback.
	if len(spy.AddedFeedback) != 0 {
		t.Errorf("expected no feedback, got %d items",
			len(spy.AddedFeedback))
	}

	// Should stamp "linter".
	if len(spy.StampedNames) != 1 ||
		spy.StampedNames[0] != stampLinter {
		t.Errorf("expected stamp [linter], got %v",
			spy.StampedNames)
	}

	// Should route to "default".
	if len(spy.RoutedOutputs) != 1 ||
		spy.RoutedOutputs[0] != routeDefault {
		t.Errorf("expected route [default], got %v",
			spy.RoutedOutputs)
	}
}

// ---------------------------------------------------------------------------
// Tests: handleQuench — valid haiku, with ACTIONED feedback
// ---------------------------------------------------------------------------

func TestHandleQuench_ValidHaiku_AcceptsActionedFeedback(t *testing.T) {
	spy := newQuenchSpy(validHaiku)
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{
			Id:    "fb-prior-1",
			State: flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED,
		},
		{
			Id:    "fb-prior-2",
			State: flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED,
		},
	}
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	err := handleQuench(context.Background(), client)
	if err != nil {
		t.Fatalf("handleQuench() error: %v", err)
	}

	// Should accept both ACTIONED items.
	if len(spy.AcceptedFixes) != 2 {
		t.Fatalf("expected 2 accepted fixes, got %d",
			len(spy.AcceptedFixes))
	}
	if spy.AcceptedFixes[0] != "fb-prior-1" ||
		spy.AcceptedFixes[1] != "fb-prior-2" {
		t.Errorf("unexpected accepted IDs: %v", spy.AcceptedFixes)
	}

	// Should NOT raise new feedback.
	if len(spy.AddedFeedback) != 0 {
		t.Errorf("expected no new feedback, got %d",
			len(spy.AddedFeedback))
	}

	// Should NOT reject anything.
	if len(spy.RejectedFixes) != 0 {
		t.Errorf("expected no rejected fixes, got %d",
			len(spy.RejectedFixes))
	}
}

// ---------------------------------------------------------------------------
// Tests: handleQuench — valid haiku, skips non-ACTIONED feedback
// ---------------------------------------------------------------------------

func TestHandleQuench_ValidHaiku_SkipsNonActionedFeedback(t *testing.T) {
	spy := newQuenchSpy(validHaiku)
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{
			Id:    "fb-resolved",
			State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED,
		},
		{
			Id:    "fb-new",
			State: flowv1.FeedbackState_FEEDBACK_STATE_NEW,
		},
		{
			Id:    "fb-wontfix",
			State: flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
		},
	}
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	err := handleQuench(context.Background(), client)
	if err != nil {
		t.Fatalf("handleQuench() error: %v", err)
	}

	// Should not accept or reject anything.
	if len(spy.AcceptedFixes) != 0 {
		t.Errorf("expected no accepted fixes, got %d",
			len(spy.AcceptedFixes))
	}
	if len(spy.RejectedFixes) != 0 {
		t.Errorf("expected no rejected fixes, got %d",
			len(spy.RejectedFixes))
	}
}

// ---------------------------------------------------------------------------
// Tests: handleQuench — invalid haiku, no prior feedback
// ---------------------------------------------------------------------------

func TestHandleQuench_InvalidHaiku_NoFeedback(t *testing.T) {
	spy := newQuenchSpy(invalidHaiku)
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	err := handleQuench(context.Background(), client)
	if err != nil {
		t.Fatalf("handleQuench() error: %v", err)
	}

	// Should raise exactly one feedback.
	if len(spy.AddedFeedback) != 1 {
		t.Fatalf("expected 1 feedback, got %d",
			len(spy.AddedFeedback))
	}
	fb := spy.AddedFeedback[0]
	if fb.ArtefactID != "haiku" {
		t.Errorf("expected artefact 'haiku', got %q",
			fb.ArtefactID)
	}
	if fb.CanWontFix {
		t.Errorf("expected CanWontFix=false for quench feedback, got true")
	}
	if !strings.Contains(fb.Message, "must be exactly 5-7-5") {
		t.Errorf("feedback message missing structure info: %q",
			fb.Message)
	}

	// Should still stamp "linter".
	if len(spy.StampedNames) != 1 ||
		spy.StampedNames[0] != stampLinter {
		t.Errorf("expected stamp [linter], got %v",
			spy.StampedNames)
	}

	// Should still route to "default".
	if len(spy.RoutedOutputs) != 1 ||
		spy.RoutedOutputs[0] != routeDefault {
		t.Errorf("expected route [default], got %v",
			spy.RoutedOutputs)
	}
}

// ---------------------------------------------------------------------------
// Tests: handleQuench — invalid haiku, rejects ACTIONED feedback
// ---------------------------------------------------------------------------

func TestHandleQuench_InvalidHaiku_RejectsActionedFeedback(t *testing.T) {
	spy := newQuenchSpy(invalidHaiku)
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{
			Id:    "fb-prior",
			State: flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED,
		},
	}
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	err := handleQuench(context.Background(), client)
	if err != nil {
		t.Fatalf("handleQuench() error: %v", err)
	}

	// Should reject the ACTIONED item.
	if len(spy.RejectedFixes) != 1 {
		t.Fatalf("expected 1 rejected fix, got %d",
			len(spy.RejectedFixes))
	}
	rejMsg, ok := spy.RejectedFixes["fb-prior"]
	if !ok {
		t.Fatal("expected fb-prior to be rejected")
	}
	if !strings.Contains(rejMsg, "Fix did not resolve") {
		t.Errorf("rejection message missing info: %q", rejMsg)
	}

	// Should NOT accept any fixes.
	if len(spy.AcceptedFixes) != 0 {
		t.Errorf("expected no accepted fixes, got %d",
			len(spy.AcceptedFixes))
	}

	// Should also raise new feedback.
	if len(spy.AddedFeedback) != 1 {
		t.Fatalf("expected 1 new feedback, got %d",
			len(spy.AddedFeedback))
	}
}

// ---------------------------------------------------------------------------
// Tests: handleQuench — invalid haiku, skips non-ACTIONED feedback
// ---------------------------------------------------------------------------

func TestHandleQuench_InvalidHaiku_SkipsNonActionedFeedback(
	t *testing.T,
) {
	spy := newQuenchSpy(invalidHaiku)
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{
			Id:    "fb-resolved",
			State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED,
		},
		{
			Id:    "fb-rejected",
			State: flowv1.FeedbackState_FEEDBACK_STATE_REJECTED,
		},
	}
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	err := handleQuench(context.Background(), client)
	if err != nil {
		t.Fatalf("handleQuench() error: %v", err)
	}

	// Should not reject or accept non-ACTIONED items.
	if len(spy.RejectedFixes) != 0 {
		t.Errorf("expected no rejected fixes, got %d",
			len(spy.RejectedFixes))
	}
	if len(spy.AcceptedFixes) != 0 {
		t.Errorf("expected no accepted fixes, got %d",
			len(spy.AcceptedFixes))
	}

	// Should still raise new feedback about the syllable issue.
	if len(spy.AddedFeedback) != 1 {
		t.Fatalf("expected 1 feedback, got %d",
			len(spy.AddedFeedback))
	}
}

// ---------------------------------------------------------------------------
// Tests: handleQuench — mixed feedback states
// ---------------------------------------------------------------------------

func TestHandleQuench_InvalidHaiku_MixedFeedbackStates(
	t *testing.T,
) {
	spy := newQuenchSpy(invalidHaiku)
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{
			Id:    "fb-actioned",
			State: flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED,
		},
		{
			Id:    "fb-resolved",
			State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED,
		},
		{
			Id:    "fb-new",
			State: flowv1.FeedbackState_FEEDBACK_STATE_NEW,
		},
	}
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	err := handleQuench(context.Background(), client)
	if err != nil {
		t.Fatalf("handleQuench() error: %v", err)
	}

	// Only the ACTIONED item should be rejected.
	if len(spy.RejectedFixes) != 1 {
		t.Fatalf("expected 1 rejected fix, got %d",
			len(spy.RejectedFixes))
	}
	if _, ok := spy.RejectedFixes["fb-actioned"]; !ok {
		t.Error("expected fb-actioned to be rejected")
	}
}

func TestHandleQuench_ValidHaiku_MixedFeedbackStates(
	t *testing.T,
) {
	spy := newQuenchSpy(validHaiku)
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{
			Id:    "fb-actioned",
			State: flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED,
		},
		{
			Id:    "fb-resolved",
			State: flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED,
		},
	}
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	err := handleQuench(context.Background(), client)
	if err != nil {
		t.Fatalf("handleQuench() error: %v", err)
	}

	// Only the ACTIONED item should be accepted.
	if len(spy.AcceptedFixes) != 1 {
		t.Fatalf("expected 1 accepted fix, got %d",
			len(spy.AcceptedFixes))
	}
	if spy.AcceptedFixes[0] != "fb-actioned" {
		t.Errorf("expected fb-actioned, got %s",
			spy.AcceptedFixes[0])
	}
}

// ---------------------------------------------------------------------------
// Tests: buildFeedbackMessage
// ---------------------------------------------------------------------------

func TestBuildFeedbackMessage_ContainsBreakdown(t *testing.T) {
	haiku := "Hello world\nThis is not right\nAt all"
	counts := [3]int{3, 4, 2}
	msg := buildFeedbackMessage(haiku, counts)

	if !strings.Contains(msg, "3-4-2") {
		t.Errorf("message missing counts: %q", msg)
	}
	if !strings.Contains(msg, "must be exactly 5-7-5") {
		t.Errorf("message missing target: %q", msg)
	}
	if !strings.Contains(msg, "Line 1:") {
		t.Errorf("message missing line breakdown: %q", msg)
	}
	if !strings.Contains(msg, "Line 2:") {
		t.Errorf("message missing line 2 breakdown: %q", msg)
	}
	if !strings.Contains(msg, "Line 3:") {
		t.Errorf("message missing line 3 breakdown: %q", msg)
	}
}

func TestBuildFeedbackMessage_SkipsBlankLines(t *testing.T) {
	haiku := "Hello world\n\nThis is not right\n\nAt all"
	counts := [3]int{3, 4, 2}
	msg := buildFeedbackMessage(haiku, counts)

	// Should have exactly 3 line breakdowns, not 5.
	lineCount := strings.Count(msg, "Line ")
	if lineCount != 3 {
		t.Errorf("expected 3 line entries, got %d in: %q",
			lineCount, msg)
	}
}

// ---------------------------------------------------------------------------
// Tests: buildRejectionMessage
// ---------------------------------------------------------------------------

func TestBuildRejectionMessage(t *testing.T) {
	counts := [3]int{6, 8, 4}
	msg := buildRejectionMessage(counts)

	if !strings.Contains(msg, "6-8-4") {
		t.Errorf("message missing counts: %q", msg)
	}
	if !strings.Contains(msg, "must be 5-7-5") {
		t.Errorf("message missing target: %q", msg)
	}
	if !strings.Contains(msg, "Fix did not resolve") {
		t.Errorf("message missing prefix: %q", msg)
	}
}

// ---------------------------------------------------------------------------
// Tests: always stamps linter regardless of validity
// ---------------------------------------------------------------------------

func TestHandleQuench_AlwaysStampsLinter(t *testing.T) {
	cases := []struct {
		name  string
		haiku string
	}{
		{"valid", validHaiku},
		{"invalid", invalidHaiku},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := newQuenchSpy(tc.haiku)
			client, cleanup := newSpyClientWithSpy(t, spy)
			defer cleanup()

			err := handleQuench(context.Background(), client)
			if err != nil {
				t.Fatalf("handleQuench() error: %v", err)
			}

			if len(spy.StampedNames) != 1 ||
				spy.StampedNames[0] != stampLinter {
				t.Errorf("expected stamp [linter], got %v",
					spy.StampedNames)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: always routes to default regardless of validity
// ---------------------------------------------------------------------------

func TestHandleQuench_AlwaysRoutesToDefault(t *testing.T) {
	cases := []struct {
		name  string
		haiku string
	}{
		{"valid", validHaiku},
		{"invalid", invalidHaiku},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := newQuenchSpy(tc.haiku)
			client, cleanup := newSpyClientWithSpy(t, spy)
			defer cleanup()

			err := handleQuench(context.Background(), client)
			if err != nil {
				t.Fatalf("handleQuench() error: %v", err)
			}

			if len(spy.RoutedOutputs) != 1 ||
				spy.RoutedOutputs[0] != routeDefault {
				t.Errorf("expected route [default], got %v",
					spy.RoutedOutputs)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: feedback message includes per-word syllable counts
// ---------------------------------------------------------------------------

func TestBuildFeedbackMessage_IncludesPerWordCounts(t *testing.T) {
	haiku := "An old silent pond\nA frog leaps in the\nSound of water"
	// Not a valid haiku, just testing message format.
	counts := [3]int{5, 5, 4}
	msg := buildFeedbackMessage(haiku, counts)

	// Should include word(count) format.
	if !strings.Contains(msg, "An(1)") {
		t.Errorf("expected An(1) in message: %q", msg)
	}
	if !strings.Contains(msg, "silent(2)") {
		t.Errorf("expected silent(2) in message: %q", msg)
	}
}

// ---------------------------------------------------------------------------
// Tests: multiple ACTIONED items rejected on invalid haiku
// ---------------------------------------------------------------------------

func TestHandleQuench_InvalidHaiku_RejectsMultipleActionedItems(
	t *testing.T,
) {
	spy := newQuenchSpy(invalidHaiku)
	spy.FeedbackItems = []*flowv1.FeedbackItem{
		{
			Id:    "fb-1",
			State: flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED,
		},
		{
			Id:    "fb-2",
			State: flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED,
		},
		{
			Id:    "fb-3",
			State: flowv1.FeedbackState_FEEDBACK_STATE_ACTIONED,
		},
	}
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	err := handleQuench(context.Background(), client)
	if err != nil {
		t.Fatalf("handleQuench() error: %v", err)
	}

	// All three should be rejected.
	if len(spy.RejectedFixes) != 3 {
		t.Fatalf("expected 3 rejected fixes, got %d",
			len(spy.RejectedFixes))
	}
	for _, id := range []string{"fb-1", "fb-2", "fb-3"} {
		if _, ok := spy.RejectedFixes[id]; !ok {
			t.Errorf("expected %s to be rejected", id)
		}
	}

	// Should still raise exactly one new feedback.
	if len(spy.AddedFeedback) != 1 {
		t.Errorf("expected 1 new feedback, got %d",
			len(spy.AddedFeedback))
	}
}

// ---------------------------------------------------------------------------
// Tests: empty haiku (not 3 lines)
// ---------------------------------------------------------------------------

func TestHandleQuench_EmptyHaiku_RaisesFeedback(t *testing.T) {
	spy := newQuenchSpy("")
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	err := handleQuench(context.Background(), client)
	if err != nil {
		t.Fatalf("handleQuench() error: %v", err)
	}

	// Empty string has 0 non-empty lines → ValidateHaiku returns
	// [0,0,0], valid=false → should raise feedback.
	if len(spy.AddedFeedback) != 1 {
		t.Fatalf("expected 1 feedback for empty haiku, got %d",
			len(spy.AddedFeedback))
	}
	if spy.AddedFeedback[0].CanWontFix {
		t.Errorf("expected CanWontFix=false for quench feedback, got true")
	}
}

// ---------------------------------------------------------------------------
// Tests: single-line haiku (wrong structure)
// ---------------------------------------------------------------------------

func TestHandleQuench_SingleLine_RaisesFeedback(t *testing.T) {
	spy := newQuenchSpy("just one line here")
	client, cleanup := newSpyClientWithSpy(t, spy)
	defer cleanup()

	err := handleQuench(context.Background(), client)
	if err != nil {
		t.Fatalf("handleQuench() error: %v", err)
	}

	if len(spy.AddedFeedback) != 1 {
		t.Fatalf("expected 1 feedback for single line, got %d",
			len(spy.AddedFeedback))
	}
}
