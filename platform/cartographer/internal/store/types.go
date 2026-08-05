package store

import "time"

// Entity represents a single knowledge-graph entity with its identifier,
// type, properties, optional vector embedding, and creation/update timestamps.
// ponytail: CreatedAt and UpdatedAt are store-domain fields not present in the
// proto Entity message (PHASE_01.md:315-320). The proto Entity carries only
// entity_id, entity_type, properties, and embedding. The service layer must
// populate the proto response without timestamp fields; timestamps are used
// internally by the store and by transaction diff/recovery logic.
type Entity struct {
	Id         string
	Type       string
	Properties map[string]string
	Embedding  []float32
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Edge represents a single directed edge between two entities.
type Edge struct {
	Id           string
	Type         string
	FromEntityID string
	ToEntityID   string
	Properties   map[string]string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// NeighborResult represents a single nearest-neighbor result returned
// by SearchNeighbors.
type NeighborResult struct {
	Entity   Entity
	Distance float64 // ponytail: named Distance (store domain) vs SearchNeighborResult.score
	// (proto wire). The service layer maps Distance to the proto's double score field
	// when constructing SearchNeighborResult responses.
}

// PropertyDef describes a single property definition in a schema type.
type PropertyDef struct {
	Name     string
	Type     string
	Required bool
}

// EntityTypeDef describes a known entity type in the graph schema.
// This is distinct from the proto EntityType message used by ApplySchema;
// it is the store's domain type for schema introspection by consumers
// such as gitstore (Phase 3). Named EntityTypeDef (not EntityType) to avoid
// collision with flowv1.EntityType from the generated proto types.
type EntityTypeDef struct {
	Name              string
	Properties        []PropertyDef
	EnableVectorIndex bool
	Rules             []ConnectionRuleDef
}

type ConnectionRuleDef struct {
	CanConnectTo []string
	Using        []string
}

// EdgeTypeDef describes a known edge type in the graph schema.
// This is the store's domain type for schema introspection by consumers
// such as gitstore (Phase 3). Named EdgeTypeDef (not EdgeType) to avoid
// collision with flowv1.EdgeType from the generated proto types.
type EdgeTypeDef struct {
	Name       string
	Properties []PropertyDef
}

// SchemaProvider is the subset of the store API that schema consumers
// (e.g., gitstore in Phase 3) depend on. The Store interface includes
// these methods directly; SchemaProvider is a narrower consumer-facing
// interface. The concrete ladybugDB type satisfies both interfaces.
type SchemaProvider interface {
	EntityTypeNames() []string
	EdgeTypeNames() []string
	EntityType(name string) (*EntityTypeDef, bool)
	EdgeType(name string) (*EdgeTypeDef, bool)
}

// HealthResult captures the three health axes checked by Health().
// The service layer maps these into the proto HealthCheckResponse fields
// (ladybug_ok, schema_applied, pvc_writable) and/or the aggregate gRPC
// health protocol (SERVING when all three are true, NOT_SERVING otherwise).
type HealthResult struct {
	LadybugOK     bool
	SchemaApplied bool
	PVCWritable   bool
}

// BranchTransactionState is the durable transaction lifecycle record owned by
// the branch store. Missing or unsupported records make recovery fail closed.
type BranchTransactionState struct {
	MainHeadAtLastSync string        `json:"main_head_at_last_sync"`
	AppliedTimeout     time.Duration `json:"applied_timeout_ns"`
	SchemaHash         string        `json:"schema_hash"`
	CommitStarted      bool          `json:"commit_started"`
	CommitCreated      bool          `json:"commit_created"`
	CommitHydrated     bool          `json:"commit_hydrated"`
	MainRehydrated     bool          `json:"main_rehydrated"`
	MergeCompleted     bool          `json:"merge_completed"`
	RollbackOnly       bool          `json:"rollback_only"`
}
