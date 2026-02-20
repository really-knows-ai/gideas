package flow

import (
	"context"
	"errors"
	"time"
)

// QueueStatus represents the state of a queue item.
type QueueStatus string

const (
	// QueueStatusWaiting indicates the item is awaiting human review.
	QueueStatusWaiting QueueStatus = "waiting"
	// QueueStatusClaimed indicates a human has claimed the item for review.
	QueueStatusClaimed QueueStatus = "claimed"
)

// QueueItem represents a Workitem parked in the HITL queue.
// The queue stores parking state only — domain-specific data (artefact
// content, feedback, decisions) lives in the Archivist and Librarian.
type QueueItem struct {
	WorkitemID string      `json:"workitem_id"`
	ShardID    string      `json:"shard_id"`
	Status     QueueStatus `json:"status"`
	EnqueuedAt time.Time   `json:"enqueued_at"`
	ClaimedAt  *time.Time  `json:"claimed_at,omitempty"`
}

// QueueFilter specifies filtering and pagination for queue list queries.
type QueueFilter struct {
	Status *QueueStatus
	Limit  int
	Offset int
}

// QueueManager provides HITL queue operations. It manages the local SQLite
// queue store, the federated peer mesh, and the REST API server.
//
// The queue is a parking lot, not an audit trail. When a human decides,
// the item is deleted from the queue — the decision is expressed through
// normal SDK operations (artefact writes, feedback, routing instructions).
type QueueManager interface {
	// Enqueue parks a Workitem in the local shard's queue with status "waiting".
	Enqueue(ctx context.Context, workitemID string) error

	// GetGlobalQueue scatter-gathers queue items from all mesh peers.
	GetGlobalQueue(ctx context.Context, filter QueueFilter) ([]QueueItem, error)

	// GetLocalQueue returns items from this shard's local store only.
	GetLocalQueue(ctx context.Context, filter QueueFilter) ([]QueueItem, error)

	// GetItem looks up a single item by Workitem ID (local first, then peers).
	GetItem(ctx context.Context, workitemID string) (*QueueItem, error)

	// Claim transitions an item from "waiting" to "claimed".
	// The claim is routed to the owning shard if the item is remote.
	Claim(ctx context.Context, workitemID string) (*QueueItem, error)

	// Release transitions a "claimed" item back to "waiting".
	// The release is routed to the owning shard if the item is remote.
	Release(ctx context.Context, workitemID string) (*QueueItem, error)

	// Complete deletes a "claimed" item from the queue. The decision itself
	// is expressed through normal SDK operations performed by the handler.
	Complete(ctx context.Context, workitemID string) error

	// GetPeers returns the addresses of currently connected mesh peers.
	GetPeers(ctx context.Context) ([]string, error)
}

// PeerResolver discovers peer addresses for the Federated Queue Mesh.
// The production implementation queries headless service DNS; tests
// inject a mock resolver.
type PeerResolver interface {
	Resolve(ctx context.Context) ([]string, error)
}

// Sentinel errors for queue operations. These map to stable error codes
// in the error catalogue (specs/05-reference/error-catalogue.md).
var (
	// ErrQueueItemNotFound is returned when a queue operation references
	// an item that does not exist on the target shard.
	ErrQueueItemNotFound = errors.New("queue item not found")

	// ErrQueueItemAlreadyClaimed is returned when attempting to claim
	// an item that is already in "claimed" state.
	ErrQueueItemAlreadyClaimed = errors.New("queue item already claimed")

	// ErrQueueItemInvalidState is returned when a state transition is
	// attempted from an invalid state (e.g., releasing a "waiting" item).
	ErrQueueItemInvalidState = errors.New("queue item invalid state transition")

	// ErrShardUnavailable is returned when the owning shard for a queue
	// item is unreachable.
	ErrShardUnavailable = errors.New("owning shard unavailable")
)
