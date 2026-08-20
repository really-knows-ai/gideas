package service

import (
	"context"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// gatewayErr maps a proxy sentinel (or gRPC) error to the PINNED gateway codes:
// NotFound → codes.NotFound, AlreadyExists → codes.AlreadyExists,
// FailedPrecondition → codes.FailedPrecondition, Unavailable → codes.Unavailable.
// mapPeerError first normalizes any raw gRPC peer error to a sentinel, then the
// sentinel map (toItemGRPCError) pins the code.
func gatewayErr(err error) error {
	return toItemGRPCError(mapPeerError(err))
}

// GatewayServer is the SDK-facing gRPC surface (QueueGatewayService): the
// stable public contract the SDK's thin QueueManager client (PHASE_04) and the
// write funnel (PHASE_03) compile against. It is a SIBLING of the internal
// QueueRegistryService — registration/lease stay internal, this surface is the
// public queue API. Every RPC funnels through the Registry's serialized
// broadcast + scatter-gather dedupe path.
//
// Error mapping contract (pinned in PHASE_01): item not found → NotFound;
// already claimed → AlreadyExists; invalid state → FailedPrecondition; quorum
// not reached → Unavailable.
type GatewayServer struct {
	flowv1.UnimplementedQueueGatewayServiceServer
	reg *Registry
}

// NewGatewayServer constructs a GatewayServer over the shared Registry.
func NewGatewayServer(reg *Registry) *GatewayServer {
	return &GatewayServer{reg: reg}
}

// Enqueue parks a Workitem in the queue with the caller's choices (R-5.2),
// serialized per item, via the funnel's enqueueBroadcast (mirror-everywhere).
func (g *GatewayServer) Enqueue(ctx context.Context, req *flowv1.EnqueueRequest) (*flowv1.EnqueueResponse, error) {
	proxy := newPeerProxy(g.reg)
	defer proxy.close()
	unlock := g.reg.lockItem(req.GetWorkitemId())
	defer unlock()

	item := &flowv1.QueueItem{WorkitemId: req.GetWorkitemId(), Choices: req.GetChoices()}
	if _, err := proxy.enqueueBroadcast(ctx, req.GetQueueName(), item); err != nil {
		return nil, gatewayErr(err)
	}
	return &flowv1.EnqueueResponse{Acknowledged: true}, nil
}

// GetGlobalQueue returns every item in the queue, optionally filtered by
// status, via scatter-gather + dedupe.
func (g *GatewayServer) GetGlobalQueue(
	ctx context.Context, req *flowv1.GetGlobalQueueRequest,
) (*flowv1.GetGlobalQueueResponse, error) {
	proxy := newPeerProxy(g.reg)
	defer proxy.close()
	items, err := proxy.listItemsDeduped(ctx, req.GetQueueName(), req.GetStatus())
	if err != nil {
		return nil, gatewayErr(err)
	}
	return &flowv1.GetGlobalQueueResponse{Items: items}, nil
}

// GetItem returns a single item by queue_name + workitem_id (including its
// choices).
func (g *GatewayServer) GetItem(ctx context.Context, req *flowv1.GetItemRequest) (*flowv1.GetItemResponse, error) {
	proxy := newPeerProxy(g.reg)
	defer proxy.close()
	item, err := proxy.getItemDeduped(ctx, req.GetQueueName(), req.GetWorkitemId())
	if err != nil {
		return nil, gatewayErr(err)
	}
	return &flowv1.GetItemResponse{Item: item}, nil
}

// Claim claims a waiting item (broadcast CAS), serialized per item.
func (g *GatewayServer) Claim(ctx context.Context, req *flowv1.ClaimRequest) (*flowv1.ClaimResponse, error) {
	proxy := newPeerProxy(g.reg)
	defer proxy.close()
	unlock := g.reg.lockItem(req.GetWorkitemId())
	defer unlock()

	item, err := proxy.claimBroadcast(ctx, req.GetQueueName(), req.GetWorkitemId())
	if err != nil {
		return nil, gatewayErr(err)
	}
	return &flowv1.ClaimResponse{Item: item}, nil
}

// Release releases a claimed item back to waiting (broadcast CAS), serialized
// per item.
func (g *GatewayServer) Release(ctx context.Context, req *flowv1.ReleaseRequest) (*flowv1.ReleaseResponse, error) {
	proxy := newPeerProxy(g.reg)
	defer proxy.close()
	unlock := g.reg.lockItem(req.GetWorkitemId())
	defer unlock()

	item, err := proxy.releaseBroadcast(ctx, req.GetQueueName(), req.GetWorkitemId())
	if err != nil {
		return nil, gatewayErr(err)
	}
	return &flowv1.ReleaseResponse{Item: item}, nil
}

// Decide decides a claimed item (removes it from the queue on every shard) and
// records the choice, serialized per item.
func (g *GatewayServer) Decide(ctx context.Context, req *flowv1.DecideRequest) (*flowv1.DecideResponse, error) {
	proxy := newPeerProxy(g.reg)
	defer proxy.close()
	unlock := g.reg.lockItem(req.GetWorkitemId())
	defer unlock()

	if err := proxy.decideBroadcast(ctx, req.GetQueueName(), req.GetWorkitemId(), req.GetChoice()); err != nil {
		return nil, gatewayErr(err)
	}
	return &flowv1.DecideResponse{Acknowledged: true}, nil
}
