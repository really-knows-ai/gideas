package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RecoverOpenTransactions recovers transactions from a previous crash.
// Implements the SPEC R9 change-log recovery diff algorithm:
//  1. Compare each branch-DB entity/edge against the corresponding main file
//     to classify it as added, modified, or unchanged.
//  2. Detect suspected deletions (entities/edges in main but absent from the branch DB).
//  3. If the branch DB content is identical to main for every entity and edge
//     (diff is empty), the transaction was already committed — clean up and skip.
func (s *CartographerServer) RecoverOpenTransactions(ctx context.Context) error {
	var branches []string
	if err := s.gitstore.WithGitLock(func() error {
		var err error
		branches, err = s.gitstore.ListBranches(ctx)
		return err
	}); err != nil {
		return fmt.Errorf("list branches: %w", err)
	}
	// Sweep orphaned refresh temp branches before the git-branch loop: the
	// refresh branch-DB swap builds its replacement under a temporary key that
	// never becomes a git branch, so a mid-swap crash strands files the
	// git-branch enumeration below can never name.
	if err := s.cleanupOrphanedTempBranches(ctx, branches); err != nil {
		return fmt.Errorf("clean up orphaned refresh temp branches: %w", err)
	}
	for _, branch := range branches {
		if !isValidUUID(branch) {
			continue
		}
		txID := branch
		entities, dumpErr := s.store.DumpAllEntities(ctx, txID)
		if errors.Is(dumpErr, store.ErrBranchNotFound) {
			if err := s.cleanupMissingRecoveryBranch(ctx, txID); err != nil {
				return fmt.Errorf("roll back transaction with missing branch DB %q: %w", txID, err)
			}
			slog.Warn("RecoverOpenTransactions: deleted orphaned git branch", "tx_id", txID)
			continue
		}
		if dumpErr != nil {
			// resetBranchStoreFromWorkingTree swaps the branch DB via a
			// non-atomic rename loop (.lbug → .schema.json → .state.json), so a
			// crash between the .lbug rename and the .schema.json rename leaves
			// the swapped-in branch DB (built under main's *current* schema)
			// paired with the stale pre-refresh .schema.json. When main gained a
			// non-destructive R6 schema change during the transaction's lifetime,
			// reopening that branch fails hard in restoreBranchSchemaMetadata →
			// validateMetadataAgainstCatalog ("database entity type %q is absent
			// from schema metadata" / property-count mismatch) — a hard error,
			// not the os.ErrNotExist branch classified as recoverable above. The
			// durable state record carries the refresh-in-progress marker across
			// the swap (persisted before the renames begin, cleared only by the
			// refresh's final persist), so a branch that cannot be opened while
			// marked is a mid-swap casualty: its uncommitted mutations are
			// unreachable through the unopenable branch DB (the .lbug is the only
			// durable record of the transaction's mutations, and it is paired
			// with an incompatible schema sidecar). Roll it back loudly — never
			// hard-fail startup into a crash loop (SPEC R9 change-log recovery
			// point 4's rollback posture for an unusable branch DB).
			midSwap, midErr := s.isMidRefreshSwapBranch(ctx, txID)
			if midErr != nil {
				return fmt.Errorf("classify unopenable branch DB %q: %w", txID, midErr)
			}
			if midSwap {
				if err := s.cleanupMissingRecoveryBranch(ctx, txID); err != nil {
					return fmt.Errorf("roll back mid-swap transaction %q: %w", txID, err)
				}
				slog.Warn(
					"RecoverOpenTransactions: rolled back transaction whose branch DB was left unusable by a mid-refresh swap crash",
					"tx_id", txID,
				)
				continue
			}
			return fmt.Errorf("open transaction branch DB %q: %w", txID, dumpErr)
		}
		durableState, stateErr := s.store.LoadBranchTransactionState(ctx, txID)
		if stateErr != nil {
			// BeginTransaction crash window (SPEC R9 change-log recovery): a
			// crash between HydrateBranchFromFiles and persistTransactionState
			// leaves the git branch and branch DB present but no
			// branches/<txID>.state.json record. The caller can never have
			// received the txID (the BeginTransaction response is sent only
			// after the persist succeeds), so the transaction is provably
			// harmless — roll it back instead of hard-failing startup.
			if isMissingBranchStateError(stateErr) {
				if err := s.cleanupMissingRecoveryBranch(ctx, txID); err != nil {
					return fmt.Errorf("roll back transaction with missing state record %q: %w", txID, err)
				}
				slog.Warn("RecoverOpenTransactions: rolled back branch with missing state record", "tx_id", txID)
				continue
			}
			return fmt.Errorf("read transaction state %q: %w", txID, stateErr)
		}
		edges, dumpErr := s.store.DumpAllEdges(ctx, txID)
		if dumpErr != nil {
			return fmt.Errorf("read transaction branch DB %q: %w", txID, dumpErr)
		}

		// The reconstruction diff must be computed against the branch DB's true
		// hydration baseline, not current main. A branch that never refreshed
		// (marker clear) was hydrated from main at MainHeadAtLastSync; diffing
		// against current main would mis-report entities another transaction
		// added to main after this branch began as this transaction's "suspected
		// deletions" (a stale branch — SPEC R9 change-log recovery point 3),
		// falsely wedging it into an unresolvable ABORTED refresh. A branch whose
		// refresh-in-progress marker is set (mid-refresh swap or ABORTED-refresh
		// crash) was re-hydrated from current main, so current main is the correct
		// baseline there; falling back to current main when the marker is set
		// preserves the pinned mid-refresh/rollback behavior.
		baseline := ""
		if !durableState.BranchRefreshInProgress && durableState.MainHeadAtLastSync != "" {
			baseline = durableState.MainHeadAtLastSync
		}
		mainEntities, mainEdges, _, err := s.buildMainFileLookups(ctx, baseline)
		if err != nil {
			return fmt.Errorf("read main graph for transaction %q: %w", txID, err)
		}
		cl := gitstore.NewChangeLog()
		entityChanged, entityErr := s.recoverEntityChanges(cl, entities, mainEntities)
		if entityErr != nil {
			return fmt.Errorf("recover entity changes for transaction %q: %w", txID, entityErr)
		}
		edgeChanged, edgeErr := s.recoverEdgeChanges(cl, edges, mainEdges)
		if edgeErr != nil {
			return fmt.Errorf("recover edge changes for transaction %q: %w", txID, edgeErr)
		}

		// SPEC recovery step 5: If the diff is empty (branch DB identical to main),
		// the transaction was already committed — clean up and do not recover.
		// The BranchRefreshInProgress marker (set by RefreshTransaction before
		// its branch-DB swap, cleared only by the refresh's final state persist)
		// distinguishes that case from a mid-refresh crash: a refresh can leave
		// the branch DB as a clean copy of main (after the swap, before the
		// changes were re-applied), and an empty diff there means the refresh
		// was interrupted — the transaction never committed — not that a merge
		// landed. The swap reorders the re-apply ahead of the rename so a
		// mutation-bearing transaction's branch DB is never a clean copy of main
		// at a crash point (its changes are always durable), which makes this
		// branch reachable only for a zero-mutation transaction; either way the
		// transaction must be rolled back loudly, never silently reported as
		// already committed (its changes never landed on main).
		if !entityChanged && !edgeChanged {
			if durableState.BranchRefreshInProgress && !durableState.MergeCompleted {
				if err := s.cleanupIdenticalRecoveryBranch(ctx, txID); err != nil {
					return fmt.Errorf("roll back mid-refresh transaction %q: %w", txID, err)
				}
				slog.Warn(
					"RecoverOpenTransactions: rolled back transaction interrupted by a mid-refresh crash (never committed)",
					"tx_id", txID,
				)
				continue
			}
			if err := s.cleanupIdenticalRecoveryBranch(ctx, txID); err != nil {
				return fmt.Errorf("clean already-committed transaction %q: %w", txID, err)
			}
			slog.Info("RecoverOpenTransactions: already committed, deleted", "tx_id", txID)
			continue
		}

		// Recovery restores the transaction's originally-applied timeout (both
		// BeginTransaction's granted timeout and any ExtendTimeout extension are
		// persisted to the branch state at ApplyTime-affecting events) so a
		// recovered transaction retains its true absolute lifetime bound instead of
		// silently resetting to the 7-day hard maximum. A transaction whose branch
		// record predates the persisted timeout (zero) still falls back to the hard
		// maximum, matching the pre-persistence recovery behavior.
		restoredTimeout := durableState.AppliedTimeout
		if restoredTimeout <= 0 {
			restoredTimeout = HardMaxTimeout
		}
		state, err := s.txManager.Create(txID, restoredTimeout, durableState.MainHeadAtLastSync)
		if err != nil {
			return fmt.Errorf("register recovered transaction %q: %w", txID, err)
		}
		// SPEC R9 ("the timeout is an absolute lifetime from BeginTransaction,
		// not an idle timeout"): the durable record carries the original begin
		// instant (CreatedAt) and the expiry granted by the last
		// timeout-affecting event (ExpiresAt — set by BeginTransaction and
		// updated by ExtendTimeout), so the recovered transaction keeps its
		// absolute lifetime instead of re-basing both from the restart instant.
		// Without this, every restart re-armed the 7-day hard-maximum baseline
		// (enforced against CreatedAt in ExtendTimeout) from the restart moment,
		// letting a transaction live materially longer than 7 days measured from
		// the original begin. A transaction whose absolute lifetime has already
		// elapsed at restart stays expired: its operations surface
		// DEADLINE_EXCEEDED and the GC rolls it back within the cleanup grace
		// period. A record that predates the persisted lifetime (zero CreatedAt)
		// keeps the pre-persistence re-base fallback.
		if !durableState.CreatedAt.IsZero() {
			state.CreatedAt = durableState.CreatedAt
			state.ExpiresAt = durableState.ExpiresAt
			if state.ExpiresAt.IsZero() {
				state.ExpiresAt = state.CreatedAt.Add(restoredTimeout)
			}
		}
		state.ChangeLog = cl
		state.RollbackOnly = durableState.RollbackOnly
		state.SchemaHash = durableState.SchemaHash
		state.CommitStarted = durableState.CommitStarted
		state.CommitCreated = durableState.CommitCreated
		// The unconditional startup rebuild (rehydrateMainAfterRecovery,
		// cmd/main.go) re-hydrated main.lbug from git main's working tree
		// before recovery ran. A durable CommitHydrated=true records that
		// main.lbug held the transaction's data at crash time — but the
		// rebuild read git main, which contains the transaction only if the
		// merge landed. The diff above is non-empty, so the merge never
		// landed and main.lbug now serves pre-transaction data; carrying the
		// flag forward would make the retried commit skip re-hydration (the
		// SPEC serialisation-flow retry contract "steps 6-9 are skipped — the
		// main DB is already hydrated" holds only for an in-process retry,
		// where the startup rebuild did not run) and leave main.lbug
		// permanently divergent from git main. Clear it so the retried commit
		// re-hydrates from the transaction branch's files.
		state.CommitHydrated = false
		state.MainRehydrated = durableState.MainRehydrated
		state.MergeCompleted = durableState.MergeCompleted
		// Carry the refresh-in-progress marker: a transaction recovered from a
		// mid-refresh crash stays marked until a later refresh completes or the
		// transaction commits, so recovery never reclassifies it.
		state.BranchRefreshInProgress = durableState.BranchRefreshInProgress
		slog.Info("RecoverOpenTransactions: recovered", "tx_id", txID)
	}
	return nil
}

