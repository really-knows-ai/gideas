# Atomic Foundry Flow: Data Model - Workitem CRD

## 1. `Workitem` (The Unit of Work)

Tracks the state and artefacts of a specific work unit flowing through a graph.

### 1.1 Design Principle: The Source of Truth

**A Workitem CRD is the authoritative record of work state.** Nodes are stateless; they read state from the CRD at the start of each execution and write mutations back to it.

### 1.2 Field Ownership & Mutability

| Field | Owner | Mutability | Mechanism |
|-------|-------|------------|-----------|
| `spec.*` | **Operator** | **Immutable** | Set at creation |
| `status.state` | **Operator** | System-Managed | Computed from assignment |
| `status.currentAssignee` | **Operator** | System-Managed | Set by Operator |
| `status.artefacts[]` | **Sidecar** | Append-Only | Via `StoreArtefact` RPC |
| `status.feedback[]` | **Sidecar** | Append/Update | Via feedback RPCs |
| `status.routingInstruction` | **Sidecar** | Overwrite | Set when Node returns Result |
| `status.guestbook` | **Sidecar** | Increment-Only | Auto-updated on visits |

---

## 2. Schema

```yaml
apiVersion: flow.gideas.io/v1
kind: Workitem
metadata:
  name: petition-dark-mode-v1
spec:
  type: "petition-v1"

status:
  state: "Running"
  currentAssignee: "security-quench-svc"
  previousAssignee: "forge-node"
  startedAt: "2026-01-04T15:00:00Z"
  lastActivityAt: "2026-01-04T15:00:30Z"
  terminalContract: ""
  failureReason: ""
  
  routingInstruction:
    type: "route_to_output"
    targetOutput: "default"
    targetNode: ""
  
  guestbook:
    forge-node: 1
    quench-node: 2
    refine-node: 1
  
  artefacts:
    - kind: "petition-draft"
      name: "petition_draft.md"
      latestVersion: "sha256:def456..."
      versions:
        - hash: "sha256:abc123..."
          createdAt: "2026-01-04T14:20:00Z"
          createdByNode: "forge-node"
      # Note: Passport stamps are stored in the Archivist alongside artefact content.
      # Use GetArtefactMetadata() to retrieve stamps for a specific version.
  
  feedback:
    - id: "fb-101"
      target: "petition-draft"
      source: "appraise-node"
      severity: "MEDIUM"
      state: "pending"
      message: "Consider adding input validation"
      history:
        - timestamp: "2026-01-04T14:30:00Z"
          author: "appraise-node"
          role: "appraiser"
          action: "opened"
          message: "Consider adding input validation"
```

---

## 3. Workitem Lifecycle State Machine

### 3.1 States

| State | Description |
|-------|-------------|
| `Pending` | Waiting for assignment or queued between nodes |
| `Running` | Assigned to a node, actively processing |
| `Completed` | Terminal Guard validated, work is done |
| `Failed` | Validation failure, timeout, or explicit failure |

### 3.2 State Machine Diagram

```
                                    ┌─────────────────────────────────────┐
                                    │                                     │
                                    ▼                                     │
┌──────────┐   assign()    ┌──────────┐   route()     ┌──────────┐       │
│          │──────────────▶│          │──────────────▶│          │───────┘
│ Pending  │               │ Running  │               │ Pending  │
│          │◀──────────────│          │               │          │
└──────────┘   (re-queue)  └────┬─────┘               └──────────┘
     │                          │
     │                          │ complete()
     │                          ▼
     │                    ┌──────────┐
     │                    │          │
     │                    │Completed │  (Terminal State)
     │                    │          │
     │                    └──────────┘
     │
     │                    ┌──────────┐
     │      fail()        │          │
     └───────────────────▶│  Failed  │  (Terminal State)
                          │          │
                          └──────────┘
                               ▲
                               │ timeout() / thrash() / error()
                               │
                          ┌────┴─────┐
                          │ Running  │
                          └──────────┘
```

### 3.3 State Transitions

| From | To | Trigger | Guard Conditions | Actions |
|------|-----|---------|------------------|---------|
| `Pending` | `Running` | `assign()` | Node is ready (`/readyz` returns 200); Node has capacity (`activeWorkitems < concurrency`) | Set `currentAssignee`; Set `startedAt`; Increment `guestbook[node]` |
| `Running` | `Pending` | `route()` | Node returns `RouteToOutput` or `RouteTo`; Target node exists; No thrash detected | Clear `currentAssignee`; Set `previousAssignee`; Set `routingInstruction` |
| `Running` | `Completed` | `complete()` | Node returns `Complete()`; Terminal contract satisfied; All required artefacts present and valid | Set `terminalContract`; Set `completedAt` |
| `Running` | `Failed` | `timeout()` | `lastActivityAt` exceeds `timeout` duration | Set `failureReason: TIMEOUT` |
| `Running` | `Failed` | `thrash()` | `sum(guestbook.values()) > maxVisits` | Set `failureReason: THRASH_DETECTED` |
| `Running` | `Failed` | `error()` | Node returns explicit failure; Handler panic; Validation error | Set `failureReason` to error code |
| `Pending` | `Failed` | `fail()` | No available nodes for extended period; System error | Set `failureReason` |

### 3.4 Terminal States

Both `Completed` and `Failed` are terminal states. Once a Workitem enters either state, no further transitions are possible. The Workitem CRD remains in etcd for the configured retention period (`retention.workitemTTL`, default 30 days) before garbage collection.

---

## 4. Routing Instruction

Written by Sidecar after Node returns a Result, consumed by Operator.

```yaml
routingInstruction:
  type: "route_to_output"
  targetOutput: "pass"
  targetNode: ""
```

| Type | Description |
|------|-------------|
| `route_to_output` | Route via named output channel |
| `route_to` | Direct route to specific node |
| `complete` | Signal terminal completion |

---

## 5. Note: Why No Full Routing History

The Workitem includes `previousAssignee` for immediate routing context.

**Historical Context is Tracked By:**
1. **`guestbook`** (infrastructure): Visit counts per node (hidden from nodes)
2. **`feedback.history`** (semantic): Debate depth per artefact
3. **Telemetry Stream** (audit): Full path reconstructable from events

**Why No `visited[]` Array?**
- etcd 1.5MB CRD limit
- Nodes make decisions based on `feedback.history`
- Separation of concerns

---

## 6. Versioning Note

Examples in this document use `flow.gideas.io/v1`. For differences between v1 and v2 and migration guidance, see governance_spec/08_versioning_and_migration.md.

## 7. Related Documents

- [06a_data_model_feedback.md](./06a_data_model_feedback.md) - Feedback model, passport stamps, Law CRD
