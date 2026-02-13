# Node Runtime Overview

## Goal

- Define node execution semantics that are fully consistent with [Flow Runtime Overview](../02-flow/00-overview.md) and [Flow Operator](../02-flow/01-operator.md).
- Make control-plane authority boundaries explicit: nodes execute work, but do not own lifecycle transitions.
- Establish the bridge to [SDK Core](../04-sdk/01-sdk-core.md) without redefining SDK API details.

## Node Runtime Boundary

- Nodes are data-plane executors that run assignment-scoped business logic.
- [Flow Operator](../02-flow/01-operator.md) owns Workitem lifecycle transitions, assignment state, and routing guard enforcement.
- [Sidecar](./01-sidecar.md) is the mandatory mediation boundary for node-originated reads, writes, and routing outcomes.
- Nodes never persist Workitem control-plane fields directly and never bypass Sidecar to reach system services.
- Node pods may be operationally persistent, but execution context is rebuilt each assignment from Workitem state and Archivist data.

## Assignment Lifecycle from the Node Perspective

- Operator assigns a `Pending` Workitem and Sidecar leases an assignment snapshot to the node.
- Node reads artefacts, feedback, and legal context through Sidecar-mediated SDK calls.
- Node performs business logic, may emit provenance updates, and returns exactly one instruction: `route_to_output`, `route_to`, or `complete`.
- Sidecar submits allowed writes and the routing instruction to Operator.
- Operator validates guard conditions and either advances lifecycle state or rejects with structured errors.

## Runtime Interaction Model

- Node <-> Sidecar: assignment-scoped SDK interaction boundary.
- Sidecar -> Operator: routing instruction and Workitem mutation requests for control-plane persistence.
- Sidecar -> [Archivist](../02-flow/04-system-services.md): artefact versions, stamps, and feedback operations.
- Sidecar -> [Librarian](../02-flow/04-system-services.md): law retrieval and legal-context queries.
- Sidecar -> service telemetry surfaces: node and boundary signals for operations and audit.

## Ownership and Mutation Boundaries

- Workitem control-plane state remains Operator-owned.
- Artefact provenance remains Archivist-owned.
- Law lifecycle state remains Librarian-owned.
- [Forge](../02-flow/03-nodes-external.md#reference-arrangement-responsibilities) reads laws for context seeding and does not write laws.
- [Assay](../02-flow/03-nodes-external.md#assay-as-standard-component) authority ceiling is fixed: resolve Tier 1-2, propose Tier 3, and appeal Tier 4-5.
- Nodes act through intent APIs; Sidecar and Operator enforce whether intent is admissible.
- `complete()` is permissioned by configuration and validated by Operator against the node's bound exit contract.

## Relationship to SDK Documents

- Use this document for runtime authority and execution boundaries.
- Use [SDK Core](../04-sdk/01-sdk-core.md), [SDK Artefacts](../04-sdk/02-sdk-artefacts.md), [SDK Legal](../04-sdk/03-sdk-legal.md), [SDK Feedback](../04-sdk/04-sdk-feedback.md), [SDK Workitems](../04-sdk/05-sdk-workitems.md), and [SDK Telemetry](../04-sdk/06-sdk-telemetry.md) for API contracts.
- Keep this document transport-agnostic where possible; reference wire/schema material in [gRPC API](../05-reference/grpc-api.md) and [CRD Reference](../05-reference/crds.md).

## Runtime Invariants

- One assignment has one active assignee and one routing outcome.
- Nodes do not mutate Workitem lifecycle fields directly.
- Sidecar mediation is mandatory for node-originated actions.
- Operator is the only authority for lifecycle transition persistence.
- Exit completion is exit-node-only and Operator-validated.
- Workitems do not expose `WorkitemType`, `spec.type`, or a freeform context bag.
- [Sort](../02-flow/03-nodes-external.md#sort-as-reference-gate) routing order and stamp semantics remain configuration-driven and unchanged.
- External API integration is allowed at the data-plane boundary, but authenticated Flow runtime operations remain Sidecar-mediated.
