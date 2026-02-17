# Foundry Node: HITL Escalation & Persistence

## 1. Escalation Patterns

### 1.1 Manager → Director → VP Chain

Use `timeout` outputs to create automatic escalation hierarchies:

```
┌─────────────────────┐
│   manager-review    │  timeout: 2h
│   outputs:          │
│     - timeout →     │──────┐
│     - approved →    │      │
│     - rejected →    │      │
└─────────────────────┘      │
                             ▼
                    ┌─────────────────────┐
                    │  director-review    │  timeout: 4h
                    │  outputs:           │
                    │    - timeout →      │──────┐
                    │    - approved →     │      │
                    │    - rejected →     │      │
                    └─────────────────────┘      │
                                                 ▼
                                        ┌─────────────────────┐
                                        │    vp-review        │  timeout: 8h
                                        │    outputs:         │
                                        │      - approved →   │
                                        │      - rejected →   │
                                        │      (no timeout)   │  ← Terminal escalation
                                        └─────────────────────┘
```

**Key Behaviors:**
1. Each escalation **resets the deadline** to the new node's timeout
2. Workitem context includes escalation history for visibility
3. Final node in chain has no `timeout` output → Failure on timeout

### 1.2 Delegate Pattern

Route to a backup reviewer when primary is unavailable:

```yaml
kind: FoundryNode
metadata:
  name: alice-review
spec:
  timeout: "8h"
  outputs:
    - name: "approved"
      targetRole: "next-stage"
    - name: "timeout"
      target: "bob-review"    # Alice's delegate
---
kind: FoundryNode
metadata:
  name: bob-review
spec:
  timeout: "8h"
  outputs:
    - name: "approved"
      targetRole: "next-stage"
    - name: "timeout"
      target: "team-lead-review"  # Escalate if both unavailable
```

### 1.3 Pool Escalation

Escalate from individual to pool:

```yaml
kind: FoundryNode
metadata:
  name: assigned-reviewer
spec:
  timeout: "4h"
  outputs:
    - name: "approved"
      targetRole: "next-stage"
    - name: "timeout"
      targetRole: "reviewer-pool"  # Any available reviewer
```

---

## 2. Persistence Configuration

HITL nodes use the **Federated Queue Mesh** pattern with SDK-managed SQLite storage.

### 2.1 Capability Requirement

```yaml
spec:
  capabilities:
    - "QUEUE:server"    # Enables QueuePeer gRPC service
  storage:
    size: "1Gi"
    mountPath: "/data"  # queue.db created here
```

**Validation:** The Operator rejects HITL nodes with `QUEUE:server` but no `spec.storage`.

### 2.2 SDK-Managed Schema

The SDK automatically initializes `{mountPath}/queue.db` with:

```sql
CREATE TABLE hitl_queue (
    id TEXT PRIMARY KEY,
    shard_id TEXT NOT NULL,           -- Pod identity (e.g., "hitl-0")
    workitem_json TEXT NOT NULL,
    enqueued_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    status TEXT DEFAULT 'waiting',    -- waiting | claimed | decided
    claimed_at TIMESTAMP,
    decided_at TIMESTAMP,
    decision_json TEXT
);

CREATE INDEX idx_status ON hitl_queue(status);
CREATE INDEX idx_shard ON hitl_queue(shard_id);
```

> **Note:** The HITL Node is a **State Engine**. Assignment tracking is delegated to the consuming layer.

### 2.3 In-Memory Mode (Testing Only)

For unit tests, set `UsePersistence: false` in the SDK config. This uses `sync.Map` and disables the QueuePeer service. Not suitable for production or scaling tests.

---

## 3. Security Model

### 3.1 Design Principle: State Engine

The HITL Node is a **mechanical queue** that tracks status transitions (`waiting` → `claimed` → `decided`). Identity tracking is delegated to the consuming layer.

**Separation of Concerns:**

| Concern | Owner | Mechanism |
|---------|-------|----------|
| **Queue State** | HITL Node | `status` field in SQLite |
| **Human Identity** | Dashboard/BFF | External IdP (OAuth, SAML, SSO) |
| **Assignment Mapping** | Dashboard/BFF | Dashboard database ("Alice has Task X") |
| **Audit Trail (Who)** | Dashboard/BFF | Decision logs with identity correlation |

### 3.2 Network Exposure

**Internal Service Only.** The HITL API is:
- Exposed on the Service port (80/8080)
- **NOT** exposed via external Ingress
- Protected by Kubernetes **NetworkPolicies**

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: hitl-access
spec:
  podSelector:
    matchLabels:
      foundry.io/node-type: hitl
  ingress:
    - from:
        - podSelector:
            matchLabels:
              app: foundry-dashboard  # Only Dashboard pods
```

### 3.3 Authentication

**None at the Node level.** The Node trusts the internal network. Authentication is the responsibility of the edge service (Dashboard) that calls this API.

### 3.4 Revised Workflow

```
1. Dashboard: User "Alice" clicks "Claim"
2. Dashboard Backend: Checks if Alice is authorized
3. Dashboard Backend: Records "Alice claimed Task X" in its own DB
4. Dashboard Backend → HITL Node: POST /queue/X/claim
5. HITL Node: Checks status == waiting, sets status = claimed, returns OK
6. Result: Item is locked. HITL Node doesn't know "Alice" has it, only that it's taken.
```

### 3.5 Audit Trail

The HITL Node logs:
- Timestamp of status transitions
- Decision output and comment
- Request metadata (client IP for debugging)

The Dashboard logs:
- Human identity (who claimed/decided)
- Authorization checks
- Full audit trail with identity correlation

---

## 4. Scaling Behavior

HITL nodes use the **Federated Queue Mesh** for horizontal scaling. This provides a unified "Global Queue" view while maintaining isolated storage per pod.

**Key Behaviors:**
- **Scatter-Gather Reads:** `GET /queue` on any pod aggregates from all peers
- **Proxy Writes:** `POST /queue/{id}/decide` is proxied to the owning shard
- **Partial Availability:** If a pod is down, its items are invisible but other pods continue serving

**Configuration:** HITL nodes automatically get Queue Mesh behavior when using `QUEUE:server` capability with `spec.storage`.

**Full Specification:** See [11_federated_queue_mesh.md](./11_federated_queue_mesh.md) for:
- Architecture diagrams
- Peer discovery via Headless Service DNS
- Failure behavior and recovery
- SDK integration (`QueueManager` interface)
- Proto definitions
