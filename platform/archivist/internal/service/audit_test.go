package service

import (
	"context"
	"sync"
	"testing"

	"github.com/gideas/flow/archivist/internal/store/sqlite"
	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc/metadata"
)

// mockAuditPublisher captures submitted audit events for testing.
type mockAuditPublisher struct {
	mu     sync.Mutex
	events []*flowv1.PublishRequest
}

func (m *mockAuditPublisher) Submit(req *flowv1.PublishRequest) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, req)
}

func (m *mockAuditPublisher) last() *flowv1.PublishRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.events) == 0 {
		return nil
	}
	return m.events[len(m.events)-1]
}

func (m *mockAuditPublisher) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.events)
}

func newTestServerWithAudit(t *testing.T) (*ArchivistServer, *mockAuditPublisher) {
	t.Helper()
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	pub := &mockAuditPublisher{}
	srv := NewArchivistServer(store, WithAuditPublisher(pub))
	return srv, pub
}

func ctxWithIdentity() context.Context {
	caps := "READ:artefact,WRITE:artefact," +
		"STAMP:artefact/txt/reviewed," +
		"WRITE:feedback/new,WRITE:feedback/actioned," +
		"WRITE:feedback/wont_fix,WRITE:feedback/resolved," +
		"WRITE:feedback/rejected,WRITE:feedback/deadlocked," +
		"WRITE:feedback/link-ruling"
	md := metadata.Pairs(
		"x-flow-node-id", "forge-node",
		"x-flow-namespace", "flow-1",
		"x-flow-workitem-id", "wi-1",
		"x-flow-capabilities", caps,
	)
	return metadata.NewIncomingContext(context.Background(), md)
}

func TestAudit_StoreArtefact_NewVersion(t *testing.T) {
	s, pub := newTestServerWithAudit(t)
	ctx := ctxWithIdentity()

	content := []byte("audit test content")
	hash := sha256Hex(content)

	resp, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          content,
		ContentHash:      hash,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.GetIsNewVersion() {
		t.Fatal("expected new version")
	}

	last := pub.last()
	if last == nil {
		t.Fatal("expected audit event")
	}
	if last.GetChannel() != "audit" {
		t.Fatalf("expected audit channel, got %v", last.GetChannel())
	}
	evt := last.GetEvent()
	if evt.GetEventType() != "audit.artefact.version_created" {
		t.Fatalf("expected event_type audit.artefact.version_created, got %q", evt.GetEventType())
	}
	if evt.GetAttributes()["resource_id"] != "doc" {
		t.Fatalf("expected resource_id=doc, got %q", evt.GetAttributes()["resource_id"])
	}
}

func TestAudit_StoreArtefact_DuplicateNoEvent(t *testing.T) {
	s, pub := newTestServerWithAudit(t)
	ctx := ctxWithIdentity()

	content := []byte("same content")
	hash := sha256Hex(content)
	req := &flowv1.StoreArtefactRequest{
		WorkitemId:       "wi-1",
		ArtefactId:       "doc",
		GovernedArtefact: "txt",
		Content:          content,
		ContentHash:      hash,
	}

	// First store — generates audit event.
	if _, err := s.StoreArtefact(ctx, req); err != nil {
		t.Fatalf("first store: %v", err)
	}
	if pub.count() != 1 {
		t.Fatalf("expected 1 audit event after first store, got %d", pub.count())
	}

	// Second store — duplicate, no audit event.
	if _, err := s.StoreArtefact(ctx, req); err != nil {
		t.Fatalf("second store: %v", err)
	}
	if pub.count() != 1 {
		t.Fatalf("expected 1 audit event after duplicate store (no new event), got %d", pub.count())
	}
}

func TestAudit_StampArtefact(t *testing.T) {
	s, pub := newTestServerWithAudit(t)
	ctx := ctxWithIdentity()

	// Store an artefact first.
	content := []byte("stamp me")
	hash := sha256Hex(content)
	if _, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId: "wi-1", ArtefactId: "doc", GovernedArtefact: "txt",
		Content: content, ContentHash: hash,
	}); err != nil {
		t.Fatalf("store: %v", err)
	}

	pub.mu.Lock()
	pub.events = nil // reset
	pub.mu.Unlock()

	_, err := s.StampArtefact(ctx, &flowv1.StampArtefactRequest{
		WorkitemId: "wi-1", ArtefactId: "doc", StampName: "reviewed",
	})
	if err != nil {
		t.Fatalf("stamp: %v", err)
	}

	last := pub.last()
	if last == nil {
		t.Fatal("expected audit event for stamp")
	}
	evt := last.GetEvent()
	if evt.GetEventType() != "audit.artefact.stamped" {
		t.Fatalf("expected event_type audit.artefact.stamped, got %q", evt.GetEventType())
	}
	if evt.GetAttributes()["stamp_name"] != "reviewed" {
		t.Fatalf("expected stamp_name=reviewed, got %q", evt.GetAttributes()["stamp_name"])
	}
}

