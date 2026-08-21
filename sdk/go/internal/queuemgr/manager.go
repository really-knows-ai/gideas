// Package queuemgr is a thin gRPC client to the queue-service's SDK-facing
// surface (flowv1.QueueGatewayService). It translates the wire protocol into
// ergonomic queue operations and maps gRPC status errors to package sentinels.
package queuemgr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// QueueStatus is the lifecycle state of a queue item.
type QueueStatus string

const (
	// QueueStatusWaiting marks a queued, unclaimed item.
	QueueStatusWaiting QueueStatus = "waiting"
	// QueueStatusClaimed marks an item currently claimed by a worker.
	QueueStatusClaimed QueueStatus = "claimed"
)

// QueueItem is the SDK view of a queue entry.
type QueueItem struct {
	WorkitemID string      `json:"workitem_id"`
	ShardID    string      `json:"shard_id"`
	QueueName  string      `json:"queue_name"`
	Status     QueueStatus `json:"status"`
	EnqueuedAt time.Time   `json:"enqueued_at"`
	ClaimedAt  *time.Time  `json:"claimed_at,omitempty"`
	Generation string      `json:"generation,omitempty"`
}

// QueueFilter narrows a global-queue listing.
type QueueFilter struct {
	Status *QueueStatus
	Limit  int
	Offset int
}

// Sentinel errors returned by a QueueManager.
var (
	ErrQueueItemNotFound       = errors.New("queuemgr: queue item not found")
	ErrQueueItemAlreadyClaimed = errors.New("queuemgr: queue item already claimed")
	ErrQueueItemInvalidState   = errors.New("queuemgr: queue item invalid state")
	ErrShardUnavailable        = errors.New("queuemgr: queue shard unavailable")
)

// QueueManager is the SDK-facing contract of the queue service.
type QueueManager interface {
	Enqueue(ctx context.Context, workitemID string) error
	GetGlobalQueue(ctx context.Context, filter QueueFilter) ([]QueueItem, error)
	GetItem(ctx context.Context, workitemID string) (*QueueItem, error)
	Claim(ctx context.Context, workitemID string) (*QueueItem, error)
	Release(ctx context.Context, workitemID string) (*QueueItem, error)
	Decide(ctx context.Context, workitemID, choice string) error
	WaitForDecision(ctx context.Context, workitemID string) (string, error)
}

var _ QueueManager = (*Manager)(nil)

// DialFunc dials the queue service and returns a ready client connection.
type DialFunc func(ctx context.Context, target string) (grpc.ClientConnInterface, error)

// Option configures a Manager.
type Option func(*Manager)

// Manager is the concrete queue-service thin client.
type Manager struct {
	queueName string
	choices   []string
	dialer    DialFunc
	svc       flowv1.QueueGatewayServiceClient

	// eventBus is the EventBus client WaitForDecision subscribes on.
	// nil ⇒ WaitForDecision of a present item fails fast (see below).
	eventBus flowv1.FlowEventBusServiceClient
}

// WithQueueName sets the queue-name scoping used on every request.
func WithQueueName(name string) Option {
	return func(m *Manager) { m.queueName = name }
}

// WithChoices sets the routing choices sent with Enqueue.
func WithChoices(choices []string) Option {
	return func(m *Manager) { m.choices = append([]string(nil), choices...) }
}

// WithDialer injects the dial function used to reach the queue service. It is
// the testability seam that production code and bufconn-backed tests share. If
// WithDialer is not supplied, NewManager dials the FLOW_QUEUE_SERVICE_ADDR
// address (which must be set).
func WithDialer(d DialFunc) Option {
	return func(m *Manager) { m.dialer = d }
}

// WithEventBus injects the EventBus client used by WaitForDecision.
// It is the EventBus counterpart of WithDialer: production wiring
// (flowv1.NewFlowEventBusServiceClient over the Event Bus connection)
// and bufconn-backed tests share it.
func WithEventBus(eb flowv1.FlowEventBusServiceClient) Option {
	return func(m *Manager) { m.eventBus = eb }
}

