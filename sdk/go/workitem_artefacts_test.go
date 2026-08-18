package flow

import "testing"

// ---------------------------------------------------------------------------
// Artefacts — GetArtefact
// ---------------------------------------------------------------------------

func TestWorkitem_GetArtefact(t *testing.T) {
	const wantID = "wi-getartefact-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	art, err := wi.GetArtefact(testPetitionID)
	if err != nil {
		t.Fatalf("GetArtefact() returned error: %v", err)
	}
	if art == nil {
		t.Fatal("expected non-nil Artefact")
	}

	req := env.spy.lastGetArtefactReq
	if req == nil {
		t.Fatal("GetArtefact was not called on the server")
	}
	if req.GetWorkitemId() != wantID {
		t.Fatalf("workitem_id = %q, want %q", req.GetWorkitemId(), wantID)
	}
	if req.GetArtefactId() != testPetitionID {
		t.Fatalf("artefact_id = %q, want %q", req.GetArtefactId(), testPetitionID)
	}

	// Verify the returned domain object has correct fields.
	if art.GovernedArtefact() != testTestArtefact {
		t.Fatalf("Artefact.GovernedArtefact() = %q, want %q", art.GovernedArtefact(), testTestArtefact)
	}
}

func TestWorkitem_GetArtefact_SessionWired(t *testing.T) {
	const wantID = "wi-getartefact-session-001"
	wi, _ := setupWorkitemTestEnv(t, wantID)

	art, err := wi.GetArtefact(testPetitionID)
	if err != nil {
		t.Fatalf("GetArtefact() returned error: %v", err)
	}

	// GetContent() makes an RPC through the session — verifies the session is wired.
	content, err := art.GetContent()
	if err != nil {
		t.Fatalf("Artefact.GetContent() returned error: %v", err)
	}
	if string(content) != testTestContent {
		t.Fatalf("content = %q, want %q", string(content), "test-content")
	}
}

// ---------------------------------------------------------------------------
// Feedback — AddFeedback
// ---------------------------------------------------------------------------

func TestWorkitem_AddFeedback(t *testing.T) {
	const wantID = "wi-addfb-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	fbID, err := wi.AddFeedback(testArtefactID, true, testLooksGood)
	if err != nil {
		t.Fatalf("AddFeedback() returned error: %v", err)
	}

	req := env.spy.lastAddFeedbackReq
	if req == nil {
		t.Fatal("AddFeedback was not called on the server")
	}
	if req.GetArtefactId() != testArtefactID {
		t.Fatalf("artefact_id = %q, want %q", req.GetArtefactId(), "artefact-001")
	}
	if !req.GetCanWontFix() {
		t.Fatal("expected CanWontFix=true")
	}
	if req.GetMessage() != testLooksGood {
		t.Fatalf("message = %q, want %q", req.GetMessage(), testLooksGood)
	}

	if fbID != testFBAuto001 {
		t.Fatalf("feedback ID = %q, want %q", fbID, "fb-auto-001")
	}
}

// ---------------------------------------------------------------------------
// Feedback — GetFeedback
// ---------------------------------------------------------------------------

func TestWorkitem_GetFeedback(t *testing.T) {
	const wantID = "wi-getfb-001"
	wi, env := setupWorkitemTestEnv(t, wantID)

	fbs, err := wi.GetFeedback(testArtefactID)
	if err != nil {
		t.Fatalf("GetFeedback() returned error: %v", err)
	}
	if len(fbs) != 2 {
		t.Fatalf("expected 2 feedback items, got %d", len(fbs))
	}

	req := env.spy.lastGetFeedbackReq
	if req == nil {
		t.Fatal("GetFeedback was not called on the server")
	}
	if req.GetArtefactId() != testArtefactID {
		t.Fatalf("artefact_id = %q, want %q", req.GetArtefactId(), "artefact-001")
	}

	// Verify returned domain objects have correct fields.
	if fbs[0].GetID() != testFB001 {
		t.Fatalf("Feedback[0].GetID() = %q, want %q", fbs[0].GetID(), testFB001)
	}
	if fbs[0].GetMessage() != testNeedsRevision {
		t.Fatalf("Feedback[0].GetMessage() = %q, want %q", fbs[0].GetMessage(), testNeedsRevision)
	}
	if fbs[1].GetID() != testFB002 {
		t.Fatalf("Feedback[1].GetID() = %q, want %q", fbs[1].GetID(), testFB002)
	}
}

func TestWorkitem_GetFeedback_SessionWired(t *testing.T) {
	const wantID = "wi-getfb-session-001"
	wi, _ := setupWorkitemTestEnv(t, wantID)

	fbs, err := wi.GetFeedback(testArtefactID)
	if err != nil {
		t.Fatalf("GetFeedback() returned error: %v", err)
	}
	if len(fbs) == 0 {
		t.Fatal("expected at least one feedback item")
	}

	// GetID() is local — no RPC.
	if fbs[0].GetID() == "" {
		t.Fatal("expected non-empty Feedback ID")
	}

	// GetDepth() makes an RPC — verifies the session is wired.
	depth, err := fbs[0].GetDepth()
	if err != nil {
		t.Fatalf("Feedback.GetDepth() returned error: %v", err)
	}
	if depth != 5 {
		t.Fatalf("expected depth=5, got %d", depth)
	}
}
