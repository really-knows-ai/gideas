// Package queue implements the HITL federated queue mesh: the SQLite store,
// peer discovery, scatter-gather reads, the QueuePeerService gRPC server, and
// the REST API server used by human-in-the-loop nodes.
package queue

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// QueueStatus represents the state of a queue item.
type QueueStatus string

// newGenerationID returns a time-ordered parking-event ID.
// Fixed-width hex UnixNano prefix (16 hex digits, zero-padded) + 32-hex
// crypto/rand suffix (16 bytes). Fixed width => lexicographic order ==
// creation order, so the R-C3 "max generation wins" dedupe is a
// deterministic creation-order proxy, not a coin flip. Same crypto/rand
// machinery as platform/pkg/randid, minus the platform dependency.
func newGenerationID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand never fails on supported platforms; fall back to a
		// zero suffix so the caller still gets a valid, time-ordered ID.
		return fmt.Sprintf("%016x-00000000000000000000000000000000", time.Now().UnixNano())
	}
	return fmt.Sprintf("%016x-%s", time.Now().UnixNano(), hex.EncodeToString(b))
}

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
	QueueName  string      `json:"queue_name"`
	Status     QueueStatus `json:"status"`
	EnqueuedAt time.Time   `json:"enqueued_at"`
	ClaimedAt  *time.Time  `json:"claimed_at,omitempty"`
	// Generation is the time-ordered parking-event ID (R-C2/R-C3). Distinct
	// parking events of the same workitem carry distinct, strictly-increasing
	// generations; the R-C3 dedupe "max generation wins" is deterministic.
	Generation string `json:"generation,omitempty"`
	// IsBackup is true when this copy is a backup held on a non-owning shard.
	// Owner copy = false (R-C3/R-C6). Computed by shard-aware call sites as
	// item.ShardID != self — there is no schema column.
	IsBackup bool `json:"is_backup,omitempty"`
	// BackupShard is the store-local recorded backup identity (R-C4/R-C5).
	// Never crosses the wire (json:"-").
	BackupShard string `json:"-"`
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

	// GetItem looks up a single item by Workitem ID (local first, then peers).
	GetItem(ctx context.Context, workitemID string) (*QueueItem, error)

	// Claim transitions an item from "waiting" to "claimed".
	// The claim is routed to the owning shard if the item is remote.
	Claim(ctx context.Context, workitemID string) (*QueueItem, error)

	// Release transitions a "claimed" item back to "waiting".
	// The release is routed to the owning shard if the item is remote.
	Release(ctx context.Context, workitemID string) (*QueueItem, error)

	// Decide deletes a "claimed" item from the queue and records the human's
	// routing choice. The choice is delivered to WaitForDecision callers.
	// Pass an empty string when no choice is needed (e.g. hitl-appraise).
	Decide(ctx context.Context, workitemID, choice string) error

	// WaitForDecision blocks until Decide is called for the given Workitem.
	// Returns the choice string passed to Decide (may be empty), or the
	// context error if the context is cancelled before a decision is made.
	// Returns ErrQueueItemNotFound if the Workitem was never enqueued via
	// this QueueManager instance.
	// Returns ("", nil) when unblocked by Stop().
	WaitForDecision(ctx context.Context, workitemID string) (string, error)
}

// PeerResolver discovers peer addresses for the Federated Queue Mesh.
// The production implementation queries headless service DNS; tests
// inject a mock resolver.
type PeerResolver interface {
	Resolve(ctx context.Context) ([]string, error)
}

// Sentinel errors for queue operations. These map to stable error codes
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
