# Foundry Node: Overview

## 1. Executive Summary

The **Foundry Node** is a **governed runtime environment** designed to execute unreliable logic safely. It serves as the fundamental unit of execution in the Foundry Flow.

Nodes are **Persistent Deployments** that boot once and maintain stateful connections to the Flow. They interact with the Flow by processing assigned Workitems, proactively creating them, or both. This enables connection pooling, expensive model caching, and zero-start-latency execution.

To guarantee the integrity of the "Immutable Kernel," every Node operates under a **Zero-Trust Security Model**:

* **Identity:** Authentication is handled exclusively by the **Sidecar** (Governor). The User Container relies on the Sidecar to broker all authenticated requests with the Control Plane.
  - **v1 Atomic Mode (Current):** The Sidecar authenticates using the Kubernetes ServiceAccount token (mounted at `/var/run/secrets/kubernetes.io/serviceaccount/token`). The Flow Operator trusts the Kubernetes Control Plane to validate pod identity and namespace membership.
  - **v2 Federated Mode (Planned):** The Sidecar will authenticate via mTLS certificate issued by the Flow Operator (acting as an Intermediate CA) during the Annexation Protocol. This enables cross-cluster and cross-flow identity without relying on a shared Kubernetes API.
  
* **Network:** Nodes have **direct, uninhibited network access**. They may communicate with external APIs directly. Network security and observability are infrastructure concerns delegated to Kubernetes NetworkPolicies or Service Mesh configurations.
* **Lineage:** The Node must strictly inherit from the official `foundry/node` base image. This is cryptographically enforced by the Flow Operator before scheduling.
---

## 1.1 The Stateless Worker Model ("Goldfish Memory")

Nodes are designed as **Stateless Workers**. The key distinction:

- **The Node Pod is Persistent:** It boots once and stays alive across many Workitems. This enables expensive initialization to happen once (LLM model loading, connection pool setup, SDK caching).
- **The Node's Memory is Stateless:** Each Workitem execution starts with fresh state. A re-entrant Workitem (e.g., one looping `Quench → Refine → Quench`) may land on any Pod replica. **The Node reads all context from the Workitem CRD and Archivist.**

### Infrastructure State vs Session State

**Infrastructure State (The Persistent Bit):**
Stays warm in the Pod across Workitems. This is the "machinery," not the "work":
- LLM model weights loaded into VRAM
- Database connection pools
- SDK initialization
- Cached dictionaries or lookup tables
- TLS connection cache

Benefits: Zero start-latency for new Workitems, efficient resource utilization.

**Session State (The Stateless Bit):**
Rebuilt from the Workitem CRD on each execution. This includes:
- Conversation history or multi-turn context
- Variables derived from the current Workitem
- In-memory drafts or intermediate results
- Request-scoped tracing context

Why: If a Workitem loops back to this node after visiting other nodes, it may arrive on a different Pod replica that has never seen it before.

### State Management Rules

| State Type | Storage | Example | Lifetime |
|------------|---------|---------|----------|
| **Large content** | Archivist via `StoreArtefact` | `petition-draft.md`, code files | Cross-execution, versioned |
| **Feedback & history** | Workitem CRD (`status.feedback`) | Dispute trails, review notes | Attached to Workitem, mutable |
| **Transient per-execution** | Local memory only | LLM response parsing, temp vars | Dies when handler returns |
| **Infrastructure metrics** | Sidecar (hidden from SDK) | Visit counts, resource usage | Internal observability only |

### The Golden Rule: Treat Every Workitem as a Stranger

When `Assigned(ctx, workitem)` is called:

**✓ DO:**
- Fetch all required artefacts from the Archivist (`FetchArtefact()`)
- Read `workitem.status.feedback` to understand pending issues
- Read `workitem.status.artefacts[]` to see versioning and hashes; retrieve passport stamps via `GetArtefactMetadata()` from the Archivist
- Use `workitem.status.previousAssignee` for context about where the work came from
- Store large results in the Archivist before routing to the next node

**Best Practices:**
- Rebuild context from the Workitem CRD on each execution
- Store cross-execution state in the Archivist
- Use `workitem.status.feedback` for dispute history
- Use `workitem.status.previousAssignee` for routing context

### Example: The Loop Pattern

```go
// ❌ WRONG: Assumes pod memory persists
var workitemCache map[string]string  // GLOBAL - WRONG!

func (n *QuenchNode) Assigned(ctx context.Context, item Workitem) Result {
    // BUG: If item loops back, it may land on a different pod
    if context, exists := workitemCache[item.ID]; exists {
        // This will be empty on a different pod!
        return process(context)
    }
    return RouteToOutput("fail")
}

// ✓ CORRECT: Reads context from Workitem
func (n *QuenchNode) Assigned(ctx context.Context, item Workitem) Result {
    // Fetch the latest draft from Archivist
    draft := n.FetchArtefact(ctx, item, "petition-draft")
    if draft == nil {
        return RouteToOutput("fail")
    }
    
    // Read feedback to understand what needs addressing
    feedback := item.Status.Feedback
    
    // Process with fresh context every time
    revised := n.revise(draft, feedback)
    
    // Store result before routing
    n.StoreArtefact(ctx, item, "petition-draft", revised)
    return RouteToOutput("default")
}
```