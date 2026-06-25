// Petition Outcome Watcher is an entry-bound watcher node for the Foundry
// Flow judiciary subsystem.
//
// It subscribes to the Federation service for petition-outcome events
// (accepted/rejected) and creates workitems for downstream processing.
// The handler is a stub in this scaffold — detailed outcome processing
// (retire dispute records, resume held workitems, create Clerk cycles)
// will be implemented in later slices.
//
// Architecture:
//   - Entry function: subscribes to Federation via FederationClient,
//     creates workitems on petition outcome events.
//   - Handler: stub — heartbeats and completes the workitem.
//   - Dedup: per-replica in-memory tracking of petition IDs (best-effort).
//
// Uses the SDK StartEntry pattern: the entry function and handler server run
// concurrently, with shared-nothing semantics between them.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	"github.com/gideas/flow/nodes/internal"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

const (
	// envFlowIdentity is the environment variable that provides the
	// subscribing Flow's identity to the Federation service.
	envFlowIdentity = "FLOW_IDENTITY"

	// reconnectBaseDelay is the initial backoff delay for reconnecting to
	// the Federation after a stream error.
	reconnectBaseDelay = 1 * time.Second

	// reconnectMaxDelay caps the exponential backoff.
	reconnectMaxDelay = 30 * time.Second
)

func main() {
	slog.Info("petition-watcher: starting")
	if err := flow.StartEntry(watchOutcomes, handleOutcome); err != nil {
		slog.Error("petition-watcher: failed", "error", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Entry function — subscribes to Federation, logs petition outcome events
// ---------------------------------------------------------------------------

// watchOutcomes is the entry function. It creates a FederationClient and
// runs a reconnect loop subscribing to petition outcome events.
func watchOutcomes(ctx context.Context, entry *flow.EntryClient) error {
	flowIdentity := os.Getenv(envFlowIdentity)
	if flowIdentity == "" {
		flowIdentity = "unknown"
		slog.Warn("petition-watcher: FLOW_IDENTITY not set, using 'unknown'")
	}

	fedClient, err := flow.NewFederationClient()
	if err != nil {
		return fmt.Errorf("petition-watcher: create federation client: %w", err)
	}
	defer func() { _ = fedClient.Close() }()

	return watchOutcomesWithClient(ctx, fedClient, flowIdentity, entry)
}

// watchOutcomesWithClient runs the reconnect loop using the given
// FederationClient. Extracted for testability.
func watchOutcomesWithClient(
	ctx context.Context,
	fedClient *flow.FederationClient,
	flowIdentity string,
	entry *flow.EntryClient,
) error {
	tracker := internal.NewPendingTracker()
	delay := reconnectBaseDelay

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		stream, err := fedClient.SubscribePetitionOutcomes(ctx, flowIdentity)
		if err != nil {
			slog.Warn("petition-watcher: subscribe failed, retrying",
				"error", err, "delay", delay)
			if !sleepCtx(ctx, delay) {
				return ctx.Err()
			}
			delay = nextBackoff(delay)
			continue
		}

		// Reset backoff on successful subscribe.
		delay = reconnectBaseDelay
		slog.Info("petition-watcher: subscribed to Federation",
			"flow_identity", flowIdentity)

		// Consume events from the stream.
		if err := consumeOutcomes(ctx, stream, tracker, entry); err != nil {
			slog.Warn("petition-watcher: stream ended, reconnecting",
				"error", err)
			continue
		}
	}
}

// consumeOutcomes reads petition outcome events from the stream and
// processes them. For ACCEPTED outcomes, it retires the dispute record via
// the Librarian (best-effort). Returns nil on EOF, or an error on stream
// failure.
func consumeOutcomes(
	ctx context.Context,
	stream *flow.PetitionOutcomeStream,
	tracker *internal.PendingTracker,
	entry *flow.EntryClient,
) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		evt, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("recv: %w", err)
		}

		petitionID := evt.GetPetitionId()
		if petitionID == "" {
			slog.Warn("petition-watcher: event missing petition_id, skipping")
			continue
		}

		// Best-effort dedup: skip if already pending on this replica.
		if !tracker.MarkPending(petitionID) {
			slog.Debug("petition-watcher: petition_id already pending, skipping",
				"petition_id", petitionID)
			continue
		}

		if flow.IsPetitionAccepted(evt) {
			slog.Info("petition-watcher: petition accepted",
				"petition_id", petitionID,
				"published_law_id", evt.GetPublishedLawId())
			handleAccepted(ctx, entry, petitionID)
		} else if flow.IsPetitionRejected(evt) {
			slog.Info("petition-watcher: petition rejected",
				"petition_id", petitionID)
			handleRejected(ctx, entry, petitionID, evt)
		} else {
			slog.Warn("petition-watcher: unknown outcome",
				"petition_id", petitionID,
				"outcome", evt.GetOutcome())
		}
	}
}

