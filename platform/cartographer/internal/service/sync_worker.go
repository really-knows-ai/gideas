package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// syncClassification classifies a sync error as recoverable or non-recoverable.
type syncClassification int

const (
	syncRecoverable syncClassification = iota
	syncNonRecoverable
)

// SyncWorker manages background remote synchronisation.
type SyncWorker struct {
	pushNeeded atomic.Bool
	wakeCh     chan struct{}
	stopCh     chan struct{}
	doneCh     chan struct{} // closed when the worker loop exits

	remoteURL string
	gitstore  gitstore.GitStore
	store     store.Store

	// auditor receives operator-visible telemetry events for permanent sync
	// failures (SPEC R10 / GIT_PLAN "log loudly + telemetry": "An
	// operator-visible telemetry event fires on each permanent failure").
	auditor TelemetryPublisher

	// cycleMu guards the per-waiter completion registry and the last cycle
	// error. Each waiter owns its completion channel; a cycle snapshots and
	// drains the registry at its start and feeds exactly that set at its end,
	// so a waiter registered mid-cycle is satisfied by the follow-up cycle the
	// buffered wakeCh triggers — never by the in-flight cycle whose snapshot
	// predates the registration.
	cycleMu  sync.Mutex
	cycleErr error              // last completed cycle's error (for direct runSyncCycle callers)
	waiters  []chan cycleResult // per-waiter completion channels; drained at each cycle start

	clock     Clock                           // for test injection
	backoffFn func(attempt int) time.Duration // for test injection; defaults to syncBackoff
}

// SyncWorkerOption configures a SyncWorker.
type SyncWorkerOption func(*SyncWorker)

// SyncWorkerWithAuditPublisher wires a telemetry publisher into a SyncWorker so
// permanent sync failures emit an operator-visible Event Bus event (SPEC R10 /
// GIT_PLAN "log loudly + telemetry"). Named distinctly from the server's
// WithAuditPublisher (a CartographerOption) because both live in this package.
func SyncWorkerWithAuditPublisher(pub TelemetryPublisher) SyncWorkerOption {
	return func(w *SyncWorker) { w.auditor = pub }
}

// NewSyncWorker creates a new SyncWorker.
func NewSyncWorker(
	remoteURL string,
	gs gitstore.GitStore,
	s store.Store,
	clock Clock,
	opts ...SyncWorkerOption,
) *SyncWorker {
	w := &SyncWorker{
		wakeCh:    make(chan struct{}, 1),
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
		remoteURL: remoteURL,
		gitstore:  gs,
		store:     s,
		clock:     clock,
		backoffFn: syncBackoff,
	}
	for _, o := range opts {
		o(w)
	}
	return w
}

// SetPushNeeded flags that a commit needs pushing. Called by CommitTransaction.
func (w *SyncWorker) SetPushNeeded() {
	w.pushNeeded.Store(true)
}

// cycleResult is the outcome of one sync cycle.
type cycleResult struct {
	err            error
	classification syncClassification
}

// WakeAndWait wakes the worker and blocks until the cycle completes.
// Returns the cycle's error: nil on success, or the worker's last error when
// the cycle ended with the push flag still set (permanent failure or retries
// exhausted). Callers that must distinguish non-recoverable outcomes
// (SPEC R10 Sync) use WakeAndWaitClassified.
func (w *SyncWorker) WakeAndWait(ctx context.Context) error {
	result, completed := w.wakeAndWait(ctx)
	if !completed {
		return ctx.Err()
	}
	return result.err
}

// WakeAndWaitClassified is WakeAndWait plus the completed cycle's error
// classification, so the Sync RPC handler can propagate only non-recoverable
// cycle errors (SPEC R10: "If the cycle encounters a non-recoverable error,
// returns the worker's last error"). completed is false only when the caller's
// context expired before the cycle finished.
func (w *SyncWorker) WakeAndWaitClassified(ctx context.Context) (completed bool, class syncClassification, err error) {
	result, completed := w.wakeAndWait(ctx)
	if !completed {
		return false, syncRecoverable, ctx.Err()
	}
	return true, result.classification, result.err
}

// wakeAndWait registers a per-waiter completion channel, wakes the worker, and
// blocks until the cycle that snapshotted this waiter completes. A waiter
// registered while a cycle is already in flight is satisfied by the follow-up
// cycle the buffered wakeCh triggers — never by the in-flight cycle, whose
// waiter snapshot predates the registration (GIT_PLAN WithAck/timer-race edge
// case). Returns completed=false when ctx expires first; the abandoned waiter
// channel is harmlessly fed by the next cycle's snapshot drain.
func (w *SyncWorker) wakeAndWait(ctx context.Context) (cycleResult, bool) {
	done := make(chan cycleResult, 1)
	w.cycleMu.Lock()
	w.waiters = append(w.waiters, done)
	w.cycleMu.Unlock()

	// Signal the worker (non-blocking — buffer absorbs if worker is already running)
	select {
	case w.wakeCh <- struct{}{}:
	default:
	}

	select {
	case result := <-done:
		return result, true
	case <-ctx.Done():
		return cycleResult{}, false
	}
}

