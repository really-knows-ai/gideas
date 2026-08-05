package store

import "errors"

// Store-layer sentinel errors. Each maps to a gRPC status code in Phase 4's service layer.
// These are defined here (not in a shared errors package) because they are gRPC-agnostic
// and specific to the store domain.

// Schema errors
var (
	ErrDatabaseNotReady        = errors.New("database not ready")
	ErrDestructiveSchemaChange = errors.New("destructive schema change: wipe required before applying")
)

// Entity CRUD errors
var (
	ErrUnknownEntityType       = errors.New("unknown entity type")
	ErrUnknownProperty         = errors.New("unknown property")
	ErrMissingRequiredProperty = errors.New("missing required property")
	ErrEntityNotFound          = errors.New("entity not found")
	ErrEntityAlreadyExists     = errors.New("entity already exists")
	ErrInvalidIDFormat         = errors.New("invalid ID format: must be a valid UUID v4")
	ErrVectorBootstrap         = errors.New(
		"vector index dimension cannot be bootstrapped: first entity must include an embedding",
	)
)

// Edge CRUD errors
var (
	ErrUnknownEdgeType        = errors.New("unknown edge type")
	ErrSourceOrTargetNotFound = errors.New("source or target entity not found")
	ErrEdgeNotFound           = errors.New("edge not found")
	ErrEdgeRuleViolation      = errors.New("edge rule violation")
)

// Query errors
var (
	ErrInvalidPageSize  = errors.New("invalid page size")
	ErrInvalidPageToken = errors.New("invalid page token")
	ErrEmptyQuery       = errors.New("empty query")
	ErrInvalidCypher    = errors.New("invalid Cypher query")
	ErrMutationCypher   = errors.New("mutation or DDL Cypher statements are not allowed")
	ErrNonIndexedType   = errors.New("entity type does not have vector index enabled")
	ErrInvalidTopK      = errors.New("invalid topK value")
)

// Embedding errors
var (
	ErrNaNOrInfEmbedding          = errors.New("embedding contains NaN or infinity values")
	ErrEmbeddingDimension         = errors.New("embedding dimension mismatch")
	ErrEmptyEmbedding             = errors.New("empty embedding")
	ErrEmbeddingUpdateUnsupported = errors.New(
		"embedding of an existing entity cannot be changed: the vector index does not " +
			"support rewriting the embedding of an existing row",
	)
)

// Branch errors
var (
	ErrBranchAlreadyExists = errors.New("branch already exists")
	ErrBranchNotFound      = errors.New("branch not found")
)

// Rehydration errors
var (
	ErrInvalidEntityDir = errors.New("invalid entities directory")
	ErrInvalidEdgeDir   = errors.New("invalid edges directory")
)
