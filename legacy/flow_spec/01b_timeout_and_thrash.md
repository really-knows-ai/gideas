# Atomic Foundry Flow: Timeout, Thrash, and Cleanup

## 1. The Reaper Loop (Watchdog)

Watches for Workitems that have exceeded their **inactivity deadline**. The deadline is calculated as `status.lastActivityAt + FoundryNode.spec.timeout`.

### 1.1 Activity-Based Deadline (Idle Timeout Model)

The timeout is based on **inactivity**. The timer resets on every SDK call or heartbeat. Active nodes (sending heartbeats or making SDK calls) remain alive indefinitely, regardless of total execution duration.

| Field | Purpose |
|-------|----------|
| `status.startedAt` | When the node began processing (informational) |
| `status.lastActivityAt` | Most recent heartbeat or SDK call (the timeout anchor) |
| `FoundryNode.spec.timeout` | Maximum idle time before timeout |

**Timeout Condition:**
```
IF Now > (status.lastActivityAt + FoundryNode.spec.timeout) THEN timeout
```

### 1.2 Timeout Handling (Escalation-Aware)

When inactivity timeout is detected, the Operator checks for an escalation route before failing:

```
1. Fetch FoundryNode CRD for current assignee
2. Check if FoundryNode.spec.outputs[] contains output named "timeout"
3. IF "timeout" output found:
   a. Log: "Inactivity timeout; escalating via 'timeout' route."
   b. Select target node from timeout output (same as Router Guard)
   c. Patch Workitem:
      - currentAssignee = targetNode
      - startedAt = Now()
      - lastActivityAt = Now()  # RESET INACTIVITY TIMER
      - state = "Pending"
   d. Clear routingInstruction
   → Workitem continues in escalation chain
4. IF "timeout" output NOT found:
   a. Mark Workitem.status.state = "Failed"
   b. Set Workitem.status.failureReason = "TIMEOUT"
```

### 1.3 Crash Detection Philosophy (Silence Is Death)

- The Operator infers Workitem state from inactivity detection only.
- **Crash Detection = Silence Detection:** If a node crashes, it stops heartbeating. The Reaper catches it after `timeout` seconds of inactivity.
- Hard crashes (e.g., OOMKill, segfault) and abandoned work are detected uniformly: if `lastActivityAt` goes stale, the Reaper declares failure/escalation.
- **Active nodes remain alive indefinitely:** A 5-minute LLM inference with continuous heartbeats completes successfully with a 60s timeout configured. The timeout measures idle time only.
- Rationale: decouple workflow state from infrastructure state; inactivity detection provides a single deterministic source of truth.

### 1.4 Escalation Chain Pattern

HITL nodes can define cascading escalation by chaining `timeout` outputs:

```yaml
# Manager-level review (2 hour timeout)
kind: FoundryNode
metadata:
  name: manager-review
spec:
  timeout: "2h"
  outputs:
    - name: "approved"
      targetRole: "next-stage"
    - name: "rejected"
      targetRole: "refiner"
    - name: "timeout"           # Escalates on timeout
      target: "director-review"
---
# Director-level review (4 hour timeout)
kind: FoundryNode
metadata:
  name: director-review
spec:
  timeout: "4h"
  outputs:
    - name: "approved"
      targetRole: "next-stage"
    - name: "rejected"
      targetRole: "refiner"
    - name: "timeout"           # Escalates further
      target: "vp-review"
```

> **Note:** When escalating, the deadline is **reset** to the new node's timeout. This prevents cumulative timeout exhaustion in long escalation chains.

### 1.5 Hard Crash Handling (OOMKilled, Eviction, Node Death)

The Operator does **not** actively detect that a Pod has vanished. It relies entirely on the Reaper Loop's deadline enforcement.

**Behavior During Hard Crash:**

1. **The Wait Period:** The Workitem remains in the `Running` state with `currentAssignee` pointing to the dead pod until the inactivity deadline passes.

2. **The Wait:** The system waits until `Now() > executionDeadline`.

3. **Resolution:** Once timeout is detected, normal escalation logic applies:
   - If `timeout` output exists → reassign to escalation target
   - If no `timeout` output → mark as `Failed` with reason `TIMEOUT`

**Implication:** The Operator cannot distinguish between "logic took too long" and "node exploded." Both are treated as timeouts.

**Design Recommendation:** To ensure robustness against hard crashes, configure a `timeout` output that routes to a retry queue or the same role.

```yaml
# Crash-resilient node pattern
kind: FoundryNode
metadata:
  name: forge-node
spec:
  timeout: "60s"
  outputs:
    - name: "default"
      targetRole: "quench"
    - name: "timeout"           # Handles both slow execution AND crashes
      targetRole: "generator"   # Route back to same role for retry
```

**Enforcement Model:** The Operator is the **ultimate authority** on Workitem state, but it relies on the **Sidecar** to perform local cleanup:

1. **Sidecar (Local Enforcement):** Detects timeout locally, cancels the gRPC context (`ctx.Done()`), and reports `WorkitemResult{TimedOut: true}`. The Pod remains running.
2. **Operator (Authoritative State):** Observes the timeout, checks for `timeout` output, and either escalates or patches `Workitem.status.state = "Failed"`.

