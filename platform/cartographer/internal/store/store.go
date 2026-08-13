// Package store provides the LadybugDB graph database abstraction layer.
//
// Design boundary: The Store interface covers only the graph database (schema,
// entity/edge CRUD, queries, branch DB lifecycle). Transaction change-log
// management — append, read, and cap enforcement — is NOT part of this layer.
// The gitstore package owns the ChangeLog type; the service-layer
// TransactionManager holds the ChangeLog and routes mutation entries to it.
// The Store has no awareness of the change log; implementers adding
// transaction-scoped features must use TransactionManager.AddChangeEntry and
// gitstore.ChangeLog directly.
//
// The 100K change-log admission cap is a hard-coded constant in this store
// layer — DefaultChangeLogCap below (SPEC.md:888-889: "hard-coded constant in
// the Cartographer store layer") — imported by gitstore's default ChangeLog
// (NewChangeLog) and by cmd/main.go when wiring the service-layer
// TransactionManager. The ErrChangeLogFull sentinel lives in gitstore next to
// the ChangeLog type it guards; the service maps it to RESOURCE_EXHAUSTED with
// a full transaction rollback (rejectFullChangeLog in cartographer_server.go).
package store

import (
	"context"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

// DefaultChangeLogCap is the per-transaction change-log admission cap: 100 000
// distinct entities/edges touched (SPEC.md:888-889 and the "Transaction change
// log exceeds capacity" error row, SPEC.md:968). It is the single source of
// truth for the cap: gitstore.NewChangeLog applies it by default and
// cmd/main.go passes it to the service-layer TransactionManager, so the
// admission and recovery ceilings cannot silently diverge.
const DefaultChangeLogCap = 100000

// Store is the interface the service layer depends on.
// It defines the complete graph database abstraction for the Cartographer.
//
// NOTE: Transaction change-log management is intentionally absent from this
// interface. The change log (append, read, 100K cap enforcement) is owned
// by the gitstore package and the service-layer TransactionManager. The Store
// layer is concerned only with LadybugDB schema and data operations; change
// tracking is a separate concern.
type Store interface {
	// Schema
	ApplySchema(ctx context.Context, schema *flowv1.Schema) error
	TableExists(entityType string) bool
	ListMainEntityTypes() ([]string, error)
	EntityTypeNames() []string
	EdgeTypeNames() []string
	EntityType(name string) (*EntityTypeDef, bool)
	EdgeType(name string) (*EdgeTypeDef, bool)

	// Entity CRUD
	//
	// Property values are map[string]string — the schema property model is
	// type: string only (SPEC). The SPEC error-table rows "Non-string entity
	// property value in CreateEntity/UpdateEntity" and "Non-string edge
	// property value in CreateEdge" are annotated in the SPEC as structurally
	// inexpressible: the proto wire contract is map<string,string>, so a
	// non-string value cannot be represented in a parsed request and no
	// INVALID_ARGUMENT guard can exist anywhere in the stack. A malformed
	// client payload fails at protobuf unmarshal (transport-level), before
	// handler or store code runs.
	CreateEntity(ctx context.Context, entityType, id string,
		properties map[string]string, embedding []float32, branch string,
	) (*Entity, error)
	UpdateEntity(ctx context.Context, id string,
		properties map[string]string, embedding []float32, branch string,
	) (*Entity, error)
	DeleteEntity(ctx context.Context, id, branch string) (*Entity, error)
	GetEntity(ctx context.Context, id, branch string) (*Entity, error)

	// Edge CRUD
	CreateEdge(ctx context.Context, edgeType, fromID, toID string,
		properties map[string]string, branch string,
	) (*Edge, error)
	DeleteEdge(ctx context.Context, id, branch string) (*Edge, error)
	GetEdge(ctx context.Context, id, branch string) (*Edge, error)
	ListEdgesOfType(ctx context.Context, edgeType, branch string) ([]Edge, error)

	// Query
	ExecuteCypher(ctx context.Context, cypher string, params map[string]any, branch string) ([]CypherRow, error)
	// ExtractEntityTypes parses and validates a Cypher statement — empty query,
	// syntax, and read-only enforcement, exactly as ExecuteCypher classifies
	// them — and returns the distinct entity-type labels its node patterns
	// reference. It is the server-authoritative statement-analysis seam for the
	// ExecuteCypher capability check (SPEC R3): the service derives the
	// referenced types from its own parse instead of trusting client metadata.
	// Extraction is best-effort: a parseable, read-only statement whose
	// patterns yield no labels returns an empty slice (never an error) and the
	// caller falls back to the READ:graph/entity/* wildcard check.
	ExtractEntityTypes(ctx context.Context, cypher string) ([]string, error)
	SearchNeighbors(ctx context.Context, embedding []float32,
		entityType string, topK int, branch string,
	) ([]NeighborResult, error)
	FullTextSearch(ctx context.Context, query, entityType string, branch string) ([]Entity, error)
	ListEntities(ctx context.Context, entityType string,
		pageSize int, pageToken string, branch string,
	) ([]Entity, string, error)

	// Rules (edge validation)
	ResolveEntityType(ctx context.Context, entityID, branch string) (string, error)

	// Transaction (branch DB management)
	CreateBranchDB(ctx context.Context, txID string) error
	DropBranchDB(ctx context.Context, txID string) error
	// CloseBranchDB closes a branch database (checkpointing its write-ahead
	// log into the branch's .lbug file) without removing the persisted branch
	// files. Used by the service's RefreshTransaction branch-DB swap to
	// materialise the replacement before renaming it onto the transaction's
	// canonical names, closing the crash window between the rename and the
	// WAL checkpoint.
	CloseBranchDB(ctx context.Context, txID string) error
	SaveBranchTransactionState(ctx context.Context, txID string, state BranchTransactionState) error
	LoadBranchTransactionState(ctx context.Context, txID string) (BranchTransactionState, error)
	InvalidateBranchState(ctx context.Context, txID string) error
	ReplicateSchemaToBranch(ctx context.Context, txID string) error
	// CheckBranchSchemaCompatibility validates the branch DB's schema against the
	// current (main) schema (SPEC R9 Commit flow step 1). Returns
	// ErrDestructiveSchemaChange when the current schema is incompatible with the
	// branch's — a type or property the transaction's data lives under has been
	// removed or changed, or a vector index has been disabled. Additive changes
	// (new types, new properties) and entity-type rule modifications are
	// non-destructive (SPEC R2/R6) and never fail this check.
	CheckBranchSchemaCompatibility(ctx context.Context, txID string) error
	RehydrateFromBranch(ctx context.Context, txID string) error
	RehydrateMainFromFiles(ctx context.Context, entitiesDir, edgesDir string) error
	HydrateBranchFromFiles(ctx context.Context, txID, entitiesDir, edgesDir string) error
	IsVectorIndexBootstrapped(ctx context.Context, entityType, branch string) (bool, error)
	GetEstablishedDimension(ctx context.Context, entityType, branch string) (int, error)

	// Wipe
	WipeAll(ctx context.Context) error
	WipeSchema(ctx context.Context) error

	// Health
	Health(ctx context.Context) (*HealthResult, error)

	// Branch scanning
	DumpAllEntities(ctx context.Context, txID string) ([]Entity, error)
	DumpAllEdges(ctx context.Context, txID string) ([]Edge, error)
	// ListEntityTypes returns the entity type names known to a branch (or main).
	//
	// ponytail: Deliberately omits context.Context — the only branch-scanned
	// method that does. The read is purely in-memory (a mutex-guarded snapshot
	// of the type-definition cache; see lockForRead in ladybug/branch.go), so
	// unlike its siblings there is no cancellable I/O to bound and a caller can
	// never block on a hung engine. Failure mode if this drifts: an
	// implementation that starts querying LadybugDB would have no context to
	// cancel, propagate deadlines through, or carry trace values, silently
	// wedging callers on a hung catalog read with no escape hatch or
	// observability correlation. Upgrade path: add ctx context.Context the
	// moment this method performs real I/O, aligning it with the rest of the
	// interface.
	ListEntityTypes(txID string) ([]string, error)

	Close() error
}

// CypherRow is one flat result row: its values in the order LadybugDB returns
// them (SPEC R2 ExecuteCypher — each Row is one flat tuple of the returned
// column values). Column names are not retained: the wire Row carries only
// values, and the SDK exposes them positionally.
type CypherRow struct {
	Values []any
}
