# SDK Reference: Workitem Operations

> **Version:** 1.0.0  
> **Status:** Implementation Contract

This document defines the workitem operations available in the Foundry Node SDK.

---

## 1. CreateWorkitem

Creates a new Workitem (proactive node capability).

### Signature

```go
func (n *NodeContext) CreateWorkitem(
    ctx context.Context,
    spec *WorkitemSpec,
) (string, error)
```

### Parameters

```go
type WorkitemSpec struct {
    Type        string            // WorkitemType name
    Intent      string            // Human-readable purpose
    Priority    string            // "low" | "medium" | "high" | "critical"
    Context     map[string]string // Initial context data
    RequestedBy string            // Source identifier
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `Type` | `string` | Yes | WorkitemType CRD name |
| `Intent` | `string` | Yes | Human-readable purpose |
| `Priority` | `string` | No | Default: "medium" |
| `Context` | `map[string]string` | No | Initial context key-values |
| `RequestedBy` | `string` | No | Source identifier |

### Returns

| Type | Description |
|------|-------------|
| `string` | New Workitem ID |

### Errors

| gRPC Code | Foundry Reason | Condition | Recovery |
|-----------|----------------|-----------|----------|
| `PERMISSION_DENIED` | `CAPABILITY_DENIED` | Node lacks `WRITE:workitem` | Add capability to FoundryNode |
| `INVALID_ARGUMENT` | `INVALID_TYPE` | WorkitemType doesn't exist | Create WorkitemType CRD |
| `INVALID_ARGUMENT` | `INVALID_PRIORITY` | Unknown priority value | Use valid priority |

### Behavior

1. **CRD Creation:** Operator creates Workitem CRD with initial state `Pending`.
2. **Routing:** Workitem enters the flow at `WorkitemType.spec.entryNode`.
3. **Context Copy:** `spec.Context` is copied to `workitem.status.context`.

### Example: Scheduled Task Node

```go
// A node that creates workitems on a schedule
func handler(ctx context.Context, node foundry.NodeContext, _ *foundry.Workitem) foundry.Result {
    // Check if it's time to create daily report
    if !shouldCreateDailyReport() {
        return foundry.RouteToOutput("skip")
    }
    
    workitemID, err := node.CreateWorkitem(ctx, &foundry.WorkitemSpec{
        Type:     "daily-report",
        Intent:   "Generate daily metrics report for " + time.Now().Format("2006-01-02"),
        Priority: "medium",
        Context: map[string]string{
            "report_date": time.Now().Format("2006-01-02"),
            "report_type": "metrics",
        },
        RequestedBy: "scheduled-task-node",
    })
    if err != nil {
        log.Error("Failed to create workitem", "err", err)
        return foundry.RouteToOutput("error")
    }
    
    log.Info("Created daily report workitem", "id", workitemID)
    return foundry.RouteToOutput("created")
}
```

### Example: Splitting Workitems

```go
// A node that splits a large task into smaller workitems
func handler(ctx context.Context, node foundry.NodeContext, workitem *foundry.Workitem) foundry.Result {
    files := strings.Split(workitem.Context["files"], ",")
    
    for _, file := range files {
        _, err := node.CreateWorkitem(ctx, &foundry.WorkitemSpec{
            Type:     "file-review",
            Intent:   "Review file: " + file,
            Priority: workitem.Priority,
            Context: map[string]string{
                "parent_workitem": workitem.ID,
                "file":            file,
            },
        })
        if err != nil {
            log.Error("Failed to create sub-workitem", "file", file, "err", err)
        }
    }
    
    return foundry.RouteToOutput("split")
}
```

---

## 2. UpdateContext

Updates the workitem's `status.context` metadata. This is the **only way** for a Node to add or modify context after creation.

### Signature

```go
func (n *NodeContext) UpdateContext(
    ctx context.Context,
    updates map[string]string,
    overwrite bool,
) error
```

### Parameters

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `updates` | `map[string]string` | Yes | Key-value pairs to add/update |
| `overwrite` | `bool` | No | If `true`, replace entire context. If `false`, merge with existing. |

### Behavior

- **Merge Mode (default):** New keys are added, existing keys are updated.
- **Overwrite Mode:** Entire `status.context` is replaced with `updates`.
- **Reserved Keys:** Keys starting with `_` are reserved for system use and cannot be set.

### Example: Adding Processing Metadata

```go
func handler(ctx context.Context, node foundry.NodeContext, workitem *foundry.Workitem) foundry.Result {
    // Process the workitem...
    result := processData(workitem)
    
    // Add processing metadata to context
    err := node.UpdateContext(ctx, map[string]string{
        "processed_at":     time.Now().Format(time.RFC3339),
        "processor_node":   node.Name(),
        "result_summary":   result.Summary,
    }, false) // merge mode
    if err != nil {
        log.Error("Failed to update context", "err", err)
    }
    
    return foundry.RouteToOutput("processed")
}
```

### Example: Tracking Foreign Source (Import)

```go
// Used by Ingress nodes after importing a bundle
err := node.UpdateContext(ctx, map[string]string{
    "foreignSource.flow":       bundle.SourceFlow,
    "foreignSource.workitemId": bundle.SourceWorkitemID,
    "foreignSource.exportedAt": bundle.ExportedAt,
}, false)
```

---

## 3. GetWorkitem

Returns the current Workitem being processed.

### Signature

```go
func (n *NodeContext) GetWorkitem() *Workitem
```

### Returns

```go
type Workitem struct {
    ID               string
    Type             string
    Intent           string
    Priority         string
    State            string  // "Pending" | "Running" | "Completed" | "Failed"
    CurrentAssignee  string
    PreviousAssignee string
    Artefacts        []ArtefactRef
    Feedback         []FeedbackItem
    Context          map[string]string
}

