// Package store provides the LadybugDB graph database abstraction layer.
//
// Design boundary: The Store interface covers only the graph database (schema,
// entity/edge CRUD, queries, branch DB lifecycle). Transaction change-log
// management — append, read, and cap enforcement — is NOT part of this layer.
// The gitstore package owns the ChangeLog type and its 100K cap
// (ErrChangeLogFull). The service-layer TransactionManager holds the ChangeLog
// and routes mutation entries to it. The Store has no awareness of the change
// log; implementers adding transaction-scoped features must use
// TransactionManager.AddChangeLogEntry and gitstore.ChangeLog directly.
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
	CreateEntity(ctx context.Context, entityType, id string, properties map[string]string, embedding []float32, branch string) (*Entity, error)
	UpdateEntity(ctx context.Context, id string, properties map[string]string, embedding []float32, branch string) (*Entity, error)
	DeleteEntity(ctx context.Context, id, branch string) (*Entity, error)
	GetEntity(ctx context.Context, id, branch string) (*Entity, error)

	// Edge CRUD
	CreateEdge(ctx context.Context, edgeType, fromID, toID string, properties map[string]string, branch string) (*Edge, error)
	DeleteEdge(ctx context.Context, id, branch string) (*Edge, error)
	GetEdge(ctx context.Context, id, branch string) (*Edge, error)
	ListEdgesOfType(ctx context.Context, edgeType, branch string) ([]Edge, error)

	// Query
	ExecuteCypher(ctx context.Context, cypher string, params map[string]any, branch string) ([]map[string]any, error)
	SearchNeighbors(ctx context.Context, embedding []float32, entityType string, topK int, branch string) ([]NeighborResult, error)
	FullTextSearch(ctx context.Context, query, entityType string, branch string) ([]Entity, error)
	ListEntities(ctx context.Context, entityType string, pageSize int, pageToken string, branch string) ([]Entity, string, error)

	// Rules (edge validation)
	ValidateEdgeRules(sourceType, targetType, edgeType string) error
	ResolveEntityType(ctx context.Context, entityID, branch string) (string, error)

	// Transaction (branch DB management)
	CreateBranchDB(txID string) error
	DropBranchDB(txID string) error
	ReplicateSchemaToBranch(txID string) error
	RehydrateFromBranch(ctx context.Context, txID string) error
	RehydrateMainFromFiles(ctx context.Context, entitiesDir, edgesDir string) error
	HydrateBranchFromFiles(ctx context.Context, txID, entitiesDir, edgesDir string) error
	IsVectorIndexBootstrapped(entityType, db string) bool
	GetEstablishedDimension(entityType, db string) (int, error)

	// Wipe
	WipeAll(ctx context.Context) error
	HasOpenTransactions() bool

	// Health
	Health(ctx context.Context) (*HealthResult, error)

	// Branch scanning
	DumpAllEntities(ctx context.Context, txID string) ([]Entity, error)
	DumpAllEdges(ctx context.Context, txID string) ([]Edge, error)
	ListEntityTypes(txID string) ([]string, error)

	Close() error
}
