package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"net"
	"sync"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
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
// proxy writes, and single-backup replication/failover for the Federated
// Queue Mesh.
type queueMesh struct {
	store    *queueStore
	shardID  string
	resolver PeerResolver
	peerPort string
	mu       sync.RWMutex
	peers    map[string]flowv1.QueuePeerServiceClient
	conns    map[string]*grpc.ClientConn
	cancel   context.CancelFunc

	// registry is the mesh's view of this queue's living shards.
	// Production: the SDK's local heartbeat-derived view (the living shard set
	// returned by each HeartbeatQueue response — R-B3), corrected down by
	// NotifyShardDead/deadShards. Tests: a static map. Empty => replication
	// defers.
	registry shardRegistry
	// deadShards records identities known dead (via NotifyShardDead/onShardDead)
	// so backup selection excludes them immediately even if the registry still
	// lists them. Guarded by m.mu.
	deadShards map[string]bool

	// onShardDead is the promotion-path hook: queuePeerServer.NotifyShardDead
	// invokes it (the production entry point answering the PHASE_02-defined /
	// PHASE_03-sent RPC); tests drive it directly. Manager.Start registers
	// handleShardDead on it.
	onShardDead func(shardID string)
}

// Shard couples a shard's identity (e.g. "shard-1") to the addr the mesh dials
// it at (e.g. "bufconn://shard-1" or "10.1.2.3:50053").
type Shard struct{ ID, Addr string }

// shardRegistry is the mesh's view of this queue's living shards
// (identity <-> addr). The SDK never reads the Queue CR directly (no cluster
// access). Empty => replication defers (chooseBackup finds no candidates).
type shardRegistry interface {
	LivingShards() []Shard            // identity<->addr, dead shards excluded
	AddrFor(id string) (string, bool) // identity -> peer-map addr
}

// staticShardRegistry is a map-backed shardRegistry — the test/standalone
// default (newQueueMesh instantiates it). When the queue-service is
// configured, the registry is a heartbeat-derived view filled from the living
// shard set in each HeartbeatQueue response.
type staticShardRegistry struct {
	shards []Shard
	byID   map[string]string
}

// newStaticShardRegistry builds a staticShardRegistry from the given shards.
func newStaticShardRegistry(shards []Shard) *staticShardRegistry {
	r := &staticShardRegistry{byID: make(map[string]string, len(shards))}
	for _, s := range shards {
		r.shards = append(r.shards, s)
		r.byID[s.ID] = s.Addr
	}
	return r
}

func (r *staticShardRegistry) LivingShards() []Shard {
	if r == nil {
		return nil
	}
	out := make([]Shard, len(r.shards))
	copy(out, r.shards)
	return out
}

func (r *staticShardRegistry) AddrFor(id string) (string, bool) {
	if r == nil {
		return "", false
	}
	addr, ok := r.byID[id]
	return addr, ok
}

// heartbeatShardRegistry is the shardRegistry backed by the queue-service's
// HeartbeatQueue response living set (R-B3), refreshed on each beat. It is the
// production mesh registry when FLOW_QUEUE_SERVICE_ADDR is configured; the
// SDK never reads the Queue CR directly (no cluster access).
type heartbeatShardRegistry struct {
	mu     sync.RWMutex
	shards []Shard
	byID   map[string]string
}

func (r *heartbeatShardRegistry) update(shards []Shard) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.shards = shards
	r.byID = make(map[string]string, len(shards))
	for _, s := range shards {
		r.byID[s.ID] = s.Addr
	}
}

func (r *heartbeatShardRegistry) LivingShards() []Shard {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Shard, len(r.shards))
	copy(out, r.shards)
	return out
}

func (r *heartbeatShardRegistry) AddrFor(id string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	addr, ok := r.byID[id]
	return addr, ok
}

// newQueueMesh creates a new mesh instance. Call start() to begin discovery.
func newQueueMesh(
	store *queueStore,
	shardID string,
	resolver PeerResolver,
	peerPort string,
) *queueMesh {
	return &queueMesh{
		store:      store,
		shardID:    shardID,
		resolver:   resolver,
		peerPort:   peerPort,
		peers:      make(map[string]flowv1.QueuePeerServiceClient),
		conns:      make(map[string]*grpc.ClientConn),
		registry:   &staticShardRegistry{},
		deadShards: make(map[string]bool),
	}
}

