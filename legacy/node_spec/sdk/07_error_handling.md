# SDK Reference: Error Handling

> **Version:** 1.0.0  
> **Status:** Implementation Contract

This document defines error handling patterns and constants for the Foundry Node SDK.

---

## 1. Error Checking

### IsError

Check if an error matches a specific Foundry error type.

```go
func IsError(err error, code string) bool
```

### Example

```go
_, err := node.StoreArtefact(ctx, "petition-draft", "draft.md", content)
if err != nil {
    if foundry.IsError(err, foundry.CAPABILITY_DENIED) {
        log.Error("Node not authorized to write this artefact type")
        return foundry.RouteToOutput("error")
    }
    if foundry.IsError(err, foundry.ARTEFACT_TOO_LARGE) {
        log.Error("Content exceeds size limit")
        return foundry.RouteToOutput("error")
    }
    // Unknown error
    log.Error("Unexpected error", "err", err)
    return foundry.RouteToOutput("error")
}
```

---

## 2. Retryable Errors

### IsRetryable

Check if an error is transient and can be retried.

```go
func IsRetryable(err error) bool
```

### Retryable Error Codes

| Code | Retry Strategy |
|------|----------------|
| `UNAVAILABLE` | Exponential backoff: 100ms → 200ms → 400ms → ... (max 30s) |
| `RESOURCE_EXHAUSTED` | Exponential backoff with jitter |
| `DEADLINE_EXCEEDED` | Retry once with extended timeout |

### Example

```go
func storeWithRetry(ctx context.Context, node foundry.NodeContext, kind, name string, content []byte) error {
    var lastErr error
    backoff := 100 * time.Millisecond
    
    for attempt := 1; attempt <= 5; attempt++ {
        _, err := node.StoreArtefact(ctx, kind, name, content)
        if err == nil {
            return nil
        }
        
        lastErr = err
        if !foundry.IsRetryable(err) {
            return err  // Non-retryable - fail immediately
        }
        
        log.Warn("Retrying store operation", "attempt", attempt, "backoff", backoff)
        time.Sleep(backoff)
        backoff *= 2
        if backoff > 30*time.Second {
            backoff = 30 * time.Second
        }
    }
    
    return fmt.Errorf("max retries exceeded: %w", lastErr)
}
```

---

## 3. Structured Error Details

Some errors include additional context accessible via typed getters.

### GetContractDetails

For `TERMINAL_CONTRACT_VIOLATED` errors from `Complete()`.

```go
type ContractDetails struct {
    MissingArtefacts []string
    MissingRoles     []string
}

func GetContractDetails(err error) *ContractDetails
```

### GetTargetDetails

For `NO_AVAILABLE_TARGET` async errors.

```go
type TargetDetails struct {
    RequestedRole  string
    AvailableNodes []string
}
```

---

## 4. Error Constants

### Permission Errors

```go
const (
    PERMISSION_DENIED  = "PERMISSION_DENIED"   // Generic permission error
    CAPABILITY_DENIED  = "CAPABILITY_DENIED"   // Missing SDK capability
    CONTEMPT_VIOLATION = "CONTEMPT_VIOLATION"  // Violating judicial mandate
)
```

### Input Validation Errors

```go
const (
    INVALID_ARGUMENT       = "INVALID_ARGUMENT"       // Generic input error
    INVALID_OUTPUT         = "INVALID_OUTPUT"         // Output name not in node config
    INVALID_KIND           = "INVALID_KIND"           // Artefact kind not registered
    INVALID_SEVERITY       = "INVALID_SEVERITY"       // Unknown severity value
    INVALID_PRIORITY       = "INVALID_PRIORITY"       // Unknown priority value
    INVALID_STATE          = "INVALID_STATE"          // Invalid state transition
    INVALID_EVENT_TYPE     = "INVALID_EVENT_TYPE"     // Bad telemetry event type
    INVALID_VALUE          = "INVALID_VALUE"          // Invalid numeric value
    INVALID_ROLE           = "INVALID_ROLE"           // Empty or invalid role
    AMBIGUOUS_ROLE         = "AMBIGUOUS_ROLE"         // Multiple roles, none specified
    MESSAGE_TOO_LONG       = "MESSAGE_TOO_LONG"       // Feedback message > 1024 chars
    STATEMENT_TOO_LONG     = "STATEMENT_TOO_LONG"     // Law statement > 1024 chars
    ARTEFACT_TOO_LARGE     = "ARTEFACT_TOO_LARGE"     // Content exceeds size limit
    PAYLOAD_TOO_LARGE      = "PAYLOAD_TOO_LARGE"      // Telemetry payload > 64KB
    JUSTIFICATION_REQUIRED = "JUSTIFICATION_REQUIRED" // wont-fix without justification
    QUERY_EMPTY            = "QUERY_EMPTY"            // Search with no query params
)
```

### Not Found Errors

```go
const (
    NOT_FOUND          = "NOT_FOUND"          // Generic not found
    ARTEFACT_NOT_FOUND = "ARTEFACT_NOT_FOUND" // Artefact not in workitem
    VERSION_NOT_FOUND  = "VERSION_NOT_FOUND"  // Specific version doesn't exist
    LAW_NOT_FOUND      = "LAW_NOT_FOUND"      // Law ID doesn't exist
    FEEDBACK_NOT_FOUND = "FEEDBACK_NOT_FOUND" // Feedback ID doesn't exist
)
```

