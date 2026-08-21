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
// corroborated orphans), and finally repair the reference shard with any item a
// non-reference shard holds that it lacks (symmetric push — the reference is a
// repairable straggler, never a reason to destroy copies). An item absent from
// the reference is dropped from a shard ONLY when it is also absent from every
// other non-reference shard's report: presence anywhere always wins. If no
// freshest mirror is recorded, or it is no longer living, the pass is a no-op.
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

	// Read every other living non-reference shard's trimmed report UP FRONT so
	// a drop is corroborated against the whole sibling set (and so the
	// symmetric reference repair sees the pass's converged reports). A shard
	// that cannot be dialed/fetched is skipped from both convergence and
	// corroboration.
	reports := map[string]map[string]string{} // shardID -> trimmed report
	clients := map[string]flowv1.QueuePeerServiceClient{}
	for _, sh := range shards {
		if sh.ShardID == freshID {
			continue // reference shard handled separately below
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
		reports[sh.ShardID] = trimReport(resp.GetItems())
		clients[sh.ShardID] = c
	}

	// Converge each non-reference shard toward the reference, corroborating
	// drops against every OTHER non-reference report (S's own report is
	// excluded — it trivially contains the item under consideration). The
	// references are the collected reports, so a converged shard's updated
	// report feeds the final symmetric reference repair.
	for _, sh := range shards {
		if sh.ShardID == freshID {
			continue // reference shard untouched (except the symmetric push below)
		}
		rep := reports[sh.ShardID]
		if rep == nil {
			continue // dial/fetch failed up front — skipped from convergence too
		}
		others := make([]map[string]string, 0, len(reports)-1)
		for otherID, other := range reports {
			if otherID != sh.ShardID {
				others = append(others, other)
			}
		}
		push, drops := planConvergence(fresh, rep, others)
		c := clients[sh.ShardID]
		for _, item := range push {
			if _, err := c.ApplyItem(ctx, &flowv1.ApplyItemRequest{Item: item}); err != nil {
				slog.Warn("queue-service: sweep apply failed", "queue", queueName, "addr", sh.Addr, "error", err)
				continue
			}
			rep[item.GetWorkitemId()] = item.GetGenerationId()
		}
		for _, d := range drops {
			if _, err := c.DropItem(ctx, &flowv1.DropItemRequest{
				WorkitemId:   d.WorkitemID,
				GenerationId: d.GenerationID,
			}); err != nil {
				slog.Warn("queue-service: sweep drop failed", "queue", queueName, "addr", sh.Addr, "error", err)
				continue
			}
			delete(rep, d.WorkitemID)
		}
	}

	// Symmetric push onto the reference: every item surviving on some
	// non-reference shard but absent from the recorded freshest reference is
	// pushed onto it. Uses the CONVERGED reports, so a genuine orphan dropped
	// earlier in this pass does not resurrect onto the reference.
	allNonRefReports := make([]map[string]string, 0, len(reports))
	for _, rep := range reports {
		allNonRefReports = append(allNonRefReports, rep)
	}
	for _, item := range planPushToReference(trimReport(fresh), allNonRefReports) {
		if _, err := freshC.ApplyItem(ctx, &flowv1.ApplyItemRequest{Item: item}); err != nil {
			slog.Warn("queue-service: sweep reference apply failed", "queue", queueName, "addr", freshAddr, "error", err)
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
// shard's trimmed report, corroborated against every OTHER non-reference
// shard's report (R-4.2/R-4.3 corrected). It returns the ApplyItem pushes
// (items in fresh missing from the shard) and the generation-guarded DropItem
// refs (items the shard holds that are absent from fresh AND absent from EVERY
// other non-reference report — a genuine orphan). An item present on ANY other
// non-reference shard is a corroborated copy that MUST survive: presence on
// another non-reference shard always wins; absence from the reference alone
// never justifies a drop (the data-loss guard). Presence-based: a shard holding
// ANY copy of an authoritative item is neither pushed-onto nor dropped, so a
// NEWER partial-broadcast copy is never downgraded.
func planConvergence(fresh []*flowv1.QueueItem, targetGen map[string]string, otherReports []map[string]string) (
	push []*flowv1.QueueItem, drops []dropRef,
) {
	freshIDs := make(map[string]struct{}, len(fresh))
	for _, item := range fresh {
		if _, ok := targetGen[item.GetWorkitemId()]; !ok {
			push = append(push, item)
		}
		freshIDs[item.GetWorkitemId()] = struct{}{}
	}
	for id, gen := range targetGen {
		if _, inFresh := freshIDs[id]; inFresh {
			continue // present in the reference — never a drop candidate
		}
		// Absent from the reference: drop only if absent from EVERY other
		// non-reference report. One corroborating copy anywhere ⇒ survive.
		corroborated := false
		for _, rep := range otherReports {
			if _, ok := rep[id]; ok {
				corroborated = true
				break
			}
		}
		if !corroborated {
			drops = append(drops, dropRef{WorkitemID: id, GenerationID: gen})
		}
	}
	return push, drops
}

// planPushToReference computes the symmetric push onto the reference shard
// (R-4.2 corrected): every item present on SOME non-reference shard but ABSENT
// from the recorded freshest reference is pushed onto it. The reference is a
// repairable straggler, never a reason to destroy copies. Reports are trimmed
// {workitem_id -> generation_id} maps, so the returned items carry id + the
// reporting shard's recorded generation — sufficient for a generation-guarded
// ApplyItem (convergence cares about presence). Each missing id is pushed once.
func planPushToReference(freshestGen map[string]string, otherReports []map[string]string) []*flowv1.QueueItem {
	missing := make(map[string]string, 0) // workitem id -> generation, deduped
	for _, rep := range otherReports {
		for id, gen := range rep {
			if _, known := freshestGen[id]; known {
				continue
			}
			if _, dup := missing[id]; !dup {
				missing[id] = gen
			}
		}
	}
	push := make([]*flowv1.QueueItem, 0, len(missing))
	for id, gen := range missing {
		push = append(push, &flowv1.QueueItem{WorkitemId: id, GenerationId: gen})
	}
	return push
}
