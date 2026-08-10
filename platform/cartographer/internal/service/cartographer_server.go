package service

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// HardMaxTimeout is the hard maximum lifetime (inclusive ceiling) for any
// transaction. It pins the constructor default, recovered-transaction timeout,
// and the ExtendTimeout ceiling (enforced via tm.hardMaxTimeout) to a single
// named constant so the three cannot diverge independently on future edits.
const HardMaxTimeout = 7 * 24 * time.Hour

// TelemetryPublisher provides non-blocking telemetry event submission.
type TelemetryPublisher interface {
	Submit(req *flowv1.PublishRequest)
}

// CartographerServer implements flowv1.CartographerServiceServer.
type CartographerServer struct {
	flowv1.UnimplementedCartographerServiceServer

	store       store.Store
	gitstore    gitstore.GitStore
	verifier    *CapabilityVerifier
	ladybugPath string

	txManager   *TransactionManager
	txAdmission sync.RWMutex

	writeLock       sync.Mutex
	beforeWriteLock func() // test barrier; nil in production

	dbReady atomic.Bool

	readSecretFn func(ctx context.Context, name string) (map[string]string, error)
	remoteURL    string

	auditor        TelemetryPublisher
	newIDFn        func() string
	podNamespace   string
	defaultTimeout time.Duration

	syncWorker *SyncWorker

	gcStop chan struct{}
}

// NewCartographerServer creates a new CartographerServer.
func NewCartographerServer(
	s store.Store,
	gs gitstore.GitStore,
	operatorKey, sidecarKey ed25519.PublicKey,
	readSecretFn func(ctx context.Context, name string) (map[string]string, error),
	remoteURL string,
	stalenessWindow time.Duration,
	podNamespace string,
	defaultTimeout time.Duration,
	changeLogCap int,
	opts ...CartographerOption,
) *CartographerServer {
	if podNamespace == "" {
		podNamespace = "default"
	}
	srv := &CartographerServer{
		store:          s,
		gitstore:       gs,
		verifier:       NewCapabilityVerifier(operatorKey, sidecarKey, stalenessWindow),
		readSecretFn:   readSecretFn,
		remoteURL:      remoteURL,
		newIDFn:        uuid.NewString,
		podNamespace:   podNamespace,
		defaultTimeout: defaultTimeout,
		auditor:        nil,
		gcStop:         make(chan struct{}),
		// HardMaxTimeout (7 days) is pinned to the package-level const and passed
		// to NewTransactionManager, which stores it as tm.hardMaxTimeout. ExtendTimeout
		// enforces the ceiling via that field, and RecoverOpenTransactions reuses the
		// same const; all three share one source of truth.
		txManager: NewTransactionManager(HardMaxTimeout, changeLogCap),
	}
	for _, o := range opts {
		o(srv)
	}
	return srv
}

// CartographerOption configures a CartographerServer.
type CartographerOption func(*CartographerServer)

func WithAuditPublisher(pub TelemetryPublisher) CartographerOption {
	return func(s *CartographerServer) { s.auditor = pub }
}

func WithLadybugPath(path string) CartographerOption {
	return func(s *CartographerServer) { s.ladybugPath = path }
}

// WithSyncWorker sets the background sync worker for remote synchronisation.
func WithSyncWorker(sw *SyncWorker) CartographerOption {
	return func(s *CartographerServer) { s.syncWorker = sw }
}

// Verifier returns the capability verifier.
func (s *CartographerServer) Verifier() *CapabilityVerifier { return s.verifier }

func (s *CartographerServer) MarkDBReady() { s.dbReady.Store(true) }
func (s *CartographerServer) StartGC()     { go s.startGC() }
func (s *CartographerServer) StopGC() {
	select {
	case <-s.gcStop:
	default:
		close(s.gcStop)
	}
}

// Publish telemetry event.
func (s *CartographerServer) publishTelemetry(eventType string, attrs map[string]string) {
	if s.auditor != nil {
		s.auditor.Submit(&flowv1.PublishRequest{
			Channel: "telemetry",
			Event: &flowv1.FlowEvent{
				EventId:       s.newIDFn(),
				EventType:     eventType,
				FlowNamespace: s.podNamespace,
				NodeId:        "cartographer",
				Timestamp:     timestamppb.Now(),
				Attributes:    attrs,
			},
		})
	}
}

// ReadSecret wraps readSecretFn.
func (s *CartographerServer) ReadSecret(ctx context.Context, name string) (map[string]string, error) {
	return s.readSecretFn(ctx, name)
}

func (s *CartographerServer) withGitLock(fn func() error) error {
	return s.gitstore.WithGitLock(fn)
}

func (s *CartographerServer) lockMainStore() {
	if s.beforeWriteLock != nil {
		s.beforeWriteLock()
	}
	s.writeLock.Lock()
}

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
	if state.CommitStarted {
		unlock()
		return nil, status.Error(codes.FailedPrecondition, "transaction commit is already in progress")
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
		SchemaHash:         state.SchemaHash,
		CommitStarted:      state.CommitStarted,
		CommitCreated:      state.CommitCreated,
		CommitHydrated:     state.CommitHydrated,
		MainRehydrated:     state.MainRehydrated,
		MergeCompleted:     state.MergeCompleted,
		RollbackOnly:       state.RollbackOnly,
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
			return status.Error(codes.FailedPrecondition,
				"cannot restore main store after partial commit without LADYBUG_DB_PATH")
		}
		s.lockMainStore()
		err := s.store.RehydrateMainFromFiles(ctx,
			filepath.Join(s.ladybugPath, "graph-repo/entities"),
			filepath.Join(s.ladybugPath, "graph-repo/edges"))
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
			return fmt.Errorf("open transaction branch DB %q: %w", txID, dumpErr)
		}
		durableState, stateErr := s.store.LoadBranchTransactionState(ctx, txID)
		if stateErr != nil {
			return fmt.Errorf("read transaction state %q: %w", txID, stateErr)
		}
		edges, dumpErr := s.store.DumpAllEdges(ctx, txID)
		if dumpErr != nil {
			return fmt.Errorf("read transaction branch DB %q: %w", txID, dumpErr)
		}

		mainEntities, mainEdges, _, err := s.buildMainFileLookups(ctx)
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
		if !entityChanged && !edgeChanged {
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
		state.ChangeLog = cl
		state.RollbackOnly = durableState.RollbackOnly
		state.SchemaHash = durableState.SchemaHash
		state.CommitStarted = durableState.CommitStarted
		state.CommitCreated = durableState.CommitCreated
		state.CommitHydrated = durableState.CommitHydrated
		state.MainRehydrated = durableState.MainRehydrated
		state.MergeCompleted = durableState.MergeCompleted
		slog.Info("RecoverOpenTransactions: recovered", "tx_id", txID)
	}
	return nil
}

func (s *CartographerServer) cleanupMissingRecoveryBranch(ctx context.Context, txID string) error {
	return s.cleanupTransaction(ctx, &TransactionState{ID: txID})
}

func (s *CartographerServer) cleanupIdenticalRecoveryBranch(ctx context.Context, txID string) error {
	return s.cleanupTransaction(ctx, &TransactionState{ID: txID})
}

