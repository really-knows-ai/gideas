# SDK Reference: Telemetry Operations

> **Version:** 1.0.0  
> **Status:** Implementation Contract

This document defines the telemetry operations available in the Foundry Node SDK.

---

## 1. RecordTelemetry

Emits a custom telemetry event.

### Signature

```go
func (n *NodeContext) RecordTelemetry(
    ctx context.Context,
    eventType string,
    payload map[string]interface{},
) error
```

### Parameters

| Name | Type | Required | Constraints | Description |
|------|------|----------|-------------|-------------|
| `eventType` | `string` | Yes | Must start with `foundry.` | Event type identifier |
| `payload` | `map[string]interface{}` | Yes | JSON-serializable; max 64KB | Event data |

### Errors

| gRPC Code | Foundry Reason | Condition | Recovery |
|-----------|----------------|-----------|----------|
| `INVALID_ARGUMENT` | `INVALID_EVENT_TYPE` | Event type doesn't start with `foundry.` | Use correct prefix |
| `INVALID_ARGUMENT` | `PAYLOAD_TOO_LARGE` | Payload exceeds 64KB | Reduce payload size |
| `UNAVAILABLE` | `SERVICE_UNAVAILABLE` | Flow Monitor unavailable | Retry (non-critical) |

### Behavior

1. **Envelope:** Sidecar wraps payload with standard envelope (timestamp, flow_id, trace_id).
2. **Routing:** Event sent to Flow Monitor for distribution to subscribers.
3. **Non-Blocking:** Call returns immediately; delivery is async.

### Example: Custom Metrics

```go
err := node.RecordTelemetry(ctx, "foundry.node.custom_metric", map[string]interface{}{
    "operation":   "llm_call",
    "model":       "gpt-4",
    "tokens_in":   1500,
    "tokens_out":  800,
    "latency_ms":  2340,
})
if err != nil {
    log.Warn("Failed to record telemetry", "err", err)
    // Non-critical - continue processing
}
```

### Example: Business Events

```go
// Record business-level events for analytics
err := node.RecordTelemetry(ctx, "foundry.business.petition_created", map[string]interface{}{
    "petition_type": "feature_request",
    "complexity":    "medium",
    "requester":     workitem.Context["requestedBy"],
    "tags":          []string{"dashboard", "ux"},
})
```

### Example: Debug Tracing

```go
// Record detailed debug info (filtered in production)
err := node.RecordTelemetry(ctx, "foundry.debug.state_snapshot", map[string]interface{}{
    "step":           "post_validation",
    "feedback_count": len(workitem.Feedback),
    "artefact_count": len(workitem.Artefacts),
    "context_keys":   getKeys(workitem.Context),
})
```

---

## 2. ReportFriction

Reports application friction for the Flow Monitor.

### Signature

```go
func (n *NodeContext) ReportFriction(
    ctx context.Context,
    value float64,
    op FrictionOp,
    reason string,
    attribution string,  // "node-logic" | "external-api" | "infrastructure"
    tags map[string]string,
) error
```

### Parameters

```go
type FrictionOp int
const (
    FrictionLog FrictionOp = iota  // Score += log(1 + value)
    FrictionAdd                     // Score += value
    FrictionMultiply                // Score *= value
    FrictionSet                     // Score = value
)
```

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `value` | `float64` | Yes | Friction magnitude (must be positive) |
| `op` | `FrictionOp` | Yes | Aggregation operation |
| `reason` | `string` | Yes | Human-readable explanation |
| `attribution` | `string` | Yes | Source: "node-logic", "external-api", or "infrastructure" |
| `tags` | `map[string]string` | No | Arbitrary dimensions for analysis |

### Errors

| gRPC Code | Foundry Reason | Condition | Recovery |
|-----------|----------------|-----------|----------|
| `INVALID_ARGUMENT` | `INVALID_VALUE` | Value is negative or NaN | Use positive value |

### Operation Semantics

| Operation | Formula | Use Case |
|-----------|---------|----------|
| `FrictionLog` | `Score += log(1 + value)` | Diminishing returns; many small frictions |
| `FrictionAdd` | `Score += value` | Linear accumulation; countable issues |
| `FrictionMultiply` | `Score *= value` | Severity multiplier; compounding issues |
| `FrictionSet` | `Score = value` | Override; reset friction state |

### Example: LLM Retries

