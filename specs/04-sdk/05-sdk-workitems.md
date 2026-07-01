# SDK Workitems

The Workitem SDK surface provides a domain-object interface to the assigned [Workitem's](../02-flow/02-workitem.md) lifecycle, artefacts, feedback, laws, children, topology, and friction. The `Workitem` is the composition root for all workitem-scoped operations — it carries an internal session reference and all methods manage their own gRPC context. No `context.Context` is accepted by any method.

## Workitem Domain Object

The handler receives a `Workitem` object at invocation. Constructed by `Client.GetWorkitem()`.

### Workitem ID

| Method | Returns | Description |
|--------|---------|-------------|
| `Workitem.ID()` | `string` | Workitem identifier. |

### Lifecycle

| Method | Returns | Description |
|--------|---------|-------------|
| `Workitem.Complete(opts ...)` | `error` | Submits completion. Returns error only — no bool (Operator returns gRPC error on rejection). |
| `Workitem.RouteTo(outputName)` | `error` | Routes through the named output channel. |
| `Workitem.Suspend(opts ...)` | `error` | Transitions to Suspended phase. Re-dispatched on resume. |
| `Workitem.Resume()` | `error` | Resumes a suspended workitem. |
| `Workitem.IsSuspended()` | `(bool, error)` | Returns locally cached suspension state (no RPC). |
| `Workitem.Heartbeat()` | `error` | Resets the Sidecar's inactivity timer. |
| `Workitem.PauseTimer()` | `error` | Suspends the Sidecar inactivity timer (used by HITL nodes). |
| `Workitem.ResumeTimer()` | `error` | Resumes the Sidecar inactivity timer. |

`Complete` and `RouteTo` drop the `bool` return the old flat `Client` methods carried. The bool was redundant — the Operator returns a gRPC error when it rejects the action, so the SDK treats a non-accepted result as an error.

`CompleteOption` and `SuspendOption` use the existing functional option pattern (unchanged signature, just no `ctx`).

### Artefacts

| Method | Returns | Description |
|--------|---------|-------------|
| `Workitem.GetArtefact(governedArtefact)` | `(*Artefact, error)` | Look up artefact by governed artefact kind (e.g. `"haiku"`). |
| `Workitem.FindArtefact(artefactID)` | `(*Artefact, error)` | Look up artefact by unique artefact ID (e.g. `"art-abc-123"`). |

### Feedback

| Method | Returns | Description |
|--------|---------|-------------|
| `Workitem.AddFeedback(artefactID, canWontFix, message)` | `(string, error)` | Creates a new feedback item. Returns the feedback ID. |
| `Workitem.GetFeedback(artefactID)` | `([]*Feedback, error)` | Returns all feedback items for the artefact as domain `*Feedback` objects. |
| `Workitem.HasUnresolvedFeedback(artefactID)` | `(bool, error)` | True if any feedback is in a non-resolved state. |

### Laws

| Method | Returns | Description |
|--------|---------|-------------|
| `Workitem.GetLawGroups(repType)` | `([]*LawGroup, error)` | Returns law groups for the given representation type. |
| `Workitem.VerifyLawAttestations(governedArtefact)` | `([]string, error)` | Returns missing stamp names required for full attestation. |
| `Workitem.Cite(lawIDs ...)` | `error` | Records usage of one or more laws, emitting citation friction. |

### Children

| Method | Returns | Description |
|--------|---------|-------------|
| `Workitem.CreateChild()` | `(*ChildWorkitem, error)` | Creates a new child Workitem. |
| `Workitem.GetChildren()` | `([]ChildWorkitemStatus, error)` | Returns status of all children. |
| `Workitem.FanOut(tasks)` | `([]*ChildWorkitem, error)` | Creates children, attaches artefacts, routes each to its target. |
| `Workitem.AwaitAll()` | `([]ChildWorkitemStatus, error)` | Blocks until every child reaches a terminal phase. |
| `Workitem.WatchChildren()` | `(*ChildWatcher, error)` | Opens a streaming subscription to the Event Bus for child lifecycle events. |

### Topology

| Method | Returns | Description |
|--------|---------|-------------|
| `Workitem.GetTopology()` | `(*Flow, error)` | Returns the flow topology visible to the calling node. |

### Friction

| Method | Returns | Description |
|--------|---------|-------------|
| `Workitem.QueryFriction(filter)` | `([]*FrictionAggregate, error)` | Returns aggregated friction data from the Friction Ledger. |

## Local Workitem Creation

Local Workitem creation (for entry-bound nodes) is performed through the `EntryClient`, not the `Workitem` object. See [SDK Cross-Flow](./09-sdk-cross-flow.md).

## Child Workitem SDK Surface

The child Workitem SDK surface provides parallel work decomposition within a single handler assignment. A node can create child Workitems, populate them with artefacts, route them for independent processing, and collect results when they complete.

Child Workitem operations require the `CREATE:workitem/child` capability.

### Creating Child Workitems

`Workitem.CreateChild()` creates a new child Workitem in `Pending` state with `parentWorkitemID` set to the caller's current Workitem. Returns a `ChildWorkitem` handle scoped to the new child.

The child Workitem is an internal implementation detail of the parent's processing. It does not participate in Flow-level entry or exit contracts.

### ChildWorkitem Handle

`CreateChild()` returns a `ChildWorkitem` handle. All methods return `error` only — no `(bool, error)` and no proto response.

| Method | Returns | Description |
|--------|---------|-------------|
| `ChildWorkitem.ID()` | `string` | Returns the child Workitem identifier. |
| `ChildWorkitem.StoreArtefact(artefactID, governedArtefact, content)` | `error` | Stores artefact content on the child. Only valid before the child is routed. |
| `ChildWorkitem.StampArtefact(artefactID, stampName)` | `error` | Applies a stamp to a child artefact. Only valid before the child is routed. |
| `ChildWorkitem.RouteTo(targetNode)` | `error` | Routes the child to a target node. |
| `ChildWorkitem.RouteToOutput(outputName)` | `error` | Routes the child through a named output. |
| `ChildWorkitem.Complete()` | `error` | Simple completion — no exit contract validation. |

Once a child has been routed, the creating node can no longer write artefacts to it or re-route it. Attempts to do so return `CHILD_ALREADY_ROUTED`.

### ChildWorkitemStatus

| Field | Type | Description |
|-------|------|-------------|
| `WorkitemID` | `string` | Child Workitem identifier. |
| `Phase` | `string` | Current lifecycle state: `Pending`, `Running`, `Completed`, `Failed`. |
| `CurrentAssignee` | `string` | Node currently assigned to the child. Empty when `Pending`. |
| `Artefacts` | `[]ArtefactRef` | Artefact references associated with the child in the Archivist. |

### ChildLifecycleEvent

| Field | Type | Description |
|-------|------|-------------|
| `WorkitemID` | `string` | Child Workitem identifier. |
| `Phase` | `string` | The phase the child transitioned to. |
| `NodeID` | `string` | The node involved in the transition (assignee for `Running`, empty for terminal states). |

### ChildWatcher (Streaming)

`Workitem.WatchChildren()` returns a `*ChildWatcher` with explicit lifecycle.

| Method | Returns | Description |
|--------|---------|-------------|
| `ChildWatcher.Recv()` | `(*ChildLifecycleEvent, error)` | Blocks until a lifecycle event arrives, stream ends (`io.EOF`), or `Stop()` is called. |
| `ChildWatcher.Stop()` | | Cancels the internal context and closes the stream. Idempotent. |

Usage:

```go
watcher, _ := workitem.WatchChildren()
defer watcher.Stop()
for {
    evt, err := watcher.Recv()
    if err == io.EOF {
        break
    }
    // process evt
}
```

### Fan-Out/Fan-In Pattern

The typical usage pattern for child Workitems:

1. **Fan-out**: Create multiple children via `Workitem.FanOut(tasks)` or `Workitem.CreateChild()`, populate each with input artefacts via `ChildWorkitem.StoreArtefact()`, then route each for processing.
2. **Wait**: Use `Workitem.AwaitAll()` which attempts streaming via `WatchChildren()` and falls back to polling via `GetChildren()`.
3. **Fan-in**: Inspect child statuses from `AwaitAll()` or `GetChildren()`. Failed children are skipped or handled according to node business logic.
4. **Complete**: After collecting results, the parent node continues its own processing — storing aggregated artefacts, routing, or completing.

`AwaitAll()` pauses the Sidecar inactivity timer while waiting and resumes it before returning (even on error).

## FanOut Tasks

```go
type FanOutTask struct {
    TargetNode string
    Artefacts  []ChildArtefact
}

type ChildArtefact struct {
    ID               string
    GovernedArtefact string
    Content          []byte
}
```

## Routing and Outcome Submission

Routing is the handler's final action — returning a `Result` from the handler function. The three routing instructions are:
- `RouteToOutput(name)` — route through a named output channel
- `RouteTo(node)` — route directly to a specific node
- `Complete()` — signal exit completion

These are expressed by calling `Workitem.RouteTo()` / `Workitem.Complete()` within the handler and then returning the `Result`.

The Sidecar submits the routing instruction to the Operator. The Operator validates routing guards — output name resolution, target node existence, exit contract satisfaction — and applies the lifecycle transition or returns a structured error.

The handler does not observe the Operator's routing decision. Once the handler returns a `Result`, the assignment is over from the node's perspective.

## Mutation Authority Boundaries

The SDK requests mutations; runtime services authorise and persist them.

| Mutation | SDK Action | Authoritative Owner |
|----------|-----------|-------------------|
| Lifecycle transitions | `Workitem.Complete()`, `Workitem.Suspend()`, routing methods | [Operator](../02-flow/01-operator.md) |
| Artefact content and versions | `Artefact.Store()` | [Archivist](../02-flow/04-system-services.md#archivist) |
| Stamps | `Artefact.Stamp()`, `ChildWorkitem.StampArtefact()` | [Archivist](../02-flow/04-system-services.md#archivist) |
| Feedback | `Workitem.AddFeedback()`, `Feedback.Resolve()`, etc. | [Archivist](../02-flow/04-system-services.md#archivist) |
| Laws | `Client.RecordFinding()` | [Librarian](../02-flow/04-system-services.md#librarian) |
| Thrash counter increments | Not exposed | [Operator](../02-flow/01-operator.md) (hidden from nodes) |

The node cannot directly set lifecycle states, modify assignment fields, alter thrash counters, or bypass entry/exit contract validation. These are Operator-owned control-plane operations that the SDK has no surface for.

## Cross-Flow Related SDK Paths

[Cross-flow transfer](../02-flow/06-cross-flow.md) is an [Embassy](../02-flow/06-cross-flow.md#embassy)-level mechanism. From the node handler's perspective, an imported Workitem looks like any other assignment — the handler receives a `Workitem` snapshot and processes it using the same SDK operations. The full SDK surface for cross-flow operations is defined in [SDK Cross-Flow](./09-sdk-cross-flow.md).

### Embassy Materialisation

When the receiving Embassy accepts an inbound transfer, it creates a new local Workitem and unpacks artefacts into the local [Archivist](../02-flow/04-system-services.md#archivist). By the time a node handler receives the assignment, all imported artefacts are already persisted locally — the handler reads them through standard [Artefact SDK](./02-sdk-artefacts.md) operations with no awareness that the content originated from another Flow.

### Naturalisation and `imported-*` Attestation Stamps

The Embassy applies [naturalisation](../02-flow/06-cross-flow.md#naturalisation) during materialisation. For each required foreign stamp that validates against the trust source (federation root or Treaty-pinned CA), the Embassy applies a local `imported-<stamp>` attestation stamp on the imported artefact. What local code sees after naturalisation:

- `imported-*` attestation stamps are present on artefact passports and are indistinguishable from any other local stamp in SDK queries.
- Foreign stamps remain attached for provenance and audit but downstream local contracts rely on the `imported-*` stamps, not on the foreign stamps directly.
- Whether `imported-*` stamps satisfy local entry or routing contracts depends on the Flow's contract configuration, evaluated at the Operator level — not in node code.

### Imported Petitions via Effective Import-Type Resolution

Imported Workitems enter the receiving Flow through the node or path resolved for the matching effective import type. Flow-authored import types use [`crossFlow.importTypes`](../02-flow/06-cross-flow.md#import-types). Built-in system import types such as `law-petition` are always present/configured per Flow by the platform rather than user-authored in YAML. In either case, the receiving node sees a normal Workitem assignment with `imported-*` attestation stamps on its artefacts. The node does not need to know the Workitem was imported — it processes based on artefact content and stamp state like any other assignment.

### Export

Export is triggered by explicit handoff to the [Embassy](../02-flow/06-cross-flow.md#embassy), not by generic exit completion. A node such as [law-applicator](../01-concepts/02-foundry-cycle.md#law-applicator) stores the petition artefact and any supporting state, then routes the Workitem to Embassy. The Embassy validates its own boundary requirements, creates the export bundle, and performs the transfer. The node does not call an export method or specify a destination — routing and federation or Treaty policy determine the remote target.

## Workitem SDK Invariants

1. All `Workitem` operations are scoped to the current assignment. No `context.Context` is accepted — the `Workitem` carries its own session.
2. The `Workitem` object is a composition root for artefacts, feedback, laws, children, topology, and friction.
3. `Complete()` and `RouteTo()` return `error` only — no bool (Operator returns gRPC error on rejection).
4. No freeform context bag, `WorkitemType`, or type discriminator exists on the SDK surface.
5. `ChildWorkitem` methods return `error` only — no `(bool, error)`, no proto responses.
6. Streaming uses `ChildWatcher` with `Recv()/Stop()`. No channel-based patterns.
7. `AwaitAll()` pauses the Sidecar timer while waiting and resumes on return (even on error).
8. Child Workitem creation requires `CREATE:workitem/child` capability.
9. The `ChildWorkitem` handle is the sole interface for mutating a child before routing. Once routed, the child is immutable from the parent's perspective.
10. The [Operator](../02-flow/01-operator.md) owns lifecycle transitions, routing validation, and contract enforcement.
11. Thrash guard state is hidden from nodes.
