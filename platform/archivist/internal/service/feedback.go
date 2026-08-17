// Package service implements the Archivist gRPC server.
package service

import (
	"context"
	"errors"
	"log/slog"

	"github.com/foundry/flow/archivist/internal/store/sqlite"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Feedback state constants matching the proto enum values.
const (
	feedbackStateNew      int32 = 1 // FEEDBACK_STATE_NEW
	feedbackStateActioned int32 = 2 // FEEDBACK_STATE_ACTIONED
	feedbackStateWontFix  int32 = 3 // FEEDBACK_STATE_WONT_FIX
	feedbackStateRejected int32 = 4 // FEEDBACK_STATE_REJECTED
	feedbackStateResolved int32 = 6 // FEEDBACK_STATE_RESOLVED
)

// AddFeedback creates a new feedback item in NEW state.
func (s *ArchivistServer) AddFeedback(
	ctx context.Context, req *flowv1.AddFeedbackRequest,
) (*flowv1.AddFeedbackResponse, error) {
	// Capability gate: WRITE:feedback/new.
	if err := checkCapability(ctx, "WRITE:feedback/new"); err != nil {
		return nil, err
	}

	workitemID := req.GetWorkitemId()
	artefactID := req.GetArtefactId()
	source := extractNodeID(ctx)

	slog.Info("AddFeedback",
		"workitem_id", workitemID,
		"artefact_id", artefactID,
		"can_wont_fix", req.GetCanWontFix(),
		"source", source,
	)

	// Resolve version_hash: use provided one or fall back to head.
	versionHash := req.GetVersionHash()
	if versionHash == "" {
		head, err := s.store.GetHead(ctx, workitemID, artefactID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "get head: %v", err)
		}
		if head != nil {
			versionHash = head.Hash
		}
	}

	feedbackID, err := s.store.AddFeedback(
		ctx, workitemID, artefactID, source,
		req.GetCanWontFix(), req.GetMessage(), versionHash,
	)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "add feedback: %v", err)
	}

	s.publishAudit(ctx, "audit.artefact.feedback.add", map[string]string{
		"action":      "add",
		"resource_id": feedbackID,
		"workitem_id": workitemID,
		"artefact_id": artefactID,
	})

	return &flowv1.AddFeedbackResponse{FeedbackId: feedbackID}, nil
}

// GetFeedback returns all feedback items for an artefact.
func (s *ArchivistServer) GetFeedback(
	ctx context.Context, req *flowv1.GetFeedbackRequest,
) (*flowv1.GetFeedbackResponse, error) {
	// Capability gate: READ:feedback.
	if err := checkCapability(ctx, "READ:feedback"); err != nil {
		return nil, err
	}

	workitemID := req.GetWorkitemId()
	artefactID := req.GetArtefactId()

	records, err := s.store.GetFeedback(ctx, workitemID, artefactID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get feedback: %v", err)
	}

	items := make([]*flowv1.FeedbackItem, 0, len(records))
	for _, r := range records {
		events, err := s.store.GetFeedbackEvents(ctx, r.ID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "get feedback events: %v", err)
		}

		protoEvents := make([]*flowv1.FeedbackEvent, 0, len(events))
		for _, e := range events {
			protoEvents = append(protoEvents, &flowv1.FeedbackEvent{
				Actor:     e.Actor,
				Action:    e.Action,
				Message:   e.Message,
				Timestamp: timestamppb.New(e.CreatedAt),
			})
		}

		items = append(items, &flowv1.FeedbackItem{
			Id:           r.ID,
			Source:       r.Source,
			CanWontFix:   r.CanWontFix,
			State:        flowv1.FeedbackState(r.State),
			Message:      r.Message,
			LinkedRuling: r.LinkedRuling,
			VersionHash:  r.VersionHash,
			History:      protoEvents,
			CreatedAt:    timestamppb.New(r.CreatedAt),
		})
	}

	return &flowv1.GetFeedbackResponse{FeedbackItems: items}, nil
}

// HasUnresolvedFeedback returns true if any feedback for the artefact is
// not in RESOLVED state.
func (s *ArchivistServer) HasUnresolvedFeedback(
	ctx context.Context, req *flowv1.HasUnresolvedFeedbackRequest,
) (*flowv1.HasUnresolvedFeedbackResponse, error) {
	// Capability gate: READ:feedback.
	if err := checkCapability(ctx, "READ:feedback"); err != nil {
		return nil, err
	}

	has, err := s.store.HasUnresolvedFeedback(ctx, req.GetWorkitemId(), req.GetArtefactId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "has unresolved feedback: %v", err)
	}
	return &flowv1.HasUnresolvedFeedbackResponse{HasUnresolved: has}, nil
}

