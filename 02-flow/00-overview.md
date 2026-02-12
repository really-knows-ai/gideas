# Flow Runtime Overview

Foundry Flow runtime for operators and platform administrators is defined by component boundaries, the execution loop, and non-negotiable behaviour invariants. Conceptual foundations remain in [Conceptual Overview](../01-concepts/00-overview.md), [Architecture](../01-concepts/01-architecture.md), [Data Model](../01-concepts/02-data-model.md), and [Governance](../01-concepts/03-governance.md).

`02-flow/` is the platform specification for operating a Flow. Node implementation detail lives in [Node Overview](../03-node/00-overview.md). Field-level schema and wire shape live in [CRD Reference](../04-reference/crds.md).

## Runtime Composition

A Flow runtime is composed of control-plane actors, data-plane workers, and boundary services:

- The [Flow Operator](./01-operator.md) reconciles configuration, assigns Workitems, validates routing outcomes, and enforces entry/exit contracts.
- The [Workitem runtime contract](./02-workitem.md) carries control-plane state and artefact references while the Workitem moves through the Flow.
- [External and reference nodes](./03-nodes-external.md) execute work through Sidecar-mediated APIs; node pods stay stateless at execution level.
- [System services](./04-system-services.md) provide law lifecycle, artefact lifecycle, citation processing, telemetry aggregation, and backup surfaces.
- [Configuration](./05-configuration.md) defines topology, contracts, capability grants, and policy limits that shape runtime behaviour.
- [Cross-flow collaboration](./06-cross-flow.md) governs export/import boundaries, trust topology, naturalisation, and law integration.
- [Operations](./07-operations.md) governs monitoring, triage, recovery, and validation drills.

```mermaid
flowchart TD
    FF["FoundryFlow<br/>configuration"] --> OP["Flow Operator<br/>reconcile and route"]
    FN["FoundryNode<br/>configuration"] --> OP

    OP --> WI["Workitem CRDs<br/>state machine"]
    WI --> OP

    OP --> SC["Node Pod<br/>Sidecar + Node"]
    SC --> OP

    SC --> AR["Archivist<br/>artefact lifecycle"]
    SC --> LB["Librarian + Citation Processor<br/>law lifecycle"]

    OP --> FM["Flow Monitor<br/>metrics traces audit"]
    SC --> FM
    AR --> FM
    LB --> FM

    LB --> XF["Cross-flow channel<br/>export import law sync"]
```

## Runtime Loop

Each Workitem moves through a deterministic control loop:

1. Operator observes a routable Workitem and assigns it to one node.
2. Sidecar leases Workitem execution snapshot to the node.
3. Node reads artefacts, laws, and feedback through Sidecar-mediated APIs.
4. Node writes artefact changes and returns a routing instruction.
5. Sidecar submits allowed writes and instruction; Operator validates guards and persists Workitem control-plane state.
6. Operator routes to the next node or validates exit completion.

The Flow remains sequential at orchestration level: one Workitem, one assignee, one routing outcome at a time.

```mermaid
sequenceDiagram
    participant OP as Operator
    participant WI as Workitem
    participant SC as Sidecar
    participant ND as Node
    participant AR as Archivist
    participant LB as Librarian

    OP->>WI: assign node
    OP->>SC: lease Workitem assignment snapshot
    SC->>ND: invoke assigned handler
    ND->>SC: get artefact and feedback
    SC->>AR: query versions stamps feedback
    AR-->>SC: artefact state
    ND->>SC: get applicable laws
    SC->>LB: law query by artefact kind
    LB-->>SC: law representations
    ND->>SC: store artefact update
    SC->>AR: persist version and provenance
    ND->>SC: return route instruction
    SC-->>OP: submit writes + instruction
    OP->>OP: validate routing guards
    OP->>WI: set next assignment or terminal state
```

## Reference Arrangement and Topology Freedom

The Foundry Cycle is the reference arrangement that Flow Architects adapt by adding nodes, merging responsibilities, splitting gate nodes, or replacing reference implementations while preserving platform invariants.

The runtime enforces behaviour through configuration and capabilities, not node names. "Forge", "Sort", or "Refine" describe standard responsibilities in the reference arrangement, but any deployment can map those responsibilities differently.

Assay is the exception: it is a standard runtime component present in every Flow and participates as a routable judicial node.

## Governance Runtime Mechanics

Law and stamp behaviour in runtime is fixed by invariant:

