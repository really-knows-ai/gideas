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

	// cycleMu guards per-cycle state for WithAck/Sync/BeginTransaction callers.
	cycleMu   sync.Mutex
	cycleErr  error         // last non-recoverable error from the current/just-completed cycle
	cycleDone chan struct{} // closed when the current cycle completes
	cycleID   int           // incremented for each new cycle

	clock Clock // for test injection
}

// NewSyncWorker creates a new SyncWorker.
func NewSyncWorker(
	remoteURL string,
	gs gitstore.GitStore,
	s store.Store,
	clock Clock,
) *SyncWorker {
	return &SyncWorker{
		wakeCh:    make(chan struct{}, 1),
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
		remoteURL: remoteURL,
		gitstore:  gs,
		store:     s,
		clock:     clock,
	}
}

// SetPushNeeded flags that a commit needs pushing. Called by CommitTransaction.
func (w *SyncWorker) SetPushNeeded() {
	w.pushNeeded.Store(true)
}

// WakeAndWait wakes the worker and blocks until the cycle completes.
// Returns the cycle's non-recoverable error, if any.
func (w *SyncWorker) WakeAndWait(ctx context.Context) error {
	w.cycleMu.Lock()
	w.cycleDone = make(chan struct{})
	w.cycleID++
	w.cycleMu.Unlock()

	// Signal the worker (non-blocking — buffer absorbs if worker is already running)
	select {
	case w.wakeCh <- struct{}{}:
	default:
	}

	select {
	case <-w.cycleDone:
	case <-ctx.Done():
		return ctx.Err()
	}

	w.cycleMu.Lock()
	err := w.cycleErr
	w.cycleMu.Unlock()
	return err
}

// cycleResult is the outcome of one sync cycle, used internally.
type cycleResult struct {
	err            error
	classification syncClassification
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
	result := w.doSyncCycle()

	w.cycleMu.Lock()
	w.cycleErr = result.err
	if w.cycleDone != nil {
		close(w.cycleDone)
		w.cycleDone = nil
	}
	w.cycleMu.Unlock()
}

// doSyncCycle performs the actual sync work.
func (w *SyncWorker) doSyncCycle() cycleResult {
	if w.remoteURL == "" {
		return cycleResult{}
	}

	// 1. FetchAndMerge from remote under git lock.
	var pulled bool
	lockErr := w.gitstore.WithGitLock(func() error {
		_, err := w.gitstore.FetchAndMerge(context.Background(), "origin", "main")
		if err != nil {
			return err
		}
		pulled = true
		return nil
	})
	if lockErr != nil {
		if errors.Is(lockErr, gitstore.ErrNoRemote) {
			return cycleResult{}
		}
		class := classifySyncError(lockErr)
		if class == syncNonRecoverable {
			slog.Warn("sync: non-recoverable fetch error", "error", lockErr)
			return cycleResult{err: lockErr, classification: syncNonRecoverable}
		}
		// Recoverable — try up to 3 times with backoff.
		for attempt := 1; attempt < 3; attempt++ {
			time.Sleep(syncBackoff(attempt))
			lockErr = w.gitstore.WithGitLock(func() error {
				_, err := w.gitstore.FetchAndMerge(context.Background(), "origin", "main")
				if err != nil {
					return err
				}
				pulled = true
				return nil
			})
			if lockErr == nil {
				break
			}
		}
		if lockErr != nil {
			slog.Error("sync: fetch failed after retries", "error", lockErr)
			return cycleResult{err: lockErr, classification: syncRecoverable}
		}
	}

	// 2. If new data was pulled, re-hydrate main from files.
	if pulled {
		entitiesDir, edgesDir := w.gitstore.HydrationDirs()
		if err := w.store.RehydrateMainFromFiles(context.Background(), entitiesDir, edgesDir); err != nil {
			slog.Error("sync: re-hydration failed", "error", err)
			return cycleResult{err: fmt.Errorf("sync re-hydration failed: %w", err), classification: syncNonRecoverable}
		}
	}

	// 3. If push needed, push to remote.
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
			return cycleResult{err: pushErr, classification: syncNonRecoverable}
		}
		// Recoverable — retry with backoff.
		for attempt := 1; attempt < 3; attempt++ {
			time.Sleep(syncBackoff(attempt))
			pushErr = w.gitstore.WithGitLock(func() error {
				return w.gitstore.PushRemote(context.Background())
			})
			if pushErr == nil {
				break
			}
		}
		if pushErr != nil {
			slog.Error("sync: push failed after retries", "error", pushErr)
			return cycleResult{err: pushErr, classification: syncRecoverable}
		}
	}

	// Push succeeded — clear the flag.
	w.pushNeeded.Store(false)
	return cycleResult{}
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