// Run starts the main worker loop. Call in a goroutine.
func (w *SyncWorker) Run() {
	w.run()
}

// Stop signals the worker to shut down. Blocks until the loop exits.
func (w *SyncWorker) Stop() {
	select {
	case <-w.stopCh:
		// Already stopped.
	default:
		close(w.stopCh)
	}
	<-w.doneCh
}

// run is the main worker loop.
func (w *SyncWorker) run() {
	defer close(w.doneCh)
	// Run one initial cycle for startup catch-up push if remote is configured.
	w.runSyncCycle()

	ticker := w.clock.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C():
			w.runSyncCycle()
		case <-w.wakeCh:
			w.runSyncCycle()
		case <-w.stopCh:
			// Final sync cycle on shutdown — attempt to deliver pending push.
			w.runSyncCycle()
			return
		}
	}
}

// runSyncCycle runs one full sync cycle: fetch → merge → re-hydrate → push.
func (w *SyncWorker) runSyncCycle() {
	w.cycleMu.Lock()
	// Snapshot the waiters registered before this cycle's work began; a waiter
	// that registers mid-cycle is satisfied by the follow-up cycle the buffered
	// wakeCh triggers, never by this one — so a cycle that began before a
	// SetPushNeeded can never acknowledge a fresh WithAck waiter before the
	// push is delivered.
	waiters := w.waiters
	w.waiters = nil
	w.cycleMu.Unlock()

	result := w.doSyncCycle()

	w.cycleMu.Lock()
	w.cycleErr = result.err
	for _, done := range waiters {
		done <- result
	}
	w.cycleMu.Unlock()
}

// doSyncCycle performs the actual sync work.
func (w *SyncWorker) doSyncCycle() cycleResult {
	if w.remoteURL == "" {
		return cycleResult{}
	}

	// 1. FetchAndMerge from the remote, then re-hydrate main if new data was
	// pulled. The whole fetch → (restore main) → re-hydrate sequence holds the
	// git lock so a concurrent commit's working-tree writes cannot interleave,
	// and the tree is restored to main before files are read so a transaction
	// branch can never leak uncommitted data into main.lbug (SPEC R10 / GIT_PLAN
	// Phase 2 step 3).
	res := w.fetchAndRehydrate()
	if res.err != nil {
		return res
	}

	// 2. If push needed, push to remote.
	if !w.pushNeeded.Load() {
		return cycleResult{}
	}

	pushErr := w.gitstore.WithGitLock(func() error {
		return w.gitstore.PushRemote(context.Background())
	})
	if pushErr != nil {
		class := classifySyncError(pushErr)
		if class == syncNonRecoverable {
			slog.Warn("sync: non-recoverable push error", "error", pushErr)
			w.publishFailure("push", pushErr)
			return cycleResult{err: pushErr, classification: syncNonRecoverable}
		}
		// Recoverable — retry with backoff.
		for attempt := 1; attempt < 3; attempt++ {
			time.Sleep(w.backoffFn(attempt))
			pushErr = w.gitstore.WithGitLock(func() error {
				return w.gitstore.PushRemote(context.Background())
			})
			if pushErr == nil {
				break
			}
		}
		if pushErr != nil {
			slog.Error("sync: push failed after retries", "error", pushErr)
			w.publishFailure("push", pushErr)
			return cycleResult{err: pushErr, classification: syncRecoverable}
		}
	}

	// Push succeeded — clear the flag.
	w.pushNeeded.Store(false)
	return cycleResult{}
}

// hydrateError marks a re-hydration failure, distinguishing it from a fetch
// error: a failed RehydrateMainFromFiles is non-recoverable (GIT_PLAN edge
// case: surfaced as "Sync re-hydration failed" (INTERNAL) to waiting callers;
// not retried within the cycle).
type hydrateError struct{ err error }

func (e *hydrateError) Error() string { return e.err.Error() }
func (e *hydrateError) Unwrap() error { return e.err }

