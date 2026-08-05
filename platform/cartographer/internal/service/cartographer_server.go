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
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
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

	schemaApplied atomic.Bool
	dbReady       atomic.Bool

	readSecretFn func(ctx context.Context, name string) (map[string]string, error)
	remoteURL    string

	auditor        TelemetryPublisher
	newIDFn        func() string
	podNamespace   string
	defaultTimeout time.Duration

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
		txManager: NewTransactionManager(defaultTimeout, HardMaxTimeout, changeLogCap),
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

func (s *CartographerServer) preflightTransactionChange(ctx context.Context, txID string) error {
	state, lookupErr := s.txManager.Lookup(txID)
	if lookupErr != nil {
		return errTransactionNotFound(txID)
	}
	if err := state.ChangeLog.CheckCapacity(); err != nil {
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
		entityChanged := s.recoverEntityChanges(cl, entities, mainEntities)
		edgeChanged := s.recoverEdgeChanges(cl, edges, mainEdges)

		// SPEC recovery step 5: If the diff is empty (branch DB identical to main),
		// the transaction was already committed — clean up and do not recover.
		if !entityChanged && !edgeChanged {
			if err := s.cleanupIdenticalRecoveryBranch(ctx, txID); err != nil {
				return fmt.Errorf("clean already-committed transaction %q: %w", txID, err)
			}
			slog.Info("RecoverOpenTransactions: already committed, deleted", "tx_id", txID)
			continue
		}

		// ponytail: Every recovered transaction gets the hard-max timeout (7 days)
		// because the original requested timeout was not persisted to the branch DB.
		// A recovered transaction that originally had a 30-minute timeout now lives
		// for up to 7 days unless explicitly rolled back or ExtendTimeout is called
		// with a shorter duration. Upgrade path: persist the requested timeout alongside
		// the branch DB (e.g. a metadata file in the git branch or a sidecar record)
		// and read it back here instead of using the hard maximum.
		state, err := s.txManager.Create(txID, HardMaxTimeout, durableState.MainHeadAtLastSync)
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
// Returns true if any change was recorded.
func (s *CartographerServer) recoverEntityChanges(
	cl *gitstore.ChangeLog,
	entities []store.Entity,
	mainEntities map[string]map[string]gitstore.EntityFile,
) bool {
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
		_ = cl.AddEntry(gitstore.ChangeLogEntry{
			Kind: kind,
			ID:   ent.Id, Type: ent.Type,
			Entity: &gitstore.EntityEntry{
				ID: ent.Id, Type: ent.Type, Properties: ent.Properties,
				Embedding: ent.Embedding, CreatedAt: ent.CreatedAt, UpdatedAt: ent.UpdatedAt,
			},
		})
		anyChange = true
	}
	// Suspected deletions: entities in main but not in the branch DB.
	for et, typeMap := range mainEntities {
		for id := range typeMap {
			if !branchSet[et][id] {
				_ = cl.AddEntry(gitstore.ChangeLogEntry{
					Kind: gitstore.ChangeDelEntity,
					ID:   id, Type: et,
					Suspected: true,
				})
				anyChange = true
			}
		}
	}
	return anyChange
}

// recoverEdgeChanges classifies branch DB edges against main files and adds
// ChangeLog entries for non-unchanged edges. It also detects suspected
// deletions for edges present in main but absent from the branch DB.
// Returns true if any change was recorded.
func (s *CartographerServer) recoverEdgeChanges(
	cl *gitstore.ChangeLog,
	edges []store.Edge,
	mainEdges map[string]map[string]gitstore.EdgeFile,
) bool {
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
		_ = cl.AddEntry(gitstore.ChangeLogEntry{
			Kind: gitstore.ChangeAddEdge,
			ID:   edge.Id, Type: edge.Type,
			Edge: &gitstore.EdgeEntry{
				ID: edge.Id, Type: edge.Type,
				FromEntityID: edge.FromEntityID, ToEntityID: edge.ToEntityID,
				Properties: edge.Properties, CreatedAt: edge.CreatedAt, UpdatedAt: edge.UpdatedAt,
			},
		})
		anyChange = true
	}
	// Suspected edge deletions: edges in main but not in the branch DB.
	for et, typeMap := range mainEdges {
		for id := range typeMap {
			if !branchSet[et][id] {
				_ = cl.AddEntry(gitstore.ChangeLogEntry{
					Kind: gitstore.ChangeDelEdge,
					ID:   id, Type: et,
					Suspected: true,
				})
				anyChange = true
			}
		}
	}
	return anyChange
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

func (s *CartographerServer) resolveBranch(txID string) string {
	if txID == "" {
		return ""
	}
	return txID
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

// extractEntityTypesFromMetadata reads entity type annotations from gRPC
// metadata (set by the SDK from Cypher MATCH patterns) and returns them as
// a string slice. When no entity type metadata is present (system-to-system
// calls, or the SDK could not parse the Cypher), an empty slice is returned.
func extractEntityTypesFromMetadata(ctx context.Context) []string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil
	}
	return md.Get(MetadataKeyEntityTypes)
}

