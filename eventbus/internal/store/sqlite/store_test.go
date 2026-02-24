package sqlite

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New(:memory:): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestInsertAndGetSince(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	evt := &Event{
		ID:         "evt-1",
		Channel:    "telemetry",
		EventType:  "friction",
		FlowID:     "flow-1",
		NodeID:     "node-1",
		WorkitemID: "wi-1",
		Timestamp:  time.Now().UTC().Truncate(time.Second),
		TraceID:    "trace-1",
		Attributes: map[string]string{"magnitude": "3.5"},
		Payload:    []byte("hello"),
		Labels: []Label{
			{Key: "law_id", Value: "law-1"},
			{Key: "law_id", Value: "law-2"},
		},
	}

	seq, err := s.Insert(ctx, evt)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if seq != 1 {
		t.Fatalf("expected sequence 1, got %d", seq)
	}
	if evt.Sequence != 1 {
		t.Fatalf("expected evt.Sequence=1, got %d", evt.Sequence)
	}

	events, err := s.GetSince(ctx, "telemetry", 0, 100)
	if err != nil {
		t.Fatalf("GetSince: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	got := events[0]
	if got.ID != "evt-1" {
		t.Errorf("ID = %q, want %q", got.ID, "evt-1")
	}
	if got.Sequence != 1 {
		t.Errorf("Sequence = %d, want 1", got.Sequence)
	}
	if got.EventType != "friction" {
		t.Errorf("EventType = %q, want %q", got.EventType, "friction")
	}
	if got.Attributes["magnitude"] != "3.5" {
		t.Errorf("Attributes[magnitude] = %q, want %q", got.Attributes["magnitude"], "3.5")
	}
	if string(got.Payload) != "hello" {
		t.Errorf("Payload = %q, want %q", got.Payload, "hello")
	}

	// Verify labels were stored and retrieved.
	if len(got.Labels) != 2 {
		t.Fatalf("Labels count = %d, want 2", len(got.Labels))
	}
	if got.Labels[0].Key != "law_id" || got.Labels[0].Value != "law-1" {
		t.Errorf("Labels[0] = %+v, want {law_id, law-1}", got.Labels[0])
	}
	if got.Labels[1].Key != "law_id" || got.Labels[1].Value != "law-2" {
		t.Errorf("Labels[1] = %+v, want {law_id, law-2}", got.Labels[1])
	}
}

func TestInsertWithoutLabels(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	evt := &Event{
		ID:        "evt-no-labels",
		Channel:   "audit",
		EventType: "audit.test",
		Timestamp: time.Now(),
	}

	seq, err := s.Insert(ctx, evt)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if seq != 1 {
		t.Fatalf("expected sequence 1, got %d", seq)
	}

	events, err := s.GetSince(ctx, "audit", 0, 100)
	if err != nil {
		t.Fatalf("GetSince: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if len(events[0].Labels) != 0 {
		t.Errorf("Labels count = %d, want 0", len(events[0].Labels))
	}
}

func TestSequencePerChannel(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Insert into "telemetry" channel twice.
	seq1, _ := s.Insert(ctx, &Event{ID: "a", Channel: "telemetry", EventType: "t", Timestamp: time.Now()})
	seq2, _ := s.Insert(ctx, &Event{ID: "b", Channel: "telemetry", EventType: "t", Timestamp: time.Now()})

	// Insert into "audit" channel once — should start at 1, not 3.
	seq3, _ := s.Insert(ctx, &Event{ID: "c", Channel: "audit", EventType: "t", Timestamp: time.Now()})

	if seq1 != 1 || seq2 != 2 {
		t.Errorf("telemetry sequences: %d, %d; want 1, 2", seq1, seq2)
	}
	if seq3 != 1 {
		t.Errorf("audit first sequence: %d; want 1", seq3)
	}
}

func TestGetSinceRespectsLastSequence(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := range 5 {
		_, _ = s.Insert(ctx, &Event{
			ID: fmt.Sprintf("evt-%d", i), Channel: "telemetry", EventType: "t", Timestamp: time.Now(),
		})
	}

	// Get events after sequence 3 — should return sequences 4, 5.
	events, err := s.GetSince(ctx, "telemetry", 3, 100)
	if err != nil {
		t.Fatalf("GetSince: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Sequence != 4 || events[1].Sequence != 5 {
		t.Errorf("sequences: %d, %d; want 4, 5", events[0].Sequence, events[1].Sequence)
	}
}

func TestMinSequence(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Empty channel.
	min, err := s.MinSequence(ctx, "telemetry")
	if err != nil {
		t.Fatalf("MinSequence: %v", err)
	}
	if min != 0 {
		t.Errorf("empty channel min = %d, want 0", min)
	}

	_, _ = s.Insert(ctx, &Event{ID: "a", Channel: "telemetry", EventType: "t", Timestamp: time.Now()})
	_, _ = s.Insert(ctx, &Event{ID: "b", Channel: "telemetry", EventType: "t", Timestamp: time.Now()})

	min, err = s.MinSequence(ctx, "telemetry")
	if err != nil {
		t.Fatalf("MinSequence: %v", err)
	}
	if min != 1 {
		t.Errorf("min = %d, want 1", min)
	}
}

func TestEvictByAge(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	old := time.Now().Add(-2 * time.Hour)
	recent := time.Now()

	_, _ = s.Insert(ctx, &Event{ID: "old", Channel: "telemetry", EventType: "t", Timestamp: old})
	_, _ = s.Insert(ctx, &Event{ID: "new", Channel: "telemetry", EventType: "t", Timestamp: recent})

	deleted, err := s.Evict(ctx, "telemetry", 1*time.Hour, 0)
	if err != nil {
		t.Fatalf("Evict: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}

	events, _ := s.GetSince(ctx, "telemetry", 0, 100)
	if len(events) != 1 {
		t.Fatalf("expected 1 remaining event, got %d", len(events))
	}
	if events[0].ID != "new" {
		t.Errorf("remaining event ID = %q, want %q", events[0].ID, "new")
	}
}

func TestEvictBySize(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Insert events with known payload sizes.
	for i := range 10 {
		_, _ = s.Insert(ctx, &Event{
			ID:        fmt.Sprintf("evt-%d", i),
			Channel:   "telemetry",
			EventType: "t",
			Timestamp: time.Now(),
			Payload:   make([]byte, 100), // 100 bytes each
		})
	}

	// Evict until under 500 bytes — should keep at most 5 events.
	deleted, err := s.Evict(ctx, "telemetry", 0, 500)
	if err != nil {
		t.Fatalf("Evict: %v", err)
	}
	if deleted < 5 {
		t.Errorf("expected at least 5 deleted, got %d", deleted)
	}

	events, _ := s.GetSince(ctx, "telemetry", 0, 100)
	if len(events) > 5 {
		t.Errorf("expected at most 5 remaining events, got %d", len(events))
	}
}

func TestEvictCascadesLabels(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	old := time.Now().Add(-2 * time.Hour)
	recent := time.Now()

	_, _ = s.Insert(ctx, &Event{
		ID: "old", Channel: "telemetry", EventType: "t", Timestamp: old,
		Labels: []Label{{Key: "law_id", Value: "law-1"}},
	})
	_, _ = s.Insert(ctx, &Event{
		ID: "new", Channel: "telemetry", EventType: "t", Timestamp: recent,
		Labels: []Label{{Key: "law_id", Value: "law-2"}},
	})

	deleted, err := s.Evict(ctx, "telemetry", 1*time.Hour, 0)
	if err != nil {
		t.Fatalf("Evict: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}

	// Verify the remaining event still has its labels and the evicted
	// event's labels are gone.
	events, _ := s.GetSince(ctx, "telemetry", 0, 100)
	if len(events) != 1 {
		t.Fatalf("expected 1 remaining event, got %d", len(events))
	}
	if len(events[0].Labels) != 1 || events[0].Labels[0].Value != "law-2" {
		t.Errorf("remaining labels = %+v, want [{law_id, law-2}]", events[0].Labels)
	}

	// Verify no orphan labels in the labels table.
	var count int
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_labels`).Scan(&count)
	if err != nil {
		t.Fatalf("count labels: %v", err)
	}
	if count != 1 {
		t.Errorf("orphan labels: total label rows = %d, want 1", count)
	}
}

func TestSequenceSurvivesReopen(t *testing.T) {
	// Use a temp file to test persistence across store instances.
	dir := t.TempDir()
	dbPath := dir + "/test.db"

	s1, err := New(dbPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	_, _ = s1.Insert(ctx, &Event{ID: "a", Channel: "telemetry", EventType: "t", Timestamp: time.Now()})
	_, _ = s1.Insert(ctx, &Event{ID: "b", Channel: "telemetry", EventType: "t", Timestamp: time.Now()})
	_ = s1.Close()

	s2, err := New(dbPath)
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	defer func() { _ = s2.Close() }()

	seq, _ := s2.Insert(ctx, &Event{ID: "c", Channel: "telemetry", EventType: "t", Timestamp: time.Now()})
	if seq != 3 {
		t.Errorf("sequence after reopen = %d, want 3", seq)
	}
}

func TestMultipleLabelsPerEvent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Insert events with different label configurations.
	_, _ = s.Insert(ctx, &Event{
		ID: "multi", Channel: "telemetry", EventType: "t", Timestamp: time.Now(),
		Labels: []Label{
			{Key: "law_id", Value: "law-1"},
			{Key: "law_id", Value: "law-2"},
			{Key: "phase", Value: "Running"},
		},
	})
	_, _ = s.Insert(ctx, &Event{
		ID: "single", Channel: "telemetry", EventType: "t", Timestamp: time.Now(),
		Labels: []Label{{Key: "law_id", Value: "law-3"}},
	})

	events, err := s.GetSince(ctx, "telemetry", 0, 100)
	if err != nil {
		t.Fatalf("GetSince: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	// First event should have 3 labels.
	if len(events[0].Labels) != 3 {
		t.Errorf("event[0] labels = %d, want 3", len(events[0].Labels))
	}

	// Second event should have 1 label.
	if len(events[1].Labels) != 1 {
		t.Errorf("event[1] labels = %d, want 1", len(events[1].Labels))
	}
}