// buildMainFileLookups reads all entity and edge files from main's git working
// tree and returns lookup maps keyed by (entityType -> entityID -> file).
func (s *CartographerServer) buildMainFileLookups(ctx context.Context) (
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
		return nil
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

// startGC runs the GC loop.
// ponytail: The 30-second fixed tick interval is a coarse heuristic suitable
// for typical transaction lifetimes (minutes to hours). On a cluster with many
// short-lived transactions, the GC backlog may accumulate between ticks,
// temporarily occupying git branch and branch-DB resources. On a cluster with
// very long-lived transactions, the frequent tick wastes CPU. Upgrade path:
// make the interval configurable via a server option or env var, or use an
// adaptive interval based on the current transaction count.
func (s *CartographerServer) startGC() {
	ticker := s.txManager.clock.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C():
			s.gcTick()
		case <-s.gcStop:
			return
		}
	}
}

func (s *CartographerServer) gcTick() {
	ctx := context.Background()
	now := s.txManager.clock.Now()
	s.txManager.mu.RLock()
	var expiredTxIDs []string
	for id, state := range s.txManager.active {
		if now.After(state.ExpiresAt.Add(30 * time.Second)) {
			expiredTxIDs = append(expiredTxIDs, id)
		}
	}
	s.txManager.mu.RUnlock()
	if len(expiredTxIDs) == 0 {
		return
	}
	for _, txID := range expiredTxIDs {
		state, unlockTx, ok := s.txManager.lockRegistered(txID)
		if !ok {
			continue
		}
		if !now.After(state.ExpiresAt.Add(30 * time.Second)) {
			unlockTx()
			continue
		}
		var merged bool
		// ponytail: the git working-tree lock (WithGitLock) is held across the
		// store-layer RehydrateMainFromFiles DB I/O below, so a slow, PVC-backed
		// LadybugDB re-hydration serializes every other git operation in the
		// process (commit/rollback/begin/cleanup/recovery, and this GC loop
		// itself) behind it. This keeps the re-hydration's file reads consistent
		// with the main working tree, but at the cost of blocking git work for
		// the full DB I/O duration. Upgrade path: narrow the lock to cover only
		// the git operations (RestoreMain/CleanUntracked/GitLogOneline) and run
		// RehydrateMainFromFiles outside it (e.g. after releasing the git lock,
		// mirroring the narrowed lock scope used in RefreshTransaction), or
		// re-order so re-hydration happens outside the git lock while still
		// holding the writeLock for main-store exclusivity.
		if err := s.withGitLock(func() error {
			if err := s.gitstore.RestoreMain(ctx); err != nil {
				return err
			}
			if err := s.gitstore.CleanUntracked(ctx); err != nil {
				return err
			}
			logs, err := s.gitstore.GitLogOneline(ctx, "transaction:"+txID)
			if err != nil {
				return err
			}
			merged = len(logs) > 0
			if !merged {
				// Re-hydrate main unconditionally (SPEC R10/serialisation
				// re-hydration), even in in-memory mode where ladybugPath is
				// unset, so main stays consistent with the git working tree.
				s.lockMainStore()
				entitiesDir, edgesDir := s.gitstore.HydrationDirs()
				err := s.store.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
				s.writeLock.Unlock()
				if err != nil {
					return err
				}
				state.MainRehydrated = false
			}
			return s.cleanupTransactionGitLocked(ctx, state)
		}); err != nil {
			unlockTx()
			continue
		}
		if err := s.finishTransactionCleanup(ctx, state); err != nil {
			unlockTx()
			continue
		}
		unlockTx()
		s.publishTelemetry("cartographer.transaction_gc", map[string]string{"tx_id": txID, "reason": "timeout"})
	}
}

