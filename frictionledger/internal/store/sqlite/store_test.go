package sqlite

import (
	"context"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("open in-memory store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestAddFriction_Basic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	event := FrictionEvent{
		FlowID:     "flow-1",
		WorkitemID: "wi-1",
		NodeID:     "node-a",
		Magnitude:  10.5,
		Timestamp:  time.Now().UTC(),
	}

	err := s.AddFriction(ctx, "evt-1", event, []string{"law-1", "law-2"})
	if err != nil {
		t.Fatalf("AddFriction: %v", err)
	}

	// Query back — should aggregate to magnitude 10.5, count 1, for each law.
	results, err := s.QueryFriction(ctx, FrictionFilter{})
	if err != nil {
		t.Fatalf("QueryFriction: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 aggregates (one per law), got %d", len(results))
	}
}

func TestAddFriction_NoLaws(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	event := FrictionEvent{
		FlowID:     "flow-1",
		WorkitemID: "wi-1",
		NodeID:     "node-a",
		Magnitude:  5.0,
		Timestamp:  time.Now().UTC(),
	}

	err := s.AddFriction(ctx, "evt-1", event, nil)
	if err != nil {
		t.Fatalf("AddFriction: %v", err)
	}

	results, err := s.QueryFriction(ctx, FrictionFilter{})
	if err != nil {
		t.Fatalf("QueryFriction: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 aggregate, got %d", len(results))
	}
	if results[0].LawID != "" {
		t.Fatalf("expected empty law_id for event without laws, got %q", results[0].LawID)
	}
	if results[0].TotalMagnitude != 5.0 {
		t.Fatalf("expected magnitude 5.0, got %f", results[0].TotalMagnitude)
	}
}

func TestAddFriction_NegativeMagnitude(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	event := FrictionEvent{
		FlowID:     "flow-1",
		WorkitemID: "wi-1",
		NodeID:     "node-a",
		Magnitude:  -1.0,
		Timestamp:  time.Now().UTC(),
	}

	err := s.AddFriction(ctx, "evt-1", event, nil)
	if err == nil {
		t.Fatal("expected error for negative magnitude, got nil")
	}
}

func TestAddFriction_FractionalMagnitude(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	event := FrictionEvent{
		FlowID:     "flow-1",
		WorkitemID: "wi-1",
		NodeID:     "node-a",
		Magnitude:  3.5,
		Timestamp:  time.Now().UTC(),
	}

	err := s.AddFriction(ctx, "evt-1", event, []string{"law-1"})
	if err != nil {
		t.Fatalf("AddFriction: %v", err)
	}

	results, err := s.QueryFriction(ctx, FrictionFilter{LawID: "law-1"})
	if err != nil {
		t.Fatalf("QueryFriction: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 aggregate, got %d", len(results))
	}
	if results[0].TotalMagnitude != 3.5 {
		t.Errorf("expected magnitude 3.5, got %f", results[0].TotalMagnitude)
	}
}

func TestQueryFriction_Aggregation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Insert 3 events: 2 for law-1, 1 for law-2, all from the same node.
	events := []struct {
		id        string
		magnitude float64
		laws      []string
	}{
		{"evt-1", 10.0, []string{"law-1"}},
		{"evt-2", 20.0, []string{"law-1"}},
		{"evt-3", 5.5, []string{"law-2"}},
	}

	for _, e := range events {
		err := s.AddFriction(ctx, e.id, FrictionEvent{
			FlowID:     "flow-1",
			WorkitemID: "wi-1",
			NodeID:     "node-a",
			Magnitude:  e.magnitude,
			Timestamp:  now,
		}, e.laws)
		if err != nil {
			t.Fatalf("AddFriction %s: %v", e.id, err)
		}
	}

	results, err := s.QueryFriction(ctx, FrictionFilter{LawID: "law-1"})
	if err != nil {
		t.Fatalf("QueryFriction: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 aggregate for law-1, got %d", len(results))
	}
	if results[0].TotalMagnitude != 30.0 {
		t.Fatalf("expected total magnitude 30.0 for law-1, got %f", results[0].TotalMagnitude)
	}
	if results[0].EventCount != 2 {
		t.Fatalf("expected event count 2 for law-1, got %d", results[0].EventCount)
	}
}

