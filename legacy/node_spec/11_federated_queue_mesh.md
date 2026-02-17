# Foundry Node: Federated Queue Mesh

## 11. Federated Queue Mesh

The **Federated Queue Mesh** is a general-purpose pattern for nodes that require persistent work queues with horizontal scaling. It provides a unified "Global Queue" view while maintaining the robustness of isolated storage ("Shared Nothing" architecture).

---

## 11.1 Overview

### Problem Statement

Stateful nodes (HITL, Embassy, custom queue-based nodes) use `StatefulSets` with isolated PVCs for resilience. When scaling to multiple replicas (e.g., `node-0`, `node-1`), the work queues become fragmented. A client connecting to `node-0` cannot see or claim work residing on `node-1`.

### Design Constraints

- **No Centralized Database:** We reject Postgres/Redis to maintain the "minimal infra" and "embedded" philosophy
- **Fault Isolation:** Pod failure should not cause total queue unavailability
- **Consistent Ownership:** Each queue item has exactly one owner shard

### Solution: Headless Mesh Pattern

Nodes act as their own aggregators via peer-to-peer gRPC communication:

- **Discovery:** Nodes discover peers via Kubernetes Headless Services (DNS)
- **Read Logic (Scatter-Gather):** Request to any node triggers parallel `GetLocalQueue` calls to all peers
- **Write Logic (Proxy Routing):** Mutations are proxied to the specific shard owner
- **Persistence:** SDK-managed SQLite schema, auto-enabled by `spec.storage`

---

## 11.2 Capability & Configuration

### Enabling the Queue Mesh

Add the `QUEUE:server` capability and define storage:

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: review-queue
spec:
  image: "registry/nodes/review-queue:v1.0"
  
  capabilities:
    - "QUEUE:server"              # Enables QueuePeer gRPC service
  
  storage:
    size: "1Gi"
    mountPath: "/data"            # queue.db created here
  
  # ... other config
```

### Validation Rules

| Condition | Result |
|-----------|--------|
| `QUEUE:server` without `spec.storage` | **Rejected** - storage required for persistence |
| `QUEUE:server` present | **StatefulSet** deployment (not Deployment) |

### Operator Behavior

When `QUEUE:server` is present, the Operator:

1. Deploys as **StatefulSet** (stable pod identity for DNS discovery)
2. Creates a **Headless Service** (no ClusterIP, enables DNS-based peer discovery)
3. Injects the Queue Manager SDK module into the Sidecar

---

## 11.3 Architecture

```
                    ┌─────────────────────────────────────────┐
                    │        Kubernetes Headless Service       │
                    │    review-queue.flow-ns.svc.cluster.local│
                    └─────────────────────────────────────────┘
                                        │
           ┌────────────────────────────┼────────────────────────────┐
           │                            │                            │
           ▼                            ▼                            ▼
    ┌─────────────┐             ┌─────────────┐             ┌─────────────┐
    │  node-0     │◄───gRPC────►│  node-1     │◄───gRPC────►│  node-2     │
    │  queue.db   │             │  queue.db   │             │  queue.db   │
    │  (shard 0)  │             │  (shard 1)  │             │  (shard 2)  │
    └─────────────┘             └─────────────┘             └─────────────┘
```

### Peer Discovery

Nodes discover peers via Kubernetes Headless Service DNS:

```
DNS Query: review-queue.flow-ns.svc.cluster.local
Returns:   review-queue-0.review-queue.flow-ns.svc.cluster.local
           review-queue-1.review-queue.flow-ns.svc.cluster.local
           review-queue-2.review-queue.flow-ns.svc.cluster.local
```

The SDK's Queue Manager:
1. Queries DNS on startup and periodically (every 30s)
2. Maintains gRPC connections to all discovered peers
3. Handles peer join/leave gracefully

---

## 11.4 Read Pattern: Scatter-Gather

A read request to **any** pod aggregates from all peers:

```
Client → node-1 (gateway)
              │
              ├── GetLocalQueue() → [items from node-1.queue.db]
              ├── gRPC: node-0.GetLocalQueue() → [items from node-0]
              └── gRPC: node-2.GetLocalQueue() → [items from node-2]
              │
              └── Merge & Return unified list
```

### Response Metadata

Each item in the response includes shard ownership:

```json
{
  "items": [
    { "id": "wi-123", "shard_id": "node-0", ... },
    { "id": "wi-456", "shard_id": "node-1", ... }
  ]
}
```

HTTP responses include header: `X-Foundry-Shard-Owner: <shard_id>`

---

## 11.5 Write Pattern: Proxy Routing

Mutations (claim, release, complete) are proxied to the owning shard:

```
Client → node-1 (gateway)
              │
              ├── Parse item ID → shard_id = "node-0"
              └── gRPC: node-0.CompleteItem(decision)
                            │
                            └── Update node-0.queue.db
                            └── Return result to gateway
              │
              └── Return result to client