// =========================================================================
// Read Path
// =========================================================================

func (s *CartographerServer) ExecuteCypher(
	ctx context.Context,
	req *flowv1.ExecuteCypherRequest,
) (*flowv1.ExecuteCypherResponse, error) {
	// Authoritative type-specific capability checking per SPEC R2/R3.
	// When the SDK parses the Cypher query, it annotates gRPC metadata with
	// entity type labels via "x-flow-entity-types". Check each extracted
	// type individually; fall back to the wildcard when metadata is absent
	// (e.g. system-to-system calls or pre-SDK clients).
	entityTypes := extractEntityTypesFromMetadata(ctx)
	if len(entityTypes) > 0 {
		for _, et := range entityTypes {
			if err := s.checkEntityCap(ctx, "READ", et); err != nil {
				return nil, err
			}
		}
	} else {
		if err := s.checkWildcardEntityCap(ctx, "READ"); err != nil {
			return nil, err
		}
	}
	if req.Cypher == "" {
		return nil, errEmptyExecuteCypherQuery()
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
	rows, err := s.store.ExecuteCypher(ctx, req.Cypher, params, s.resolveBranch(req.TransactionId))
	if err != nil {
		return nil, mapStoreError(err)
	}
	return &flowv1.ExecuteCypherResponse{Rows: rowsToTuples(rows)}, nil
}

// rowsToTuples converts a []map[string]any result from LadybugDB into a
// []*flowv1.FlatTuple. Each map entry is stringified and wrapped in a
// structpb.Value to produce flat tuples per SPEC §R2.
//
// Go map iteration order is randomised, so ranging over each row's map would
// yield a nondeterministic, unaligned value order. To honour R2's "flat
// tuples" deterministic contract, the column set is the sorted union of every
// row's keys; each tuple lists values in that column order, emitting a null
// for columns absent from that row. This makes value order and column
// alignment identical across rows and across calls.
func rowsToTuples(rows []map[string]any) []*flowv1.FlatTuple {
	columns := make([]string, 0, len(rows))
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		for k := range row {
			if !seen[k] {
				seen[k] = true
				columns = append(columns, k)
			}
		}
	}
	sort.Strings(columns)

	nullVal := structpb.NewNullValue()
	result := make([]*flowv1.FlatTuple, 0, len(rows))
	for _, row := range rows {
		values := make([]*structpb.Value, 0, len(columns))
		for _, col := range columns {
			v, ok := row[col]
			if !ok {
				values = append(values, nullVal)
				continue
			}
			val, err := structpb.NewValue(v)
			if err != nil {
				val = structpb.NewStringValue(fmt.Sprintf("%v", v))
			}
			values = append(values, val)
		}
		result = append(result, &flowv1.FlatTuple{Values: values})
	}
	return result
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
	results, err := s.store.SearchNeighbors(ctx, req.Embedding, req.EntityType, topK, s.resolveBranch(req.TransactionId))
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
	results, err := s.store.FullTextSearch(ctx, req.Query, req.EntityType, s.resolveBranch(req.TransactionId))
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
		ctx, req.EntityType, pageSize, req.PageToken, s.resolveBranch(req.TransactionId),
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
	if !s.store.TableExists(req.EntityType) {
		return nil, errUnknownEntityType(req.EntityType)
	}
	if err := s.checkEntityCap(ctx, "WRITE", req.EntityType); err != nil {
		return nil, err
	}
	unlockTx, err := s.lockTransactionMutation(req.TransactionId)
	if err != nil {
		return nil, err
	}
	defer unlockTx()
	branch := s.resolveBranch(req.TransactionId)

	var ent *store.Entity
	if req.TransactionId != "" {
		if err := s.preflightTransactionChange(ctx, req.TransactionId); err != nil {
			return nil, err
		}
		ent, err = s.store.CreateEntity(ctx, req.EntityType, req.Id, req.Properties, req.Embedding, branch)
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
	} else {
		s.lockMainStore()
		ent, err = s.store.CreateEntity(ctx, req.EntityType, req.Id, req.Properties, req.Embedding, branch)
		s.writeLock.Unlock()
		if err != nil {
			return nil, mapStoreError(err)
		}
	}
	return &flowv1.CreateEntityResponse{
		EntityId: ent.Id, EntityType: ent.Type, Properties: ent.Properties, Embedding: ent.Embedding,
	}, nil
}

