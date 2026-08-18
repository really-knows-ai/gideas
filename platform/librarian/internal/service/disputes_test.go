package service

import (
	"context"
	"testing"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Dispute Record RPCs
// ---------------------------------------------------------------------------

func TestCreateDisputeRecord_Basic(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	resp, err := srv.CreateDisputeRecord(ctx, &flowv1.CreateDisputeRecordRequest{
		PetitionId:  "petition-1",
		CitedLawIds: []string{"law-a", "law-b"},
	})
	if err != nil {
		t.Fatalf("CreateDisputeRecord: %v", err)
	}
	rec := resp.GetRecord()
	if rec.GetPetitionId() != "petition-1" {
		t.Fatalf("expected petition_id %q, got %q", "petition-1", rec.GetPetitionId())
	}
	if len(rec.GetCitedLawIds()) != 2 {
		t.Fatalf("expected 2 cited law IDs, got %d", len(rec.GetCitedLawIds()))
	}
	if rec.GetStatus() != flowv1.DisputeStatus_DISPUTE_STATUS_ACTIVE {
		t.Fatalf("expected ACTIVE status, got %v", rec.GetStatus())
	}
	if rec.GetCreatedAt() == nil {
		t.Fatal("expected non-nil created_at")
	}
}

func TestCreateDisputeRecord_EmptyPetitionID(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.CreateDisputeRecord(ctx, &flowv1.CreateDisputeRecordRequest{
		CitedLawIds: []string{"law-a"},
	})
	if err == nil {
		t.Fatal("expected InvalidArgument for empty petition_id")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestCreateDisputeRecord_EmptyCitedLawIDs(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.CreateDisputeRecord(ctx, &flowv1.CreateDisputeRecordRequest{
		PetitionId: "petition-empty",
	})
	if err == nil {
		t.Fatal("expected InvalidArgument for empty cited_law_ids")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestRetireDisputeRecord_Basic(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	// Create first.
	if _, err := srv.CreateDisputeRecord(ctx, &flowv1.CreateDisputeRecordRequest{
		PetitionId:  "petition-retire",
		CitedLawIds: []string{"law-1"},
	}); err != nil {
		t.Fatalf("CreateDisputeRecord: %v", err)
	}

	resp, err := srv.RetireDisputeRecord(ctx, &flowv1.RetireDisputeRecordRequest{
		PetitionId: "petition-retire",
	})
	if err != nil {
		t.Fatalf("RetireDisputeRecord: %v", err)
	}
	if !resp.GetAcknowledged() {
		t.Fatal("expected acknowledged=true")
	}

	// Should no longer appear in active disputes.
	getResp, err := srv.GetActiveDisputes(ctx, &flowv1.GetActiveDisputesRequest{})
	if err != nil {
		t.Fatalf("GetActiveDisputes: %v", err)
	}
	if len(getResp.GetRecords()) != 0 {
		t.Fatalf("expected 0 active disputes after retirement, got %d", len(getResp.GetRecords()))
	}
}

func TestRetireDisputeRecord_NonExistent(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	_, err := srv.RetireDisputeRecord(ctx, &flowv1.RetireDisputeRecordRequest{
		PetitionId: "petition-ghost",
	})
	if err == nil {
		t.Fatal("expected NotFound for non-existent petition")
	}
	if s, ok := status.FromError(err); !ok || s.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

func TestGetActiveDisputes_ReturnsOnlyActive(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	if _, err := srv.CreateDisputeRecord(ctx, &flowv1.CreateDisputeRecordRequest{
		PetitionId:  "petition-a",
		CitedLawIds: []string{"law-1"},
	}); err != nil {
		t.Fatalf("CreateDisputeRecord petition-a: %v", err)
	}
	if _, err := srv.CreateDisputeRecord(ctx, &flowv1.CreateDisputeRecordRequest{
		PetitionId:  "petition-b",
		CitedLawIds: []string{"law-2"},
	}); err != nil {
		t.Fatalf("CreateDisputeRecord petition-b: %v", err)
	}

	// Retire one.
	if _, err := srv.RetireDisputeRecord(ctx, &flowv1.RetireDisputeRecordRequest{
		PetitionId: "petition-a",
	}); err != nil {
		t.Fatalf("RetireDisputeRecord: %v", err)
	}

	resp, err := srv.GetActiveDisputes(ctx, &flowv1.GetActiveDisputesRequest{})
	if err != nil {
		t.Fatalf("GetActiveDisputes: %v", err)
	}
	if len(resp.GetRecords()) != 1 {
		t.Fatalf("expected 1 active dispute, got %d", len(resp.GetRecords()))
	}
	if resp.GetRecords()[0].GetPetitionId() != "petition-b" {
		t.Fatalf("expected petition-b, got %q", resp.GetRecords()[0].GetPetitionId())
	}
}

func TestGetActiveDisputes_WithLawIDFilter(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	if _, err := srv.CreateDisputeRecord(ctx, &flowv1.CreateDisputeRecordRequest{
		PetitionId:  "petition-x",
		CitedLawIds: []string{"law-10", "law-20"},
	}); err != nil {
		t.Fatalf("CreateDisputeRecord petition-x: %v", err)
	}
	if _, err := srv.CreateDisputeRecord(ctx, &flowv1.CreateDisputeRecordRequest{
		PetitionId:  "petition-y",
		CitedLawIds: []string{"law-30"},
	}); err != nil {
		t.Fatalf("CreateDisputeRecord petition-y: %v", err)
	}

	// Filter by law-20 -- should only return petition-x.
	resp, err := srv.GetActiveDisputes(ctx, &flowv1.GetActiveDisputesRequest{
		LawId: "law-20",
	})
	if err != nil {
		t.Fatalf("GetActiveDisputes with filter: %v", err)
	}
	if len(resp.GetRecords()) != 1 {
		t.Fatalf("expected 1 dispute citing law-20, got %d", len(resp.GetRecords()))
	}
	if resp.GetRecords()[0].GetPetitionId() != "petition-x" {
		t.Fatalf("expected petition-x, got %q", resp.GetRecords()[0].GetPetitionId())
	}
}
