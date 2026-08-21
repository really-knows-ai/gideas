package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
// proxy tests reach fakes deterministically with no real network.
type peerDialer func(ctx context.Context, addr string) (*grpc.ClientConn, error)

// errShardUnavailable is returned when no living shard can serve a proxy op.
// Mirrors the SDK's ErrShardUnavailable semantics but is a local sentinel so
// the queue-service surfaces it as 503 QUEUE_UNAVAILABLE.
var errShardUnavailable = fmt.Errorf("queue unavailable: no living shard")

// peerProxy broadcasts QueuePeerService write/read operations to the living
// shards of a queue. The authoritative liveness view is the Queue CR (R-B5): a
// shard is living iff it is present in .status.shards[], its phase is not
// "evicted", and its lastHeartbeatAt is within the lease TTL. Per-item write
// serialization (the in-flight guard) lives on the Registry, which is SHARED
// across the per-request peerProxy instances created by rest.go/itemgrpc.go.
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

// fetch fetches the Queue CR and returns its living shard entries.
// Returns errShardUnavailable if the queue has no living shards at all.
func (p *peerProxy) fetch(ctx context.Context, queueName string) ([]v1.QueueShardStatus, error) {
	q := &v1.Queue{}
	if err := p.reg.client.Get(ctx, p.reg.key(queueName), q); err != nil {
		if isNotFound(err) {
			return nil, errQueueItemNotFound
		}
		return nil, fmt.Errorf("get queue %s: %w", queueName, err)
	}
	var livingShards []v1.QueueShardStatus
	for _, s := range q.Status.Shards {
		if p.living(s) {
			livingShards = append(livingShards, s)
		}
	}
	if len(livingShards) == 0 {
		return nil, errShardUnavailable
	}
	return livingShards, nil
}

// errQueueItemNotFound is returned when no living shard holds the item across
// a scatter-gather read (every living mirror queried and none has it).
var errQueueItemNotFound = fmt.Errorf("queue item not found")

// errQueueItemAlreadyClaimed / errQueueItemInvalidState mirror the store's
// sentinels (surfaced by a peer shard as gRPC AlreadyExists / FailedPrecondition);
// the REST frontend maps them to HTTP 409.
var (
	errQueueItemAlreadyClaimed = fmt.Errorf("queue item already claimed")
	errQueueItemInvalidState   = fmt.Errorf("queue item invalid state transition")
)

// quorumFor returns the quorum ack threshold: ⌊n/2⌋+1 of the full living-shard
// set (denominator n = the queue's full living-shard count from the CR, per the
// authored tests). A shard that fails mid-write simply does not count as
// confirmed and is repaired later by the sweep.
func quorumFor(n int) int {
	return n/2 + 1
}

// mintGenerationID returns a time-ordered parking-event ID: fixed-width hex
// UnixNano prefix (16 hex digits) + 32-hex crypto/rand suffix. Fixed width ⇒
// lexicographic order == creation order, so the R-3.6 "max generation wins"
// dedupe is a deterministic creation-order proxy. Same machinery as the SDK's
// newGenerationID.
func mintGenerationID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand never fails on supported platforms; fall back to a zero
		// suffix so the caller still gets a valid, time-ordered ID.
		return fmt.Sprintf("%016x-00000000000000000000000000000000", time.Now().UnixNano())
	}
	return fmt.Sprintf("%016x-%s", time.Now().UnixNano(), hex.EncodeToString(b))
}

// pickShardID chooses one of the living shards at random to be the owner
// (R-3.5). Tests assert the owner is one of the living shards, never which one.
func pickShardID(shards []v1.QueueShardStatus) string {
	n := len(shards)
	if n == 0 {
		return ""
	}
	var b [1]byte
	if _, err := rand.Read(b[:]); err != nil {
		return shards[0].ShardID
	}
	return shards[int(b[0])%n].ShardID
}

// enqueueBroadcast applies a broadcast write (R-3.1/3.2/3.5): mints the
// generation + owner shard_id at random, fans ApplyItem out to EVERY living
// shard, acks after ⌊n/2⌋+1 of the full living set confirm (a shard failing
// mid-write simply does not count as confirmed). Returns the applied item (with
// owner + choices) or errShardUnavailable if quorum is not met.
func (p *peerProxy) enqueueBroadcast(
	ctx context.Context, queueName string, item *flowv1.QueueItem,
) (*flowv1.QueueItem, error) {
	shards, err := p.fetch(ctx, queueName)
	if err != nil {
		return nil, err
	}
	n := len(shards)

	// Construct the applied item fresh rather than copying the caller's struct
	// (a generated protobuf value carries an internal Mutex, which a struct copy
	// would shamelessly duplicate). The proto getters are nil-safe.
	applied := &flowv1.QueueItem{
		WorkitemId:   item.GetWorkitemId(),
		Choices:      item.GetChoices(),
		GenerationId: mintGenerationID(),
		ShardId:      pickShardID(shards),
		QueueName:    queueName,
		EnqueuedAt:   time.Now().UTC().Format(time.RFC3339),
		Status:       "waiting",
	}

	confirmed := 0
	for _, s := range shards {
		c, err := p.dial(ctx, s.Addr)
		if err != nil {
			slog.Warn("queue-service: enqueue dial failed", "addr", s.Addr, "error", err)
			continue
		}
		if _, err := c.ApplyItem(ctx, &flowv1.ApplyItemRequest{Item: applied}); err != nil {
			slog.Warn("queue-service: enqueue apply failed", "addr", s.Addr, "error", err)
			continue
		}
		// F2: tag the confirming shard as the per-queue freshest mirror — the
		// last confirmer wins. All shards carry identical generation-guarded
		// data, so any confirmer is authoritative (no updated_at arbitration).
		p.reg.recordFreshest(queueName, s.ShardID)
		confirmed++
	}
	if confirmed < quorumFor(n) {
		return nil, errShardUnavailable
	}
	return applied, nil
}