```

### Local vs Remote

| Request Target | Owner | Action |
|----------------|-------|--------|
| `node-1` | `node-1` | Execute locally |
| `node-1` | `node-0` | Proxy to `node-0` via gRPC |
| `node-1` | `node-2` (down) | Return `503 Service Unavailable` |

---

## 11.6 Failure Behavior (Partial Availability)

If a pod is down, the mesh operates in **degraded mode**:

| Scenario | Behavior |
|----------|----------|
| `node-1` down | Items on `node-1` invisible in reads; `node-0`, `node-2` continue serving |
| Write to down shard | `503 Service Unavailable` with `X-Foundry-Shard-Owner: node-1` |
| Recovery | Pod restarts, rejoins mesh, items become visible again |

**Design Philosophy:** Partial availability is preferred over total outage. Work continues on healthy shards while unhealthy shards recover.

### Failure Detection

- **Peer Health:** gRPC keepalive (10s interval, 20s timeout)
- **Query Timeout:** Scatter-gather uses 5s timeout per peer; slow peers are excluded from response
- **Retry Policy:** No automatic retry for writes to down shards (fail fast, let client retry)

---

## 11.7 Persistence Schema

The SDK manages `{mountPath}/queue.db` with this schema:

```sql
CREATE TABLE queue_items (
    id TEXT PRIMARY KEY,
    shard_id TEXT NOT NULL,           -- Pod identity (e.g., "node-0")
    workitem_id TEXT NOT NULL,        -- Reference to Workitem CRD
    payload_json TEXT NOT NULL,       -- Application-specific data
    status TEXT DEFAULT 'pending',    -- pending | claimed | processing | completed
    enqueued_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    claimed_by TEXT,
    claimed_at TIMESTAMP,
    completed_at TIMESTAMP,
    result_json TEXT
);

CREATE INDEX idx_status ON queue_items(status);
CREATE INDEX idx_claimed_by ON queue_items(claimed_by);
CREATE INDEX idx_shard ON queue_items(shard_id);
CREATE INDEX idx_workitem ON queue_items(workitem_id);
```

---

## 11.8 SDK Integration

### Queue Manager Module

The SDK provides a `QueueManager` for nodes using `QUEUE:server`:

```go
// Go SDK
type QueueManager interface {
    // Enqueue adds an item to the local shard
    Enqueue(ctx context.Context, workitemID string, payload interface{}) (string, error)
    
    // GetGlobalQueue returns items from all shards (scatter-gather)
    GetGlobalQueue(ctx context.Context, filter QueueFilter) ([]QueueItem, error)
    
    // GetLocalQueue returns items from this shard only
    GetLocalQueue(ctx context.Context, filter QueueFilter) ([]QueueItem, error)
    
    // Claim marks an item as claimed (proxied if remote)
    Claim(ctx context.Context, itemID, claimedBy string) (*QueueItem, error)
    
    // Release unclaims an item (proxied if remote)
    Release(ctx context.Context, itemID string) (*QueueItem, error)
    
    // Complete marks an item as done (proxied if remote)
    Complete(ctx context.Context, itemID string, result interface{}) error
    
    // GetPeers returns currently connected peer shard IDs
    GetPeers(ctx context.Context) ([]string, error)
}
```

```typescript
// TypeScript SDK
interface QueueManager {
    enqueue(workitemId: string, payload: unknown): Promise<string>;
    getGlobalQueue(filter?: QueueFilter): Promise<QueueItem[]>;
    getLocalQueue(filter?: QueueFilter): Promise<QueueItem[]>;
    claim(itemId: string, claimedBy: string): Promise<QueueItem>;
    release(itemId: string): Promise<QueueItem>;
    complete(itemId: string, result?: unknown): Promise<void>;
    getPeers(): Promise<string[]>;
}
```

### Accessing the Queue Manager

```go
func (n *MyQueueNode) Assigned(ctx context.Context, item Workitem) Result {
    // Enqueue work for later processing
    queueID, err := n.Queue.Enqueue(ctx, item.Metadata.Name, MyPayload{...})
    if err != nil {
        return RouteToOutput("error")
    }
    
    // Item is now in the queue, block or return based on node type
    // ...
}
```

---

## 11.9 Use Cases

### HITL Nodes

Human-in-the-loop nodes use the Queue Mesh for human decision queues:
- Workitem arrives → enqueued for human review
- Human claims via REST API (scatter-gather shows all items)
- Human decides → proxied to owning shard → workitem continues

See [10_hitl_api.md](./10_hitl_api.md) for HITL-specific behavior.

### Embassy Nodes (Export Queue)

Embassy nodes use the Queue Mesh for reliable async export:
- Export request → enqueued for delivery
- Background worker claims and attempts delivery
- On failure → released back to queue with backoff
- On success → completed, workitem marked as exported

See [flow_spec/cross_flow_collaboration/06_operations.md](../flow_spec/cross_flow_collaboration/06_operations.md) for export queue details.

### Custom Queue Nodes

Any node requiring durable work queues with scaling can use `QUEUE:server`:
- Batch processing nodes
- Rate-limited API nodes (queue requests, process at limit)
- Approval workflow nodes

---

## 11.10 Telemetry

Queue Mesh nodes emit standard telemetry events:

| Event | When | Payload |
|-------|------|---------|
| `foundry.queue.enqueued` | Item added to queue | `{workitemId, shardId, queueDepth}` |
| `foundry.queue.claimed` | Item claimed | `{workitemId, shardId, claimedBy, waitTime}` |
| `foundry.queue.released` | Item released | `{workitemId, shardId, claimedBy, claimDuration}` |
| `foundry.queue.completed` | Item completed | `{workitemId, shardId, processingTime}` |
| `foundry.queue.peer_joined` | New peer discovered | `{peerId, peerCount}` |
| `foundry.queue.peer_left` | Peer connection lost | `{peerId, peerCount, reason}` |

### 11.10.1 ID Semantics
- The mesh uses the Workitem ID as the canonical identifier.
- `shard_id` identifies the owning pod. Clients do not need to know `shard_id` for read operations; actions are automatically proxied to the owner.

---

## 11.11 Proto Definition

See [sdk/proto/queue.proto](./sdk/proto/queue.proto) for the `QueuePeer` gRPC service definition.