- Forge reads laws for context seeding and does not write laws.
- Laws are single objects with one goal and one-or-more representations; any mutation creates a new whole-law version.
- Stamp names are named governance checkpoints chosen by the Flow Architect; the platform attaches no built-in semantics to names.
- Sort is a gate in the reference arrangement with fixed decision order:
  1. unresolved non-deadlocked feedback routes to Refine;
  2. deadlocked feedback routes to Assay;
  3. missing required stamps route to the node configured to provide each missing stamp;
  4. all feedback resolved and required stamps present allows Sort to apply `approval` and complete the reference path.

Deadlocked feedback is unresolved by state, so reference implementations must treat deadlock as a special-case branch when evaluating unresolved feedback predicates.

- `approval` is a reference-arrangement convention, not a privileged system stamp.
- Assay authority is bounded: resolve Tier 1-2, propose Tier 3, appeal Tier 4-5.

## Exit Completion Model

Exit completion is configuration-bound:

- A node is an exit node only when configured with an exit contract binding.
- Only exit nodes may call `complete()`.
- The Operator, not the node, validates the bound contract.
- Exit contracts are keyed by artefact kind with required stamp-name lists.
- If multiple artefacts of a required kind exist, all must satisfy that kind's requirement.
- A required kind with an empty stamp list means presence-only.
- A contract with no artefact entries imposes no artefact requirements.

When completion triggers cross-flow export, only artefact kinds listed in the bound exit contract are exported. An empty contract exports metadata only.

## Data Ownership Boundaries

The runtime splits control-plane state from provenance state:

- Workitem CRD stores assignment state and artefact references (`id`, `kind`).
- Archivist stores artefact version history, passport stamps, and feedback in SQLite.
- Archivist stores raw artefact content bytes in a blob store (typically fast PVC-backed storage, optionally cloud object storage) keyed by content hash.
- Nodes access artefact and governance state through Sidecar and SDK surfaces; nodes do not call system services directly.

This split keeps Workitems small and watchable while retaining full provenance depth.

## Local Routing and Cross-Flow Boundaries

Local routing and cross-flow transfer are different runtime mechanisms:

- Local routing moves one Workitem between nodes inside one Flow.
- Cross-flow transfer exports a bundle and creates a new Workitem lifecycle in the receiving Flow.
- Export/import is copy-on-write across sovereignty boundaries.
- Successful import creates a `Pending` Workitem that is first-scheduled to configured `importNode` when capacity allows.

Imported stamps are always cryptographically verifiable when chain validation succeeds. Local governance authority depends on topology:

- Sibling flows under a shared State Root: imported stamps are immediately authoritative when stamp names match local requirements.
- Treaty or non-sibling crossings: imported stamps are provenance-only until naturalisation and required local checks are completed.

```mermaid
flowchart LR
    subgraph Local["Single Flow"]
        W1["Workitem A"] --> N1["Node X"] --> N2["Node Y"] --> T1["Exit node"]
    end

    T1 -->|"complete + export"| BND["Boundary"]

    subgraph Remote["Receiving Flow"]
        IMP["Import bundle"] --> W2["Workitem B<br/>Pending"]
        W2 --> IN["Assign configured<br/>importNode"]
        IN --> NAT["Naturalisation and local checks"]
    end

    BND --> IMP
```

## Operational Signal Surface

A running Flow emits three first-class signal families:

- Telemetry: metrics and traces across Operator, Sidecars, nodes, and services.
- Audit: immutable event stream for assignment, routing, law lifecycle, feedback transitions, and stamp actions.
- Friction: quantitative heat tagged to source (law, node, topology path) for governance-cost analysis.

These signals are runtime outputs, not optional observability add-ons.

## Runtime Invariants

The following invariants hold for every Flow deployment:

1. A Workitem is assigned to exactly one node at a time.
2. Flow routing decisions are enforced by the Operator.
3. Sidecar mediates authenticated node access and write operations.
4. Forge reads laws only; law writing belongs to authorised downstream actors.
5. Sort gate ordering is deterministic and configuration-driven for stamp-provider routing.
6. Stamps are named checkpoints with write-once-per-version behaviour.
7. Exit completion is exit-node-only and Operator-validated against bound contracts.
8. Workitem admission is entry-contract-bound.
9. Artefact provenance (versions, stamps, feedback) is Archivist-owned, not Workitem-owned.
10. Assay is always present and cannot exceed its authority ceiling.
11. Cross-flow verifiability and local authority are distinct and topology-dependent.
12. Imported Workitems are created in `Pending` and first-scheduled to configured `importNode` when capacity allows.

These invariants are elaborated normatively in the remaining `02-flow` documents.
