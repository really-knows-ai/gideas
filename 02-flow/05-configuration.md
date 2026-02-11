# Plan: `02-flow/05-configuration.md`

Status: planning only. Spec drafting has not started.

## Purpose

Define the normative behaviour-shaping configuration semantics for Flow runtime operation, while leaving field-by-field schema truth to `04-reference/crds.md`.

## Audience

- Flow architects defining topology and governance behaviour
- Platform operators managing Flow configuration lifecycle
- Runtime implementers enforcing config validation and reconciliation behaviour

## Inputs

Primary source material:

- `legacy/flow_spec/00a_architecture_configuration.md`
- `legacy/flow_spec/05_data_model_processes.md`
- `legacy/flow_spec/13_helm_configuration.md`
- `legacy/flow_spec/helm/values.yaml`
- `legacy/flow_spec/01_operator_overview.md`
- `legacy/flow_spec/cross_flow_collaboration/02_treaties.md`
- `legacy/node_spec/03_configuration_crd.md`
- `legacy/node_spec/03a_configuration_patterns.md`

Normative constraints:

- `AGENTS.md` key decisions
- `01-concepts/01-architecture.md`
- `01-concepts/02-data-model.md`
- `01-concepts/03-governance.md`
- `02-flow/00-overview.md`
- `02-flow/01-operator.md`
- `02-flow/02-workitem.md`
- `02-flow/03-nodes-external.md`
- `02-flow/04-system-services.md`

## Proposed Outline

1. **Configuration Authority Model**
   - this document as behaviour-semantic source of truth
   - relationship to `04-reference/crds.md` schema reference
2. **Flow-Level Configuration Surface (`FoundryFlow`)**
   - singleton assumptions and runtime scope boundaries
3. **Topology and Routing Configuration**
   - entry node, routable outputs, direct routing, and topology integrity constraints
4. **Terminal Configuration Model**
   - terminal contract definitions by artefact kind
   - node terminal binding and `complete()` eligibility
5. **Contract Semantics**
   - kind-keyed required stamp-name arrays
   - `[]` present-only semantics, `{}` no artefact requirements
   - all-of-kind validation when multiple artefacts share a required kind
6. **Stamp Grant and Capability Configuration**
   - `STAMP:artefact/<kind>/<stamp-name>` grant semantics
   - write-once per artefact version behaviour implications
7. **Reference Arrangement Defaults vs Custom Flow Design**
   - recommendations for Forge/Quench/Appraise/Sort/Refine
   - Assay mandatory inclusion and authority boundaries
8. **Cross-Flow Configuration Surface**
   - sibling trust assumptions, treaty configuration, directed trust edges
   - local authority vs provenance semantics for imported stamps
9. **Operational Policy Knobs**
   - timeout budgets, thrash limits, retention, and hearing/governance thresholds
10. **Validation and Admission Invariants**
   - required consistency checks, rejection conditions, and safe defaults
11. **Configuration Evolution Rules**
   - backward-compatible changes and migration expectations within v1

## Decisions That Must Be Explicit in `05-configuration`

- This file is normative for behaviour semantics; `04-reference/crds.md` remains field/schema truth.
- Terminal nodes are explicitly configured by contract reference; no auto-terminal inference from empty outputs.
- Only terminal nodes can call `complete()`, and contract selection is fixed by node binding.
- Terminal contracts are per artefact kind with explicit stamp-name requirements.
- Contract semantics:
  - `{"petition-draft": ["stamp-a", "stamp-b"]}` means listed stamps are required for that artefact kind
  - `{"audit-log": []}` means artefacts of that kind must exist, with no required stamps
  - `{}` means no artefact requirements
- If multiple artefacts of a required kind exist, all of them must satisfy that kind's requirements.
- Cross-flow export includes only artefact kinds listed by the selected terminal contract.
- Stamp names are convention; the platform does not attach special semantics to names like `approval`.
- Sort applies `approval` only as a reference-arrangement convention.
- Assay is a standard Flow component and cannot be omitted.
- Cross-flow stamp authority is topology-dependent (sibling-authoritative, treaty provenance-only until naturalisation/local checks).
- No v1/v2 split framing is permitted.

## Diagrams Planned for `05-configuration`

1. Configuration object relationship diagram (`FoundryFlow`, `FoundryNode`, contracts, capability grants)
2. Routing resolution diagram (output mapping and direct route validation)
3. Terminal contract evaluation flow (binding -> validation -> completion/export outcomes)

Mermaid rule reminder: in `flowchart` and `sequenceDiagram`, use `<br/>` for line breaks.

## Cross-Links to Add on First Mention

- `../01-concepts/01-architecture.md`
- `../01-concepts/02-data-model.md`
- `../01-concepts/03-governance.md`
- `./00-overview.md`
- `./01-operator.md`
- `./02-workitem.md`
- `./03-nodes-external.md`
- `./04-system-services.md`
- `./06-cross-flow.md`
- `./07-operations.md`
- `../03-node/08-configuration.md`
- `../03-node/09-patterns.md`
- `../04-reference/crds.md`
- `../04-reference/error-catalog.md`

## Explicit "Do Not Say" List for `05-configuration`

- Do not define terminal behaviour via `outputs: []` inference.
- Do not use role-based `requiredRoles` semantics for terminal validation.
- Do not assign built-in platform meaning to specific stamp names.
- Do not present the reference arrangement as mandatory topology.
- Do not treat treaties as bidirectional by default.
- Do not use v1/v2 split framing.

## Acceptance Criteria for `05-configuration` Completion

- Fully aligned with `PLAN.md` guardrails and AGENTS key decisions.
- No contradictions with `02-flow/01-operator.md`, `02-flow/02-workitem.md`, or `02-flow/06-cross-flow.md`.
- Behavior semantics are unambiguous and implementation-feasible.
- Validation failures and operator rejection conditions are clearly defined.
- Reference defaults and custom-topology freedom are both explicit.
- Downstream docs can safely defer behaviour semantics to this document.