// handleAccepted processes an accepted petition: retires the dispute record
// via the Librarian. Errors are logged but do not stop processing (best-effort).
// Held workitem discovery and resume will be added in slice 13.10.4.
func handleAccepted(ctx context.Context, entry *flow.EntryClient, petitionID string) {
	if entry == nil {
		slog.Warn("petition-watcher: no entry client, skipping retire dispute record",
			"petition_id", petitionID)
		return
	}

	if err := entry.RetireDisputeRecord(ctx, petitionID); err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			slog.Warn("petition-watcher: dispute record already retired",
				"petition_id", petitionID)
		} else {
			slog.Warn("petition-watcher: retire dispute record failed",
				"petition_id", petitionID, "error", err)
		}
		return
	}

	slog.Info("petition-watcher: dispute record retired",
		"petition_id", petitionID)
	// Discover and resume held workitems whose suspend condition references
	// this petition_id.
	resumeHeldWorkitems(ctx, entry, petitionID)
}

// rejectionReport is the JSON-serializable structure stored in the
// rejection_report metadata key for Clerk-Forge to interpret.
type rejectionReport struct {
	PetitionID        string   `json:"petition_id"`
	Reason            string   `json:"reason"`
	ConflictingLawIDs []string `json:"conflicting_law_ids"`
	RemediationText   string   `json:"remediation_text"`
}

// handleRejected processes a rejected petition: retires the dispute record
// (best-effort, same as acceptance) and creates a new Clerk cycle Workitem
// with rejection context metadata. Errors are logged but do not stop
// processing.
func handleRejected(ctx context.Context, entry *flow.EntryClient, petitionID string, evt *flowv1.PetitionOutcomeEvent) {
	if entry == nil {
		slog.Warn("petition-watcher: no entry client, skipping rejection handling",
			"petition_id", petitionID)
		return
	}

	// Step 1: Retire dispute record (best-effort, same as acceptance path).
	if err := entry.RetireDisputeRecord(ctx, petitionID); err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			slog.Warn("petition-watcher: dispute record already retired",
				"petition_id", petitionID)
		} else {
			slog.Warn("petition-watcher: retire dispute record failed",
				"petition_id", petitionID, "error", err)
		}
		// Best-effort: continue to create Clerk cycle workitem regardless.
	} else {
		slog.Info("petition-watcher: dispute record retired",
			"petition_id", petitionID)
	}

	// Step 2: Build rejection report JSON from event's rejection field.
	rej := evt.GetRejection()
	report := rejectionReport{
		PetitionID: petitionID,
	}
	if rej != nil {
		report.Reason = rej.GetReason().String()
		report.ConflictingLawIDs = rej.GetConflictingLawIds()
		report.RemediationText = rej.GetRemediationText()
	}

	reportBytes, err := json.Marshal(report)
	if err != nil {
		slog.Warn("petition-watcher: failed to marshal rejection report",
			"petition_id", petitionID, "error", err)
		return
	}

	// Step 3: Create new Clerk cycle Workitem with rejection context.
	metadata := map[string]string{
		"petition_id":      petitionID,
		"trigger":          "petition-rejected",
		"rejection_report": string(reportBytes),
	}

	wiID, err := entry.CreateWorkitem(ctx, metadata)
	if err != nil {
		slog.Warn("petition-watcher: create clerk cycle workitem failed",
			"petition_id", petitionID, "error", err)
		return
	}

	slog.Info("petition-watcher: clerk cycle workitem created",
		"petition_id", petitionID, "workitem_id", wiID)
	// Discover and resume held workitems whose suspend condition references
	// this petition_id.
	resumeHeldWorkitems(ctx, entry, petitionID)
}