func (s *CartographerServer) cleanupMissingRecoveryBranch(ctx context.Context, txID string) error {
	return s.cleanupTransaction(ctx, &TransactionState{ID: txID})
}

// isMissingBranchStateError reports whether LoadBranchTransactionState failed
// because no durable lifecycle record exists for the branch — the
// BeginTransaction crash window (git branch + branch DB persisted, state
// record never written). The store signals the missing record with
// ErrBranchStateMissing; every other state error (corrupt record, unsupported
// version, invalid baseline) is a genuine state problem and stays a hard
// failure.
func isMissingBranchStateError(err error) bool {
	return errors.Is(err, store.ErrBranchStateMissing)
}

// isMidRefreshSwapBranch reports whether a branch whose persisted DB failed to
// open carries the refresh-in-progress marker, identifying the mid-refresh
// branch-DB swap crash window (see RecoverOpenTransactions): the marker is
// persisted on the transaction's durable record before the swap's renames
// begin and cleared only by the refresh's final persist, so it is set in every
// mid-swap crash state. A branch marked refresh-in-progress that cannot be
// opened is a swap casualty — the swapped-in .lbug is paired with the stale
// pre-refresh .schema.json — and must be rolled back loudly instead of
// hard-failing startup. A branch without the marker (or with no durable
// record) keeps the hard-error path so genuine corruption stays loud.
func (s *CartographerServer) isMidRefreshSwapBranch(ctx context.Context, txID string) (bool, error) {
	durable, err := s.store.LoadBranchTransactionState(ctx, txID)
	if err != nil {
		if isMissingBranchStateError(err) {
			return false, nil
		}
		return false, err
	}
	return durable.BranchRefreshInProgress, nil
}

