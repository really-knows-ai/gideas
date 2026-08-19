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

// defaultHeartbeatInterval is the SDK-internal mirror of
// flow.DefaultHeartbeatInterval (15s). internal/queue cannot import the flow
// package (import cycle), so the value is duplicated here with the
// cross-reference comment.
const defaultHeartbeatInterval = 15 * time.Second

// registryDialer connects to the queue-service. Production uses the package
// default (grpc.NewClient with insecure creds); same-package tests inject a
// bufconn dialer via the named seam.
type registryDialer func(ctx context.Context, addr string) (*grpc.ClientConn, error)

// queueRegistryClient is a thin client for the queue-service's
// QueueRegistryService surface: registration, heartbeat, deregistration.
type queueRegistryClient struct {
	conn      *grpc.ClientConn
	client    flowv1.QueueRegistryServiceClient
	shardID   string
	queueName string
	shardAddr string
	interval  time.Duration
}

// newQueueRegistryClient builds a queueRegistryClient. dial is an explicit
// parameter so production passes the real dialer and tests pass a bufconn
// dialer.
func newQueueRegistryClient(
	addr string, dial registryDialer,
	shardID, queueName, shardAddr string, interval time.Duration,
) (*queueRegistryClient, error) {
	if dial == nil {
		dial = defaultRegistryDialer
	}
	conn, err := dial(context.Background(), addr)
	if err != nil {
		return nil, err
	}
	return &queueRegistryClient{
		conn:      conn,
		client:    flowv1.NewQueueRegistryServiceClient(conn),
		shardID:   shardID,
		queueName: queueName,
		shardAddr: shardAddr,
		interval:  interval,
	}, nil
}

// defaultRegistryDialer is the production dialer. It is lazy (grpc.NewClient
// connects on first use) so a slow/unreachable queue-service never blocks
// construction.
func defaultRegistryDialer(_ context.Context, addr string) (*grpc.ClientConn, error) {
	return grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
}

// register registers the shard with the queue-service.
func (r *queueRegistryClient) register(ctx context.Context) error {
	_, err := r.client.RegisterQueue(ctx, &flowv1.RegisterQueueRequest{
		QueueName: r.queueName,
		ShardId:   r.shardID,
		ShardAddr: r.shardAddr,
	})
	return err
}

// heartbeat refreshes the shard's lease. Failures are non-fatal; the loop logs
// and retries on the next tick (standalone parity — never fail the node).
func (r *queueRegistryClient) heartbeat(ctx context.Context) error {
	_, err := r.client.HeartbeatQueue(ctx, &flowv1.HeartbeatQueueRequest{
		QueueName: r.queueName,
		ShardId:   r.shardID,
		ShardAddr: r.shardAddr,
	})
	return err
}

// deregister drops the shard from the queue's registry on clean shutdown.
func (r *queueRegistryClient) deregister(ctx context.Context) error {
	_, err := r.client.DeregisterQueue(ctx, &flowv1.DeregisterQueueRequest{
		QueueName: r.queueName,
		ShardId:   r.shardID,
	})
	return err
}

// close closes the underlying gRPC connection.
func (r *queueRegistryClient) close() error {
	if r.conn != nil {
		return r.conn.Close()
	}
	return nil
}

// heartbeatLoop ticks HeartbeatQueue at r.interval until ctx is cancelled. It
// mirrors agent.go:heartbeatLoop: re-check ctx.Err() before each send so no
// heartbeat lands after Stop.
func (r *queueRegistryClient) heartbeatLoop(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
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
			if err := r.heartbeat(ctx); err != nil {
				slog.Warn("flow hitl: queue-service heartbeat failed", "error", err)
			}
		}
	}
}
