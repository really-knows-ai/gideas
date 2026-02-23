// Package service implements the LibrarianService gRPC server.
//
// This file adds hearing trigger logic: the Librarian subscribes to the
// friction channel on the Event Bus and creates review hearing Workitems
// via the Operator when friction thresholds are crossed. It also runs a
// periodic review-TTL-expiry scanner.

package service

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/gideas/flow/librarian/internal/store/sqlite"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"google.golang.org/grpc"
)

// ---------------------------------------------------------------------------
// Interfaces
// ---------------------------------------------------------------------------

// EventBusSubscriber abstracts the Event Bus Subscribe RPC for testing.
type EventBusSubscriber interface {
	Subscribe(
		ctx context.Context, req *flowv1.SubscribeRequest,
		opts ...grpc.CallOption,
	) (grpc.ServerStreamingClient[flowv1.FlowEvent], error)
}

// HearingCreator abstracts the Operator's CreateHearingWorkitem RPC.
type HearingCreator interface {
	CreateHearingWorkitem(
		ctx context.Context, req *flowv1.CreateHearingWorkitemRequest,
		opts ...grpc.CallOption,
	) (*flowv1.CreateHearingWorkitemResponse, error)
}

// ---------------------------------------------------------------------------
// Review TTL configuration
// ---------------------------------------------------------------------------

// ReviewTTLConfig holds per-tier TTL durations for review-TTL-expiry
// triggers. Zero means "no TTL configured for this tier".
type ReviewTTLConfig struct {
	Tier1 time.Duration
	Tier2 time.Duration
	Tier3 time.Duration
	Tier4 time.Duration
	Tier5 time.Duration
}

// ForTier returns the configured TTL for the given tier (1-5).
// Returns zero if unconfigured.
func (c ReviewTTLConfig) ForTier(tier int) time.Duration {
	switch tier {
	case 1:
		return c.Tier1
	case 2:
		return c.Tier2
	case 3:
		return c.Tier3
	case 4:
		return c.Tier4
	case 5:
		return c.Tier5
	default:
		return 0
	}
}

// ---------------------------------------------------------------------------
// HearingTrigger
// ---------------------------------------------------------------------------

// HearingTrigger manages friction-channel subscriptions and review-TTL-expiry
// scanning. It coordinates with the Operator to create hearing Workitems.
type HearingTrigger struct {
	subscriber EventBusSubscriber
	operator   HearingCreator
	store      *sqlite.Store
	ttlConfig  ReviewTTLConfig
	scanPeriod time.Duration // how often to scan for TTL expiry
	auditor    AuditPublisher

	// pendingHearings tracks law IDs for which a hearing has already been
	// requested, preventing duplicate hearing creation.
	pendingMu       sync.Mutex
	pendingHearings map[string]struct{}

	// nowFn allows tests to override the clock.
	nowFn func() time.Time
}

// HearingTriggerConfig holds the configuration for creating a HearingTrigger.
type HearingTriggerConfig struct {
	Subscriber EventBusSubscriber
	Operator   HearingCreator
	Store      *sqlite.Store
	TTLConfig  ReviewTTLConfig
	ScanPeriod time.Duration
	Auditor    AuditPublisher
}

// NewHearingTrigger creates a HearingTrigger. scanPeriod defaults to 5 min
// if zero.
func NewHearingTrigger(cfg HearingTriggerConfig) *HearingTrigger {
	if cfg.ScanPeriod <= 0 {
		cfg.ScanPeriod = 5 * time.Minute
	}
	return &HearingTrigger{
		subscriber:      cfg.Subscriber,
		operator:        cfg.Operator,
		store:           cfg.Store,
		ttlConfig:       cfg.TTLConfig,
		scanPeriod:      cfg.ScanPeriod,
		auditor:         cfg.Auditor,
		pendingHearings: make(map[string]struct{}),
		nowFn:           time.Now,
	}
}

// Run starts both the friction-channel subscriber and the review-TTL scanner.
// It blocks until ctx is cancelled.
func (ht *HearingTrigger) Run(ctx context.Context) {
	var wg sync.WaitGroup

	if ht.subscriber != nil && ht.operator != nil {
		wg.Go(func() {
			ht.subscribeFriction(ctx)
		})
	} else {
		slog.Info("Hearing trigger: friction subscriber disabled (missing Event Bus or Operator client)")
	}

	if ht.operator != nil {
		wg.Go(func() {
			ht.scanReviewTTL(ctx)
		})
	} else {
		slog.Info("Hearing trigger: TTL scanner disabled (missing Operator client)")
	}

	wg.Wait()
}

// ---------------------------------------------------------------------------
// Friction-channel subscription
// ---------------------------------------------------------------------------

