// TTL Watcher is an entry-bound watcher node for the Foundry Flow judiciary
// subsystem.
//
// It periodically polls the Librarian via QueryLaws for laws whose age
// exceeds their tier's configured review TTL, then creates hearing workitems.
// The handler stores the target law ID as a law-reference artefact and
// routes onward.
//
// Architecture:
//   - Entry function: polls Librarian on a timer, creates workitems for expired laws.
//   - Handler: stores law-reference artefact, routes to "default" output.
//   - Dedup: per-replica in-memory tracking of pending law IDs (best-effort).
//
// Uses the SDK StartEntry pattern: the entry function and handler server run
// concurrently, with shared-nothing semantics between them.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal"
	"github.com/gideas/flow/nodes/internal/nodeconfig"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

const (
	// defaultScanPeriod is the polling interval when not configured.
	defaultScanPeriod = 5 * time.Minute
)

// ttlConfig holds per-tier TTL durations and the scan interval.
// Values are Go duration strings (e.g. "168h", "720h").
type ttlConfig struct {
	ScanPeriod string `yaml:"scanPeriod"`
	Tier1      string `yaml:"tier1"`
	Tier2      string `yaml:"tier2"`
	Tier3      string `yaml:"tier3"`
	Tier4      string `yaml:"tier4"`
	Tier5      string `yaml:"tier5"`
}

// scanInterval returns the configured scan period or the default.
func (c *ttlConfig) scanInterval() time.Duration {
	if c.ScanPeriod != "" {
		if d, err := time.ParseDuration(c.ScanPeriod); err == nil && d > 0 {
			return d
		}
	}
	return defaultScanPeriod
}

// tierTTL returns the per-tier TTL map from config. Tiers with empty or
// unparseable durations are omitted (those laws are never considered expired).
func (c *ttlConfig) tierTTL() map[flowv1.LawTier]time.Duration {
	m := make(map[flowv1.LawTier]time.Duration)
	for tier, raw := range map[flowv1.LawTier]string{
		flowv1.LawTier_LAW_TIER_FINDING:            c.Tier1,
		flowv1.LawTier_LAW_TIER_RULING:             c.Tier2,
		flowv1.LawTier_LAW_TIER_LOCAL_STATUTE:      c.Tier3,
		flowv1.LawTier_LAW_TIER_STATE_CONSTITUTION: c.Tier4,
		flowv1.LawTier_LAW_TIER_FEDERAL_ACCORD:     c.Tier5,
	} {
		if raw == "" {
			continue
		}
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			slog.Warn("ttl-watcher: ignoring invalid tier TTL",
				"tier", tier, "value", raw)
			continue
		}
		m[tier] = d
	}
	return m
}