// NewManager builds a Manager, applying opts then dialing the queue service.
func NewManager(opts ...Option) (*Manager, error) {
	m := &Manager{}
	for _, o := range opts {
		o(m)
	}

	cc, err := m.dial(context.Background())
	if err != nil {
		return nil, fmt.Errorf("queuemgr: dial queue service: %w", err)
	}
	m.svc = flowv1.NewQueueGatewayServiceClient(cc)
	return m, nil
}

func (m *Manager) dial(ctx context.Context) (grpc.ClientConnInterface, error) {
	if m.dialer != nil {
		return m.dialer(ctx, "")
	}
	addr := os.Getenv("FLOW_QUEUE_SERVICE_ADDR")
	if addr == "" {
		return nil, errors.New("queuemgr: FLOW_QUEUE_SERVICE_ADDR unset and no WithDialer supplied")
	}
	return grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
}

// statusErr maps a grpc status error to the corresponding package sentinel. It
// is the reverse of the service's toItemGRPCError mapping. A non-gRPC error or
// an unmapped code passes through unchanged.
func statusErr(err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	switch st.Code() {
	case codes.NotFound:
		return ErrQueueItemNotFound
	case codes.AlreadyExists:
		return ErrQueueItemAlreadyClaimed
	case codes.FailedPrecondition:
		return ErrQueueItemInvalidState
	case codes.Unavailable:
		return ErrShardUnavailable
	default:
		return err
	}
}

// toItem maps a proto QueueItem to the SDK QueueItem.
func toItem(p *flowv1.QueueItem) *QueueItem {
	if p == nil {
		return nil
	}
	it := &QueueItem{
		WorkitemID: p.GetWorkitemId(),
		ShardID:    p.GetShardId(),
		QueueName:  p.GetQueueName(),
		Status:     QueueStatus(p.GetStatus()),
		Generation: p.GetGenerationId(),
	}
	if et, err := time.Parse(time.RFC3339, p.GetEnqueuedAt()); err == nil {
		it.EnqueuedAt = et
	}
	if ct := p.GetClaimedAt(); ct != "" {
		if t, err := time.Parse(time.RFC3339, ct); err == nil {
			it.ClaimedAt = &t
		}
	}
	return it
}

// Enqueue registers workitemID into the queue with the configured choices. The
// queue-service publishes the eventual decision as a queue.decided EventBus
// event, which WaitForDecision subscribes to.
func (m *Manager) Enqueue(ctx context.Context, workitemID string) error {
	_, err := m.svc.Enqueue(ctx, &flowv1.EnqueueRequest{
		WorkitemId: workitemID,
		QueueName:  m.queueName,
		Choices:    m.choices,
	})
	if err != nil {
		return statusErr(err)
	}
	return nil
}

// GetGlobalQueue lists queue items.
//
// ponytail: QueueFilter.Limit/Offset are accepted client-side but not
// serialized — the GetGlobalQueueRequest message has no pagination fields, so
// the service returns the whole (or status-filtered) queue. If the service ever
// adds server-side pagination, wire Limit/Offset through.
func (m *Manager) GetGlobalQueue(ctx context.Context, filter QueueFilter) ([]QueueItem, error) {
	req := &flowv1.GetGlobalQueueRequest{QueueName: m.queueName}
	if filter.Status != nil {
		req.Status = string(*filter.Status)
	}
	resp, err := m.svc.GetGlobalQueue(ctx, req)
	if err != nil {
		return nil, statusErr(err)
	}
	out := make([]QueueItem, 0, len(resp.GetItems()))
	for _, p := range resp.GetItems() {
		if it := toItem(p); it != nil {
			out = append(out, *it)
		}
	}
	return out, nil
}

