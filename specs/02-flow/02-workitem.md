# Workitems

Workitems are the Flow control-plane state machine for work execution. They carry assignment state and routing outcomes while work moves through the runtime. Artefacts are associated with Workitems through a reverse reference in the [Archivist](../02-flow/04-system-services.md#archivist) — each artefact records the `workitem_id` it belongs to.

## Runtime Role

A Workitem is the unit of orchestration state, not the unit of provenance storage.

- It anchors assignment lifecycle in the control plane.
- It carries node routing instructions between assignments.
- It does not store artefact references, version history, stamps, or feedback bodies.
- Artefacts are associated with the Workitem in the Archivist, which records the `workitem_id` each artefact belongs to.

The Workitem state machine is single-assignee: one Workitem is assigned to exactly one node at a time.

## Ownership and Mutability Boundaries

Workitem mutability is partitioned by actor. Ownership is strict and non-overlapping.

| Surface | Owner | Mutability | Purpose |
|---|---|---|---|
| lifecycle state | Operator | Managed transitions | `Pending`, `Running`, terminal states |
| assignment fields | Operator | Managed transitions | Current and previous assignee tracking |
| routing instruction | Operator from Sidecar-submitted result | Overwrite per assignment | Next action requested by node |
| thrash counters | Operator | Increment-only | Loop budget enforcement |

Nodes do not mutate Workitem state directly. All node-originated state changes are mediated by the [Sidecar](../03-node/01-sidecar.md), then validated and persisted by the [Flow Operator](./01-operator.md).

## Lifecycle States and Transitions

Workitem lifecycle uses deterministic control-plane states:

- `Pending`: waiting for assignment.
- `Running`: currently assigned to a node.
- `Completed`: terminal success after contract validation.
- `Failed`: terminal failure due to runtime guard or processing error.

```mermaid
stateDiagram-v2
    [*] --> Pending
    Pending --> Running : assign
    Running --> Pending : route
    Running --> Completed : complete + contract valid
    Running --> Failed : timeout / thrash / invalid route / node failure
    Pending --> Failed : unrecoverable scheduling failure
    Completed --> [*]
    Failed --> [*]
```

Transition guards are fixed:

- `Pending -> Running` requires a valid target node and records current assignment ownership on the Workitem.
- `Running -> Pending` requires a valid non-terminal routing instruction.
- `Running -> Completed` requires exit-node `complete()` and successful contract validation.
- Any guard violation or runtime failure transitions to `Failed` when recovery budget is exhausted.

## Routing Instruction Contract

Each assignment ends with exactly one routing instruction.

- `route_to_output`: route by named output configured on the current node.
- `route_to`: route directly to a specific node.
- `complete`: request exit completion.

Instruction validity checks:

- Output and direct targets must resolve in current configuration.
- `complete` is valid only from exit nodes.
- Invalid instructions are rejected with structured errors and do not advance completion.

Routing semantics are runtime-level control behaviour; schema-level instruction fields are defined in [CRD Reference](../05-reference/crds.md). Error mappings are defined in [Error Catalogue](../05-reference/error-catalogue.md).

## Thrash Guard and Feedback Deadlock

Thrash and deadlock are distinct mechanisms with different sources and outcomes.

- **Thrash Guard** is infrastructure loop control on Workitem assignment history.
  - Enforcement key: total visits across all nodes.
  - Diagnostic signal: per-node counters.
  - Outcome: Workitem fails when aggregate visit budget is exceeded.

- **Feedback deadlock** is governance dispute detection on artefact feedback history.
  - Source of truth: Archivist feedback records via SDK queries.
  - Enforcement actor: gate node routing logic under configured deadlock threshold policy (the reference [Sort](../01-concepts/02-foundry-cycle.md#sort-gate) node in the standard arrangement).
  - Outcome: Workitem routes to [Assay](./03-nodes-external.md#assay-as-standard-component) for adjudication.

Thrash failure and governance deadlock escalation are never treated as equivalent transitions.

## Artefact Association Model

Workitems do not carry artefact references. The [Archivist](../02-flow/04-system-services.md#archivist) is the single source of truth for artefact-to-Workitem relationships.

- Each artefact in the Archivist records the `workitem_id` it belongs to.
- Each artefact has an `id` (unique within the Workitem) and a `kind` (immutable for a given `id`).
- Multiple artefacts of the same `kind` are supported through distinct `id` values.
- The Archivist enforces identity rules: existing `id` with a different `kind` is rejected as `ARTEFACT_KIND_CONFLICT`.

Nodes interact with artefacts through SDK abstractions (for example, storing artefact content by `id`). The Sidecar submits requests to the Archivist, which persists content and maintains the artefact-to-Workitem association.

Provenance ownership is entirely within the Archivist:

- artefact identity and association -> Archivist
- version history -> Archivist
- stamps/passports -> Archivist
- feedback -> Archivist

```mermaid
flowchart LR
    WI["Workitem CRD<br/>assignment + routing"] --> OP["Operator<br/>queries Archivist for contract checks"]
    OP --> ARDB["Archivist SQLite<br/>artefacts versions stamps feedback"]
    ARDB --> BL["Blob store (PVC/object)<br/>content bytes by hash"]
```

This split keeps Workitem objects bounded and watch-efficient while preserving complete governance history.

## Entry and Exit Boundary Interaction

Entry admission and exit completion are Workitem boundary transitions controlled by configuration and Operator validation.

- Only nodes bound to an entry contract can admit Workitems into a Flow lifecycle.
- Entry checks validate the bound entry contract against current artefact state in the Archivist.
- Entry and exit contracts use the same per-kind validation shape.
- Cross-flow import admission creates Workitems in `Pending`, then Operator schedules first assignment to configured `importNode` when capacity allows.
- Review-hearing admission uses Assay's hearing entry binding, then Operator schedules first assignment to Assay when capacity allows.

## Exit Completion Interaction

Exit completion is a Workitem state transition controlled by configuration and Operator validation.

- Only exit nodes may emit `complete()`.
- Exit binding is fixed in node configuration.
- The node does not choose a contract at runtime.
- Operator validates the bound exit contract against current artefact state in the Archivist.
- In the reference arrangement, governed artefact completion is user-configured through Sort, while review-hearing Workitems complete through Assay's hearing exit binding.

Contract evaluation rules:

- Requirements are keyed by artefact kind.
- Required stamp lists are name-based governance checkpoints.
- Empty stamp list means presence-only for that kind.
- Empty contract means no artefact requirements.
- If multiple artefacts of a required kind exist, all must satisfy the requirement.

If validation fails, completion is rejected and the Workitem does not transition to `Completed`.

```mermaid
sequenceDiagram
    participant ND as Node
    participant SC as Sidecar
    participant OP as Operator
    participant AR as Archivist

    ND->>SC: complete()
    SC-->>OP: completion instruction
    OP->>OP: confirm node is exit-bound
    OP->>AR: query artefact state for bound contract
    AR-->>OP: kinds stamps feedback state
    OP->>OP: evaluate per-kind requirements
    OP-->>SC: accept or reject completion
```

When completion triggers cross-flow export, only artefact kinds listed in the bound exit contract are export-eligible. Empty contract completion exports metadata only.

## No Workitem Context Bag

Workitems have no freeform context object. There is no `status.context` and no reserved key namespace for bag-style metadata.

All relevant work context must be represented by explicit Workitem state and governed artefacts.

## Retention and Finalisation

`Completed` and `Failed` are terminal states. Terminal Workitems are retained according to configured retention policy and then cleaned up by operational policy.

- Retention duration is configuration-driven.
- Cleanup sequencing must preserve required audit and provenance visibility.
- Operational procedures are defined in [Operations](./07-operations.md).

## Workitem Invariants

All Flow runtimes preserve these Workitem invariants:

1. The Workitem CRD has no `spec` block. All state is Operator-managed.
2. A Workitem has one current assignee at a time.
3. Node mutations are Sidecar-mediated; nodes do not write Workitem state directly.
4. Routing advances only on valid, resolvable instructions.
5. Thrash enforcement uses aggregate visit count across all nodes.
6. Feedback deadlock decisions are based on Archivist-backed feedback state.
7. Artefact-to-Workitem association is Archivist-owned. The Workitem CRD carries no artefact references.
8. Exit completion is exit-node-only and Operator-validated.
9. Exit contract checks query the Archivist for artefact kinds and stamps.
10. Cross-flow export scope follows bound exit-contract kind entries.
11. Workitems expose no freeform context bag.
12. Workitem admission is constrained by bound entry-contract kind entries.
13. Imported Workitems are created in `Pending` and first-scheduled to configured `importNode` when capacity allows.

These invariants are consumed by [Flow Operator](./01-operator.md), [External Nodes](./03-nodes-external.md), [System Services](./04-system-services.md), and [Configuration Semantics](./05-configuration.md).
