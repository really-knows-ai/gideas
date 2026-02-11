# Plan: `02-flow/03-nodes-external.md`

Status: planning only. Spec drafting has not started.

## Purpose

Define how nodes participate in the Flow runtime, including reference node responsibilities, capability boundaries, and external integration behaviour without violating core governance and routing invariants.

## Audience

- Flow architects composing node topologies
- Node and Sidecar implementers
- Operators responsible for runtime safety and policy enforcement

## Inputs

Primary source material:

- `legacy/flow_spec/10_reference_flow_haiku.md`
- `legacy/flow_spec/09_assay_node.md`
- `legacy/flow_spec/09a_assay_execution.md`
- `legacy/flow_spec/01a_routing_guards.md`
- `legacy/flow_spec/16_webhook_integration.md`
- `legacy/node_spec/00_overview.md`
- `legacy/node_spec/08_responsibilities.md`
- `legacy/node_spec/04_sdk_core.md`
- `legacy/node_spec/sidecar/00_overview.md`

Normative constraints:

- `AGENTS.md` key decisions
- `01-concepts/00-overview.md`
- `01-concepts/01-architecture.md`
- `01-concepts/02-data-model.md`
- `01-concepts/03-governance.md`
- `02-flow/00-overview.md`
- `02-flow/01-operator.md`
- `02-flow/02-workitem.md`

## Proposed Outline

1. **Node Runtime Role and Boundaries**
   - where node logic sits in the Flow lifecycle
   - what nodes can and cannot directly mutate
2. **Execution Contract (Node <-> Sidecar <-> Operator)**
   - request/response shape, routing result expectations, lifecycle handoff
3. **Capability Model and Authorization Surface**
   - `READ:*`, `WRITE:*`, `STAMP:*`, `ESCALATE:*` semantics
   - enforcement boundaries between Sidecar and Operator
4. **Reference Arrangement Node Responsibilities**
   - Forge, Quench, Appraise, Sort, Refine behavioural intent
   - reference arrangement guidance vs custom topology freedom
5. **Sort as Gate Node in the Reference Arrangement**
   - fixed routing logic and approval stamp behaviour
   - discovery of stamp-provider mappings from Flow configuration
6. **Assay as Standard Flow Component**
   - mandatory presence, routing triggers, authority ceiling
   - escalation behaviour and governance appeal path
7. **External Integration Nodes**
   - webhook/event-driven integrations and boundary safety
   - requirements for auditability, idempotency, and failure handling
8. **Failure and Retry Semantics at Node Boundary**
   - timeout, retry, and explicit failure signaling
   - distinction between node execution failures and governance deadlocks
9. **Node Telemetry and Friction Signals**
   - expected emissions and attribution tags
10. **Node Invariants Checklist**
   - guarantees consumed by configuration, operations, and node SDK docs

## Decisions That Must Be Explicit in `03-nodes-external`

- Forge reads laws for context seeding and does not write laws.
- Sort is the reference gate that applies `approval` only after all required conditions are met.
- Sort routing targets are discovered from configuration, not hardcoded by role names.
- Assay is always present in every Flow and is not an optional reference node.
- Assay can resolve Tier 1-2 conflicts, propose Tier 3 changes, and appeal Tier 4-5 issues.
- Stamp permissions are capability grants by artefact kind and stamp name (`STAMP:artefact/<kind>/<stamp-name>`).
- Stamp application is write-once per artefact version hash.
- External nodes still operate through Sidecar/Operator contracts and cannot bypass governance checks.
- Cross-flow export/import starts a separate Workitem lifecycle and is not intra-flow routing.

## Diagrams Planned for `03-nodes-external`

1. Reference arrangement node interaction graph
2. Node execution sequence (Operator <-> Sidecar <-> Node <-> system services)
3. Sort decision flowchart (feedback/deadlock/stamp/approval branches)

Mermaid rule reminder: in `flowchart` and `sequenceDiagram`, use `<br/>` for line breaks.

## Cross-Links to Add on First Mention

- `../01-concepts/00-overview.md`
- `../01-concepts/01-architecture.md`
- `../01-concepts/02-data-model.md`
- `../01-concepts/03-governance.md`
- `./00-overview.md`
- `./01-operator.md`
- `./02-workitem.md`
- `./04-system-services.md`
- `./05-configuration.md`
- `./06-cross-flow.md`
- `./07-operations.md`
- `../03-node/00-overview.md`
- `../03-node/01-sidecar.md`
- `../03-node/02-sdk-core.md`
- `../03-node/03-sdk-artefacts.md`
- `../03-node/04-sdk-legal.md`
- `../03-node/05-sdk-feedback.md`
- `../03-node/06-sdk-workitems.md`
- `../03-node/07-sdk-telemetry.md`
- `../03-node/08-configuration.md`
- `../03-node/09-patterns.md`
- `../04-reference/crds.md`
- `../04-reference/grpc-api.md`
- `../04-reference/error-catalog.md`

## Explicit "Do Not Say" List for `03-nodes-external`

- Do not present the Foundry Cycle reference arrangement as mandatory topology.
- Do not state or imply Forge writes laws.
- Do not state or imply Assay writes Tier 1 Findings.
- Do not treat `approval` as a built-in special stamp semantic.
- Do not imply nodes can bypass Sidecar/Operator write and routing contracts.
- Do not imply external integrations bypass governance checks.
- Do not use v1/v2 split framing.

## Acceptance Criteria for `03-nodes-external` Completion

- Fully aligned with `PLAN.md` guardrails and AGENTS key decisions.
- No contradictions with `02-flow/01-operator.md` or `02-flow/02-workitem.md`.
- Reference node responsibilities are explicit, bounded, and implementable.
- Capability model and stamp semantics are unambiguous.
- External integration behaviour preserves auditability and control-plane invariants.
- Cross-links cover all first-mention concepts with downstream detail docs.
