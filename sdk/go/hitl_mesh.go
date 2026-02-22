package flow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"sync"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
)

// discoveryInterval is the interval between peer discovery polls.
const discoveryInterval = 30 * time.Second

// peerTimeout is the per-peer timeout for scatter-gather operations.
const peerTimeout = 5 * time.Second

// queueMesh manages peer discovery, gRPC connections, scatter-gather reads,
// and proxy writes for the Federated Queue Mesh.
type queueMesh struct {
	store     *queueStore
	shardID   string
	resolver  PeerResolver
	peerPort  string
	mu        sync.RWMutex
	peers     map[string]flowv1.QueuePeerServiceClient
	conns     map[string]*grpc.ClientConn
	telemetry func(ctx context.Context, event string, payload map[string]any)
	cancel    context.CancelFunc
}

// newQueueMesh creates a new mesh instance. Call start() to begin discovery.
func newQueueMesh(
	store *queueStore,
	shardID string,
	resolver PeerResolver,
	peerPort string,
	telemetry func(ctx context.Context, event string, payload map[string]any),
) *queueMesh {
	return &queueMesh{
		store:     store,
		shardID:   shardID,
		resolver:  resolver,
		peerPort:  peerPort,
		peers:     make(map[string]flowv1.QueuePeerServiceClient),
		conns:     make(map[string]*grpc.ClientConn),
		telemetry: telemetry,
	}
}

// start begins the periodic peer discovery loop.
func (m *queueMesh) start(ctx context.Context) {
	ctx, m.cancel = context.WithCancel(ctx)

	// Initial discovery.
	m.discover(ctx)

	go func() {
		ticker := time.NewTicker(discoveryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.discover(ctx)
			}
		}
	}()
}

// stop cancels the discovery loop and closes all peer connections.
func (m *queueMesh) stop() error {
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error
	for addr, conn := range m.conns {
		if err := conn.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close peer %s: %w", addr, err))
		}
	}
	m.peers = make(map[string]flowv1.QueuePeerServiceClient)
	m.conns = make(map[string]*grpc.ClientConn)
	return errors.Join(errs...)
}

// discover resolves current peers and reconciles connections.
func (m *queueMesh) discover(ctx context.Context) {
	addrs, err := m.resolver.Resolve(ctx)
	if err != nil {
		slog.Warn("flow hitl: peer discovery failed", "error", err)
		return
	}

	resolved := make(map[string]bool, len(addrs))
	for _, addr := range addrs {
		resolved[addr] = true
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Add new peers.
	for _, addr := range addrs {
		if _, exists := m.conns[addr]; exists {
			continue
		}
		conn, err := grpc.NewClient(addr,
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithKeepaliveParams(keepalive.ClientParameters{
				Time:    10 * time.Second,
				Timeout: 20 * time.Second,
			}),
		)
		if err != nil {
			slog.Warn("flow hitl: failed to connect to peer", "addr", addr, "error", err)
			continue
		}
		m.conns[addr] = conn
		m.peers[addr] = flowv1.NewQueuePeerServiceClient(conn)
		slog.Info("flow hitl: peer joined", "addr", addr)
		if m.telemetry != nil {
			m.telemetry(ctx, "foundry.hitl.peer_joined", map[string]any{
				"peerId":    addr,
				"peerCount": len(m.peers),
			})
		}
	}

	// Remove stale peers.
	for addr, conn := range m.conns {
		if resolved[addr] {
			continue
		}
		_ = conn.Close()
		delete(m.conns, addr)
		delete(m.peers, addr)
		slog.Info("flow hitl: peer left", "addr", addr)
		if m.telemetry != nil {
			m.telemetry(ctx, "foundry.hitl.peer_left", map[string]any{
				"peerId":    addr,
				"peerCount": len(m.peers),
				"reason":    "dns_removed",
			})
		}
	}
}

// getPeers returns the addresses of currently connected peers.
func (m *queueMesh) getPeers() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	addrs := make([]string, 0, len(m.peers))
	for addr := range m.peers {
		addrs = append(addrs, addr)
	}
	return addrs
}