// ResolveFeedback transitions feedback from NEW or REJECTED to ACTIONED.
func (s *ArchivistServer) ResolveFeedback(
	ctx context.Context, req *flowv1.ResolveFeedbackRequest,
) (*flowv1.ResolveFeedbackResponse, error) {
	// Capability gate: WRITE:feedback/actioned.
	if err := checkCapability(ctx, "WRITE:feedback/actioned"); err != nil {
		return nil, err
	}

	actor := extractNodeID(ctx)

	record, err := s.store.TransitionFeedback(ctx, req.GetFeedbackId(),
		[]int32{feedbackStateNew, feedbackStateRejected},
		feedbackStateActioned,
		actor, "actioned", req.GetMessage(),
	)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "resolve feedback: %v", err)
	}

	s.publishAudit(ctx, "audit.artefact.feedback.resolve", map[string]string{
		"action":      "resolve",
		"resource_id": req.GetFeedbackId(),
	})

	return &flowv1.ResolveFeedbackResponse{
		UpdatedItem: feedbackRecordToProto(record),
	}, nil
}

// RefuseFeedback transitions feedback from NEW or REJECTED to WONT_FIX.
func (s *ArchivistServer) RefuseFeedback(
	ctx context.Context, req *flowv1.RefuseFeedbackRequest,
) (*flowv1.RefuseFeedbackResponse, error) {
	// Capability gate: WRITE:feedback/wont_fix.
	if err := checkCapability(ctx, "WRITE:feedback/wont_fix"); err != nil {
		return nil, err
	}

	actor := extractNodeID(ctx)

	record, err := s.store.TransitionFeedback(ctx, req.GetFeedbackId(),
		[]int32{feedbackStateNew, feedbackStateRejected},
		feedbackStateWontFix,
		actor, "refused", "",
	)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "refuse feedback: %v", err)
	}

	s.publishAudit(ctx, "audit.artefact.feedback.refuse", map[string]string{
		"action":      "refuse",
		"resource_id": req.GetFeedbackId(),
	})

	return &flowv1.RefuseFeedbackResponse{
		UpdatedItem: feedbackRecordToProto(record),
	}, nil
}

// AcceptFix transitions feedback from ACTIONED to RESOLVED.
func (s *ArchivistServer) AcceptFix(
	ctx context.Context, req *flowv1.AcceptFixRequest,
) (*flowv1.AcceptFixResponse, error) {
	// Capability gate: WRITE:feedback/resolved.
	if err := checkCapability(ctx, "WRITE:feedback/resolved"); err != nil {
		return nil, err
	}

	actor := extractNodeID(ctx)

	record, err := s.store.TransitionFeedback(ctx, req.GetFeedbackId(),
		[]int32{feedbackStateActioned},
		feedbackStateResolved,
		actor, "accepted_fix", "",
	)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "accept fix: %v", err)
	}

	s.publishAudit(ctx, "audit.artefact.feedback.accept", map[string]string{
		"action":      "accept",
		"resource_id": req.GetFeedbackId(),
	})

	return &flowv1.AcceptFixResponse{
		UpdatedItem: feedbackRecordToProto(record),
	}, nil
}

// RejectFix transitions feedback from ACTIONED to REJECTED.
func (s *ArchivistServer) RejectFix(
	ctx context.Context, req *flowv1.RejectFixRequest,
) (*flowv1.RejectFixResponse, error) {
	// Capability gate: WRITE:feedback/rejected.
	if err := checkCapability(ctx, "WRITE:feedback/rejected"); err != nil {
		return nil, err
	}

	actor := extractNodeID(ctx)

	record, err := s.store.TransitionFeedback(ctx, req.GetFeedbackId(),
		[]int32{feedbackStateActioned},
		feedbackStateRejected,
		actor, "rejected_fix", req.GetMessage(),
	)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "reject fix: %v", err)
	}

	s.publishAudit(ctx, "audit.artefact.feedback.reject", map[string]string{
		"action":      "reject",
		"resource_id": req.GetFeedbackId(),
	})

	return &flowv1.RejectFixResponse{
		UpdatedItem: feedbackRecordToProto(record),
	}, nil
}

// AcceptRefusal transitions feedback from WONT_FIX to RESOLVED.
func (s *ArchivistServer) AcceptRefusal(
	ctx context.Context, req *flowv1.AcceptRefusalRequest,
) (*flowv1.AcceptRefusalResponse, error) {
	// Capability gate: WRITE:feedback/resolved.
	if err := checkCapability(ctx, "WRITE:feedback/resolved"); err != nil {
		return nil, err
	}

	actor := extractNodeID(ctx)

	record, err := s.store.TransitionFeedback(ctx, req.GetFeedbackId(),
		[]int32{feedbackStateWontFix},
		feedbackStateResolved,
		actor, "accepted_refusal", "",
	)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "accept refusal: %v", err)
	}

	s.publishAudit(ctx, "audit.artefact.feedback.accept", map[string]string{
		"action":      "accept",
		"resource_id": req.GetFeedbackId(),
	})

	return &flowv1.AcceptRefusalResponse{
		UpdatedItem: feedbackRecordToProto(record),
	}, nil
}

