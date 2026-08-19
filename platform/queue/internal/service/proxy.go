package service

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	v1 "github.com/foundry/flow/operator/api/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// peerDialer connects to a shard's QueuePeerService. nil ⇒ the production
// dialer; same-package tests inject a bufconn dialer so the REST/item-gRPC
// proxy tests and the lease-sweep NotifyShardDead fan-out reach fakes
// deterministically with no real network.
type peerDialer func(ctx context.Context, addr string) (*grpc.ClientConn, error)

// errShardUnavailable is returned when no living shard can serve a proxy op.
// Mirrors the SDK's ErrShardUnavailable semantics but is a local sentinel so
// the queue-service surfaces it as 503 QUEUE_UNAVAILABLE.
var errShardUnavailable = fmt.Errorf("queue unavailable: no living shard")

// peerProxy routes QueuePeerService operations to the living shards of a
// queue. The authoritative liveness view is the Queue CR (R-B5): a shard is
// living iff it is present in .status.shards[], its phase is not "evicted",
// and its lastHeartbeatAt is within the lease TTL.
type peerProxy struct {
	reg   *Registry
	mu    sync.Mutex
	conns map[string]*grpc.ClientConn
	peers map[string]flowv1.QueuePeerServiceClient
}

func newPeerProxy(reg *Registry) *peerProxy {
	return &peerProxy{
		reg:   reg,
		conns: make(map[string]*grpc.ClientConn),
		peers: make(map[string]flowv1.QueuePeerServiceClient),
	}
}

// close closes cached peer connections.
func (p *peerProxy) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for addr, conn := range p.conns {
		_ = conn.Close()
		delete(p.conns, addr)
		delete(p.peers, addr)
	}
}

// dial obtains (and caches) a QueuePeerService client for a shard addr,
// through r.peerDialer (nil ⇒ production dialer).
func (p *peerProxy) dial(ctx context.Context, addr string) (flowv1.QueuePeerServiceClient, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.peers[addr]; ok {
		return c, nil
	}
	dial := p.reg.peerDialer
	if dial == nil {
		dial = productionPeerDialer
	}
	conn, err := dial(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("dial peer %s: %w", addr, err)
	}
	p.conns[addr] = conn
	c := flowv1.NewQueuePeerServiceClient(conn)
	p.peers[addr] = c
	slog.Info("queue-service: peer conn", "addr", addr)
	return c, nil
}

// productionPeerDialer is the default dialer used when r.peerDialer is nil.
func productionPeerDialer(ctx context.Context, addr string) (*grpc.ClientConn, error) {
	return grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
}

// living returns whether a CR shard entry is living: present, non-evicted, and
// fresh (heartbeat within the lease TTL).
func (p *peerProxy) living(s v1.QueueShardStatus) bool {
	if s.Phase == phaseEvicted {
		return false
	}
	if s.LastHeartbeatAt.IsZero() {
		return false
	}
	return time.Since(s.LastHeartbeatAt.Time) <= p.reg.leaseTTL
}

// fetch fetches the Queue CR and returns it plus its living shard entries.
// Returns errShardUnavailable if the queue has no living shards at all.
func (p *peerProxy) fetch(ctx context.Context, queueName string) (*v1.Queue, []v1.QueueShardStatus, error) {
	q := &v1.Queue{}
	if err := p.reg.client.Get(ctx, p.reg.key(queueName), q); err != nil {
		if isNotFound(err) {
			return nil, nil, errQueueItemNotFound
		}
		return nil, nil, fmt.Errorf("get queue %s: %w", queueName, err)
	}
	var livingShards []v1.QueueShardStatus
	for _, s := range q.Status.Shards {
		if p.living(s) {
			livingShards = append(livingShards, s)
		}
	}
	if len(livingShards) == 0 {
		return q, nil, errShardUnavailable
	}
	return q, livingShards, nil
}

// livingClient returns the living QueuePeerService clients for the queue.
func (p *peerProxy) livingClients(ctx context.Context, queueName string) ([]flowv1.QueuePeerServiceClient, error) {
	_, shards, err := p.fetch(ctx, queueName)
	if err != nil {
		return nil, err
	}
	clients := make([]flowv1.QueuePeerServiceClient, 0, len(shards))
	for _, s := range shards {
		c, err := p.dial(ctx, s.Addr)
		if err != nil {
			slog.Warn("queue-service: peer dial failed", "addr", s.Addr, "error", err)
			continue
		}
		clients = append(clients, c)
	}
	if len(clients) == 0 {
		return nil, errShardUnavailable
	}
	return clients, nil
}

