package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	v1 "github.com/foundry/flow/operator/api/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DefaultQueueLeaseTTL is the queue-shard lease TTL: 3 missed heartbeats × the
// SDK's DefaultHeartbeatInterval (15s). A shard whose lastHeartbeatAt is older
// than this is evicted (dropped from the Queue CR status) and its death is
// broadcast to the surviving shards. Mirror of DefaultCapabilityStalenessWindow
// — the single source of truth for the default lease TTL.
const DefaultQueueLeaseTTL = 3 * 15 * time.Second // 45s

// defaultLeaseSweepInterval is the cadence of the eviction sweep ticker. It
// must be ≪ the lease TTL so eviction is prompt (mirrors mesh.discoveryInterval
// for the mesh's periodic loop).
const DefaultLeaseSweepInterval = 10 * time.Second

// StartSweep begins the periodic lease-eviction sweep loop, context-cancellable
// like mesh.start.
func (r *Registry) StartSweep(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(r.sweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := r.sweepEvictions(ctx); err != nil {
					slog.Warn("queue-service: lease sweep failed", "error", err)
				}
			}
		}
	}()
}

// sweepEvictions performs one eviction pass over every Queue CR in the
// registry's namespace: any shard whose lastHeartbeatAt is older than the lease
// TTL is evicted via the four-step transition.
func (r *Registry) sweepEvictions(ctx context.Context) error {
	var list v1.QueueList
	if err := r.client.List(ctx, &list, client.InNamespace(r.Namespace)); err != nil {
		return fmt.Errorf("list queues: %w", err)
	}
	for _, q := range list.Items {
		var stale []v1.QueueShardStatus
		for _, s := range q.Status.Shards {
			if s.Phase == phaseEvicted {
				continue // already tombstoned by a prior sweep; drop next pass
			}
			if s.LastHeartbeatAt.IsZero() || time.Since(s.LastHeartbeatAt.Time) > r.leaseTTL {
				stale = append(stale, s)
			}
		}
		if len(stale) == 0 {
			continue
		}
		if err := r.evictQueue(ctx, q.Name, stale); err != nil {
			slog.Warn("queue-service: eviction failed", "queue", q.Name, "error", err)
		}
	}
	return nil
}

// evictQueue performs the four-step eviction transition for the given stale
// shards of a queue: (1) mark phase=evicted, (2) drop from .status.shards[],
// (3) fire OnShardEvicted, (4) fan out NotifyShardDead to each surviving
// (still-living) shard.
func (r *Registry) evictQueue(ctx context.Context, queueName string, stale []v1.QueueShardStatus) error {
	// Step 1: mark each stale shard phase=evicted (tombstones it for any
	// concurrent reader).
	q, err := r.get(ctx, queueName)
	if err != nil {
		return err
	}
	for _, s := range stale {
		if i := shardIndex(q, s.ShardID); i >= 0 {
			q.Status.Shards[i].Phase = phaseEvicted
		}
	}
	if err := r.client.Status().Update(ctx, q); err != nil {
		return err
	}

	// Step 2: remove the evicted shards from .status.shards[].
	q, err = r.get(ctx, queueName)
	if err != nil {
		return err
	}
	var remaining []v1.QueueShardStatus
	for _, s := range q.Status.Shards {
		if s.Phase == phaseEvicted {
			continue
		}
		remaining = append(remaining, s)
	}
	q.Status.Shards = remaining
	if err := r.client.Status().Update(ctx, q); err != nil {
		return err
	}

	// Step 3: fire the broadcast hook before the death notice goes out.
	for _, s := range stale {
		if r.OnShardEvicted != nil {
			r.OnShardEvicted(queueName, s.ShardID)
		} else {
			slog.Warn("queue-service: shard evicted", "queue", queueName, "shard", s.ShardID)
		}
	}

	// Step 4: fan out NotifyShardDead(shardID) to each surviving living shard.
	// Addresses come from the surviving (non-evicted, fresh) CR shard entries.
	if len(remaining) == 0 {
		return nil
	}
	return r.fanOutShardDead(ctx, queueName, remaining, stale)
}

// fanOutShardDead dials each surviving living shard's QueuePeerService and
// calls NotifyShardDead for each dead shard. Connects through the peerDialer
// seam (nil ⇒ production dialer).
func (r *Registry) fanOutShardDead(
	ctx context.Context, queueName string,
	survivors []v1.QueueShardStatus, dead []v1.QueueShardStatus,
) error {
	proxy := newPeerProxy(r)
	defer proxy.close()

	var firstErr error
	for _, sv := range survivors {
		c, err := proxy.dial(ctx, sv.Addr)
		if err != nil {
			slog.Warn("queue-service: fan-out dial failed", "queue", queueName, "addr", sv.Addr, "error", err)
			continue
		}
		for _, d := range dead {
			if _, err := c.NotifyShardDead(ctx, &flowv1.NotifyShardDeadRequest{ShardId: d.ShardID}); err != nil {
				slog.Warn(
					"queue-service: NotifyShardDead failed",
					"queue", queueName, "survivor", sv.ShardID,
					"dead", d.ShardID, "error", err,
				)
				if firstErr == nil {
					firstErr = err
				}
			} else {
				slog.Info(
					"queue-service: notified shard of death",
					"queue", queueName, "survivor", sv.ShardID, "dead", d.ShardID,
				)
			}
		}
	}
	return firstErr
}
