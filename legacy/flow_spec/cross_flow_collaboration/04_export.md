# Cross-Flow Collaboration: Export Operation

## 1. Overview

`Export()` is a **Session Terminator**. It bundles the current Workitem's state, transmits it to a target Flow, and completes the local Workitem atomically.

**Key Constraint:** Export terminates the Node's execution lifecycle. You cannot export to multiple targets sequentially—the first successful export ends the session.

## 2. Capability Requirement

The `EXPORT` capability is **mutually inclusive** with `isTerminal: true`.

**Validation (Flow Operator Admission Webhook):**
- If `capabilities` contains `"EXPORT"`, the node MUST have `isTerminal: true`
- If not, the `FoundryNode` deployment is **rejected**

```yaml
# Valid: Export Node
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: export-to-execution
spec:
  image: "registry/nodes/exporter:v1.0"
  roles: ["exporter"]
  capabilities:
    - "READ:artefact/*"
    - "EXPORT"                    # Requires isTerminal
  outputs: []                     # Terminal node
  isTerminal: true
  terminalContract: "exported"
```

```yaml
# Invalid: EXPORT without isTerminal (REJECTED)
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: bad-exporter
spec:
  capabilities:
    - "EXPORT"
  outputs:
    - name: "continue"            # Has outputs = not terminal
      targetRole: "processor"
  # isTerminal: false (implicit)  # VALIDATION ERROR
```

## 3. Export Execution

When a node calls `Export()`:

1. **Bundle:** Sidecar creates the `.fb` archive containing manifest, artefacts (content + passport from Archivist), and provenance
2. **Sign:** Sidecar signs the manifest with the node's private key
3. **Transmit:** Sidecar sends the bundle to the target Ingress endpoint
4. **Receipt:** Target returns new Workitem ID; Sidecar records in `status.output.exportResult`
5. **Complete:** If transmission succeeds, Sidecar marks Workitem as `Completed`
6. **Terminate:** No further node logic executes

**This is atomic.** If transmission fails, the Workitem is NOT completed—the node can handle the error.

### 3.1 The Receipt Pattern

The `Export()` call returns the **new Workitem ID** from the target Flow. This creates a permanent audit link:

```go
result, err := n.Export(ctx, ExportRequest{
    TargetURL: "https://flow-execute.example.com/ingress",
})
if err != nil {
    // Handle failure - workitem is still Running
    return RouteToOutput("retry_queue")
}
// result.RemoteWorkitemID = "flow-execute:wi-abc123"
// Automatically recorded in status.output.exportResult
return Complete()
```

**Workitem Status (after export):**
```yaml
status:
  phase: "Completed"
  output:
    exportResult:
      targetFlow: "flow-execute"
      remoteWorkitemId: "wi-abc123"
      exportedAt: "2026-01-09T14:30:00Z"
```

**No Callback:** The source Flow does NOT receive notification when the target completes the imported Workitem. Once `Export()` succeeds, the source's responsibility ends. The receipt provides a queryable link for audit/tracing purposes only.

### 3.2 Failure Handling (Synchronous Export)

When synchronous `Export()` fails, the Workitem remains `Running` and the handler can decide:

```go
result, err := n.Export(ctx, request)
if err != nil {
    switch {
    case errors.Is(err, foundry.ErrTargetUnavailable):
        // Target down - retry later
        return RouteToOutput("retry_queue")
    case errors.Is(err, foundry.ErrTreatyViolation):
        // Trust issue - cannot recover automatically
        return RouteToOutput("manual_review")
    default:
        // Unknown error - fail the workitem
        return foundry.FailWithError(err)
    }
}
```

| Error | Meaning | Recovery |
|-------|---------|----------|
| `UNAVAILABLE` | Target Ingress unreachable | Retry via queue or fallback |
| `PERMISSION_DENIED` | Treaty validation failed | Manual review (cert issue) |
| `INVALID_ARGUMENT` | Malformed bundle | Bug - fix and retry |
| `DEADLINE_EXCEEDED` | Transmission timeout | Retry with longer timeout |

## 4. Export Node Configuration

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: export-to-execution
spec:
  image: "registry/nodes/exporter:v1.0"
  roles: ["exporter"]
  timeout: "60s"
  
  capabilities:
    - "READ:artefact/*"
    - "EXPORT"
  
  outputs: []
  isTerminal: true
  terminalContract: "exported"
  
  env:
    - name: EXPORT_TARGET_URL
      value: "https://flow-execute.external.com/ingress"