// claimBroadcast broadcasts ClaimItem (waiting→claimed CAS) to every living
// shard, quorum-acked. A shard confirming the claim counts toward quorum; a
// shard answering AlreadyExists (already claimed) or failing mid-write does
// not. If any shard confirms and quorum is met, the claimed item is returned.
// If NO shard confirms and every shard answered AlreadyExists, the item is
// already claimed everywhere → errQueueItemAlreadyClaimed. Absent everywhere →
// errQueueItemNotFound; otherwise quorum not met → errShardUnavailable.
func (p *peerProxy) claimBroadcast(ctx context.Context, queueName, workitemID string) (*flowv1.QueueItem, error) {
	shards, err := p.fetch(ctx, queueName)
	if err != nil {
		return nil, err
	}
	n := len(shards)

	confirmed := 0
	already := 0
	notFound := 0
	var won *flowv1.QueueItem
	for _, s := range shards {
		c, err := p.dial(ctx, s.Addr)
		if err != nil {
			continue
		}
		resp, err := c.ClaimItem(ctx, &flowv1.ClaimItemRequest{WorkitemId: workitemID})
		if err != nil {
			switch {
			case isGRPCAlreadyExists(err):
				already++
			case isGRPCNotFound(err):
				notFound++
			default:
				// unavailable or other: silent failure, repaired by the sweep
			}
			continue
		}
		confirmed++
		if won == nil && resp.GetItem() != nil {
			won = resp.GetItem()
		}
	}
	if confirmed >= quorumFor(n) {
		return won, nil
	}
	if confirmed == 0 && already == n {
		return nil, errQueueItemAlreadyClaimed
	}
	if confirmed == 0 && notFound == n {
		return nil, errQueueItemNotFound
	}
	return nil, errShardUnavailable
}

// releaseBroadcast broadcasts ReleaseItem (claimed→waiting CAS) to every living
// shard, quorum-acked. A peer FailedPrecondition means the item was not claimed
// — i.e. it is already in the target waiting state — so it counts as an
// idempotent confirmation (release is a no-op success on an already-released
// item). Absent everywhere → errQueueItemNotFound; quorum not met →
// errShardUnavailable.
func (p *peerProxy) releaseBroadcast(ctx context.Context, queueName, workitemID string) (*flowv1.QueueItem, error) {
	shards, err := p.fetch(ctx, queueName)
	if err != nil {
		return nil, err
	}
	n := len(shards)

	confirmed := 0
	alreadyReleased := 0
	notFound := 0
	var released *flowv1.QueueItem
	for _, s := range shards {
		c, err := p.dial(ctx, s.Addr)
		if err != nil {
			continue
		}
		resp, err := c.ReleaseItem(ctx, &flowv1.ReleaseItemRequest{WorkitemId: workitemID})
		if err != nil {
			switch {
			case isGRPCFailedPrecondition(err):
				alreadyReleased++
			case isGRPCNotFound(err):
				notFound++
			default:
				// unavailable or other: silent failure
			}
			continue
		}
		confirmed++
		if released == nil && resp.GetItem() != nil {
			released = resp.GetItem()
		}
	}
	if confirmed+alreadyReleased >= quorumFor(n) {
		if released == nil {
			released = &flowv1.QueueItem{WorkitemId: workitemID, Status: "waiting"}
		}
		return released, nil
	}
	if confirmed+alreadyReleased == 0 && notFound == n {
		return nil, errQueueItemNotFound
	}
	return nil, errShardUnavailable
}

