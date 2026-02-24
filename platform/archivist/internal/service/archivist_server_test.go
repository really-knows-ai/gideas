package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/gideas/flow/archivist/internal/store/sqlite"
	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
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
		Severity:   flowv1.Severity_SEVERITY_HIGH,
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

// ---------------------------------------------------------------------------
// Capability Enforcement Tests
// ---------------------------------------------------------------------------

// nodeCtx creates a context simulating a node-originated call with
// the given capabilities. The x-flow-node-id presence signals that
// this is a node call subject to capability enforcement.
func nodeCtx(caps string) context.Context {
	md := metadata.Pairs(
		metadataKeyNodeID, "node-1",
		metadataKeyCapabilities, caps,
	)
	return metadata.NewIncomingContext(context.Background(), md)
}

// systemCtx creates a bare context with no node identity — simulating
// a system-to-system call (e.g. Operator calling Archivist).
func systemCtx() context.Context {
	return context.Background()
}

func TestCapability_StoreArtefact_Denied(t *testing.T) {
	s := newTestServer(t)
	ctx := nodeCtx("READ:artefact") // No WRITE:artefact.

	_, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          []byte("x"),
		ContentHash:      sha256Hex([]byte("x")),
	})
	if err == nil {
		t.Fatal("expected PermissionDenied for missing WRITE:artefact")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestCapability_StoreArtefact_AllowedBroad(t *testing.T) {
	s := newTestServer(t)
	ctx := nodeCtx("WRITE:artefact")

	resp, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          []byte("x"),
		ContentHash:      sha256Hex([]byte("x")),
	})
	if err != nil {
		t.Fatalf("expected success with WRITE:artefact, got %v", err)
	}
	if !resp.GetIsNewVersion() {
		t.Fatal("expected is_new_version=true")
	}
}

func TestCapability_StoreArtefact_AllowedScoped(t *testing.T) {
	s := newTestServer(t)
	ctx := nodeCtx("WRITE:artefact/txt") // Scoped to "txt" governed artefact.

	resp, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          []byte("x"),
		ContentHash:      sha256Hex([]byte("x")),
	})
	if err != nil {
		t.Fatalf("expected success with WRITE:artefact/txt, got %v", err)
	}
	if !resp.GetIsNewVersion() {
		t.Fatal("expected is_new_version=true")
	}
}

func TestCapability_StoreArtefact_ScopedDenied(t *testing.T) {
	s := newTestServer(t)
	ctx := nodeCtx("WRITE:artefact/json") // Scoped to "json" but storing "txt".

	_, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          []byte("x"),
		ContentHash:      sha256Hex([]byte("x")),
	})
	if err == nil {
		t.Fatal("expected PermissionDenied when scope does not match")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestCapability_GetArtefact_Denied(t *testing.T) {
	s := newTestServer(t)
	ctx := nodeCtx("WRITE:artefact") // No READ:artefact.

	_, err := s.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId: "wi-1",
		ArtefactId: "doc",
	})
	if err == nil {
		t.Fatal("expected PermissionDenied for missing READ:artefact")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestCapability_SystemCall_BypassesEnforcement(t *testing.T) {
	s := newTestServer(t)
	ctx := systemCtx() // No node identity — system call.

	// Store an artefact with no capabilities at all (system call).
	resp, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-sys",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          []byte("system data"),
		ContentHash:      sha256Hex([]byte("system data")),
	})
	if err != nil {
		t.Fatalf("system call should bypass capability enforcement, got %v", err)
	}
	if !resp.GetIsNewVersion() {
		t.Fatal("expected is_new_version=true")
	}
}

