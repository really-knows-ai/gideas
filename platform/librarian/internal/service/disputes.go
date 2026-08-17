package service

import (
	"context"
	"log/slog"
	"strings"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CreateDisputeRecord creates a dispute record linking a petition to the
// laws it cites. Called by law-applicator on the T4-5 path.
func (s *LibrarianServer) CreateDisputeRecord(
	ctx context.Context, req *flowv1.CreateDisputeRecordRequest,
) (*flowv1.CreateDisputeRecordResponse, error) {
	if req.GetPetitionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "petition_id is required")
	}
	if len(req.GetCitedLawIds()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "at least one cited_law_id is required")
	}

	rec, err := s.store.CreateDisputeRecord(ctx, req.GetPetitionId(), req.GetCitedLawIds())
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") ||
			strings.Contains(err.Error(), "PRIMARY KEY") {
			return nil, status.Errorf(codes.AlreadyExists,
				"dispute record already exists for petition %q", req.GetPetitionId())
		}
		slog.Error("CreateDisputeRecord failed", "petition_id", req.GetPetitionId(), "error", err)
		return nil, status.Errorf(codes.Internal, "create dispute record: %v", err)
	}

	slog.Info("CreateDisputeRecord",
		"petition_id", rec.PetitionID,
		"cited_law_ids", rec.CitedLawIDs,
	)

	s.publishAudit("audit.dispute.created", map[string]string{
		"action":        "created",
		"petition_id":   rec.PetitionID,
		"cited_law_ids": strings.Join(rec.CitedLawIDs, ","),
	})

	return &flowv1.CreateDisputeRecordResponse{
		Record: storeDisputeToProto(rec),
	}, nil
}

// RetireDisputeRecord retires a dispute record when a petition outcome
// is resolved. Called by the petition-outcome-watcher.
func (s *LibrarianServer) RetireDisputeRecord(
	ctx context.Context, req *flowv1.RetireDisputeRecordRequest,
) (*flowv1.RetireDisputeRecordResponse, error) {
	if req.GetPetitionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "petition_id is required")
	}

	err := s.store.RetireDisputeRecord(ctx, req.GetPetitionId())
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, status.Errorf(codes.NotFound,
				"dispute record %q not found or already retired", req.GetPetitionId())
		}
		slog.Error("RetireDisputeRecord failed", "petition_id", req.GetPetitionId(), "error", err)
		return nil, status.Errorf(codes.Internal, "retire dispute record: %v", err)
	}

	slog.Info("RetireDisputeRecord", "petition_id", req.GetPetitionId())

	s.publishAudit("audit.dispute.retired", map[string]string{
		"action":      "retired",
		"petition_id": req.GetPetitionId(),
	})

	return &flowv1.RetireDisputeRecordResponse{Acknowledged: true}, nil
}

// GetActiveDisputes returns active dispute records, optionally filtered by
// a cited law ID. Called by Sort to check for pending disputes.
func (s *LibrarianServer) GetActiveDisputes(
	ctx context.Context, req *flowv1.GetActiveDisputesRequest,
) (*flowv1.GetActiveDisputesResponse, error) {
	records, err := s.store.GetActiveDisputes(ctx, req.GetLawId())
	if err != nil {
		slog.Error("GetActiveDisputes failed", "law_id", req.GetLawId(), "error", err)
		return nil, status.Errorf(codes.Internal, "get active disputes: %v", err)
	}

	protoRecords := make([]*flowv1.DisputeRecord, 0, len(records))
	for _, rec := range records {
		protoRecords = append(protoRecords, storeDisputeToProto(rec))
	}

	return &flowv1.GetActiveDisputesResponse{Records: protoRecords}, nil
}