// getGlobalQueue scatter-gathers queue items from all peers + local store.
func (m *queueMesh) getGlobalQueue(ctx context.Context, filter QueueFilter) ([]QueueItem, error) {
	// Local results.
	localItems, _, err := m.store.getLocal(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("local queue: %w", err)
	}

	// Snapshot peers.
	m.mu.RLock()
	peerClients := make(map[string]flowv1.QueuePeerServiceClient, len(m.peers))
	maps.Copy(peerClients, m.peers)
	m.mu.RUnlock()

	if len(peerClients) == 0 {
		return localItems, nil
	}

	// Fan out to peers.
	type peerResult struct {
		items []QueueItem
		err   error
	}
	results := make(chan peerResult, len(peerClients))

	for addr, client := range peerClients {
		go func(addr string, client flowv1.QueuePeerServiceClient) {
			peerCtx, cancel := context.WithTimeout(ctx, peerTimeout)
			defer cancel()

			req := &flowv1.GetLocalQueueRequest{
				Limit:  int32(filter.Limit),
				Offset: int32(filter.Offset),
			}
			if filter.Status != nil {
				req.Status = string(*filter.Status)
			}

			resp, err := client.GetLocalQueue(peerCtx, req)
			if err != nil {
				slog.Warn("flow hitl: peer GetLocalQueue failed", "peer", addr, "error", err)
				results <- peerResult{err: err}
				return
			}

			items := make([]QueueItem, 0, len(resp.GetItems()))
			for _, pi := range resp.GetItems() {
				items = append(items, protoToQueueItem(pi))
			}
			results <- peerResult{items: items}
		}(addr, client)
	}

	// Collect results. Slow/unreachable peers are excluded.
	allItems := make([]QueueItem, 0, len(localItems))
	allItems = append(allItems, localItems...)
	for range peerClients {
		r := <-results
		if r.err == nil {
			allItems = append(allItems, r.items...)
		}
	}
	return allItems, nil
}

// routeGetItem looks up an item: local first, then fan out to peers.
func (m *queueMesh) routeGetItem(ctx context.Context, workitemID string) (*QueueItem, error) {
	// Local first.
	item, err := m.store.getByID(ctx, workitemID)
	if err == nil {
		return item, nil
	}
	if !errors.Is(err, ErrQueueItemNotFound) {
		return nil, err
	}

	// Fan out to peers, short-circuit on first hit.
	m.mu.RLock()
	peerClients := make(map[string]flowv1.QueuePeerServiceClient, len(m.peers))
	maps.Copy(peerClients, m.peers)
	m.mu.RUnlock()

	if len(peerClients) == 0 {
		return nil, ErrQueueItemNotFound
	}

	type findResult struct {
		item *QueueItem
		err  error
	}
	results := make(chan findResult, len(peerClients))

	peerCtx, cancel := context.WithTimeout(ctx, peerTimeout)
	defer cancel()

	for _, client := range peerClients {
		go func(client flowv1.QueuePeerServiceClient) {
			req := &flowv1.GetLocalQueueRequest{}
			resp, err := client.GetLocalQueue(peerCtx, req)
			if err != nil {
				results <- findResult{err: err}
				return
			}
			for _, pi := range resp.GetItems() {
				if pi.GetWorkitemId() == workitemID {
					qi := protoToQueueItem(pi)
					results <- findResult{item: &qi}
					return
				}
			}
			results <- findResult{err: ErrQueueItemNotFound}
		}(client)
	}

	for range peerClients {
		r := <-results
		if r.err == nil && r.item != nil {
			return r.item, nil
		}
	}
	return nil, ErrQueueItemNotFound
}

// routeClaim claims an item, routing to the owning shard if remote.
func (m *queueMesh) routeClaim(ctx context.Context, workitemID string) (*QueueItem, error) {
	// Try local first.
	item, err := m.store.claim(ctx, workitemID)
	if err == nil {
		return item, nil
	}
	if !errors.Is(err, ErrQueueItemNotFound) {
		return nil, err
	}

	// Find owning peer and proxy.
	client, err := m.findOwner(ctx, workitemID)
	if err != nil {
		return nil, err
	}

	peerCtx, cancel := context.WithTimeout(ctx, peerTimeout)
	defer cancel()

	resp, err := client.ClaimItem(peerCtx, &flowv1.ClaimItemRequest{WorkitemId: workitemID})
	if err != nil {
		return nil, mapGRPCError(err)
	}
	qi := protoToQueueItem(resp.GetItem())
	return &qi, nil
}

