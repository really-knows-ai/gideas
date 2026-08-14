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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// syncClassification classifies a sync error as recoverable or non-recoverable.
type syncClassification int

const (
	syncRecoverable syncClassification = iota
	syncNonRecoverable
)

// DefaultSyncInterval is the background sync worker's periodic cycle interval
// (SPEC R10: the worker "wakes every minute (configurable)"). Single source of
// truth for the default: cmd/main.go derives the SYNC_INTERVAL env fallback
// from this constant, so the wiring default and the worker default cannot
// silently diverge.
const DefaultSyncInterval = time.Minute

// DefaultGitOperationTimeout is the per-operation deadline for git operations
// in the sync worker cycle (SPEC R10: "a configurable deadline the worker
// derives per operation (default: 5 minutes)" — a hung remote aborts the
// operation with DEADLINE_EXCEEDED, SPEC:978, instead of wedging the worker
// permanently). Single source of truth for the default, mirroring
// DefaultSyncInterval.
const DefaultGitOperationTimeout = 5 * time.Minute

// SyncWorker manages background remote synchronisation.
type SyncWorker struct {
	// pushNeeded is the externally-visible "a push is pending" signal, written
	// by SetPushNeeded and cleared by the worker after a push delivers. It is
	// a bool view over the monotonic pushGeneration counter: clearing is
	// conditional on the generation being unchanged since the push began, so a
	// concurrent SetPushNeeded can never be wiped by a clear (SPEC R10 WithAck
	// contract — see SetPushNeeded and doSyncCycle).
	pushNeeded atomic.Bool
	// pushGeneration counts SetPushNeeded calls. A successful push clears
	// pushNeeded only when no new request arrived while the push was in
	// flight; the generation snapshot is how the worker detects that arrival.
	pushGeneration atomic.Uint64
	wakeCh         chan struct{}
	stopCh         chan struct{}
	doneCh         chan struct{} // closed when the worker loop exits
	// stopOnce guards the stopCh close: Stop() may be called from multiple
	// goroutines (shutdown path plus t.Cleanup), so the signal must be closed
	// exactly once — the select/default close-once idiom is a data race (two
	// concurrent Stop() calls can both close and panic).
	stopOnce sync.Once

	remoteURL string
	gitstore  gitstore.GitStore
	store     store.Store

	// auditor receives operator-visible telemetry events for permanent sync
	// failures (SPEC R10 error-classification contract "log loudly +
	// telemetry": the non-recoverable and retries-exhausted branches both emit
	// an operator-visible telemetry event).
	auditor TelemetryPublisher

	// podNamespace is the Kubernetes namespace owning this flow, stamped onto
	// sync-failure telemetry events (FlowEvent.flow_namespace: "Kubernetes
	// namespace that owns this flow") so push-failure events can be attributed
	// to the flow that owns the remote. Mirrors the server's publishTelemetry
	// (FlowNamespace: s.podNamespace) so the two emitters in the binary stay
	// consistent.
	podNamespace string

	// hydrateFailed records that the last cycle's post-fetch re-hydration of
	// main.lbug failed. The git files are already merged (the fetch succeeded),
	// so the next cycle re-runs the re-hydration even though HEAD no longer
	// moves — without this, main.lbug stays inconsistent until the remote
	// advances again or the pod restarts (the next sync cycle retries the
	// re-hydration — the git files are already merged, so re-hydration is a
	// read from the working tree). Touched only by the
	// worker goroutine, which owns every sync cycle.
	hydrateFailed bool

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

	// syncInterval is the periodic sync cycle interval (SPEC R10: "wakes every
	// minute (configurable)"). Defaults to DefaultSyncInterval; overridable via
	// SyncWorkerWithSyncInterval.
	syncInterval time.Duration

	// gitOpTimeout bounds each git operation in the sync cycle (SPEC R10,
	// SPEC:978: "a configurable deadline the worker derives per operation
	// (default: 5 minutes)"). Defaults to DefaultGitOperationTimeout — the
	// only source for the value: cmd/main.go's clone-on-init and pre-flight
	// auth reads derive the same constant, so the worker and the init paths
	// cannot silently diverge. The knob is not operator-configurable (no env
	// var, no SyncWorkerOption): the SPEC defines only the default, and the
	// wiring surface is kept to what production uses.
	gitOpTimeout time.Duration
}