// ---------------------------------------------------------------------------
// Handler — processes assigned petition-outcome workitems (stub)
// ---------------------------------------------------------------------------

// handleOutcome is the SDK handler entry point for petition-outcome workitems.
// This is a stub — detailed processing will be added in later slices.
func handleOutcome(ctx context.Context, wctx *flowv1.WorkitemContext) error {
	_ = os.Setenv(flow.EnvWorkitemID, wctx.GetWorkitemId())
	client, err := flow.NewClient()
	if err != nil {
		return fmt.Errorf("petition-watcher: handler: create client: %w", err)
	}
	defer func() { _ = client.Close() }()

	return processOutcome(ctx, client, wctx)
}

// processOutcome performs the stub handler logic: heartbeat and complete.
// Later slices will add: retire dispute record, resume held workitems,
// create Clerk cycle on rejection.
func processOutcome(ctx context.Context, client *flow.Client, wctx *flowv1.WorkitemContext) error {
	slog.Info("petition-watcher: handling outcome (stub)",
		"workitem_id", wctx.GetWorkitemId(),
		"metadata", wctx.GetMetadata(),
	)

	// Send a heartbeat to signal liveness.
	if _, err := client.Heartbeat(ctx); err != nil {
		return fmt.Errorf("petition-watcher: handler: heartbeat: %w", err)
	}

	// Stub: complete the workitem (no routing needed yet).
	if _, err := client.Complete(ctx); err != nil {
		return fmt.Errorf("petition-watcher: handler: complete: %w", err)
	}

	slog.Info("petition-watcher: handler completed (stub)",
		"workitem_id", wctx.GetWorkitemId())

	return nil
}

// ---------------------------------------------------------------------------
// Utility
// ---------------------------------------------------------------------------

// resumeHeldWorkitems discovers suspended workitems whose resume condition
// contains the petition_id and resumes each one. Errors are logged but do not
// stop processing (best-effort). A failure to resume one workitem does not
// prevent resuming others.
func resumeHeldWorkitems(ctx context.Context, entry *flow.EntryClient, petitionID string) {
	workitemIDs, err := entry.ListSuspendedWorkitems(ctx, petitionID)
	if err != nil {
		slog.Warn("petition-watcher: list suspended workitems failed",
			"petition_id", petitionID, "error", err)
		return
	}

	if len(workitemIDs) == 0 {
		slog.Info("petition-watcher: no held workitems found",
			"petition_id", petitionID)
		return
	}

	for _, wiID := range workitemIDs {
		if err := entry.ResumeWorkitem(ctx, wiID); err != nil {
			slog.Warn("petition-watcher: resume workitem failed",
				"workitem_id", wiID, "petition_id", petitionID, "error", err)
		} else {
			slog.Info("petition-watcher: resumed held workitem",
				"workitem_id", wiID, "petition_id", petitionID)
		}
	}
}

// sleepCtx sleeps for the given duration, respecting context cancellation.
// Returns true if the sleep completed, false if cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// nextBackoff doubles the delay up to reconnectMaxDelay.
func nextBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > reconnectMaxDelay {
		return reconnectMaxDelay
	}
	return next
}
