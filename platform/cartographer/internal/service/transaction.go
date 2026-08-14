package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

func (s *CartographerServer) lockTransaction(txID string) (func(), error) {
	if txID == "" {
		return func() {}, nil
	}
	_, unlock, err := s.txManager.LockActive(txID)
	return unlock, err
}

func (s *CartographerServer) lockTransactionMutation(txID string) (func(), error) {
	if txID == "" {
		return func() {}, nil
	}
	state, unlock, err := s.txManager.LockActive(txID)
	if err != nil {
		return nil, err
	}
	// A transaction whose commit has started is closed for mutations (its
	// branch files are being serialised onto the working tree): from the write
	// surface the handle no longer references a usable active transaction,
	// matching the SPEC error-table row "Transaction not found" ("was already
	// committed/rolled back" → NOT_FOUND). FAILED_PRECONDITION is not used
	// here — no SPEC error-table row justifies it for a mutation against a
	// mid-commit transaction, so returning it would be a reverse-map
	// divergence.
	if state.CommitStarted {
		unlock()
		return nil, errTransactionNotFound(txID)
	}
	return unlock, nil
}

// addTransactionChange records a completed branch mutation. A capacity rejection
// invalidates the entire transaction, so its branch resources are rolled back
// while the caller still holds the transaction lifecycle lock.
func (s *CartographerServer) addTransactionChange(
	ctx context.Context, txID string, entry gitstore.ChangeLogEntry,
) error {
	err := s.txManager.AddChangeLogEntry(txID, entry)
	if !errors.Is(err, gitstore.ErrChangeLogFull) {
		return mapGitError(err)
	}
	return s.rejectFullChangeLog(ctx, txID, err)
}

// preflightTransactionChange rejects a mutation before the branch write when
// it would grow the change log past its cap (SPEC:891-892 admission
// predicate). The entry carries the request's ID where known; CreateEntity and
// CreateEdge pass an empty ID for an auto-generated UUID, which CheckCapacity
// treats as a new element (a fresh UUID can never reuse a logged slot). A
// capacity rejection invalidates the entire transaction, so its branch
// resources are rolled back while the caller still holds the transaction
// lifecycle lock.
func (s *CartographerServer) preflightTransactionChange(
	ctx context.Context, txID string, entry gitstore.ChangeLogEntry,
) error {
	state, lookupErr := s.txManager.Lookup(txID)
	if lookupErr != nil {
		return errTransactionNotFound(txID)
	}
	if err := state.ChangeLog.CheckCapacity(entry); err != nil {
		return s.rejectFullChangeLog(ctx, txID, err)
	}
	return nil
}

func (s *CartographerServer) rejectFullChangeLog(ctx context.Context, txID string, capErr error) error {
	state, lookupErr := s.txManager.Lookup(txID)
	if lookupErr != nil {
		return &ChangeLogFullError{
			CapError:   capErr,
			RollbackOK: false,
			CleanupErr: lookupErr,
		}
	}
	state.RollbackOnly = true
	persistErr := s.persistTransactionState(ctx, state)
	var invalidateErr error
	if persistErr != nil {
		invalidateErr = s.store.InvalidateBranchState(ctx, txID)
	}
	cleanupErr := s.cleanupTransaction(ctx, state)
	return &ChangeLogFullError{
		CapError:      capErr,
		RollbackOK:    cleanupErr == nil,
		PersistErr:    persistErr,
		InvalidateErr: invalidateErr,
		CleanupErr:    cleanupErr,
	}
}

func durableTransactionState(state *TransactionState) store.BranchTransactionState {
	return store.BranchTransactionState{
		MainHeadAtLastSync: state.MainHeadAtLastSync,
		AppliedTimeout:     state.AppliedTimeout,
		// The absolute lifetime bounds are persisted at every timeout-affecting
		// event (BeginTransaction, ExtendTimeout) so recovery restores the
		// transaction's true lifetime instead of re-basing it from the restart
		// instant (SPEC R9: "absolute lifetime from BeginTransaction").
		CreatedAt:               state.CreatedAt,
		ExpiresAt:               state.ExpiresAt,
		SchemaHash:              state.SchemaHash,
		CommitStarted:           state.CommitStarted,
		CommitCreated:           state.CommitCreated,
		CommitHydrated:          state.CommitHydrated,
		MainRehydrated:          state.MainRehydrated,
		MergeCompleted:          state.MergeCompleted,
		RollbackOnly:            state.RollbackOnly,
		BranchRefreshInProgress: state.BranchRefreshInProgress,
	}
}