// computeSchemaHash hashes all compatibility-relevant application schema data.
func computeSchemaHash(schema store.SchemaProvider) string {
	type canonicalRule struct {
		CanConnectTo []string
		Using        []string
	}
	type canonicalEntity struct {
		Name              string
		Properties        []store.PropertyDef
		EnableVectorIndex bool
		Rules             []canonicalRule
	}
	type canonicalEdge struct {
		Name       string
		Properties []store.PropertyDef
	}
	canonical := struct {
		Entities []canonicalEntity
		Edges    []canonicalEdge
	}{}
	for _, name := range sortedCopy(schema.EntityTypeNames()) {
		def, ok := schema.EntityType(name)
		if !ok {
			continue
		}
		entity := canonicalEntity{
			Name: name, Properties: sortedProperties(def.Properties), EnableVectorIndex: def.EnableVectorIndex,
		}
		for _, rule := range def.Rules {
			entity.Rules = append(entity.Rules, canonicalRule{
				CanConnectTo: sortedCopy(rule.CanConnectTo), Using: sortedCopy(rule.Using),
			})
		}
		sort.Slice(entity.Rules, func(i, j int) bool {
			left, _ := json.Marshal(entity.Rules[i])
			right, _ := json.Marshal(entity.Rules[j])
			return string(left) < string(right)
		})
		canonical.Entities = append(canonical.Entities, entity)
	}
	for _, name := range sortedCopy(schema.EdgeTypeNames()) {
		def, ok := schema.EdgeType(name)
		if !ok {
			continue
		}
		canonical.Edges = append(canonical.Edges, canonicalEdge{Name: name, Properties: sortedProperties(def.Properties)})
	}
	encoded, _ := json.Marshal(canonical)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func sortedCopy(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func sortedProperties(properties []store.PropertyDef) []store.PropertyDef {
	result := append([]store.PropertyDef(nil), properties...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name != result[j].Name {
			return result[i].Name < result[j].Name
		}
		if result[i].Type != result[j].Type {
			return result[i].Type < result[j].Type
		}
		return !result[i].Required && result[j].Required
	})
	return result
}

// checkEntityCap implements Mode 1 + Mode 2 capability checking for entity
// operations. It first checks for a specific type (<prefix>:graph/entity/<type>),
// then falls back to the wildcard (<prefix>:graph/entity/*).
func (s *CartographerServer) checkEntityCap(ctx context.Context, prefix, entityType string) error {
	caps, err := ExtractCapabilities(ctx)
	if err != nil {
		return err
	}
	if caps == nil {
		return errCapabilityDenied(prefix + ":graph/entity/" + entityType)
	}
	if err := s.verifier.CheckSpecificType(caps, prefix, entityType); err != nil {
		if wErr := s.verifier.CheckWildcard(caps, prefix); wErr != nil {
			return errCapabilityDenied(prefix + ":graph/entity/" + entityType)
		}
	}
	return nil
}

// checkTxCap checks that the caller holds the exact required transaction
// capability (e.g. "WRITE:graph/tx" or "READ:graph/tx").
func (s *CartographerServer) checkTxCap(ctx context.Context, required string) error {
	caps, err := ExtractCapabilities(ctx)
	if err != nil {
		return err
	}
	if caps == nil {
		return errCapabilityDenied(required)
	}
	if slices.Contains(caps.Caps, required) {
		return nil
	}
	return errCapabilityDenied(required)
}

// checkWildcardEntityCap checks that the caller holds the wildcard entity
// capability (<prefix>:graph/entity/*). It uses already-verified capabilities
// from the context (stored by the ingress interceptor verify()).
func (s *CartographerServer) checkWildcardEntityCap(ctx context.Context, prefix string) error {
	caps, err := ExtractCapabilities(ctx)
	if err != nil {
		return err
	}
	if caps == nil {
		return errCapabilityDenied(prefix + ":graph/entity/*")
	}
	if err := s.verifier.CheckWildcard(caps, prefix); err != nil {
		return errCapabilityDenied(prefix + ":graph/entity/*")
	}
	return nil
}

// =========================================================================
// Read Path
// =========================================================================

// ExecuteCypher implements the SPEC RPC check order for ExecuteCypher
// (SPEC:958): empty query → Cypher syntax → read-only enforcement →
// capability. The Cartographer is the sole authority for per-type capability
// validation (SPEC R3): the store parses the statement (the same Prepare path
// ExecuteCypher uses) and derives the referenced entity-type labels
// server-side; the caller's capabilities are then checked against each
// distinct label, falling back to READ:graph/entity/* when the statement
// yields no labels. The SDK attaches no entity-type metadata and cannot
// influence the authorization decision.
func (s *CartographerServer) ExecuteCypher(
	ctx context.Context,
	req *flowv1.ExecuteCypherRequest,
) (*flowv1.ExecuteCypherResponse, error) {
	if req.Cypher == "" {
		return nil, errEmptyExecuteCypherQuery()
	}
	// Syntax and read-only enforcement surface before the capability check:
	// ErrMutationCypher maps to PERMISSION_DENIED, ErrInvalidCypher to
	// INVALID_ARGUMENT (mapStoreError), matching the SPEC error table.
	entityTypes, err := s.store.ExtractEntityTypes(ctx, req.Cypher)
	if err != nil {
		return nil, mapStoreError(err)
	}
	if len(entityTypes) > 0 {
		for _, et := range entityTypes {
			if err := s.checkEntityCap(ctx, "READ", et); err != nil {
				return nil, err
			}
		}
	} else {
		// No labels extracted — the statement is a cross-type read (e.g. an
		// unlabelled MATCH) or a pattern the analyzer cannot classify: fall
		// back to the READ:graph/entity/* wildcard check.
		if err := s.checkWildcardEntityCap(ctx, "READ"); err != nil {
			return nil, err
		}
	}
	unlockTx, err := s.lockTransaction(req.TransactionId)
	if err != nil {
		return nil, err
	}
	defer unlockTx()
	var params map[string]any
	if req.Params != nil {
		if s := req.Params.GetStructValue(); s != nil {
			params = s.AsMap()
		} else {
			return nil, errCypherParamsNotAStruct()
		}
	}
	rows, err := s.store.ExecuteCypher(ctx, req.Cypher, params, req.TransactionId)
	if err != nil {
		return nil, mapStoreError(err)
	}
	return &flowv1.ExecuteCypherResponse{Rows: rowsToRows(rows)}, nil
}

// rowsToRows converts ordered LadybugDB rows into the SPEC R2 flat-tuple Row
// contract: each Row is one flat tuple of string values in the order LadybugDB
// returns them — no column reordering and no cross-row alignment or
// null-filling. Values are string-typed in v1 (all properties are type: string
// per R1); non-string scalars and structured values are stringified.
func rowsToRows(rows []store.CypherRow) []*flowv1.Row {
	result := make([]*flowv1.Row, 0, len(rows))
	for _, row := range rows {
		values := make([]string, 0, len(row.Values))
		for _, v := range row.Values {
			values = append(values, cypherValueString(v))
		}
		result = append(result, &flowv1.Row{Values: values})
	}
	return result
}

// cypherValueString stringifies one column value for the string-only v1 row
// contract. Strings pass through verbatim; a null column (e.g. an absent
// property in a RETURN) becomes the empty string, since the v1 wire carries no
// null marker; every other value is formatted with its default representation.
func cypherValueString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func (s *CartographerServer) SearchNeighbors(
	ctx context.Context,
	req *flowv1.SearchNeighborsRequest,
) (*flowv1.SearchNeighborsResponse, error) {
	entityType := req.EntityType
	if entityType == "" {
		if err := s.checkWildcardEntityCap(ctx, "READ"); err != nil {
			return nil, err
		}
	} else {
		if err := s.checkEntityCap(ctx, "READ", entityType); err != nil {
			return nil, err
		}
	}
	topK := int(req.TopK)
	if topK < 0 {
		return nil, errInvalidTopK(topK)
	}
	if topK == 0 {
		topK = 10
	}
	unlockTx, err := s.lockTransaction(req.TransactionId)
	if err != nil {
		return nil, err
	}
	defer unlockTx()
	if len(req.Embedding) == 0 {
		return nil, status.Error(codes.InvalidArgument, "embedding is required")
	}
	for _, v := range req.Embedding {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return nil, status.Error(codes.InvalidArgument, "embedding contains NaN or Inf")
		}
	}
	if entityType != "" && !s.store.TableExists(entityType) {
		return nil, errUnknownEntityType(entityType)
	}
	results, err := s.store.SearchNeighbors(ctx, req.Embedding, req.EntityType, topK, req.TransactionId)
	if err != nil {
		return nil, mapStoreError(err)
	}
	proto := make([]*flowv1.SearchNeighborResult, 0, len(results))
	for _, r := range results {
		proto = append(proto, &flowv1.SearchNeighborResult{
			EntityId: r.Entity.Id, EntityType: r.Entity.Type,
			Properties: r.Entity.Properties, Score: r.Distance,
		})
	}
	return &flowv1.SearchNeighborsResponse{Results: proto}, nil
}

func (s *CartographerServer) FullTextSearch(
	ctx context.Context,
	req *flowv1.FullTextSearchRequest,
) (*flowv1.FullTextSearchResponse, error) {
	if req.EntityType == "" {
		if err := s.checkWildcardEntityCap(ctx, "READ"); err != nil {
			return nil, err
		}
	} else {
		if err := s.checkEntityCap(ctx, "READ", req.EntityType); err != nil {
			return nil, err
		}
	}
	if req.Query == "" {
		return nil, errEmptyFullTextSearchQuery()
	}
	unlockTx, err := s.lockTransaction(req.TransactionId)
	if err != nil {
		return nil, err
	}
	defer unlockTx()
	if req.EntityType != "" && !s.store.TableExists(req.EntityType) {
		return nil, errUnknownEntityType(req.EntityType)
	}
	results, err := s.store.FullTextSearch(ctx, req.Query, req.EntityType, req.TransactionId)
	if err != nil {
		return nil, mapStoreError(err)
	}
	proto := make([]*flowv1.Entity, 0, len(results))
	for _, e := range results {
		proto = append(proto, &flowv1.Entity{
			EntityId: e.Id, EntityType: e.Type, Properties: e.Properties,
		})
	}
	return &flowv1.FullTextSearchResponse{Results: proto}, nil
}

func (s *CartographerServer) ListEntities(
	ctx context.Context,
	req *flowv1.ListEntitiesRequest,
) (*flowv1.ListEntitiesResponse, error) {
	if err := s.checkEntityCap(ctx, "READ", req.EntityType); err != nil {
		return nil, err
	}
	if !s.store.TableExists(req.EntityType) {
		return nil, errUnknownEntityType(req.EntityType)
	}
	unlockTx, err := s.lockTransaction(req.TransactionId)
	if err != nil {
		return nil, err
	}
	defer unlockTx()
	pageSize := int(req.PageSize)
	if pageSize < 0 {
		return nil, errInvalidPageSize(pageSize)
	}
	if pageSize == 0 {
		pageSize = 1000
	}
	if pageSize > 1000 {
		return nil, errInvalidPageSize(pageSize)
	}
	entities, nextToken, err := s.store.ListEntities(
		ctx, req.EntityType, pageSize, req.PageToken, req.TransactionId,
	)
	if err != nil {
		return nil, mapStoreError(err)
	}
	proto := make([]*flowv1.Entity, 0, len(entities))
	for _, e := range entities {
		proto = append(proto, &flowv1.Entity{
			EntityId: e.Id, EntityType: e.Type, Properties: e.Properties,
		})
	}
	return &flowv1.ListEntitiesResponse{Entities: proto, NextPageToken: nextToken}, nil
}

