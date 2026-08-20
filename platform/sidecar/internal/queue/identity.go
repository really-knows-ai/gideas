package queue

import (
	"net"

	"github.com/foundry/flow/pkg/randid"
)

// NewShardID returns a random per-boot shard ID from pkg/randid (never
// HOSTNAME — identity is a random nanoid, not the pod name).
func NewShardID() string {
	return randid.NewRandomID()
}

// ShardAddr joins the dialable host + peer port into a dialable address.
// Identity is never the address and never dialable; the address is derived
// from the real listener, never from the shard ID (R-2.1/R-2.2). Callers pass
// the sidecar's dialable host (e.g. the node host from FLOW_NODE_ADDRESS) and
// the peer listen port (FLOW_SIDECAR_PORT, default 50051).
func ShardAddr(host, port string) string {
	return net.JoinHostPort(host, port)
}
