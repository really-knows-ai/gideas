# Plan: `02-flow/02-workitem.md`

Status: planning only. Spec drafting has not started.

## Purpose

Define the Workitem runtime contract for the Flow layer: ownership, lifecycle, routing instructions, and terminal interaction boundaries between the Operator, Sidecar, Nodes, and Archivist.

## Audience

- Flow operators and platform administrators
- Runtime implementers maintaining control-plane behaviour
- Node/Sidecar implementers who rely on Workitem semantics

## Inputs

Primary source material:

- `legacy/flow_spec/06_data_model_work.md`
- `legacy/flow_spec/06a_data_model_feedback.md`
- `legacy/flow_spec/05_data_model_processes.md`
- `legacy/flow_spec/01c_assignment_flow.md`
- `legacy/flow_spec/01a_routing_guards.md`
- `legacy/flow_spec/02c_archivist.md`

Normative constraints:

- `AGENTS.md` key decisions
- `01-concepts/00-overview.md`
- `01-concepts/02-data-model.md`
- `01-concepts/03-governance.md`
- `02-flow/00-overview.md`
- `02-flow/01-operator.md`

## Proposed Outline

1. **Workitem Role in Runtime State**
   - Workitem as control-plane state anchor
   - what the Workitem is and is not responsible for
2. **Ownership and Mutability Boundaries**
   - immutable `spec`
   - Operator-owned assignment/lifecycle fields
   - Sidecar-owned routing and artefact reference writes
3. **Lifecycle States and Transitions**
   - `Pending`, `Running`, `Completed`, `Failed`
   - transition guards and failure paths
4. **Routing Instruction Contract**
   - `route_to_output`, `route_to`, `complete`
   - validation requirements and rejection behaviour
5. **Thrash Guard vs Feedback Deadlock**
   - infrastructure loop detection (visit budget)
   - governance deadlock escalation (feedback depth via Archivist state)
6. **Artefact Reference Model**
   - Workitem stores artefact references only (`id`, `kind`)
   - version/stamp/feedback provenance lives in Archivist
7. **Terminal Contract Interaction**
   - `complete()` only from terminal nodes
   - per-kind requirement checks and all-of-kind semantics
   - completion behaviour when requirements are unsatisfied
8. **Context and Reserved Keys**
   - underscore-prefix system key reservation
   - pointer to Node SDK doc for reserved key enumeration
9. **Retention and Finalization Semantics**
   - terminal state persistence window
   - handoff to operational cleanup policy
10. **Workitem Invariants Checklist**
   - guarantees required by Operator, Sidecar, and SDK docs

## Decisions That Must Be Explicit in `02-workitem`

- Feedback does not live on Workitem status; feedback is persisted in Archivist.
- Passports/stamps and version history do not live on Workitem status; they are Archivist-owned.
- `currentAssignee` is scalar: one Workitem, one assigned node at a time.
- Thrash Guard enforcement uses total visits across all nodes, with per-node counters for diagnostics.
- Terminal requirements are per artefact kind; if multiple artefacts of a required kind exist, all must satisfy that kind's requirements.
- A contract with no artefact entries imposes no artefact requirements.
- Export filtering is based on terminal contract kind entries (empty contract exports metadata only).
- Workitem `context` keys starting with `_` are reserved for system use.

## Diagrams Planned for `02-workitem`

1. Workitem lifecycle state diagram with guard-triggered transitions
2. Data ownership diagram (Workitem vs Archivist)
3. Completion validation sequence (`complete()` through Operator contract checks)

Mermaid rule reminder: in `flowchart` and `sequenceDiagram`, use `<br/>` for line breaks.

## Cross-Links to Add on First Mention

- `../01-concepts/00-overview.md`
- `../01-concepts/02-data-model.md`
- `../01-concepts/03-governance.md`
- `./00-overview.md`
- `./01-operator.md`
- `./04-system-services.md`
- `./05-configuration.md`
- `./06-cross-flow.md`
- `../03-node/01-sidecar.md`
- `../03-node/02-sdk-core.md`
- `../03-node/06-sdk-workitems.md`
- `../04-reference/crds.md`
- `../04-reference/error-catalog.md`

## Explicit "Do Not Say" List for `02-workitem`

- Do not place feedback, stamps, or version history on Workitem status.
- Do not model artefact identity as `kind + name` instead of stable per-Workitem `id`.
- Do not describe stamp requirements as role-based `requiredRoles` semantics.
- Do not imply concurrent multi-node assignment for a single Workitem.
- Do not use v1/v2 split framing.

## Acceptance Criteria for `02-workitem` Completion

- Fully aligned with `PLAN.md` guardrails and AGENTS key decisions.
- No contradictions with `01-concepts/02-data-model.md` or `02-flow/01-operator.md`.
- Field ownership and mutability are explicit and non-overlapping.
- Lifecycle transitions, guard conditions, and failure reasons are deterministic.
- Workitem vs Archivist responsibility split is unambiguous.
- Terminal interaction and export implications are clearly specified.
