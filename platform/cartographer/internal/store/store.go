// Package store provides the LadybugDB graph database abstraction layer.
//
// Design boundary: The Store interface covers only the graph database (schema,
// entity/edge CRUD, queries, branch DB lifecycle). Transaction change-log
// management — append, read, and cap enforcement — is NOT part of this layer.
// The gitstore package owns the ChangeLog type and its 100K cap
// (ErrChangeLogFull). The service-layer TransactionManager holds the ChangeLog
// and routes mutation entries to it. The Store has no awareness of the change
// log; implementers adding transaction-scoped features must use
// TransactionManager.AddChangeEntry and gitstore.ChangeLog directly.
//
// ponytail: SPEC divergence (~839). SPEC.md describes the 100K change-log cap
// (ErrChangeLogFull → RESOURCE_EXHAUSTED) as "a hard-coded constant in the
// Cartographer store layer." In the implementation the constant
// (gitstore.DefaultChangeLogCap, gitstore/changelog.go) and the sentinel
// (gitstore.ErrChangeLogFull) live in the gitstore package; the cap is enforced
// by ChangeLog.Add/CheckCapacity plus the service-layer TransactionManager
// (transaction_manager.go), which routes entries and surfaces overflow as
// RESOURCE_EXHAUSTED with a full transaction rollback (rejectFullChangeLog in
// cartographer_server.go). The Store layer has no awareness of the change log —
// this is the intended design boundary, not a defect. The trade-off: the
// constant is owned by gitstore, so the store layer cannot tune it. Upgrade
// path: if SPEC:842's placement is ever enforced, centralize the constant in
// this store layer and have gitstore import it.
package store

import (
	"context"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
)

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
	ValidateSchema(ctx context.Context, schema *flowv1.Schema) error
	EntityTypeNames() []string
	EdgeTypeNames() []string
	EntityType(name string) (*EntityTypeDef, bool)
	EdgeType(name string) (*EdgeTypeDef, bool)

	// Entity CRUD
	//
	// Property values are map[string]string — the schema property model is
	// type: string only (SPEC). The SPEC error-table row "Non-string property
	// value → INVALID_ARGUMENT" (R7 enforcement points 1-2) is therefore not
	// directly testable at the store layer: a non-string value is
	// unrepresentable in this signature. That check lives materially at the
	// proto/service coercion layer. This is a layering consequence of the
	// string-only property model, not a defect.
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
	IsVectorIndexBootstrapped(entityType, branch string) bool
	GetEstablishedDimension(entityType, branch string) (int, error)

	// Wipe
	WipeAll(ctx context.Context) error
	WipeSchema(ctx context.Context) error

	// Health
	Health(ctx context.Context) (*HealthResult, error)

	// Branch scanning
	DumpAllEntities(ctx context.Context, txID string) ([]Entity, error)
	DumpAllEdges(ctx context.Context, txID string) ([]Edge, error)
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
