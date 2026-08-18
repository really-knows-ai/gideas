package service

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

//nolint:dupl // intentional similarity with DoesNotResolve test
func TestStoreArtefact_AutoResolvesStaleFeedback(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	// Store v1.
	v1 := []byte("version 1")
	v1Hash := sha256Hex(v1)
	_, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          v1,
		ContentHash:      v1Hash,
	})
	if err != nil {
		t.Fatalf("StoreArtefact v1: %v", err)
	}

	// Add feedback on v1 with canWontFix=false.
	addResp, err := s.AddFeedback(ctx, &flowv1.AddFeedbackRequest{
		WorkitemId:  "wi-1",
		ArtefactId:  "doc",
		CanWontFix:  false,
		Message:     "structural issue",
		VersionHash: v1Hash,
	})
	if err != nil {
		t.Fatalf("AddFeedback: %v", err)
	}
	feedbackID := addResp.GetFeedbackId()

	// Store v2 — this should auto-resolve the v1 feedback.
	v2 := []byte("version 2")
	v2Hash := sha256Hex(v2)
	_, err = s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          v2,
		ContentHash:      v2Hash,
	})
	if err != nil {
		t.Fatalf("StoreArtefact v2: %v", err)
	}

	// Verify old feedback is now RESOLVED.
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
	if item.GetId() != feedbackID {
		t.Fatalf("expected feedback id %q, got %q", feedbackID, item.GetId())
	}
	if item.GetState() != flowv1.FeedbackState_FEEDBACK_STATE_RESOLVED {
		t.Fatalf("expected state RESOLVED, got %v", item.GetState())
	}
}

//nolint:dupl // intentional similarity with AutoResolves test
func TestStoreArtefact_DoesNotResolveCanWontFixFeedback(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	// Store v1.
	v1 := []byte("version 1")
	v1Hash := sha256Hex(v1)
	_, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          v1,
		ContentHash:      v1Hash,
	})
	if err != nil {
		t.Fatalf("StoreArtefact v1: %v", err)
	}

	// Add feedback with canWontFix=true on v1.
	addResp, err := s.AddFeedback(ctx, &flowv1.AddFeedbackRequest{
		WorkitemId:  "wi-1",
		ArtefactId:  "doc",
		CanWontFix:  true,
		Message:     "subjective review",
		VersionHash: v1Hash,
	})
	if err != nil {
		t.Fatalf("AddFeedback: %v", err)
	}
	feedbackID := addResp.GetFeedbackId()

	// Store v2.
	v2 := []byte("version 2")
	v2Hash := sha256Hex(v2)
	_, err = s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          v2,
		ContentHash:      v2Hash,
	})
	if err != nil {
		t.Fatalf("StoreArtefact v2: %v", err)
	}

	// Verify feedback is still in NEW state (not resolved).
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
	if item.GetId() != feedbackID {
		t.Fatalf("expected feedback id %q, got %q", feedbackID, item.GetId())
	}
	if item.GetState() != flowv1.FeedbackState_FEEDBACK_STATE_NEW {
		t.Fatalf("expected state NEW, got %v", item.GetState())
	}
}
