package service

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"sort"
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

	txManager *TransactionManager

	writeLock sync.Mutex

	schemaApplied atomic.Bool
	dbReady       atomic.Bool

	readSecretFn func(ctx context.Context, name string) (map[string]string, error)
	remoteURL    string

	auditor        TelemetryPublisher
	newIDFn        func() string
	podNamespace   string
	defaultTimeout time.Duration

	gcStop chan struct{}

	gitLockHeld atomic.Bool
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
		// ponytail: hardMaxTimeout of 7*24*time.Hour (7 days) is passed as a bare
		// literal to NewTransactionManager. The same constant is used by ExtendTimeout
		// via tm.hardMaxTimeout. If this value drifts on future edits (e.g. is replaced
		// with a config parameter in a different location), ExtendTimeout silently uses
		// the wrong ceiling. There is no named constant shared between the constructor
		// call site and the TransactionManager field. Upgrade path: define a package-level
		// const HardMaxTimeout = 7 * 24 * time.Hour and reference it in both places.
		txManager: NewTransactionManager(defaultTimeout, 7*24*time.Hour, changeLogCap),
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
	s.gitLockHeld.Store(true)
	defer s.gitLockHeld.Store(false)
	return s.gitstore.WithGitLock(fn)
}

func (s *CartographerServer) rollbackTransaction(ctx context.Context, txID string) error {
	if s.gitLockHeld.Load() {
		return status.Error(codes.Internal, "rollbackTransaction called while holding git lock")
	}
	return s.withGitLock(func() error {
		_ = s.gitstore.DeleteBranch(ctx, txID)
		_ = s.gitstore.RestoreMain(ctx)
		_ = s.gitstore.CleanUntracked(ctx)
		s.txManager.Delete(txID)
		_ = s.store.DropBranchDB(txID)
		return nil
	})
}

