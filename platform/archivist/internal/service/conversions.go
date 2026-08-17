// Package service implements the Archivist gRPC server.
package service

import (
	"github.com/foundry/flow/archivist/internal/store/sqlite"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// feedbackRecordToProto converts a store FeedbackRecord to a proto FeedbackItem.
// Note: history is not populated here; use GetFeedback for full history.
func feedbackRecordToProto(r *sqlite.FeedbackRecord) *flowv1.FeedbackItem {
	if r == nil {
		return nil
	}
	return &flowv1.FeedbackItem{
		Id:           r.ID,
		Source:       r.Source,
		CanWontFix:   r.CanWontFix,
		State:        flowv1.FeedbackState(r.State),
		Message:      r.Message,
		LinkedRuling: r.LinkedRuling,
		VersionHash:  r.VersionHash,
		CreatedAt:    timestamppb.New(r.CreatedAt),
	}
}