func (s *CartographerServer) cleanupIdenticalRecoveryBranch(ctx context.Context, txID string) error {
	return s.cleanupTransaction(ctx, &TransactionState{ID: txID})
}

// cleanupOrphanedTempBranches removes branch files stranded by a mid-refresh
// crash. resetBranchStoreFromWorkingTree builds the refresh replacement branch
// under a temporary key (tempID := s.newIDFn()) and renames
// branches/<tempID>.{lbug,schema.json,state.json} onto the transaction's
// canonical names, so a crash at any point before the swap's rename loop
// completes (or an os.Rename failure inside it) strands the not-yet-renamed
// temp files under branches/. The temporary key never becomes a git branch, so
// the git-branch enumeration above never visits it — without this sweep every
// mid-refresh crash leaks up to three durable files (plus the engine's
// write-ahead-log companion) indefinitely. The live git-branch set is the
// discriminator: BeginTransaction creates the git branch before any branch
// file (and cleanup deletes the files before the git branch), so a valid-UUID
// branch key with files but no git branch is provably a refresh temp key and
// is removed; a key that names a git branch is a live transaction and is never
// touched.
func (s *CartographerServer) cleanupOrphanedTempBranches(ctx context.Context, liveBranches []string) error {
	if s.ladybugPath == "" {
		return nil
	}
	live := make(map[string]bool, len(liveBranches))
	for _, b := range liveBranches {
		live[b] = true
	}
	branchesDir := filepath.Join(s.ladybugPath, "branches")
	entries, err := os.ReadDir(branchesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("list branch files: %w", err)
	}
	dropped := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		key, ok := branchKeyFromFile(entry.Name())
		if !ok || !isValidUUID(key) || live[key] || dropped[key] {
			continue
		}
		dropped[key] = true
		// The engine's write-ahead-log companions (<key>.lbug.wal and
		// <key>.lbug.wal.checkpoint) are the artifacts a crash tears alongside
		// the database file; remove them with it (mirroring removeCorruptedMain)
		// so the sweep leaves no trace of the orphaned temp branch.
		for _, name := range []string{key + ".lbug.wal", key + ".lbug.wal.checkpoint"} {
			if err := os.Remove(filepath.Join(branchesDir, name)); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove orphaned refresh temp branch WAL %q: %w", name, err)
			}
		}
		if err := s.store.DropBranchDB(ctx, key); err != nil {
			return fmt.Errorf("clean up orphaned refresh temp branch %q: %w", key, err)
		}
		slog.Warn("RecoverOpenTransactions: removed orphaned refresh temp branch files", "temp_id", key)
	}
	return nil
}

// branchKeyFromFile derives the branch key a branches-directory file belongs
// to. Files that are not branch files (e.g. the store's .state-*.tmp
// atomic-write temporaries) report ok=false.
func branchKeyFromFile(name string) (string, bool) {
	for _, suffix := range []string{".lbug", ".schema.json", ".state.json"} {
		if before, ok := strings.CutSuffix(name, suffix); ok {
			return before, true
		}
	}
	return "", false
}