// subscribeFriction opens a streaming subscription to the friction channel
// on the Event Bus and processes threshold-crossing events.
func (ht *HearingTrigger) subscribeFriction(ctx context.Context) {
	for {
		if err := ht.subscribeOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return // Context cancelled, shutting down.
			}
			slog.Warn("Friction subscription interrupted, reconnecting",
				"error", err,
			)
			// Backoff before reconnect.
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}
}

func (ht *HearingTrigger) subscribeOnce(ctx context.Context) error {
	stream, err := ht.subscriber.Subscribe(ctx, &flowv1.SubscribeRequest{
		Channel: flowv1.EventChannel_EVENT_CHANNEL_FRICTION,
		Filter: &flowv1.SubscribeFilter{
			EventType: "friction.threshold_crossed",
		},
	})
	if err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	slog.Info("Friction channel subscription active")

	for {
		evt, err := stream.Recv()
		if err != nil {
			if err == io.EOF || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("recv: %w", err)
		}

		lawID := evt.GetAttributes()["law_id"]
		if lawID == "" {
			slog.Warn("Received threshold_crossed event without law_id, skipping")
			continue
		}

		ht.triggerHearing(ctx, lawID, "friction_threshold")
	}
}

// ---------------------------------------------------------------------------
// Review-TTL-expiry scanner
// ---------------------------------------------------------------------------

// scanReviewTTL periodically scans all active laws and triggers hearings
// for those whose age exceeds their tier's configured review TTL.
func (ht *HearingTrigger) scanReviewTTL(ctx context.Context) {
	slog.Info("Review-TTL scanner started", "scan_period", ht.scanPeriod)

	// Run an initial scan immediately.
	ht.doScan(ctx)

	ticker := time.NewTicker(ht.scanPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Review-TTL scanner stopped")
			return
		case <-ticker.C:
			ht.doScan(ctx)
		}
	}
}

func (ht *HearingTrigger) doScan(ctx context.Context) {
	// Query all active laws.
	laws, err := ht.store.QueryLaws(ctx, sqlite.QueryFilter{})
	if err != nil {
		slog.Warn("Review-TTL scan failed", "error", err)
		return
	}

	now := ht.nowFn()
	for _, law := range laws {
		ttl := ht.ttlConfig.ForTier(law.Tier)
		if ttl <= 0 {
			continue // No TTL configured for this tier.
		}

		age := now.Sub(law.UpdatedAt)
		if age >= ttl {
			ht.triggerHearing(ctx, law.ID, "review_ttl_expiry")
		}
	}
}

// ---------------------------------------------------------------------------
// Hearing creation
// ---------------------------------------------------------------------------

// triggerHearing creates a hearing Workitem for the given law if one hasn't
// already been triggered. The reason parameter is logged for diagnostics.
func (ht *HearingTrigger) triggerHearing(ctx context.Context, lawID, reason string) {
	ht.pendingMu.Lock()
	if _, exists := ht.pendingHearings[lawID]; exists {
		ht.pendingMu.Unlock()
		return // Hearing already pending.
	}
	ht.pendingHearings[lawID] = struct{}{}
	ht.pendingMu.Unlock()

	slog.Info("Triggering review hearing",
		"law_id", lawID,
		"reason", reason,
	)

	resp, err := ht.operator.CreateHearingWorkitem(ctx, &flowv1.CreateHearingWorkitemRequest{
		LawId: lawID,
	})
	if err != nil {
		slog.Error("Failed to create hearing Workitem",
			"law_id", lawID,
			"reason", reason,
			"error", err,
		)
		// Remove from pending so it can be retried.
		ht.pendingMu.Lock()
		delete(ht.pendingHearings, lawID)
		ht.pendingMu.Unlock()
		return
	}

	slog.Info("Hearing Workitem created",
		"law_id", lawID,
		"workitem_id", resp.GetWorkitemId(),
		"reason", reason,
	)

	// Publish audit event.
	if ht.auditor != nil {
		_, err := ht.auditor.Publish(ctx, &flowv1.PublishRequest{
			Channel: flowv1.EventChannel_EVENT_CHANNEL_AUDIT,
			Event: &flowv1.FlowEvent{
				EventId:   newHearingEventID(),
				EventType: "audit.hearing.triggered",
				Timestamp: nil, // Set by Event Bus.
				Attributes: map[string]string{
					"law_id":      lawID,
					"reason":      reason,
					"workitem_id": resp.GetWorkitemId(),
				},
			},
		})
		if err != nil {
			slog.Warn("Audit publish failed for hearing trigger",
				"law_id", lawID,
				"error", err,
			)
		}
	}
}

// ClearPending removes a law from the pending hearings set. This should be
// called when a hearing is completed to allow re-triggering if needed.
func (ht *HearingTrigger) ClearPending(lawID string) {
	ht.pendingMu.Lock()
	delete(ht.pendingHearings, lawID)
	ht.pendingMu.Unlock()
}

func newHearingEventID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return fmt.Sprintf("%x", b)
}
