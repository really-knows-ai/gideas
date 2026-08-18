package service

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// ---------------------------------------------------------------------------
// RetireLaw
// ---------------------------------------------------------------------------

func TestRetireLaw_Basic(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	findResp, _ := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "To retire",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "x"}},
	})

	resp, err := srv.RetireLaw(ctx, &flowv1.RetireLawRequest{LawId: findResp.GetLawId()})
	if err != nil {
		t.Fatalf("RetireLaw: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}

	// Should no longer be retrievable.
	_, err = srv.GetLaw(ctx, &flowv1.GetLawRequest{LawId: findResp.GetLawId()})
	if err == nil {
		t.Fatal("expected NotFound after retirement")
	}
}

func TestRetireLaw_EmptyID(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.RetireLaw(ctx, &flowv1.RetireLawRequest{})
	if err == nil {
		t.Fatal("expected InvalidArgument error")
	}
}

func TestRetireLaw_DeletesVecEmbedding(t *testing.T) {
	srv := newTestServerWithEmbedder(t)
	ctx := context.Background()

	// Create a law via RecordFinding (which stores embedding).
	findResp, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "To retire",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "retire me"}},
	})
	if err != nil {
		t.Fatalf("RecordFinding: %v", err)
	}

	// Verify embedding exists.
	if !vecEmbeddingStored(t, srv, findResp.GetLawId(), "To retire") {
		t.Fatal("expected vec embedding before retirement")
	}

	// Retire the law.
	_, err = srv.RetireLaw(ctx, &flowv1.RetireLawRequest{LawId: findResp.GetLawId()})
	if err != nil {
		t.Fatalf("RetireLaw: %v", err)
	}

	// Verify embedding is deleted.
	if vecEmbeddingStored(t, srv, findResp.GetLawId(), "To retire") {
		t.Fatal("expected vec embedding to be deleted after retirement")
	}
}
