# Foundry Node: SDK Core Contract

## 1. The Handler Contract

Nodes must implement a handler that receives work and returns a routing decision:

```go
type NodeHandler interface {
    // Called when a Workitem is assigned to this node
    Assigned(ctx context.Context, workitem Workitem) Result
}
```

The handler is called asynchronously after `ProcessWorkitem` RPC. The node processes work, makes SDK calls as needed, then returns a `Result` via `CompleteWorkitem` RPC.

### 1.1 Workitem Struct (SDK View)

The SDK's `Workitem` struct is a **filtered view** of the full CRD. Some fields are hidden to enforce separation of concerns:

```go
type Workitem struct {
    Metadata WorkitemMetadata
    Spec     WorkitemSpec
    Status   WorkitemStatus
}

type WorkitemStatus struct {
    State            string
    CurrentAssignee  string
    PreviousAssignee string
    Artefacts        []ArtefactMetadata
    Feedback         []Feedback           // Threaded, artefact-scoped
    // NOTE: Guestbook is NOT exposed - hidden from node logic
}

type Feedback struct {
    ID            string
    Target        string            // Artefact being critiqued (required)
    Source        string
    Severity      string
    State         string
    Message       string
    History       []FeedbackEvent   // Append-only dispute log
    Justification *Justification    // For wont-fix
    LinkedRuling  string            // For rejected/resolved
}

type FeedbackEvent struct {
    Timestamp string
    Author    string
    Role      string
    Action    string  // opened | fixed | rejected | wont-fix | escalated
    Message   string
}
```

**Hidden Fields:** The `guestbook` (visit counts) is stripped by the Sidecar before passing to the Node. This enforces:
- Infrastructure concerns (thrash detection) stay in the Sidecar
- Application concerns (semantic fatigue) use `feedback.history` depth

---

## 2. The Result Object API

The `Result` object encapsulates routing instructions returned from Node handlers.

### 2.1 Result Types

| Method | Use Case | Validation |
|--------|----------|------------|
| `RouteToOutput(name)` | Route via named output channel | Output must exist in Node's `outputs` config |
| `RouteTo(node)` | Direct routing to specific node | Node must exist |
| `Complete()` | Signal terminal completion | Node must have `isTerminal: true` |

### 2.2 API Definition

```go
type Result struct {
    routeToOutput string
    routeTo       string
    complete      bool
    TimedOut      bool  // Set when context cancelled due to timeout
}

func RouteToOutput(outputName string) Result {
    return Result{routeToOutput: outputName}
}

func RouteTo(nodeName string) Result {
    return Result{routeTo: nodeName}
}

func Complete() Result {
    return Result{complete: true}
}
```

### 2.3 Routing Flow

```
Node returns RouteToOutput("done")
        │
        ▼
┌─────────────────────────────────────────┐
│ Sidecar Validates                       │
│ - "done" exists in Node's outputs?      │
│ - YES: Write routingInstruction to CRD  │
│ - NO: Return error to Node              │
└─────────────────────────────────────────┘
        │
        ▼
┌─────────────────────────────────────────┐
│ Operator Processes                      │
│ - Resolve targets from outputs config   │
│ - Select ready node (distribution)      │
│ - Update currentAssignee                │
└─────────────────────────────────────────┘
        │
        ▼
    Next Node

### 2.4 Terminal Completion Semantics

When a handler returns `Complete()`, the Sidecar performs terminal contract validation synchronously. On success, it writes terminal completion to the Workitem and returns success to the handler. On failure, it returns `TERMINAL_CONTRACT_VIOLATED` with `ContractDetails`. There is no asynchronous contract check after the handler returns.
```

---

## 3. Context Injection Contract

The Sidecar manages context for the current workitem session. The SDK must:

1. Extract `workitem_id` from the `Assigned` event payload at session start.
2. Store it in context for all subsequent SDK calls.
3. Pass context to all SDK methods for telemetry attribution and permission enforcement.

```go
func (n *MyNode) Assigned(ctx context.Context, workitem Workitem) Result {
    // ctx contains workitem_id, node identity, capabilities
    n.Stamp(ctx, "python-source", "main.py", "security-cleared")
    n.Cite(ctx, lawID)
    n.RecordTelemetry(ctx, event)
    
    return RouteToOutput("default")
}
```

---

## 4. Error Handling

There is **no built-in error routing** for SDK call failures. The Node must explicitly handle errors and return a routing decision.

**Design Rationale:** The developer must decide what "failure" means in their business context. Is a failed `Stamp` a retryable transient issue or a fatal compliance breach? Only the domain logic knows.

### 4.1 Error Handling Pattern

```go
func (n *MyNode) Assigned(ctx context.Context, workitem Workitem) Result {
    content, err := n.FetchArtefact(ctx, "petition-draft", "draft.md")
    if err != nil {
        if foundry.IsNotFound(err) {
            return RouteToOutput("error")
        }
        if foundry.IsTemporary(err) {
            return RouteToOutput("retry")
        }
        return RouteToOutput("fail")
    }
    
    _, err = n.StoreArtefact(ctx, "petition-draft", "draft.md", newContent)
    if err != nil {
        return RouteToOutput("error")
    }
    
    return RouteToOutput("pass")
}
```

### 4.2 Common Error Codes

| Code | Meaning | Typical Response |
|------|---------|------------------|
| `PERMISSION_DENIED` | Missing capability in CRD | Fatal - configuration error |
| `NOT_FOUND` | Artefact/Law doesn't exist | Check logic or route to error |
| `RESOURCE_EXHAUSTED` | Archivist full or rate-limited | Backoff and retry |
| `CONFLICT_DETECTED` | Law citation conflicts | Route to human review |
| `INVALID_ARGUMENT` | Malformed request | Fatal - code bug |
| `CONTEMPT_VIOLATION` | Attempting wont-fix on judicial mandate | Route to compliance review |

### 4.3 Unhandled Panics

If your handler crashes (panics) without returning a `Result`:
1. The Sidecar detects the handler goroutine death
2. The Workitem remains in `Running` state
3. The **Reaper Loop** eventually times out and either escalates (if `timeout` output exists) or fails the workitem

**Best Practice:** Always wrap handler logic in a recover block or use the SDK's `SafeHandler` wrapper (if available) to convert panics into explicit error routes.

---

## 5. Related Documents

- [04a_sdk_patterns.md](./04a_sdk_patterns.md) - Sort Node pattern, common SDK methods, HITL base classes
