package flow

import (
	"net/http"

	"github.com/foundry/flow/sdk/go/internal/queue"
)

// Re-exported HITL queue surface. The queue mesh implementation — SQLite
// store, peer discovery (DNSResolver), QueuePeerService gRPC server,
// HTTP server, and the QueueManager — lives in the internal/queue package;
// these aliases keep the public node-facing names stable on the flow package.
type (
	// QueueManager is the HITL queue interface (parking lot, not audit trail).
	QueueManager = queue.QueueManager

	// QueueManagerOption configures NewQueueManager.
	QueueManagerOption = queue.Option

	// queueManagerImpl is the concrete QueueManager returned by NewQueueManager.
	queueManagerImpl = queue.Manager
)

// NewQueueManager creates a new QueueManager. Call Start() to initialise
// the SQLite store, mesh discovery, and HTTP server.
func NewQueueManager(opts ...QueueManagerOption) (*queueManagerImpl, error) {
	return queue.NewManager(opts...)
}

// WithQueueName sets the queue name for scoping queue items.
// Defaults to FLOW_NODE_ID environment variable, then "default".
func WithQueueName(name string) QueueManagerOption {
	return queue.WithQueueName(name)
}

// WithCustomRoutes registers additional HTTP routes on the QueueManager's
// REST API mux. The provided function is called after the standard HITL
// routes are registered, so it can add node-specific endpoints (e.g. GET
// /choices for hitl) on the same server without forking the SDK.
func WithCustomRoutes(fn func(mux *http.ServeMux)) QueueManagerOption {
	return queue.WithCustomRoutes(fn)
}