// buildMainFileLookups reads all entity and edge files from main (at the
// transaction's baseline head, baseline, when provided — otherwise current
// main) and returns lookup maps keyed by (entityType -> entityID -> file).
// The reconstruction diff (recoverEntityChanges/recoverEdgeChanges) must be
// computed against the transaction's true baseline, not current main: for a
// transaction whose branch predates a main advancement (stale branch), entities
// added to main after the branch began are absent from the branch DB for
// reasons unrelated to the transaction, and reporting them as "suspected
// deletions" would falsely wedge the transaction into an unresolvable ABORTED
// refresh. Reading main at the baseline excludes those post-begin additions
// and yields the correct divergence. An absent/empty baseline (a state without
// a persisted begin head) falls back to current main, matching pre-baseline
// recovery behavior. The caller holds the git lock; this always restores the
// working tree to main before returning.
func (s *CartographerServer) buildMainFileLookups(
	ctx context.Context, baseline string,
) (
	map[string]map[string]gitstore.EntityFile,
	map[string]map[string]gitstore.EdgeFile,
	string,
	error,
) {
	mainEntities := make(map[string]map[string]gitstore.EntityFile)
	mainEdges := make(map[string]map[string]gitstore.EdgeFile)
	var mainHead string
	err := s.gitstore.WithGitLock(func() error {
		if err := s.gitstore.RestoreMain(ctx); err != nil {
			return err
		}
		if err := s.gitstore.CleanUntracked(ctx); err != nil {
			return err
		}
		if baseline != "" {
			if err := s.gitstore.CheckoutCommit(ctx, baseline); err != nil {
				return err
			}
		}
		var err error
		mainHead, err = s.gitstore.BranchHEAD(ctx, "main")
		if err != nil {
			return err
		}
		mainEntityTypes, err := s.gitstore.ListEntityTypes(ctx)
		if err != nil {
			return err
		}
		for _, et := range mainEntityTypes {
			files, err := s.gitstore.ReadAllEntityFiles(ctx, et)
			if err != nil {
				return err
			}
			byID := make(map[string]gitstore.EntityFile, len(files))
			for _, f := range files {
				byID[f.ID] = f
			}
			mainEntities[et] = byID
		}
		mainEdgeTypes, err := s.gitstore.ListEdgeTypes(ctx)
		if err != nil {
			return err
		}
		for _, et := range mainEdgeTypes {
			files, err := s.gitstore.ReadAllEdgeFiles(ctx, et)
			if err != nil {
				return err
			}
			byID := make(map[string]gitstore.EdgeFile, len(files))
			for _, f := range files {
				byID[f.ID] = f
			}
			mainEdges[et] = byID
		}
		// The baseline checkout is a detached-HEAD read; always return the
		// working tree to main so nothing after this read observes the stale
		// tree (recovery cleanup and any subsequent buildMainFileLookups both
		// start from RestoreMain, but leaving the tree on main is the
		// invariant).
		return s.gitstore.RestoreMain(ctx)
	})
	if err != nil {
		return nil, nil, "", err
	}
	return mainEntities, mainEdges, mainHead, nil
}

// recoverEntityChanges classifies branch DB entities against main files and
// adds ChangeLog entries for non-unchanged entities. It also detects suspected
// deletions for entities present in main but absent from the branch DB.
// Returns true if any change was recorded, and an error if a change-log entry
// could not be recorded (e.g. ErrChangeLogFull).
func (s *CartographerServer) recoverEntityChanges(
	cl *gitstore.ChangeLog,
	entities []store.Entity,
	mainEntities map[string]map[string]gitstore.EntityFile,
) (bool, error) {
	anyChange := false
	// Build a set of (type, id) present in the branch DB for deletion detection.
	branchSet := make(map[string]map[string]bool)
	for _, ent := range entities {
		if branchSet[ent.Type] == nil {
			branchSet[ent.Type] = make(map[string]bool)
		}
		branchSet[ent.Type][ent.Id] = true

		mainFile, exists := mainEntities[ent.Type][ent.Id]
		if exists && entityContentEqual(ent, mainFile) {
			continue // unchanged — skip
		}
		kind := gitstore.ChangeModEntity
		if !exists {
			kind = gitstore.ChangeAddEntity
		}
		if err := cl.AddEntry(gitstore.ChangeLogEntry{
			Kind: kind,
			ID:   ent.Id, Type: ent.Type,
			Entity: &gitstore.EntityEntry{
				ID: ent.Id, Type: ent.Type, Properties: ent.Properties,
				Embedding: ent.Embedding, CreatedAt: ent.CreatedAt, UpdatedAt: ent.UpdatedAt,
			},
		}); err != nil {
			return false, fmt.Errorf("record recovered entity change: %w", err)
		}
		anyChange = true
	}
	// Suspected deletions: entities in main but not in the branch DB.
	for et, typeMap := range mainEntities {
		for id := range typeMap {
			if !branchSet[et][id] {
				if err := cl.AddEntry(gitstore.ChangeLogEntry{
					Kind: gitstore.ChangeDelEntity,
					ID:   id, Type: et,
					Suspected: true,
				}); err != nil {
					return false, fmt.Errorf("record recovered entity deletion: %w", err)
				}
				anyChange = true
			}
		}
	}
	return anyChange, nil
}

