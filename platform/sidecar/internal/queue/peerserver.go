package queue

import (
	"context"
	"errors"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PeerServer is the sidecar's QueuePeerService gRPC server: the passive
// mirror's gRPC surface. It serves the local dumb-mirror store and applies
// broadcast writes from the queue-service's serialized funnel.
//
// ReplicateItem and NotifyShardDead are NOT implemented — the single-backup
// machinery they served is gone. Because PeerServer embeds
// flowv1.UnimplementedQueuePeerServiceServer, calling either legacy RPC returns
// Unimplemented by default.
type PeerServer struct {
	flowv1.UnimplementedQueuePeerServiceServer
	store     Store
	shardID   string
	queueName string
}

// NewPeerServer builds the peer server over the given mirror store. shardID is
// the sidecar's own random per-boot id, echoed as the served_by_shard_id
// tie-break tag on local-queue reads.
func NewPeerServer(store Store, shardID, queueName string) *PeerServer {
	return &PeerServer{store: store, shardID: shardID, queueName: queueName}
}

// GetLocalQueue returns ALL rows stored on this shard regardless of shard_id —
// the dumb-mirror read (R-1.1/R-3.6). The served_by_shard_id tag lets the
// queue-service's scatter-gather dedupe break ties by which shard served each
// copy.
func (p *PeerServer) GetLocalQueue(
	ctx context.Context, req *flowv1.GetLocalQueueRequest,
) (*flowv1.GetLocalQueueResponse, error) {
	items, total, err := p.store.GetLocalQueue(ctx, mapFilter(req))
	if err != nil {
		return nil, storeErrorToGRPC(err)
	}
	protoItems := make([]*flowv1.QueueItem, 0, len(items))
	for _, item := range items {
		protoItems = append(protoItems, toWire(item))
	}
	return &flowv1.GetLocalQueueResponse{
		Items:           protoItems,
		Total:           int32(total),
		Limit:           req.GetLimit(),
		Offset:          req.GetOffset(),
		ServedByShardId: p.shardID,
	}, nil
}

// ClaimItem claims a waiting item (broadcast CAS).
func (p *PeerServer) ClaimItem(
	ctx context.Context, req *flowv1.ClaimItemRequest,
) (*flowv1.ClaimItemResponse, error) {
	item, err := p.store.Claim(ctx, req.GetWorkitemId())
	if err != nil {
		return nil, storeErrorToGRPC(err)
	}
	return &flowv1.ClaimItemResponse{Item: toWire(*item)}, nil
}

// ReleaseItem releases a claimed item back to waiting (broadcast CAS).
func (p *PeerServer) ReleaseItem(
	ctx context.Context, req *flowv1.ReleaseItemRequest,
) (*flowv1.ReleaseItemResponse, error) {
	item, err := p.store.Release(ctx, req.GetWorkitemId())
	if err != nil {
		return nil, storeErrorToGRPC(err)
	}
	return &flowv1.ReleaseItemResponse{Item: toWire(*item)}, nil
}

// DecideItem deletes a claimed item (decision made) from the local store.
func (p *PeerServer) DecideItem(
	ctx context.Context, req *flowv1.DecideItemRequest,
) (*flowv1.DecideItemResponse, error) {
	if err := p.store.Decide(ctx, req.GetWorkitemId(), req.GetChoice()); err != nil {
		return nil, storeErrorToGRPC(err)
	}
	return &flowv1.DecideItemResponse{Acknowledged: true}, nil
}

// ApplyItem applies a broadcast write (R-3.3): generation-guarded
// insert/update. A re-delivered or older generation is a no-op.
func (p *PeerServer) ApplyItem(
	ctx context.Context, req *flowv1.ApplyItemRequest,
) (*flowv1.ApplyItemResponse, error) {
	if req.GetItem() == nil {
		return nil, status.Error(codes.InvalidArgument, "missing item")
	}
	if err := p.store.ApplyItem(ctx, fromWire(req.GetItem())); err != nil {
		return nil, storeErrorToGRPC(err)
	}
	return &flowv1.ApplyItemResponse{Acknowledged: true}, nil
}

// DropItem drops a copy generation-guarded (R-3.3/R-C5): a missed/raced drop
// (missing row or generation mismatch) is NotFound and never destroys a newer
// parking event's copy.
func (p *PeerServer) DropItem(
	ctx context.Context, req *flowv1.DropItemRequest,
) (*flowv1.DropItemResponse, error) {
	if err := p.store.DropItem(ctx, req.GetWorkitemId(), req.GetGenerationId()); err != nil {
		return nil, storeErrorToGRPC(err)
	}
	return &flowv1.DropItemResponse{Acknowledged: true}, nil
}

// mapFilter maps a wire GetLocalQueueRequest filter to the internal QueueFilter.
func mapFilter(req *flowv1.GetLocalQueueRequest) QueueFilter {
	filter := QueueFilter{
		Limit:  int(req.GetLimit()),
		Offset: int(req.GetOffset()),
	}
	if req.GetStatus() != "" {
		st := QueueStatus(req.GetStatus())
		filter.Status = &st
	}
	return filter
}

// storeErrorToGRPC maps the store's sentinel errors to stable gRPC codes so the
// queue-service's client can map them back (SDK contract parity).
func storeErrorToGRPC(err error) error {
	switch {
	case errors.Is(err, ErrQueueItemNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ErrQueueItemAlreadyClaimed):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, ErrQueueItemInvalidState):
		return status.Error(codes.FailedPrecondition, err.Error())
	default:
		return status.Errorf(codes.Internal, "queue store: %v", err)
	}
}

// --- Proto conversion helpers ---

// toWire maps an internal QueueItem to the wire flowv1.QueueItem.
func toWire(item QueueItem) *flowv1.QueueItem {
	pi := &flowv1.QueueItem{
		WorkitemId:   item.WorkitemID,
		ShardId:      item.ShardID,
		QueueName:    item.QueueName,
		Status:       string(item.Status),
		EnqueuedAt:   item.EnqueuedAt.Format(time.RFC3339),
		GenerationId: item.Generation,
		Choices:      item.Choices,
	}
	if item.ClaimedAt != nil {
		pi.ClaimedAt = item.ClaimedAt.Format(time.RFC3339)
	}
	return pi
}

// fromWire maps a wire flowv1.QueueItem to the internal QueueItem.
func fromWire(pi *flowv1.QueueItem) QueueItem {
	item := QueueItem{
		WorkitemID: pi.GetWorkitemId(),
		ShardID:    pi.GetShardId(),
		QueueName:  pi.GetQueueName(),
		Status:     QueueStatus(pi.GetStatus()),
		Generation: pi.GetGenerationId(),
		Choices:    pi.GetChoices(),
	}
	if pi.GetEnqueuedAt() != "" {
		item.EnqueuedAt, _ = time.Parse(time.RFC3339, pi.GetEnqueuedAt())
	}
	if pi.GetClaimedAt() != "" {
		t, _ := time.Parse(time.RFC3339, pi.GetClaimedAt())
		item.ClaimedAt = &t
	}
	return item
}