// SyncWorkerOption configures a SyncWorker.
type SyncWorkerOption func(*SyncWorker)

// SyncWorkerWithAuditPublisher wires a telemetry publisher into a SyncWorker so
// permanent sync failures emit an operator-visible Event Bus event (SPEC R10
// error-classification contract "log loudly + telemetry"). Named distinctly
// from the server's WithAuditPublisher (a CartographerOption) because both live
// in this package.
func SyncWorkerWithAuditPublisher(pub TelemetryPublisher) SyncWorkerOption {
	return func(w *SyncWorker) { w.auditor = pub }
}

// SyncWorkerWithPodNamespace sets the Kubernetes namespace owning this flow,
// stamped onto sync-failure telemetry events (FlowEvent.flow_namespace) so
// push-failure events are attributable to the flow that owns the remote. The
// server's publishTelemetry stamps the same field from its own podNamespace
// (NewCartographerServer), keeping the two telemetry emitters in the binary
// consistent in multi-namespace deployments.
func SyncWorkerWithPodNamespace(ns string) SyncWorkerOption {
	return func(w *SyncWorker) { w.podNamespace = ns }
}

// SyncWorkerWithSyncInterval sets the periodic sync cycle interval (SPEC R10:
// the worker "wakes every minute (configurable)"). The default is
// DefaultSyncInterval (one minute). The duration must be positive —
// time.NewTicker panics on non-positive intervals — so the operator-facing
// wiring (cmd/main.go SYNC_INTERVAL) validates it at startup.
func SyncWorkerWithSyncInterval(d time.Duration) SyncWorkerOption {
	return func(w *SyncWorker) { w.syncInterval = d }
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
		wakeCh:       make(chan struct{}, 1),
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
		remoteURL:    remoteURL,
		gitstore:     gs,
		store:        s,
		clock:        clock,
		backoffFn:    syncBackoff,
		syncInterval: DefaultSyncInterval,
		gitOpTimeout: DefaultGitOperationTimeout,
	}
	for _, o := range opts {
		o(w)
	}
	return w
}