// recoverEdgeChanges classifies branch DB edges against main files and adds
// ChangeLog entries for non-unchanged edges. It also detects suspected
// deletions for edges present in main but absent from the branch DB.
// Returns true if any change was recorded, and an error if a change-log entry
// could not be recorded (e.g. ErrChangeLogFull).
func (s *CartographerServer) recoverEdgeChanges(
	cl *gitstore.ChangeLog,
	edges []store.Edge,
	mainEdges map[string]map[string]gitstore.EdgeFile,
) (bool, error) {
	anyChange := false
	branchSet := make(map[string]map[string]bool)
	for _, edge := range edges {
		if branchSet[edge.Type] == nil {
			branchSet[edge.Type] = make(map[string]bool)
		}
		branchSet[edge.Type][edge.Id] = true

		mainFile, exists := mainEdges[edge.Type][edge.Id]
		if exists && edgeContentEqual(edge, mainFile) {
			continue // unchanged — skip
		}
		// SPEC R9 recovery step 2 requires "the same comparison logic" as step 1:
		// an edge whose UUID exists in main with different content was modified,
		// an edge absent from main was added. Edge modification cannot be produced
		// through the write path (there is no UpdateEdge), but the recovered
		// branch can legitimately diverge from main on an edge when main advanced
		// out-of-band (e.g. a Sync pull brought in a changed edge file), so
		// the classification must distinguish the two instead of collapsing every
		// differing edge into ChangeAddEdge.
		kind := gitstore.ChangeModEdge
		if !exists {
			kind = gitstore.ChangeAddEdge
		}
		if err := cl.AddEntry(gitstore.ChangeLogEntry{
			Kind: kind,
			ID:   edge.Id, Type: edge.Type,
			Edge: &gitstore.EdgeEntry{
				ID: edge.Id, Type: edge.Type,
				FromEntityID: edge.FromEntityID, ToEntityID: edge.ToEntityID,
				Properties: edge.Properties, CreatedAt: edge.CreatedAt, UpdatedAt: edge.UpdatedAt,
			},
		}); err != nil {
			return false, fmt.Errorf("record recovered edge change: %w", err)
		}
		anyChange = true
	}
	// Suspected edge deletions: edges in main but not in the branch DB.
	for et, typeMap := range mainEdges {
		for id := range typeMap {
			if !branchSet[et][id] {
				if err := cl.AddEntry(gitstore.ChangeLogEntry{
					Kind: gitstore.ChangeDelEdge,
					ID:   id, Type: et,
					Suspected: true,
				}); err != nil {
					return false, fmt.Errorf("record recovered edge deletion: %w", err)
				}
				anyChange = true
			}
		}
	}
	return anyChange, nil
}

// reconcileFailedCommitGitLocked resolves the only ambiguous commit milestone
// from Git before returning an error. The caller holds the Git lock.
func (s *CartographerServer) reconcileFailedCommitGitLocked(
	ctx context.Context, state *TransactionState,
) error {
	if err := s.gitstore.Checkout(ctx, state.ID); err != nil {
		return fmt.Errorf("checkout transaction branch: %w", err)
	}
	commitExists, err := s.gitstore.CommitExistsOnBranch(ctx, state.ID)
	if err != nil {
		return fmt.Errorf("detect transaction commit: %w", err)
	}
	if commitExists {
		state.CommitStarted = true
		state.CommitCreated = true
		return s.persistTransactionState(ctx, state)
	}
	if err := s.gitstore.HardResetToBranch(ctx, state.ID); err != nil {
		return fmt.Errorf("clean transaction branch: %w", err)
	}
	previous := durableTransactionState(state)
	state.CommitStarted = false
	state.CommitCreated = false
	state.CommitHydrated = false
	state.MainRehydrated = false
	// The persist below makes the flag-clearing durable. When no commit
	// milestone was ever reached (all four flags were already false — e.g. a
	// step-5 divergence failure, which the SPEC leaves with the transaction
	// open and unmodified), the cleared state is byte-identical to the durable
	// record and the persist would be a no-op. Skipping it keeps a
	// broken-state corner (an unpersistable empty MainHeadAtLastSync baseline,
	// rejected by the store's own branch-state validation) from turning the
	// step-5 FAILED_PRECONDITION into an INTERNAL reconcile error.
	if previous.CommitStarted || previous.CommitCreated || previous.CommitHydrated || previous.MainRehydrated {
		if err := s.persistTransactionState(ctx, state); err != nil {
			state.CommitStarted = previous.CommitStarted
			state.CommitCreated = previous.CommitCreated
			state.CommitHydrated = previous.CommitHydrated
			state.MainRehydrated = previous.MainRehydrated
			return fmt.Errorf("persist cleared commit state: %w", err)
		}
	}
	return nil
}