func TestCapability_NodeCallNoCapabilities_Denied(t *testing.T) {
	s := newTestServer(t)
	// Node-originated call with no capabilities at all.
	md := metadata.Pairs(metadataKeyNodeID, "node-1")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          []byte("x"),
		ContentHash:      sha256Hex([]byte("x")),
	})
	if err == nil {
		t.Fatal("expected PermissionDenied for node call with empty capabilities")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestCapability_StampArtefact_ExactMatch(t *testing.T) {
	s := newTestServer(t)

	// First store an artefact (as system call to avoid capability gate).
	ctx := systemCtx()
	content := []byte("stampable content")
	hash := sha256Hex(content)
	if _, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-stamp",
		ArtefactId:       "doc",
		GovernedArtefact: "petition-draft",
		Content:          content,
		ContentHash:      hash,
	}); err != nil {
		t.Fatalf("StoreArtefact: %v", err)
	}

	// Node with STAMP:artefact/petition-draft/linter can stamp linter on petition-draft.
	nodeWithStamp := nodeCtx("STAMP:artefact/petition-draft/linter")
	_, err := s.StampArtefact(nodeWithStamp, &flowv1.StampArtefactRequest{
		WorkitemId: "wi-stamp",
		ArtefactId: "doc",
		StampName:  "linter",
	})
	if err != nil {
		t.Fatalf("expected success with exact stamp capability, got %v", err)
	}

	// Same node cannot stamp "security-review" on petition-draft.
	_, err = s.StampArtefact(nodeWithStamp, &flowv1.StampArtefactRequest{
		WorkitemId: "wi-stamp",
		ArtefactId: "doc",
		StampName:  "security-review",
	})
	if err == nil {
		t.Fatal("expected PermissionDenied for wrong stamp name")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestCapability_AddFeedback_Denied(t *testing.T) {
	s := newTestServer(t)
	ctx := nodeCtx("READ:feedback") // No WRITE:feedback/new.

	_, err := s.AddFeedback(ctx, &flowv1.AddFeedbackRequest{
		WorkitemId: "wi-1",
		ArtefactId: "doc",
		Severity:   flowv1.Severity_SEVERITY_HIGH,
		Message:    "test",
	})
	if err == nil {
		t.Fatal("expected PermissionDenied for missing WRITE:feedback/new")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestCapability_ListArtefacts_NoGateRequired(t *testing.T) {
	s := newTestServer(t)
	// Node call with no capabilities — ListArtefacts is implicit (no gate).
	md := metadata.Pairs(metadataKeyNodeID, "node-1")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := s.ListArtefacts(ctx, &flowv1.ListArtefactsRequest{
		WorkitemId: "wi-1",
	})
	if err != nil {
		t.Fatalf("ListArtefacts should not require capabilities, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Cross-Workitem Read Tests
// ---------------------------------------------------------------------------

// crossWorkitemCtx creates a context for a cross-Workitem read test: node
// identity, workitem identity, and READ:artefact capability.
func crossWorkitemCtx(workitemID string) context.Context {
	md := metadata.Pairs(
		metadataKeyNodeID, "node-1",
		"x-flow-workitem-id", workitemID,
		metadataKeyCapabilities, "READ:artefact",
	)
	return metadata.NewIncomingContext(context.Background(), md)
}

// mockOperatorClient implements flowv1.OperatorServiceClient for testing
// cross-Workitem reads. Only ValidateChildAccess is implemented; all
// other methods panic.
type mockOperatorClient struct {
	flowv1.OperatorServiceClient
	validateResp *flowv1.ValidateChildAccessResponse
	validateErr  error
}

func (m *mockOperatorClient) ValidateChildAccess(
	_ context.Context, _ *flowv1.ValidateChildAccessRequest, _ ...grpc.CallOption,
) (*flowv1.ValidateChildAccessResponse, error) {
	return m.validateResp, m.validateErr
}

// newTestServerWithOperator creates a test server with a mock Operator client.
func newTestServerWithOperator(t *testing.T, opClient *mockOperatorClient) *ArchivistServer {
	t.Helper()
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewArchivistServer(store, WithOperatorClient(opClient))
}

// storeChildArtefact is a helper that stores an artefact under the canonical
// child workitem "child-wi".
func storeChildArtefact(t *testing.T, s *ArchivistServer, artefactID string, content []byte) {
	t.Helper()
	hash := sha256Hex(content)
	_, err := s.StoreArtefact(context.Background(), &flowv1.StoreArtefactRequest{
		WorkitemId:       "child-wi",
		ArtefactId:       artefactID,
		GovernedArtefact: "txt",
		Content:          content,
		ContentHash:      hash,
	})
	if err != nil {
		t.Fatalf("StoreArtefact(child-wi, %s): %v", artefactID, err)
	}
}

func TestGetArtefact_CrossWorkitem_Valid(t *testing.T) {
	opClient := &mockOperatorClient{
		validateResp: &flowv1.ValidateChildAccessResponse{Valid: true, Phase: "Completed"},
	}
	s := newTestServerWithOperator(t, opClient)

	// Store artefact under child workitem.
	storeChildArtefact(t, s, "doc", []byte("child data"))

	// Parent reads child's artefact via target_workitem_id.
	ctx := crossWorkitemCtx("parent-wi")
	resp, err := s.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId:       "parent-wi",
		ArtefactId:       "doc",
		TargetWorkitemId: "child-wi",
	})
	if err != nil {
		t.Fatalf("GetArtefact cross-Workitem: unexpected error: %v", err)
	}
	if string(resp.GetContent()) != "child data" {
		t.Fatalf("expected 'child data', got %q", string(resp.GetContent()))
	}
}

func TestGetArtefact_CrossWorkitem_WrongParent(t *testing.T) {
	opClient := &mockOperatorClient{
		validateResp: &flowv1.ValidateChildAccessResponse{Valid: false, Phase: "Completed"},
	}
	s := newTestServerWithOperator(t, opClient)

	storeChildArtefact(t, s, "doc", []byte("child data"))

	ctx := crossWorkitemCtx("wrong-parent")
	_, err := s.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId:       "wrong-parent",
		ArtefactId:       "doc",
		TargetWorkitemId: "child-wi",
	})
	if err == nil {
		t.Fatal("expected error for wrong parent")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}

func TestGetArtefact_CrossWorkitem_ChildNotCompleted(t *testing.T) {
	opClient := &mockOperatorClient{
		validateResp: &flowv1.ValidateChildAccessResponse{Valid: false, Phase: "Running"},
	}
	s := newTestServerWithOperator(t, opClient)

	ctx := crossWorkitemCtx("parent-wi")
	_, err := s.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId:       "parent-wi",
		ArtefactId:       "doc",
		TargetWorkitemId: "child-wi",
	})
	if err == nil {
		t.Fatal("expected error for non-completed child")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", err)
	}
}

func TestGetArtefact_CrossWorkitem_OperatorUnavailable(t *testing.T) {
	opClient := &mockOperatorClient{
		validateErr: status.Error(codes.Unavailable, "connection refused"),
	}
	s := newTestServerWithOperator(t, opClient)

	ctx := crossWorkitemCtx("parent-wi")
	_, err := s.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId:       "parent-wi",
		ArtefactId:       "doc",
		TargetWorkitemId: "child-wi",
	})
	if err == nil {
		t.Fatal("expected error when Operator is unavailable")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Internal {
		t.Fatalf("expected Internal (fail-closed), got %v", err)
	}
}

