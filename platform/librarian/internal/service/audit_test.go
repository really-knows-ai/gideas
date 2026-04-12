package service

import (
	"context"
	"sync"
	"testing"

	"github.com/gideas/flow/librarian/internal/store/sqlite"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// mockAuditPublisher captures submitted audit events.
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

// ---------------------------------------------------------------------------
// Audit publishing tests
// ---------------------------------------------------------------------------

func newTestServerWithAudit(t *testing.T) (*LibrarianServer, *mockAuditPublisher) {
	t.Helper()
	idCounter = 0
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	pub := &mockAuditPublisher{}
	srv := NewLibrarianServer(store, nil, testIDGen, 0.85, WithAuditPublisher(pub))
	return srv, pub
}

func TestAudit_RecordFinding(t *testing.T) {
	srv, pub := newTestServerWithAudit(t)
	ctx := context.Background()

	_, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Test finding",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "test"}},
	})
	if err != nil {
		t.Fatalf("RecordFinding: %v", err)
	}

	if pub.count() != 1 {
		t.Fatalf("expected 1 audit event, got %d", pub.count())
	}
	last := pub.last()
	if last.GetChannel() != "audit" {
		t.Fatalf("expected audit channel, got %v", last.GetChannel())
	}
	evt := last.GetEvent()
	if evt.GetEventType() != "audit.law.created" {
		t.Fatalf("expected audit.law.created, got %q", evt.GetEventType())
	}
}

func TestAudit_WriteLaw_NewAndUpdate(t *testing.T) {
	srv, pub := newTestServerWithAudit(t)
	ctx := context.Background()

	// New law (no ID).
	resp, err := srv.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Goal:            "New ruling",
			Tier:            2,
			Representations: []*flowv1.Representation{{Type: "text/plain", Content: "r"}},
		},
	})
	if err != nil {
		t.Fatalf("WriteLaw new: %v", err)
	}

	last := pub.last()
	if last.GetEvent().GetEventType() != "audit.law.created" {
		t.Fatalf("expected audit.law.created, got %q", last.GetEvent().GetEventType())
	}

	// Update existing law.
	_, err = srv.WriteLaw(ctx, &flowv1.WriteLawRequest{
		Law: &flowv1.Law{
			Id:              resp.GetLawId(),
			Goal:            "Updated ruling",
			Tier:            2,
			Representations: []*flowv1.Representation{{Type: "text/plain", Content: "r2"}},
		},
	})
	if err != nil {
		t.Fatalf("WriteLaw update: %v", err)
	}

	last = pub.last()
	if last.GetEvent().GetEventType() != "audit.law.updated" {
		t.Fatalf("expected audit.law.updated, got %q", last.GetEvent().GetEventType())
	}
}

func TestAudit_RetireLaw(t *testing.T) {
	srv, pub := newTestServerWithAudit(t)
	ctx := context.Background()

	resp, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Retire me",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "r"}},
	})
	if err != nil {
		t.Fatalf("RecordFinding: %v", err)
	}

	_, err = srv.RetireLaw(ctx, &flowv1.RetireLawRequest{LawId: resp.GetLawId()})
	if err != nil {
		t.Fatalf("RetireLaw: %v", err)
	}

	last := pub.last()
	if last.GetEvent().GetEventType() != "audit.law.retired" {
		t.Fatalf("expected audit.law.retired, got %q", last.GetEvent().GetEventType())
	}
}

func TestAudit_ApplyLifecycleAction_Promote(t *testing.T) {
	srv, pub := newTestServerWithAudit(t)
	ctx := context.Background()

	resp, err := srv.RecordFinding(ctx, &flowv1.RecordFindingRequest{
		Goal:            "Promote me",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "p"}},
	})
	if err != nil {
		t.Fatalf("RecordFinding: %v", err)
	}

	_, err = srv.ApplyLifecycleAction(ctx, &flowv1.ApplyLifecycleActionRequest{
		LawId:   resp.GetLawId(),
		Verdict: flowv1.Verdict_VERDICT_PROMOTE,
	})
	if err != nil {
		t.Fatalf("ApplyLifecycleAction: %v", err)
	}

	last := pub.last()
	if last.GetEvent().GetEventType() != "audit.law.promoted" {
		t.Fatalf("expected audit.law.promoted, got %q", last.GetEvent().GetEventType())
	}
}

func TestAudit_NilPublisher(t *testing.T) {
	// Verify nil publisher causes no panic.
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	idCounter = 0

	srv := NewLibrarianServer(store, nil, testIDGen, 0.85) // No audit publisher.

	_, err = srv.RecordFinding(context.Background(), &flowv1.RecordFindingRequest{
		Goal:            "Nil pub test",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "n"}},
	})
	if err != nil {
		t.Fatalf("unexpected error with nil publisher: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Dispute Record Audit Events
// ---------------------------------------------------------------------------

func TestAudit_CreateDisputeRecord(t *testing.T) {
	srv, pub := newTestServerWithAudit(t)
	ctx := context.Background()

	_, err := srv.CreateDisputeRecord(ctx, &flowv1.CreateDisputeRecordRequest{
		PetitionId:  "petition-audit-1",
		CitedLawIds: []string{"law-a", "law-b"},
	})
	if err != nil {
		t.Fatalf("CreateDisputeRecord: %v", err)
	}

	if pub.count() != 1 {
		t.Fatalf("expected 1 audit event, got %d", pub.count())
	}
	last := pub.last()
	if last.GetChannel() != "audit" {
		t.Fatalf("expected audit channel, got %q", last.GetChannel())
	}
	evt := last.GetEvent()
	if evt.GetEventType() != "audit.dispute.created" {
		t.Fatalf("expected audit.dispute.created, got %q", evt.GetEventType())
	}
	if evt.GetAttributes()["petition_id"] != "petition-audit-1" {
		t.Fatalf("expected petition_id attribute, got %v", evt.GetAttributes())
	}
	if evt.GetAttributes()["cited_law_ids"] != "law-a,law-b" {
		t.Fatalf("expected cited_law_ids attribute, got %v", evt.GetAttributes())
	}
}

func TestAudit_RetireDisputeRecord(t *testing.T) {
	srv, pub := newTestServerWithAudit(t)
	ctx := context.Background()

	// Create first (generates 1 audit event).
	if _, err := srv.CreateDisputeRecord(ctx, &flowv1.CreateDisputeRecordRequest{
		PetitionId:  "petition-audit-2",
		CitedLawIds: []string{"law-c"},
	}); err != nil {
		t.Fatalf("CreateDisputeRecord: %v", err)
	}

	// Retire (generates 2nd audit event).
	if _, err := srv.RetireDisputeRecord(ctx, &flowv1.RetireDisputeRecordRequest{
		PetitionId: "petition-audit-2",
	}); err != nil {
		t.Fatalf("RetireDisputeRecord: %v", err)
	}

	if pub.count() != 2 {
		t.Fatalf("expected 2 audit events, got %d", pub.count())
	}
	last := pub.last()
	evt := last.GetEvent()
	if evt.GetEventType() != "audit.dispute.retired" {
		t.Fatalf("expected audit.dispute.retired, got %q", evt.GetEventType())
	}
	if evt.GetAttributes()["petition_id"] != "petition-audit-2" {
		t.Fatalf("expected petition_id attribute, got %v", evt.GetAttributes())
	}
}
