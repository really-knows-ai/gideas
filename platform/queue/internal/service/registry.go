// Package service implements the queue-service: a stateless service that
// maintains Queue custom-resource instances (the durable, etcd-backed registry
// of queues and their shards), serves the QueueRegistryService gRPC surface,
// the REST browse frontend, and the single-item gRPC surface.
package service

import (
	"context"
	"fmt"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	v1 "github.com/foundry/flow/operator/api/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Shard phase values — the PHASE_02 CRD enum is `active;evicted`. Registration
// and heartbeat write "active"; the eviction sweep writes "evicted". A real API
// server rejects any value outside this enum.
const (
	phaseActive  = "active"
	phaseEvicted = "evicted"
)

// metav1Now wraps a time.Time as a metav1.Time for CR status fields.
func metav1Now(t time.Time) metav1.Time {
	return metav1.NewTime(t)
}

// Registry is both the QueueRegistryService gRPC server and the owner of the
// lease-eviction sweep. It maintains Queue CR instances via client-go.
//
// Namespace is set post-construction (e.g. from FLOW_NAMESPACE in cmd/main.go,
// default "default"); every Queue CR operation is scoped to it. The NewRegistry
// signature stays exactly NewRegistry(c, leaseTTL, sweepInterval) — tests set
// r.Namespace explicitly, mirroring production.
type Registry struct {
	flowv1.UnimplementedQueueRegistryServiceServer
	client client.Client
	// leaseTTL is the duration after which a shard's lastHeartbeatAt is
	// considered stale and evicted.
	leaseTTL time.Duration
	// sweepInterval is the cadence of the eviction sweep ticker.
	sweepInterval time.Duration
	// peerDialer dials the QueuePeerService on living shards. nil ⇒ the
	// production gRPC dialer; same-package tests inject a bufconn dialer.
	peerDialer peerDialer
	// OnShardEvicted fires (queueName, shardID) when the sweep evicts a shard,
	// BEFORE the NotifyShardDead fan-out. nil ⇒ slog.Warn.
	OnShardEvicted func(queueName, shardID string)
	// Namespace scopes every Queue CR operation. Resolved in cmd/main.go and
	// set after construction; defaults to "default".
	Namespace string
}

// NewRegistry constructs a Registry. TTL and sweep interval are explicit
// constructor parameters so unit tests inject sub-second values. Namespace is a
// field, set post-construction.
func NewRegistry(c client.Client, leaseTTL, sweepInterval time.Duration) *Registry {
	return &Registry{
		client:        c,
		leaseTTL:      leaseTTL,
		sweepInterval: sweepInterval,
	}
}

// key returns the namespaced ObjectKey for a queue CR.
func (r *Registry) key(queueName string) client.ObjectKey {
	return client.ObjectKey{Namespace: r.Namespace, Name: queueName}
}

// get fetches the Queue CR (namespaced).
func (r *Registry) get(ctx context.Context, queueName string) (*v1.Queue, error) {
	q := &v1.Queue{}
	if err := r.client.Get(ctx, r.key(queueName), q); err != nil {
		return nil, err
	}
	return q, nil
}

// shardIndex returns the index of the shard in the CR's status, or -1.
func shardIndex(q *v1.Queue, shardID string) int {
	for i, s := range q.Status.Shards {
		if s.ShardID == shardID {
			return i
		}
	}
	return -1
}

// upsertShard sets (or updates) the shard entry with the given addr/phase and
// heartbeat timestamp, leaving all other shards untouched.
func upsertShard(q *v1.Queue, shardID, addr, phase string, now time.Time) {
	i := shardIndex(q, shardID)
	entry := v1.QueueShardStatus{
		ShardID:         shardID,
		Addr:            addr,
		LastHeartbeatAt: metav1Now(now),
		Phase:           phase,
	}
	if i < 0 {
		q.Status.Shards = append(q.Status.Shards, entry)
		return
	}
	q.Status.Shards[i] = entry
}

// livingShards returns the queue's living shards encoded as the wire
// ShardRegistration type — used as the HeartbeatQueueResponse received by every
// shard on each beat so each refreshes its local "who else is alive" view.
func livingShards(q *v1.Queue) []*flowv1.ShardRegistration {
	var out []*flowv1.ShardRegistration
	for _, s := range q.Status.Shards {
		if s.Phase == phaseEvicted {
			continue
		}
		out = append(out, &flowv1.ShardRegistration{
			ShardId:         s.ShardID,
			ShardAddr:       s.Addr,
			LastHeartbeatAt: s.LastHeartbeatAt.Time.UTC().Format(time.RFC3339),
			Phase:           s.Phase,
		})
	}
	return out
}

// createQueue builds and creates the Queue CR with the initial shard embedded
// (the API server persists status sent on Create even with the status
// subresource). Shared by the first registration and by HeartbeatQueue's
// self-heal path.
func (r *Registry) createQueue(ctx context.Context, queueName, shardID, shardAddr, phase string) error {
	q := &v1.Queue{}
	q.Name = queueName
	q.Namespace = r.Namespace
	q.Spec.QueueName = queueName
	q.Status.Shards = []v1.QueueShardStatus{{
		ShardID:         shardID,
		Addr:            shardAddr,
		LastHeartbeatAt: metav1Now(time.Now().UTC()),
		Phase:           phase,
	}}
	return r.client.Create(ctx, q)
}

// RegisterQueue get/creates the Queue CR and idempotently upserts the shard. On
// first registration the CR is created with the initial .status.shards[]
// (persisted by the API server even with the status subresource); subsequent
// registrations re-fetch and Status().Update().
func (r *Registry) RegisterQueue(
	ctx context.Context, req *flowv1.RegisterQueueRequest,
) (*flowv1.RegisterQueueResponse, error) {
	q, err := r.get(ctx, req.GetQueueName())
	if err != nil {
		if !isNotFound(err) {
			return nil, toInternal("register queue: get", err)
		}
		// First registration: create the CR with the initial shard embedded in
		// client.Create (the API server persists status sent on Create).
		if err := r.createQueue(ctx, req.GetQueueName(), req.GetShardId(), req.GetShardAddr(), phaseActive); err != nil {
			return nil, toInternal("register queue: create", err)
		}
		return &flowv1.RegisterQueueResponse{Acknowledged: true}, nil
	}

	// Existing CR: re-fetch to avoid conflicts, upsert the shard via status.
	now := time.Now().UTC()
	upsertShard(q, req.GetShardId(), req.GetShardAddr(), phaseActive, now)
	if err := r.client.Status().Update(ctx, q); err != nil {
		return nil, toInternal("register queue: upsert", err)
	}
	return &flowv1.RegisterQueueResponse{Acknowledged: true}, nil
}

// HeartbeatQueue refreshes the shard's lastHeartbeatAt (idempotent; keeps a
// re-registered shard's entry active) and returns the queue's current living
// shard set.
func (r *Registry) HeartbeatQueue(
	ctx context.Context, req *flowv1.HeartbeatQueueRequest,
) (*flowv1.HeartbeatQueueResponse, error) {
	q, err := r.get(ctx, req.GetQueueName())
	if err != nil {
		if !isNotFound(err) {
			return nil, toInternal("heartbeat: get", err)
		}
		// Self-heal (R-B3 "idempotent upsert"): the SDK registers exactly once
		// at boot, so if the queue-service was unavailable then, later
		// heartbeats must be able to establish the lease once it recovers —
		// create the CR here rather than returning NotFound forever (which
		// would leave the shard permanently outside the living set, invisible
		// to failover).
		if err := r.createQueue(ctx, req.GetQueueName(), req.GetShardId(), req.GetShardAddr(), phaseActive); err != nil {
			return nil, toInternal("heartbeat: create", err)
		}
		q, err = r.get(ctx, req.GetQueueName())
		if err != nil {
			return nil, toInternal("heartbeat: get after create", err)
		}
	}

	now := time.Now().UTC()
	upsertShard(q, req.GetShardId(), req.GetShardAddr(), phaseActive, now)
	if err := r.client.Status().Update(ctx, q); err != nil {
		return nil, toInternal("heartbeat: status update", err)
	}

	// Re-read to return the authoritative post-write living set.
	q2, err := r.get(ctx, req.GetQueueName())
	if err != nil {
		return nil, toInternal("heartbeat: re-get", err)
	}
	return &flowv1.HeartbeatQueueResponse{
		Acknowledged: true,
		Shards:       livingShards(q2),
	}, nil
}

// DeregisterQueue drops the shard from .status.shards[].
func (r *Registry) DeregisterQueue(
	ctx context.Context, req *flowv1.DeregisterQueueRequest,
) (*flowv1.DeregisterQueueResponse, error) {
	q, err := r.get(ctx, req.GetQueueName())
	if err != nil {
		if isNotFound(err) {
			return nil, status.Error(codes.NotFound, "queue not found")
		}
		return nil, toInternal("deregister: get", err)
	}
	i := shardIndex(q, req.GetShardId())
	if i < 0 {
		return nil, status.Error(codes.NotFound, "shard not found")
	}
	q.Status.Shards = append(q.Status.Shards[:i], q.Status.Shards[i+1:]...)
	if err := r.client.Status().Update(ctx, q); err != nil {
		return nil, toInternal("deregister: status update", err)
	}
	return &flowv1.DeregisterQueueResponse{Acknowledged: true}, nil
}

// ListQueues lists the registered queues (and their shards) from the CR
// registry, scoped to r.Namespace.
func (r *Registry) ListQueues(
	ctx context.Context, _ *flowv1.ListQueuesRequest,
) (*flowv1.ListQueuesResponse, error) {
	var list v1.QueueList
	if err := r.client.List(ctx, &list, client.InNamespace(r.Namespace)); err != nil {
		return nil, toInternal("list queues", err)
	}
	resp := &flowv1.ListQueuesResponse{}
	for _, q := range list.Items {
		resp.Queues = append(resp.Queues, &flowv1.QueueRegistration{
			QueueName: q.Name,
			Shards:    livingShards(&q),
		})
	}
	return resp, nil
}

// toInternal wraps an error as a gRPC Internal error with a readable message.
func toInternal(prefix string, err error) error {
	return status.Error(codes.Internal, fmt.Sprintf("%s: %v", prefix, err))
}

// isNotFound reports whether the error is a Kubernetes NotFound error.
func isNotFound(err error) bool {
	return apierrors.IsNotFound(err)
}
