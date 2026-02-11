# Plan: `02-flow/07-operations.md`

Status: planning only. Spec drafting has not started.

## Purpose

Define how operators run, monitor, troubleshoot, and recover a Flow in production, including telemetry, alerting, incident response, backup/restore, and validation practices.

## Audience

- Platform operators and SRE teams
- Flow administrators responsible for runtime reliability
- Runtime implementers defining operability guarantees

## Inputs

Primary source material:

- `legacy/flow_spec/04_flow_monitor.md`
- `legacy/flow_spec/05_metrics_catalog.md`
- `legacy/flow_spec/05a_alerting_operations.md`
- `legacy/flow_spec/12_error_catalog.md`
- `legacy/flow_spec/14_disaster_recovery.md`
- `legacy/flow_spec/15_backup_and_restore.md`
- `legacy/flow_spec/18_platform_testing.md`
- `legacy/governance_spec/10_audit_logging.md`

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
- `02-flow/06-cross-flow.md`

## Proposed Outline

1. **Operational Scope and Reliability Goals**
   - runtime outcomes operators are accountable for
   - service-level expectations and control boundaries
2. **Telemetry Architecture**
   - Flow Monitor ingestion path
   - metrics, traces, and structured audit event streams
3. **Core Metrics and Friction Operations**
   - lifecycle and health metrics
   - friction ledger aggregation and attribution practices
4. **Alerting and Triage Playbooks**
   - alert classes and severity mapping
   - first-response diagnostics by symptom category
5. **Error Taxonomy and Operator Actions**
   - error families, source attribution, and remediation routing
6. **Backup and Recovery Boundaries**
   - service-owned backups vs infrastructure-owned backups
   - Librarian, Archivist metadata store, blob store, CRDs, and audit pipeline boundaries
7. **Disaster Recovery Procedures**
   - restore order and data consistency constraints
   - degraded-mode behaviour and recovery validation checks
8. **Upgrade and Change Management**
   - rolling upgrades, compatibility assumptions, and rollback posture
9. **Testing and Operational Verification**
   - conformance, failure-injection, and recovery drills
   - pre-release and continuous validation expectations
10. **Capacity and Cost Signals**
    - scaling pressure indicators for operator and system services
    - governance/friction cost visibility for operational decision-making
11. **Operations Invariants Checklist**
    - guarantees that downstream runbooks and references depend on

## Decisions That Must Be Explicit in `07-operations`

- Friction is a first-class operational signal and must be queryable by source attribution.
- Audit logging is mandatory runtime output, not optional tooling.
- Archivist storage split drives backup strategy:
  - SQLite provenance requires snapshot/restore procedures
  - blob bytes follow storage-backend durability/restore model
- Workitem CRDs live in etcd; etcd backup/restore is cluster-admin responsibility.
- Backup guidance must resolve legacy conflicts by clearly assigning ownership per data store.
- Recovery procedures must preserve governance integrity (stamps, feedback, law lineage).
- Cross-flow operations must preserve provenance chain and authority semantics from `02-flow/06-cross-flow.md`.
- Operational guidance must align with error catalog semantics and avoid undocumented failure codes.
- Testing strategy must include failure/recovery paths, not only happy-path throughput.

## Diagrams Planned for `07-operations`

1. Telemetry pipeline diagram (components -> Flow Monitor -> metrics/traces/log sinks)
2. Incident triage flowchart (symptom -> likely source -> first checks)
3. Backup/restore boundary map (service-owned vs infra-owned responsibility)

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
- `./06-cross-flow.md`
- `../03-node/07-sdk-telemetry.md`
- `../04-reference/error-catalog.md`
- `../04-reference/grpc-api.md`
- `../04-reference/crds.md`

## Explicit "Do Not Say" List for `07-operations`

- Do not treat friction as optional or "nice to have" telemetry.
- Do not merge contradictory backup guidance without explicit ownership boundaries.
- Do not imply Workitem CRD backup is handled by application services.
- Do not ignore governance data integrity during restore procedures.
- Do not invent error semantics outside the shared error catalog.
- Do not use v1/v2 split framing.

## Acceptance Criteria for `07-operations` Completion

- Fully aligned with `PLAN.md` guardrails and AGENTS key decisions.
- No contradictions with `02-flow/04-system-services.md` storage contracts.
- Backup/recovery responsibilities are explicit and operationally actionable.
- Metrics, alerting, and audit guidance are coherent and implementation-feasible.
- Incident and testing guidance covers both reliability and governance integrity.
- Cross-flow and governance-sensitive operations preserve authority and provenance semantics.
