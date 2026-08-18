package service

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// createDeadlockedFeedback is a helper that creates a feedback item and
// transitions it to DEADLOCKED state. Returns the feedback ID.
func createDeadlockedFeedback(
	t *testing.T, s *ArchivistServer,
	ctx context.Context,
) string {
	t.Helper()

	const workitemID = "wi-1"
	const artefactID = "doc"

	// Store an artefact so feedback has something to reference.
	content := []byte("test content")
	hash := sha256Hex(content)
	_, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       workitemID,
		ArtefactId:       artefactID,
		GovernedArtefact: "txt",
		Content:          content,
		ContentHash:      hash,
	})
	if err != nil {
		t.Fatalf("StoreArtefact: %v", err)
	}

	// Add feedback.
	addResp, err := s.AddFeedback(ctx, &flowv1.AddFeedbackRequest{
		WorkitemId: workitemID,
		ArtefactId: artefactID,
		CanWontFix: false,
		Message:    "test feedback",
	})
	if err != nil {
		t.Fatalf("AddFeedback: %v", err)
	}
	feedbackID := addResp.GetFeedbackId()

	// Transition to DEADLOCKED.
	_, err = s.DeadlockFeedback(ctx, &flowv1.DeadlockFeedbackRequest{
		WorkitemId: workitemID,
		FeedbackId: feedbackID,
	})
	if err != nil {
		t.Fatalf("DeadlockFeedback: %v", err)
	}

	return feedbackID
}

func TestLinkRuling_Success(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	feedbackID := createDeadlockedFeedback(t, s, ctx)

	resp, err := s.LinkRuling(ctx, &flowv1.LinkRulingRequest{
		WorkitemId:  "wi-1",
		FeedbackId:  feedbackID,
		LawId:       "law-001",
		TargetState: flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
	})
	if err != nil {
		t.Fatalf("LinkRuling: unexpected error: %v", err)
	}

	item := resp.GetUpdatedItem()
	if item.GetLinkedRuling() != "law-001" {
		t.Fatalf("expected linked_ruling=law-001, got %q", item.GetLinkedRuling())
	}
	if item.GetState() != flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX {
		t.Fatalf("expected state=WONT_FIX, got %v", item.GetState())
	}
}

func TestLinkRuling_Success_Rejected(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	feedbackID := createDeadlockedFeedback(t, s, ctx)

	resp, err := s.LinkRuling(ctx, &flowv1.LinkRulingRequest{
		WorkitemId:  "wi-1",
		FeedbackId:  feedbackID,
		LawId:       "law-002",
		TargetState: flowv1.FeedbackState_FEEDBACK_STATE_REJECTED,
	})
	if err != nil {
		t.Fatalf("LinkRuling: unexpected error: %v", err)
	}

	item := resp.GetUpdatedItem()
	if item.GetLinkedRuling() != "law-002" {
		t.Fatalf("expected linked_ruling=law-002, got %q", item.GetLinkedRuling())
	}
	if item.GetState() != flowv1.FeedbackState_FEEDBACK_STATE_REJECTED {
		t.Fatalf("expected state=REJECTED, got %v", item.GetState())
	}
}

func TestLinkRuling_NotDeadlocked(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	// Create artefact.
	content := []byte("test")
	hash := sha256Hex(content)
	_, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          content,
		ContentHash:      hash,
	})
	if err != nil {
		t.Fatalf("StoreArtefact: %v", err)
	}

	// Add feedback (stays in NEW state).
	addResp, err := s.AddFeedback(ctx, &flowv1.AddFeedbackRequest{
		WorkitemId: "wi-1",
		ArtefactId: "doc",
		CanWontFix: false,
		Message:    "test feedback",
	})
	if err != nil {
		t.Fatalf("AddFeedback: %v", err)
	}

	// LinkRuling should fail — feedback is in NEW state, not DEADLOCKED.
	_, err = s.LinkRuling(ctx, &flowv1.LinkRulingRequest{
		WorkitemId:  "wi-1",
		FeedbackId:  addResp.GetFeedbackId(),
		LawId:       "law-001",
		TargetState: flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
	})
	if err == nil {
		t.Fatal("expected error for non-DEADLOCKED feedback")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}
}