// RejectRefusal transitions feedback from WONT_FIX to REJECTED.
func (s *ArchivistServer) RejectRefusal(
	ctx context.Context, req *flowv1.RejectRefusalRequest,
) (*flowv1.RejectRefusalResponse, error) {
	// Capability gate: WRITE:feedback/rejected.
	if err := checkCapability(ctx, "WRITE:feedback/rejected"); err != nil {
		return nil, err
	}

	actor := extractNodeID(ctx)

	record, err := s.store.TransitionFeedback(ctx, req.GetFeedbackId(),
		[]int32{feedbackStateWontFix},
		feedbackStateRejected,
		actor, "rejected_refusal", req.GetMessage(),
	)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "reject refusal: %v", err)
	}

	s.publishAudit(ctx, "audit.artefact.feedback.reject", map[string]string{
		"action":      "reject",
		"resource_id": req.GetFeedbackId(),
	})

	return &flowv1.RejectRefusalResponse{
		UpdatedItem: feedbackRecordToProto(record),
	}, nil
}

// GetFeedbackDepth returns the number of events in a feedback item's history.
func (s *ArchivistServer) GetFeedbackDepth(
	ctx context.Context, req *flowv1.GetFeedbackDepthRequest,
) (*flowv1.GetFeedbackDepthResponse, error) {
	// Capability gate: READ:feedback.
	if err := checkCapability(ctx, "READ:feedback"); err != nil {
		return nil, err
	}

	depth, err := s.store.GetFeedbackDepth(ctx, req.GetFeedbackId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get feedback depth: %v", err)
	}
	return &flowv1.GetFeedbackDepthResponse{Depth: depth}, nil
}

// DeadlockFeedback transitions feedback from any non-resolved, non-deadlocked
// state to DEADLOCKED. The gate node calls this when feedback depth exceeds
// the configured threshold.
func (s *ArchivistServer) DeadlockFeedback(
	ctx context.Context, req *flowv1.DeadlockFeedbackRequest,
) (*flowv1.DeadlockFeedbackResponse, error) {
	// Capability gate: WRITE:feedback/deadlocked.
	if err := checkCapability(ctx, "WRITE:feedback/deadlocked"); err != nil {
		return nil, err
	}

	actor := extractNodeID(ctx)

	record, err := s.store.TransitionFeedback(ctx, req.GetFeedbackId(),
		[]int32{
			feedbackStateNew, feedbackStateActioned,
			feedbackStateWontFix, feedbackStateRejected,
		},
		5, // FEEDBACK_STATE_DEADLOCKED
		actor, "deadlocked", "",
	)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "deadlock feedback: %v", err)
	}

	s.publishAudit(ctx, "audit.artefact.feedback.deadlock", map[string]string{
		"action":      "deadlock",
		"resource_id": req.GetFeedbackId(),
	})

	return &flowv1.DeadlockFeedbackResponse{
		UpdatedItem: feedbackRecordToProto(record),
	}, nil
}

// LinkRuling atomically links a judiciary ruling to a deadlocked feedback
// item, transitioning it to the specified terminal state and enabling the
// contempt guard. The feedback must be in DEADLOCKED state and must not
// already have a linked ruling. The target_state must be WONT_FIX or REJECTED.
func (s *ArchivistServer) LinkRuling(
	ctx context.Context, req *flowv1.LinkRulingRequest,
) (*flowv1.LinkRulingResponse, error) {
	// Capability gate: WRITE:feedback/link-ruling.
	if err := checkCapability(ctx, "WRITE:feedback/link-ruling"); err != nil {
		return nil, err
	}

	feedbackID := req.GetFeedbackId()
	lawID := req.GetLawId()
	targetState := req.GetTargetState()

	slog.Info("LinkRuling",
		"workitem_id", req.GetWorkitemId(),
		"feedback_id", feedbackID,
		"law_id", lawID,
		"target_state", targetState.String(),
	)

	if feedbackID == "" {
		return nil, status.Errorf(codes.InvalidArgument, "feedback_id is required")
	}
	if lawID == "" {
		return nil, status.Errorf(codes.InvalidArgument, "law_id is required")
	}
	if targetState == flowv1.FeedbackState_FEEDBACK_STATE_UNSPECIFIED {
		return nil, status.Errorf(codes.InvalidArgument, "target_state is required (must be WONT_FIX or REJECTED)")
	}

	record, err := s.store.LinkRuling(ctx, feedbackID, lawID, int32(targetState))
	if err != nil {
		switch {
		case errors.Is(err, sqlite.ErrFeedbackNotFound):
			return nil, status.Errorf(codes.NotFound, "link ruling: %v", err)
		case errors.Is(err, sqlite.ErrFeedbackNotDeadlocked),
			errors.Is(err, sqlite.ErrContemptGuard):
			return nil, status.Errorf(codes.FailedPrecondition, "link ruling: %v", err)
		default:
			return nil, status.Errorf(codes.Internal, "link ruling: %v", err)
		}
	}

	s.publishAudit(ctx, "audit.artefact.feedback.link-ruling", map[string]string{
		"action":      "link-ruling",
		"resource_id": feedbackID,
		"law_id":      lawID,
	})

	return &flowv1.LinkRulingResponse{
		UpdatedItem: feedbackRecordToProto(record),
	}, nil
}
