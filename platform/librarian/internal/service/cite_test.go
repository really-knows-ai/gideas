package service

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// ---------------------------------------------------------------------------
// Cite
// ---------------------------------------------------------------------------

func TestCite_Basic(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Create a law first.
	findResp, _ := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Citeable law",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "cite me"}},
	})

	resp, err := srv.Cite(ctx, &flowv1.CiteRequest{
		LawIds: []string{findResp.GetLawId()},
	})
	if err != nil {
		t.Fatalf("Cite: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}
}

func TestCite_EmptyLawIDs(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.Cite(ctx, &flowv1.CiteRequest{})
	if err == nil {
		t.Fatal("expected error for empty law_ids")
	}
}

func TestCite_MissingLaw_NoError(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Cite a non-existent law — should succeed (citation is a signal).
	resp, err := srv.Cite(ctx, &flowv1.CiteRequest{
		LawIds: []string{"nonexistent-law"},
	})
	if err != nil {
		t.Fatalf("Cite should not fail for missing laws: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}
}