// decideBroadcast fans DecideItem (row delete on every shard) to every living
// shard, quorum-acked. First it ensures the item is claimable: a broadcast
// ClaimItem transitions a waiting item to claimed (an AlreadyExists — already
// claimed — is fine and counts, since it is decidable); a NotFound means no
// such item. Then the DecideItem fan-out deletes it. The cancel contract is
// decide with an empty choice (matches how the SDK expresses a cancellation).
// Returns nil (ack) on quorum of decided shards, errQueueItemNotFound if absent
// everywhere, errQueueItemInvalidState if every shard rejects the transition,
// else errShardUnavailable.
//
// ponytail: the ensure-claim step is required so a decide on an unclaimed item
// (the "cancel" and REST decide paths) reaches the claimable state and removes
// it; it tolerates an AlreadyExists claim, so an already-claimed item is
// decided normally. If a future pass decides a truly external claimer's item,
// the upgrade path is a claim-with-owner check.
func (p *peerProxy) decideBroadcast(ctx context.Context, queueName, workitemID, choice string) error {
	shards, err := p.fetch(ctx, queueName)
	if err != nil {
		return err
	}
	n := len(shards)

	// Ensure-claim: bring the item to the decidable (claimed) state.
	claimed := 0
	notFoundClaim := 0
	for _, s := range shards {
		c, err := p.dial(ctx, s.Addr)
		if err != nil {
			continue
		}
		_, err = c.ClaimItem(ctx, &flowv1.ClaimItemRequest{WorkitemId: workitemID})
		switch {
		case err == nil, isGRPCAlreadyExists(err):
			claimed++
		case isGRPCNotFound(err):
			notFoundClaim++
		default:
			// unavailable or other: silent failure
		}
	}
	if claimed == 0 && notFoundClaim == n {
		return errQueueItemNotFound
	}

	confirmed := 0
	notFound := 0
	invalid := 0
	for _, s := range shards {
		c, err := p.dial(ctx, s.Addr)
		if err != nil {
			continue
		}
		_, err = c.DecideItem(ctx, &flowv1.DecideItemRequest{WorkitemId: workitemID, Choice: choice})
		if err == nil {
			confirmed++
			continue
		}
		switch {
		case isGRPCNotFound(err):
			notFound++
		case isGRPCFailedPrecondition(err):
			invalid++
		default:
			// unavailable or other: silent failure
		}
	}
	if confirmed >= quorumFor(n) {
		return nil
	}
	if confirmed == 0 && notFound == n {
		return errQueueItemNotFound
	}
	if confirmed == 0 && invalid == n {
		return errQueueItemInvalidState
	}
	return errShardUnavailable
}

// listItemsDeduped scatter-gathers GetLocalQueue from every living shard and
// dedupes by workitem_id: max generation wins (deterministic lexicographic),
// tie-broken by the owner copy using served_by_shard_id (R-3.6). A living shard
// that fails to answer (dial or GetLocalQueue error) is unverifiable → the read
// cannot be trusted → errShardUnavailable (SPEC proxy-write error row).
func (p *peerProxy) listItemsDeduped(ctx context.Context, queueName, filter string) ([]*flowv1.QueueItem, error) {
	shards, err := p.fetch(ctx, queueName)
	if err != nil {
		return nil, err
	}

	best := map[string]*flowv1.QueueItem{}
	bestOwner := map[string]bool{} // whether the winning copy is its owner
	for _, s := range shards {
		c, err := p.dial(ctx, s.Addr)
		if err != nil {
			return nil, errShardUnavailable
		}
		resp, err := c.GetLocalQueue(ctx, &flowv1.GetLocalQueueRequest{Status: filter})
		if err != nil {
			return nil, errShardUnavailable
		}
		served := resp.GetServedByShardId()
		for _, it := range resp.GetItems() {
			id := it.GetWorkitemId()
			cur, ok := best[id]
			candOwner := it.GetShardId() == served
			// Store the response item pointer directly (a generation's Copy of
			// a protobuf struct would duplicate its internal Mutex). Items are
			// never mutated, so sharing the read-only pointer is safe.
			if !ok {
				best[id] = it
				bestOwner[id] = candOwner
				continue
			}
			if it.GetGenerationId() > cur.GetGenerationId() ||
				(it.GetGenerationId() == cur.GetGenerationId() && candOwner && !bestOwner[id]) {
				best[id] = it
				bestOwner[id] = candOwner
			}
		}
	}

	out := make([]*flowv1.QueueItem, 0, len(best))
	for _, it := range best {
		out = append(out, it)
	}
	return out, nil
}

// getItemDeduped returns a single item via the scatter-gather + dedupe of
// listItemsDeduped, filtered to workitemID. errQueueItemNotFound if no living
// shard holds it (all living shards answered); errShardUnavailable if a living
// shard fails mid-read.
func (p *peerProxy) getItemDeduped(ctx context.Context, queueName, workitemID string) (*flowv1.QueueItem, error) {
	items, err := p.listItemsDeduped(ctx, queueName, "")
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if it.GetWorkitemId() == workitemID {
			return it, nil
		}
	}
	return nil, errQueueItemNotFound
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
	if isGRPCAlreadyExists(err) {
		return errQueueItemAlreadyClaimed
	}
	if isGRPCFailedPrecondition(err) {
		return errQueueItemInvalidState
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

// isGRPCAlreadyExists reports whether the error is a gRPC AlreadyExists status
// (the peer shard's ClaimItem maps ErrQueueItemAlreadyClaimed to it).
func isGRPCAlreadyExists(err error) bool {
	return status.Code(err) == codes.AlreadyExists
}

// isGRPCFailedPrecondition reports whether the error is a gRPC
// FailedPrecondition status (the peer shard's DecideItem/ClaimItem maps
// ErrQueueItemInvalidState to it).
func isGRPCFailedPrecondition(err error) bool {
	return status.Code(err) == codes.FailedPrecondition
}