```

## 5. Synchronous vs Async Export

The basic `Export()` operation is **synchronous**: it blocks until transmission succeeds or fails. This is simple but fragile—if the target is down, the export fails.

For production deployments with unreliable targets, use the **Export Queue Pattern** (Section 6).

| Mode | Reliability | Latency | Complexity |
|------|-------------|---------|------------|
| **Synchronous** | Low (fails if target down) | Low | Simple |
| **Queued (Async)** | High (retries with backoff) | Higher | Requires StatefulSet |

## 6. Export Queue Pattern (Async)

For reliable cross-flow delivery to potentially-unavailable targets, the Embassy Node can operate as a **Persistent Edge Queue**.

### 6.1 The Problem

Synchronous export is fragile:
- Target Ingress may be temporarily unavailable
- Network partitions cause immediate failure
- No automatic retry mechanism

### 6.2 The Solution: Stateful Edge Queue

Convert the Embassy Node from a stateless gateway to a **stateful outbound queue**:

1. **Main Thread:** Accepts workitem, enqueues to persistent SQLite, immediately acks
2. **Background Worker:** Polls queue, builds bundles, transmits with retry, reports failures

```
┌─────────────────────────────────────────────────────────────────────┐
│                        Embassy Node (StatefulSet)                   │
│  ┌─────────────────────┐      ┌─────────────────────────────────┐   │
│  │    Main Handler     │      │       Background Worker         │   │
│  │                     │      │                                 │   │
│  │  Receive Workitem   │      │  Poll Queue                     │   │
│  │       ↓             │      │       ↓                         │   │
│  │  Enqueue to SQLite  │      │  Build Bundle (Stream to PVC)   │   │
│  │       ↓             │      │       ↓                         │   │
│  │  Complete() [ACK]   │      │  Transmit (Retry on Fail)       │   │
│  │                     │      │       ↓                         │   │
│  └─────────────────────┘      │  Delete from Queue              │   │
│                               │       or                        │   │
│  ┌─────────────────────┐      │  Create FailureReport Workitem  │   │
│  │    SQLite Queue     │◄─────│                                 │   │
│  │  /var/lib/.../q.db  │      └─────────────────────────────────┘   │
│  └─────────────────────┘                                            │
│             ▲                                                       │
│             │ Persistent Volume (PVC)                               │
└─────────────────────────────────────────────────────────────────────┘
```

### 6.3 Infrastructure: StatefulSet Deployment

To ensure stable storage identity, the Operator deploys Embassy Nodes with `spec.storage` as **StatefulSets**:

**Operator Behavior:**
- If `FoundryNode.spec.storage` is **defined**: Deploy as `StatefulSet`
- If `FoundryNode.spec.storage` is **undefined**: Deploy as `Deployment`

**Why StatefulSet:**
- Ordered, stable pod identity (`embassy-0`, `embassy-1`, ...)
- Stable storage: `embassy-0` always mounts `pvc-embassy-0`
- On crash/restart, pod remounts same SQLite database

### 6.4 Configuration: Queued Export Node

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: export-queue-embassy
spec:
  image: "registry/nodes/embassy-queue:v1.0"
  roles: ["exporter"]
  
  # Persistent storage (triggers StatefulSet deployment)
  storage:
    size: "50Gi"
    accessMode: "ReadWriteOnce"
    mountPath: "/var/lib/foundry/queue"  # SQLite + bundle staging
  
  capabilities:
    - "READ:artefact/*"
    - "EXPORT"
    - "CREATE:workitem"            # For failure reports
  
  outputs: []
  isTerminal: true
  terminalContract: "queued"       # "Accepted into queue"
  
  env:
    - name: EXPORT_TARGET_URL
      value: "https://flow-execute.external.com/ingress"
    - name: MAX_RETRIES
      value: "50"
    - name: RETRY_BACKOFF_BASE
      value: "30s"
    - name: FAILURE_REPORT_TYPE
      value: "export-failure-v1"
```

### 6.5 Execution Model

**Main Handler (Enqueue):**

```go
func (n *EmbassyNode) Handle(ctx context.Context, item Workitem) Result {
    // 1. Enqueue to persistent SQLite
    err := n.queue.Enqueue(ExportJob{
        WorkitemID:  item.Metadata.Name,
        TargetURL:   n.config.TargetURL,
        ArtefactIDs: item.Status.Artefacts,
        EnqueuedAt:  time.Now(),
    })
    if err != nil {
        return RouteToOutput("queue-failed")
    }
    
    // 2. Block until background worker completes or fails
    // The workitem is NOT completed here - it remains Running
    // The background worker will complete/fail it
    return n.WaitForQueueResult(ctx, item.Metadata.Name)
}
```

**Background Worker (Async):**