// start begins the periodic peer discovery loop. After each discover returns
// (initial and per-ticker), it fires backfillBackups outside discover's
// m.mu.Lock() critical section — calling it from inside discover would
// deadlock the non-reentrant sync.RWMutex (see the locking-discipline note).
func (m *queueMesh) start(ctx context.Context) {
	ctx, m.cancel = context.WithCancel(ctx)

	// Initial discovery.
	m.discover(ctx)
	m.backfillBackups(ctx)

	go func() {
		ticker := time.NewTicker(discoveryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.discover(ctx)
				m.backfillBackups(ctx)
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
	}
}

// livingShardIDs returns the identities of living shards from the registry
// minus deadShards (backup-selection candidate set; R-C1). Reads deadShards
// under m.mu.RLock.
func (m *queueMesh) livingShardIDs() []string {
	m.mu.RLock()
	dead := make(map[string]bool, len(m.deadShards))
	maps.Copy(dead, m.deadShards)
	m.mu.RUnlock()

	var ids []string
	for _, s := range m.registry.LivingShards() {
		if !dead[s.ID] {
			ids = append(ids, s.ID)
		}
	}
	return ids
}

// markDead records deadShard in deadShards (under m.mu) so backup selection
// excludes it immediately, even if the registry still lists it. This is the
// ONLY lock-taking call in handleShardDead — it releases before the promotion
// loop, so the loop runs unlocked (see the locking-discipline note).
func (m *queueMesh) markDead(shardID string) {
	m.mu.Lock()
	m.deadShards[shardID] = true
	m.mu.Unlock()
}

// chooseBackup picks a random backup identity from livingShardIDs() minus
// excluding, returning just the identity ("" when no candidate — R-C1
// deferral). Uses math/rand/v2 top-level functions, so it is not injectable;
// tests pin the setup (2-shard enqueue then grow) instead.
func (m *queueMesh) chooseBackup(excluding []string) string {
	exclude := make(map[string]bool, len(excluding))
	for _, e := range excluding {
		exclude[e] = true
	}
	var candidates []string
	for _, id := range m.livingShardIDs() {
		if !exclude[id] {
			candidates = append(candidates, id)
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	return candidates[rand.IntN(len(candidates))]
}

// replicateAndRecord is the single place that sends ReplicateItem and then
// records the outcome on the owner row: success => store.setBackupShard(wi,
// backupShard); failure or unresolvable addr => store.setBackupShard(wi, "")
// + slog.Warn (backfill-eligible, per R-C1 deferred-restore). backupShard ==
// "" => no-op. The peers[addr] lookup takes m.mu.RLock.
func (m *queueMesh) replicateAndRecord(
	ctx context.Context, wi, generation, ownerShard, queueName, backupShard string,
) error {
	if backupShard == "" {
		return nil
	}
	addr, ok := m.registry.AddrFor(backupShard)
	if !ok {
		_ = m.store.setBackupShard(ctx, wi, "")
		slog.Warn("flow hitl: replicate no addr for backup", "workitem_id", wi, "backup", backupShard)
		return fmt.Errorf("no addr for backup shard %q", backupShard)
	}

	m.mu.RLock()
	client, ok := m.peers[addr]
	m.mu.RUnlock()
	if !ok {
		_ = m.store.setBackupShard(ctx, wi, "")
		slog.Warn("flow hitl: replicate no peer client", "workitem_id", wi, "backup", backupShard, "addr", addr)
		return fmt.Errorf("no peer client for backup addr %q", addr)
	}

	peerCtx, cancel := context.WithTimeout(ctx, peerTimeout)
	defer cancel()
	_, err := client.ReplicateItem(peerCtx, &flowv1.ReplicateItemRequest{
		Item: &flowv1.QueueItem{
			WorkitemId:   wi,
			ShardId:      ownerShard,
			QueueName:    queueName,
			Status:       "waiting",
			GenerationId: generation,
		},
	})
	if err != nil {
		// ponytail: a failed replicate leaves the item backfill-eligible (''),
		// owner remains authoritative; the stale copy is superseded by a newer
		// generation. Warn-and-continue keeps Enqueue failure-free.
		_ = m.store.setBackupShard(ctx, wi, "")
		slog.Warn("flow hitl: replicate failed", "workitem_id", wi, "backup", backupShard, "error", err)
		return err
	}
	return m.store.setBackupShard(ctx, wi, backupShard)
}

// dropBackup calls DropItem on the backup shard (generation-guarded, R-C5).
// Failure or unresolvable addr => slog.Warn (stale copy superseded by
// generation). The peers[addr] lookup takes m.mu.RLock.
func (m *queueMesh) dropBackup(ctx context.Context, workitemID, generation, backupShard string) error {
	addr, ok := m.registry.AddrFor(backupShard)
	if !ok {
		slog.Warn("flow hitl: drop no addr for backup", "workitem_id", workitemID, "backup", backupShard)
		return fmt.Errorf("no addr for backup shard %q", backupShard)
	}

	m.mu.RLock()
	client, ok := m.peers[addr]
	m.mu.RUnlock()
	if !ok {
		slog.Warn("flow hitl: drop no peer client", "workitem_id", workitemID, "backup", backupShard, "addr", addr)
		return fmt.Errorf("no peer client for backup addr %q", addr)
	}

	peerCtx, cancel := context.WithTimeout(ctx, peerTimeout)
	defer cancel()
	if _, err := client.DropItem(peerCtx, &flowv1.DropItemRequest{
		WorkitemId:   workitemID,
		GenerationId: generation,
	}); err != nil {
		slog.Warn("flow hitl: drop backup failed", "workitem_id", workitemID, "backup", backupShard, "error", err)
		return err
	}
	return nil
}

// propagateDrop is the one drop-propagation helper, called from both decide
// paths (local routeDecide and remote queuePeerServer.DecideItem). No-op when
// the row has no recorded backup.
func (m *queueMesh) propagateDrop(ctx context.Context, item QueueItem) {
	if item.BackupShard == "" {
		return
	}
	// Best-effort: a missed drop is superseded by the generation UUID on the
	// next park. Callers intentionally ignore the error (warn happens inside).
	_ = m.dropBackup(ctx, item.WorkitemID, item.Generation, item.BackupShard)
}

// handleShardDead promotes this shard's backup rows for the dead shard and
// restores the owner+backup invariant (R-C4). Runs WITHOUT holding m.mu —
// markDead is the only call that takes Lock, and it releases before the
// promotion loop; everything else takes RLock internally. Then backfillBackups
// restores any ownership-gap backups.
func (m *queueMesh) handleShardDead(ctx context.Context, deadShard string) {
	m.markDead(deadShard)

	rows, err := m.store.listBackupsForOwner(ctx, deadShard)
	if err != nil {
		slog.Warn("flow hitl: promotion list backups failed", "dead", deadShard, "error", err)
		return
	}
	for _, row := range rows {
		// Idempotency guard: already-promoted / gone rows are skipped.
		promoted, err := m.store.promoteBackup(ctx, row.WorkitemID, deadShard, m.shardID)
		if err != nil {
			if errors.Is(err, ErrQueueItemNotFound) {
				continue
			}
			slog.Warn("flow hitl: promotion failed", "workitem_id", row.WorkitemID, "dead", deadShard, "error", err)
			continue
		}
		chosen := m.chooseBackup([]string{m.shardID, deadShard})
		if chosen != "" {
			err := m.replicateAndRecord(
				ctx, promoted.WorkitemID, promoted.Generation, m.shardID, promoted.QueueName, chosen,
			)
			if err == nil {
				continue // replicateAndRecord set backup_shard = chosen on success
			}
		}
		// None available or replicate failed => deferred / backfill-eligible.
		_ = m.store.setBackupShard(ctx, promoted.WorkitemID, "")
	}

	m.backfillBackups(ctx)
}

// backfillBackups restores owner rows whose backup_shard == ” (R-C1 deferred
// backup restore / R-C4 deferred fresh-backup). Scans owner rows via
// listOwnerRows, picks a random backup, replicates, and records the choice.
// Returns when no candidates exist (does not spin). Never called while m.mu
// is held (see the locking-discipline note).
func (m *queueMesh) backfillBackups(ctx context.Context) {
	if len(m.livingShardIDs()) == 0 {
		return
	}
	ownerRows, _, err := m.store.listOwnerRows(ctx, QueueFilter{})
	if err != nil {
		slog.Warn("flow hitl: backfill scan failed", "error", err)
		return
	}
	for _, row := range ownerRows {
		if row.BackupShard != "" {
			continue
		}
		chosen := m.chooseBackup([]string{m.shardID, row.ShardID})
		if chosen == "" {
			return
		}
		// Best-effort: on failure replicateAndRecord resets backup_shard to ''
		// (backfill-eligible); there is nothing to act on here.
		_ = m.replicateAndRecord(ctx, row.WorkitemID, row.Generation, m.shardID, row.QueueName, chosen)
	}
}

// getGlobalQueue scatter-gathers queue items from all peers + local store,
// then dedupes by workitem_id (R-C3): the copy with the maximum generation
// wins — lexicographic comparison, deterministic because generations are
// time-ordered — tie-broken by the non-backup (owner) copy. Per-shard
// limit/offset forwarding is unchanged; an unreachable/slow peer is
// skipped-and-warned, not failed.
func (m *queueMesh) getGlobalQueue(ctx context.Context, filter QueueFilter) ([]QueueItem, error) {
	// Local results: owner rows + backup rows this shard holds.
	localItems, _, err := m.store.listOwnerRows(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("local queue: %w", err)
	}
	backupItems, _, err := m.store.listBackups(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("local backups: %w", err)
	}
	for i := range backupItems {
		backupItems[i].IsBackup = true
	}
	all := append(localItems, backupItems...)

	// Snapshot peers.
	m.mu.RLock()
	peerClients := make(map[string]flowv1.QueuePeerServiceClient, len(m.peers))
	maps.Copy(peerClients, m.peers)
	m.mu.RUnlock()

	if len(peerClients) == 0 {
		// No peers: still return the local owner rows AND any backup rows this
		// shard holds, deduped by R-C3 (owner preferred, backup when owner
		// absent) — a peerless shard may hold the only remaining copy of an
		// item as a backup row. Returning only owner rows would hide it.
		return dedupeItems(all), nil
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
	allItems := make([]QueueItem, 0, len(all))
	allItems = append(allItems, all...)
	for range peerClients {
		r := <-results
		if r.err == nil {
			allItems = append(allItems, r.items...)
		}
	}

	// Dedupe by workitem_id (R-C3): keep the copy with the max generation
	// (lexicographic, deterministic), tie-broken by the non-backup copy.
	return dedupeItems(allItems), nil
}

// dedupeItems reduces a collected item set to exactly one copy per
// workitem_id (R-C3): the maximum generation wins (time-ordered ->
// deterministic creation order), tie-broken by the owner (non-backup) copy.
func dedupeItems(items []QueueItem) []QueueItem {
	best := make(map[string]*QueueItem, len(items))
	order := make([]string, 0, len(items))
	for i := range items {
		item := items[i]
		cur, ok := best[item.WorkitemID]
		if !ok {
			cp := item
			best[item.WorkitemID] = &cp
			order = append(order, item.WorkitemID)
			continue
		}
		// Prefer higher generation; on a tie prefer the owner (non-backup).
		if item.Generation > cur.Generation ||
			(item.Generation == cur.Generation && !item.IsBackup && cur.IsBackup) {
			cp := item
			best[item.WorkitemID] = &cp
		}
	}
	out := make([]QueueItem, 0, len(order))
	for _, id := range order {
		out = append(out, *best[id])
	}
	return out
}

// routeGetItem looks up an item: local first, then fan out to peers.
func (m *queueMesh) routeGetItem(ctx context.Context, workitemID string) (*QueueItem, error) {
	// Local first — owner-row-only (R-C6): a backup row this shard holds points
	// at a foreign owner and must not be served here, mirroring the remote
	// fan-out's !IsBackup filter. Treat it as "not mine" (same as absence).
	item, err := m.store.getByID(ctx, workitemID)
	if err == nil && item.ShardID == m.shardID {
		return item, nil
	}
	if err != nil && !errors.Is(err, ErrQueueItemNotFound) {
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
				// Owner-row-only matching (R-C6): a backup row's shard_id points at
				// a foreign owner and must not be served by the backup holder.
				if !pi.GetIsBackup() && pi.GetWorkitemId() == workitemID {
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

// localOwnerRow reports whether this shard OWNS a row — nil when it does. A row
// held here as a backup (shard_id != self) is "not mine" — the same signal as
// absence — so callers fall through to findOwner + proxy unchanged (R-C6).
// Semantics: getByID -> ErrQueueItemNotFound if absent; row.ShardID == self =>
// nil (owned); row.ShardID != self (backup row) => ErrQueueItemNotFound
// (deliberately the same sentinel as absence, so each branch's existing
// fall-through is reused with no new error shape). Runs BEFORE the store
// mutation in every branch.
func (m *queueMesh) localOwnerRow(ctx context.Context, workitemID string) error {
	item, err := m.store.getByID(ctx, workitemID)
	if err != nil {
		return err
	}
	if item.ShardID != m.shardID {
		// Backup row this shard holds — treat as not mine (same as absence).
		return ErrQueueItemNotFound
	}
	return nil
}

// routeClaim claims an item, routing to the owning shard if remote.
func (m *queueMesh) routeClaim(ctx context.Context, workitemID string) (*QueueItem, error) {
	// Ownership guard first: a locally-held backup row (shard_id != self) is
	// "not mine" and must fall through to the living owner (R-C6) — an
	// unguarded local claim would double-claim on the backup holder.
	if err := m.localOwnerRow(ctx, workitemID); err != nil {
		if !errors.Is(err, ErrQueueItemNotFound) {
			return nil, err
		}
	} else {
		return m.store.claim(ctx, workitemID)
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
	// Ownership guard (R-C6): a locally-held backup row falls through to the
	// living owner instead of erroring ErrQueueItemInvalidState on the unclaimed
	// backup row.
	if err := m.localOwnerRow(ctx, workitemID); err != nil {
		if !errors.Is(err, ErrQueueItemNotFound) {
			return nil, err
		}
	} else {
		return m.store.release(ctx, workitemID)
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

// routeDecide decides an item (deletes it), routing to the owning shard if
// remote. The local branch is ownership-guarded (R-C6) and, on success,
// propagates the backup drop through the shared propagation helper.
func (m *queueMesh) routeDecide(ctx context.Context, workitemID, choice string) error {
	// Ownership guard first (R-C6): a locally-held backup row (not mine) falls
	// through to findOwner + proxy DecideItem. Only shard_id == self rows
	// proceed to the store — deciding a backup row would error invalid-state
	// on the unclaimed backup row otherwise.
	err := m.localOwnerRow(ctx, workitemID)
	if err == nil {
		decided, err := m.store.decideWithRow(ctx, workitemID)
		if err != nil {
			// decideWithRow returns ErrQueueItemNotFound when the row is absent
			// (or a fetch/claiming race) — fall through to the living owner.
			if !errors.Is(err, ErrQueueItemNotFound) {
				return err
			}
		} else {
			// Success: propagate the backup drop. decided is the pre-delete row
			// with generation + backup_shard.
			m.propagateDrop(ctx, decided)
			return nil
		}
	} else if !errors.Is(err, ErrQueueItemNotFound) {
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
				// Owner-row-only matching (R-C6): a backup row's shard_id points at
				// a foreign owner; matching it would route Claim/Decide to the
				// backup holder.
				if !pi.GetIsBackup() && pi.GetWorkitemId() == workitemID {
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
// delegating to the local queueStore. mesh is nil-safe: when set (by
// Manager.Start), DecideItem propagates the backup drop and NotifyShardDead
// fires the promotion path; raw harness servers leave it nil and both are
// skipped.
type queuePeerServer struct {
	flowv1.UnimplementedQueuePeerServiceServer
	store    *queueStore
	onDecide func(workitemID, choice string) // signals local WaitForDecision when a remote peer triggers DecideItem
	mesh     *queueMesh
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

	// Serve both owner rows AND backup rows this shard holds (D4/D3): the
	// collector's dedupe in getGlobalQueue is the single place owner
	// preference is applied.
	ownerItems, ownerTotal, err := s.store.listOwnerRows(ctx, filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get local queue: %v", err)
	}
	backupItems, backupTotal, err := s.store.listBackups(ctx, filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get local backups: %v", err)
	}
	for i := range backupItems {
		backupItems[i].IsBackup = true
	}

	items := append(ownerItems, backupItems...)
	protoItems := make([]*flowv1.QueueItem, 0, len(items))
	for _, item := range items {
		protoItems = append(protoItems, queueItemToProto(item))
	}

	return &flowv1.GetLocalQueueResponse{
		Items:  protoItems,
		Total:  int32(ownerTotal + backupTotal),
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
	// Order pinned: fetch the row pre-delete (decideWithRow) so drop
	// propagation has generation + backup_shard before the delete.
	item, err := s.store.decideWithRow(ctx, req.GetWorkitemId())
	if err != nil {
		return nil, storeErrorToGRPC(err)
	}
	// Propagate the backup drop (offset R-C5). Best-effort — a missed drop is
	// superseded by the generation UUID on the next park.
	if s.mesh != nil {
		s.mesh.propagateDrop(ctx, item)
	}
	// Signal any local WaitForDecision callers. This handles the cross-shard
	// case where a remote peer proxies DecideItem to the owning shard.
	if s.onDecide != nil {
		s.onDecide(req.GetWorkitemId(), req.GetChoice())
	}
	return &flowv1.DecideItemResponse{Acknowledged: true}, nil
}

// ReplicateItem stores a backup row for a foreign owner (R-C1).
func (s *queuePeerServer) ReplicateItem(
	ctx context.Context, req *flowv1.ReplicateItemRequest,
) (*flowv1.ReplicateItemResponse, error) {
	item := req.GetItem()
	if item == nil || item.GetWorkitemId() == "" {
		return nil, status.Error(codes.InvalidArgument, "missing item")
	}
	if err := s.store.insertBackup(
		ctx, item.GetWorkitemId(), item.GetShardId(), item.GetQueueName(), item.GetGenerationId(),
	); err != nil {
		return nil, status.Errorf(codes.Internal, "insert backup: %v", err)
	}
	return &flowv1.ReplicateItemResponse{Acknowledged: true}, nil
}

// DropItem drops a backup copy of an item, generation-guarded (R-C5). A
// missed/raced drop (generation mismatch or absent row) is NotFound.
func (s *queuePeerServer) DropItem(
	ctx context.Context, req *flowv1.DropItemRequest,
) (*flowv1.DropItemResponse, error) {
	if err := s.store.dropByGeneration(ctx, req.GetWorkitemId(), req.GetGenerationId()); err != nil {
		return nil, storeErrorToGRPC(err)
	}
	return &flowv1.DropItemResponse{Acknowledged: true}, nil
}

// NotifyShardDead answers the PHASE_02-defined / PHASE_03-sent RPC (R-B6/R-C4):
// it fires the promotion path on this shard's mesh. Nil-safe: when mesh is nil
// (raw harness), the promotion path is skipped.
func (s *queuePeerServer) NotifyShardDead(
	ctx context.Context, req *flowv1.NotifyShardDeadRequest,
) (*flowv1.NotifyShardDeadResponse, error) {
	if req.GetShardId() == "" {
		return nil, status.Error(codes.InvalidArgument, "missing shard_id")
	}
	if s.mesh != nil {
		// The production entry point for the promotion path. The hook runs
		// outside the lock; handleShardDead's markDead is its only lock call.
		if s.mesh.onShardDead != nil {
			s.mesh.onShardDead(req.GetShardId())
		}
	}
	return &flowv1.NotifyShardDeadResponse{Acknowledged: true}, nil
}

// --- Proto conversion helpers ---

func protoToQueueItem(pi *flowv1.QueueItem) QueueItem {
	item := QueueItem{
		WorkitemID: pi.GetWorkitemId(),
		ShardID:    pi.GetShardId(),
		QueueName:  pi.GetQueueName(),
		Status:     QueueStatus(pi.GetStatus()),
		Generation: pi.GetGenerationId(),
		IsBackup:   pi.GetIsBackup(),
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
		WorkitemId:   item.WorkitemID,
		ShardId:      item.ShardID,
		QueueName:    item.QueueName,
		Status:       string(item.Status),
		EnqueuedAt:   item.EnqueuedAt.Format(time.RFC3339),
		GenerationId: item.Generation,
		IsBackup:     item.IsBackup,
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
		peers = append(peers, net.JoinHostPort(ip, r.Port))
	}
	return peers, nil
}
