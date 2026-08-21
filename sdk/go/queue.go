package flow

import (
	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"github.com/foundry/flow/sdk/go/internal/queuemgr"
)

// The HITL QueueManager is now a thin client to the runtime queue-service;
// these aliases keep the public node-facing names stable on the flow package.
type (
	// QueueManager is the HITL queue interface (parking lot, not audit trail).
	QueueManager = queuemgr.QueueManager

	// QueueManagerOption configures NewQueueManager.
	QueueManagerOption = queuemgr.Option

	// queueManagerImpl is the concrete QueueManager returned by NewQueueManager.
	queueManagerImpl = queuemgr.Manager
)

// NewQueueManager creates a new QueueManager thin client over the queue-service.
func NewQueueManager(opts ...QueueManagerOption) (*queueManagerImpl, error) {
	return queuemgr.NewManager(opts...)
}

// WithQueueName sets the queue name for scoping queue items.
// Defaults to FLOW_NODE_ID environment variable, then "default".
func WithQueueName(name string) QueueManagerOption {
	return queuemgr.WithQueueName(name)
}

// WithChoices sets the routing choices sent with each Enqueue. The choices
// ride the Enqueue RPC payload and are persisted as item metadata (R-5.2).
func WithChoices(choices []string) QueueManagerOption {
	return queuemgr.WithChoices(choices)
}

// WithDialer overrides how the QueueManager connects to the queue-service.
// Production uses grpc.NewClient against FLOW_QUEUE_SERVICE_ADDR; tests inject
// a bufconn dialer. Test-only seam retained on the public API so node test
// harnesses can run the thin client over bufconn without real network I/O.
func WithDialer(d queuemgr.DialFunc) QueueManagerOption { return queuemgr.WithDialer(d) }

// WithEventBus injects the EventBus client used by WaitForDecision, so a node
// can subscribe to queue.decided events. It is the EventBus counterpart of
// WithDialer. If unset, WaitForDecision of a present item fails fast.
func WithEventBus(eb flowv1.FlowEventBusServiceClient) QueueManagerOption {
	return queuemgr.WithEventBus(eb)
}
