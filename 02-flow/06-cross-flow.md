# Plan: `02-flow/06-cross-flow.md`

Status: planning only. Spec drafting has not started.

## Purpose

Define how sovereign Flows exchange work and provenance across boundaries, including trust topology, stamp authority, naturalisation, law integration, and escalation behaviour.

## Audience

- Flow architects designing inter-Flow collaboration
- Platform operators managing trust and import/export controls
- Runtime implementers of export/import and governance integration paths

## Inputs

Primary source material:

- `legacy/flow_spec/cross_flow_collaboration/01_overview.md`
- `legacy/flow_spec/cross_flow_collaboration/02_treaties.md`
- `legacy/flow_spec/cross_flow_collaboration/03_bundle_format.md`
- `legacy/flow_spec/cross_flow_collaboration/04_export.md`
- `legacy/flow_spec/cross_flow_collaboration/05_import.md`
- `legacy/flow_spec/cross_flow_collaboration/06_operations.md`
- `legacy/flow_spec/03_identity_and_federation.md`
- `legacy/Tier5.md`

Normative constraints:

- `AGENTS.md` key decisions
- `01-concepts/01-architecture.md`
- `01-concepts/02-data-model.md`
- `01-concepts/03-governance.md`
- `02-flow/00-overview.md`
- `02-flow/01-operator.md`
- `02-flow/02-workitem.md`
- `02-flow/04-system-services.md`
- `02-flow/05-configuration.md`

## Proposed Outline

1. **Cross-Flow Boundary Model**
   - sovereignty and copy-on-write lifecycle across Flow boundaries
2. **Export/Transfer/Import Lifecycle**
   - terminal-triggered export, transport, receiver-side import
3. **Trust Topologies**
   - sibling Flows under shared State Root
   - treaty-based exchange for non-sibling crossings
4. **Stamp Verifiability vs Local Authority**
   - cryptographic verification model
   - topology-dependent authority semantics
5. **Naturalisation Semantics**
   - when imported provenance is preserved-only
   - when local checks/stamps must establish authority
6. **Treaty Model and Directionality**
   - receiver-side trust policy
   - directed trust edges and bilateral exchange via two directed agreements
7. **Terminal Contract Export Scope**
   - export filtering by terminal-contract-listed kinds
   - empty-contract metadata-only export behaviour
8. **Law Integration Protocol**
   - semantic search + contradiction evaluation
   - tier-dependent conflict outcomes, including Tier 3 grace period
9. **Runtime Escalation Paths Across Flows**
   - Assay judicial role
   - appeals via Librarian channels for Tier 4/5 involvement
10. **Cross-Flow Failure Modes and Retries**
    - transfer failure handling, replay/idempotency, and audit requirements
11. **Cross-Flow Invariants Checklist**
    - guarantees consumed by config, operator, and operations docs

## Decisions That Must Be Explicit in `06-cross-flow`

- Cross-flow exchange uses copy-on-write; imported work starts a separate Workitem lifecycle.
- Sibling Flows do not require Treaties when sharing a Governance Flow trust root.
- Imported stamps are always cryptographically verifiable when chain validation succeeds.
- Local authority is topology-dependent:
  - sibling/shared-root: imported stamps are immediately authoritative
  - treaty/non-sibling: imported stamps are provenance-only until naturalisation/local checks
- Treaties are directed; bidirectional exchange requires two directed trust relationships.
- Terminal export scope is constrained by the selected terminal contract's artefact kinds.
- Law integration protocol enforces tiered supremacy outcomes:
  - Tier 1-2 conflicts retire immediately
  - Tier 3 conflicts allow grace period before forced integration
- Runtime law conflicts still route through Assay; supremacy informs but does not bypass judicial process.
- Assay may resolve Tier 1-2, propose Tier 3, and appeal Tier 4-5 via governance channels.

## Diagrams Planned for `06-cross-flow`

1. Cross-Flow lifecycle diagram (Export -> Transfer -> Import)
2. Trust topology diagram (sibling shared-root vs treaty-directed edges)
3. Law integration and escalation flow diagram (integration path + runtime appeal path)

Mermaid rule reminder: in `flowchart` and `sequenceDiagram`, use `<br/>` for line breaks.

## Cross-Links to Add on First Mention

- `../01-concepts/01-architecture.md`
- `../01-concepts/02-data-model.md`
- `../01-concepts/03-governance.md`
- `./00-overview.md`
- `./01-operator.md`
- `./02-workitem.md`
- `./04-system-services.md`
- `./05-configuration.md`
- `./07-operations.md`
- `../03-node/08-configuration.md`
- `../03-node/09-patterns.md`
- `../04-reference/crds.md`
- `../04-reference/grpc-api.md`
- `../04-reference/error-catalog.md`

## Explicit "Do Not Say" List for `06-cross-flow`

- Do not claim imported stamps are always non-authoritative.
- Do not claim imported stamps are always authoritative.
- Do not require Treaties for sibling Flows under shared trust root.
- Do not treat Treaties as bidirectional by default.
- Do not present export/import as intra-Flow routing.
- Do not use v1/v2 split framing.

## Acceptance Criteria for `06-cross-flow` Completion

- Fully aligned with `PLAN.md` guardrails and AGENTS key decisions.
- No contradictions with `01-concepts/03-governance.md` or `02-flow/05-configuration.md`.
- Verifiability and authority semantics are clearly separated.
- Treaty and sibling topologies are unambiguous and implementable.
- Law integration protocol and runtime escalation paths are clearly specified.
- Export scope behaviour is consistent with terminal contract semantics.