// RecoverOpenTransactions recovers transactions from a previous crash.
// Implements the SPEC R9 change-log recovery diff algorithm:
//  1. Compare each branch-DB entity/edge against the corresponding main file
//     to classify it as added, modified, or unchanged.
//  2. Detect suspected deletions (entities/edges in main but absent from the branch DB).
//  3. If the branch DB content is identical to main for every entity and edge
//     (diff is empty), the transaction was already committed — clean up and skip.
func (s *CartographerServer) RecoverOpenTransactions(ctx context.Context) error {
	branches, err := s.gitstore.ListBranches(ctx)
	if err != nil {
		return fmt.Errorf("list branches: %w", err)
	}
	for _, branch := range branches {
		if !isValidUUID(branch) {
			continue
		}
		txID := branch
		entities, dumpErr := s.store.DumpAllEntities(ctx, txID)
		edges, _ := s.store.DumpAllEdges(ctx, txID)
		hasBranchDB := dumpErr == nil
		if !hasBranchDB {
			_ = s.gitstore.WithGitLock(func() error {
				return s.gitstore.DeleteBranch(ctx, txID)
			})
			slog.Warn("RecoverOpenTransactions: deleted orphaned git branch", "tx_id", txID)
			continue
		}

		mainEntities, mainEdges := s.buildMainFileLookups(ctx)

		cl := gitstore.NewChangeLog()
		entityChanged := s.recoverEntityChanges(cl, entities, mainEntities)
		edgeChanged := s.recoverEdgeChanges(cl, edges, mainEdges)

		// SPEC recovery step 5: If the diff is empty (branch DB identical to main),
		// the transaction was already committed — clean up and do not recover.
		if !entityChanged && !edgeChanged {
			_ = s.gitstore.WithGitLock(func() error {
				_ = s.gitstore.RestoreMain(ctx)
				_ = s.gitstore.DeleteBranch(ctx, txID)
				return nil
			})
			_ = s.store.DropBranchDB(txID)
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
		state, err := s.txManager.Create(txID, 7*24*time.Hour, "")
		if err != nil {
			_ = s.gitstore.WithGitLock(func() error { _ = s.gitstore.DeleteBranch(ctx, txID); return nil })
			_ = s.store.DropBranchDB(txID)
			continue
		}
		state.ChangeLog = cl
		slog.Info("RecoverOpenTransactions: recovered", "tx_id", txID)
	}
	return nil
}

// buildMainFileLookups reads all entity and edge files from main's git working
// tree and returns lookup maps keyed by (entityType -> entityID -> file).
func (s *CartographerServer) buildMainFileLookups(ctx context.Context) (
	map[string]map[string]gitstore.EntityFile,
	map[string]map[string]gitstore.EdgeFile,
) {
	mainEntities := make(map[string]map[string]gitstore.EntityFile)
	mainEdges := make(map[string]map[string]gitstore.EdgeFile)
	_ = s.gitstore.WithGitLock(func() error {
		_ = s.gitstore.RestoreMain(ctx)
		_ = s.gitstore.CleanUntracked(ctx)
		mainEntityTypes, _ := s.gitstore.ListEntityTypes(ctx)
		for _, et := range mainEntityTypes {
			files, _ := s.gitstore.ReadAllEntityFiles(ctx, et)
			byID := make(map[string]gitstore.EntityFile, len(files))
			for _, f := range files {
				byID[f.ID] = f
			}
			mainEntities[et] = byID
		}
		mainEdgeTypes, _ := s.gitstore.ListEdgeTypes(ctx)
		for _, et := range mainEdgeTypes {
			files, _ := s.gitstore.ReadAllEdgeFiles(ctx, et)
			byID := make(map[string]gitstore.EdgeFile, len(files))
			for _, f := range files {
				byID[f.ID] = f
			}
			mainEdges[et] = byID
		}
		return nil
	})
	return mainEntities, mainEdges
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
		state, err := s.txManager.Lookup(txID)
		if err != nil {
			continue
		}
		if !now.After(state.ExpiresAt.Add(30 * time.Second)) {
			continue
		}
		var merged bool
		_ = s.withGitLock(func() error {
			_ = s.gitstore.RestoreMain(ctx)
			_ = s.gitstore.CleanUntracked(ctx)
			logs, _ := s.gitstore.GitLogOneline(ctx, "transaction:"+txID)
			merged = len(logs) > 0
			_ = s.gitstore.DeleteBranch(ctx, txID)
			return nil
		})
		// ponytail: When ladybugPath is empty (default in-memory config), the
		// !merged re-hydration path is silently skipped. This means expired
		// transactions whose branch was never merged into main leave the in-memory
		// store unchanged — their data persists until the process restarts. This
		// is correct for the dev/stub store (no persistence) but risks stale
		// branch data accumulating in production LadybugDB deployments that
		// neglect to set ladybugPath. Upgrade path: require ladybugPath in
		// production configs and return an error if re-hydration is needed but
		// the path is empty.
		if !merged && s.ladybugPath != "" {
			_ = s.store.WipeAll(ctx)
			_ = s.store.RehydrateMainFromFiles(ctx,
				filepath.Join(s.ladybugPath, "graph-repo/entities"),
				filepath.Join(s.ladybugPath, "graph-repo/edges"))
		}
		s.txManager.Delete(txID)
		_ = s.store.DropBranchDB(txID)
		s.publishTelemetry("cartographer.transaction_gc", map[string]string{"tx_id": txID, "reason": "timeout"})
	}
}

