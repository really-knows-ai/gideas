package service

import (
	"errors"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// errWildcardMissing is an internal sentinel used for handler branching only.
// Handlers MUST NOT return it through the gRPC boundary.
var errWildcardMissing = errors.New("wildcard capability missing — internal sentinel for handler branching; not a gRPC status error")

// mapStoreError maps a store-layer error to a gRPC status error.
func mapStoreError(err error) error {
	if err == nil {
		return nil
	}

	switch {
	case errors.Is(err, store.ErrUnknownEntityType):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, store.ErrUnknownProperty):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, store.ErrMissingRequiredProperty):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, store.ErrNonStringProperty):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, store.ErrEntityNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, store.ErrEntityAlreadyExists):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, store.ErrInvalidIDFormat):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, store.ErrVectorBootstrap):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, store.ErrUnknownEdgeType):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, store.ErrSourceOrTargetNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, store.ErrEdgeNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, store.ErrEdgeRuleViolation):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, store.ErrInvalidPageSize):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, store.ErrInvalidPageToken):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, store.ErrEmptyQuery):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, store.ErrInvalidCypher):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, store.ErrMutationCypher):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, store.ErrNonIndexedType):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, store.ErrInvalidTopK):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, store.ErrNaNOrInfEmbedding):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, store.ErrEmbeddingDimension):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, store.ErrEmbeddingRequired):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, store.ErrTableStructureMismatch):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, store.ErrDatabaseNotReady):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, store.ErrBranchAlreadyExists):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, store.ErrInvalidEntityDir):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, store.ErrInvalidEdgeDir):
		return status.Error(codes.InvalidArgument, err.Error())

	// Schema errors (from schema package, surfaced through store)
	case isSchemaError(err):
		return status.Error(codes.InvalidArgument, err.Error())

	default:
		return status.Error(codes.Internal, err.Error())
	}
}

// mapTxError maps a transaction-manager error to a gRPC status error.
func mapTxError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gitstore.ErrChangeLogFull) {
		return status.Error(codes.ResourceExhausted, "transaction change log full (100K cap)")
	}
	return status.Error(codes.Internal, err.Error())
}