// =========================================================================
// Write Path
// =========================================================================

func (s *CartographerServer) CreateEntity(
	ctx context.Context,
	req *flowv1.CreateEntityRequest,
) (*flowv1.CreateEntityResponse, error) {
	if req.TransactionId == "" {
		return nil, status.Error(codes.FailedPrecondition, "No active transaction")
	}
	// SPEC RPC check order (CreateEntity: active transaction → structural
	// validation → data-integrity): the active-transaction gate (UUID format,
	// existence, rollback-only, timeout, commit-in-progress) runs before the
	// structural and capability checks, so a request combining a nonexistent
	// transaction with a structural fault surfaces NOT_FOUND, not the
	// structural error.
	unlockTx, err := s.lockTransactionMutation(req.TransactionId)
	if err != nil {
		return nil, err
	}
	defer unlockTx()
	if !s.store.TableExists(req.EntityType) {
		return nil, errUnknownEntityType(req.EntityType)
	}
	// SPEC order: structural validation precedes the capability check. An
	// explicitly-supplied ID that is not a valid UUID v4 is structurally invalid
	// and must yield INVALID_ARGUMENT even when the caller lacks write
	// capability (mirrors UpdateEntity/DeleteEntity/DeleteEdge). An empty ID is
	// valid — the store auto-generates it.
	if req.Id != "" && !isValidUUID(req.Id) {
		return nil, status.Error(codes.InvalidArgument, "invalid entity ID format")
	}
	if err := s.checkEntityCap(ctx, "WRITE", req.EntityType); err != nil {
		return nil, err
	}
	branch := req.TransactionId

	if err := s.preflightTransactionChange(ctx, req.TransactionId, gitstore.ChangeLogEntry{
		Kind: gitstore.ChangeAddEntity, ID: req.Id, Type: req.EntityType,
	}); err != nil {
		return nil, err
	}
	ent, err := s.store.CreateEntity(ctx, req.EntityType, req.Id, req.Properties, req.Embedding, branch)
	if err != nil {
		return nil, mapStoreError(err)
	}
	if err := s.addTransactionChange(ctx, req.TransactionId, gitstore.ChangeLogEntry{
		Kind: gitstore.ChangeAddEntity, ID: ent.Id, Type: ent.Type,
		Entity: &gitstore.EntityEntry{
			ID: ent.Id, Type: ent.Type, Properties: ent.Properties,
			Embedding: ent.Embedding, CreatedAt: ent.CreatedAt, UpdatedAt: ent.UpdatedAt,
		},
	}); err != nil {
		return nil, mapGitError(err)
	}
	return &flowv1.CreateEntityResponse{
		EntityId: ent.Id, EntityType: ent.Type, Properties: ent.Properties, Embedding: ent.Embedding,
	}, nil
}

func (s *CartographerServer) UpdateEntity(
	ctx context.Context,
	req *flowv1.UpdateEntityRequest,
) (*flowv1.UpdateEntityResponse, error) {
	if req.TransactionId == "" {
		return nil, status.Error(codes.FailedPrecondition, "No active transaction")
	}
	// SPEC RPC check order (UpdateEntity: active transaction → entity existence
	// → type-specific capability → property/embedding validation): the
	// active-transaction gate runs before the structural ID checks, so a
	// request combining a nonexistent transaction with a missing or
	// malformed ID surfaces NOT_FOUND (or the transaction error), not the
	// structural INVALID_ARGUMENT.
	unlockTx, err := s.lockTransactionMutation(req.TransactionId)
	if err != nil {
		return nil, err
	}
	defer unlockTx()
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "entity ID is required")
	}
	if !isValidUUID(req.Id) {
		return nil, status.Error(codes.InvalidArgument, "invalid entity ID format")
	}
	// Resolve entity type for capability check.
	branch := req.TransactionId
	entityType, resolveErr := s.store.ResolveEntityType(ctx, req.Id, branch)
	if resolveErr != nil {
		return nil, mapStoreError(resolveErr)
	}
	if err := s.checkEntityCap(ctx, "WRITE", entityType); err != nil {
		return nil, err
	}
	if err := s.preflightTransactionChange(ctx, req.TransactionId, gitstore.ChangeLogEntry{
		Kind: gitstore.ChangeModEntity, ID: req.Id, Type: entityType,
	}); err != nil {
		return nil, err
	}
	ent, err := s.store.UpdateEntity(ctx, req.Id, req.Properties, req.Embedding, branch)
	if err != nil {
		return nil, mapStoreError(err)
	}
	if err := s.addTransactionChange(ctx, req.TransactionId, gitstore.ChangeLogEntry{
		Kind: gitstore.ChangeModEntity, ID: ent.Id, Type: ent.Type,
		Entity: &gitstore.EntityEntry{
			ID: ent.Id, Type: ent.Type, Properties: ent.Properties,
			Embedding: ent.Embedding, CreatedAt: ent.CreatedAt, UpdatedAt: ent.UpdatedAt,
		},
	}); err != nil {
		return nil, mapGitError(err)
	}
	return &flowv1.UpdateEntityResponse{
		EntityId: ent.Id, EntityType: ent.Type, Properties: ent.Properties, Embedding: ent.Embedding,
	}, nil
}

func (s *CartographerServer) DeleteEntity(
	ctx context.Context,
	req *flowv1.DeleteEntityRequest,
) (*flowv1.DeleteEntityResponse, error) {
	if req.TransactionId == "" {
		return nil, status.Error(codes.FailedPrecondition, "No active transaction")
	}
	// SPEC RPC check order (DeleteEntity: active transaction → entity existence
	// → type-specific capability): the active-transaction gate runs before the
	// structural ID checks, so a request combining a nonexistent transaction
	// with a missing or malformed ID surfaces NOT_FOUND (or the transaction
	// error), not the structural INVALID_ARGUMENT.
	unlockTx, err := s.lockTransactionMutation(req.TransactionId)
	if err != nil {
		return nil, err
	}
	defer unlockTx()
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "entity ID is required")
	}
	if !isValidUUID(req.Id) {
		return nil, status.Error(codes.InvalidArgument, "invalid entity ID format")
	}
	// Resolve entity type for capability check.
	branch := req.TransactionId
	entityType, resolveErr := s.store.ResolveEntityType(ctx, req.Id, branch)
	if resolveErr != nil {
		return nil, mapStoreError(resolveErr)
	}
	if err := s.checkEntityCap(ctx, "WRITE", entityType); err != nil {
		return nil, err
	}
	if err := s.preflightTransactionChange(ctx, req.TransactionId, gitstore.ChangeLogEntry{
		Kind: gitstore.ChangeDelEntity, ID: req.Id, Type: entityType,
	}); err != nil {
		return nil, err
	}
	// Enumerate the edges that DeleteEntity's cascade will remove (DETACH
	// DELETE) so they can be recorded in the change log. Without this, the
	// cascade-deleted edges never reach the log and their git files are not
	// removed on commit, breaking SPEC R7 §4 atomicity ("edges are removed
	// atomically with the entity") across a commit.
	// ponytail: this enumeration is an un-paginated full-edge-table scan —
	// DumpAllEdges(ctx, branch) loads every edge in the branch into memory
	// to filter those connected to the deleted entity, so a single delete
	// costs O(E) and D deletes inside one transaction cost O(D×E) (and
	// O(D×E) transient heap for the intermediate allEdges slice). On a
	// graph with a very large edge count and a transaction that deletes
	// many entities this becomes a quadratic stall on the write path,
	// amplified per replica by branch-DB re-hydration. Upgrade path:
	// add a store primitive that lists edges by endpoint (FROM/TO id),
	// or have the store's DeleteEntity return the cascade set it already
	// removes, eliminating the scan entirely.
	var cascadeEdges []store.Edge
	allEdges, dumpErr := s.store.DumpAllEdges(ctx, branch)
	if dumpErr != nil {
		return nil, mapStoreError(dumpErr)
	}
	for _, e := range allEdges {
		if e.FromEntityID == req.Id || e.ToEntityID == req.Id {
			cascadeEdges = append(cascadeEdges, e)
		}
	}
	ent, err := s.store.DeleteEntity(ctx, req.Id, branch)
	if err != nil {
		return nil, mapStoreError(err)
	}
	for _, e := range cascadeEdges {
		if err := s.addTransactionChange(ctx, req.TransactionId, gitstore.ChangeLogEntry{
			Kind: gitstore.ChangeDelEdge, ID: e.Id, Type: e.Type,
		}); err != nil {
			return nil, err
		}
	}
	if err := s.addTransactionChange(ctx, req.TransactionId, gitstore.ChangeLogEntry{
		Kind: gitstore.ChangeDelEntity, ID: ent.Id, Type: ent.Type,
	}); err != nil {
		return nil, err
	}
	return &flowv1.DeleteEntityResponse{
		EntityId: ent.Id, EntityType: ent.Type, Properties: ent.Properties,
	}, nil
}

