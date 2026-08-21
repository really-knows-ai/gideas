package service

import (
	"context"
	"errors"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CancelQueuedItem cancels a specific queued item for an internal caller. The
// cancel contract IS a decision with an empty choice (matches how the SDK
// expresses a cancellation as an empty-choice decision). Broadcast through the
// write funnel (serialized per item, fan-out to every living shard).
func (r *Registry) CancelQueuedItem(
	ctx context.Context, req *flowv1.CancelQueuedItemRequest,
) (*flowv1.CancelQueuedItemResponse, error) {
	proxy := newPeerProxy(r)
	defer proxy.close()
	unlock := r.lockItem(req.GetWorkitemId())
	defer unlock()
	if err := proxy.decideBroadcast(ctx, req.GetQueueName(), req.GetWorkitemId(), ""); err != nil {
		return nil, toItemGRPCError(err)
	}
	return &flowv1.CancelQueuedItemResponse{Acknowledged: true}, nil
}

// DecideQueuedItem decides a specific queued item for an internal caller.
// Broadcast through the write funnel (serialized per item, fan-out to every
// living shard) with the caller's choice.
func (r *Registry) DecideQueuedItem(
	ctx context.Context, req *flowv1.DecideQueuedItemRequest,
) (*flowv1.DecideQueuedItemResponse, error) {
	proxy := newPeerProxy(r)
	defer proxy.close()
	unlock := r.lockItem(req.GetWorkitemId())
	defer unlock()
	if err := proxy.decideBroadcast(ctx, req.GetQueueName(), req.GetWorkitemId(), req.GetChoice()); err != nil {
		return nil, toItemGRPCError(err)
	}
	r.publishQueueDecided(req.GetQueueName(), req.GetWorkitemId(), req.GetChoice())
	return &flowv1.DecideQueuedItemResponse{Acknowledged: true}, nil
}

// toItemGRPCError maps proxy sentinel errors to gRPC status codes: NotFound for
// a genuinely-absent item, Unavailable for a possibly-dead shard.
func toItemGRPCError(err error) error {
	switch {
	case errors.Is(err, errQueueItemNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, errQueueItemAlreadyClaimed):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, errQueueItemInvalidState):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, errShardUnavailable):
		return status.Error(codes.Unavailable, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