func TestQueryFriction_MultipleLawsPerEvent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Single event attributed to two laws.
	err := s.AddFriction(ctx, "evt-1", FrictionEvent{
		FlowID:     "flow-1",
		WorkitemID: "wi-1",
		NodeID:     "node-a",
		Magnitude:  15.0,
		Timestamp:  now,
	}, []string{"law-1", "law-2"})
	if err != nil {
		t.Fatalf("AddFriction: %v", err)
	}

	// Query all — should return two aggregates (one per law), each with magnitude 15.
	results, err := s.QueryFriction(ctx, FrictionFilter{})
	if err != nil {
		t.Fatalf("QueryFriction: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 aggregates, got %d", len(results))
	}

	for _, r := range results {
		if r.TotalMagnitude != 15.0 {
			t.Errorf("expected magnitude 15.0 for law %q, got %f", r.LawID, r.TotalMagnitude)
		}
		if r.EventCount != 1 {
			t.Errorf("expected event count 1 for law %q, got %d", r.LawID, r.EventCount)
		}
	}
}

func TestQueryFriction_FilterByNode(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Two events from different nodes.
	if err := s.AddFriction(ctx, "evt-1", FrictionEvent{
		FlowID: "flow-1", WorkitemID: "wi-1", NodeID: "node-a", Magnitude: 10.0, Timestamp: now,
	}, []string{"law-1"}); err != nil {
		t.Fatalf("AddFriction evt-1: %v", err)
	}
	if err := s.AddFriction(ctx, "evt-2", FrictionEvent{
		FlowID: "flow-1", WorkitemID: "wi-1", NodeID: "node-b", Magnitude: 20.0, Timestamp: now,
	}, []string{"law-1"}); err != nil {
		t.Fatalf("AddFriction evt-2: %v", err)
	}

	results, err := s.QueryFriction(ctx, FrictionFilter{NodeID: "node-a"})
	if err != nil {
		t.Fatalf("QueryFriction: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for node-a, got %d", len(results))
	}
	if results[0].TotalMagnitude != 10.0 {
		t.Fatalf("expected magnitude 10.0 for node-a, got %f", results[0].TotalMagnitude)
	}
}

func TestQueryFriction_FilterByTimeRange(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	t1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)

	if err := s.AddFriction(ctx, "evt-1", FrictionEvent{
		FlowID: "flow-1", WorkitemID: "wi-1", NodeID: "node-a", Magnitude: 10.0, Timestamp: t1,
	}, nil); err != nil {
		t.Fatalf("AddFriction evt-1: %v", err)
	}
	if err := s.AddFriction(ctx, "evt-2", FrictionEvent{
		FlowID: "flow-1", WorkitemID: "wi-1", NodeID: "node-a", Magnitude: 20.0, Timestamp: t2,
	}, nil); err != nil {
		t.Fatalf("AddFriction evt-2: %v", err)
	}
	if err := s.AddFriction(ctx, "evt-3", FrictionEvent{
		FlowID: "flow-1", WorkitemID: "wi-1", NodeID: "node-a", Magnitude: 30.0, Timestamp: t3,
	}, nil); err != nil {
		t.Fatalf("AddFriction evt-3: %v", err)
	}

	// Filter for events in the first half of 2025.
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)
	results, err := s.QueryFriction(ctx, FrictionFilter{
		StartTime: &start,
		EndTime:   &end,
	})
	if err != nil {
		t.Fatalf("QueryFriction: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 aggregate, got %d", len(results))
	}
	if results[0].TotalMagnitude != 30.0 {
		t.Fatalf("expected magnitude 30.0 (10+20), got %f", results[0].TotalMagnitude)
	}
	if results[0].EventCount != 2 {
		t.Fatalf("expected event count 2, got %d", results[0].EventCount)
	}
}

func TestQueryFriction_EmptyStore(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	results, err := s.QueryFriction(ctx, FrictionFilter{})
	if err != nil {
		t.Fatalf("QueryFriction on empty store: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results from empty store, got %d", len(results))
	}
}