// fetchAndRehydrate runs FetchAndMerge and, when the remote had new data,
// re-hydrates main.lbug from the updated working tree. Recoverable fetch
// errors are retried up to 3 attempts with backoff; non-recoverable and
// retries-exhausted failures return with the error classified and an
// operator-visible telemetry event emitted (SPEC R10 error table).
func (w *SyncWorker) fetchAndRehydrate() cycleResult {
	ctx := context.Background()
	attempt := 0
	for {
		res := w.fetchAttempt(ctx)
		if res.err == nil {
			return cycleResult{}
		}
		if errors.Is(res.err, gitstore.ErrNoRemote) {
			return cycleResult{}
		}
		var he *hydrateError
		if errors.As(res.err, &he) {
			slog.Error("sync: re-hydration failed", "error", res.err)
			w.publishFailure("hydrate", res.err)
			return res
		}
		if res.classification == syncNonRecoverable {
			slog.Warn("sync: non-recoverable fetch error", "error", res.err)
			w.publishFailure("fetch", res.err)
			return res
		}
		if attempt >= 2 {
			slog.Error("sync: fetch failed after retries", "error", res.err)
			w.publishFailure("fetch", res.err)
			return res
		}
		attempt++
		time.Sleep(w.backoffFn(attempt))
	}
}

// fetchAttempt performs one git-locked fetch attempt followed by the
// re-hydration of main.lbug when the fetch advanced main.
func (w *SyncWorker) fetchAttempt(ctx context.Context) cycleResult {
	var hydrateErr error
	lockErr := w.gitstore.WithGitLock(func() error {
		preHead, headErr := w.gitstore.BranchHEAD(ctx, "main")
		if headErr != nil && !errors.Is(headErr, gitstore.ErrBranchNotFound) {
			return headErr
		}
		newHead, err := w.gitstore.FetchAndMerge(ctx, "origin", "main")
		if err != nil {
			return err
		}
		// Re-hydrate only when the remote actually had new data (SPEC R10: "if
		// new data was pulled re-hydrates"; GIT_PLAN.md:30,84). FetchAndMerge
		// returns the unchanged local hash when the remote is up-to-date or
		// strictly behind, so the HEAD comparison is the new-data signal.
		if newHead.IsZero() || newHead.String() == preHead {
			return nil
		}
		// The working tree must be on main before files are read: with a
		// transaction open it is checked out on the transaction branch, and
		// re-hydrating from it would publish uncommitted transaction data into
		// main.lbug. RestoreMain + CleanUntracked make the tree exactly main;
		// both are no-ops when the tree already is main.
		if err := w.gitstore.RestoreMain(ctx); err != nil {
			hydrateErr = &hydrateError{err: fmt.Errorf("restore main before re-hydration: %w", err)}
			return nil
		}
		if err := w.gitstore.CleanUntracked(ctx); err != nil {
			hydrateErr = &hydrateError{err: fmt.Errorf("clean working tree before re-hydration: %w", err)}
			return nil
		}
		entitiesDir, edgesDir := w.gitstore.HydrationDirs()
		// RehydrateMainFromFiles holds the LadybugDB write lock (db.mu) for its
		// entire wipe-and-load cycle.
		if err := w.store.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir); err != nil {
			hydrateErr = &hydrateError{err: fmt.Errorf("sync re-hydration failed: %w", err)}
		}
		return nil
	})
	if lockErr != nil {
		return cycleResult{err: lockErr, classification: classifySyncError(lockErr)}
	}
	if hydrateErr != nil {
		return cycleResult{err: hydrateErr, classification: syncNonRecoverable}
	}
	return cycleResult{}
}

// publishFailure emits an operator-visible Event Bus telemetry event for a
// permanent sync-cycle failure (SPEC.md:122, GIT_PLAN:31-32,132 "log loudly +
// telemetry"). Event type "cartographer.push_failed" matches the convention
// pinned by the service tests.
func (w *SyncWorker) publishFailure(operation string, err error) {
	if w.auditor == nil {
		return
	}
	w.auditor.Submit(&flowv1.PublishRequest{
		Channel: "telemetry",
		Event: &flowv1.FlowEvent{
			EventId:   uuid.NewString(),
			EventType: "cartographer.push_failed",
			NodeId:    "cartographer",
			Timestamp: timestamppb.Now(),
			Attributes: map[string]string{
				"operation": operation,
				"error":     err.Error(),
			},
		},
	})
}

// syncBackoff returns backoff duration for attempt n (1-indexed).
func syncBackoff(attempt int) time.Duration {
	switch attempt {
	case 1:
		return 1 * time.Second
	case 2:
		return 2 * time.Second
	default:
		return 4 * time.Second
	}
}

// classifySyncError classifies a remote-sync error.
func classifySyncError(err error) syncClassification {
	if err == nil {
		return syncRecoverable // shouldn't happen, but safe
	}
	// Non-recoverable: auth failures, divergence, push rejection
	if errors.Is(err, gitstore.ErrAuthFailed) ||
		errors.Is(err, gitstore.ErrAuthConfigMissing) ||
		errors.Is(err, gitstore.ErrPullDiverged) ||
		errors.Is(err, gitstore.ErrPushRejected) {
		return syncNonRecoverable
	}
	// Recoverable: network errors, timeouts, connection refused
	return syncRecoverable
}
