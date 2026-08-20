// Package queuemgr is a thin gRPC client to the queue-service's SDK-facing
// surface (flowv1.QueueGatewayService). It translates the wire protocol into
// ergonomic queue operations and maps gRPC status errors to package sentinels.
package queuemgr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
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

	mu    sync.Mutex
	decCh map[string]chan string // workitemID -> buffered decision channel
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

// NewManager builds a Manager, applying opts then dialing the queue service.
func NewManager(opts ...Option) (*Manager, error) {
	m := &Manager{decCh: map[string]chan string{}}
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

// registerDecision creates a client-local buffered decision entry for id.
func (m *Manager) registerDecision(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.decCh[id] = make(chan string, 1)
}

// deliverDecision stores the choice for id (buffered, non-blocking).
func (m *Manager) deliverDecision(id, choice string) {
	m.mu.Lock()
	ch, ok := m.decCh[id]
	m.mu.Unlock()
	if !ok {
		return
	}
	select {
	case ch <- choice:
	default: // buffer full — drop; caller should read via WaitForDecision
	}
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

// Enqueue registers workitemID into the queue with the configured choices, and
// installs a client-local decision entry so WaitForDecision can observe the
// eventual decision.
func (m *Manager) Enqueue(ctx context.Context, workitemID string) error {
	_, err := m.svc.Enqueue(ctx, &flowv1.EnqueueRequest{
		WorkitemId: workitemID,
		QueueName:  m.queueName,
		Choices:    m.choices,
	})
	if err != nil {
		return statusErr(err)
	}
	m.registerDecision(workitemID)
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

// Decide records the human's routing choice and delivers it to any
// client-local WaitForDecision entry for id.
func (m *Manager) Decide(ctx context.Context, workitemID, choice string) error {
	_, err := m.svc.Decide(ctx, &flowv1.DecideRequest{
		QueueName:  m.queueName,
		WorkitemId: workitemID,
		Choice:     choice,
	})
	if err != nil {
		return statusErr(err)
	}
	m.deliverDecision(workitemID, choice)
	return nil
}

// WaitForDecision returns the decision choice for id, awaiting it if not yet
// decided. It returns ErrQueueItemNotFound when the id was never enqueued on
// this Manager, or ctx.Err() when the context completes first.
//
// ponytail: decisions are delivered via a client-local in-process buffered
// channel keyed by workitemID (registered on Enqueue, filled on Decide). This
// only works when Enqueue/Decide/WaitForDecision all run on the same Manager
// instance. PHASE_06 replaces this with EventBus pub/sub so decisions can be
// observed across processes and shards.
//
// ponytail: the decCh map grows by one buffered channel per workitemID and is
// never reclaimed, so a long-running HITL node leaks one entry per enqueued
// workitem over time — slow but real until PHASE_06 drops WaitForDecision.
func (m *Manager) WaitForDecision(ctx context.Context, workitemID string) (string, error) {
	m.mu.Lock()
	ch, ok := m.decCh[workitemID]
	m.mu.Unlock()
	if !ok {
		return "", ErrQueueItemNotFound
	}
	select {
	case choice := <-ch:
		return choice, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