func (s *CartographerServer) persistTransactionState(ctx context.Context, state *TransactionState) error {
	return s.store.SaveBranchTransactionState(ctx, state.ID, durableTransactionState(state))
}

// cleanupTransaction removes transaction resources in retry-safe order. The
// caller must hold the transaction lifecycle lock.
func (s *CartographerServer) cleanupTransaction(ctx context.Context, state *TransactionState) error {
	if err := s.withGitLock(func() error { return s.cleanupTransactionGitLocked(ctx, state) }); err != nil {
		return fmt.Errorf("clean transaction git state: %w", err)
	}
	return s.finishTransactionCleanup(ctx, state)
}

func (s *CartographerServer) cleanupTransactionGitLocked(ctx context.Context, state *TransactionState) error {
	if err := s.gitstore.RestoreMain(ctx); err != nil {
		return err
	}
	if err := s.gitstore.CleanUntracked(ctx); err != nil {
		return err
	}
	if state.MainRehydrated {
		if s.ladybugPath == "" {
			// Restoring the main store after a partial commit is a re-hydration
			// failure, classified INTERNAL by the SPEC error-table row "Commit
			// serialisation or re-hydration failed". No error-table row assigns
			// FAILED_PRECONDITION to this condition, so returning that code would
			// be an unjustified wire status.
			return status.Error(codes.Internal,
				"cannot restore main store after partial commit without LADYBUG_DB_PATH")
		}
		s.lockMainStore()
		entitiesDir, edgesDir := s.gitstore.HydrationDirs()
		err := s.store.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
		s.writeLock.Unlock()
		if err != nil {
			return fmt.Errorf("restore main store: %w", err)
		}
		state.MainRehydrated = false
	}
	return nil
}