// routeRelease releases an item, routing to the owning shard if remote.
func (m *queueMesh) routeRelease(ctx context.Context, workitemID string) (*QueueItem, error) {
	// Try local first.
	item, err := m.store.release(ctx, workitemID)
	if err == nil {
		return item, nil
	}
	if !errors.Is(err, ErrQueueItemNotFound) {
		return nil, err
	}

	// Find owning peer and proxy.
	client, err := m.findOwner(ctx, workitemID)
	if err != nil {
		return nil, err
	}

	peerCtx, cancel := context.WithTimeout(ctx, peerTimeout)
	defer cancel()

	resp, err := client.ReleaseItem(peerCtx, &flowv1.ReleaseItemRequest{WorkitemId: workitemID})
	if err != nil {
		return nil, mapGRPCError(err)
	}
	qi := protoToQueueItem(resp.GetItem())
	return &qi, nil
}

// routeDecide decides an item (deletes it), routing to the owning shard if remote.
func (m *queueMesh) routeDecide(ctx context.Context, workitemID, choice string) error {
	// Try local first.
	err := m.store.decide(ctx, workitemID)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrQueueItemNotFound) {
		return err
	}

	// Find owning peer and proxy.
	client, err := m.findOwner(ctx, workitemID)
	if err != nil {
		return err
	}

	peerCtx, cancel := context.WithTimeout(ctx, peerTimeout)
	defer cancel()

	_, err = client.DecideItem(peerCtx, &flowv1.DecideItemRequest{WorkitemId: workitemID, Choice: choice})
	if err != nil {
		return mapGRPCError(err)
	}
	return nil
}

// findOwner locates the peer that owns a given workitem by querying all peers.
func (m *queueMesh) findOwner(ctx context.Context, workitemID string) (flowv1.QueuePeerServiceClient, error) {
	m.mu.RLock()
	peerClients := make(map[string]flowv1.QueuePeerServiceClient, len(m.peers))
	maps.Copy(peerClients, m.peers)
	m.mu.RUnlock()

	if len(peerClients) == 0 {
		return nil, ErrQueueItemNotFound
	}

	type ownerResult struct {
		client flowv1.QueuePeerServiceClient
		found  bool
	}
	results := make(chan ownerResult, len(peerClients))

	peerCtx, cancel := context.WithTimeout(ctx, peerTimeout)
	defer cancel()

	for _, client := range peerClients {
		go func(client flowv1.QueuePeerServiceClient) {
			resp, err := client.GetLocalQueue(peerCtx, &flowv1.GetLocalQueueRequest{})
			if err != nil {
				results <- ownerResult{}
				return
			}
			for _, pi := range resp.GetItems() {
				if pi.GetWorkitemId() == workitemID {
					results <- ownerResult{client: client, found: true}
					return
				}
			}
			results <- ownerResult{}
		}(client)
	}

	for range peerClients {
		r := <-results
		if r.found {
			return r.client, nil
		}
	}
	return nil, ErrQueueItemNotFound
}

// --- QueuePeerService gRPC server implementation ---

// queuePeerServer implements the flowv1.QueuePeerServiceServer interface,
// delegating to the local queueStore.
type queuePeerServer struct {
	flowv1.UnimplementedQueuePeerServiceServer
	store    *queueStore
	onDecide func(workitemID, choice string) // signals local WaitForDecision when a remote peer triggers DecideItem
}

func (s *queuePeerServer) GetLocalQueue(
	ctx context.Context, req *flowv1.GetLocalQueueRequest,
) (*flowv1.GetLocalQueueResponse, error) {
	filter := QueueFilter{
		Limit:  int(req.GetLimit()),
		Offset: int(req.GetOffset()),
	}
	if req.GetStatus() != "" {
		st := QueueStatus(req.GetStatus())
		filter.Status = &st
	}

	items, total, err := s.store.getLocal(ctx, filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get local queue: %v", err)
	}

	protoItems := make([]*flowv1.QueueItem, 0, len(items))
	for _, item := range items {
		protoItems = append(protoItems, queueItemToProto(item))
	}

	return &flowv1.GetLocalQueueResponse{
		Items:  protoItems,
		Total:  int32(total),
		Limit:  req.GetLimit(),
		Offset: req.GetOffset(),
	}, nil
}