### Conflict Errors

```go
const (
    ALREADY_EXISTS    = "ALREADY_EXISTS"    // Generic duplicate
    DUPLICATE_FINDING = "DUPLICATE_FINDING" // Semantically similar law exists
)
```

### State Errors

```go
const (
    FAILED_PRECONDITION = "FAILED_PRECONDITION" // System not in required state
    LAW_EXPIRED         = "LAW_EXPIRED"         // Law has expired
)
```

### Infrastructure Errors

```go
const (
    UNAVAILABLE         = "UNAVAILABLE"         // Service temporarily unavailable
    SERVICE_UNAVAILABLE = "SERVICE_UNAVAILABLE" // Specific service down
    RESOURCE_EXHAUSTED  = "RESOURCE_EXHAUSTED"  // Rate limited or quota exceeded
    STORAGE_FULL        = "STORAGE_FULL"        // PVC at capacity
    RATE_LIMITED        = "RATE_LIMITED"        // Rate limit exceeded
    CONTEXT_CANCELLED   = "CONTEXT_CANCELLED"   // Handler context cancelled
)
```

### Data Integrity Errors

```go
const (
    DATA_LOSS          = "DATA_LOSS"          // Corruption detected
    ARTEFACT_CORRUPTED = "ARTEFACT_CORRUPTED" // Hash mismatch on fetch
)
```

---

## 5. gRPC Code Mapping

| Foundry Reason | gRPC Code |
|----------------|-----------|
| `PERMISSION_DENIED`, `CAPABILITY_DENIED`, `CONTEMPT_VIOLATION` | `PERMISSION_DENIED` (7) |
| `INVALID_*`, `*_TOO_LONG`, `*_TOO_LARGE`, `*_REQUIRED` | `INVALID_ARGUMENT` (3) |
| `*_NOT_FOUND` | `NOT_FOUND` (5) |
| `ALREADY_EXISTS`, `DUPLICATE_FINDING` | `ALREADY_EXISTS` (6) |
| `FAILED_PRECONDITION`, `LAW_EXPIRED`, `INVALID_STATE` | `FAILED_PRECONDITION` (9) |
| `UNAVAILABLE`, `SERVICE_UNAVAILABLE` | `UNAVAILABLE` (14) |
| `RESOURCE_EXHAUSTED`, `STORAGE_FULL` | `RESOURCE_EXHAUSTED` (8) |
| `DATA_LOSS`, `ARTEFACT_CORRUPTED` | `DATA_LOSS` (15) |
| `CONTEXT_CANCELLED` | `DEADLINE_EXCEEDED` (4) |

---

## 6. Error Handling Patterns

### Pattern: Graceful Degradation

```go
func handler(ctx context.Context, node foundry.NodeContext, workitem *foundry.Workitem) foundry.Result {
    // Try to fetch existing artefact
    content, err := node.FetchArtefact(ctx, "petition-draft")
    if err != nil {
        if foundry.IsError(err, foundry.ARTEFACT_NOT_FOUND) {
            // Expected for new workitems - create initial version
            content = generateInitialDraft(workitem)
        } else {
            // Unexpected error - fail
            return foundry.RouteToOutput("error")
        }
    }
    
    // Continue processing...
}
```

### Pattern: Circuit Breaker

```go
var (
    libraryFailures int
    libraryCircuitOpen bool
    libraryCircuitOpenUntil time.Time
)

func searchWithCircuitBreaker(ctx context.Context, node foundry.NodeContext, query *foundry.LibraryQuery) ([]*foundry.Law, error) {
    // Check circuit breaker
    if libraryCircuitOpen && time.Now().Before(libraryCircuitOpenUntil) {
        return nil, fmt.Errorf("circuit breaker open")
    }
    
    laws, err := node.SearchLibrary(ctx, query)
    if err != nil {
        if foundry.IsError(err, foundry.SERVICE_UNAVAILABLE) {
            libraryFailures++
            if libraryFailures >= 5 {
                libraryCircuitOpen = true
                libraryCircuitOpenUntil = time.Now().Add(30 * time.Second)
            }
        }
        return nil, err
    }
    
    // Reset on success
    libraryFailures = 0
    libraryCircuitOpen = false
    return laws, nil
}
```

### Pattern: Error Aggregation

```go
func handler(ctx context.Context, node foundry.NodeContext, workitem *foundry.Workitem) foundry.Result {
    var errs []error
    
    // Try multiple operations, collect errors
    if _, err := node.StoreArtefact(ctx, "draft", "draft.md", draftContent); err != nil {
        errs = append(errs, fmt.Errorf("store draft: %w", err))
    }
    
    if _, err := node.RecordFinding(ctx, statement, labels); err != nil && 
       !foundry.IsError(err, foundry.DUPLICATE_FINDING) {
        errs = append(errs, fmt.Errorf("record finding: %w", err))
    }
    
    if len(errs) > 0 {
        log.Error("Multiple errors occurred", "count", len(errs), "errors", errs)
        return foundry.RouteToOutput("error")
    }
    
    return foundry.RouteToOutput("success")
}
```

---

## 7. Complete Error Reference

See [flow_spec/12_error_catalog.md](../../flow_spec/12_error_catalog.md) for:
- All error codes with descriptions
- Source components for each error
- Detailed recovery strategies
- Async vs sync error classification