func (s *CartographerServer) CreateEdge(
	ctx context.Context,
	req *flowv1.CreateEdgeRequest,
) (*flowv1.CreateEdgeResponse, error) {
	if req.TransactionId == "" {
		return nil, status.Error(codes.FailedPrecondition, "No active transaction")
	}
	// SPEC RPC check order (CreateEdge: active transaction → structural →
	// entity existence → type-specific capability → edge-rule auth): the
	// active-transaction gate runs before the structural edge-type check, so a
	// request combining a nonexistent transaction with an empty/unknown edge
	// type surfaces NOT_FOUND, not INVALID_ARGUMENT.
	unlockTx, err := s.lockTransactionMutation(req.TransactionId)
	if err != nil {
		return nil, err
	}
	defer unlockTx()
	if req.EdgeType == "" {
		return nil, status.Error(codes.InvalidArgument, "edge type is required")
	}
	// SPEC order: structural (unknown edge type / rule) validation precedes
	// entity-existence and capability checks, so an unknown edge type yields
	// INVALID_ARGUMENT even when the caller lacks write capability.
	branch := req.TransactionId
	edef, ok := s.store.EdgeType(req.EdgeType)
	if !ok {
		return nil, status.Errorf(codes.InvalidArgument,
			"unknown edge type: %q", req.EdgeType)
	}
	// Structural edge-property validation precedes the entity-existence probe
	// (SPEC RPC check-order: CreateEdge: structural → entity existence →
	// capability → edge-rule auth), so an unknown or missing-required edge
	// property yields INVALID_ARGUMENT even when the source entity is missing —
	// the existence probe's NOT_FOUND must not mask the structural error.
	if err := validateEdgePropsForCreate(edef, req.Properties); err != nil {
		return nil, err
	}
	// Resolve source entity type for capability check.
	sourceType, resolveErr := s.store.ResolveEntityType(ctx, req.FromEntityId, branch)
	if resolveErr != nil {
		return nil, mapStoreError(resolveErr)
	}
	// SPEC RPC check-order (CreateEdge: structural → entity existence →
	// type-specific capability → edge-rule auth) and error-table row "Source or
	// target entity not found on CreateEdge → NOT_FOUND" require BOTH endpoint
	// entities' existence to be verified before the capability gate, so a
	// missing target yields NOT_FOUND even when the caller lacks
	// WRITE:graph/entity/<source-type> (the store's CreateEdge also verifies the
	// target, but only after this capability gate, which would surface
	// PERMISSION_DENIED first).
	if _, resolveErr := s.store.ResolveEntityType(ctx, req.ToEntityId, branch); resolveErr != nil {
		return nil, mapStoreError(resolveErr)
	}
	if err := s.checkEntityCap(ctx, "WRITE", sourceType); err != nil {
		return nil, err
	}
	if err := s.preflightTransactionChange(ctx, req.TransactionId, gitstore.ChangeLogEntry{
		Kind: gitstore.ChangeAddEdge, ID: "", Type: req.EdgeType,
	}); err != nil {
		return nil, err
	}
	edge, err := s.store.CreateEdge(ctx, req.EdgeType, req.FromEntityId, req.ToEntityId, req.Properties, branch)
	if err != nil {
		return nil, mapStoreError(err)
	}
	if err := s.addTransactionChange(ctx, req.TransactionId, gitstore.ChangeLogEntry{
		Kind: gitstore.ChangeAddEdge, ID: edge.Id, Type: edge.Type,
		Edge: &gitstore.EdgeEntry{
			ID: edge.Id, Type: edge.Type,
			FromEntityID: edge.FromEntityID, ToEntityID: edge.ToEntityID,
			Properties: edge.Properties, CreatedAt: edge.CreatedAt, UpdatedAt: edge.UpdatedAt,
		},
	}); err != nil {
		return nil, mapGitError(err)
	}
	return &flowv1.CreateEdgeResponse{
		EdgeId: edge.Id, EdgeType: edge.Type,
		FromEntityId: edge.FromEntityID, ToEntityId: edge.ToEntityID, Properties: edge.Properties,
	}, nil
}

// validateEdgePropsForCreate mirrors the store's structural edge-property
// validation (SPEC R6 error table: unknown edge property / missing required
// edge property → INVALID_ARGUMENT). It is surfaced at the service boundary
// before the source/target entity-existence probe so a structurally invalid
// edge property yields INVALID_ARGUMENT rather than a NOT_FOUND-masked
// existence error (SPEC RPC check-order: structural → entity existence).
func validateEdgePropsForCreate(edef *store.EdgeTypeDef, properties map[string]string) error {
	declared := make(map[string]bool, len(edef.Properties))
	for _, p := range edef.Properties {
		declared[p.Name] = true
		if p.Required {
			if _, ok := properties[p.Name]; !ok {
				return status.Errorf(codes.InvalidArgument, "missing required property: %q for edge type %q", p.Name, edef.Name)
			}
		}
	}
	for key := range properties {
		if !declared[key] {
			return status.Errorf(codes.InvalidArgument, "unknown property: %q for edge type %q", key, edef.Name)
		}
	}
	return nil
}

