package sqlite

import (
	"context"
	"testing"
)

// ---------------------------------------------------------------------------
// Feedback: can_wont_fix field tests
// ---------------------------------------------------------------------------

// feedbackByID reads back a single feedback record through the production
// GetFeedback surface (there is no single-record read API). All feedback
// tests operate on the (wi-1, art-1) pair.
func feedbackByID(t *testing.T, ctx context.Context, s *Store, id string) *FeedbackRecord {
	t.Helper()
	records, err := s.GetFeedback(ctx, "wi-1", "art-1")
	if err != nil {
		t.Fatalf("GetFeedback: %v", err)
	}
	for i := range records {
		if records[i].ID == id {
			return &records[i]
		}
	}
	t.Fatalf("feedback record %q not found", id)
	return nil
}

func TestAddFeedback_CanWontFix(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Add feedback with canWontFix=true.
	id, err := s.AddFeedback(ctx, "wi-1", "art-1", "quench", true, "test message", "hash-v1")
	if err != nil {
		t.Fatalf("AddFeedback: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty feedback ID")
	}

	// Retrieve and verify via the production GetFeedback surface.
	f := feedbackByID(t, ctx, s, id)
	if !f.CanWontFix {
		t.Fatal("expected CanWontFix=true")
	}
}

func TestAddFeedback_CanWontFixDefault(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Add feedback with canWontFix=false (the default).
	id, err := s.AddFeedback(ctx, "wi-1", "art-1", "quench", false, "test message", "hash-v1")
	if err != nil {
		t.Fatalf("AddFeedback: %v", err)
	}

	f := feedbackByID(t, ctx, s, id)
	if f.CanWontFix {
		t.Fatal("expected CanWontFix=false")
	}
}

// ---------------------------------------------------------------------------
// ResolveStaleFeedback tests
// ---------------------------------------------------------------------------

func TestResolveStaleFeedback_ResolvesOlderVersion(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Create feedback on version A (canWontFix=false).
	idA, err := s.AddFeedback(ctx, "wi-1", "art-1", "quench", false, "old version msg", "hash-v1")
	if err != nil {
		t.Fatalf("AddFeedback v1: %v", err)
	}

	// Create feedback on version B (current head — should NOT be resolved).
	_, err = s.AddFeedback(ctx, "wi-1", "art-1", "quench", false, "current version msg", "hash-v2")
	if err != nil {
		t.Fatalf("AddFeedback v2: %v", err)
	}

	// Resolve stale feedback for new head hash-v2.
	n, err := s.ResolveStaleFeedback(ctx, "wi-1", "art-1", "hash-v2")
	if err != nil {
		t.Fatalf("ResolveStaleFeedback: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 resolved item, got %d", n)
	}

	// Verify old feedback is now RESOLVED.
	fA := feedbackByID(t, ctx, s, idA)
	if fA.State != 6 {
		t.Fatalf("expected state=RESOLVED(6), got %d", fA.State)
	}

	// Verify event was appended.
	events, err := s.GetFeedbackEvents(ctx, idA)
	if err != nil {
		t.Fatalf("GetFeedbackEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events (created + resolved), got %d", len(events))
	}
	if events[1].Action != "resolved" {
		t.Fatalf("expected action 'resolved', got %q", events[1].Action)
	}
	if events[1].Actor != "archivist" {
		t.Fatalf("expected actor 'archivist', got %q", events[1].Actor)
	}
}

func TestResolveStaleFeedback_SkipsCanWontFixTrue(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Add feedback with canWontFix=true on an older version.
	id, err := s.AddFeedback(ctx, "wi-1", "art-1", "appraisal", true, "subjective review", "hash-v1")
	if err != nil {
		t.Fatalf("AddFeedback: %v", err)
	}

	// Resolve stale feedback for new head hash-v2.
	n, err := s.ResolveStaleFeedback(ctx, "wi-1", "art-1", "hash-v2")
	if err != nil {
		t.Fatalf("ResolveStaleFeedback: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 resolved items (canWontFix=true skipped), got %d", n)
	}

	// Verify feedback is still NEW.
	f := feedbackByID(t, ctx, s, id)
	if f.State != 1 {
		t.Fatalf("expected state=NEW(1), got %d", f.State)
	}
}

func TestResolveStaleFeedback_SkipsTerminalStates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Add feedback with canWontFix=false on old version.
	id, err := s.AddFeedback(ctx, "wi-1", "art-1", "quench", false, "old msg", "hash-v1")
	if err != nil {
		t.Fatalf("AddFeedback: %v", err)
	}
	id2, err := s.AddFeedback(ctx, "wi-1", "art-1", "quench", false, "old msg 2", "hash-v1")
	if err != nil {
		t.Fatalf("AddFeedback: %v", err)
	}

	// Manually set one to RESOLVED (state=6) and one to DEADLOCKED (state=5).
	_, err = s.db.ExecContext(ctx,
		`UPDATE feedback_items SET state = 6 WHERE id = ?`, id)
	if err != nil {
		t.Fatalf("update state to RESOLVED: %v", err)
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE feedback_items SET state = 5 WHERE id = ?`, id2)
	if err != nil {
		t.Fatalf("update state to DEADLOCKED: %v", err)
	}

	// ResolveStaleFeedback should skip both (terminal states).
	n, err := s.ResolveStaleFeedback(ctx, "wi-1", "art-1", "hash-v2")
	if err != nil {
		t.Fatalf("ResolveStaleFeedback: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 resolved (already RESOLVED+DEADLOCKED), got %d", n)
	}
}

func TestResolveStaleFeedback_SkipsLinkedRuling(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Add feedback with canWontFix=false and a linked ruling.
	id, err := s.AddFeedback(ctx, "wi-1", "art-1", "quench", false, "old msg", "hash-v1")
	if err != nil {
		t.Fatalf("AddFeedback: %v", err)
	}

	// Set linked_ruling to simulate judiciary outcome.
	_, err = s.db.ExecContext(ctx,
		`UPDATE feedback_items SET linked_ruling = 'law-001' WHERE id = ?`, id)
	if err != nil {
		t.Fatalf("update linked_ruling: %v", err)
	}

	// ResolveStaleFeedback should skip it (has linked ruling).
	n, err := s.ResolveStaleFeedback(ctx, "wi-1", "art-1", "hash-v2")
	if err != nil {
		t.Fatalf("ResolveStaleFeedback: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 resolved (linked ruling), got %d", n)
	}
}