func TestGetArtefact_NoTargetWorkitem_UnchangedBehaviour(t *testing.T) {
	s := newTestServer(t) // No operator client — fine for normal reads.

	content := []byte("normal data")
	hash := sha256Hex(content)
	_, err := s.StoreArtefact(context.Background(), &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          content,
		ContentHash:      hash,
	})
	if err != nil {
		t.Fatalf("StoreArtefact: %v", err)
	}

	resp, err := s.GetArtefact(context.Background(), &flowv1.GetArtefactRequest{
		WorkitemId: "wi-1",
		ArtefactId: "doc",
	})
	if err != nil {
		t.Fatalf("GetArtefact (no target): unexpected error: %v", err)
	}
	if string(resp.GetContent()) != "normal data" {
		t.Fatalf("expected 'normal data', got %q", string(resp.GetContent()))
	}
}

func TestGetArtefact_CrossWorkitem_NoOperatorClient(t *testing.T) {
	s := newTestServer(t) // No operator client.

	ctx := crossWorkitemCtx("parent-wi")
	_, err := s.GetArtefact(ctx, &flowv1.GetArtefactRequest{
		WorkitemId:       "parent-wi",
		ArtefactId:       "doc",
		TargetWorkitemId: "child-wi",
	})
	if err == nil {
		t.Fatal("expected error when operator client not configured")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.Unavailable {
		t.Fatalf("expected Unavailable, got %v", err)
	}
}

func TestListArtefacts_CrossWorkitem_Valid(t *testing.T) {
	opClient := &mockOperatorClient{
		validateResp: &flowv1.ValidateChildAccessResponse{Valid: true, Phase: "Completed"},
	}
	s := newTestServerWithOperator(t, opClient)

	storeChildArtefact(t, s, "doc1", []byte("a"))
	storeChildArtefact(t, s, "doc2", []byte("b"))

	ctx := crossWorkitemCtx("parent-wi")
	resp, err := s.ListArtefacts(ctx, &flowv1.ListArtefactsRequest{
		WorkitemId:       "parent-wi",
		TargetWorkitemId: "child-wi",
	})
	if err != nil {
		t.Fatalf("ListArtefacts cross-Workitem: unexpected error: %v", err)
	}
	if len(resp.GetArtefactRefs()) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(resp.GetArtefactRefs()))
	}
}

func TestListArtefacts_CrossWorkitem_Invalid(t *testing.T) {
	opClient := &mockOperatorClient{
		validateResp: &flowv1.ValidateChildAccessResponse{Valid: false, Phase: "Completed"},
	}
	s := newTestServerWithOperator(t, opClient)

	ctx := crossWorkitemCtx("wrong-parent")
	_, err := s.ListArtefacts(ctx, &flowv1.ListArtefactsRequest{
		WorkitemId:       "wrong-parent",
		TargetWorkitemId: "child-wi",
	})
	if err == nil {
		t.Fatal("expected error for invalid cross-Workitem read")
	}
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", err)
	}
}
