# Foundry Node: SDK Patterns & Methods

## 1. The Sort Node Pattern (Smart Gate)

The **Sort Node** is a common pattern for controlling flow through the graph. It acts as a "Smart Gate," checking feedback state and history depth to decide routing.

**Design:** Sort Nodes detect semantic fatigue using `feedback.history` depth, NOT visit counts. The `guestbook` is hidden from nodes - it's used internally by the Sidecar for infrastructure thrash detection only.

```go
type SortNode struct {
    maxFeedbackDepth int  // e.g., 3
}

func (n *SortNode) Assigned(ctx context.Context, item Workitem) Result {
    // Check for semantic fatigue (history depth exceeded)
    for _, fb := range filterFeedback(item.Status.Feedback, "pending") {
        if len(fb.History) > n.maxFeedbackDepth {
            // Too many back-and-forth rejections - escalate to judiciary
            n.UpdateFeedbackState(ctx, fb.ID, "disputed")
            return RouteToOutput("deadlock")
        }
    }
    
    // Check if any feedback is still pending
    pendingFeedback := filterFeedback(item.Status.Feedback, "pending")
    if len(pendingFeedback) > 0 {
        // Unresolved feedback - route to Refine
        return RouteToOutput("refine")
    }
    
    // All clear - proceed to next stage
    return RouteToOutput("done")
}
```

---

## 2. Common SDK Methods

```go
// Artefact operations
func (n *Node) StoreArtefact(ctx context.Context, kind, name string, content []byte) (version string, error)
func (n *Node) FetchArtefact(ctx context.Context, kind, name string) ([]byte, error)
func (n *Node) FetchArtefactVersion(ctx context.Context, kind, name, version string) ([]byte, error)
func (n *Node) GetArtefactMetadata(ctx context.Context, kind, name string) (ArtefactMetadata, error)

// Stamping (stamps latest version)
func (n *Node) Stamp(ctx context.Context, kind, name, comment string) error

// Law citation
func (n *Node) Cite(ctx context.Context, lawID string) error

// Telemetry
func (n *Node) RecordTelemetry(ctx context.Context, event TelemetryEvent) error

// Friction Reporting (with tags for Prometheus dimensions)
func (n *Node) RecordFriction(ctx context.Context, value float64, op FrictionOp, reason string, tags map[string]string) error

// Heartbeat (manual, for long operations without FoundryAgent)
func (n *Node) Heartbeat(ctx context.Context) error

// Feedback (threaded, artefact-scoped)
// Message limit: 1024 chars. Returns INVALID_ARGUMENT if exceeded.
func (n *Node) AddFeedback(ctx context.Context, target, message string, severity Severity) (feedbackID string, error)
func (n *Node) ResolveFeedback(ctx context.Context, feedbackID, message string) error  // Appends "fixed" to history
func (n *Node) RejectFix(ctx context.Context, feedbackID, message string) error        // Appends "rejected" to history
func (n *Node) UpdateFeedbackState(ctx context.Context, feedbackID string, state string) error

// Optional mid-flow validation
func (n *Node) ValidateArtefact(ctx context.Context, artefactKind string, requiredState string) (bool, error)
```

### 2.1 FrictionOp Enum

```go
type FrictionOp int

const (
    FRICTION_LOG      FrictionOp = iota  // Logarithmic: diminishing returns
    FRICTION_ADD                          // Adds value to friction score
    FRICTION_MULTIPLY                     // Multiplies friction score by value
)
```

> **Note:** The `SET` operation exists at the protocol level but is not exposed in the Node SDK. It is reserved for system-level operations.

### 2.2 RecordFriction Examples

```go
// Report friction during a retry loop
for attempts := 0; attempts < 5; attempts++ {
    result, err := n.attemptRefinement(ctx)
    if err == nil {
        return RouteToOutput("success")
    }
    n.RecordFriction(ctx, 10.0, FRICTION_ADD, "Refinement attempt failed", map[string]string{
        "attempt": strconv.Itoa(attempts),
    })
}
n.RecordFriction(ctx, 2.0, FRICTION_MULTIPLY, "Refinement loop exhausted", nil)
return RouteToOutput("fail")
```

