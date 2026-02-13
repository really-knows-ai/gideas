# SDK Reference: Health & Topology Operations

> **Version:** 1.0.0  
> **Status:** Implementation Contract

This document defines the health and topology operations available in the Foundry Node SDK.

---

## 1. Heartbeat

Signals that the handler is alive during long-running operations.

### Signature

```go
func (n *NodeContext) Heartbeat(ctx context.Context) error
```

### Behavior

1. **Timeout Reset:** Prevents Sidecar from declaring the handler dead.
2. **Frequency:** Call at least every 30 seconds during long operations.
3. **No-op if Fast:** Safe to call frequently; negligible overhead.

### Errors

| gRPC Code | Foundry Reason | Condition | Recovery |
|-----------|----------------|-----------|----------|
| `DEADLINE_EXCEEDED` | `CONTEXT_CANCELLED` | Handler context already cancelled | Exit handler gracefully |

### Why Heartbeats?

The Sidecar monitors handler liveness. If no activity (SDK calls or heartbeats) is detected for `executionDeadline`, the Sidecar:

1. Terminates the handler.
2. Reports `HEARTBEAT_TIMEOUT` to the Operator.
3. Workitem transitions to `Failed` or routes to `timeout` output.

### Example: Long-Running LLM Operation

```go
func handler(ctx context.Context, node foundry.NodeContext, workitem *foundry.Workitem) foundry.Result {
    // Fetch large document
    content, _ := node.FetchArtefact(ctx, "source-document")
    
    // Split into chunks for LLM processing
    chunks := splitIntoChunks(content, 4000)
    
    results := make([]string, len(chunks))
    for i, chunk := range chunks {
        // Process each chunk (may take 10-30 seconds each)
        result, err := processWithLLM(ctx, chunk)
        if err != nil {
            return foundry.RouteToOutput("error")
        }
        results[i] = result
        
        // Keep the session alive
        if err := node.Heartbeat(ctx); err != nil {
            log.Error("Heartbeat failed - context likely cancelled")
            return foundry.RouteToOutput("error")
        }
    }
    
    // Store combined result
    combined := strings.Join(results, "\n\n")
    node.StoreArtefact(ctx, "processed-output", "output.md", []byte(combined))
    
    return foundry.RouteToOutput("complete")
}
```

### Example: Periodic Heartbeat in Background

```go
func handler(ctx context.Context, node foundry.NodeContext, workitem *foundry.Workitem) foundry.Result {
    // Start background heartbeat ticker
    heartbeatCtx, cancel := context.WithCancel(ctx)
    defer cancel()
    
    go func() {
        ticker := time.NewTicker(15 * time.Second)
        defer ticker.Stop()
        
        for {
            select {
            case <-heartbeatCtx.Done():
                return
            case <-ticker.C:
                if err := node.Heartbeat(heartbeatCtx); err != nil {
                    log.Warn("Heartbeat failed", "err", err)
                    return
                }
            }
        }
    }()
    
    // Do long-running work without manual heartbeat calls
    result := doExpensiveComputation(ctx)
    
    return foundry.RouteToOutput("complete")
}
```

### Heartbeat Timeout Configuration

Configured in FoundryNode spec:

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: long-running-processor
spec:
  executionDeadline: "10m"  # 10 minute timeout
  heartbeatInterval: "30s"  # Expected heartbeat frequency
```

---

## 2. GetNodesByRole

Queries available nodes by role.

### Signature

```go
func (n *NodeContext) GetNodesByRole(
    ctx context.Context,
    role string,
) ([]string, error)
```

### Parameters

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `role` | `string` | Yes | Role to query (e.g., "security-reviewer") |

### Returns

| Type | Description |
|------|-------------|
| `[]string` | Node names with the specified role (only ready nodes) |

### Errors

| gRPC Code | Foundry Reason | Condition | Recovery |
|-----------|----------------|-----------|----------|
| `INVALID_ARGUMENT` | `INVALID_ROLE` | Empty role string | Provide valid role |

### Behavior

1. **Ready Filter:** Only returns nodes in `Ready` state.
2. **Live Query:** Queries Operator for current topology.
3. **May Be Empty:** Returns empty slice if no nodes have the role.

### Example: Check Reviewer Availability

```go
func handler(ctx context.Context, node foundry.NodeContext, workitem *foundry.Workitem) foundry.Result {
    // Check if security reviewers are available
    reviewers, err := node.GetNodesByRole(ctx, "security-reviewer")
    if err != nil {
        return foundry.RouteToOutput("error")
    }
    
    if len(reviewers) == 0 {
        log.Warn("No security reviewers available - escalating to HITL")
        return foundry.RouteToOutput("escalate-human")
    }
    
    log.Info("Security reviewers available", "count", len(reviewers), "nodes", reviewers)
    return foundry.RouteToOutput("review")
}
```

### Example: Load Balancing Decision

```go
func handler(ctx context.Context, node foundry.NodeContext, workitem *foundry.Workitem) foundry.Result {
    // Check capacity of different processor types
    fastProcessors, _ := node.GetNodesByRole(ctx, "fast-processor")
    thoroughProcessors, _ := node.GetNodesByRole(ctx, "thorough-processor")
    
    // Route based on availability and workitem priority
    if workitem.Priority == "critical" && len(fastProcessors) > 0 {
        return foundry.RouteToOutput("fast-track")
    }
    
    if len(thoroughProcessors) > 0 {
        return foundry.RouteToOutput("thorough")
    }
    
    // Fallback
    return foundry.RouteToOutput("default")
}
```

### Example: Dynamic Routing

```go
func handler(ctx context.Context, node foundry.NodeContext, workitem *foundry.Workitem) foundry.Result {
    // Get the required reviewer role from context
    requiredRole := workitem.Context["required_reviewer_role"]
    if requiredRole == "" {
        requiredRole = "general-reviewer"
    }
    
    reviewers, err := node.GetNodesByRole(ctx, requiredRole)
    if err != nil {
        return foundry.RouteToOutput("error")
    }
    
    if len(reviewers) == 0 {
        // Record why we're escalating
        node.RecordTelemetry(ctx, "foundry.node.escalation", map[string]interface{}{
            "reason":        "no_reviewers_available",
            "required_role": requiredRole,
        })
        return foundry.RouteToOutput("escalate")
    }
    
    // Route to the role-specific output
    return foundry.RouteToOutput(requiredRole + "-review")
}
```

---

## Role Reference

Roles are defined in `FoundryNode.spec.roles[]`:

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: security-review-node
spec:
  roles:
    - security-reviewer
    - compliance-checker
```

Common role conventions:

| Role Pattern | Description |
|--------------|-------------|
| `*-generator` | Content generation nodes |
| `*-reviewer` | Review/validation nodes |
| `*-processor` | Processing/transformation nodes |
| `*-approver` | Approval authority nodes |

---

## Health States

Nodes transition through these states:

| State | Description | GetNodesByRole |
|-------|-------------|----------------|
| `Starting` | Pod booting, sidecar initializing | Not included |
| `Ready` | Accepting workitems | **Included** |
| `Busy` | Processing a workitem | **Included** |
| `Draining` | Completing current work, not accepting new | Not included |
| `Unhealthy` | Failed health checks | Not included |
