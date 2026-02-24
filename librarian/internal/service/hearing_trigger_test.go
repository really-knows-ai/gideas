package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gideas/flow/librarian/internal/store/sqlite"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
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

// mockHearingCreator captures CreateHearingWorkitem calls.
type mockHearingCreator struct {
	mu    sync.Mutex
	calls []*flowv1.CreateHearingWorkitemRequest
}

func (m *mockHearingCreator) CreateHearingWorkitem(
	_ context.Context, req *flowv1.CreateHearingWorkitemRequest,
	_ ...grpc.CallOption,
) (*flowv1.CreateHearingWorkitemResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, req)
	return &flowv1.CreateHearingWorkitemResponse{WorkitemId: "hearing-" + req.GetLawId()}, nil
}

func (m *mockHearingCreator) lastLawID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return ""
	}
	return m.calls[len(m.calls)-1].GetLawId()
}

func (m *mockHearingCreator) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
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
// Review-TTL-expiry trigger tests
// ---------------------------------------------------------------------------

func TestHearingTrigger_ReviewTTLExpiry(t *testing.T) {
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Seed a tier-1 law.
	idCounter = 0
	srv := NewLibrarianServer(store, nil, testIDGen, 0.85)
	resp, err := srv.RecordFinding(context.Background(), &flowv1.RecordFindingRequest{
		Goal:            "Old finding",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "old"}},
	})
	if err != nil {
		t.Fatalf("RecordFinding: %v", err)
	}
	lawID := resp.GetLawId()

	// Create a hearing trigger with a very short TTL.
	operator := &mockHearingCreator{}
	auditor := &mockAuditPublisher{}

	ht := NewHearingTrigger(HearingTriggerConfig{
		Operator: operator,
		Store:    store,
		TTLConfig: ReviewTTLConfig{
			Tier1: 1 * time.Millisecond, // Immediately expired.
		},
		ScanPeriod: 100 * time.Millisecond,
		Auditor:    auditor,
	})

	// Override now to ensure the law's age exceeds the TTL.
	ht.nowFn = func() time.Time {
		return time.Now().Add(1 * time.Hour) // 1 hour in the future.
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// Run in background.
	done := make(chan struct{})
	go func() {
		ht.Run(ctx)
		close(done)
	}()

	// Wait for the scan to trigger a hearing.
	deadline := time.After(2 * time.Second)
	for operator.callCount() <= 0 {

		select {
		case <-deadline:
			t.Fatal("timed out waiting for hearing to be triggered")
		case <-time.After(50 * time.Millisecond):
		}
	}

	cancel()
	<-done

	if operator.lastLawID() != lawID {
		t.Fatalf("expected hearing for law %q, got %q", lawID, operator.lastLawID())
	}

	// Verify audit event was published.
	if auditor.count() == 0 {
		t.Fatal("expected audit event for hearing trigger")
	}
	last := auditor.last()
	if last.GetEvent().GetEventType() != "audit.hearing.triggered" {
		t.Fatalf("expected audit.hearing.triggered, got %q", last.GetEvent().GetEventType())
	}
}

func TestHearingTrigger_NoDuplicateHearing(t *testing.T) {
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Seed a tier-1 law.
	idCounter = 0
	srv := NewLibrarianServer(store, nil, testIDGen, 0.85)
	_, err = srv.RecordFinding(context.Background(), &flowv1.RecordFindingRequest{
		Goal:            "Dup test",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "dup"}},
	})
	if err != nil {
		t.Fatalf("RecordFinding: %v", err)
	}

	operator := &mockHearingCreator{}

	ht := NewHearingTrigger(HearingTriggerConfig{
		Operator: operator,
		Store:    store,
		TTLConfig: ReviewTTLConfig{
			Tier1: 1 * time.Millisecond,
		},
		ScanPeriod: 50 * time.Millisecond,
	})
	ht.nowFn = func() time.Time {
		return time.Now().Add(1 * time.Hour)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		ht.Run(ctx)
		close(done)
	}()

	<-ctx.Done()
	<-done

	// Despite multiple scan cycles, only one hearing should be created.
	if operator.callCount() != 1 {
		t.Fatalf("expected exactly 1 hearing, got %d", operator.callCount())
	}
}

func TestHearingTrigger_NoTTLConfigured_NoHearing(t *testing.T) {
	store, err := sqlite.New(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Seed a tier-1 law.
	idCounter = 0
	srv := NewLibrarianServer(store, nil, testIDGen, 0.85)
	_, err = srv.RecordFinding(context.Background(), &flowv1.RecordFindingRequest{
		Goal:            "No TTL",
		Representations: []*flowv1.Representation{{Type: "text/plain", Content: "n"}},
	})
	if err != nil {
		t.Fatalf("RecordFinding: %v", err)
	}

	operator := &mockHearingCreator{}

	ht := NewHearingTrigger(HearingTriggerConfig{
		Operator:   operator,
		Store:      store,
		TTLConfig:  ReviewTTLConfig{}, // No TTLs.
		ScanPeriod: 50 * time.Millisecond,
	})
	ht.nowFn = func() time.Time {
		return time.Now().Add(100 * time.Hour)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		ht.Run(ctx)
		close(done)
	}()

	<-ctx.Done()
	<-done

	if operator.callCount() != 0 {
		t.Fatalf("expected no hearings (no TTL configured), got %d", operator.callCount())
	}
}

// ---------------------------------------------------------------------------
// Friction-channel trigger test
// ---------------------------------------------------------------------------

func TestHearingTrigger_FrictionTrigger(t *testing.T) {
	operator := &mockHearingCreator{}
	auditor := &mockAuditPublisher{}

	ht := NewHearingTrigger(HearingTriggerConfig{
		Operator: operator,
		Auditor:  auditor,
	})

	// Directly test the triggerHearing method.
	ctx := context.Background()
	ht.triggerHearing(ctx, "law-friction-1", "friction_threshold")

	if operator.callCount() != 1 {
		t.Fatalf("expected 1 hearing, got %d", operator.callCount())
	}
	if operator.lastLawID() != "law-friction-1" {
		t.Fatalf("expected law_id law-friction-1, got %q", operator.lastLawID())
	}

	// Duplicate should be suppressed.
	ht.triggerHearing(ctx, "law-friction-1", "friction_threshold")
	if operator.callCount() != 1 {
		t.Fatalf("expected 1 hearing after duplicate, got %d", operator.callCount())
	}

	// Clear pending and trigger again.
	ht.ClearPending("law-friction-1")
	ht.triggerHearing(ctx, "law-friction-1", "friction_threshold")
	if operator.callCount() != 2 {
		t.Fatalf("expected 2 hearings after ClearPending, got %d", operator.callCount())
	}
}