func (s *CartographerServer) DeleteEdge(
	ctx context.Context,
	req *flowv1.DeleteEdgeRequest,
) (*flowv1.DeleteEdgeResponse, error) {
	if req.TransactionId == "" {
		return nil, status.Error(codes.FailedPrecondition, "No active transaction")
	}
	// SPEC RPC check order (DeleteEdge: active transaction → edge existence →
	// type-specific capability): the active-transaction gate runs before the
	// structural ID checks, so a request combining a nonexistent transaction
	// with a missing or malformed ID surfaces NOT_FOUND (or the transaction
	// error), not the structural INVALID_ARGUMENT.
	unlockTx, err := s.lockTransactionMutation(req.TransactionId)
	if err != nil {
		return nil, err
	}
	defer unlockTx()
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "edge ID is required")
	}
	if !isValidUUID(req.Id) {
		return nil, status.Error(codes.InvalidArgument, "invalid edge ID format")
	}
	// Resolve source entity type for capability check.
	branch := req.TransactionId
	existingEdge, edgeErr := s.store.GetEdge(ctx, req.Id, branch)
	if edgeErr != nil {
		return nil, mapStoreError(edgeErr)
	}
	sourceType, resolveErr := s.store.ResolveEntityType(ctx, existingEdge.FromEntityID, branch)
	if resolveErr != nil {
		return nil, mapStoreError(resolveErr)
	}
	if err := s.checkEntityCap(ctx, "WRITE", sourceType); err != nil {
		return nil, err
	}
	if err := s.preflightTransactionChange(ctx, req.TransactionId, gitstore.ChangeLogEntry{
		Kind: gitstore.ChangeDelEdge, ID: req.Id, Type: existingEdge.Type,
	}); err != nil {
		return nil, err
	}
	edge, err := s.store.DeleteEdge(ctx, req.Id, branch)
	if err != nil {
		return nil, mapStoreError(err)
	}
	if err := s.addTransactionChange(ctx, req.TransactionId, gitstore.ChangeLogEntry{
		Kind: gitstore.ChangeDelEdge, ID: edge.Id, Type: edge.Type,
	}); err != nil {
		return nil, mapGitError(err)
	}
	return &flowv1.DeleteEdgeResponse{
		EdgeId: edge.Id, EdgeType: edge.Type,
	}, nil
}

// =========================================================================
// Transaction Path
// =========================================================================

// branchResourceError marks git branch-creation failures in BeginTransaction
// (CreateBranch, HardResetToBranch) that SPEC error-table row "BeginTransaction
// resource exhausted" classifies as RESOURCE_EXHAUSTED: "Out of file handles,
// memory, or disk space; branch or LadybugDB creation failed". BranchHEAD is
// deliberately not wrapped — it is a read of main, so a failure there (e.g. a
// corrupt repo) stays INTERNAL via mapGitError.
type branchResourceError struct{ err error }

