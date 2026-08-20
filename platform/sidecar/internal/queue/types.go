// Package queue implements the sidecar's queue-shard role: the in-memory
// mirror store, the QueuePeerService server, the registry client (heartbeat),
// and the random per-boot shard identity. The sidecar is a passive shard: it
// stores writes applied by the queue-service, serves reads asked by the
// queue-service, and heartbeats for liveness. It never discovers or routes to
// peers.
package queue

import (
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

// QueueItem is the sidecar mirror's local view of the wire QueueItem. It
// reflects the wire shape (proto flow.v1.QueueItem) — including choices, the
// item metadata the queue-service serves generically (R-5.2).
//
//	ponytail: QueueItem/QueueStatus/QueueFilter are re-declared here rather
//	than imported because the sidecar is a separate module that cannot import
//	sdk/go/internal/queue (which is being deleted). The duplication is
//	bounded — only the handful of shared types the mirror needs — and
//	intentional: they will not be shared again once the SDK mesh is gone.
type QueueItem struct {
	WorkitemID string      `json:"workitem_id"`
	ShardID    string      `json:"shard_id"`
	QueueName  string      `json:"queue_name"`
	Status     QueueStatus `json:"status"`
	EnqueuedAt time.Time   `json:"enqueued_at"`
	ClaimedAt  *time.Time  `json:"claimed_at,omitempty"`
	Generation string      `json:"generation,omitempty"`
	Choices    []string    `json:"choices,omitempty"`
}

// QueueFilter specifies filtering and pagination for queue list queries.
type QueueFilter struct {
	Status *QueueStatus
	Limit  int
	Offset int
}

// Sentinel errors for queue operations. These map to stable gRPC codes.
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
)