// SetPushNeeded flags that a commit needs pushing. Called by CommitTransaction,
// MergeCompleted, and WipeGraph. Bumping the monotonic pushGeneration lets the
// worker detect that a new request arrived while a push was in flight, so a
// successful push only clears the flag when no request landed since it began —
// a lost SetPushNeeded would let WakeAndWait/WithAck report success for a
// commit never delivered to the remote, with no later timer cycle delivering
// it (SPEC R10 WithAck: "If the cycle ends with the flag cleared (push
// delivered), the call returns success").
func (w *SyncWorker) SetPushNeeded() {
	w.pushGeneration.Add(1)
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
// waiter snapshot predates the registration (the WithAck/timer-race edge
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

// Stop signals the worker to shut down. Blocks until the loop exits. Safe to
// call from multiple goroutines: the stop signal is closed exactly once
// (sync.Once), so concurrent Stop() calls cannot double-close stopCh, and each
// caller still blocks until the loop has exited.
func (w *SyncWorker) Stop() {
	w.stopOnce.Do(func() { close(w.stopCh) })
	<-w.doneCh
}

// run is the main worker loop.
func (w *SyncWorker) run() {
	defer close(w.doneCh)
	// Run one initial cycle for startup catch-up push if remote is configured.
	w.runSyncCycle()

	ticker := w.clock.NewTicker(w.syncInterval)
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
	// ponytail: the empty-remoteURL early return covers a test-only
	// construction — TestSyncWorkerInterval builds a worker over a nil
	// gitstore and relies on the no-op startup cycle (there is no store to
	// fetch into, so the cycle must not run). In production this worker shape
	// cannot arise: cmd/main.go creates the worker only when REMOTE_URL is
	// non-empty and hands it the same value the server's remote gates read
	// (cartographer_server.go — Sync() returns FAILED_PRECONDITION when
	// s.remoteURL == "", BeginTransaction skips the implicit sync), so the
	// server-side gates own the remoteURL == "" cases and this no-op never
	// runs with a mismatched URL. The sibling production misconfiguration — a
	// non-empty REMOTE_URL whose SetRemote was rejected non-fatally at startup
	// (pullOnInit=false is the default, cmd/main.go) leaves the gitstore with
	// no remote — is handled loudly, not here: the gitstore's ErrNoRemote is
	// classified non-recoverable (classifySyncError), so every woken or timer
	// cycle logs + emits telemetry in fetchAndRehydrate and Sync() surfaces
	// FAILED_PRECONDITION "no remote configured" (mapGitError) instead of
	// silently reporting a full cycle (SPEC R10's "one full cycle" promise).
	// Residual divergence: w.remoteURL and the server's s.remoteURL are
	// independent copies with no cross-reference, so a wiring bug that built
	// the worker with "" while the server holds a URL would still mask Sync as
	// success. Upgrade path: assert at wiring time that NewSyncWorker's
	// remoteURL matches the server's, or derive both from one source.
	if w.remoteURL == "" {
		return cycleResult{}
	}

	// 1. FetchAndMerge from the remote, then re-hydrate main if new data was
	// pulled. The whole fetch → (restore main) → re-hydrate sequence holds the
	// git lock so a concurrent commit's working-tree writes cannot interleave,
	// and the tree is restored to main before files are read so a transaction
	// branch can never leak uncommitted data into main.lbug (SPEC R10: the
	// re-hydration after a pull runs under the git lock on the restored
	// working tree).
	res := w.fetchAndRehydrate()
	if res.err != nil {
		return res
	}

	// 2. If push needed, push to remote. Each attempt runs under the
	// configurable per-operation deadline (gitOp — SPEC R10 / SPEC:978), so a
	// hung remote aborts with DEADLINE_EXCEEDED instead of wedging the worker.
	if !w.pushNeeded.Load() {
		return cycleResult{}
	}
	// Snapshot the push-request generation before the attempt: SetPushNeeded
	// bumps it, so after a successful push the flag is cleared only when no
	// new request arrived while the push was in flight. An unconditional clear
	// would lose a concurrent SetPushNeeded (CommitTransaction, MergeCompleted,
	// WipeGraph) landing between push success and the clear — WakeAndWait/
	// WithAck would report success for a commit never delivered to the remote,
	// and no later timer cycle would deliver it (SPEC R10 WithAck contract). A
	// request that arrives while the push is in flight leaves the flag set; the
	// next cycle (the wake its WithAck caller triggers, or the next timer tick)
	// delivers it.
	gen := w.pushGeneration.Load()

	pushErr := w.gitstore.WithGitLock(func() error {
		return w.gitOp(func(ctx context.Context) error {
			return w.gitstore.PushRemote(ctx)
		})
	})
	if pushErr != nil {
		if class := classifySyncError(pushErr); class == syncNonRecoverable {
			slog.Warn("sync: non-recoverable push error", "error", pushErr)
			w.publishFailure("push", pushErr)
			return cycleResult{err: pushErr, classification: syncNonRecoverable}
		}
		// Recoverable — retry with backoff. Every attempt is re-classified
		// (SPEC R10: a retry that surfaces a non-recoverable error — e.g. auth
		// revoked — must stop the cycle: "No retry this cycle"), and the
		// retries-exhausted return classifies the final error rather than
		// assuming a class, so a non-recoverable final error propagates to Sync
		// callers (SPEC:628: "If the cycle encounters a non-recoverable error,
		// returns the worker's last error").
		for attempt := 1; attempt < 3; attempt++ {
			time.Sleep(w.backoffFn(attempt))
			pushErr = w.gitstore.WithGitLock(func() error {
				return w.gitOp(func(ctx context.Context) error {
					return w.gitstore.PushRemote(ctx)
				})
			})
			if pushErr == nil {
				break
			}
			if class := classifySyncError(pushErr); class == syncNonRecoverable {
				slog.Warn("sync: non-recoverable push error on retry", "error", pushErr)
				w.publishFailure("push", pushErr)
				return cycleResult{err: pushErr, classification: syncNonRecoverable}
			}
		}
		if pushErr != nil {
			slog.Error("sync: push failed after retries", "error", pushErr)
			w.publishFailure("push", pushErr)
			return cycleResult{err: pushErr, classification: classifySyncError(pushErr)}
		}
	}

	// Push succeeded — clear the flag only if no new push request arrived while
	// the push was in flight (the generation snapshot above). A request that
	// raced the push stays pending and is delivered by the next cycle instead
	// of being silently lost.
	if w.pushGeneration.Load() == gen {
		w.pushNeeded.Store(false)
	}
	return cycleResult{}
}

// hydrateError marks a re-hydration failure, distinguishing it from a fetch
// error: a failed RehydrateMainFromFiles is non-recoverable (SPEC error-table
// row "Sync re-hydration failed" → INTERNAL: surfaced to waiting callers; not
// retried within the cycle — the retry happens on a later cycle or the next
// startup's R8 recovery).
type hydrateError struct{ err error }

func (e *hydrateError) Error() string { return e.err.Error() }
func (e *hydrateError) Unwrap() error { return e.err }

// fetchAndRehydrate runs FetchAndMerge and, when the remote had new data,
// re-hydrates main.lbug from the updated working tree. Recoverable fetch
// errors are retried up to 3 attempts with backoff; non-recoverable and
// retries-exhausted failures return with the error classified and an
// operator-visible telemetry event emitted (SPEC R10 error table). Each
// attempt's git operations run under the configurable per-operation deadline
// (gitOp, SPEC R10 / SPEC:978), so a hung remote aborts with
// DEADLINE_EXCEEDED instead of wedging the worker.
func (w *SyncWorker) fetchAndRehydrate() cycleResult {
	attempt := 0
	for {
		res := w.fetchAttempt()
		if res.err == nil {
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
// re-hydration of main.lbug when the fetch advanced main or the previous
// cycle's re-hydration failed (the next-cycle re-hydration retry contract). The whole
// locked sequence runs under the configurable per-operation deadline (gitOp,
// SPEC R10 / SPEC:978), so a hung FetchAndMerge aborts with DEADLINE_EXCEEDED
// instead of wedging the worker.
func (w *SyncWorker) fetchAttempt() cycleResult {
	var hydrateErr error
	lockErr := w.gitstore.WithGitLock(func() error {
		return w.gitOp(func(ctx context.Context) error {
			preHead, headErr := w.gitstore.BranchHEAD(ctx, "main")
			if headErr != nil && !errors.Is(headErr, gitstore.ErrBranchNotFound) {
				return headErr
			}
			newHead, err := w.gitstore.FetchAndMerge(ctx, "origin", "main")
			if err != nil {
				return err
			}
			// Re-hydrate when the remote had new data, or when the previous
			// cycle's re-hydration failed and main.lbug is still inconsistent
			// (SPEC R10: "if new data was pulled re-hydrates").
			// FetchAndMerge returns the unchanged local hash when the remote is
			// up-to-date or strictly behind, so the HEAD comparison is the
			// new-data signal. A failed re-hydration is retried by the next cycle
			// — the git files are already merged, so re-hydration
			// is a pure read from the working tree.
			if newHead.IsZero() || (newHead.String() == preHead && !w.hydrateFailed) {
				return nil
			}
			// The working tree must be on main before files are read: with a
			// transaction open it is checked out on the transaction branch, and
			// re-hydrating from it would publish uncommitted transaction data into
			// main.lbug. RestoreMain + CleanUntracked make the tree exactly main;
			// both are no-ops when the tree already is main.
			if err := w.gitstore.RestoreMain(ctx); err != nil {
				w.hydrateFailed = true
				hydrateErr = &hydrateError{err: fmt.Errorf("restore main before re-hydration: %w", err)}
				return nil
			}
			if err := w.gitstore.CleanUntracked(ctx); err != nil {
				w.hydrateFailed = true
				hydrateErr = &hydrateError{err: fmt.Errorf("clean working tree before re-hydration: %w", err)}
				return nil
			}
			entitiesDir, edgesDir := w.gitstore.HydrationDirs()
			// RehydrateMainFromFiles holds the LadybugDB write lock (db.mu) for its
			// entire wipe-and-load cycle.
			if err := w.store.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir); err != nil {
				w.hydrateFailed = true
				hydrateErr = &hydrateError{err: fmt.Errorf("sync re-hydration failed: %w", err)}
			} else {
				w.hydrateFailed = false
			}
			return nil
		})
	})
	if lockErr != nil {
		return cycleResult{err: lockErr, classification: classifySyncError(lockErr)}
	}
	if hydrateErr != nil {
		return cycleResult{err: hydrateErr, classification: syncNonRecoverable}
	}
	return cycleResult{}
}

// gitOp runs a git operation under the worker's configurable per-operation
// deadline (SPEC R10 / SPEC:978: default DefaultGitOperationTimeout, five
// minutes). A hung remote aborts the operation when the deadline fires: the
// operation's context expires, go-git's FetchContext/PushContext return the
// context error, and the worker continues instead of wedging permanently. The
// deadline outcome is wrapped in a gRPC DeadlineExceeded status — which
// mapGitError passes through unchanged (errors.go) — so callers that surface
// the worker's last error (WithAck, SPEC:617-618: "the call returns an error
// with the worker's last push error") report the SPEC:978 code rather than
// INTERNAL. classifySyncError classes the wrapped error as recoverable (a hung
// remote is a network timeout — SPEC:610 recoverable), so the cycle retries
// within its budget and the push flag stays set for the next cycle.
func (w *SyncWorker) gitOp(fn func(ctx context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), w.gitOpTimeout)
	defer cancel()
	err := fn(ctx)
	if err != nil && ctx.Err() == context.DeadlineExceeded {
		return status.Error(codes.DeadlineExceeded,
			fmt.Sprintf("git operation deadline exceeded (configured %s): %v", w.gitOpTimeout, err))
	}
	return err
}

// syncFailureEventType is the Event Bus event type for permanent sync-cycle
// failures (SPEC R10 "log loudly + telemetry": the R10 error-classification
// table's non-recoverable and retries-exhausted branches),
// matching the convention pinned by the service tests.
const syncFailureEventType = "cartographer.push_failed"

// publishFailure emits an operator-visible Event Bus telemetry event for a
// permanent sync-cycle failure. Event type syncFailureEventType.
func (w *SyncWorker) publishFailure(operation string, err error) {
	if w.auditor == nil {
		return
	}
	w.auditor.Submit(&flowv1.PublishRequest{
		Channel: "telemetry",
		Event: &flowv1.FlowEvent{
			EventId:       uuid.NewString(),
			EventType:     syncFailureEventType,
			FlowNamespace: w.podNamespace,
			NodeId:        "cartographer",
			Timestamp:     timestamppb.Now(),
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
	// Non-recoverable: no-remote configuration, auth failures, divergence,
	// push rejection, and the pre-flight config errors where "the git
	// operation cannot be attempted at all" (SPEC:123). ErrNoRemote is the
	// production misconfiguration of a REMOTE_URL whose SetRemote was rejected
	// non-fatally at startup (pullOnInit=false, cmd/main.go): the gitstore has
	// no remote, so retrying within the cycle can never succeed — it must fail
	// the cycle immediately so Sync() surfaces the mapped status (SPEC
	// error-table row "Remote not configured" → FAILED_PRECONDITION, pinned by
	// TestSync_WakesWorkerAndBlocks). The same "cannot be attempted" logic
	// classes an unsupported URL scheme as non-recoverable (SPEC error-table
	// row "Unsupported remote URL scheme" → INVALID_ARGUMENT): a scheme that
	// is not https:// or ssh:// is permanent, so retrying it can never
	// succeed; it must fail the cycle immediately so Sync() surfaces the
	// mapped status. In production the unsupported-scheme error is unreachable
	// in this cycle — SetRemote validates the scheme once at startup (main
	// fails startup on a rejected URL when pullOnInit=true; with
	// pullOnInit=false a rejected remote is a logged non-fatal warning that
	// leaves the gitstore with no remote, surfacing ErrNoRemote instead,
	// cmd/main.go) and resolveAuth folds any resolver error (including
	// buildResolveAuthFn's unsupported-scheme default branch) into
	// ErrAuthConfigMissing — so a broken remote URL never reaches this cycle;
	// the branch is kept as the test-injectable mapping for the SPEC row.
	if errors.Is(err, gitstore.ErrNoRemote) ||
		errors.Is(err, gitstore.ErrAuthFailed) ||
		errors.Is(err, gitstore.ErrAuthConfigMissing) ||
		errors.Is(err, gitstore.ErrUnsupportedURLScheme) ||
		errors.Is(err, gitstore.ErrPullDiverged) ||
		errors.Is(err, gitstore.ErrPushRejected) {
		return syncNonRecoverable
	}
	// Recoverable: network errors, timeouts, connection refused
	return syncRecoverable
}
