package queue

import (
	"context"
	"log/slog"
	"sync"
	"time"

	flowv1 "github.com/foundry/flow/gen/flow/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// registryDialer connects to the queue-service. Production uses the package
// default (grpc.NewClient with insecure creds); tests inject a bufconn dialer
// via the named seam.
type registryDialer func(ctx context.Context, addr string) (*grpc.ClientConn, error)

// RegistryClient is a thin client for the queue-service's
// QueueRegistryService surface: registration, heartbeat, deregistration.
type RegistryClient struct {
	conn      *grpc.ClientConn
	client    flowv1.QueueRegistryServiceClient
	queueName string
	shardID   string
	// shardAddr is the dialable address of this sidecar's peer server, carried
	// separately from identity (R-2.2) on every registration/heartbeat.
	shardAddr string
	interval  time.Duration
	// onHeartbeat, when set, receives the living shard set (identity+addr)
	// carried by each HeartbeatQueue response so the caller can refresh its
	// "who else is alive and where" view.
	onHeartbeat func([]flowv1.ShardRegistration)
}

// NewRegistryClient builds a RegistryClient. dial is an explicit parameter so
// production passes the real dialer and tests pass a bufconn dialer; when nil
// it defaults to grpc.NewClient with insecure credentials.
func NewRegistryClient(
	addr string, dial registryDialer,
	queueName, shardID, shardAddr string, interval time.Duration,
) (*RegistryClient, error) {
	if dial == nil {
		dial = defaultRegistryDialer
	}
	conn, err := dial(context.Background(), addr)
	if err != nil {
		return nil, err
	}
	return &RegistryClient{
		conn:        conn,
		client:      flowv1.NewQueueRegistryServiceClient(conn),
		queueName:   queueName,
		shardID:     shardID,
		shardAddr:   shardAddr,
		interval:    interval,
		onHeartbeat: nil,
	}, nil
}

// defaultRegistryDialer is the production dialer. It is lazy (grpc.NewClient
// connects on first use) so a slow/unreachable queue-service never blocks
// construction.
func defaultRegistryDialer(_ context.Context, addr string) (*grpc.ClientConn, error) {
	return grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
}

// Register registers the shard with the queue-service.
func (r *RegistryClient) Register(ctx context.Context) error {
	_, err := r.client.RegisterQueue(ctx, &flowv1.RegisterQueueRequest{
		QueueName: r.queueName,
		ShardId:   r.shardID,
		ShardAddr: r.shardAddr,
	})
	return err
}

// Heartbeat refreshes the shard's lease. Failures are non-fatal (never fail the
// sidecar); on success it refreshes the living-shard view via onHeartbeat.
func (r *RegistryClient) Heartbeat(ctx context.Context) error {
	resp, err := r.client.HeartbeatQueue(ctx, &flowv1.HeartbeatQueueRequest{
		QueueName: r.queueName,
		ShardId:   r.shardID,
		ShardAddr: r.shardAddr,
	})
	if err != nil {
		return err
	}
	if r.onHeartbeat != nil {
		shards := make([]flowv1.ShardRegistration, 0, len(resp.GetShards()))
		for _, s := range resp.GetShards() {
			// Dereference each pointer element: callers own value elements.
			shards = append(shards, flowv1.ShardRegistration{
				ShardId:   s.GetShardId(),
				ShardAddr: s.GetShardAddr(),
			})
		}
		r.onHeartbeat(shards)
	}
	return nil
}

// Deregister drops the shard from the queue's registry on clean shutdown.
func (r *RegistryClient) Deregister(ctx context.Context) error {
	_, err := r.client.DeregisterQueue(ctx, &flowv1.DeregisterQueueRequest{
		QueueName: r.queueName,
		ShardId:   r.shardID,
	})
	return err
}

// Close closes the underlying gRPC connection.
func (r *RegistryClient) Close() error {
	if r.conn != nil {
		return r.conn.Close()
	}
	return nil
}

// HeartbeatLoop ticks Heartbeat at r.interval until ctx is cancelled. It
// mirrors the original SDK pattern: re-check ctx.Err() before each send so no
// heartbeat lands after cancellation; non-fatal failures are logged and retried
// on the next tick.
func (r *RegistryClient) HeartbeatLoop(ctx context.Context, wg *sync.WaitGroup) {
	if wg != nil {
		defer wg.Done()
	}
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return
			}
			if err := r.Heartbeat(ctx); err != nil {
				slog.Warn("queue-service heartbeat failed", "queue", r.queueName, "shard", r.shardID, "error", err)
			}
		}
	}
}
