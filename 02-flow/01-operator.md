# Plan: `02-flow/01-operator.md`

Status: planning only. Spec drafting has not started.

## Purpose

Define the Flow Operator as the control-plane authority for reconciliation, assignment, routing, and terminal validation, including trust bootstrapping responsibilities in federated deployments.

## Audience

- Flow operators and platform administrators
- Runtime implementers maintaining controller behaviour
- Flow architects who need predictable routing and completion semantics

## Inputs

Primary source material:

- `legacy/flow_spec/01_operator_overview.md`
- `legacy/flow_spec/01a_routing_guards.md`
- `legacy/flow_spec/01b_operator_reconciliation.md`
- `legacy/flow_spec/01b_timeout_and_thrash.md`
- `legacy/flow_spec/01c_assignment_flow.md`
- `legacy/flow_spec/05_data_model_processes.md`
- `legacy/flow_spec/03_identity_and_federation.md`
- `legacy/governance_spec/01_certificate_authority.md`

Normative constraints:

- `AGENTS.md` key decisions
- `01-concepts/01-architecture.md`
- `01-concepts/02-data-model.md`
- `01-concepts/03-governance.md`
- `02-flow/00-overview.md`

## Proposed Outline

1. **Operator Role and Boundaries**
   - control-plane scope, non-responsibilities, and ownership lines
2. **Reconciliation Surfaces**
   - `FoundryFlow` singleton config reconciliation
   - `FoundryNode` reconciliation to workloads/services/policies
3. **Assignment Lifecycle**
   - Workitem queueing, node selection, assignment handoff, and completion reporting
4. **Routing and Guard Evaluation**
   - routing instruction handling (`route_to_output`, `route_to`, `complete`)
   - guard ordering: target validity, timeout, thrash, terminal constraints
5. **Terminal Contract Enforcement**
   - terminal-node-only `complete()`
   - bound-contract lookup and per-kind stamp requirement checks
   - completion failure behaviour and error reporting
6. **Failure Handling and Recovery Semantics**
   - retry model, node unavailability, timeout transitions, thrash termination
7. **Trust and Identity Responsibilities**
   - local certificate authority role
   - sibling annexation handshake and intermediate CA issuance path
8. **Telemetry and Audit Emissions**
   - operator-originated lifecycle events and required metrics/traces
9. **Operator Invariants Checklist**
   - explicit behaviour guarantees referenced by other `02-flow` docs

## Decisions That Must Be Explicit in `01-operator`

- Terminal status is configuration-bound (`terminal` contract reference), not inferred from empty outputs.
- Only terminal nodes may call `complete()`; non-terminal calls fail.
- Terminal contract validation is performed by the Operator.
- Contract checks are per artefact kind and apply to all artefacts of that kind.
- Thrash Guard enforcement uses total visit count across all nodes.
- Sort routing targets for missing stamps are discovered from Flow configuration, not hardcoded by node role.

## Diagrams Planned for `01-operator`

1. Reconciliation loop diagram (`FoundryFlow`, `FoundryNode`, `Workitem` watchers)
2. Assignment and routing sequence (Operator <-> Sidecar/Node)
3. `complete()` validation sequence with terminal contract checks

Mermaid rule reminder: in `flowchart` and `sequenceDiagram`, use `<br/>` for line breaks.

## Cross-Links to Add on First Mention

- `../01-concepts/01-architecture.md`
- `../01-concepts/02-data-model.md`
- `../01-concepts/03-governance.md`
- `./00-overview.md`
- `./02-workitem.md`
- `./04-system-services.md`
- `./05-configuration.md`
- `./06-cross-flow.md`
- `../03-node/01-sidecar.md`
- `../04-reference/crds.md`
- `../04-reference/error-catalog.md`

## Explicit "Do Not Say" List for `01-operator`

- Do not define terminal nodes by `outputs: []` inference.
- Do not state or imply Sidecar performs terminal contract validation.
- Do not hardcode missing-stamp routing to fixed node names.
- Do not describe feedback, passports, or version history as Workitem-owned data.
- Do not use v1/v2 split framing.

## Acceptance Criteria for `01-operator` Completion

- Fully aligned with `PLAN.md` guardrails and AGENTS key decisions.
- No contradictions with any `01-concepts` document or `02-flow/00-overview.md`.
- Routing guard order and failure transitions are deterministic and implementable.
- Reconciliation ownership is explicit and non-overlapping.
- Terminal contract validation behaviour is unambiguous, including error paths.
- Trust/annexation responsibilities are stated at operator level without leaking into unrelated docs.
