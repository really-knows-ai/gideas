package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/foundry/flow/cartographer/internal/gitstore"
	"github.com/foundry/flow/cartographer/internal/schemaerrors"
	"github.com/foundry/flow/cartographer/internal/store"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ChangeLogFullError carries the outcome of a change-log capacity rollback.
type ChangeLogFullError struct {
	CapError      error
	RollbackOK    bool
	PersistErr    error
	InvalidateErr error
	CleanupErr    error
}

func (e *ChangeLogFullError) Error() string {
	if e.RollbackOK {
		if e.PersistErr != nil {
			return fmt.Sprintf("%v; persist rollback-only state failed: %v; transaction rolled back", e.CapError, e.PersistErr)
		}
		return e.CapError.Error()
	}
	if e.PersistErr != nil && e.InvalidateErr != nil {
		return fmt.Sprintf(
			"%v; persist rollback-only state failed: %v; fail-closed invalidation failed: %v; transaction rollback failed: %v",
			e.CapError, e.PersistErr, e.InvalidateErr, e.CleanupErr,
		)
	}
	if e.PersistErr != nil {
		return fmt.Sprintf(
			"%v; persist rollback-only state failed: %v; transaction rollback failed: %v",
			e.CapError, e.PersistErr, e.CleanupErr,
		)
	}
	return fmt.Sprintf("%v; transaction rollback failed: %v", e.CapError, e.CleanupErr)
}

func (e *ChangeLogFullError) GRPCStatus() *status.Status {
	st := status.New(codes.ResourceExhausted, e.Error())
	st, _ = st.WithDetails(&errdetails.ErrorInfo{
		Reason: "change_log_full",
		Metadata: map[string]string{
			"rollback_ok":    fmt.Sprintf("%t", e.RollbackOK),
			"persist_err":    errOrEmpty(e.PersistErr),
			"invalidate_err": errOrEmpty(e.InvalidateErr),
			"cleanup_err":    errOrEmpty(e.CleanupErr),
		},
	})
	return st
}

func errOrEmpty(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// errWildcardMissing is an internal sentinel used for handler branching only.
// Handlers MUST NOT return it through the gRPC boundary.
var errWildcardMissing = errors.New(
	"wildcard capability missing — internal sentinel for handler branching; not a gRPC status error",
)

// mapStoreError maps a store-layer error to a gRPC status error.
//
//nolint:gocyclo
func mapStoreError(err error) error {
	if err == nil {
		return nil
	}

	// Pass through already-formatted gRPC status errors without double-wrapping
	// (identical to mapGitError), so store-layer errors that carry their own
	// status code (e.g. RESOURCE_EXHAUSTED during export enumeration) are not
	// flattened into a generic Internal.
	if _, ok := status.FromError(err); ok {
		return err
	}

	switch {
	case errors.Is(err, store.ErrUnknownEntityType):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, store.ErrUnknownProperty):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, store.ErrMissingRequiredProperty):
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
	case errors.Is(err, store.ErrNaNOrInfEmbedding), errors.Is(err, store.ErrEmptyEmbedding):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, store.ErrEmbeddingDimension):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, store.ErrDestructiveSchemaChange):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, store.ErrDatabaseNotReady):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, store.ErrBranchAlreadyExists):
		return status.Error(codes.AlreadyExists, err.Error())

	// Schema errors (from schema package, surfaced through store)
	case isSchemaError(err):
		return status.Error(codes.InvalidArgument, err.Error())

	default:
		return status.Error(codes.Internal, err.Error())
	}
}

// mapGitError maps a gitstore-layer error to a gRPC status error.
func mapGitError(err error) error {
	if err == nil {
		return nil
	}

	// Pass through already-formatted gRPC status errors without double-wrapping.
	if _, ok := status.FromError(err); ok {
		return err
	}

	switch {
	case errors.Is(err, gitstore.ErrNoRemote):
		return errRemoteNotConfigured()
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
		return status.Error(codes.Internal, "commit merge failed (post-re-hydration)")
	// A caller context deadline/cancel surfaced through the sync worker's
	// WakeAndWait (SPEC R10 WithAck: "A caller that hits the context deadline
	// receives DEADLINE_EXCEEDED and the flag stays set", SPEC:621-622) must
	// keep its gRPC code instead of collapsing into INTERNAL. context errors
	// do not implement GRPCStatus, so status.FromError's passthrough does not
	// catch them.
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

func isSchemaError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, schemaerrors.ErrDuplicateTypeName) ||
		errors.Is(err, schemaerrors.ErrDuplicatePropertyName) ||
		errors.Is(err, schemaerrors.ErrInvalidName) ||
		errors.Is(err, schemaerrors.ErrReservedWord) ||
		errors.Is(err, schemaerrors.ErrImplicitColumnCollision) ||
		errors.Is(err, schemaerrors.ErrInvalidPropertyType) ||
		errors.Is(err, schemaerrors.ErrEmptyRuleList) ||
		errors.Is(err, schemaerrors.ErrUndeclaredTypeRef) ||
		errors.Is(err, schemaerrors.ErrNilElement) ||
		errors.Is(err, schemaerrors.ErrNilSchema)
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

func errCapabilityDenied(required string) error {
	return status.Errorf(codes.PermissionDenied, "capability denied: %s", required)
}

func errInvalidCapabilitySignature() error {
	return status.Error(codes.PermissionDenied, "invalid capability signature")
}

func errStaleCapability() error {
	return status.Error(codes.PermissionDenied, "stale capability (anti-replay)")
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

func errInvalidTransactionTimeoutDuration(detail string) error {
	return status.Errorf(codes.InvalidArgument, "invalid transaction timeout duration: %s", detail)
}

func errWipeGraphOpenTransactions() error {
	return status.Error(codes.FailedPrecondition, "cannot wipe graph: open transactions exist")
}

func errRemoteNotConfigured() error {
	return status.Error(codes.FailedPrecondition, "no remote configured")
}

func errUnsupportedExportFormat(format string) error {
	return status.Errorf(codes.InvalidArgument, "unsupported export format: %q", format)
}

func errExportGraphMidStream(detail string) error {
	return status.Errorf(codes.Internal, "export graph stream failure: %s", detail)
}

func errExportGraphBufferAllocation(detail string) error {
	return status.Errorf(codes.ResourceExhausted, "export graph buffer allocation failed: %s", detail)
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

func errBeginTransactionResourceExhausted(detail string) error {
	return status.Errorf(codes.ResourceExhausted, "cannot begin transaction: %s", detail)
}

func errEmptyExecuteCypherQuery() error {
	return status.Error(codes.InvalidArgument, "empty Cypher query")
}

func errEmptyFullTextSearchQuery() error {
	return status.Error(codes.InvalidArgument, "empty full-text search query")
}

func errWipeGraphMidWipe(detail string) error {
	return status.Errorf(codes.Internal, "wipe graph failed partway through: %s", detail)
}

func errCypherParamsNotAStruct() error {
	return status.Error(codes.InvalidArgument, "cypher query parameters must be a JSON object")
}

func errCapabilitySignedByUnrecognized(signer string) error {
	return status.Errorf(codes.PermissionDenied, "unrecognized capability signer: %q", signer)
}
