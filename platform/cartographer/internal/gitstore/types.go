// Package gitstore provides a Git-backed file-per-element serialisation layer
// for entity and edge data. It uses go-git for pure-Go git operations and
// supports branching, committing, merging, and remote sync.
package gitstore

import (
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

// EntityJSON is the file-per-element serialisation format for entities.
// Embedding is a pointer to distinguish nil (not set, omitted from JSON)
// from an empty slice (set but empty, serialised as "embedding": []).
type EntityJSON struct {
	ID         uuid.UUID         `json:"id"`
	Type       string            `json:"type"`
	Properties map[string]string `json:"properties,omitempty"`
	Embedding  *[]float32        `json:"embedding,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

// EdgeJSON is the file-per-element serialisation format for edges.
type EdgeJSON struct {
	ID           uuid.UUID         `json:"id"`
	Type         string            `json:"type"`
	FromEntityID uuid.UUID         `json:"from"`
	ToEntityID   uuid.UUID         `json:"to"`
	Properties   map[string]string `json:"properties,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

// Entity is the domain type for batch WriteEntityFiles / WriteEdgeFiles.
// UUIDs are stored as their canonical string form for JSON interop.
type Entity struct {
	ID         string
	Type       string
	Properties map[string]string
	Embedding  []float32
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Edge is the domain type for batch serialisation.
type Edge struct {
	ID           string
	Type         string
	FromEntityID string
	ToEntityID   string
	Properties   map[string]string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// EntityFile represents a file read back from the git store.
type EntityFile struct {
	ID         string
	Type       string
	Properties map[string]string
	Embedding  []float32
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Path       string // relative path in the git repo, e.g. "entities/Component/<uuid>.json"
}

// EdgeFile represents a file read back from the git store.
type EdgeFile struct {
	ID           string
	Type         string
	FromEntityID string
	ToEntityID   string
	Properties   map[string]string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Path         string // relative path in the git repo
}

// EntityEntry stores the full snapshot for an added or modified entity.
type EntityEntry struct {
	ID         string
	Type       string
	Properties map[string]string
	Embedding  []float32
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// EdgeEntry stores the full snapshot for an added edge.
type EdgeEntry struct {
	ID           string
	Type         string
	FromEntityID string
	ToEntityID   string
	Properties   map[string]string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ChangeKind identifies the type of mutation.
type ChangeKind int

const (
	ChangeAddEntity ChangeKind = iota
	ChangeModEntity
	ChangeDelEntity
	ChangeAddEdge
	ChangeModEdge
	ChangeDelEdge
)

// DeletionInfo carries the type and suspected flag for a deleted entity or edge.
type DeletionInfo struct {
	Type      string
	Suspected bool // true when reconstructed during startup recovery
}

// ChangeLogEntry is a generic entry used by Recovery and the
// TransactionManager.AddChangeLogEntry method. The map-based ChangeLog
// stores entries by category for efficient access by GetTransactionDiff.
type ChangeLogEntry struct {
	Kind      ChangeKind
	ID        string
	Type      string
	Suspected bool         // true when reconstructed during startup recovery (deletions only)
	Entity    *EntityEntry // full snapshot for add/modify entity
	Edge      *EdgeEntry   // full snapshot for add edge
}

// ChangeLog tracks mutations within a transaction using per-category maps.
type ChangeLog struct {
	AddedEntities    map[string]*EntityEntry  // entityID -> entry
	ModifiedEntities map[string]*EntityEntry  // entityID -> entry
	DeletedEntities  map[string]*DeletionInfo // entityID -> deletion info (type + suspected flag)
	AddedEdges       map[string]*EdgeEntry    // edgeID -> entry
	ModifiedEdges    map[string]*EdgeEntry    // edgeID -> entry
	DeletedEdges     map[string]*DeletionInfo // edgeID -> deletion info (type + suspected flag)
	mu               sync.Mutex
	count            int // distinct entities/edges touched (the cap counts these)
	cap              int
}

// Sentinel errors used across the gitstore package.
var (
	ErrInvalidUUID          = errors.New("invalid UUID v4")
	ErrBranchNotFound       = errors.New("branch not found")
	ErrBranchAlreadyExists  = errors.New("branch already exists")
	ErrNoRemote             = errors.New("no remote configured")
	ErrAuthFailed           = errors.New("remote credentials rejected")
	ErrAuthConfigMissing    = errors.New("remote auth configuration missing")
	ErrUnsupportedURLScheme = errors.New("unsupported remote URL scheme")
	ErrRemoteUnreachable    = errors.New("remote unreachable")
	ErrPushRejected         = errors.New("push rejected (non-fast-forward)")
	ErrPullDiverged         = errors.New("pull would diverge")
	ErrMergeDiverged        = errors.New("merge would diverge")
	ErrChangeLogFull        = errors.New("change log full (100K cap)")
	ErrChangeLogNilSnapshot = errors.New("change log entry missing required snapshot")
	ErrUnknownChangeKind    = errors.New("unknown change kind")
	ErrEntityTypeMismatch   = errors.New("entity type mismatch")
	ErrEdgeTypeMismatch     = errors.New("edge type mismatch")
	ErrInvalidHash          = errors.New("invalid commit hash")
	ErrRemoteURLNoHost      = errors.New("remote URL has no host")
	ErrEmptyBasePath        = errors.New("gitstore: basePath must not be empty")
	errHasData              = errors.New("has data")
)
