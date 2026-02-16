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