func TestAudit_FeedbackLifecycle(t *testing.T) {
	s, pub := newTestServerWithAudit(t)
	ctx := ctxWithIdentity()

	// Store an artefact first.
	content := []byte("feedback target")
	hash := sha256Hex(content)
	if _, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId: "wi-1", ArtefactId: "doc", GovernedArtefact: "txt",
		Content: content, ContentHash: hash,
	}); err != nil {
		t.Fatalf("store: %v", err)
	}

	// AddFeedback.
	addResp, err := s.AddFeedback(ctx, &flowv1.AddFeedbackRequest{
		WorkitemId: "wi-1", ArtefactId: "doc",
		CanWontFix: false, Message: "needs work",
	})
	if err != nil {
		t.Fatalf("add feedback: %v", err)
	}
	feedbackID := addResp.GetFeedbackId()
	assertLastAuditEvent(t, pub, "audit.artefact.feedback.add", feedbackID)

	// ResolveFeedback (NEW -> ACTIONED).
	if _, err := s.ResolveFeedback(ctx, &flowv1.ResolveFeedbackRequest{
		FeedbackId: feedbackID, Message: "fixed it",
	}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	assertLastAuditEvent(t, pub, "audit.artefact.feedback.resolve", feedbackID)

	// AcceptFix (ACTIONED -> RESOLVED).
	if _, err := s.AcceptFix(ctx, &flowv1.AcceptFixRequest{
		FeedbackId: feedbackID,
	}); err != nil {
		t.Fatalf("accept fix: %v", err)
	}
	assertLastAuditEvent(t, pub, "audit.artefact.feedback.accept", feedbackID)
}

func TestAudit_RefuseRejectDeadlockCycle(t *testing.T) {
	s, pub := newTestServerWithAudit(t)
	ctx := ctxWithIdentity()

	// Store artefact.
	content := []byte("refuse test")
	hash := sha256Hex(content)
	if _, err := s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId: "wi-1", ArtefactId: "doc", GovernedArtefact: "txt",
		Content: content, ContentHash: hash,
	}); err != nil {
		t.Fatalf("store: %v", err)
	}

	// Add feedback.
	addResp, err := s.AddFeedback(ctx, &flowv1.AddFeedbackRequest{
		WorkitemId: "wi-1", ArtefactId: "doc",
		CanWontFix: false, Message: "bad",
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	fid := addResp.GetFeedbackId()

	// Refuse (NEW -> WONT_FIX).
	if _, err := s.RefuseFeedback(ctx, &flowv1.RefuseFeedbackRequest{FeedbackId: fid}); err != nil {
		t.Fatalf("refuse: %v", err)
	}
	assertLastAuditEvent(t, pub, "audit.artefact.feedback.refuse", fid)

	// RejectRefusal (WONT_FIX -> REJECTED).
	if _, err := s.RejectRefusal(ctx, &flowv1.RejectRefusalRequest{FeedbackId: fid, Message: "nope"}); err != nil {
		t.Fatalf("reject refusal: %v", err)
	}
	assertLastAuditEvent(t, pub, "audit.artefact.feedback.reject", fid)

	// Resolve again (REJECTED -> ACTIONED).
	if _, err := s.ResolveFeedback(ctx, &flowv1.ResolveFeedbackRequest{FeedbackId: fid, Message: "ok fine"}); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// RejectFix (ACTIONED -> REJECTED).
	if _, err := s.RejectFix(ctx, &flowv1.RejectFixRequest{FeedbackId: fid, Message: "still bad"}); err != nil {
		t.Fatalf("reject fix: %v", err)
	}
	assertLastAuditEvent(t, pub, "audit.artefact.feedback.reject", fid)

	// Deadlock (REJECTED -> DEADLOCKED).
	if _, err := s.DeadlockFeedback(ctx, &flowv1.DeadlockFeedbackRequest{FeedbackId: fid}); err != nil {
		t.Fatalf("deadlock: %v", err)
	}
	assertLastAuditEvent(t, pub, "audit.artefact.feedback.deadlock", fid)
}

func TestAudit_NilPublisher(t *testing.T) {
	// Verify that a nil publisher causes no panic.
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	s := NewArchivistServer(store) // no WithAuditPublisher
	ctx := context.Background()

	content := []byte("nil publisher")
	hash := sha256Hex(content)
	_, err = s.StoreArtefact(ctx, &flowv1.StoreArtefactRequest{
		WorkitemId: "wi-1", ArtefactId: "doc", GovernedArtefact: "txt",
		Content: content, ContentHash: hash,
	})
	if err != nil {
		t.Fatalf("unexpected error with nil publisher: %v", err)
	}
}

func assertLastAuditEvent(t *testing.T, pub *mockAuditPublisher, expectedType, expectedResourceID string) {
	t.Helper()
	last := pub.last()
	if last == nil {
		t.Fatalf("expected audit event %q, got none", expectedType)
	}
	evt := last.GetEvent()
	if evt.GetEventType() != expectedType {
		t.Fatalf("expected event_type %q, got %q", expectedType, evt.GetEventType())
	}
	if expectedResourceID != "" && evt.GetAttributes()["resource_id"] != expectedResourceID {
		t.Fatalf("expected resource_id=%q, got %q", expectedResourceID, evt.GetAttributes()["resource_id"])
	}
}
