# Node SDK: Export Operations

## 1. Overview

`Export()` bundles the current workitem and transmits it to a target Flow. This is a **SESSION TERMINATOR** - no further node logic executes after success.

**Key Principle:** Export is a finality event. The local Workitem is completed and a new one is created in the target Flow.

**Prerequisite:** The node calling `Export()` **must** be configured with `isTerminal: true` in its `FoundryNode` CRD specification. Attempting to call `Export()` from a non-terminal node will result in an `ErrExportNotTerminal` error at runtime.

---

## 2. Signature

```go
// Export bundles the current workitem and transmits it to a target Flow.
// This is a SESSION TERMINATOR - no further node logic executes after success.
//
// Requirements:
//   - Node must have EXPORT capability
//   - Node must be a terminal node (isTerminal: true)
//
// On success: Workitem is marked Completed; handler MUST return immediately
// On failure: Returns error, Workitem remains in current state
func (n *Node) Export(ctx context.Context, target ExportTarget) error
```

---

## 3. ExportTarget

```go
// ExportTarget specifies where to send the exported bundle.
type ExportTarget struct {
    // Endpoint is the URL of the target Ingress Node.
    // Required.
    Endpoint string
    
    // FlowID is an optional identifier for the target Flow.
    // Used for logging and metrics. Not validated.
    FlowID string
    
    // Artefacts specifies which artefacts to include in the bundle.
    // Empty slice means ALL artefacts are included.
    // Use this to exclude large or irrelevant artefacts.
    Artefacts []string
    
    // Timeout overrides the default export timeout.
    // Default: 30s
    Timeout time.Duration
}
```

---

## 4. Usage Example

```go
func (n *ExportNode) Assigned(ctx context.Context, item Workitem) Result {
    // Validate the workitem is ready for export
    draft := n.FetchArtefact(ctx, item, "petition-draft")
    if draft == nil {
        return RouteToOutput("missing-artefact")
    }
    
    // Export to the execution flow
    err := n.Export(ctx, ExportTarget{
        Endpoint:  os.Getenv("EXPORT_TARGET_URL"),
        FlowID:    "flow-execute",
        Artefacts: []string{"petition-draft", "audit-log"}, // Only these two
    })
    
    if err != nil {
        // Export failed - workitem NOT completed
        // Can retry, escalate, or fail
        n.EmitTelemetry(ctx, "export_failed", map[string]string{
            "error": err.Error(),
        })
        return RouteToOutput("export-failed")
    }
    
    // Export succeeded — terminate handler immediately
    return foundry.Complete() // explicit termination for readability
}
```

---

## 5. Error Handling

```go
// Export errors
var (
    // ErrExportNotTerminal indicates the node lacks terminal status.
    // Fix: Set isTerminal: true in FoundryNode spec.
    ErrExportNotTerminal = errors.New("EXPORT requires terminal node")
    
    // ErrExportNoCapability indicates the node lacks EXPORT capability.
    // Fix: Add "EXPORT" to capabilities in FoundryNode spec.
    ErrExportNoCapability = errors.New("node lacks EXPORT capability")
    
    // ErrExportTargetUnreachable indicates the target endpoint is down.
    // Resolution: Retry or check endpoint configuration.
    ErrExportTargetUnreachable = errors.New("target ingress unreachable")
    
    // ErrExportRejected indicates the target rejected the bundle.
    // Resolution: Check target's Treaty configuration.
    ErrExportRejected = errors.New("target rejected bundle")
)
```

---

## 6. Behavior Notes

| Scenario | Behavior |
|----------|----------|
| Export succeeds | Workitem → `Completed`, handler MUST return immediately |
| Export fails (network) | Error returned, Workitem unchanged, node can retry |
| Export rejected (Treaty) | Error returned with rejection reason |
| Node lacks EXPORT capability | Error at call time (not admission) |
| Node is not terminal | Error at call time |

---

## 7. Complete Example: Export Terminal Node

```go
package main

import (
    "context"
    "os"
    
    foundry "github.com/gideas/foundry-sdk-go"
)

type ExportToExecutionNode struct {
    foundry.AbstractNode
}

func (n *ExportToExecutionNode) Assigned(ctx context.Context, item foundry.Workitem) foundry.Result {
    // Validate readiness for export
    draft := n.FetchArtefact(ctx, item, "approved-petition")
    if draft == nil {
        return n.RouteToOutput("not-ready")
    }
    
    // Check all required stamps are present
    if !n.IsArtefactValid(ctx, item, "approved-petition") {
        return n.RouteToOutput("not-valid")
    }
    
    // Export to execution flow
    err := n.Export(ctx, foundry.ExportTarget{
        Endpoint: os.Getenv("EXECUTION_FLOW_INGRESS"),
        FlowID:   "flow-execute",
    })
    
    if err != nil {
        // Log the failure
        n.EmitTelemetry(ctx, "export_failed", map[string]any{
            "target": "flow-execute",
            "error":  err.Error(),
        })
        
        // Route to error handling
        return n.RouteToOutput("export-failed")
    }
    
    // Unreachable - Export() terminates the session on success
    return foundry.Result{}
}

func main() {
    foundry.Run(&ExportToExecutionNode{})
}
```