type ArtefactRef struct {
    Kind          string
    Name          string
    LatestVersion string
    Passport      []Stamp
}
```

### Behavior

- Returns a **snapshot** of the workitem at handler invocation time.
- Does **not** reflect changes made during handler execution.
- Use `FetchArtefact` / `GetArtefactMetadata` for current artefact state.

### Example: Reading Workitem Context

```go
func handler(ctx context.Context, node foundry.NodeContext, workitem *foundry.Workitem) foundry.Result {
    // Access workitem metadata
    log.Info("Processing workitem",
        "id", workitem.ID,
        "type", workitem.Type,
        "intent", workitem.Intent,
        "priority", workitem.Priority,
    )
    
    // Access context data
    targetLanguage := workitem.Context["target_language"]
    if targetLanguage == "" {
        targetLanguage = "python"  // Default
    }
    
    // Check existing artefacts
    for _, art := range workitem.Artefacts {
        log.Info("Has artefact", "kind", art.Kind, "version", art.LatestVersion)
    }
    
    // Check pending feedback
    pendingCount := 0
    for _, fb := range workitem.Feedback {
        if fb.State == "pending" {
            pendingCount++
        }
    }
    
    if pendingCount > 0 {
        return foundry.RouteToOutput("needs-fixes")
    }
    
    return foundry.RouteToOutput("ready")
}
```

### Example: Using Previous Assignee

```go
func handler(ctx context.Context, node foundry.NodeContext, workitem *foundry.Workitem) foundry.Result {
    // Check who sent this workitem
    if workitem.PreviousAssignee == "security-review-node" {
        // Coming from security review - apply strict checks
        return handlePostSecurityReview(ctx, node, workitem)
    }
    
    // Normal flow
    return handleNormalFlow(ctx, node, workitem)
}
```

---

## Priority Reference

| Priority | Description | Scheduling Behavior |
|----------|-------------|---------------------|
| `low` | Background task | Processed when resources available |
| `medium` | Standard work | Normal queue priority |
| `high` | Important work | Processed before medium/low |
| `critical` | Urgent work | Immediate processing; may preempt |

---

## Workitem Lifecycle

```
┌─────────────────────────────────────────────────────────────────────┐
│                         WORKITEM LIFECYCLE                          │
└─────────────────────────────────────────────────────────────────────┘

     CreateWorkitem()
           │
           ▼
     ┌─────────┐
     │ Pending │──────────────────────────────────────┐
     └────┬────┘                                      │
          │ Operator assigns to node                  │
          ▼                                           │
     ┌─────────┐                                      │
     │ Running │◄─────────────────────────────────────┤
     └────┬────┘                                      │
          │                                           │
          ├── RouteToOutput("x") ──► Pending (next node)
          │
          ├── RouteTo("node-name") ─► Pending (specific node)
          │
          ├── Complete() ──────────► Completed ✓
          │
          └── Timeout / Error ─────► Failed ✗
```

---

## Context Key Conventions

Reserved context keys (set by system):

| Key | Description |
|-----|-------------|
| `_created_at` | ISO timestamp of creation |
| `_created_by` | Node that created the workitem |
| `_parent_id` | Parent workitem ID (if split) |
| `_retry_count` | Number of retries after failure |

Recommended application keys:

| Key Pattern | Description |
|-------------|-------------|
| `{domain}_*` | Domain-specific context (e.g., `review_file`, `audit_scope`) |
| `source_*` | Source information (e.g., `source_repo`, `source_branch`) |
| `target_*` | Target information (e.g., `target_environment`) |