func (s *CartographerServer) reapplyTransactionChanges(
	ctx context.Context, txID string, changeLog *gitstore.ChangeLog,
) error {
	for _, entry := range changeLog.Entries() {
		switch entry.Kind {
		case gitstore.ChangeAddEntity:
			_, err := s.store.CreateEntity(
				ctx, entry.Type, entry.ID, entry.Entity.Properties, entry.Entity.Embedding, txID,
			)
			if err != nil {
				if errors.Is(err, store.ErrEntityAlreadyExists) || errors.Is(err, store.ErrEmbeddingDimension) {
					return errRefreshConflict(txID)
				}
				return mapStoreError(err)
			}
		case gitstore.ChangeModEntity:
			_, err := s.store.UpdateEntity(ctx, entry.ID, entry.Entity.Properties, entry.Entity.Embedding, txID)
			if err != nil {
				if errors.Is(err, store.ErrEntityNotFound) || errors.Is(err, store.ErrEmbeddingDimension) {
					return errRefreshConflict(txID)
				}
				return mapStoreError(err)
			}
		case gitstore.ChangeDelEntity:
			_, err := s.store.DeleteEntity(ctx, entry.ID, txID)
			if err != nil && !errors.Is(err, store.ErrEntityNotFound) {
				return mapStoreError(err)
			}
		case gitstore.ChangeAddEdge:
			_, err := s.store.CreateEdge(
				ctx, entry.Type, entry.Edge.FromEntityID, entry.Edge.ToEntityID, entry.Edge.Properties, txID,
			)
			if err != nil {
				if errors.Is(err, store.ErrSourceOrTargetNotFound) || errors.Is(err, store.ErrEmbeddingDimension) {
					return errRefreshConflict(txID)
				}
				return mapStoreError(err)
			}
		case gitstore.ChangeModEdge:
			// The store has no edge-update primitive (edges are immutable through
			// the write path), so a modification cannot be re-applied. A
			// ChangeModEdge only exists in a recovered change log and only when
			// the branch's edge content differs from main's — a divergence
			// validateRefresh always rejects above (before/current edge files
			// differ), so this case is unreachable. Fail loudly instead of
			// silently dropping the modification, leaving the branch in the clean
			// re-hydrated state with the change log preserved (SPEC R9 refresh
			// conflict semantics).
			return errRefreshConflict(txID)
		case gitstore.ChangeDelEdge:
			_, err := s.store.DeleteEdge(ctx, entry.ID, txID)
			if err != nil && !errors.Is(err, store.ErrEdgeNotFound) {
				return mapStoreError(err)
			}
		}
	}
	return nil
}

type gitGraphSnapshot struct {
	entities map[string]gitstore.EntityFile
	edges    map[string]gitstore.EdgeFile
}

func (s *CartographerServer) snapshotWorkingTree(ctx context.Context) (gitGraphSnapshot, error) {
	snapshot := gitGraphSnapshot{
		entities: make(map[string]gitstore.EntityFile),
		edges:    make(map[string]gitstore.EdgeFile),
	}
	entityTypes, err := s.gitstore.ListEntityTypes(ctx)
	if err != nil {
		return snapshot, err
	}
	for _, entityType := range entityTypes {
		files, err := s.gitstore.ReadAllEntityFiles(ctx, entityType)
		if err != nil {
			return snapshot, err
		}
		for _, file := range files {
			snapshot.entities[file.ID] = file
		}
	}
	edgeTypes, err := s.gitstore.ListEdgeTypes(ctx)
	if err != nil {
		return snapshot, err
	}
	for _, edgeType := range edgeTypes {
		files, err := s.gitstore.ReadAllEdgeFiles(ctx, edgeType)
		if err != nil {
			return snapshot, err
		}
		for _, file := range files {
			snapshot.edges[file.ID] = file
		}
	}
	return snapshot, nil
}

func (s *CartographerServer) validateRefresh(
	ctx context.Context,
	state *TransactionState, before, current gitGraphSnapshot,
) error {
	for _, entry := range state.ChangeLog.Entries() {
		switch entry.Kind {
		case gitstore.ChangeAddEntity:
			if _, exists := current.entities[entry.ID]; exists {
				return errRefreshConflict(state.ID)
			}
		case gitstore.ChangeModEntity, gitstore.ChangeDelEntity:
			oldFile, existed := before.entities[entry.ID]
			newFile, exists := current.entities[entry.ID]
			// The change log is map-based and loses operation order, so an
			// entity created then updated (or created then deleted) within the
			// transaction has no baseline file on the branch tree. SPEC R9
			// refresh step 3 conflicts only on UUID overlap against main ("same
			// entity/edge modified on main", SPEC:979) or an embedding-dimension
			// mismatch; both-absent (no baseline AND no main file) is no overlap
			// and must not conflict. Conflict fires only when main's file differs
			// from the transaction's baseline (present↔absent or content change).
			if existed != exists || !reflect.DeepEqual(oldFile, newFile) {
				return errRefreshConflict(state.ID)
			}
		case gitstore.ChangeAddEdge:
			if _, exists := current.edges[entry.ID]; exists {
				return errRefreshConflict(state.ID)
			}
		case gitstore.ChangeModEdge, gitstore.ChangeDelEdge:
			oldFile, existed := before.edges[entry.ID]
			newFile, exists := current.edges[entry.ID]
			// Mirrors the entity case above: an edge created then deleted within
			// the transaction has no baseline file, and both-absent must not
			// conflict (SPEC R9 refresh step 3 — UUID overlap against main only).
			if existed != exists || !reflect.DeepEqual(oldFile, newFile) {
				return errRefreshConflict(state.ID)
			}
		}
		if entry.Entity != nil && len(entry.Entity.Embedding) > 0 {
			dimension, err := s.store.GetEstablishedDimension(ctx, entry.Type, "main")
			if err != nil {
				return mapStoreError(err)
			}
			if dimension > 0 && dimension != len(entry.Entity.Embedding) {
				return errRefreshConflict(state.ID)
			}
		}
	}
	return nil
}

