package service

import (
	"context"
	"fmt"
	"math"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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
		params = req.Params.AsMap()
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
		// The wire field is `distance`: it carries the raw LadybugDB cosine
		// distance — lower is more similar, and the store sorts ascending by
		// distance (store.SearchNeighbors). The field is documented as a
		// distance on the wire (proto/flow/v1/cartographer.proto) and the SDK
		// surfaces it as SearchResult.Distance (sdk/go/graph.go), so a consumer
		// ordering by Distance descending would invert the similarity ordering.
		proto = append(proto, &flowv1.SearchNeighborResult{
			EntityId: r.Entity.Id, EntityType: r.Entity.Type,
			Properties: r.Entity.Properties, Distance: r.Distance,
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
			EntityId: e.Id, EntityType: e.Type, Properties: e.Properties, Embedding: e.Embedding,
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
			EntityId: e.Id, EntityType: e.Type, Properties: e.Properties, Embedding: e.Embedding,
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
	// SPEC order (SPEC:1004): active transaction → structural validation →
	// data-integrity. The structural checks that precede the capability gate
	// are the unknown-type check above, this ID-format check, and the
	// property-validation check below: an explicitly-supplied ID that is not a
	// valid UUID v4, an unknown property, or a missing-required property is
	// structurally invalid and must yield INVALID_ARGUMENT even when the caller
	// lacks write capability (mirrors CreateEdge's validateEdgePropsForCreate,
	// SPEC:1005). An empty ID is valid — the store auto-generates it.
	if req.Id != "" && !isValidUUID(req.Id) {
		return nil, status.Error(codes.InvalidArgument, "invalid entity ID format")
	}
	// Structural entity-property validation runs before the capability gate
	// (SPEC RPC check-order SPEC:1004: structural validation → data-integrity;
	// R7 §1: unknown property / missing required property → INVALID_ARGUMENT),
	// so a request combining an unknown property with a missing WRITE
	// capability surfaces INVALID_ARGUMENT — not PERMISSION_DENIED — matching
	// the CreateEdge path (SPEC:1005). The store re-validates on its own
	// boundary because reapplyTransactionChanges calls it directly, bypassing
	// this service-side check.
	edef, ok := s.store.EntityType(req.EntityType)
	if !ok {
		return nil, errUnknownEntityType(req.EntityType)
	}
	if err := validateEntityPropsForCreate(edef, req.Properties); err != nil {
		return nil, err
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
			// Capture the cascade-deleted edge's endpoints and properties so
			// GetTransactionDiff can populate DiffEntry's declared payload
			// fields instead of dropping the data in hand (SPEC R2
			// Transaction.Diff: a review node must be able to tell which
			// endpoints a deleted edge connected).
			Edge: &gitstore.EdgeEntry{
				ID: e.Id, Type: e.Type,
				FromEntityID: e.FromEntityID, ToEntityID: e.ToEntityID,
				Properties: e.Properties, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
			},
		}); err != nil {
			return nil, err
		}
	}
	if err := s.addTransactionChange(ctx, req.TransactionId, gitstore.ChangeLogEntry{
		Kind: gitstore.ChangeDelEntity, ID: ent.Id, Type: ent.Type,
		// Capture the deleted entity's properties and embedding so
		// GetTransactionDiff can populate DiffEntry's declared payload fields.
		Entity: &gitstore.EntityEntry{
			ID: ent.Id, Type: ent.Type, Properties: ent.Properties,
			Embedding: ent.Embedding, CreatedAt: ent.CreatedAt, UpdatedAt: ent.UpdatedAt,
		},
	}); err != nil {
		return nil, err
	}
	return &flowv1.DeleteEntityResponse{
		EntityId: ent.Id, EntityType: ent.Type, Properties: ent.Properties, Embedding: ent.Embedding,
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

// validateEntityPropsForCreate mirrors the store's structural entity-property
// validation (SPEC R7 §1 / error table: unknown entity property / missing
// required entity property → INVALID_ARGUMENT). It is surfaced at the service
// boundary before the capability gate so a structurally invalid property set
// yields INVALID_ARGUMENT rather than PERMISSION_DENIED (SPEC RPC check-order:
// CreateEntity: structural validation → data-integrity). The checks run in the
// store's order (unknown property before missing-required, mirroring
// CreateEntity's store-side validation) so a request invalid on both axes
// surfaces the same error it would through the store.
func validateEntityPropsForCreate(def *store.EntityTypeDef, properties map[string]string) error {
	declared := make(map[string]bool, len(def.Properties))
	for _, p := range def.Properties {
		declared[p.Name] = true
	}
	for key := range properties {
		if !declared[key] {
			return status.Errorf(codes.InvalidArgument, "unknown property: %q for entity type %q", key, def.Name)
		}
	}
	for _, p := range def.Properties {
		if p.Required {
			if _, ok := properties[p.Name]; !ok {
				return status.Errorf(codes.InvalidArgument, "missing required property: %q for entity type %q", p.Name, def.Name)
			}
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
		// Capture the deleted edge's endpoints and properties so
		// GetTransactionDiff can populate DiffEntry's declared payload fields
		// instead of dropping the data in hand.
		Edge: &gitstore.EdgeEntry{
			ID: edge.Id, Type: edge.Type,
			FromEntityID: edge.FromEntityID, ToEntityID: edge.ToEntityID,
			Properties: edge.Properties, CreatedAt: edge.CreatedAt, UpdatedAt: edge.UpdatedAt,
		},
	}); err != nil {
		return nil, mapGitError(err)
	}
	// SPEC R2: "DeleteEdge(id, transactionId?) … Returns the deleted edge".
	// The store's DeleteEdge returns the full edge record (endpoints and
	// properties, via findEdgeByID), so populate every declared field — the
	// SDK's tx.DeleteEdge builds the returned Edge from these fields, and
	// omitting them would silently drop the edge's endpoints and properties.
	return &flowv1.DeleteEdgeResponse{
		EdgeId: edge.Id, EdgeType: edge.Type,
		FromEntityId: edge.FromEntityID, ToEntityId: edge.ToEntityID, Properties: edge.Properties,
	}, nil
}

// branchResourceError marks git branch-creation failures in BeginTransaction
// (CreateBranch, HardResetToBranch) that SPEC error-table row "BeginTransaction
// resource exhausted" classifies as RESOURCE_EXHAUSTED: "Out of file handles,
// memory, or disk space; branch or LadybugDB creation failed". BranchHEAD is
// deliberately not wrapped — it is a read of main, so a failure there (e.g. a
// corrupt repo) stays INTERNAL via mapGitError.
type branchResourceError struct{ err error }

func (e *branchResourceError) Error() string { return e.err.Error() }
func (e *branchResourceError) Unwrap() error { return e.err }

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
	// The open-transactions guard runs before the git lock (SPEC R2 WipeGraph
	// check order: "open-transactions check → git wipe + commit"). HasActive
	// takes each registered transaction's lifecycle lock
	// (transaction_manager.go), while every other git-acquiring path
	// (Commit/Rollback/Refresh/GC) takes the lifecycle lock before the git
	// lock; running the guard inside the git lock would invert that order and
	// deadlock a concurrent WipeGraph against in-flight transaction work.
	// Hoisting is also race-safe: txAdmission.Lock (held for the whole wipe)
	// blocks new BeginTransaction calls, and a transaction can never transition
	// from inactive to active, so a guard that passes here stays passed for the
	// git-side duration.
	if s.txManager.HasActive() {
		return nil, errWipeGraphOpenTransactions()
	}
	var wipeErr error
	lockErr := s.withGitLock(func() error {
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
		// The wipe commit is a mutation-making commit on main, so it must be
		// flagged for remote push (SPEC R10: "backing up every committed
		// change"). Without the flag the remote backup retains the pre-wipe
		// graph indefinitely, and a manual reprovision from the remote (R10
		// Init clone) would resurrect exactly the data the destructive change
		// deleted. The flag is set while holding the git lock (SetPushNeeded
		// is a non-blocking atomic store), so it covers the git-side success
		// path even when the store-side wipe subsequently fails mid-way.
		if s.syncWorker != nil {
			s.syncWorker.SetPushNeeded()
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
		// (loadEntitiesFromDirOnConn/loadEdgesFromDirOnConn) treats a missing
		// dir as empty.
		return nil
	})
	if wipeErr != nil {
		// The open-transactions guard is its own SPEC error (FAILED_PRECONDITION,
		// error-table row 918) and runs before the git lock above. Every git-side
		// failure here (git rm entities/edges, wipe commit, clean untracked) is a
		// mid-wipe failure — the graph may be partially cleaned — and maps to
		// INTERNAL per error-table row 940, mirroring the store-side mid-wipe path
		// below (errWipeGraphMidWipe).
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