// GetItem fetches a single item, or ErrQueueItemNotFound.
func (m *Manager) GetItem(ctx context.Context, workitemID string) (*QueueItem, error) {
	resp, err := m.svc.GetItem(ctx, &flowv1.GetItemRequest{
		QueueName:  m.queueName,
		WorkitemId: workitemID,
	})
	if err != nil {
		return nil, statusErr(err)
	}
	return toItem(resp.GetItem()), nil
}

// Claim claims an item, or the AlreadyClaimed/NotFound sentinels.
func (m *Manager) Claim(ctx context.Context, workitemID string) (*QueueItem, error) {
	resp, err := m.svc.Claim(ctx, &flowv1.ClaimRequest{
		QueueName:  m.queueName,
		WorkitemId: workitemID,
	})
	if err != nil {
		return nil, statusErr(err)
	}
	return toItem(resp.GetItem()), nil
}

// Release releases an item back to waiting, or the InvalidState/NotFound
// sentinels.
func (m *Manager) Release(ctx context.Context, workitemID string) (*QueueItem, error) {
	resp, err := m.svc.Release(ctx, &flowv1.ReleaseRequest{
		QueueName:  m.queueName,
		WorkitemId: workitemID,
	})
	if err != nil {
		return nil, statusErr(err)
	}
	return toItem(resp.GetItem()), nil
}

// Decide records the human's routing choice. The queue-service publishes it as
// a queue.decided EventBus event, which WaitForDecision subscribes to.
func (m *Manager) Decide(ctx context.Context, workitemID, choice string) error {
	_, err := m.svc.Decide(ctx, &flowv1.DecideRequest{
		QueueName:  m.queueName,
		WorkitemId: workitemID,
		Choice:     choice,
	})
	if err != nil {
		return statusErr(err)
	}
	return nil
}

// WaitForDecision returns the decision choice for id, awaiting it if not yet
// decided. It first checks the queue service: if the item is absent
// (never enqueued OR already decided+deleted) it returns ErrQueueItemNotFound
// immediately without subscribing. Otherwise it subscribes to the EventBus
// (channel "queue", event type "queue.decided") and waits for the first event
// whose workitem_id matches, returning that event's "choice" attribute. The
// subscribe→consume loop runs inside ReconnectStream, so EventBus downtime
// (a dropped stream or a failed subscribe) backs off and retries with
// reconnect-backoff instead of failing hard. It returns ctx.Err() when the
// context completes first.
func (m *Manager) WaitForDecision(ctx context.Context, workitemID string) (string, error) {
	// 1. GetItem FIRST: absent item ⇒ ErrQueueItemNotFound, never subscribe.
	if _, err := m.GetItem(ctx, workitemID); err != nil {
		return "", err
	}

	// 2. Fail fast if no EventBus client injected (never block, never spin —
	//    checked before the retry loop so a nil eventBus cannot busy-retry).
	if m.eventBus == nil {
		return "", fmt.Errorf("queuemgr: WaitForDecision: no EventBus client injected (WithEventBus)")
	}

	// 3. Subscribe→consume loop inside ReconnectStream. The consume closure
	//    runs the workitem-filtered Recv-loop; the first event matching
	//    workitemID decides, capturing its "choice" into the outer variable and
	//    returning nil to end. A Recv error bubbles up to ReconnectStream,
	//    which backs off and re-subscribes.
	var choice string
	err := ReconnectStream(
		ctx,
		func() (flowv1.FlowEventBusService_SubscribeClient, error) {
			return m.eventBus.Subscribe(ctx, &flowv1.SubscribeRequest{
				Channel: "queue",
				Filter:  &flowv1.SubscribeFilter{EventType: "queue.decided"},
			})
		},
		func(stream flowv1.FlowEventBusService_SubscribeClient) error {
			for {
				evt, err := stream.Recv()
				if err != nil {
					return err
				}
				if evt.GetWorkitemId() != workitemID {
					continue
				}
				choice = evt.GetAttributes()["choice"]
				return nil
			}
		},
		waitDecisionBackoff,
		sleepCtx,
	)
	if err != nil {
		return "", err
	}
	return choice, nil
}