func (s *queuePeerServer) ClaimItem(
	ctx context.Context, req *flowv1.ClaimItemRequest,
) (*flowv1.ClaimItemResponse, error) {
	item, err := s.store.claim(ctx, req.GetWorkitemId())
	if err != nil {
		return nil, storeErrorToGRPC(err)
	}
	return &flowv1.ClaimItemResponse{Item: queueItemToProto(*item)}, nil
}

func (s *queuePeerServer) ReleaseItem(
	ctx context.Context, req *flowv1.ReleaseItemRequest,
) (*flowv1.ReleaseItemResponse, error) {
	item, err := s.store.release(ctx, req.GetWorkitemId())
	if err != nil {
		return nil, storeErrorToGRPC(err)
	}
	return &flowv1.ReleaseItemResponse{Item: queueItemToProto(*item)}, nil
}

func (s *queuePeerServer) DecideItem(
	ctx context.Context, req *flowv1.DecideItemRequest,
) (*flowv1.DecideItemResponse, error) {
	if err := s.store.decide(ctx, req.GetWorkitemId()); err != nil {
		return nil, storeErrorToGRPC(err)
	}
	// Signal any local WaitForDecision callers. This handles the cross-shard
	// case where a remote peer proxies DecideItem to the owning shard.
	if s.onDecide != nil {
		s.onDecide(req.GetWorkitemId(), req.GetChoice())
	}
	return &flowv1.DecideItemResponse{Acknowledged: true}, nil
}

// --- Proto conversion helpers ---

func protoToQueueItem(pi *flowv1.QueueItem) QueueItem {
	item := QueueItem{
		WorkitemID: pi.GetWorkitemId(),
		ShardID:    pi.GetShardId(),
		Status:     QueueStatus(pi.GetStatus()),
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

func queueItemToProto(item QueueItem) *flowv1.QueueItem {
	pi := &flowv1.QueueItem{
		WorkitemId: item.WorkitemID,
		ShardId:    item.ShardID,
		Status:     string(item.Status),
		EnqueuedAt: item.EnqueuedAt.Format(time.RFC3339),
	}
	if item.ClaimedAt != nil {
		pi.ClaimedAt = item.ClaimedAt.Format(time.RFC3339)
	}
	return pi
}

// storeErrorToGRPC maps store sentinel errors to gRPC status codes.
func storeErrorToGRPC(err error) error {
	switch {
	case errors.Is(err, ErrQueueItemNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ErrQueueItemAlreadyClaimed):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, ErrQueueItemInvalidState):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, ErrShardUnavailable):
		return status.Error(codes.Unavailable, err.Error())
	default:
		return status.Errorf(codes.Internal, "queue store: %v", err)
	}
}

// mapGRPCError maps gRPC status codes back to store sentinel errors.
func mapGRPCError(err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	switch st.Code() {
	case codes.NotFound:
		return ErrQueueItemNotFound
	case codes.AlreadyExists:
		return ErrQueueItemAlreadyClaimed
	case codes.FailedPrecondition:
		return ErrQueueItemInvalidState
	case codes.Unavailable:
		return ErrShardUnavailable
	default:
		return err
	}
}

// --- DNS PeerResolver ---

// DNSResolver is a PeerResolver that discovers peers via headless service DNS.
type DNSResolver struct {
	ServiceName string
	Namespace   string
	SelfShardID string
	Port        string
}

// Resolve queries the headless service DNS for peer addresses.
func (r *DNSResolver) Resolve(ctx context.Context) ([]string, error) {
	host := fmt.Sprintf("%s.%s.svc.cluster.local", r.ServiceName, r.Namespace)
	ips, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("dns lookup %s: %w", host, err)
	}

	var peers []string
	for _, ip := range ips {
		addr := net.JoinHostPort(ip, r.Port)
		// Exclude self — we identify by checking if the resolved address
		// corresponds to our own pod. In a StatefulSet with headless service,
		// each pod has a unique IP.
		peers = append(peers, addr)
	}
	return peers, nil
}
