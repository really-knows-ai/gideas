package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/gideas/flow/archivist/internal/store/sqlite"
	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h[:])
}

func newTestServer(t *testing.T) *ArchivistServer {
	t.Helper()
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewArchivistServer(store)
}

func TestStoreArtefact_NewVersion(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	content := []byte("Hello from Step 1")
	hash := sha256Hex(content)

	resp, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "greeting",
		GovernedArtefact: "txt",
		Content:          content,
		ContentHash:      hash,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.GetIsNewVersion() {
		t.Fatal("expected is_new_version=true for first store")
	}
	if resp.GetVersionHash() != hash {
		t.Fatalf("expected version_hash=%q, got %q", hash, resp.GetVersionHash())
	}
}

func TestStoreArtefact_DuplicateContent(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	content := []byte("Hello from Step 1")
	hash := sha256Hex(content)

	req := &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "greeting",
		GovernedArtefact: "txt",
		Content:          content,
		ContentHash:      hash,
	}

	// First store.
	if _, err := s.StoreArtefact(ctx, req); err != nil {
		t.Fatalf("first StoreArtefact: %v", err)
	}

	// Second store with same content.
	resp, err := s.StoreArtefact(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetIsNewVersion() {
		t.Fatal("expected is_new_version=false for duplicate content")
	}
}

func TestStoreArtefact_UpdatedContent(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	// Store v1.
	v1Content := []byte("version 1")
	v1Hash := sha256Hex(v1Content)
	if _, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          v1Content,
		ContentHash:      v1Hash,
	}); err != nil {
		t.Fatalf("StoreArtefact v1: %v", err)
	}

	// Store v2 with different content.
	v2Content := []byte("version 2")
	v2Hash := sha256Hex(v2Content)
	resp, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          v2Content,
		ContentHash:      v2Hash,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.GetIsNewVersion() {
		t.Fatal("expected is_new_version=true for updated content")
	}
	if resp.GetVersionHash() != v2Hash {
		t.Fatalf("expected version_hash=%q, got %q", v2Hash, resp.GetVersionHash())
	}
}

func TestGetArtefact_LatestVersion(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	content := []byte("Hello from Step 1")
	hash := sha256Hex(content)

	if _, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "greeting",
		GovernedArtefact: "txt",
		Content:          content,
		ContentHash:      hash,
	}); err != nil {
		t.Fatalf("StoreArtefact: %v", err)
	}

	resp, err := s.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId: "wi-1",
		ArtefactId: "greeting",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(resp.GetContent()) != "Hello from Step 1" {
		t.Fatalf("expected 'Hello from Step 1', got %q", string(resp.GetContent()))
	}
	if resp.GetVersionHash() != hash {
		t.Fatalf("expected version_hash=%q, got %q", hash, resp.GetVersionHash())
	}
	if resp.GetGovernedArtefact() != "txt" {
		t.Fatalf("expected governed_artefact=txt, got %q", resp.GetGovernedArtefact())
	}
}

func TestGetArtefact_NotFound(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	_, err := s.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId: "wi-1",
		ArtefactId: "missing",
	})
	if err == nil {
		t.Fatal("expected error for missing artefact")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

func TestGetArtefactVersion(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	content := []byte("specific version")
	hash := sha256Hex(content)

	if _, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          content,
		ContentHash:      hash,
	}); err != nil {
		t.Fatalf("StoreArtefact: %v", err)
	}

	resp, err := s.GetArtefactVersion(ctx, &flowv1.GetArtefactVersionRequest{
		WorkitemId:  "wi-1",
		ArtefactId:  "doc",
		VersionHash: hash,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(resp.GetContent()) != "specific version" {
		t.Fatalf("expected 'specific version', got %q", string(resp.GetContent()))
	}
}

func TestGetArtefactVersion_NotFound(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	_, err := s.GetArtefactVersion(ctx, &flowv1.GetArtefactVersionRequest{
		WorkitemId:  "wi-1",
		ArtefactId:  "doc",
		VersionHash: "nonexistent-hash",
	})
	if err == nil {
		t.Fatal("expected error for missing version")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

func TestListArtefacts(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	if _, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "doc1",
		GovernedArtefact: "txt",
		Content:          []byte("a"),
		ContentHash:      sha256Hex([]byte("a")),
	}); err != nil {
		t.Fatalf("StoreArtefact doc1: %v", err)
	}
	if _, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "doc2",
		GovernedArtefact: "json",
		Content:          []byte("b"),
		ContentHash:      sha256Hex([]byte("b")),
	}); err != nil {
		t.Fatalf("StoreArtefact doc2: %v", err)
	}

	resp, err := s.ListArtefacts(ctx, &flowv1.ListArtefactsRequest{
		WorkitemId: "wi-1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetArtefactRefs()) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(resp.GetArtefactRefs()))
	}
}