// resetBranchStoreFromWorkingTree rebuilds the transaction's branch DB from
// the current git working tree (the transaction branch, reset to main's state
// at Refresh step 1). SPEC R9 change-log recovery reconstructs the
// transaction's change log from the branch DB on restart, so the branch DB is
// the only durable record of the transaction's mutations — the in-memory
// change log is lost at a crash and the working tree was reset to main. The
// rebuild therefore builds the replacement branch DB (under a temporary key),
// re-applies the transaction's changes onto it, and swaps it in only once it
// is fully built and re-applied, so at every crash point in this sequence the
// durable branch DB — the only durable record of the transaction's mutations —
// holds the complete change set and recovery never sees a clean copy of main
// for a transaction whose changes were still being re-applied (see
// RecoverOpenTransactions' BranchRefreshInProgress guard).
func (s *CartographerServer) resetBranchStoreFromWorkingTree(ctx context.Context, txID string) error {
	if s.ladybugPath == "" {
		// Rebuilding the branch DB from the working tree is re-hydration work;
		// the SPEC error-table row "Commit serialisation or re-hydration failed"
		// assigns INTERNAL to re-hydration failures, and buildBranchStoreFromWorkingTree
		// below already returns INTERNAL for the same work — an unset
		// LADYBUG_DB_PATH is that failure mode surfacing before the first I/O, not
		// a FAILED_PRECONDITION no error-table row assigns.
		return status.Error(codes.Internal, "refresh requires LADYBUG_DB_PATH")
	}
	oldPath := filepath.Join(s.ladybugPath, "branches", txID+".lbug")
	if _, err := os.Stat(oldPath); err != nil {
		// No persisted branch DB file (in-memory branch store — the standard
		// test configuration): nothing survives a process crash, so the
		// drop-and-recreate window below cannot lose durable data. Keep the
		// simple in-place rebuild.
		if err := s.store.DropBranchDB(ctx, txID); err != nil {
			return mapStoreError(err)
		}
		if err := s.buildBranchStoreFromWorkingTree(ctx, txID); err != nil {
			return err
		}
		state, lookupErr := s.txManager.Lookup(txID)
		if lookupErr != nil {
			return errTransactionNotFound(txID)
		}
		if err := s.reapplyTransactionChanges(ctx, txID, state.ChangeLog); err != nil {
			if restoreErr := s.restoreCleanBranchStore(ctx, txID); restoreErr != nil {
				return fmt.Errorf("reapply transaction: %v; restore clean refreshed branch: %w", err, restoreErr)
			}
			return err
		}
		return nil
	}

	// File-backed branch: build the replacement under a temporary key, then
	// swap. The replacement must be fully built (schema replicated + hydrated)
	// and carry the re-applied transaction changes before the existing branch
	// DB is evicted.
	tempID := s.newIDFn()
	if err := s.buildBranchStoreFromWorkingTree(ctx, tempID); err != nil {
		_ = s.store.DropBranchDB(ctx, tempID)
		return err
	}
	state, lookupErr := s.txManager.Lookup(txID)
	if lookupErr != nil {
		_ = s.store.DropBranchDB(ctx, tempID)
		return errTransactionNotFound(txID)
	}
	// Mark the refresh in progress and make the marker durable before the
	// swap: the flag is persisted on the transaction's own record now and on
	// the temporary key's mirror below, so from the moment the swap renames
	// the mirror onto the canonical state record — and for any crash in this
	// refresh — recovery can tell a mid-refresh crash from a genuine
	// post-merge crash (see RecoverOpenTransactions). It is cleared only by
	// RefreshTransaction's final state persist.
	state.BranchRefreshInProgress = true
	if err := s.persistTransactionState(ctx, state); err != nil {
		_ = s.store.DropBranchDB(ctx, tempID)
		return mapStoreError(err)
	}
	// Mirror the transaction's durable lifecycle record under the temporary
	// key so the swap never leaves the branch without a state record (the
	// final persist in RefreshTransaction rewrites it with the refreshed
	// baseline).
	if err := s.store.SaveBranchTransactionState(ctx, tempID, durableTransactionState(state)); err != nil {
		_ = s.store.DropBranchDB(ctx, tempID)
		return mapStoreError(err)
	}
	// Re-apply the transaction's changes onto the replacement branch before the
	// swap, so the branch DB that replaces the old one already carries the full
	// transaction state; the old branch DB (which holds the previous state of
	// the transaction's mutations) is left intact until then, so a crash while
	// re-applying still leaves the durable record of the mutations recoverable.
	// On a conflict (errRefreshConflict / store error) the replacement is
	// discarded and the branch is restored to the SPEC R9 refresh step-4 clean
	// state (re-hydrated from main, no transaction changes applied), matching
	// the pre-refactor behaviour where the post-swap reapply failure triggered a
	// second reset.
	if err := s.reapplyTransactionChanges(ctx, tempID, state.ChangeLog); err != nil {
		_ = s.store.DropBranchDB(ctx, tempID)
		if restoreErr := s.restoreCleanBranchStore(ctx, txID); restoreErr != nil {
			return fmt.Errorf("reapply transaction: %v; restore clean refreshed branch: %w", err, restoreErr)
		}
		return err
	}
	// Evict the old branch's in-memory handle without deleting its files (the
	// store's in-memory handle must be released so the next operation reopens
	// the replacement from the swapped-in files), then close the replacement
	// and move its files onto the transaction's canonical names. Keeping the
	// old files until the atomic rename overwrites them closes the crash
	// window between the eviction and the rename: the durable record of the
	// transaction's mutations is never absent on disk.
	if err := s.store.CloseBranchDB(ctx, txID); err != nil {
		_ = s.store.DropBranchDB(ctx, tempID)
		return mapStoreError(err)
	}
	// Close the replacement branch before renaming its files: the engine's
	// write-ahead-log companion (`<temp>.lbug.wal`) is path-based, and the
	// connection's close is what checkpoints the WAL into `<temp>.lbug`.
	// Renaming an open database file first would leave a crash window in which
	// the swapped-in `<txID>.lbug` is missing the un-checkpointed rows still
	// held in the orphaned WAL; RecoverOpenTransactions would classify those
	// absent entities as suspected deletions and the recovered commit would
	// re-apply them as real deletions of main's committed data. Closing before
	// the rename materialises the file completely, so a crash after the
	// rename cannot lose data.
	if err := s.store.CloseBranchDB(ctx, tempID); err != nil {
		_ = s.store.DropBranchDB(ctx, tempID)
		return mapStoreError(err)
	}
	branchesDir := filepath.Join(s.ladybugPath, "branches")
	// Move the replacement files onto the transaction's canonical names.
	for _, pair := range [][2]string{
		{tempID + ".lbug", txID + ".lbug"},
		{tempID + ".schema.json", txID + ".schema.json"},
		{tempID + ".state.json", txID + ".state.json"},
	} {
		src, dst := filepath.Join(branchesDir, pair[0]), filepath.Join(branchesDir, pair[1])
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("swap refreshed branch DB for %q: %w", txID, err)
		}
	}
	// Release the temporary key's in-memory registration (its files were
	// renamed onto the transaction's canonical names above).
	return mapStoreError(s.store.DropBranchDB(ctx, tempID))
}