func (s *CartographerServer) UpdateEntity(
	ctx context.Context,
	req *flowv1.UpdateEntityRequest,
) (*flowv1.UpdateEntityResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "entity ID is required")
	}
	if !isValidUUID(req.Id) {
		return nil, status.Error(codes.InvalidArgument, "invalid entity ID format")
	}
	unlockTx, err := s.lockTransactionMutation(req.TransactionId)
	if err != nil {
		return nil, err
	}
	defer unlockTx()
	// Resolve entity type for capability check.
	branch := s.resolveBranch(req.TransactionId)
	entityType, resolveErr := s.store.ResolveEntityType(ctx, req.Id, branch)
	if resolveErr != nil {
		return nil, mapStoreError(resolveErr)
	}
	if err := s.checkEntityCap(ctx, "WRITE", entityType); err != nil {
		return nil, err
	}
	var ent *store.Entity
	if req.TransactionId != "" {
		if err := s.preflightTransactionChange(ctx, req.TransactionId); err != nil {
			return nil, err
		}
		ent, err = s.store.UpdateEntity(ctx, req.Id, req.Properties, req.Embedding, branch)
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
	} else {
		s.lockMainStore()
		ent, err = s.store.UpdateEntity(ctx, req.Id, req.Properties, req.Embedding, branch)
		s.writeLock.Unlock()
		if err != nil {
			return nil, mapStoreError(err)
		}
	}
	return &flowv1.UpdateEntityResponse{
		EntityId: ent.Id, EntityType: ent.Type, Properties: ent.Properties, Embedding: ent.Embedding,
	}, nil
}

func (s *CartographerServer) DeleteEntity(
	ctx context.Context,
	req *flowv1.DeleteEntityRequest,
) (*flowv1.DeleteEntityResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "entity ID is required")
	}
	if !isValidUUID(req.Id) {
		return nil, status.Error(codes.InvalidArgument, "invalid entity ID format")
	}
	unlockTx, err := s.lockTransactionMutation(req.TransactionId)
	if err != nil {
		return nil, err
	}
	defer unlockTx()
	// Resolve entity type for capability check.
	branch := s.resolveBranch(req.TransactionId)
	entityType, resolveErr := s.store.ResolveEntityType(ctx, req.Id, branch)
	if resolveErr != nil {
		return nil, mapStoreError(resolveErr)
	}
	if err := s.checkEntityCap(ctx, "WRITE", entityType); err != nil {
		return nil, err
	}
	var ent *store.Entity
	if req.TransactionId != "" {
		if err := s.preflightTransactionChange(ctx, req.TransactionId); err != nil {
			return nil, err
		}
		// Enumerate the edges that DeleteEntity's cascade will remove (DETACH
		// DELETE) so they can be recorded in the change log. Without this, the
		// cascade-deleted edges never reach the log and their git files are not
		// removed on commit, breaking SPEC R7 §4 atomicity ("edges are removed
		// atomically with the entity") across a commit.
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
		ent, err = s.store.DeleteEntity(ctx, req.Id, branch)
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
	} else {
		s.lockMainStore()
		ent, err = s.store.DeleteEntity(ctx, req.Id, branch)
		s.writeLock.Unlock()
		if err != nil {
			return nil, mapStoreError(err)
		}
	}
	return &flowv1.DeleteEntityResponse{
		EntityId: ent.Id, EntityType: ent.Type, Properties: ent.Properties,
	}, nil
}

