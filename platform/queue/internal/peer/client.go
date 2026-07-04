// Package peer manages gRPC client connections to QueueManager shards for a
// single queue. DNS resolution is performed on every call to Peers() — no
// caching, DNS is the source of truth.
package peer

import (
	"context"
	"sync"

	flowv1 "github.com/gideas/flow/gen/flow/v1"
	flow "github.com/gideas/flow/sdk/go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// PeerClient maintains a pool of gRPC connections to QueuePeerService servers
// for a single queue. Addresses are resolved via a PeerResolver on each call
// to Peers().
type PeerClient struct {
	resolver flow.PeerResolver
	pool     sync.Map // shardAddr → *grpc.ClientConn
}

// NewPeerClient creates a PeerClient that uses the given resolver for DNS
// discovery.
func NewPeerClient(resolver flow.PeerResolver) *PeerClient {
	return &PeerClient{resolver: resolver}
}

// Peers resolves current shard addresses and returns them. Each call performs
// a fresh DNS lookup.
func (c *PeerClient) Peers(ctx context.Context) ([]string, error) {
	return c.resolver.Resolve(ctx)
}

// GetClient returns a cached or new gRPC QueuePeerServiceClient for the given
// shard address.
func (c *PeerClient) GetClient(addr string) (flowv1.QueuePeerServiceClient, error) {
	if v, ok := c.pool.Load(addr); ok {
		return flowv1.NewQueuePeerServiceClient(v.(*grpc.ClientConn)), nil
	}

	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, err
	}

	// Store the connection. If a concurrent caller already stored one, close
	// ours and use the existing one.
	existing, loaded := c.pool.LoadOrStore(addr, conn)
	if loaded {
		_ = conn.Close()
		return flowv1.NewQueuePeerServiceClient(existing.(*grpc.ClientConn)), nil
	}

	return flowv1.NewQueuePeerServiceClient(conn), nil
}

// Close drains all cached connections.
func (c *PeerClient) Close() error {
	var errs []error
	c.pool.Range(func(key, value any) bool {
		if err := value.(*grpc.ClientConn).Close(); err != nil {
			errs = append(errs, err)
		}
		c.pool.Delete(key)
		return true
	})
	if len(errs) > 0 {
		return errs[0] // ponytail: only return first error; upgrade to errors.Join if go ≥1.20
	}
	return nil
}