```go
// Attribute friction to a specific law
laws, _ := n.SearchLibrary(ctx, query)
for _, law := range laws {
    if !checkCompliance(content, law) {
        n.RecordFriction(ctx, 5.0, FRICTION_ADD, "Law compliance check failed", map[string]string{
            "law_id":     law.Metadata.Name,
            "law_tier":   law.Labels["tier"],
            "check_type": "format-validation",
        })
    }
}
```

---

## 3. Store & Link Pattern (Large Feedback Content)

Feedback messages are limited to **1024 characters** to prevent etcd bloat. For detailed analysis reports, stack traces, or extensive justifications, use the "Store & Link" pattern:

```go
// ❌ WRONG: Embedding large content directly
n.AddFeedback(ctx, "source.py", hugeStackTrace, SEVERITY_HIGH)  // Returns INVALID_ARGUMENT

// ✅ CORRECT: Store in Archivist, link in feedback
_, err := n.StoreArtefact(ctx, "error-report", "analysis.md", []byte(hugeStackTrace))
if err != nil {
    return RouteToOutput("error")
}
n.AddFeedback(ctx, "source.py", "Validation failed. See 'analysis.md' for details.", SEVERITY_HIGH)
```

**Why the limit?** Kubernetes etcd enforces a 1.5MB limit per CRD. With multiple feedback items and history events, verbose messages can push Workitems toward this ceiling.

---

## 4. ValidateArtefact (Optional Mid-Flow Check)

Nodes can optionally validate artefacts mid-flow without requiring a terminal contract:

```go
func (n *SortNode) Assigned(ctx context.Context, item Workitem) Result {
    valid, err := n.ValidateArtefact(ctx, "petition-draft", "valid")
    if err != nil {
        return RouteToOutput("fail")
    }
    
    if !valid {
        return RouteToOutput("fail")
    }
    
    return RouteToOutput("pass")
}
```

This performs the same GovernedArtefact → Passport validation as the Terminal Guard, but without requiring terminal completion.

**Parameters:**
- `artefactKind`: The name of the GovernedArtefact CRD
- `requiredState`: Either `"present"` (exists) or `"valid"` (all required stamps present)

---

## 5. Standard Node Implementations

The SDK provides abstract base classes for common node patterns.

### 5.1 AbstractHitlNode (Human-In-The-Loop)

```go
type AbstractHitlNode struct {
    Node
    queue       HitlQueue
    server      *HitlServer
    config      HitlConfig
}

type HitlConfig struct {
    UsePersistence bool
    Port           int
    QueueCapacity  int
}

type HitlQueue interface {
    Add(ctx context.Context, item WorkitemContext) error
    Get(id string) (WorkitemContext, error)
    List(filter HitlFilter) ([]WorkitemContext, error)
    Remove(id string) error
    Decide(id string, decision HitlDecision) error
}
```

#### The Parking Lot Pattern

When `Assigned()` is called, the workitem enters the "Parking Lot" - a blocking queue awaiting human action:

```go
func (n *AbstractHitlNode) Assigned(ctx context.Context, workitem Workitem) Result {
    waitCtx := n.queue.Add(ctx, WorkitemContext{
        Workitem:   workitem,
        EnqueuedAt: time.Now(),
    })
    
    select {
    case decision := <-waitCtx.DecisionChan:
        return n.handleDecision(decision)
    case <-ctx.Done():
        return Result{TimedOut: true}
    }
}
```

#### Persistence Configuration

| `UsePersistence` | Storage Backend | Use Case |
|------------------|-----------------|----------|
| `false` | `sync.Map` (in-memory) | Testing, development |
| `true` | SQLite at `/data/hitl.db` | Production (requires `spec.storage`) |

### 5.2 Concrete HITL Implementations

**HitlSortNode:**
```go
type HitlSortNode struct {
    AbstractHitlNode
}

func (n *HitlSortNode) handleDecision(d HitlDecision) Result {
    return RouteToOutput(d.Output)
}
```

**HitlAppraiseNode:**
```go
type HitlAppraiseNode struct {
    AbstractHitlNode
}

func (n *HitlAppraiseNode) handleDecision(ctx context.Context, d HitlDecision) Result {
    for _, fb := range d.Feedback {
        n.AddFeedback(ctx, fb.Target, fb.Message, fb.Severity)
    }
    return RouteToOutput(d.Output)
}
```

See [10_hitl_api.md](./10_hitl_api.md) for full specification.