// computeSchemaHash computes a hash of schema type names.
func computeSchemaHash(entityTypes, edgeTypes []string) string {
	h := sha256.New()
	sort.Strings(entityTypes)
	sort.Strings(edgeTypes)
	for _, et := range entityTypes {
		h.Write([]byte(et))
		h.Write([]byte{0})
	}
	for _, et := range edgeTypes {
		h.Write([]byte(et))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
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
// from the context, avoiding the full Ed25519 signature re-verification that
// CheckCapability performs.
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
	if req.TransactionId != "" {
		if err := s.txManager.ValidateActive(req.TransactionId); err != nil {
			return nil, err
		}
	}
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
func rowsToTuples(rows []map[string]any) []*flowv1.FlatTuple {
	result := make([]*flowv1.FlatTuple, 0, len(rows))
	for _, row := range rows {
		values := make([]*structpb.Value, 0, len(row))
		for _, v := range row {
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
	if req.TransactionId != "" {
		if err := s.txManager.ValidateActive(req.TransactionId); err != nil {
			return nil, err
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
	if req.TransactionId != "" {
		if err := s.txManager.ValidateActive(req.TransactionId); err != nil {
			return nil, err
		}
	}
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
	if !s.store.TableExists(req.EntityType) {
		return nil, errUnknownEntityType(req.EntityType)
	}
	if err := s.checkEntityCap(ctx, "READ", req.EntityType); err != nil {
		return nil, err
	}
	if req.TransactionId != "" {
		if err := s.txManager.ValidateActive(req.TransactionId); err != nil {
			return nil, err
		}
	}
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
	if req.TransactionId != "" {
		if err := s.txManager.ValidateActive(req.TransactionId); err != nil {
			return nil, err
		}
	}
	branch := s.resolveBranch(req.TransactionId)

	var ent *store.Entity
	var err error
	if req.TransactionId != "" {
		ent, err = s.store.CreateEntity(ctx, req.EntityType, req.Id, req.Properties, req.Embedding, branch)
		if err != nil {
			return nil, mapStoreError(err)
		}
		if err := s.txManager.AddChangeLogEntry(req.TransactionId, gitstore.ChangeLogEntry{
			Kind: gitstore.ChangeAddEntity, ID: ent.Id, Type: ent.Type,
			Entity: &gitstore.EntityEntry{
				ID: ent.Id, Type: ent.Type, Properties: ent.Properties,
				Embedding: ent.Embedding, CreatedAt: ent.CreatedAt, UpdatedAt: ent.UpdatedAt,
			},
		}); err != nil {
			return nil, mapGitError(err)
		}
	} else {
		s.writeLock.Lock()
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
	// Resolve entity type for capability check.
	branch := s.resolveBranch(req.TransactionId)
	entityType, resolveErr := s.store.ResolveEntityType(ctx, req.Id, branch)
	if resolveErr == nil {
		if err := s.checkEntityCap(ctx, "WRITE", entityType); err != nil {
			return nil, err
		}
	} else {
		// Fall back to wildcard if type resolution fails.
		if err := s.verifier.CheckCapability(ctx, "WRITE:graph/entity/*"); err != nil {
			return nil, err
		}
	}
	if req.TransactionId != "" {
		if err := s.txManager.ValidateActive(req.TransactionId); err != nil {
			return nil, err
		}
	}
	var ent *store.Entity
	var err error
	if req.TransactionId != "" {
		ent, err = s.store.UpdateEntity(ctx, req.Id, req.Properties, req.Embedding, branch)
		if err != nil {
			return nil, mapStoreError(err)
		}
		if err := s.txManager.AddChangeLogEntry(req.TransactionId, gitstore.ChangeLogEntry{
			Kind: gitstore.ChangeModEntity, ID: ent.Id, Type: ent.Type,
			Entity: &gitstore.EntityEntry{
				ID: ent.Id, Type: ent.Type, Properties: ent.Properties,
				Embedding: ent.Embedding, CreatedAt: ent.CreatedAt, UpdatedAt: ent.UpdatedAt,
			},
		}); err != nil {
			return nil, mapGitError(err)
		}
	} else {
		s.writeLock.Lock()
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
	// Resolve entity type for capability check.
	branch := s.resolveBranch(req.TransactionId)
	entityType, resolveErr := s.store.ResolveEntityType(ctx, req.Id, branch)
	if resolveErr == nil {
		if err := s.checkEntityCap(ctx, "WRITE", entityType); err != nil {
			return nil, err
		}
	} else {
		// Fall back to wildcard if type resolution fails.
		if err := s.verifier.CheckCapability(ctx, "WRITE:graph/entity/*"); err != nil {
			return nil, err
		}
	}
	if req.TransactionId != "" {
		if err := s.txManager.ValidateActive(req.TransactionId); err != nil {
			return nil, err
		}
	}

	var ent *store.Entity
	var err error
	if req.TransactionId != "" {
		existing, lookupErr := s.store.GetEntity(ctx, req.Id, branch)
		if lookupErr != nil {
			return nil, mapStoreError(lookupErr)
		}
		if err := s.txManager.AddChangeLogEntry(req.TransactionId, gitstore.ChangeLogEntry{
			Kind: gitstore.ChangeDelEntity, ID: existing.Id, Type: existing.Type,
		}); err != nil {
			return nil, mapGitError(err)
		}
		ent, err = s.store.DeleteEntity(ctx, req.Id, branch)
		if err != nil {
			return nil, mapStoreError(err)
		}
	} else {
		s.writeLock.Lock()
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
	// Resolve source entity type for capability check.
	branch := s.resolveBranch(req.TransactionId)
	sourceType, resolveErr := s.store.ResolveEntityType(ctx, req.FromEntityId, branch)
	if resolveErr == nil {
		if err := s.checkEntityCap(ctx, "WRITE", sourceType); err != nil {
			return nil, err
		}
	} else {
		// Fall back to wildcard if type resolution fails.
		if err := s.verifier.CheckCapability(ctx, "WRITE:graph/entity/*"); err != nil {
			return nil, err
		}
	}
	if req.TransactionId != "" {
		if err := s.txManager.ValidateActive(req.TransactionId); err != nil {
			return nil, err
		}
	}
	var edge *store.Edge
	var err error
	if req.TransactionId != "" {
		edge, err = s.store.CreateEdge(ctx, req.EdgeType, req.FromEntityId, req.ToEntityId, req.Properties, branch)
		if err != nil {
			return nil, mapStoreError(err)
		}
		if err := s.txManager.AddChangeLogEntry(req.TransactionId, gitstore.ChangeLogEntry{
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
		s.writeLock.Lock()
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
	// Resolve source entity type for capability check.
	branch := s.resolveBranch(req.TransactionId)
	existingEdge, edgeErr := s.store.GetEdge(ctx, req.Id, branch)
	if edgeErr == nil {
		sourceType, resolveErr := s.store.ResolveEntityType(ctx, existingEdge.FromEntityID, branch)
		if resolveErr == nil {
			if err := s.checkEntityCap(ctx, "WRITE", sourceType); err != nil {
				return nil, err
			}
		} else {
			// Fall back to wildcard if type resolution fails.
			if err := s.verifier.CheckCapability(ctx, "WRITE:graph/entity/*"); err != nil {
				return nil, err
			}
		}
	} else {
		// Edge not found — let the handler proceed (error will be returned by store).
		// Check wildcard as a reasonable fallback.
		if err := s.verifier.CheckCapability(ctx, "WRITE:graph/entity/*"); err != nil {
			return nil, err
		}
	}
	if req.TransactionId != "" {
		if err := s.txManager.ValidateActive(req.TransactionId); err != nil {
			return nil, err
		}
	}
	var edge *store.Edge
	var err error
	if req.TransactionId != "" {
		edge, err = s.store.DeleteEdge(ctx, req.Id, branch)
		if err != nil {
			return nil, mapStoreError(err)
		}
		if err := s.txManager.AddChangeLogEntry(req.TransactionId, gitstore.ChangeLogEntry{
			Kind: gitstore.ChangeDelEdge, ID: edge.Id, Type: edge.Type,
		}); err != nil {
			return nil, mapGitError(err)
		}
	} else {
		s.writeLock.Lock()
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
	txID := s.newIDFn()
	requestedTimeout := s.defaultTimeout
	if req.Timeout != nil {
		requestedTimeout = req.Timeout.AsDuration()
	}
	var mainHead string
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
		return nil
	}); err != nil {
		return nil, mapGitError(err)
	}
	if err := s.store.CreateBranchDB(txID); err != nil {
		_ = s.gitstore.WithGitLock(func() error {
			_ = s.gitstore.DeleteBranch(ctx, txID)
			_ = s.gitstore.RestoreMain(ctx)
			return nil
		})
		return nil, errBeginTransactionResourceExhausted(fmt.Sprintf("create branch DB: %v", err))
	}
	_ = s.store.ReplicateSchemaToBranch(txID)
	if s.ladybugPath != "" {
		_ = s.store.HydrateBranchFromFiles(ctx, txID,
			filepath.Join(s.ladybugPath, "graph-repo/entities"),
			filepath.Join(s.ladybugPath, "graph-repo/edges"))
	}
	state, err := s.txManager.Create(txID, requestedTimeout, mainHead)
	if err != nil {
		_ = s.gitstore.WithGitLock(func() error {
			_ = s.gitstore.DeleteBranch(ctx, txID)
			_ = s.gitstore.RestoreMain(ctx)
			return nil
		})
		_ = s.store.DropBranchDB(txID)
		return nil, errBeginTransactionResourceExhausted(fmt.Sprintf("register tx: %v", err))
	}
	state.SchemaHash = computeSchemaHash(s.store.EntityTypeNames(), s.store.EdgeTypeNames())
	return &flowv1.BeginTransactionResponse{
		TransactionId: txID, AppliedTimeout: durationpb.New(state.AppliedTimeout),
	}, nil
}

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
	if err := s.txManager.ValidateActive(req.TransactionId); err != nil {
		return nil, err
	}
	state, _ := s.txManager.Lookup(req.TransactionId)

	// Zero-mutation check.
	if state.ChangeLog.Len() == 0 {
		s.txManager.Delete(req.TransactionId)
		_ = s.store.DropBranchDB(req.TransactionId)
		_ = s.gitstore.WithGitLock(func() error {
			_ = s.gitstore.DeleteBranch(ctx, req.TransactionId)
			return s.gitstore.RestoreMain(ctx)
		})
		return &flowv1.CommitTransactionResponse{}, nil
	}

	// Schema compatibility check.
	currentHash := computeSchemaHash(s.store.EntityTypeNames(), s.store.EdgeTypeNames())
	if state.SchemaHash != "" && state.SchemaHash != currentHash {
		return nil, errSchemaChangedIncompatibly("schema changed since tx began")
	}

	var commitErr error
	_ = s.withGitLock(func() error {
		if err := s.gitstore.Checkout(ctx, req.TransactionId); err != nil {
			commitErr = fmt.Errorf("checkout: %w", err)
			return nil
		}
		tc := groupChanges(state.ChangeLog)
		for et, entries := range tc.addedEntities {
			if err := s.gitstore.WriteEntityFiles(ctx, et, toGitEntities(entries)); err != nil {
				commitErr = fmt.Errorf("write entity files: %w", err)
				return nil
			}
		}
		for et, entries := range tc.modifiedEntities {
			if err := s.gitstore.WriteEntityFiles(ctx, et, toGitEntities(entries)); err != nil {
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
		// Divergence check: verify main has not advanced since last sync
		// (SPEC serialisation flow step 5 — must precede step 6 git add+commit).
		curHead, _ := s.gitstore.BranchHEAD(ctx, "main")
		if curHead != state.MainHeadAtLastSync && state.MainHeadAtLastSync != "" {
			commitErr = errCommitNotUpToDate()
			return nil
		}
		_ = s.gitstore.AddAll(ctx, "entities")
		_ = s.gitstore.AddAll(ctx, "edges")
		if err := s.gitstore.Commit(ctx, fmt.Sprintf("transaction:%s", req.TransactionId)); err != nil {
			commitErr = fmt.Errorf("commit: %w", err)
			return nil
		}
		// SPEC steps 7-8: Acquire write lock, wipe + rehydrate main DB.
		s.writeLock.Lock()
		if err := s.store.WipeAll(ctx); err != nil {
			s.writeLock.Unlock()
			commitErr = fmt.Errorf("wipe main store: %w", err)
			return nil
		}
		if s.ladybugPath != "" {
			if err := s.store.RehydrateMainFromFiles(ctx,
				filepath.Join(s.ladybugPath, "graph-repo/entities"),
				filepath.Join(s.ladybugPath, "graph-repo/edges")); err != nil {
				s.writeLock.Unlock()
				commitErr = fmt.Errorf("rehydrate main from files: %w", err)
				return nil
			}
		}
		if err := s.store.RehydrateFromBranch(ctx, req.TransactionId); err != nil {
			s.writeLock.Unlock()
			commitErr = fmt.Errorf("rehydrate from branch: %w", err)
			return nil
		}
		// SPEC step 9: Release write lock.
		s.writeLock.Unlock()

		// SPEC step 10: Fast-forward merge to main.
		if err := s.gitstore.FastForwardMerge(ctx, req.TransactionId, "main"); err != nil {
			commitErr = fmt.Errorf("merge: %w", err)
			return nil
		}
		// SPEC step 11: Restore main in the working tree.
		_ = s.gitstore.RestoreMain(ctx)
		_ = s.gitstore.CleanUntracked(ctx)
		return nil
	})

	// Fire-and-forget push (outside git lock to avoid nested lock acquisition).
	if s.remoteURL != "" {
		go func() {
			pushCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := s.gitstore.WithGitLock(func() error {
				return s.gitstore.PushRemote(pushCtx)
			}); err != nil {
				slog.Warn("commit: remote push failed", "error", err.Error())
				s.publishTelemetry("cartographer.push_failed", map[string]string{"error": err.Error()})
			}
		}()
	}

	if commitErr != nil {
		return nil, mapGitError(commitErr)
	}
	s.txManager.Delete(req.TransactionId)
	_ = s.store.DropBranchDB(req.TransactionId)
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
	if err := s.txManager.ValidateActive(req.TransactionId); err != nil {
		return nil, err
	}
	if err := s.rollbackTransaction(ctx, req.TransactionId); err != nil {
		return nil, err
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
	if err := s.txManager.ValidateActive(req.TransactionId); err != nil {
		return nil, err
	}
	state, _ := s.txManager.Lookup(req.TransactionId)

	if state.ChangeLog.Len() == 0 {
		var mainHead string
		_ = s.gitstore.WithGitLock(func() error {
			var err error
			mainHead, err = s.gitstore.BranchHEAD(ctx, "main")
			return err
		})
		s.txManager.mu.Lock()
		state.MainHeadAtLastSync = mainHead
		s.txManager.mu.Unlock()
		return &flowv1.RefreshTransactionResponse{}, nil
	}

	if err := s.withGitLock(func() error {
		if err := s.gitstore.Checkout(ctx, req.TransactionId); err != nil {
			return err
		}
		mainHash, err := s.gitstore.BranchHEAD(ctx, "main")
		if err != nil {
			return err
		}
		if err := s.gitstore.SetBranchRef(ctx, req.TransactionId, mainHash); err != nil {
			return err
		}
		if err := s.gitstore.CleanUntracked(ctx); err != nil {
			return err
		}
		s.txManager.mu.Lock()
		state.MainHeadAtLastSync = mainHash
		s.txManager.mu.Unlock()
		return nil
	}); err != nil {
		return nil, mapGitError(err)
	}

	if s.ladybugPath != "" {
		_ = s.store.HydrateBranchFromFiles(ctx, req.TransactionId,
			filepath.Join(s.ladybugPath, "graph-repo/entities"),
			filepath.Join(s.ladybugPath, "graph-repo/edges"))
	}
	// SPEC R9 refresh flow steps 3-5: validate against main and re-apply changes.
	for _, entry := range state.ChangeLog.Entries() {
		switch entry.Kind {
		case gitstore.ChangeAddEntity:
			_, err := s.store.CreateEntity(
				ctx, entry.Type, entry.ID, entry.Entity.Properties, entry.Entity.Embedding, req.TransactionId,
			)
			if err != nil {
				if errors.Is(err, store.ErrEntityAlreadyExists) || errors.Is(err, store.ErrEmbeddingDimension) {
					return nil, errRefreshConflict(req.TransactionId)
				}
				return nil, mapStoreError(err)
			}
		case gitstore.ChangeModEntity:
			_, err := s.store.UpdateEntity(ctx, entry.ID, entry.Entity.Properties, entry.Entity.Embedding, req.TransactionId)
			if err != nil {
				if errors.Is(err, store.ErrEntityNotFound) || errors.Is(err, store.ErrEmbeddingDimension) {
					return nil, errRefreshConflict(req.TransactionId)
				}
				return nil, mapStoreError(err)
			}
		case gitstore.ChangeDelEntity:
			_, err := s.store.DeleteEntity(ctx, entry.ID, req.TransactionId)
			if err != nil && !errors.Is(err, store.ErrEntityNotFound) {
				return nil, mapStoreError(err)
			}
		case gitstore.ChangeAddEdge:
			_, err := s.store.CreateEdge(
				ctx, entry.Type, entry.Edge.FromEntityID, entry.Edge.ToEntityID, entry.Edge.Properties, req.TransactionId,
			)
			if err != nil {
				if errors.Is(err, store.ErrSourceOrTargetNotFound) || errors.Is(err, store.ErrEmbeddingDimension) {
					return nil, errRefreshConflict(req.TransactionId)
				}
				return nil, mapStoreError(err)
			}
		case gitstore.ChangeDelEdge:
			_, err := s.store.DeleteEdge(ctx, entry.ID, req.TransactionId)
			if err != nil && !errors.Is(err, store.ErrEdgeNotFound) {
				return nil, mapStoreError(err)
			}
		}
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
	if err := s.txManager.ValidateActive(req.TransactionId); err != nil {
		return nil, err
	}
	state, _ := s.txManager.Lookup(req.TransactionId)
	resp := &flowv1.GetTransactionDiffResponse{}
	for _, entry := range state.ChangeLog.Entries() {
		de := &flowv1.DiffEntry{
			Id: entry.ID, Type: entry.Type,
		}
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
	if err := s.txManager.ValidateActive(req.TransactionId); err != nil {
		return nil, err
	}
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
		_ = s.gitstore.CleanUntracked(ctx)
		return nil
	})
	if wipeErr != nil {
		return nil, wipeErr
	}
	s.writeLock.Lock()
	if err := s.store.WipeAll(ctx); err != nil {
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
		return &flowv1.HealthCheckResponse{
			LadybugOk: false, PvcWritable: false,
		}, nil
	}
	return &flowv1.HealthCheckResponse{
		LadybugOk: health.LadybugOK, SchemaApplied: s.schemaApplied.Load(), PvcWritable: health.PVCWritable,
	}, nil
}

// =========================================================================
// Administrative Path
// =========================================================================

func (s *CartographerServer) PullFromRemote(
	ctx context.Context, req *flowv1.PullFromRemoteRequest,
) (*flowv1.PullFromRemoteResponse, error) {
	if err := s.verifier.CheckCapability(ctx, "WRITE:graph/entity/*"); err != nil {
		return nil, err
	}
	if s.remoteURL == "" {
		return nil, errRemoteNotConfigured()
	}
	if err := s.withGitLock(func() error {
		empty, err := s.gitstore.IsEmpty(ctx)
		if err != nil {
			return err
		}
		if empty {
			return s.gitstore.CloneSingleBranch(ctx, s.remoteURL, "main")
		}
		return s.gitstore.PullAndFastForward(ctx)
	}); err != nil {
		return nil, mapGitError(err)
	}
	s.writeLock.Lock()
	defer s.writeLock.Unlock()
	if err := s.store.WipeAll(ctx); err != nil {
		return nil, errPullFromRemoteRehydrationFailed(err.Error())
	}
	if s.ladybugPath != "" {
		if err := s.store.RehydrateMainFromFiles(ctx,
			filepath.Join(s.ladybugPath, "graph-repo/entities"),
			filepath.Join(s.ladybugPath, "graph-repo/edges")); err != nil {
			return nil, errPullFromRemoteRehydrationFailed(err.Error())
		}
	}
	return &flowv1.PullFromRemoteResponse{}, nil
}

// ExportGraph streams the serialised graph.
func (s *CartographerServer) ExportGraph(
	req *flowv1.ExportGraphRequest,
	stream grpc.ServerStreamingServer[flowv1.ExportGraphResponse],
) error {
	ctx := stream.Context()
	if err := s.verifier.CheckCapability(ctx, "READ:graph/entity/*"); err != nil {
		return err
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

func toGitEntities(entries []*gitstore.EntityEntry) []gitstore.Entity {
	r := make([]gitstore.Entity, len(entries))
	for i, e := range entries {
		r[i] = gitstore.Entity{
			ID: e.ID, Type: e.Type, Properties: e.Properties,
			Embedding: e.Embedding, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
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