// findOwner locates the living shard owning the workitem by fanning out
// GetLocalQueue over living shards only. Decision rule (pinned): no living
// owner + any non-living CR shard present ⇒ errShardUnavailable (503); no
// living owner + all CR shards living ⇒ errQueueItemNotFound (404).
func (p *peerProxy) findOwner(
	ctx context.Context, queueName, workitemID string,
) (flowv1.QueuePeerServiceClient, error) {
	q, shards, err := p.fetch(ctx, queueName)
	if err != nil {
		return nil, err
	}

	queried := 0
	for _, s := range shards {
		c, err := p.dial(ctx, s.Addr)
		if err != nil {
			slog.Warn("queue-service: peer dial failed", "addr", s.Addr, "error", err)
			continue
		}
		resp, err := c.GetLocalQueue(ctx, &flowv1.GetLocalQueueRequest{})
		if err != nil {
			slog.Warn("queue-service: peer GetLocalQueue failed", "addr", s.Addr, "error", err)
			continue
		}
		queried++
		for _, pi := range resp.GetItems() {
			if pi.GetWorkitemId() == workitemID {
				return c, nil
			}
		}
	}

	// No living shard owns the item. Decision rule (pinned):
	//   - if we could not successfully query ANY living shard (all living
	//     shards unreachable), 503 — we cannot distinguish "item on a dead
	//     shard" from "no such item";
	//   - else if any CR-registered shard is non-living (stale/evicted), 503 —
	//     the item may sit on the dead shard;
	//   - else all CR shards living and none owns it, 404 (genuinely absent).
	if queried == 0 {
		return nil, errShardUnavailable
	}
	if p.hasNonLiving(q) {
		return nil, errShardUnavailable
	}
	return nil, errQueueItemNotFound
}

// hasNonLiving reports whether any CR shard entry of the queue is non-living
// (evicted, or stale beyond the lease TTL). Used to disambiguate the 503/404
// decision rule: a non-living CR shard means the item may sit on a dead shard.
func (p *peerProxy) hasNonLiving(q *v1.Queue) bool {
	for _, s := range q.Status.Shards {
		if !p.living(s) {
			return true
		}
	}
	return false
}

// errQueueItemNotFound is returned when no living shard owns the item and all
// CR shards are living.
var errQueueItemNotFound = fmt.Errorf("queue item not found")

// getItem proxies GetLocalQueue/owner-lookup to return a single item from the
// living owner.
func (p *peerProxy) getItem(ctx context.Context, queueName, workitemID string) (*flowv1.QueueItem, error) {
	owner, err := p.findOwner(ctx, queueName, workitemID)
	if err != nil {
		return nil, err
	}
	resp, err := owner.GetLocalQueue(ctx, &flowv1.GetLocalQueueRequest{})
	if err != nil {
		return nil, fmt.Errorf("get local queue: %w", err)
	}
	for _, pi := range resp.GetItems() {
		if pi.GetWorkitemId() == workitemID {
			return pi, nil
		}
	}
	// Owner reported it does not hold the item between findOwner and here.
	return nil, errQueueItemNotFound
}

// claim proxies ClaimItem to the living owner.
func (p *peerProxy) claim(ctx context.Context, queueName, workitemID string) (*flowv1.QueueItem, error) {
	owner, err := p.findOwner(ctx, queueName, workitemID)
	if err != nil {
		return nil, err
	}
	resp, err := owner.ClaimItem(ctx, &flowv1.ClaimItemRequest{WorkitemId: workitemID})
	if err != nil {
		return nil, mapPeerError(err)
	}
	return resp.GetItem(), nil
}

// release proxies ReleaseItem to the living owner.
func (p *peerProxy) release(ctx context.Context, queueName, workitemID string) (*flowv1.QueueItem, error) {
	owner, err := p.findOwner(ctx, queueName, workitemID)
	if err != nil {
		return nil, err
	}
	resp, err := owner.ReleaseItem(ctx, &flowv1.ReleaseItemRequest{WorkitemId: workitemID})
	if err != nil {
		return nil, mapPeerError(err)
	}
	return resp.GetItem(), nil
}

// decide proxies DecideItem (with the human's choice) to the living owner.
func (p *peerProxy) decide(ctx context.Context, queueName, workitemID, choice string) error {
	owner, err := p.findOwner(ctx, queueName, workitemID)
	if err != nil {
		return err
	}
	_, err = owner.DecideItem(ctx, &flowv1.DecideItemRequest{WorkitemId: workitemID, Choice: choice})
	return mapPeerError(err)
}

// listItems scatter-gathers Item lists from every living shard of the queue.
// Returns the raw aggregate — a workitem_id present on two living shards
// appears twice (per-workitem_id dedupe lands in PHASE_04).
func (p *peerProxy) listItems(ctx context.Context, queueName string) ([]*flowv1.QueueItem, error) {
	clients, err := p.livingClients(ctx, queueName)
	if err != nil {
		return nil, err
	}
	var items []*flowv1.QueueItem
	for _, c := range clients {
		resp, err := c.GetLocalQueue(ctx, &flowv1.GetLocalQueueRequest{})
		if err != nil {
			slog.Warn("queue-service: list items peer failed", "error", err)
			continue
		}
		items = append(items, resp.GetItems()...)
	}
	return items, nil
}

// mapPeerError converts a QueuePeerService gRPC error into a local sentinel.
func mapPeerError(err error) error {
	if err == nil {
		return nil
	}
	if isGRPCUnavailable(err) {
		return errShardUnavailable
	}
	if isGRPCNotFound(err) {
		return errQueueItemNotFound
	}
	return err
}

// isGRPCUnavailable reports whether the error is a gRPC Unavailable status.
func isGRPCUnavailable(err error) bool {
	return status.Code(err) == codes.Unavailable
}

// isGRPCNotFound reports whether the error is a gRPC NotFound status.
func isGRPCNotFound(err error) bool {
	return status.Code(err) == codes.NotFound
}