```go
func (n *EmbassyNode) RunWorker(ctx context.Context) {
    for {
        job, err := n.queue.Dequeue()
        if err != nil {
            time.Sleep(5 * time.Second)
            continue
        }
        
        // Build bundle on PVC (streaming from Archivist)
        bundlePath, err := n.buildBundle(ctx, job)
        if err != nil {
            n.markWorkitemFailed(ctx, job, err)
            n.queue.Delete(job)
            continue
        }
        
        // Attempt transmission with retry
        result, err := n.transmitWithRetry(ctx, job, bundlePath)
        if err != nil {
            if job.RetryCount >= n.config.MaxRetries {
                // Exhausted retries - fail the workitem
                n.markWorkitemFailed(ctx, job, err)
                n.queue.Delete(job)
            } else {
                n.queue.Retry(job, err)
            }
            continue
        }
        
        // Success - complete workitem with receipt
        n.completeWithReceipt(ctx, job, result)
        os.Remove(bundlePath)
        n.queue.Delete(job)
    }
}
```

### 6.6 Atomic Export (Failure Handling)

**Critical:** The Workitem is **NOT completed when enqueued**. Enqueueing is an internal implementation detail, not a workflow state change.

| Event | Workitem Status | Action |
|-------|-----------------|--------|
| Enqueue succeeds | `Running` | Workitem remains active in Embassy queue |
| Transmission succeeds | `Completed` | Sidecar completes workitem with Receipt |
| Transmission fails (retryable) | `Running` | Queue retries with backoff |
| Retries exhausted | `Failed` | Workitem marked `Failed` with error details |
| Fatal error (e.g., `TREATY_VIOLATION`) | `Failed` | Immediate failure, no retry |

**Why This Matters:**
- No "zombie reports" or orphaned failure workitems
- Failure is recorded on the **source workitem itself**
- Lineage is preserved - you can always find what happened to a workitem
- Operators can query for `Failed` workitems with `failureReason: "export_exhausted"`

**Failure Status Example:**
```yaml
status:
  phase: "Failed"
  failureReason: "export_exhausted"
  failureMessage: "Export to https://flow-execute.example.com failed after 50 retries"
  failureDetails:
    targetUrl: "https://flow-execute.example.com/ingress"
    lastError: "PERMISSION_DENIED: Treaty validation failed - CA certificate expired"
    retryCount: 50
    firstAttempt: "2026-01-02T10:00:00Z"
    lastAttempt: "2026-01-09T14:30:00Z"
```

### 6.7 Queue Schema

The SQLite queue stores pending export jobs:

```sql
CREATE TABLE export_queue (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    workitem_id     TEXT NOT NULL,
    target_url      TEXT NOT NULL,
    artefact_ids    TEXT NOT NULL,  -- JSON array
    enqueued_at     TIMESTAMP NOT NULL,
    retry_count     INTEGER DEFAULT 0,
    last_attempt_at TIMESTAMP,
    last_error      TEXT,
    status          TEXT DEFAULT 'pending'  -- pending, processing, failed
);

CREATE INDEX idx_queue_status ON export_queue(status);
```

### 6.8 Retry Strategy

Default exponential backoff with jitter:

| Retry | Delay |
|-------|-------|
| 1 | 30s |
| 2 | 1m |
| 3 | 2m |
| 5 | 8m |
| 10 | ~4h |
| 20 | ~24h |
| 50 | ~7d (then fail) |

Configurable via `RETRY_BACKOFF_BASE` and `MAX_RETRIES` environment variables.

## 7. Multi-Target Export (Fork Pattern)

Since `Export()` is a session terminator, a single node **cannot** export to multiple targets sequentially. The first successful export ends the session.

**To broadcast to multiple targets, use the Fork Pattern:**

```
                    ┌─────────────────────┐
                    │    Fork Node        │
                    │  (Duplicates Work)  │
                    └─────────────────────┘
                              │
            ┌─────────────────┼─────────────────┐
            ▼                 ▼                 ▼
    ┌───────────────┐ ┌───────────────┐ ┌───────────────┐
    │ Export Node A │ │ Export Node B │ │ Export Node C │
    │ → Flow Alpha  │ │ → Flow Beta   │ │ → Flow Gamma  │
    └───────────────┘ └───────────────┘ └───────────────┘
```

**Fork Node Implementation:**

```go
func (n *ForkNode) Handle(ctx context.Context, item Workitem) Result {
    targets := []string{"flow-alpha", "flow-beta", "flow-gamma"}
    
    for _, target := range targets {
        // Create a copy of the workitem for each target
        n.CreateWorkitem(ctx, CreateWorkitemRequest{
            Type:   "export-job-v1",
            Context: map[string]string{
                "export_description": fmt.Sprintf("Export to %s", target),
                "export_target":      target,
                "source_workitem_id": item.Metadata.Name,
            },
        })
    }
    
    return Complete()  // Original workitem is done
}
```

Each spawned workitem routes to a target-specific Export Node.
