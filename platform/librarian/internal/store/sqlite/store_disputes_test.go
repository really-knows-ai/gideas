package sqlite

import (
	"context"
	"testing"
)

// ---------------------------------------------------------------------------
// Dispute Record Tests
// ---------------------------------------------------------------------------

func TestCreateDisputeRecord_Basic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rec, err := s.CreateDisputeRecord(ctx, "petition-1", []string{"law-a", "law-b"})
	if err != nil {
		t.Fatalf("CreateDisputeRecord: %v", err)
	}
	if rec.PetitionID != "petition-1" {
		t.Fatalf("expected petition_id %q, got %q", "petition-1", rec.PetitionID)
	}
	if len(rec.CitedLawIDs) != 2 {
		t.Fatalf("expected 2 cited law IDs, got %d", len(rec.CitedLawIDs))
	}
	if rec.Status != DisputeStatusActive {
		t.Fatalf("expected status active, got %q", rec.Status)
	}
	if rec.CreatedAt.IsZero() {
		t.Fatal("expected non-zero created_at")
	}
}

func TestRetireDisputeRecord_Basic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.CreateDisputeRecord(ctx, "petition-2", []string{"law-c"})
	if err != nil {
		t.Fatalf("CreateDisputeRecord: %v", err)
	}

	err = s.RetireDisputeRecord(ctx, "petition-2")
	if err != nil {
		t.Fatalf("RetireDisputeRecord: %v", err)
	}

	// Should no longer appear in active disputes.
	records, err := s.GetActiveDisputes(ctx, "")
	if err != nil {
		t.Fatalf("GetActiveDisputes: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("expected 0 active disputes after retirement, got %d", len(records))
	}
}

func TestGetActiveDisputes_ReturnsOnlyActive(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.CreateDisputeRecord(ctx, "petition-a", []string{"law-1"}); err != nil {
		t.Fatalf("CreateDisputeRecord petition-a: %v", err)
	}
	if _, err := s.CreateDisputeRecord(ctx, "petition-b", []string{"law-2"}); err != nil {
		t.Fatalf("CreateDisputeRecord petition-b: %v", err)
	}
	// Retire one.
	if err := s.RetireDisputeRecord(ctx, "petition-a"); err != nil {
		t.Fatalf("RetireDisputeRecord petition-a: %v", err)
	}

	records, err := s.GetActiveDisputes(ctx, "")
	if err != nil {
		t.Fatalf("GetActiveDisputes: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 active dispute, got %d", len(records))
	}
	if records[0].PetitionID != "petition-b" {
		t.Fatalf("expected petition-b, got %q", records[0].PetitionID)
	}
}

func TestGetActiveDisputes_LawIDFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.CreateDisputeRecord(ctx, "petition-x", []string{"law-10", "law-20"}); err != nil {
		t.Fatalf("CreateDisputeRecord petition-x: %v", err)
	}
	if _, err := s.CreateDisputeRecord(ctx, "petition-y", []string{"law-30"}); err != nil {
		t.Fatalf("CreateDisputeRecord petition-y: %v", err)
	}

	// Filter by law-20 -- should only return petition-x.
	records, err := s.GetActiveDisputes(ctx, "law-20")
	if err != nil {
		t.Fatalf("GetActiveDisputes with filter: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 dispute citing law-20, got %d", len(records))
	}
	if records[0].PetitionID != "petition-x" {
		t.Fatalf("expected petition-x, got %q", records[0].PetitionID)
	}
}

func TestCreateDisputeRecord_DuplicatePetitionID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, err := s.CreateDisputeRecord(ctx, "petition-dup", []string{"law-1"}); err != nil {
		t.Fatalf("first CreateDisputeRecord: %v", err)
	}

	_, err := s.CreateDisputeRecord(ctx, "petition-dup", []string{"law-2"})
	if err == nil {
		t.Fatal("expected error on duplicate petition_id, got nil")
	}
}

func TestRetireDisputeRecord_NonExistent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.RetireDisputeRecord(ctx, "petition-ghost")
	if err == nil {
		t.Fatal("expected error for non-existent petition_id, got nil")
	}
}
