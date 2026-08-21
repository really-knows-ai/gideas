package service

import (
	"context"
	"log/slog"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	v1 "github.com/foundry/flow/operator/api/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DefaultSweepInterval is the cadence of the convergence backstop sweep — the
// single named constant the plan requires (R-4.2). It runs alongside the lease
// sweep, well below the write path, reconciling shard drift.
const DefaultSweepInterval = 60 * time.Second

// dropRef identifies an extra/orphaned item to drop from one shard,
// generation-guarded: DropItem only deletes a row whose current generation
// matches GenerationID, so a raced newer parking event is never destroyed
// (R-4.3).
type dropRef struct {
	WorkitemID   string
	GenerationID string
}

// Sweeper is the convergence backstop (R-4.2/R-4.3, F2/F3). It periodically
// reconciles every living shard of a queue against the recorded freshest mirror:
// pushing missing authoritative items (ApplyItem) and dropping orphaned extras
// (DropItem). Purely additive — it reuses peerProxy (fetch/dial/GetLocalQueue/
// ApplyItem/DropItem); no new proto RPC, no sidecar change (F3).
type Sweeper struct {
	reg      *Registry
	interval time.Duration
}

// NewSweeper constructs a Sweeper over the registry with the given cadence.
// Tests inject sub-second intervals; production passes DefaultSweepInterval.
func NewSweeper(reg *Registry, interval time.Duration) *Sweeper {
	return &Sweeper{reg: reg, interval: interval}
}

// Sweep performs ONE reconciliation pass for the queue: read the recorded
// freshest mirror's authoritative item set, then converge every OTHER living
// shard toward it (ApplyItem for missing items, generation-guarded DropItem for
// extras). The reference shard is untouched. If no freshest mirror is recorded,
// or it is no longer living, the pass is a no-op.
func (s *Sweeper) Sweep(ctx context.Context, queueName string) error {
	freshID := s.reg.FreshestShardID(queueName)
	if freshID == "" {
		return nil // no recorded freshest mirror — nothing authoritative to converge to
	}
	proxy := newPeerProxy(s.reg)
	defer proxy.close()

	shards, err := proxy.fetch(ctx, queueName)
	if err != nil {
		return err
	}

	// Resolve the recorded freshest shard's address from the living set. The
	// diff target is this recorded identity (F2), NEVER updated_at/heartbeat.
	var freshAddr string
	for _, sh := range shards {
		if sh.ShardID == freshID {
			freshAddr = sh.Addr
			break
		}
	}
	if freshAddr == "" {
		return nil // recorded freshest mirror is no longer living
	}

	freshC, err := proxy.dial(ctx, freshAddr)
	if err != nil {
		return err
	}
	freshResp, err := freshC.GetLocalQueue(ctx, &flowv1.GetLocalQueueRequest{})
	if err != nil {
		return err
	}
	fresh := freshResp.GetItems()

	for _, sh := range shards {
		if sh.ShardID == freshID {
			continue // reference shard untouched
		}
		c, err := proxy.dial(ctx, sh.Addr)
		if err != nil {
			slog.Warn("queue-service: sweep dial failed", "queue", queueName, "addr", sh.Addr, "error", err)
			continue
		}
		resp, err := c.GetLocalQueue(ctx, &flowv1.GetLocalQueueRequest{})
		if err != nil {
			slog.Warn("queue-service: sweep fetch failed", "queue", queueName, "addr", sh.Addr, "error", err)
			continue
		}
		push, drops := planConvergence(fresh, trimReport(resp.GetItems()))
		for _, item := range push {
			if _, err := c.ApplyItem(ctx, &flowv1.ApplyItemRequest{Item: item}); err != nil {
				slog.Warn("queue-service: sweep apply failed", "queue", queueName, "addr", sh.Addr, "error", err)
			}
		}
		for _, d := range drops {
			if _, err := c.DropItem(ctx, &flowv1.DropItemRequest{
				WorkitemId:   d.WorkitemID,
				GenerationId: d.GenerationID,
			}); err != nil {
				slog.Warn("queue-service: sweep drop failed", "queue", queueName, "addr", sh.Addr, "error", err)
			}
		}
	}
	return nil
}

// Run loops the convergence sweep cadence over every queue in the registry's
// namespace until ctx is cancelled. Mirrors Registry.StartSweep's shape.
func (s *Sweeper) Run(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.sweepQueues(ctx); err != nil {
					slog.Warn("queue-service: convergence sweep failed", "error", err)
				}
			}
		}
	}()
}

// sweepQueues performs one pass over every Queue CR in the namespace.
func (s *Sweeper) sweepQueues(ctx context.Context) error {
	var list v1.QueueList
	if err := s.reg.client.List(ctx, &list, client.InNamespace(s.reg.Namespace)); err != nil {
		return err
	}
	for _, q := range list.Items {
		if err := s.Sweep(ctx, q.Name); err != nil {
			slog.Warn("queue-service: sweep failed", "queue", q.Name, "error", err)
		}
	}
	return nil
}

// trimReport maps a GetLocalQueue response's items to {workitem_id ->
// generation_id} only, discarding payloads (choices, timestamps, status,
// shard_id) — the per-shard payload-free report (F3).
func trimReport(items []*flowv1.QueueItem) map[string]string {
	report := make(map[string]string, len(items))
	for _, it := range items {
		report[it.GetWorkitemId()] = it.GetGenerationId()
	}
	return report
}

// planConvergence diffs the freshest mirror's authoritative set against one
// shard's trimmed report (R-4.2/R-4.3). It returns the ApplyItem pushes (items
// in fresh missing from the shard) and the generation-guarded DropItem refs
// (items the shard holds that are absent from fresh, carrying the shard's
// recorded generation for the guard). Presence-based: a shard holding ANY copy
// of an authoritative item is neither pushed-onto nor dropped, so a NEWER
// partial-broadcast copy is never downgraded.
func planConvergence(fresh []*flowv1.QueueItem, shardGen map[string]string) (
	push []*flowv1.QueueItem, drops []dropRef,
) {
	for _, item := range fresh {
		if _, ok := shardGen[item.GetWorkitemId()]; !ok {
			push = append(push, item)
		}
	}
	for id, gen := range shardGen {
		absent := true
		for _, item := range fresh {
			if item.GetWorkitemId() == id {
				absent = false
				break
			}
		}
		if absent {
			drops = append(drops, dropRef{WorkitemID: id, GenerationID: gen})
		}
	}
	return push, drops
}