> **Note:** Context cancellation is the primary enforcement mechanism, preserving logs and avoiding resource churn.

---

## 2. Thrash Guard (Infrastructure Loop Detection)

The Sidecar maintains a `guestbook` frequency map in the Workitem status to prevent infinite loops caused by infrastructure failures (bugs, misconfigurations, crashes).

**Mechanism:**
1. Each time a node begins processing a workitem, the Sidecar increments `guestbook[nodeName]`
2. Before invoking the Node handler, the Sidecar checks if `guestbook[nodeName] > maxVisits` (default: 30)
3. If exceeded: Sidecar immediately fails the workitem with `THRASH_DETECTED`

**Scope:** This is an **infrastructure guard** against runaway loops.

| Guard | Detection | Owner | Response |
|-------|-----------|-------|----------|
| **Thrash Guard** | `guestbook` visit count | Sidecar | Fail workitem immediately |
| **Fatigue Detection** | `feedback.history` depth | Gate Node | Escalate to Assay |

**Design Note:** The `guestbook` is internal to the Sidecar. Routing decisions based on visit history are the Gate Node's responsibility, using feedback depth.

### 2.2 Interaction with Routing Guards

Thrash Guard operates at infrastructure level and can preempt semantic routing when `maxVisits` is exceeded. Routing Guards validate outputs and terminal contracts during normal operation. Configure `execution.maxVisits` in Helm to set Thrash Guard limits; configure node outputs and contracts for Routing Guard semantics.

### 2.1 Why No Full `visited[]` History

The Workitem status includes `previousAssignee` for immediate routing context. The full visit history is maintained internally by the Operator and Sidecar.

**Responsibility Distribution:**

| Concern | Tracker | Visibility | Purpose |
|---------|---------|-----------|---------|
| **Infrastructure Loops** | Sidecar's `guestbook` | Hidden from Node logic | Detect and fail runaway loops (30 visits = crash) |
| **Semantic Loops** | `feedback.history` depth | Visible to Gate Nodes | Detect LLM disagreements (3 rounds = escalate) |
| **Immediate Context** | Operator's `previousAssignee` | Visible to Node logic | Return-to-sender routing (e.g., `$sender`) |
| **Full Audit Trail** | Telemetry (Flow Monitor) | External monitoring | Historical reconstruction via Prometheus/SQLite |

**Why Not a Full `visited[]` Array?**

1. **etcd Size Limits:** Kubernetes etcd has a 1.5MB limit per CRD. Long-running flows with hundreds of hops would cause the Workitem CRD to exceed this limit.

2. **Semantic vs Infrastructure:** The system intentionally separates infrastructure concerns (handled by Sidecar's `guestbook`) from semantic concerns (handled by `feedback.history` depth).

3. **Stateless Worker Model:** Nodes make decisions based on current Workitem state (artefacts, feedback, context).

4. **Clean Separation:** `previousAssignee` is sufficient for the most common pattern: routing back to the node that sent you the work (the `$sender` pattern).

---

## 3. Node Termination Grace Period

When generating the `FoundryNode` Deployment manifest, the Operator calculates and injects the `terminationGracePeriodSeconds` field to ensure infrastructure patience exceeds application patience.

**Formula:**
```
terminationGracePeriodSeconds = MAX(FoundryNode.spec.timeout, Flow.spec.standardNodeTimeout) * 2
```

**Fallback:** If no timeouts are defined, default to `60s` (System Default 30s × 2).

**Rationale:** The `2×` multiplier guarantees the Sidecar's internal watchdog has time to detect a hang, log the error, and flush telemetry before Kubernetes executes the `SIGKILL`. This separates operational scale-down events from genuine application failures in telemetry.

---

## 4. Retention & Cleanup (etcd Hygiene)

The Operator manages Workitem lifecycle to prevent etcd bloat from accumulated completed work.

### 4.1 TTL Controller

The Operator runs a background reconciliation loop that deletes Workitems meeting these criteria:
- `status.state` is `Completed` or `Failed`
- Age since state change exceeds `FoundryFlow.spec.retention.workitemTTL`

```go
// Operator TTL Reconciler (runs every 5 minutes)
func (r *WorkitemReconciler) cleanupExpired(ctx context.Context) error {
    ttl := r.flowConfig.Spec.Retention.WorkitemTTL  // e.g., 30d
    
    var workitems WorkitemList
    r.Client.List(ctx, &workitems, client.MatchingFields{
        "status.state": []string{"Completed", "Failed"},
    })
    
    for _, wi := range workitems.Items {
        age := time.Since(wi.Status.CompletedAt)
        if age > ttl {
            r.Client.Delete(ctx, &wi)
            r.Metrics.WorkitemsDeleted.Inc()
        }
    }
    return nil
}
```

### 4.2 Configuration

```yaml
# In FoundryFlow.spec
retention:
  workitemTTL: "30d"     # Delete completed workitems after 30 days
  artefactRetention:
    maxVersions: 10      # Keep last 10 versions per artefact
    maxAge: "7d"         # Delete old versions (latest always kept)
```

**Cascading Cleanup:** When a Workitem is deleted, the Archivist's garbage collector removes orphaned artefact versions that are no longer referenced by any Workitem.
