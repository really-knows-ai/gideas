# SDK Workitems

The Workitem SDK surface provides read access to the assigned [Workitem's](../02-flow/02-workitem.md) state, local Workitem creation, and routing outcome submission. The [Operator](../02-flow/01-operator.md) owns Workitem lifecycle persistence — the SDK expresses intent through structured requests, and the Operator validates and applies state changes.

## Workitem Read Surface

The handler receives a `Workitem` object at invocation. This object is a snapshot of state at assignment time.

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | Workitem identifier |
| `phase` | string | Current lifecycle state: `Pending`, `Running`, `Completed`, `Failed` |
| `currentAssignee` | string | Node currently assigned (this node, during handler execution) |

The snapshot does not update during handler execution.

Live artefact state — content, versions, stamps, feedback — is accessed through the [Artefact SDK](./02-sdk-artefacts.md) and [Feedback SDK](./04-sdk-feedback.md) operations, which query the Archivist on demand. The Archivist maintains artefact-to-Workitem associations; the Workitem CRD itself carries no artefact references.

Fields intentionally absent from the Workitem read surface:

- No `type` or `WorkitemType` — Flow admission is not type-gated.
- No `context` — there is no freeform context bag. All work context is represented by explicit Workitem state and governed artefacts.
- No `feedback` — feedback lives in the [Archivist](../02-flow/04-system-services.md#archivist), accessed through the [Artefact object](./04-sdk-feedback.md#feedback-query-surfaces).
- No thrash counters — [thrash guard](../02-flow/02-workitem.md#thrash-guard-and-feedback-deadlock) state is infrastructure-level and hidden from nodes.

## Assignment-Scoped Action Model

All SDK Workitem operations apply to the currently assigned Workitem. The [Sidecar](../03-node/01-sidecar.md) injects the assignment's Workitem identity into every outgoing request. No parameter exists for targeting a different Workitem.

A handler cannot:

- Query the Flow's Workitem queue or pending assignments.
- Observe other nodes' active assignments.

By default, all SDK Workitem operations are scoped to the current assignment. Nodes granted the `READ:workitem` capability can read Workitem state beyond the current assignment — this is an opt-in capability configured on the [FoundryNode](../05-reference/crds.md#foundrynode) CRD, not a default behaviour.

## Local Workitem Creation

Nodes can create new Workitems within the same Flow.

| Operation | Parameters |
|-----------|-----------|
| `CreateWorkitem()` | No parameters. |

The created Workitem enters `Pending` state and is subject to entry contract validation by the [Operator](../02-flow/01-operator.md). The creating node must be bound to an [entry contract](../02-flow/05-configuration.md#entry-and-exit-contract-semantics) — nodes without an entry binding cannot create Workitems.

Entry contract validation checks artefact state in the Archivist against the node's bound entry contract requirements. Validation failure rejects the creation and returns a structured error.

Created Workitems are independent lifecycle objects. They are not child Workitems, sub-tasks, or continuations of the creating handler's assignment. The Operator schedules them through normal assignment logic.

## Child Workitem SDK Surface

The child Workitem SDK surface provides parallel work decomposition within a single handler assignment. A node can create child Workitems, populate them with artefacts, route them for independent processing, and collect results when they complete. This enables fan-out/fan-in patterns without requiring intermediate nodes or custom orchestration.

Child Workitem operations require the `CREATE:workitem/child` capability on the [FoundryNode](../05-reference/crds.md#foundrynode) CRD.

### Creating Child Workitems

| Operation | Parameters | Returns |
|-----------|-----------|---------|
| `CreateChildWorkitem()` | No parameters. | `(*ChildWorkitem, error)` |

Creates a new child Workitem in `Pending` state with `parentWorkitemID` set to the caller's current Workitem. Returns a `ChildWorkitem` handle scoped to the new child.

The child Workitem is an internal implementation detail of the parent's processing. It does not participate in Flow-level entry or exit contracts.

### ChildWorkitem Handle

`CreateChildWorkitem()` returns a `ChildWorkitem` handle — a scoped client with methods that operate on the child Workitem's artefacts and lifecycle. The handle targets the child's Workitem ID for all operations.

| Method | Parameters | Returns | Description |
|--------|-----------|---------|-------------|
| `ID()` | None | `string` | Returns the child Workitem identifier. |
| `StoreArtefact(ctx, artefactID, governedArtefact, content)` | `context`, artefact ID, governed artefact name, content bytes | `error` | Stores artefact content on the child Workitem. Only valid before the child has been routed. |
| `StampArtefact(ctx, artefactID, stampName)` | `context`, artefact ID, stamp name | `error` | Applies a stamp to a child artefact. Requires the appropriate `STAMP:artefact` capability. Only valid before the child has been routed. |
| `RouteTo(ctx, targetNode)` | `context`, target node name | `(bool, error)` | Submits a `route_to` routing instruction for the child. The child transitions to normal assignment processing. Returns whether the routing was accepted. |
| `RouteToOutput(ctx, outputName)` | `context`, output name | `(bool, error)` | Submits a `route_to_output` routing instruction for the child. Returns whether the routing was accepted. |
| `Complete(ctx)` | `context` | `(bool, error)` | Simple completion — no exit contract validation. Returns whether completion was accepted. |

Once a child has been routed, the creating node can no longer write artefacts to it or re-route it. Attempts to do so return `CHILD_ALREADY_ROUTED`.

### Querying Child State

| Operation | Parameters | Returns | Description |
|-----------|-----------|---------|-------------|
| `GetChildren(ctx)` | `context` | `([]ChildWorkitemStatus, error)` | Returns the current state of all child Workitems for the caller's parent Workitem. |
| `GetChildArtefact(ctx, childWorkitemID, artefactID)` | `context`, child Workitem ID, artefact ID | `(*GetArtefactResponse, error)` | Reads an artefact from a completed child Workitem. The target child must be in `Completed` state and must be a child of the caller's current Workitem. |
| `ListChildArtefacts(ctx, childWorkitemID)` | `context`, child Workitem ID | `([]*ArtefactRef, error)` | Lists all artefacts associated with a child Workitem. Same parent-child and completion-state validation as `GetChildArtefact`. |

### ChildWorkitemStatus

| Field | Type | Description |
|-------|------|-------------|
| `WorkitemID` | `string` | Child Workitem identifier. |
| `Phase` | `string` | Current lifecycle state: `Pending`, `Running`, `Completed`, `Failed`. |
| `CurrentAssignee` | `string` | Node currently assigned to the child. Empty when `Pending`. |
| `Artefacts` | `[]ArtefactRef` | Artefact references associated with the child in the Archivist. |

### Watching Child Lifecycle Events

| Operation | Parameters | Returns | Description |
|-----------|-----------|---------|-------------|
| `WatchChildren(ctx)` | `context` | `(<-chan ChildLifecycleEvent, error)` | Opens a streaming subscription to the [Flow Event Bus](../02-flow/04-system-services.md#flow-event-bus) `WORKITEM` channel, filtered by `parent_workitem_id` matching the caller's current Workitem. Returns a channel of lifecycle events as children transition through phases. |

### ChildLifecycleEvent

| Field | Type | Description |
|-------|------|-------------|
| `WorkitemID` | `string` | Child Workitem identifier. |
| `Phase` | `string` | The phase the child transitioned to. |
| `NodeID` | `string` | The node involved in the transition (assignee for `Running`, empty for terminal states). |

### Fan-Out/Fan-In Pattern

The typical usage pattern for child Workitems:

1. **Fan-out**: Create multiple children via `CreateChildWorkitem()`, populate each with input artefacts via the handle's `StoreArtefact()`, then route each for processing.
2. **Wait**: Use `WatchChildren()` to stream lifecycle events, or poll with `GetChildren()`, until all children reach terminal state.
3. **Fan-in**: Read artefacts from completed children via `GetChildArtefact()` and `ListChildArtefacts()`. Failed children are skipped or handled according to node business logic.
4. **Complete**: After collecting results, the parent node continues its own processing — storing aggregated artefacts, routing, or completing.

## Routing and Outcome Submission

Routing is the handler's final action — the single [Result](./01-sdk-core.md#routing-instruction-model) returned to the platform. The three routing instructions (`RouteToOutput`, `RouteTo`, `Complete`) are defined in [SDK Core](./01-sdk-core.md#routing-instruction-model).

The Sidecar submits the routing instruction to the Operator. The Operator validates routing guards — output name resolution, target node existence, exit contract satisfaction — and applies the lifecycle transition or returns a structured error.

The handler does not observe the Operator's routing decision. Once the handler returns a `Result`, the assignment is over from the node's perspective.

## Mutation Authority Boundaries

The SDK requests mutations; runtime services authorise and persist them.

| Mutation | SDK Action | Authoritative Owner |
|----------|-----------|-------------------|
| Lifecycle transitions (`Pending` -> `Running` -> `Completed`/`Failed`) | Implicit (assignment, routing, completion) | [Operator](../02-flow/01-operator.md) |
| Routing instruction | Handler returns `Result` | [Operator](../02-flow/01-operator.md) validates and applies |
| Artefact content and versions | `StoreArtefact` | [Archivist](../02-flow/04-system-services.md#archivist) persists content and maintains artefact-to-Workitem association |
| Stamps | `StampArtefact` | [Archivist](../02-flow/04-system-services.md#archivist) |
| Feedback | `AddFeedback`, transitions | [Archivist](../02-flow/04-system-services.md#archivist) |
| Laws | `RecordFinding` | [Librarian](../02-flow/04-system-services.md#librarian) |
| Thrash counter increments | Not exposed | [Operator](../02-flow/01-operator.md) (hidden from nodes) |

The node cannot directly set lifecycle states, modify assignment fields, alter thrash counters, or bypass entry/exit contract validation. These are Operator-owned control-plane operations that the SDK has no surface for.

## Cross-Flow Related SDK Paths

[Cross-flow transfer](../02-flow/06-cross-flow.md) is an Operator-level mechanism. From the node handler's perspective, an imported Workitem looks like any other assignment — the handler receives a `Workitem` snapshot and processes it using the same SDK operations.

Import-specific behaviours are transparent to the node:

- Imported artefacts are already persisted in the local [Archivist](../02-flow/04-system-services.md#archivist) by the time the handler sees them.
- Imported stamps are preserved on the artefact passport. Whether they satisfy local stamp requirements depends on [trust topology](../02-flow/06-cross-flow.md#trust-topologies) (sibling vs treaty), but this evaluation happens at the Operator level during contract checks, not in node code.
- [Naturalisation](../02-flow/06-cross-flow.md#naturalisation) requirements are expressed through entry contracts and local stamp requirements. The node processes the Workitem normally — if local stamps are needed, the normal routing loop drives the Workitem to the nodes configured to provide them.

Export is triggered by exit completion. When a handler calls `Complete()` on an exit-bound node and the [exit contract](../02-flow/05-configuration.md#entry-and-exit-contract-semantics) is satisfied, the Operator handles the export bundle creation. The node does not call an export method or specify a destination — export configuration is Flow-level, not node-level.

## Workitem SDK Invariants

1. All Workitem SDK operations are scoped to the current assignment by default. Nodes with `READ:workitem` capability can read beyond the current assignment.
2. The `Workitem` object is a snapshot at assignment time and does not update during handler execution.
3. No freeform context bag, `WorkitemType`, or type discriminator exists on the SDK surface.
4. Feedback is not part of the Workitem read surface — it is accessed through [Artefact](./02-sdk-artefacts.md) and [Feedback](./04-sdk-feedback.md) operations.
5. Local Workitem creation requires an entry contract binding on the creating node.
6. The [Operator](../02-flow/01-operator.md) owns lifecycle transitions, routing validation, and contract enforcement.
7. Thrash guard state is hidden from nodes.
8. Cross-flow import and export are transparent to node handlers.
9. Child Workitem creation requires `CREATE:workitem/child` capability.
10. The `ChildWorkitem` handle is the sole interface for mutating a child before routing. Once routed, the child is immutable from the parent's perspective.
11. Cross-Workitem artefact reads are read-only, parent-scoped, and completion-gated.
12. `WatchChildren()` is a streaming subscription to the Flow Event Bus `WORKITEM` channel filtered by `parent_workitem_id`.