func (e *branchResourceError) Error() string { return e.err.Error() }
func (e *branchResourceError) Unwrap() error { return e.err }

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
	// the sync worker so the branch starts from the latest remote state.
	// ponytail: the wait is hard-capped at a fixed 30-second budget so a slow
	// remote cannot tie up the request path unboundedly. The cap truncates any
	// caller deadline longer than 30s (context.WithTimeout derives
	// min(parent deadline, 30s)), so a slow cycle surfaces DEADLINE_EXCEEDED
	// from WakeAndWait even when the caller was willing to wait longer. Here
	// the failure is only logged and the transaction still begins from the
	// current local main head: the consequence is a possibly-stale branch
	// snapshot that later fails the commit-time divergence check
	// (errCommitNotUpToDate) once the periodic cycle advances main —
	// recoverable via RefreshTransaction, but the begin silently proceeded
	// past a sync the caller asked to await. Deployment risk: the wait runs
	// under txAdmission.RLock, so a slow or unreachable remote (recoverable
	// network errors are retried inside the cycle with backoff) stalls every
	// BeginTransaction by up to 30s and blocks WipeGraph (which needs the
	// write lock) behind it. Upgrade path: derive the budget from the caller
	// context when it is shorter than the ceiling, and/or make the ceiling a
	// configurable server knob.
	if s.syncWorker != nil && s.remoteURL != "" {
		syncCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if syncErr := s.syncWorker.WakeAndWait(syncCtx); syncErr != nil {
			slog.Warn("begin tx: sync worker cycle failed, proceeding", "error", syncErr)
		}
	}
	requestedTimeout := s.defaultTimeout
	if req.Timeout != nil {
		requestedTimeout = req.Timeout.AsDuration()
	}
	var mainHead string
	var branchStoreErr error
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
			branchStoreErr = s.store.HydrateBranchFromFiles(ctx, txID,
				filepath.Join(s.ladybugPath, "graph-repo/entities"),
				filepath.Join(s.ladybugPath, "graph-repo/edges"))
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
	state.SchemaHash = computeSchemaHash(s.store)
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
		// The mainHeadAtLastSync != "" clause skips the check when no baseline
		// is recorded (a state created without one) rather than failing closed.
		// This is a defensive fallback: BeginTransaction always snapshots main's
		// HEAD as the baseline and persisted branch states are validated to
		// carry one (transaction_state.go), so the empty value is normally
		// unreachable. Consequence of the skip: a stale-branch commit is caught
		// later by step 10's fast-forward merge, surfacing INTERNAL
		// (ErrMergeDiverged) instead of step 5's FAILED_PRECONDITION
		// (errCommitNotUpToDate) — an accepted error-code difference for the
		// no-baseline corner.
		curHead, err := s.gitstore.BranchHEAD(ctx, "main")
		if err != nil {
			commitErr = fmt.Errorf("branch head: %w", err)
			return nil
		}
		if curHead != mainHeadAtLastSync && mainHeadAtLastSync != "" {
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
				err = s.store.RehydrateMainFromFiles(ctx,
					filepath.Join(s.ladybugPath, "graph-repo/entities"),
					filepath.Join(s.ladybugPath, "graph-repo/edges"))
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
	// ponytail: the WithAck wait is hard-capped at a fixed 30-second budget
	// so a slow remote cannot tie up the request path unboundedly. The cap
	// truncates any caller deadline longer than 30s (context.WithTimeout
	// derives min(parent deadline, 30s)), so a slow cycle surfaces
	// DEADLINE_EXCEEDED from WakeAndWait and CommitTransaction reports failure
	// even though the caller was willing to wait longer. Deployment risk: the
	// commit (git merge, main re-hydration, cleanup) and SetPushNeeded have
	// already completed before the wait, so a timed-out ack returns an error
	// for a commit that is actually durable — the push flag stays set and the
	// periodic cycle delivers the push later, and a client retry is safe (the
	// MergeCompleted path finishes cleanup and returns success), but the
	// ambiguity is real. The wait also holds the transaction lifecycle lock,
	// serializing other RPCs on the same transaction for up to 30s. Upgrade
	// path: derive the budget from the caller context when it is shorter than
	// the ceiling, and/or make the ceiling a configurable server knob.
	if s.syncWorker != nil {
		s.syncWorker.SetPushNeeded()
		if req.GetAck() {
			syncCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if syncErr := s.syncWorker.WakeAndWait(syncCtx); syncErr != nil {
				return nil, mapGitError(syncErr)
			}
		}
	}
	return &flowv1.CommitTransactionResponse{}, nil
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
	if err := s.persistTransactionState(ctx, state); err != nil {
		state.CommitStarted = previous.CommitStarted
		state.CommitCreated = previous.CommitCreated
		state.CommitHydrated = previous.CommitHydrated
		state.MainRehydrated = previous.MainRehydrated
		return fmt.Errorf("persist cleared commit state: %w", err)
	}
	return nil
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
	if state.MergeCompleted {
		return nil, status.Error(codes.FailedPrecondition,
			"transaction is already committed; retry CommitTransaction to finish cleanup")
	}
	if err := s.cleanupTransaction(ctx, state); err != nil {
		return nil, mapGitError(err)
	}
	return &flowv1.RollbackTransactionResponse{}, nil
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
			if !existed || !exists || !reflect.DeepEqual(oldFile, newFile) {
				return errRefreshConflict(state.ID)
			}
		case gitstore.ChangeAddEdge:
			if _, exists := current.edges[entry.ID]; exists {
				return errRefreshConflict(state.ID)
			}
		case gitstore.ChangeModEdge, gitstore.ChangeDelEdge:
			oldFile, existed := before.edges[entry.ID]
			newFile, exists := current.edges[entry.ID]
			if !existed || !exists || !reflect.DeepEqual(oldFile, newFile) {
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

func (s *CartographerServer) resetBranchStoreFromWorkingTree(ctx context.Context, txID string) error {
	if s.ladybugPath == "" {
		return status.Error(codes.FailedPrecondition, "refresh requires LADYBUG_DB_PATH")
	}
	if err := s.store.DropBranchDB(ctx, txID); err != nil {
		return mapStoreError(err)
	}
	if err := s.store.CreateBranchDB(ctx, txID); err != nil {
		return mapStoreError(err)
	}
	if err := s.store.ReplicateSchemaToBranch(ctx, txID); err != nil {
		return mapStoreError(err)
	}
	if err := s.store.HydrateBranchFromFiles(ctx, txID,
		filepath.Join(s.ladybugPath, "graph-repo/entities"),
		filepath.Join(s.ladybugPath, "graph-repo/edges")); err != nil {
		return mapStoreError(err)
	}
	return nil
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
	if state.CommitStarted {
		return nil, status.Error(codes.FailedPrecondition, "transaction commit is already in progress")
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
			return err
		}
		if err := s.reapplyTransactionChanges(ctx, req.TransactionId, state.ChangeLog); err != nil {
			if resetErr := s.resetBranchStoreFromWorkingTree(ctx, req.TransactionId); resetErr != nil {
				return fmt.Errorf("reapply transaction: %v; restore refreshed branch: %w", err, resetErr)
			}
			return err
		}
		oldMainHead := state.MainHeadAtLastSync
		oldSchemaHash := state.SchemaHash
		state.MainHeadAtLastSync = mainHash
		// The branch DB has been reset and re-hydrated from latest main, so the
		// schema baseline is refreshed to the current schema: a refreshed
		// transaction is re-based on main's schema and can commit even after a
		// schema push (SPEC R2/R6 non-destructive changes no longer block the
		// commit-time compatibility check, and a destructive change would be
		// re-detected against the refreshed baseline).
		state.SchemaHash = computeSchemaHash(s.store)
		if err := s.persistTransactionState(ctx, state); err != nil {
			state.MainHeadAtLastSync = oldMainHead
			state.SchemaHash = oldSchemaHash
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
	_, unlockTx, err := s.txManager.LockActive(req.TransactionId)
	if err != nil {
		return nil, err
	}
	defer unlockTx()
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

// =========================================================================
// Service-Facing RPCs
// =========================================================================

func (s *CartographerServer) ApplySchema(
	ctx context.Context, req *flowv1.ApplySchemaRequest,
) (*flowv1.ApplySchemaResponse, error) {
	if !s.dbReady.Load() {
		return nil, errApplySchemaBeforeDBReady()
	}
	if err := s.store.ApplySchema(ctx, req.Schema); err != nil {
		return nil, mapStoreError(err)
	}
	return &flowv1.ApplySchemaResponse{}, nil
}

func (s *CartographerServer) WipeGraph(
	ctx context.Context, req *flowv1.WipeGraphRequest,
) (*flowv1.WipeGraphResponse, error) {
	s.txAdmission.Lock()
	defer s.txAdmission.Unlock()
	var wipeErr error
	lockErr := s.withGitLock(func() error {
		if s.txManager.HasActive() {
			wipeErr = errWipeGraphOpenTransactions()
			return nil
		}
		// Restore the working tree to main before the git rm/commit. The tree
		// can legitimately be checked out on a transaction branch after a
		// failed commit (reconcileFailedCommitGitLocked, RefreshTransaction),
		// and the HasActive guard above excludes expired transactions, so a
		// wipe issued in the GC grace window after a tx expiry would otherwise
		// commit the deletion to the transaction branch and leave main's file
		// history un-wiped. RestoreMain + CleanUntracked make the tree exactly
		// main; both are no-ops when the tree already is main (mirrors the
		// sync cycle's pre-re-hydration sequence).
		if err := s.gitstore.RestoreMain(ctx); err != nil {
			wipeErr = fmt.Errorf("restore main before wipe: %w", err)
			return nil
		}
		if err := s.gitstore.CleanUntracked(ctx); err != nil {
			wipeErr = fmt.Errorf("clean untracked before wipe: %w", err)
			return nil
		}
		if err := s.gitstore.GitRm(ctx, "entities"); err != nil {
			wipeErr = fmt.Errorf("git rm entities: %w", err)
			return nil
		}
		if err := s.gitstore.GitRm(ctx, "edges"); err != nil {
			wipeErr = fmt.Errorf("git rm edges: %w", err)
			return nil
		}
		if err := s.gitstore.Commit(ctx, "wipe"); err != nil {
			wipeErr = err
			return nil
		}
		if err := s.gitstore.CleanUntracked(ctx); err != nil {
			wipeErr = fmt.Errorf("clean untracked: %w", err)
			return nil
		}
		// SPEC R2 WipeGraph: the sequence is git rm -r (entities+edges),
		// commit "wipe", then git clean -fd. Root directories are NOT
		// recreated here — a clean wipe leaves entities/ and edges/ absent.
		// Downstream file-per-element writes (WriteEntityFiles/WriteEdgeFiles)
		// recreate type dirs on demand via MkdirAll, and re-hydration
		// (loadEntitiesFromDir/loadEdgesFromDir) treats a missing dir as empty.
		return nil
	})
	if wipeErr != nil {
		// The open-transactions guard is its own SPEC error (FAILED_PRECONDITION,
		// error-table row 918). Every other git-side failure (git rm entities/edges,
		// wipe commit, clean untracked) is a mid-wipe failure — the graph may be
		// partially cleaned — and maps to INTERNAL per error-table row 940,
		// mirroring the store-side mid-wipe path below (errWipeGraphMidWipe).
		if status.Code(wipeErr) == codes.FailedPrecondition {
			return nil, wipeErr
		}
		return nil, errWipeGraphMidWipe(wipeErr.Error())
	}
	if lockErr != nil {
		return nil, mapGitError(lockErr)
	}
	s.lockMainStore()
	if err := s.store.WipeSchema(ctx); err != nil {
		s.writeLock.Unlock()
		return nil, errWipeGraphMidWipe(err.Error())
	}
	s.writeLock.Unlock()
	return &flowv1.WipeGraphResponse{}, nil
}

func (s *CartographerServer) HealthCheck(
	ctx context.Context, req *flowv1.HealthCheckRequest,
) (*flowv1.HealthCheckResponse, error) {
	health, err := s.store.Health(ctx)
	if err != nil {
		return nil, err
	}
	return &flowv1.HealthCheckResponse{
		LadybugOk: health.LadybugOK, SchemaApplied: health.SchemaApplied, PvcWritable: health.PVCWritable,
	}, nil
}

// =========================================================================
// Administrative Path
// =========================================================================

// Sync wakes the background sync worker and blocks until one full sync cycle
// completes (fetch → merge → re-hydrate → push).
func (s *CartographerServer) Sync(ctx context.Context, req *flowv1.SyncRequest) (*flowv1.SyncResponse, error) {
	if s.remoteURL == "" {
		return nil, errRemoteNotConfigured()
	}
	if err := s.checkWildcardEntityCap(ctx, "WRITE"); err != nil {
		return nil, err
	}
	if s.syncWorker == nil {
		return nil, status.Error(codes.Internal, "sync worker not initialised")
	}
	// SPEC R10 Sync: only a non-recoverable cycle error surfaces to the caller
	// ("If the cycle encounters a non-recoverable error, returns the worker's
	// last error"). A recoverable-exhausted cycle (all retries failed) was
	// already logged + telemetry'd by the worker; Sync reports success and the
	// push flag stays set for the next cycle. WithAck callers keep the full
	// error (SPEC R10: an acked commit returns an error whenever the flag is
	// still set).
	completed, class, err := s.syncWorker.WakeAndWaitClassified(ctx)
	if err == nil {
		return &flowv1.SyncResponse{}, nil
	}
	if !completed {
		return nil, status.FromContextError(err).Err()
	}
	if class != syncNonRecoverable {
		return &flowv1.SyncResponse{}, nil
	}
	return nil, mapGitError(err)
}

// ExportGraph streams the serialised graph.
func (s *CartographerServer) ExportGraph(
	req *flowv1.ExportGraphRequest,
	stream grpc.ServerStreamingServer[flowv1.ExportGraphResponse],
) error {
	ctx := stream.Context()
	if err := s.checkWildcardEntityCap(ctx, "READ"); err != nil {
		return err
	}

	// Reject an unsupported format before enumerating the graph. Collecting the
	// full graph only to discard it on an unsupported format wastes I/O (SPEC
	// R11/R2 maps unsupported format to INVALID_ARGUMENT).
	if format := req.GetFormat(); format != ExportFormatJSON && format != ExportFormatGraphML {
		return errUnsupportedExportFormat(format)
	}

	// collectExportData may panic (e.g. bytes.ErrTooLarge, make() OOM) when the
	// in-memory serialisation buffer exceeds available memory, which would crash
	// the process rather than return a controlled error. The recover below converts
	// such panics to RESOURCE_EXHAUSTED, matching SPEC R11 and the error table.
	var data []byte
	var collectErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				collectErr = errExportGraphBufferAllocation(fmt.Sprintf("%v", r))
			}
		}()
		data, collectErr = collectExportData(s, ctx, req.GetFormat())
	}()
	if collectErr != nil {
		return collectErr
	}

	const chunkSize = 1024 * 64
	for i := 0; i < len(data); i += chunkSize {
		end := min(i+chunkSize, len(data))
		if err := stream.Send(&flowv1.ExportGraphResponse{Chunk: data[i:end]}); err != nil {
			return errExportGraphMidStream(err.Error())
		}
	}
	return nil
}

// groupChanges organises change log entries by type and operation.
type typeChanges struct {
	addedEntities    map[string][]*gitstore.EntityEntry
	modifiedEntities map[string][]*gitstore.EntityEntry
	deletedEntities  map[string][]string
	addedEdges       map[string][]*gitstore.EdgeEntry
	modifiedEdges    map[string][]*gitstore.EdgeEntry
	deletedEdges     map[string][]string
}

func groupChanges(cl *gitstore.ChangeLog) typeChanges {
	tc := typeChanges{
		addedEntities:    make(map[string][]*gitstore.EntityEntry),
		modifiedEntities: make(map[string][]*gitstore.EntityEntry),
		deletedEntities:  make(map[string][]string),
		addedEdges:       make(map[string][]*gitstore.EdgeEntry),
		modifiedEdges:    make(map[string][]*gitstore.EdgeEntry),
		deletedEdges:     make(map[string][]string),
	}
	for _, entry := range cl.Entries() {
		switch entry.Kind {
		case gitstore.ChangeAddEntity:
			tc.addedEntities[entry.Type] = append(tc.addedEntities[entry.Type], entry.Entity)
		case gitstore.ChangeModEntity:
			tc.modifiedEntities[entry.Type] = append(tc.modifiedEntities[entry.Type], entry.Entity)
		case gitstore.ChangeDelEntity:
			tc.deletedEntities[entry.Type] = append(tc.deletedEntities[entry.Type], entry.ID)
		case gitstore.ChangeAddEdge:
			tc.addedEdges[entry.Type] = append(tc.addedEdges[entry.Type], entry.Edge)
		case gitstore.ChangeModEdge:
			tc.modifiedEdges[entry.Type] = append(tc.modifiedEdges[entry.Type], entry.Edge)
		case gitstore.ChangeDelEdge:
			tc.deletedEdges[entry.Type] = append(tc.deletedEdges[entry.Type], entry.ID)
		}
	}
	return tc
}

// toGitEntities converts change-log entity entries to gitstore.Entity values
// for file serialisation. Embeddings are stripped for entity types that are
// not vector-indexed (SPEC R7: embedding data is discarded — not persisted),
// so that no embedding data leaks into the git repository for non-indexed
// types. For indexed types the embedding is retained so corruption recovery
// (SPEC R8) can restore the full graph state including vector data.
func (s *CartographerServer) toGitEntities(entries []*gitstore.EntityEntry) []gitstore.Entity {
	r := make([]gitstore.Entity, len(entries))
	for i, e := range entries {
		emb := e.Embedding
		if def, ok := s.store.EntityType(e.Type); ok && !def.EnableVectorIndex {
			emb = nil
		}
		r[i] = gitstore.Entity{
			ID: e.ID, Type: e.Type, Properties: e.Properties,
			Embedding: emb, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
		}
	}
	return r
}

func toGitEdges(entries []*gitstore.EdgeEntry) []gitstore.Edge {
	r := make([]gitstore.Edge, len(entries))
	for i, e := range entries {
		r[i] = gitstore.Edge{
			ID: e.ID, Type: e.Type, FromEntityID: e.FromEntityID,
			ToEntityID: e.ToEntityID, Properties: e.Properties,
			CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
		}
	}
	return r
}

// entityContentEqual returns true when the data content of a store.Entity
// matches a gitstore.EntityFile (ignoring timestamps, since these may differ
// due to JSON round-trip precision or re-hydration timing).
func entityContentEqual(a store.Entity, b gitstore.EntityFile) bool {
	if a.Id != b.ID || a.Type != b.Type {
		return false
	}
	if len(a.Properties) != len(b.Properties) {
		return false
	}
	for k, v := range a.Properties {
		if b.Properties[k] != v {
			return false
		}
	}
	if len(a.Embedding) != len(b.Embedding) {
		return false
	}
	for i := range a.Embedding {
		if a.Embedding[i] != b.Embedding[i] {
			return false
		}
	}
	return true
}

// edgeContentEqual returns true when the data content of a store.Edge
// matches a gitstore.EdgeFile (ignoring timestamps).
func edgeContentEqual(a store.Edge, b gitstore.EdgeFile) bool {
	if a.Id != b.ID || a.Type != b.Type {
		return false
	}
	if a.FromEntityID != b.FromEntityID || a.ToEntityID != b.ToEntityID {
		return false
	}
	if len(a.Properties) != len(b.Properties) {
		return false
	}
	for k, v := range a.Properties {
		if b.Properties[k] != v {
			return false
		}
	}
	return true
}