func TestLinkRuling_ContemptGuard(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	feedbackID := createDeadlockedFeedback(t, s, ctx)

	// First LinkRuling succeeds.
	_, err := s.LinkRuling(ctx, &flowv1.LinkRulingRequest{
		WorkitemId:  "wi-1",
		FeedbackId:  feedbackID,
		LawId:       "law-001",
		TargetState: flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
	})
	if err != nil {
		t.Fatalf("first LinkRuling: unexpected error: %v", err)
	}

	// Second LinkRuling should fail — contempt guard.
	_, err = s.LinkRuling(ctx, &flowv1.LinkRulingRequest{
		WorkitemId:  "wi-1",
		FeedbackId:  feedbackID,
		LawId:       "law-002",
		TargetState: flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
	})
	if err == nil {
		t.Fatal("expected error for contempt guard violation")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}
}

func TestLinkRuling_FeedbackNotFound(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	_, err := s.LinkRuling(ctx, &flowv1.LinkRulingRequest{
		WorkitemId:  "wi-1",
		FeedbackId:  "nonexistent",
		LawId:       "law-001",
		TargetState: flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
	})
	if err == nil {
		t.Fatal("expected error for nonexistent feedback")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

func TestLinkRuling_MissingFields(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	// Empty feedback_id.
	_, err := s.LinkRuling(ctx, &flowv1.LinkRulingRequest{
		WorkitemId:  "wi-1",
		FeedbackId:  "",
		LawId:       "law-001",
		TargetState: flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
	})
	if err == nil {
		t.Fatal("expected error for empty feedback_id")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}

	// Empty law_id.
	_, err = s.LinkRuling(ctx, &flowv1.LinkRulingRequest{
		WorkitemId:  "wi-1",
		FeedbackId:  "fb-1",
		LawId:       "",
		TargetState: flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
	})
	if err == nil {
		t.Fatal("expected error for empty law_id")
	}
	st, ok = status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}

	// Missing target_state (UNSPECIFIED).
	_, err = s.LinkRuling(ctx, &flowv1.LinkRulingRequest{
		WorkitemId: "wi-1",
		FeedbackId: "fb-1",
		LawId:      "law-001",
	})
	if err == nil {
		t.Fatal("expected error for missing target_state")
	}
	st, ok = status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestLinkRuling_VisibleInGetFeedback(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	feedbackID := createDeadlockedFeedback(t, s, ctx)

	// Link ruling.
	_, err := s.LinkRuling(ctx, &flowv1.LinkRulingRequest{
		WorkitemId:  "wi-1",
		FeedbackId:  feedbackID,
		LawId:       "law-001",
		TargetState: flowv1.FeedbackState_FEEDBACK_STATE_WONT_FIX,
	})
	if err != nil {
		t.Fatalf("LinkRuling: %v", err)
	}

	// Verify linked_ruling is visible in GetFeedback.
	fbResp, err := s.GetFeedback(ctx, &flowv1.GetFeedbackRequest{
		WorkitemId: "wi-1",
		ArtefactId: "doc",
	})
	if err != nil {
		t.Fatalf("GetFeedback: %v", err)
	}

	if len(fbResp.GetFeedbackItems()) != 1 {
		t.Fatalf("expected 1 feedback item, got %d", len(fbResp.GetFeedbackItems()))
	}

	item := fbResp.GetFeedbackItems()[0]
	if item.GetLinkedRuling() != "law-001" {
		t.Fatalf("expected linked_ruling=law-001 in GetFeedback, got %q", item.GetLinkedRuling())
	}
}