func (s *CartographerServer) CreateEdge(
	ctx context.Context,
	req *flowv1.CreateEdgeRequest,
) (*flowv1.CreateEdgeResponse, error) {
	if req.EdgeType == "" {
		return nil, status.Error(codes.InvalidArgument, "edge type is required")
	}
	unlockTx, err := s.lockTransactionMutation(req.TransactionId)
	if err != nil {
		return nil, err
	}
	defer unlockTx()
	// SPEC order: structural (unknown edge type / rule) validation precedes
	// entity-existence and capability checks, so an unknown edge type yields
	// INVALID_ARGUMENT even when the caller lacks write capability.
	branch := s.resolveBranch(req.TransactionId)
	if _, ok := s.store.EdgeType(req.EdgeType); !ok {
		return nil, status.Errorf(codes.InvalidArgument,
			"unknown edge type: %q", req.EdgeType)
	}
	// Resolve source entity type for capability check.
	sourceType, resolveErr := s.store.ResolveEntityType(ctx, req.FromEntityId, branch)
	if resolveErr != nil {
		return nil, mapStoreError(resolveErr)
	}
	if err := s.checkEntityCap(ctx, "WRITE", sourceType); err != nil {
		return nil, err
	}
	var edge *store.Edge
	if req.TransactionId != "" {
		if err := s.preflightTransactionChange(ctx, req.TransactionId); err != nil {
			return nil, err
		}
		edge, err = s.store.CreateEdge(ctx, req.EdgeType, req.FromEntityId, req.ToEntityId, req.Properties, branch)
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
	} else {
		s.lockMainStore()
		edge, err = s.store.CreateEdge(ctx, req.EdgeType, req.FromEntityId, req.ToEntityId, req.Properties, branch)
		s.writeLock.Unlock()
		if err != nil {
			return nil, mapStoreError(err)
		}
	}
	return &flowv1.CreateEdgeResponse{
		EdgeId: edge.Id, EdgeType: edge.Type,
		FromEntityId: edge.FromEntityID, ToEntityId: edge.ToEntityID, Properties: edge.Properties,
	}, nil
}