func (s *CartographerServer) finishTransactionCleanup(ctx context.Context, state *TransactionState) error {
	if err := s.store.DropBranchDB(ctx, state.ID); err != nil {
		return fmt.Errorf("drop transaction branch DB: %w", err)
	}
	if err := s.withGitLock(func() error {
		if err := s.gitstore.RestoreMain(ctx); err != nil {
			return err
		}
		if err := s.gitstore.CleanUntracked(ctx); err != nil {
			return err
		}
		exists, err := s.gitstore.BranchExists(ctx, state.ID)
		if err != nil {
			return err
		}
		if exists {
			return s.gitstore.DeleteBranch(ctx, state.ID)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("delete transaction git branch: %w", err)
	}
	s.txManager.Delete(state.ID)
	return nil
}

// =========================================================================
// Transaction Path
// =========================================================================

func (s *CartographerServer) BeginTransaction(
	ctx context.Context,
	req *flowv1.BeginTransactionRequest,
) (*flowv1.BeginTransactionResponse, error) {
	if err := s.checkTxCap(ctx, "WRITE:graph/tx"); err != nil {
		return nil, err
	}
	s.txAdmission.RLock()
	defer s.txAdmission.RUnlock()
	txID := s.newIDFn()
	// Implicit sync before branch creation: if a remote is configured, wake
	// the sync worker so the branch starts from the latest remote state (SPEC
	// R10: "waits for one full cycle (fetch → merge → re-hydrate → push)
	// before creating the branch"). The caller's own context deadline bounds
	// the wait — a slow-but-eventually-successful cycle is awaited in full,
	// and the begin proceeds early only when the cycle fails or the caller's
	// deadline fires (sync errors are non-blocking). A caller without a
	// deadline waits at most one cycle, which is internally bounded by the
	// worker's per-operation git deadlines (DefaultGitOperationTimeout).
	if s.syncWorker != nil && s.remoteURL != "" {
		if syncErr := s.syncWorker.WakeAndWait(ctx); syncErr != nil {
			slog.Warn("begin tx: sync worker cycle failed, proceeding", "error", syncErr)
		}
	}
	requestedTimeout := s.defaultTimeout
	if req.Timeout != nil {
		requestedTimeout = req.Timeout.AsDuration()
	}
	var mainHead string
	var branchStoreErr error
	var schemaHash string
	if err := s.withGitLock(func() error {
		var err error
		mainHead, err = s.gitstore.BranchHEAD(ctx, "main")
		if err != nil {
			return fmt.Errorf("get main HEAD: %w", err)
		}
		if err := s.gitstore.CreateBranch(ctx, txID); err != nil {
			return &branchResourceError{err: fmt.Errorf("create branch: %w", err)}
		}
		if err := s.gitstore.HardResetToBranch(ctx, txID); err != nil {
			return &branchResourceError{err: fmt.Errorf("hard reset: %w", err)}
		}
		if err := s.store.CreateBranchDB(ctx, txID); err != nil {
			branchStoreErr = fmt.Errorf("create branch DB: %w", err)
		} else if err := s.store.ReplicateSchemaToBranch(ctx, txID); err != nil {
			branchStoreErr = fmt.Errorf("replicate branch schema: %w", err)
		} else if s.ladybugPath != "" {
			entitiesDir, edgesDir := s.gitstore.HydrationDirs()
			branchStoreErr = s.store.HydrateBranchFromFiles(ctx, txID, entitiesDir, edgesDir)
		}
		if branchStoreErr != nil {
			var cleanups []string
			if err := s.store.DropBranchDB(ctx, txID); err != nil {
				cleanups = append(cleanups, fmt.Sprintf("drop branch DB: %v", err))
			}
			if err := s.gitstore.RestoreMain(ctx); err != nil {
				cleanups = append(cleanups, fmt.Sprintf("restore main: %v", err))
			}
			if err := s.gitstore.CleanUntracked(ctx); err != nil {
				cleanups = append(cleanups, fmt.Sprintf("clean untracked: %v", err))
			}
			if err := s.gitstore.DeleteBranch(ctx, txID); err != nil {
				cleanups = append(cleanups, fmt.Sprintf("delete branch: %v", err))
			}
			if len(cleanups) > 0 {
				branchStoreErr = fmt.Errorf("%v; cleanup failures: [%s]",
					branchStoreErr, strings.Join(cleanups, "; "))
			}
		}
		// Snapshot the schema hash while holding the git lock: the store's
		// re-hydration (RehydrateMainFromFiles) promotes vector-enabled flags
		// on the shared defs in place (ensureEmbeddingLoadSchema) under the
		// same git lock, so reading them outside the lock would race a
		// concurrent re-hydration and persist a nondeterministic SchemaHash
		// into branch state. The git lock is the mutual-exclusion seam for
		// every re-hydration call site (mirrors RefreshTransaction's hash
		// computation, which runs inside the git lock).
		schemaHash = computeSchemaHash(s.store)
		return nil
	}); err != nil {
		// Git branch-creation failures (CreateBranch/HardResetToBranch) map to
		// the SPEC error-table row "BeginTransaction resource exhausted"
		// (RESOURCE_EXHAUSTED — branch creation failed), matching the store-side
		// branchStoreErr path below. Other git failures (e.g. BranchHEAD read)
		// keep their mapGitError mapping.
		var resErr *branchResourceError
		if errors.As(err, &resErr) {
			return nil, errBeginTransactionResourceExhausted(resErr.Error())
		}
		return nil, mapGitError(err)
	}
	if branchStoreErr != nil {
		return nil, errBeginTransactionResourceExhausted(branchStoreErr.Error())
	}
	state, err := s.txManager.Create(txID, requestedTimeout, mainHead)
	if err != nil {
		var cleanups []string
		if lockErr := s.gitstore.WithGitLock(func() error {
			if err := s.gitstore.DeleteBranch(ctx, txID); err != nil {
				cleanups = append(cleanups, fmt.Sprintf("delete branch: %v", err))
			}
			if err := s.gitstore.RestoreMain(ctx); err != nil {
				cleanups = append(cleanups, fmt.Sprintf("restore main: %v", err))
			}
			return nil
		}); lockErr != nil {
			cleanups = append(cleanups, fmt.Sprintf("git lock: %v", lockErr))
		}
		if err := s.store.DropBranchDB(ctx, txID); err != nil {
			cleanups = append(cleanups, fmt.Sprintf("drop branch DB: %v", err))
		}
		// A timeout-validation rejection from Create (INVALID_ARGUMENT per SPEC
		// error-table row "Invalid transaction timeout duration", applying to
		// BeginTransaction) must propagate its code after cleanup — not be
		// flattened into the admission-failure RESOURCE_EXHAUSTED below. Other
		// Create failures (duplicate registration) remain admission failures.
		if status.Code(err) == codes.InvalidArgument {
			return nil, err
		}
		msg := fmt.Sprintf("register tx: %v", err)
		if len(cleanups) > 0 {
			msg = fmt.Sprintf("%s; cleanup failures: [%s]", msg, strings.Join(cleanups, "; "))
		}
		return nil, errBeginTransactionResourceExhausted(msg)
	}
	state.SchemaHash = schemaHash
	if err := s.persistTransactionState(ctx, state); err != nil {
		cleanupErr := s.cleanupTransaction(ctx, state)
		if cleanupErr != nil {
			return nil, errBeginTransactionResourceExhausted(fmt.Sprintf(
				"persist transaction state: %v; cleanup: %v", err, cleanupErr))
		}
		return nil, errBeginTransactionResourceExhausted(fmt.Sprintf("persist transaction state: %v", err))
	}
	return &flowv1.BeginTransactionResponse{
		TransactionId: txID, AppliedTimeout: durationpb.New(state.AppliedTimeout),
	}, nil
}

//nolint:gocyclo
func (s *CartographerServer) CommitTransaction(
	ctx context.Context,
	req *flowv1.CommitTransactionRequest,
) (*flowv1.CommitTransactionResponse, error) {
	if err := s.checkTxCap(ctx, "WRITE:graph/tx"); err != nil {
		return nil, err
	}
	if err := validateTxID(req.TransactionId); err != nil {
		return nil, err
	}
	state, unlockTx, err := s.txManager.LockActive(req.TransactionId)
	if err != nil {
		return nil, err
	}
	defer unlockTx()
	mainHeadAtLastSync := state.MainHeadAtLastSync
	if state.MergeCompleted {
		// The fast-forward merge already landed on main before the previous
		// attempt failed at state persistence or cleanup, so the merge is
		// durable locally even though this retry only finishes the cleanup.
		// Flag it for push regardless of this attempt's cleanup outcome (SPEC
		// R10: commit() sets the push-needed flag) — a cleanup failure must not
		// leave a locally-merged commit silently un-pushed.
		if s.syncWorker != nil {
			s.syncWorker.SetPushNeeded()
		}
		if err := s.cleanupTransaction(ctx, state); err != nil {
			return nil, mapGitError(err)
		}
		// SPEC R10 (SPEC:630-634): WithAck blocks until the sync cycle
		// delivers the push, mirroring the main commit path below. Without
		// this, an acked commit retried on the MergeCompleted path returns
		// success while the push flag is still set and the commit is
		// undelivered.
		if s.syncWorker != nil && req.GetAck() {
			if syncErr := s.syncWorker.WakeAndWait(ctx); syncErr != nil {
				return nil, mapGitError(syncErr)
			}
		}
		return &flowv1.CommitTransactionResponse{}, nil
	}

	// Zero-mutation check.
	if state.ChangeLog.Len() == 0 {
		if err := s.cleanupTransaction(ctx, state); err != nil {
			return nil, mapGitError(err)
		}
		return &flowv1.CommitTransactionResponse{}, nil
	}

	// Schema compatibility check (SPEC R9 commit flow step 1): the branch
	// LadybugDB's schema is validated against the current schema. Only a change
	// that makes the branch's data incompatible with the current schema — a type
	// or property the transaction's data lives under removed or changed, or a
	// vector index disabled — fails the commit (FAILED_PRECONDITION, error-table
	// row "Schema changed incompatibly during tx"). Additive changes (new types,
	// new properties) and rule modifications are non-destructive (SPEC R2/R6) and
	// do not block commit; RefreshTransaction re-hydrates the branch from latest
	// main, re-baselining it on the current schema.
	if err := s.store.CheckBranchSchemaCompatibility(ctx, req.TransactionId); err != nil {
		if errors.Is(err, store.ErrDestructiveSchemaChange) {
			return nil, errSchemaChangedIncompatibly(err.Error())
		}
		return nil, mapStoreError(err)
	}

	var commitErr error
	lockErr := s.withGitLock(func() error {
		defer func() {
			if commitErr == nil {
				return
			}
			if reconcileErr := s.reconcileFailedCommitGitLocked(ctx, state); reconcileErr != nil {
				commitErr = fmt.Errorf("%v; reconcile failed commit: %w", commitErr, reconcileErr)
			}
		}()
		if err := s.gitstore.Checkout(ctx, req.TransactionId); err != nil {
			commitErr = fmt.Errorf("checkout: %w", err)
			return nil
		}
		commitExists, err := s.gitstore.CommitExistsOnBranch(ctx, req.TransactionId)
		if err != nil {
			commitErr = fmt.Errorf("detect transaction commit: %w", err)
			return nil
		}
		if commitExists && !state.CommitCreated {
			state.CommitCreated = true
			state.CommitStarted = true
			if err := s.persistTransactionState(ctx, state); err != nil {
				commitErr = fmt.Errorf("persist detected commit: %w", err)
				return nil
			}
		}
		if state.CommitCreated {
			txHead, txErr := s.gitstore.BranchHEAD(ctx, req.TransactionId)
			mainHead, mainErr := s.gitstore.BranchHEAD(ctx, "main")
			if txErr != nil || mainErr != nil {
				commitErr = fmt.Errorf("detect transaction merge: tx=%v main=%v", txErr, mainErr)
				return nil
			}
			if txHead == mainHead {
				state.MergeCompleted = true
				state.MainRehydrated = false
				if err := s.persistTransactionState(ctx, state); err != nil {
					commitErr = fmt.Errorf("persist detected merge: %w", err)
					return nil
				}
				if err := s.cleanupTransactionGitLocked(ctx, state); err != nil {
					commitErr = err
				}
				return nil
			}
		}
		if !state.CommitCreated {
			tc := groupChanges(state.ChangeLog)
			if err := s.resolveSameIDSequences(ctx, req.TransactionId, &tc); err != nil {
				commitErr = fmt.Errorf("resolve same-id change sequences: %w", err)
				return nil
			}
			for et, entries := range tc.addedEntities {
				if err := s.gitstore.WriteEntityFiles(ctx, et, s.toGitEntities(entries)); err != nil {
					commitErr = fmt.Errorf("write entity files: %w", err)
					return nil
				}
			}
			for et, entries := range tc.modifiedEntities {
				if err := s.gitstore.WriteEntityFiles(ctx, et, s.toGitEntities(entries)); err != nil {
					commitErr = fmt.Errorf("write modified entity files: %w", err)
					return nil
				}
			}
			for et, ids := range tc.deletedEntities {
				if err := s.gitstore.RemoveEntityFiles(ctx, et, ids); err != nil {
					commitErr = fmt.Errorf("remove entity files: %w", err)
					return nil
				}
			}
			for et, entries := range tc.addedEdges {
				if err := s.gitstore.WriteEdgeFiles(ctx, et, toGitEdges(entries)); err != nil {
					commitErr = fmt.Errorf("write edge files: %w", err)
					return nil
				}
			}
			for et, entries := range tc.modifiedEdges {
				if err := s.gitstore.WriteEdgeFiles(ctx, et, toGitEdges(entries)); err != nil {
					commitErr = fmt.Errorf("write modified edge files: %w", err)
					return nil
				}
			}
			for et, ids := range tc.deletedEdges {
				if err := s.gitstore.RemoveEdgeFiles(ctx, et, ids); err != nil {
					commitErr = fmt.Errorf("remove edge files: %w", err)
					return nil
				}
			}
		}
		// Divergence check: verify main has not advanced since last sync
		// (SPEC serialisation flow step 5 — must precede step 6 git add+commit).
		// The check fails closed: when no baseline is recorded the commit is
		// rejected with FAILED_PRECONDITION whenever main's HEAD is non-empty,
		// so a stale-branch commit can never slip past step 5 and surface the
		// step-10 merge failure (INTERNAL, ErrMergeDiverged) instead of the
		// SPEC-prescribed "Commit not up-to-date with main" FAILED_PRECONDITION
		// (error table, SPEC:980). BeginTransaction always snapshots main's
		// HEAD as the baseline, so an empty value is only reachable through a
		// recovered or test-constructed state whose persisted baseline is
		// absent — which must fail closed rather than silently skip the
		// serialisation guard.
		curHead, err := s.gitstore.BranchHEAD(ctx, "main")
		if err != nil {
			commitErr = fmt.Errorf("branch head: %w", err)
			return nil
		}
		if curHead != mainHeadAtLastSync {
			commitErr = errCommitNotUpToDate()
			return nil
		}
		if !state.CommitCreated {
			if err := s.gitstore.AddAll(ctx, "entities"); err != nil {
				commitErr = fmt.Errorf("add entity files: %w", err)
				return nil
			}
			if err := s.gitstore.AddAll(ctx, "edges"); err != nil {
				commitErr = fmt.Errorf("add edge files: %w", err)
				return nil
			}
			state.CommitStarted = true
			if err := s.persistTransactionState(ctx, state); err != nil {
				commitErr = fmt.Errorf("persist commit start: %w", err)
				return nil
			}
			if err := s.gitstore.Commit(ctx, fmt.Sprintf("transaction:%s", req.TransactionId)); err != nil {
				commitErr = fmt.Errorf("commit: %w", err)
				return nil
			}
			state.CommitCreated = true
			if err := s.persistTransactionState(ctx, state); err != nil {
				commitErr = fmt.Errorf("persist created commit: %w", err)
				return nil
			}
		}
		if !state.CommitHydrated {
			// SPEC steps 7-8: git lock is outer; main write lock is inner. With
			// transaction-only writes there are no non-transactional mutations
			// to main.lbug, so the LadybugDB write lock serialises the
			// re-hydration only against the other main-store writers: Sync()
			// (the sync worker's post-pull re-hydration) and WipeGraph().
			s.lockMainStore()
			state.MainRehydrated = true
			if err := s.persistTransactionState(ctx, state); err != nil {
				state.MainRehydrated = false
				s.writeLock.Unlock()
				commitErr = fmt.Errorf("persist main rehydration start: %w", err)
				return nil
			}
			if s.ladybugPath != "" {
				entitiesDir, edgesDir := s.gitstore.HydrationDirs()
				err = s.store.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
			} else {
				err = s.store.RehydrateFromBranch(ctx, req.TransactionId)
			}
			s.writeLock.Unlock()
			if err != nil {
				commitErr = fmt.Errorf("rehydrate main: %w", err)
				return nil
			}
			state.CommitHydrated = true
			if err := s.persistTransactionState(ctx, state); err != nil {
				state.CommitHydrated = false
				commitErr = fmt.Errorf("persist completed main rehydration: %w", err)
				return nil
			}
		}

		// SPEC step 10: Fast-forward merge to main.
		if err := s.gitstore.FastForwardMerge(ctx, req.TransactionId, "main"); err != nil {
			txHead, txErr := s.gitstore.BranchHEAD(ctx, req.TransactionId)
			mainHead, mainErr := s.gitstore.BranchHEAD(ctx, "main")
			if txErr != nil || mainErr != nil || txHead != mainHead {
				commitErr = fmt.Errorf("merge: %w", err)
				return nil
			}
		}
		state.MergeCompleted = true
		state.MainRehydrated = false
		if err := s.persistTransactionState(ctx, state); err != nil {
			commitErr = fmt.Errorf("persist completed merge: %w", err)
			return nil
		}
		if err := s.cleanupTransactionGitLocked(ctx, state); err != nil {
			commitErr = err
		}
		return nil
	})

	if lockErr != nil {
		return nil, mapGitError(lockErr)
	}
	if commitErr != nil {
		return nil, mapGitError(commitErr)
	}
	if err := s.finishTransactionCleanup(ctx, state); err != nil {
		return nil, mapStoreError(err)
	}
	// Notify the sync worker that a commit needs pushing.
	// SPEC R10 (SPEC:615-622): WithAck blocks until the sync cycle completes;
	// the caller's own context deadline bounds the wait, so a caller that
	// hits the context deadline receives DEADLINE_EXCEEDED and the push flag
	// stays set (mapGitError preserves the context error's code). A caller
	// without a deadline waits at most one cycle, which is internally bounded
	// by the worker's per-operation git deadlines
	// (DefaultGitOperationTimeout).
	if s.syncWorker != nil {
		s.syncWorker.SetPushNeeded()
		if req.GetAck() {
			if syncErr := s.syncWorker.WakeAndWait(ctx); syncErr != nil {
				return nil, mapGitError(syncErr)
			}
		}
	}
	return &flowv1.CommitTransactionResponse{}, nil
}

func (s *CartographerServer) RollbackTransaction(
	ctx context.Context,
	req *flowv1.RollbackTransactionRequest,
) (*flowv1.RollbackTransactionResponse, error) {
	if err := s.checkTxCap(ctx, "WRITE:graph/tx"); err != nil {
		return nil, err
	}
	if err := validateTxID(req.TransactionId); err != nil {
		return nil, err
	}
	state, unlockTx, err := s.txManager.LockCleanup(req.TransactionId)
	if err != nil {
		return nil, err
	}
	defer unlockTx()
	// The merge has already landed on main, so the transaction is effectively
	// committed — the SPEC error-table row "Transaction not found" defines the
	// contract for operations on an already-committed transaction ("was already
	// committed/rolled back" → NOT_FOUND). FAILED_PRECONDITION would
	// contradict that mapping. The caller finishes the remaining cleanup by
	// retrying CommitTransaction (MergeCompleted path).
	if state.MergeCompleted {
		return nil, errTransactionNotFound(req.TransactionId)
	}
	if err := s.cleanupTransaction(ctx, state); err != nil {
		return nil, mapGitError(err)
	}
	return &flowv1.RollbackTransactionResponse{}, nil
}

func (s *CartographerServer) RefreshTransaction(
	ctx context.Context,
	req *flowv1.RefreshTransactionRequest,
) (*flowv1.RefreshTransactionResponse, error) {
	if err := s.checkTxCap(ctx, "WRITE:graph/tx"); err != nil {
		return nil, err
	}
	if err := validateTxID(req.TransactionId); err != nil {
		return nil, err
	}
	state, unlockTx, err := s.txManager.LockActive(req.TransactionId)
	if err != nil {
		return nil, err
	}
	defer unlockTx()
	// A transaction whose commit has started is closed for refresh (its branch
	// files are being serialised): from the refresh surface the handle no
	// longer references a usable active transaction, matching the SPEC
	// error-table row "Transaction not found" ("was already committed/rolled
	// back" → NOT_FOUND). FAILED_PRECONDITION is not used here — no SPEC
	// error-table row justifies it for a refresh against a mid-commit
	// transaction.
	if state.CommitStarted {
		return nil, errTransactionNotFound(req.TransactionId)
	}

	// SPEC R9 Refresh flow applies to every refresh, empty or not: the branch is
	// reset to and re-hydrated from latest main, then each transaction change is
	// validated and re-applied. For a zero-mutation transaction the validate and
	// re-apply steps below are no-ops, so the flow reduces to reset-and-re-hydrate
	// — which is still required. The previous empty-refresh short-circuit only
	// advanced MainHeadAtLastSync, leaving the branch DB on its stale begin-time
	// snapshot: a subsequent mutate+commit then passed the divergence check
	// (sync head == main), committed against the stale branch, re-hydrated main
	// from files missing the entities added to main in the interim, and the
	// fast-forward merge failed (INTERNAL) with main LadybugDB and git main left
	// divergent.
	var mainHash string
	if err := s.withGitLock(func() error {
		if err := s.gitstore.HardResetToBranch(ctx, req.TransactionId); err != nil {
			return err
		}
		before, err := s.snapshotWorkingTree(ctx)
		if err != nil {
			return err
		}
		if err := s.gitstore.RestoreMain(ctx); err != nil {
			return err
		}
		if err := s.gitstore.CleanUntracked(ctx); err != nil {
			return err
		}
		current, err := s.snapshotWorkingTree(ctx)
		if err != nil {
			return err
		}
		mainHash, err = s.gitstore.BranchHEAD(ctx, "main")
		if err != nil {
			return err
		}
		if err := s.gitstore.SetBranchRef(ctx, req.TransactionId, mainHash); err != nil {
			return err
		}
		if err := s.gitstore.HardResetToBranch(ctx, req.TransactionId); err != nil {
			return err
		}
		if err := s.resetBranchStoreFromWorkingTree(ctx, req.TransactionId); err != nil {
			return err
		}
		if err := s.validateRefresh(ctx, state, before, current); err != nil {
			// SPEC R9 refresh step 4: on a conflict the branch LadybugDB must
			// remain at the step-2 state (re-hydrated from main, no transaction
			// changes applied). resetBranchStoreFromWorkingTree already
			// re-applied the changes onto the swapped-in branch, so restore the
			// clean state before surfacing the ABORTED conflict.
			if restoreErr := s.restoreCleanBranchStore(ctx, req.TransactionId); restoreErr != nil {
				return fmt.Errorf("validate refresh: %v; restore clean refreshed branch: %w", err, restoreErr)
			}
			return err
		}
		oldMainHead := state.MainHeadAtLastSync
		oldSchemaHash := state.SchemaHash
		oldRefreshMarker := state.BranchRefreshInProgress
		state.MainHeadAtLastSync = mainHash
		// The branch DB has been reset and re-hydrated from latest main, so the
		// schema baseline is refreshed to the current schema: a refreshed
		// transaction is re-based on main's schema and can commit even after a
		// schema push (SPEC R2/R6 non-destructive changes no longer block the
		// commit-time compatibility check, and a destructive change would be
		// re-detected against the refreshed baseline).
		state.SchemaHash = computeSchemaHash(s.store)
		// The refresh completed — clear the in-progress marker so the durable
		// record no longer distinguishes this transaction from a normal open
		// transaction (RecoverOpenTransactions' empty-diff branch).
		state.BranchRefreshInProgress = false
		if err := s.persistTransactionState(ctx, state); err != nil {
			state.MainHeadAtLastSync = oldMainHead
			state.SchemaHash = oldSchemaHash
			state.BranchRefreshInProgress = oldRefreshMarker
			return fmt.Errorf("persist refreshed transaction: %w", err)
		}
		return nil
	}); err != nil {
		return nil, mapGitError(err)
	}
	return &flowv1.RefreshTransactionResponse{}, nil
}

func (s *CartographerServer) GetTransactionDiff(
	ctx context.Context,
	req *flowv1.GetTransactionDiffRequest,
) (*flowv1.GetTransactionDiffResponse, error) {
	if err := s.checkTxCap(ctx, "READ:graph/tx"); err != nil {
		return nil, err
	}
	if err := validateTxID(req.TransactionId); err != nil {
		return nil, err
	}
	state, unlockTx, err := s.txManager.LockActive(req.TransactionId)
	if err != nil {
		return nil, err
	}
	defer unlockTx()
	resp := &flowv1.GetTransactionDiffResponse{}
	for _, entry := range state.ChangeLog.Entries() {
		de := &flowv1.DiffEntry{
			Id: entry.ID, Type: entry.Type,
		}
		de.Suspected = entry.Suspected
		if entry.Entity != nil {
			de.Properties = entry.Entity.Properties
			de.Embedding = entry.Entity.Embedding
		}
		if entry.Edge != nil {
			de.Properties = entry.Edge.Properties
			de.FromEntityId = entry.Edge.FromEntityID
			de.ToEntityId = entry.Edge.ToEntityID
		}
		switch entry.Kind {
		case gitstore.ChangeAddEntity:
			resp.AddedEntities = append(resp.AddedEntities, de)
		case gitstore.ChangeModEntity:
			resp.ModifiedEntities = append(resp.ModifiedEntities, de)
		case gitstore.ChangeDelEntity:
			resp.DeletedEntities = append(resp.DeletedEntities, de)
		case gitstore.ChangeAddEdge:
			resp.AddedEdges = append(resp.AddedEdges, de)
		case gitstore.ChangeModEdge:
			resp.ModifiedEdges = append(resp.ModifiedEdges, de)
		case gitstore.ChangeDelEdge:
			resp.DeletedEdges = append(resp.DeletedEdges, de)
		}
	}
	return resp, nil
}

func (s *CartographerServer) ExtendTimeout(
	ctx context.Context,
	req *flowv1.ExtendTimeoutRequest,
) (*flowv1.ExtendTimeoutResponse, error) {
	if err := s.checkTxCap(ctx, "WRITE:graph/tx"); err != nil {
		return nil, err
	}
	if err := validateTxID(req.TransactionId); err != nil {
		return nil, err
	}
	// Admission gate (unknown / rollback-only / timed-out transaction) runs
	// before duration validation so the transaction error surfaces first,
	// matching the write-RPC check order. The lifecycle lock is released
	// immediately: ExtendTimeout acquires it itself for the expiry mutation
	// and persist and re-verifies admission under it, closing the window
	// between this gate and the mutation. Holding it here would deadlock the
	// manager's own lifecycle acquisition.
	if _, unlockTx, err := s.txManager.LockActive(req.TransactionId); err != nil {
		return nil, err
	} else {
		unlockTx()
	}
	duration := req.Duration.AsDuration()
	if err := s.txManager.ExtendTimeout(req.TransactionId, duration, func(state *TransactionState) error {
		return s.persistTransactionState(ctx, state)
	}); err != nil {
		return nil, err
	}
	// The server applies the requested duration verbatim (an over-limit
	// extension is rejected with an error, never silently capped), so the
	// applied timeout equals the granted duration. Mirror BeginTransaction's
	// applied_timeout so the SDK surfaces the value the server granted.
	return &flowv1.ExtendTimeoutResponse{
		AppliedTimeout: durationpb.New(duration),
	}, nil
}
