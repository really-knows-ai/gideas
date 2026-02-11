# Plan: `02-flow/04-system-services.md`

Status: planning only. Spec drafting has not started.

## Purpose

Define the runtime system services that power Flow behaviour, with clear ownership boundaries, data contracts, and governance lifecycle responsibilities.

## Audience

- Platform operators and administrators
- Runtime/service implementers
- Flow architects who need predictable platform behaviour

## Inputs

Primary source material:

- `legacy/flow_spec/02_system_services_overview.md`
- `legacy/flow_spec/02a_librarian.md`
- `legacy/flow_spec/02b_law_search.md`
- `legacy/flow_spec/02c_archivist.md`
- `legacy/flow_spec/02e_backup_service.md`
- `legacy/flow_spec/07_living_law_mechanisms.md`
- `legacy/flow_spec/04_flow_monitor.md`
- `legacy/Tier5.md`

Normative constraints:

- `AGENTS.md` key decisions
- `01-concepts/01-architecture.md`
- `01-concepts/02-data-model.md`
- `01-concepts/03-governance.md`
- `02-flow/00-overview.md`
- `02-flow/01-operator.md`
- `02-flow/02-workitem.md`
- `02-flow/03-nodes-external.md`

## Proposed Outline

1. **Service Landscape and Planes**
   - service list, runtime role, and non-overlapping responsibility boundaries
2. **Librarian (Law Lifecycle Service)**
   - law storage, representation handling, retrieval responsibilities
   - integration checks and escalation hooks for higher-tier law changes
3. **Law Search Surface**
   - query responsibilities, consistency expectations, and scaling model
4. **Archivist (Artefact Lifecycle Service)**
   - SQLite provenance model (versions, passports, feedback)
   - blob-store content model keyed by content hash
   - read/write/query boundaries via Sidecar and SDK
5. **Flow Monitor and Friction Ledger Surface**
   - telemetry ingestion, audit stream, friction attribution and aggregation
6. **Citation and Hearing Lifecycle Integration**
   - citation accumulation and citation-threshold hearing triggers (Citation Processor)
   - TTL-expiry and near-expiry hearing triggers (Librarian)
   - verdict application contracts and Assay evidence query path to Citation Processor
7. **Backup and Recovery Service Boundaries**
   - what is backed up by platform services vs infrastructure operators
8. **Inter-Service Contracts**
   - call paths and data dependencies among Operator, Sidecar, Librarian, Archivist, and monitor
9. **Failure and Degradation Modes**
   - service unavailability behaviour and required fail-safe semantics
10. **System Service Invariants Checklist**
   - guarantees consumed by configuration, operations, and reference docs

## Decisions That Must Be Explicit in `04-system-services`

- Archivist is authoritative for artefact provenance beyond raw bytes.
- Workitem CRD stores artefact references only (`id`, `kind`); feedback and stamps are Archivist-owned.
- Archivist storage split is normative:
  - SQLite for version history, feedback, passport stamps
  - blob store for content bytes by content hash
- Laws are single objects with one goal and multiple representations; whole-law versioning applies.
- Law representations are equivalent expressions of one goal, not linked sibling law objects.
- Hearing verdict schemas are tier-specific and must match AGENTS decisions.
- Hearing trigger ownership is split by condition:
  - citation threshold crossings trigger hearings from the Citation Processor
  - Tier 1 and Tier 2 TTL-expiry paths trigger hearings from the Librarian
- Hearings are a cross-component lifecycle (Librarian, Citation Processor, Assay, Operator), not a standalone hearing service.
- In both hearing paths, Assay queries the Citation Processor for supporting evidence.
- Friction is a first-class operational signal with source attribution.
- Tiered law integration protocol and escalation paths (including Tier 3 grace period behaviour) are preserved.

## Diagrams Planned for `04-system-services`

1. Service interaction map across control/governance/data planes
2. Archivist data model split (SQLite provenance vs blob bytes)
3. Law lifecycle path (citation/TTL trigger -> hearing -> verdict -> law lifecycle update)

Mermaid rule reminder: in `flowchart` and `sequenceDiagram`, use `<br/>` for line breaks.

## Cross-Links to Add on First Mention

- `../01-concepts/01-architecture.md`
- `../01-concepts/02-data-model.md`
- `../01-concepts/03-governance.md`
- `./00-overview.md`
- `./01-operator.md`
- `./02-workitem.md`
- `./03-nodes-external.md`
- `./05-configuration.md`
- `./06-cross-flow.md`
- `./07-operations.md`
- `../03-node/01-sidecar.md`
- `../03-node/02-sdk-core.md`
- `../03-node/03-sdk-artefacts.md`
- `../03-node/04-sdk-legal.md`
- `../03-node/05-sdk-feedback.md`
- `../03-node/07-sdk-telemetry.md`
- `../04-reference/crds.md`
- `../04-reference/grpc-api.md`
- `../04-reference/error-catalog.md`

## Explicit "Do Not Say" List for `04-system-services`

- Do not treat laws as grouped linked CRDs.
- Do not store passports as blob-sidecar JSON files.
- Do not place feedback on Workitem status.
- Do not present Forge as a law-writing actor.
- Do not frame friction as optional or secondary telemetry.
- Do not imply a standalone hearing service owns both trigger paths and adjudication.
- Do not use v1/v2 split framing.

## Acceptance Criteria for `04-system-services` Completion

- Fully aligned with `PLAN.md` guardrails and AGENTS key decisions.
- No contradictions with `01-concepts/02-data-model.md` or `01-concepts/03-governance.md`.
- Service ownership boundaries are explicit and non-overlapping.
- Data contracts between services are implementation-feasible and testable.
- Law lifecycle and hearing semantics are unambiguous.
- Trigger ownership is explicit and aligned with concepts (`Citation Processor` for citation threshold, `Librarian` for TTL-expiry paths).
- Archivist model is consistent with all Workitem and SDK-facing behaviour.