// restoreCleanBranchStore re-hydrates a transaction's branch DB to a clean
// copy of the current working tree (SPEC R9 refresh step 4: after an ABORTED
// refresh the branch DB remains at the step-2 re-hydrated state, with no
// transaction changes applied). The durable state record is preserved — with
// the refresh-in-progress marker still set — so recovery distinguishes this
// mid-refresh state from an already-committed transaction.
//
// SPEC R9 refresh step 4's "On ABORTED, the transaction's change log is
// preserved" promise is honoured in-process: RefreshTransaction (transaction.go)
// keeps the in-memory change log intact and returns the ABORTED conflict, so the
// node can call GetTransactionDiff and decide how to proceed (typically
// Rollback). That preservation is deliberately in-memory only — the SPEC
// declares the change log in-memory ("Transaction change log — the Cartographer
// tracks ... in an in-memory log") and reconstructs it from the branch DB on
// restart. In the crash window between an ABORTED refresh and the node's
// rollback, the durable branch DB is this clean copy of main (no transaction
// changes), so restart recovery cannot reconstruct the change log: the diff is
// empty and, because the refresh-in-progress marker is still set on the durable
// record (it is cleared only by the refresh's successful final persist), the
// transaction is rolled back loudly — the SPEC R9 change-log recovery point 4
// posture for an unusable branch — never silently reported as committed and
// never deleting another transaction's data. The in-memory-only trade-off is
// therefore consistent with the SPEC: the change log is inspectable while the
// node is alive, and a crash in this window yields a loud rollback, not silent
// loss.
func (s *CartographerServer) restoreCleanBranchStore(ctx context.Context, txID string) error {
	if err := s.store.CloseBranchDB(ctx, txID); err != nil {
		return mapStoreError(err)
	}
	if s.ladybugPath != "" {
		branchesDir := filepath.Join(s.ladybugPath, "branches")
		for _, f := range []string{txID + ".lbug", txID + ".schema.json"} {
			if err := os.Remove(filepath.Join(branchesDir, f)); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("restore clean refreshed branch %q: %w", txID, err)
			}
		}
	}
	if err := s.buildBranchStoreFromWorkingTree(ctx, txID); err != nil {
		return err
	}
	return nil
}

// buildBranchStoreFromWorkingTree creates a fresh branch DB for key and
// populates it with main's schema and the working tree's file-per-element
// data (Refresh flow step 2).
func (s *CartographerServer) buildBranchStoreFromWorkingTree(ctx context.Context, key string) error {
	if err := s.store.CreateBranchDB(ctx, key); err != nil {
		return mapStoreError(err)
	}
	if err := s.store.ReplicateSchemaToBranch(ctx, key); err != nil {
		return mapStoreError(err)
	}
	entitiesDir, edgesDir := s.gitstore.HydrationDirs()
	if err := s.store.HydrateBranchFromFiles(ctx, key, entitiesDir, edgesDir); err != nil {
		// SPEC error-table row "Commit serialisation or re-hydration failed"
		// (SPEC:987) assigns INTERNAL to re-hydration failures during the
		// transaction lifecycle. The store's hydration surface reports
		// ErrInvalidEntityDir/ErrInvalidEdgeDir for working-tree directory
		// inconsistencies; those are re-hydration failures — not client input
		// errors — so they must surface INTERNAL, never INVALID_ARGUMENT via
		// mapStoreError.
		return status.Errorf(codes.Internal, "re-hydrate branch from working tree: %v", err)
	}
	return nil
}