// mapGitError maps a gitstore-layer error to a gRPC status error.
func mapGitError(err error) error {
	if err == nil {
		return nil
	}

	switch {
	case errors.Is(err, gitstore.ErrNoRemote):
		return status.Error(codes.FailedPrecondition, "no remote configured")
	case errors.Is(err, gitstore.ErrAuthFailed):
		return status.Error(codes.Unauthenticated, "remote credentials rejected")
	case errors.Is(err, gitstore.ErrAuthConfigMissing):
		return status.Error(codes.FailedPrecondition, "remote auth configuration missing")
	case errors.Is(err, gitstore.ErrUnsupportedURLScheme):
		return status.Error(codes.InvalidArgument, "unsupported remote URL scheme")
	case errors.Is(err, gitstore.ErrRemoteUnreachable):
		return status.Error(codes.Unavailable, "remote unreachable")
	case errors.Is(err, gitstore.ErrPushRejected):
		return status.Error(codes.FailedPrecondition, "push rejected (non-fast-forward)")
	case errors.Is(err, gitstore.ErrPullDiverged):
		return status.Error(codes.FailedPrecondition, "remote pull would diverge")
	case errors.Is(err, gitstore.ErrMergeDiverged):
		return status.Error(codes.Aborted, "merge would diverge")
	case errors.Is(err, gitstore.ErrChangeLogFull):
		return status.Error(codes.ResourceExhausted, "change log full (100K cap)")
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

func isSchemaError(err error) bool {
	if err == nil {
		return false
	}
	// Schema errors wrap well-known sentinels from the schema package.
	// We match on message prefixes since we can't import the schema package
	// without creating a cycle (store -> schema <- service).
	msg := err.Error()
	schemaPrefixes := []string{
		"duplicate type name",
		"duplicate property name",
		"invalid name format",
		"name is a reserved word",
		"property name collides with",
		"property type must be 'string'",
		"rule entry has empty",
		"rule references undeclared type",
	}
	for _, p := range schemaPrefixes {
		if len(msg) >= len(p) && msg[:len(p)] == p {
			return true
		}
	}
	return false
}

// Convenience constructors matching the SPEC error table.

func errUnknownEntityType(typeName string) error {
	return status.Errorf(codes.InvalidArgument, "unknown entity type: %q", typeName)
}

func errInvalidPageSize(given int) error {
	return status.Errorf(codes.InvalidArgument, "invalid page size: %d", given)
}

func errInvalidTopK(given int) error {
	return status.Errorf(codes.InvalidArgument, "invalid topK value: %d", given)
}

func errNonIndexedEntityType(typeName string) error {
	return status.Errorf(codes.InvalidArgument, "entity type %q does not have vector index enabled", typeName)
}

func errEntityNotFound(id string) error {
	return status.Errorf(codes.NotFound, "entity not found: %q", id)
}

func errEdgeNotFound(id string) error {
	return status.Errorf(codes.NotFound, "edge not found: %q", id)
}

func errCapabilityDenied(required string) error {
	return status.Errorf(codes.PermissionDenied, "capability denied: %s", required)
}

func errInvalidCapabilitySignature() error {
	return status.Error(codes.PermissionDenied, "invalid capability signature")
}

func errStaleCapability() error {
	return status.Error(codes.PermissionDenied, "stale capability (anti-replay)")
}

func errMutationNotAllowed() error {
	return status.Error(codes.PermissionDenied, "mutation or DDL Cypher statements are not allowed")
}

func errInvalidCypherSyntax(detail string) error {
	return status.Errorf(codes.InvalidArgument, "invalid Cypher syntax: %s", detail)
}

func errTransactionTimedOut(txID string) error {
	return status.Errorf(codes.DeadlineExceeded, "transaction %q has timed out", txID)
}

func errTransactionNotFound(txID string) error {
	return status.Errorf(codes.NotFound, "transaction %q not found", txID)
}

func errInvalidTransactionIDFormat(txID string) error {
	return status.Errorf(codes.InvalidArgument, "invalid transaction ID format: %q", txID)
}

func errInvalidExtendTimeoutDuration(detail string) error {
	return status.Errorf(codes.InvalidArgument, "invalid extend timeout duration: %s", detail)
}

func errWipeGraphOpenTransactions() error {
	return status.Error(codes.FailedPrecondition, "cannot wipe graph: open transactions exist")
}

func errRemoteNotConfigured() error {
	return status.Error(codes.FailedPrecondition, "no remote configured")
}

func errRemoteAuthConfigMissing() error {
	return status.Error(codes.FailedPrecondition, "remote auth configuration missing")
}

func errRemoteCredentialsRejected() error {
	return status.Error(codes.Unauthenticated, "remote credentials rejected")
}

func errUnsupportedRemoteURLScheme(scheme string) error {
	return status.Errorf(codes.InvalidArgument, "unsupported remote URL scheme: %q", scheme)
}

func errRemotePullDiverged() error {
	return status.Error(codes.FailedPrecondition, "remote pull would diverge")
}

func errPullFromRemoteRehydrationFailed(detail string) error {
	return status.Errorf(codes.Internal, "pull from remote re-hydration failed: %s", detail)
}

func errUnsupportedExportFormat(fmt string) error {
	return status.Errorf(codes.InvalidArgument, "unsupported export format: %q", fmt)
}

func errExportGraphBufferAllocation(detail string) error {
	return status.Errorf(codes.ResourceExhausted, "export graph buffer allocation failed: %s", detail)
}

func errExportGraphMidStream(detail string) error {
	return status.Errorf(codes.Internal, "export graph stream failure: %s", detail)
}

func errApplySchemaBeforeDBReady() error {
	return status.Error(codes.FailedPrecondition, "database not ready: ApplySchema called before DB recovery completed")
}

func errRefreshConflict(txID string) error {
	return status.Errorf(codes.Aborted, "transaction %q refresh conflict: same entity/edge modified on main", txID)
}

func errCommitNotUpToDate() error {
	return status.Error(codes.FailedPrecondition, "transaction commit failed: main has advanced since last sync")
}

func errSchemaChangedIncompatibly(detail string) error {
	return status.Errorf(codes.FailedPrecondition, "schema changed incompatibly since transaction began: %s", detail)
}

func errTransactionChangeLogExceeded() error {
	return status.Error(codes.ResourceExhausted, "transaction change log cap exceeded (100K)")
}

func errBeginTransactionResourceExhausted(detail string) error {
	return status.Errorf(codes.ResourceExhausted, "cannot begin transaction: %s", detail)
}

func errEmptyExecuteCypherQuery() error {
	return status.Error(codes.InvalidArgument, "empty Cypher query")
}

func errEmptyFullTextSearchQuery() error {
	return status.Error(codes.InvalidArgument, "empty full-text search query")
}

func errInvalidPageToken(token string) error {
	return status.Errorf(codes.InvalidArgument, "invalid page token: %q", token)
}

func errWipeGraphMidWipe(detail string) error {
	return status.Errorf(codes.Internal, "wipe graph failed partway through: %s", detail)
}

func errNoTransportCredentials() error {
	return status.Error(codes.Unavailable, "no transport credentials configured")
}

func errCapabilitySignedByUnrecognized(signer string) error {
	return status.Errorf(codes.PermissionDenied, "unrecognized capability signer: %q", signer)
}