```go
// Report friction from LLM retry loop
retries := 0
for retries < maxRetries {
    result, err := callLLM(ctx, prompt)
    if err == nil && isValid(result) {
        break
    }
    retries++
}

if retries > 0 {
    err := node.ReportFriction(ctx,
        float64(retries),
        foundry.FrictionAdd,
        fmt.Sprintf("LLM required %d retries to produce valid output", retries),
        map[string]string{
            "model":      "gpt-4",
            "phase":      "generation",
            "error_type": "validation_failure",
        },
    )
    if err != nil {
        log.Warn("Failed to report friction", "err", err)
    }
}
```

### Example: External API Latency

```go
start := time.Now()
result, err := callExternalAPI(ctx, request)
latency := time.Since(start)

// Report friction if latency exceeds threshold
if latency > 5*time.Second {
    frictionValue := latency.Seconds() / 5.0  // 1.0 at 5s, 2.0 at 10s, etc.
    
    err := node.ReportFriction(ctx,
        frictionValue,
        foundry.FrictionLog,  // Diminishing returns for slow APIs
        fmt.Sprintf("External API latency: %v", latency),
        map[string]string{
            "api":        "github",
            "endpoint":   "/repos/search",
            "latency_ms": strconv.Itoa(int(latency.Milliseconds())),
        },
    )
}
```

### Example: Complexity Multiplier

```go
// Use multiplier for compounding complexity
cyclomaticComplexity := analyzeComplexity(code)

if cyclomaticComplexity > 10 {
    multiplier := 1.0 + (float64(cyclomaticComplexity-10) * 0.1)  // 1.1 at 11, 1.2 at 12, etc.
    
    err := node.ReportFriction(ctx,
        multiplier,
        foundry.FrictionMultiply,
        fmt.Sprintf("High cyclomatic complexity: %d", cyclomaticComplexity),
        map[string]string{
            "metric":     "cyclomatic_complexity",
            "value":      strconv.Itoa(cyclomaticComplexity),
            "threshold":  "10",
        },
    )
}
```

---

## Standard Event Types

See [flow_spec/11_telemetry_catalog.md](../../flow_spec/11_telemetry_catalog.md) for the complete event catalog.

### Node Events

| Event Type | Description |
|------------|-------------|
| `foundry.node.custom_metric` | Custom node metrics |
| `foundry.node.state_change` | Node state transitions |

### Business Events

| Event Type | Description |
|------------|-------------|
| `foundry.business.*` | Domain-specific business events |

### Debug Events

| Event Type | Description |
|------------|-------------|
| `foundry.debug.*` | Debug/trace events (filtered in prod) |

---

## Friction Tag Conventions

Recommended tags for friction reports:

| Tag | Description | Example Values |
|-----|-------------|----------------|
| `model` | LLM model used | `gpt-4`, `claude-3` |
| `phase` | Processing phase | `generation`, `validation`, `review` |
| `law_id` | Related law ID | `f-105`, `s-001` |
| `error_type` | Error classification | `timeout`, `validation_failure` |
| `api` | External API | `github`, `jira` |
| `component` | System component | `librarian`, `archivist` |

---

## Best Practices

### 1. Always Report Retries

```go
// Good: Report all retry scenarios
for attempt := 1; attempt <= maxAttempts; attempt++ {
    result, err := doOperation()
    if err == nil {
        if attempt > 1 {
            node.ReportFriction(ctx, float64(attempt-1), foundry.FrictionAdd,
                "Operation required retries", nil)
        }
        break
    }
}
```

### 2. Use Appropriate Operations

```go
// FrictionLog for many small frictions
node.ReportFriction(ctx, 1.0, foundry.FrictionLog, "Minor validation warning", nil)

// FrictionAdd for countable issues
node.ReportFriction(ctx, 1.0, foundry.FrictionAdd, "Security issue found", nil)

// FrictionMultiply for severity
node.ReportFriction(ctx, 2.0, foundry.FrictionMultiply, "Critical blocker", nil)
```

### 3. Include Rich Tags

```go
// Good: Rich context for analysis
node.ReportFriction(ctx, 5.0, foundry.FrictionAdd, "LLM hallucination", map[string]string{
    "model":       "gpt-4",
    "phase":       "code_generation",
    "law_id":      "f-105",
    "retry_count": "3",
    "error_type":  "format_violation",
})

// Bad: No context
node.ReportFriction(ctx, 5.0, foundry.FrictionAdd, "Error", nil)
```