func (s *CartographerServer) DeleteEdge(
	ctx context.Context,
	req *flowv1.DeleteEdgeRequest,
) (*flowv1.DeleteEdgeResponse, error) {
	if req.Id == "" {
		return nil, status.Error(codes.InvalidArgument, "edge ID is required")
	}
	if !isValidUUID(req.Id) {
		return nil, status.Error(codes.InvalidArgument, "invalid edge ID format")
	}
	unlockTx, err := s.lockTransactionMutation(req.TransactionId)
	if err != nil {
		return nil, err
	}
	defer unlockTx()
	// Resolve source entity type for capability check.
	branch := s.resolveBranch(req.TransactionId)
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
	var edge *store.Edge
	if req.TransactionId != "" {
		if err := s.preflightTransactionChange(ctx, req.TransactionId); err != nil {
			return nil, err
		}
		edge, err = s.store.DeleteEdge(ctx, req.Id, branch)
		if err != nil {
			return nil, mapStoreError(err)
		}
		if err := s.addTransactionChange(ctx, req.TransactionId, gitstore.ChangeLogEntry{
			Kind: gitstore.ChangeDelEdge, ID: edge.Id, Type: edge.Type,
		}); err != nil {
			return nil, mapGitError(err)
		}
	} else {
		s.lockMainStore()
		edge, err = s.store.DeleteEdge(ctx, req.Id, branch)
		s.writeLock.Unlock()
		if err != nil {
			return nil, mapStoreError(err)
		}
	}
	return &flowv1.DeleteEdgeResponse{
		EdgeId: edge.Id, EdgeType: edge.Type,
	}, nil
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
			return fmt.Errorf("create branch: %w", err)
		}
		if err := s.gitstore.HardResetToBranch(ctx, txID); err != nil {
			return fmt.Errorf("hard reset: %w", err)
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
		return nil, mapGitError(err)
	}
	if branchStoreErr != nil {
		return nil, errBeginTransactionResourceExhausted(branchStoreErr.Error())
	}
	state, err := s.txManager.Create(txID, requestedTimeout, mainHead)
	if err != nil {
		var cleanups []string
		_ = s.gitstore.WithGitLock(func() error {
			if err := s.gitstore.DeleteBranch(ctx, txID); err != nil {
				cleanups = append(cleanups, fmt.Sprintf("delete branch: %v", err))
			}
			if err := s.gitstore.RestoreMain(ctx); err != nil {
				cleanups = append(cleanups, fmt.Sprintf("restore main: %v", err))
			}
			return nil
		})
		if err := s.store.DropBranchDB(ctx, txID); err != nil {
			cleanups = append(cleanups, fmt.Sprintf("drop branch DB: %v", err))
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

	// Schema compatibility check.
	currentHash := computeSchemaHash(s.store)
	if state.SchemaHash != "" && state.SchemaHash != currentHash {
		return nil, errSchemaChangedIncompatibly("schema changed since tx began")
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
			for et, ids := range tc.deletedEdges {
				if err := s.gitstore.RemoveEdgeFiles(ctx, et, ids); err != nil {
					commitErr = fmt.Errorf("remove edge files: %w", err)
					return nil
				}
			}
		}
		// Divergence check: verify main has not advanced since last sync
		// (SPEC serialisation flow step 5 — must precede step 6 git add+commit).
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
			// SPEC steps 7-8: git lock is outer; main write lock is inner.
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
	// Pull-before-push (inside git lock to ensure atomic fetch+merge+push).
	if s.remoteURL != "" {
		go func() {
			pushCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := s.gitstore.WithGitLock(func() error {
				// ponytail: This pull-before-push diverges from SPEC R10's
				// Commit step 14, which specifies a remote push only (fire-and-
				// forget). FetchAndMerge is an extra, unspecified git operation
				// on the local `main` working tree performed to reduce
				// non-fast-forward push rejections when the remote has advanced
				// since the last sync. Failure modes/consequences:
				//   (1) FetchAndMerge can itself advance local `main` via
				//       setLocalRefAndCheckout, so a rejected push still
				//       leaves local main ahead of (or diverged from) what the
				//       remote accepts, and the divergence keeps re-issuing the
				//       merge on the next commit;
				//   (2) when local and remote have truly diverged,
				//       FetchAndMerge builds a MERGE COMMIT on local `main` —
				//       a state the local transaction history never authored —
				//       which can then be pushed, mixing a peer's commits
				//       (or an older replica's) into the published timeline;
				//   (3) under HA/multi-replica deployment a peer push in the
				//       gap between fetch and push makes this merge race —
				//       FetchAndMerge pulls the peer's commits, then the push
				//       fails as non-fast-forward and the failure is only
				//       logged/telemetry, so a peer's commit may be lost to the
				//       local timeline while main stays consistent on the
				//       remote, requiring a later pull to reconcile;
				//   (4) it adds a network round-trip on every commit and, if
				//       the merge is created, a local re-hydration is skipped
				//       — the branch DB was already re-hydrated from the
				//       pre-merge main, so a merge with remote content is not
				//       reflected in main until the NEXT commit's hydration.
				// Upgrade path: push without mutating local main, retrying the
				// push itself (not a pre-merge) when it fails non-fast-forward;
				// and treat remote divergence as an operator-visible condition
				// (telemetry event) rather than silently merging on the
				// working tree.
				if _, err := s.gitstore.FetchAndMerge(pushCtx, "origin", "main"); err != nil {
					return err
				}
				return s.gitstore.PushRemote(pushCtx)
			}); err != nil {
				slog.Warn("commit: remote push failed", "error", err.Error())
				s.publishTelemetry("cartographer.push_failed", map[string]string{"error": err.Error()})
			}
		}()
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

func (s *CartographerServer) refreshEmptyTransactionHead(
	ctx context.Context, state *TransactionState,
) error {
	var mainHead string
	if err := s.withGitLock(func() error {
		var err error
		mainHead, err = s.gitstore.BranchHEAD(ctx, "main")
		return err
	}); err != nil {
		return err
	}
	oldMainHead := state.MainHeadAtLastSync
	state.MainHeadAtLastSync = mainHead
	if err := s.persistTransactionState(ctx, state); err != nil {
		state.MainHeadAtLastSync = oldMainHead
		return fmt.Errorf("persist refreshed transaction: %w", err)
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
		case gitstore.ChangeDelEdge:
			oldFile, existed := before.edges[entry.ID]
			newFile, exists := current.edges[entry.ID]
			if !existed || !exists || !reflect.DeepEqual(oldFile, newFile) {
				return errRefreshConflict(state.ID)
			}
		}
		if entry.Entity != nil && len(entry.Entity.Embedding) > 0 {
			dimension, err := s.store.GetEstablishedDimension(entry.Type, "main")
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

	if state.ChangeLog.Len() == 0 {
		if err := s.refreshEmptyTransactionHead(ctx, state); err != nil {
			return nil, mapGitError(err)
		}
		return &flowv1.RefreshTransactionResponse{}, nil
	}

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
		if err := s.validateRefresh(state, before, current); err != nil {
			return err
		}
		if err := s.reapplyTransactionChanges(ctx, req.TransactionId, state.ChangeLog); err != nil {
			if resetErr := s.resetBranchStoreFromWorkingTree(ctx, req.TransactionId); resetErr != nil {
				return fmt.Errorf("reapply transaction: %v; restore refreshed branch: %w", err, resetErr)
			}
			return err
		}
		oldMainHead := state.MainHeadAtLastSync
		state.MainHeadAtLastSync = mainHash
		if err := s.persistTransactionState(ctx, state); err != nil {
			state.MainHeadAtLastSync = oldMainHead
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
	if err := s.txManager.ExtendTimeout(req.TransactionId, duration); err != nil {
		return nil, err
	}
	return &flowv1.ExtendTimeoutResponse{}, nil
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
	s.schemaApplied.Store(true)
	return &flowv1.ApplySchemaResponse{}, nil
}

func (s *CartographerServer) WipeGraph(
	ctx context.Context, req *flowv1.WipeGraphRequest,
) (*flowv1.WipeGraphResponse, error) {
	s.txAdmission.Lock()
	defer s.txAdmission.Unlock()
	var wipeErr error
	_ = s.withGitLock(func() error {
		if s.txManager.HasActive() {
			wipeErr = errWipeGraphOpenTransactions()
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
		return nil, wipeErr
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

func (s *CartographerServer) PullFromRemote(
	ctx context.Context, req *flowv1.PullFromRemoteRequest,
) (*flowv1.PullFromRemoteResponse, error) {
	if s.remoteURL == "" {
		return nil, errRemoteNotConfigured()
	}
	if err := s.checkWildcardEntityCap(ctx, "WRITE"); err != nil {
		return nil, err
	}
	var hydrationErr error
	if err := s.withGitLock(func() error {
		empty, err := s.gitstore.IsEmpty(ctx)
		if err != nil {
			return err
		}
		if empty {
			if err := s.gitstore.CloneSingleBranch(ctx, s.remoteURL, "main"); err != nil {
				return err
			}
		} else if _, err := s.gitstore.FetchAndMerge(ctx, "origin", "main"); err != nil {
			return err
		}
		// Unconditionally re-hydrate main from the git working tree on a successful
		// pull (SPEC R10), mirroring the Commit path: derive the file directories
		// from the gitstore so main is refreshed even when running in-memory
		// (ladybugPath unset), keeping the in-memory main consistent with the
		// pulled working tree.
		// ponytail: Unlike SPEC R10, which sequences "release the git lock, then
		// acquire the LadybugDB write lock and drop/re-hydrate main", this path holds
		// the git working-tree lock across lockMainStore() + RehydrateMainFromFiles.
		// Holding both locks together serialises the drop/re-hydrate against any
		// concurrent git mutation (Commit/Refresh/GC) for the duration of the re-hydration,
		// so the working tree cannot change underneath the file read; releasing the git
		// lock first would admit a commit between the fetch and the re-hydrate, giving main
		// a re-hydrate from a stale snapshot. The cost is that a slow re-hydration stalls
		// every git mutation on the single-leader Cartographer for the whole window.
		// Upgrade path: pin the pulled working-tree HEAD (e.g. read the file bytes under
		// the git lock into memory, or record the pulled commit SHA), release the git lock,
		// then re-hydrate under only the write lock — matching SPEC R10 sequencing.
		s.lockMainStore()
		entitiesDir, edgesDir := s.gitstore.HydrationDirs()
		hydrationErr = s.store.RehydrateMainFromFiles(ctx, entitiesDir, edgesDir)
		s.writeLock.Unlock()
		return nil
	}); err != nil {
		return nil, mapGitError(err)
	}
	if hydrationErr != nil {
		return nil, errPullFromRemoteRehydrationFailed(hydrationErr.Error())
	}
	return &flowv1.PullFromRemoteResponse{}, nil
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
	deletedEdges     map[string][]string
}

func groupChanges(cl *gitstore.ChangeLog) typeChanges {
	tc := typeChanges{
		addedEntities:    make(map[string][]*gitstore.EntityEntry),
		modifiedEntities: make(map[string][]*gitstore.EntityEntry),
		deletedEntities:  make(map[string][]string),
		addedEdges:       make(map[string][]*gitstore.EdgeEntry),
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