func main() {
	slog.Info("ttl-watcher: starting")
	if err := flow.StartEntry(watchTTL, handleHearing); err != nil {
		slog.Error("ttl-watcher: failed", "error", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Entry function — polls Librarian for expired laws, creates workitems
// ---------------------------------------------------------------------------

// watchTTL is the entry function. It loads config, then polls the Librarian
// on a timer to find laws exceeding their tier's review TTL.
func watchTTL(ctx context.Context, entry *flow.EntryClient) error {
	cfg, err := nodeconfig.Load[ttlConfig](nodeconfig.Path())
	if err != nil {
		return fmt.Errorf("ttl-watcher: load config: %w", err)
	}

	tierTTLs := cfg.tierTTL()
	if len(tierTTLs) == 0 {
		slog.Warn("ttl-watcher: no tier TTLs configured, entry loop will poll but never trigger")
	}

	interval := cfg.scanInterval()
	slog.Info("ttl-watcher: configured",
		"scan_period", interval, "tier_count", len(tierTTLs))

	tracker := internal.NewPendingTracker()

	// Run the first scan immediately, then on a timer.
	if err := scanAndCreate(ctx, entry, tierTTLs, tracker, time.Now); err != nil {
		slog.Warn("ttl-watcher: initial scan failed", "error", err)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := scanAndCreate(ctx, entry, tierTTLs, tracker, time.Now); err != nil {
				slog.Warn("ttl-watcher: scan failed", "error", err)
			}
		}
	}
}

// scanAndCreate queries the Librarian for all laws and creates hearing
// workitems for any whose age exceeds the configured tier TTL.
// The nowFunc parameter exists for testability.
func scanAndCreate(
	ctx context.Context,
	entry *flow.EntryClient,
	tierTTLs map[flowv1.LawTier]time.Duration,
	tracker *internal.PendingTracker,
	nowFunc func() time.Time,
) error {
	laws, err := entry.QueryLaws(ctx, "", "")
	if err != nil {
		return fmt.Errorf("query laws: %w", err)
	}

	now := nowFunc()
	for _, law := range laws {
		expired := isExpired(law, tierTTLs, now)
		if !expired {
			continue
		}

		lawID := law.GetId()
		if !tracker.MarkPending(lawID) {
			slog.Debug("ttl-watcher: law already pending, skipping",
				"law_id", lawID)
			continue
		}

		slog.Info("ttl-watcher: creating hearing workitem",
			"law_id", lawID, "tier", law.GetTier())

		if _, err := entry.CreateWorkitem(ctx, map[string]string{
			"law_id": lawID,
		}); err != nil {
			tracker.ClearPending(lawID)
			slog.Warn("ttl-watcher: create workitem failed",
				"law_id", lawID, "error", err)
		}
	}

	return nil
}

// isExpired returns true if the law's age exceeds its tier's TTL.
// Laws with UNSPECIFIED tier or no configured TTL are never expired.
// Age is measured from updated_at (falling back to created_at).
func isExpired(law *flowv1.Law, tierTTLs map[flowv1.LawTier]time.Duration, now time.Time) bool {
	ttl, ok := tierTTLs[law.GetTier()]
	if !ok {
		return false
	}

	ts := lawTimestamp(law)
	if ts == nil {
		return false
	}

	age := now.Sub(ts.AsTime())
	return age > ttl
}

// lawTimestamp returns the law's updated_at timestamp, falling back to
// created_at. Returns nil if neither is set.
func lawTimestamp(law *flowv1.Law) *timestamppb.Timestamp {
	if ts := law.GetUpdatedAt(); ts != nil {
		return ts
	}
	return law.GetCreatedAt()
}

// ---------------------------------------------------------------------------
// Handler — processes assigned hearing workitems
// ---------------------------------------------------------------------------

// handleHearing is the SDK handler entry point for hearing workitems.
func handleHearing(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("ttl-watcher: handler: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	return processHearing(ctx, client, wctx)
}

// processHearing performs the core handler logic: validate metadata, heartbeat,
// store law-reference artefact, and route to default output.
func processHearing(ctx context.Context, client *flow.Client, wctx *flowv1.WorkitemContext) error {
	lawID := wctx.GetMetadata()["law_id"]
	if lawID == "" {
		return fmt.Errorf("ttl-watcher: handler: missing law_id in metadata")
	}

	slog.Info("ttl-watcher: handling hearing",
		"workitem_id", wctx.GetWorkitemId(),
		"law_id", lawID,
	)

	if _, err := client.Heartbeat(ctx); err != nil {
		return fmt.Errorf("ttl-watcher: handler: heartbeat: %w", err)
	}

	if _, err := client.StoreArtefact(ctx, "law-reference", "law-reference", []byte(lawID)); err != nil {
		return fmt.Errorf("ttl-watcher: handler: store law-reference: %w", err)
	}

	slog.Info("ttl-watcher: stored law-reference artefact", "law_id", lawID)

	if _, err := client.RouteToOutput(ctx, "default"); err != nil {
		return fmt.Errorf("ttl-watcher: handler: route: %w", err)
	}

	slog.Info("ttl-watcher: routed to default output",
		"workitem_id", wctx.GetWorkitemId())

	return nil
}