func TestQueryFriction_TimestampBounds(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	t1 := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC)

	if err := s.AddFriction(ctx, "evt-1", FrictionEvent{
		FlowID: "flow-1", WorkitemID: "wi-1", NodeID: "node-a", Magnitude: 10.0, Timestamp: t1,
	}, []string{"law-1"}); err != nil {
		t.Fatalf("AddFriction evt-1: %v", err)
	}
	if err := s.AddFriction(ctx, "evt-2", FrictionEvent{
		FlowID: "flow-1", WorkitemID: "wi-1", NodeID: "node-a", Magnitude: 20.0, Timestamp: t2,
	}, []string{"law-1"}); err != nil {
		t.Fatalf("AddFriction evt-2: %v", err)
	}

	results, err := s.QueryFriction(ctx, FrictionFilter{})
	if err != nil {
		t.Fatalf("QueryFriction: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 aggregate, got %d", len(results))
	}
	if !results[0].Earliest.Equal(t1) {
		t.Errorf("expected earliest=%v, got %v", t1, results[0].Earliest)
	}
	if !results[0].Latest.Equal(t2) {
		t.Errorf("expected latest=%v, got %v", t2, results[0].Latest)
	}
}

// --- Checkpoint Tests ---

func TestCheckpoint_DefaultZero(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	seq, err := s.GetCheckpoint(ctx, "telemetry")
	if err != nil {
		t.Fatalf("GetCheckpoint: %v", err)
	}
	if seq != 0 {
		t.Fatalf("expected default checkpoint 0, got %d", seq)
	}
}

func TestCheckpoint_SetAndGet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.SetCheckpoint(ctx, "telemetry", 42); err != nil {
		t.Fatalf("SetCheckpoint: %v", err)
	}

	seq, err := s.GetCheckpoint(ctx, "telemetry")
	if err != nil {
		t.Fatalf("GetCheckpoint: %v", err)
	}
	if seq != 42 {
		t.Fatalf("expected checkpoint 42, got %d", seq)
	}
}

func TestCheckpoint_Upsert(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.SetCheckpoint(ctx, "telemetry", 10); err != nil {
		t.Fatalf("SetCheckpoint: %v", err)
	}
	if err := s.SetCheckpoint(ctx, "telemetry", 20); err != nil {
		t.Fatalf("SetCheckpoint update: %v", err)
	}

	seq, err := s.GetCheckpoint(ctx, "telemetry")
	if err != nil {
		t.Fatalf("GetCheckpoint: %v", err)
	}
	if seq != 20 {
		t.Fatalf("expected checkpoint 20 after upsert, got %d", seq)
	}
}

func TestCheckpoint_MultipleChannels(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.SetCheckpoint(ctx, "telemetry", 100); err != nil {
		t.Fatalf("SetCheckpoint telemetry: %v", err)
	}
	if err := s.SetCheckpoint(ctx, "friction", 200); err != nil {
		t.Fatalf("SetCheckpoint friction: %v", err)
	}

	seq1, err := s.GetCheckpoint(ctx, "telemetry")
	if err != nil {
		t.Fatalf("GetCheckpoint telemetry: %v", err)
	}
	seq2, err := s.GetCheckpoint(ctx, "friction")
	if err != nil {
		t.Fatalf("GetCheckpoint friction: %v", err)
	}

	if seq1 != 100 {
		t.Errorf("telemetry checkpoint = %d, want 100", seq1)
	}
	if seq2 != 200 {
		t.Errorf("friction checkpoint = %d, want 200", seq2)
	}
}

// --- QueryFrictionByLaw Tests ---

func TestQueryFrictionByLaw_Basic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := s.AddFriction(ctx, "evt-1", FrictionEvent{
		FlowID: "flow-1", WorkitemID: "wi-1", NodeID: "node-a", Magnitude: 10.0, Timestamp: now,
	}, []string{"law-1"}); err != nil {
		t.Fatalf("AddFriction evt-1: %v", err)
	}
	if err := s.AddFriction(ctx, "evt-2", FrictionEvent{
		FlowID: "flow-1", WorkitemID: "wi-1", NodeID: "node-a", Magnitude: 15.5, Timestamp: now,
	}, []string{"law-1"}); err != nil {
		t.Fatalf("AddFriction evt-2: %v", err)
	}

	total, err := s.QueryFrictionByLaw(ctx, "law-1")
	if err != nil {
		t.Fatalf("QueryFrictionByLaw: %v", err)
	}
	if total != 25.5 {
		t.Errorf("expected total 25.5 for law-1, got %f", total)
	}
}

func TestQueryFrictionByLaw_NoEvents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	total, err := s.QueryFrictionByLaw(ctx, "law-nonexistent")
	if err != nil {
		t.Fatalf("QueryFrictionByLaw: %v", err)
	}
	if total != 0 {
		t.Errorf("expected total 0 for non-existent law, got %f", total)
	}
}