func TestGetArtefactMetadata(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	v1 := []byte("v1")
	v2 := []byte("v2")
	if _, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          v1,
		ContentHash:      sha256Hex(v1),
	}); err != nil {
		t.Fatalf("StoreArtefact v1: %v", err)
	}
	if _, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          v2,
		ContentHash:      sha256Hex(v2),
	}); err != nil {
		t.Fatalf("StoreArtefact v2: %v", err)
	}

	resp, err := s.GetArtefactMetadata(ctx, &flowv1.GetArtefactMetadataRequest{
		WorkitemId: "wi-1",
		ArtefactId: "doc",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetVersionHistory()) != 2 {
		t.Fatalf("expected 2 versions, got %d", len(resp.GetVersionHistory()))
	}
}

func TestEndToEnd_StoreAndRetrieve(t *testing.T) {
	// Simulates the full data handover: Step 1 writes, Step 2 reads.
	s := newTestServer(t)
	ctx := context.Background()

	content := []byte("Hello from Step 1")
	hash := sha256Hex(content)

	// Step 1: Store
	storeResp, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "test-workitem-001",
		ArtefactId:       "greeting",
		GovernedArtefact: "txt",
		Content:          content,
		ContentHash:      hash,
	})
	if err != nil {
		t.Fatalf("StoreArtefact failed: %v", err)
	}
	if !storeResp.GetIsNewVersion() {
		t.Fatal("expected new version")
	}

	// Step 2: Retrieve
	getResp, err := s.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId: "test-workitem-001",
		ArtefactId: "greeting",
	})
	if err != nil {
		t.Fatalf("GetArtefact failed: %v", err)
	}
	if string(getResp.GetContent()) != "Hello from Step 1" {
		t.Fatalf("expected 'Hello from Step 1', got %q", string(getResp.GetContent()))
	}
}

// ---------------------------------------------------------------------------
// LinkRuling Tests
// ---------------------------------------------------------------------------

// createDeadlockedFeedback is a helper that creates a feedback item and
// transitions it to DEADLOCKED state. Returns the feedback ID.
func createDeadlockedFeedback(
	t *testing.T, s *ArchivistServer,
	ctx context.Context, workitemID, artefactID string,
) string {
	t.Helper()

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
		Severity:   flowv1.Severity_SEVERITY_HIGH,
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

	feedbackID := createDeadlockedFeedback(t, s, ctx, "wi-1", "doc")

	resp, err := s.LinkRuling(ctx, &flowv1.LinkRulingRequest{
		WorkitemId: "wi-1",
		FeedbackId: feedbackID,
		LawId:      "law-001",
	})
	if err != nil {
		t.Fatalf("LinkRuling: unexpected error: %v", err)
	}

	item := resp.GetUpdatedItem()
	if item.GetLinkedRuling() != "law-001" {
		t.Fatalf("expected linked_ruling=law-001, got %q", item.GetLinkedRuling())
	}
	if item.GetState() != flowv1.FeedbackState_FEEDBACK_STATE_DEADLOCKED {
		t.Fatalf("expected state=DEADLOCKED, got %v", item.GetState())
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
		Severity:   flowv1.Severity_SEVERITY_HIGH,
		Message:    "test feedback",
	})
	if err != nil {
		t.Fatalf("AddFeedback: %v", err)
	}

	// LinkRuling should fail — feedback is in NEW state, not DEADLOCKED.
	_, err = s.LinkRuling(ctx, &flowv1.LinkRulingRequest{
		WorkitemId: "wi-1",
		FeedbackId: addResp.GetFeedbackId(),
		LawId:      "law-001",
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

	feedbackID := createDeadlockedFeedback(t, s, ctx, "wi-1", "doc")

	// First LinkRuling succeeds.
	_, err := s.LinkRuling(ctx, &flowv1.LinkRulingRequest{
		WorkitemId: "wi-1",
		FeedbackId: feedbackID,
		LawId:      "law-001",
	})
	if err != nil {
		t.Fatalf("first LinkRuling: unexpected error: %v", err)
	}

	// Second LinkRuling should fail — contempt guard.
	_, err = s.LinkRuling(ctx, &flowv1.LinkRulingRequest{
		WorkitemId: "wi-1",
		FeedbackId: feedbackID,
		LawId:      "law-002",
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
		WorkitemId: "wi-1",
		FeedbackId: "nonexistent",
		LawId:      "law-001",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent feedback")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}
}

func TestLinkRuling_MissingFields(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	// Empty feedback_id.
	_, err := s.LinkRuling(ctx, &flowv1.LinkRulingRequest{
		WorkitemId: "wi-1",
		FeedbackId: "",
		LawId:      "law-001",
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
		WorkitemId: "wi-1",
		FeedbackId: "fb-1",
		LawId:      "",
	})
	if err == nil {
		t.Fatal("expected error for empty law_id")
	}
	st, ok = status.FromError(err)
	if !ok || st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestLinkRuling_VisibleInGetFeedback(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	feedbackID := createDeadlockedFeedback(t, s, ctx, "wi-1", "doc")

	// Link ruling.
	_, err := s.LinkRuling(ctx, &flowv1.LinkRulingRequest{
		WorkitemId: "wi-1",
		FeedbackId: feedbackID,
		LawId:      "law-001",
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
