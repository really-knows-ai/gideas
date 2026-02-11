# Plan: `02-flow/00-overview.md`

Status: planning only. Spec drafting has not started.

## Purpose

Define the Flow runtime from an operator/platform perspective: what components exist, how they collaborate at runtime, and where hard boundaries and guarantees live.

## Audience

- Flow operators
- Platform administrators
- Flow architects defining topology and control policy

## Inputs

Primary source material:

- `legacy/flow_spec/00_architecture_overview.md`
- `legacy/flow_spec/10_reference_flow_haiku.md`
- `legacy/flow_spec/02_system_services_overview.md`
- `legacy/flow_spec/01_operator_overview.md`
- `legacy/Tier5.md`

Normative constraints:

- `AGENTS.md` key decisions
- `01-concepts/00-overview.md`
- `01-concepts/01-architecture.md`
- `01-concepts/02-data-model.md`
- `01-concepts/03-governance.md`

## Proposed Outline

1. **What `02-flow` Specifies**
   - runtime scope and responsibility boundary for platform operators
2. **Runtime Composition**
   - Operator, system services, external nodes, built-in Assay
3. **End-to-End Runtime Loop**
   - assignment cycle, node execution, sidecar mediation, Archivist/Librarian interactions
4. **Reference Arrangement vs Custom Topology**
   - Foundry Cycle as reference arrangement, configurable topology under fixed invariants
5. **Governance Runtime Mechanics**
   - read/write law authority boundaries, Sort gate role, Assay authority ceiling
6. **Boundary Semantics**
   - intra-flow routing vs cross-flow export/import
   - verifiability vs local authority for imported stamps
7. **Operational Signal Surface**
   - friction, telemetry, audit as first-class runtime outputs
8. **Runtime Invariants Checklist**
   - explicit guarantees refined in downstream `02-flow` docs

## Diagrams Planned for `00-overview`

1. Runtime component interaction flowchart
2. Workitem lifecycle sequence (Operator <-> Node/Sidecar <-> Archivist/Librarian)
3. Boundary model (local routing vs cross-flow export/import)

Mermaid rule reminder: in `flowchart` and `sequenceDiagram`, use `<br/>` for line breaks.

## Cross-Links to Add on First Mention

- `../01-concepts/00-overview.md`
- `../01-concepts/01-architecture.md`
- `../01-concepts/02-data-model.md`
- `../01-concepts/03-governance.md`
- `./01-operator.md`
- `./02-workitem.md`
- `./03-nodes-external.md`
- `./04-system-services.md`
- `./05-configuration.md`
- `./06-cross-flow.md`
- `./07-operations.md`
- `../03-node/00-overview.md`
- `../04-reference/crds.md`

## Explicit "Do Not Say" List for `00-overview`

- Do not use v1/v2 split framing.
- Do not state or imply Forge writes laws.
- Do not treat `approval` as a built-in special stamp semantic.
- Do not place feedback/passports on Workitem status.

## Acceptance Criteria for `00-overview` Completion

- Fully aligned with guardrails in `PLAN.md`.
- No contradictions with any `01-concepts` document.
- First-mention links present for all concepts with detail pages.
- Clearly distinguishes:
  - reference arrangement vs configurable topology
  - local routing vs cross-flow export/import
  - cryptographic verifiability vs local governance authority
- All described mechanisms are implementable without hidden assumptions.
