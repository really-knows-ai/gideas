# Sidecar Boundary

## Goal

- Define Sidecar as the non-optional enforcement layer between node code and Flow runtime services.
- Specify identity, capability, and assignment-scoping responsibilities in one coherent boundary model.
- Make fail-closed behaviour explicit for governance-integrity paths.

## Sidecar as a Mandatory Runtime Component

- Every node pod includes a Sidecar that brokers all authenticated runtime operations.
- Node containers do not hold Flow runtime identity credentials and cannot call system services directly.
- Sidecar availability is a prerequisite for assignment execution and state mutation submission.
- Node code may call external business services over the data-plane network path, but this never grants direct access to Flow runtime services.
- Bypass paths are invalid by design, even when network connectivity exists.

## Identity and Trust Mediation

- Sidecar holds runtime identity material and presents authenticated service calls on behalf of the node.
- Sidecar binds identity to assignment scope, preventing cross-assignment impersonation.
- Stamp and governance actions are mediated through Sidecar so trust-chain evidence remains verifiable.
- Cross-flow provenance verification and local authority remain separate concepts at the boundary.

## Assignment Lease and Workitem Scoping

- Sidecar receives and enforces assignment lease context from Operator.
- SDK calls are automatically scoped to the leased Workitem identity.
- Requests that target unleased or foreign Workitem state are rejected.
- Lease expiry or invalidation terminates further assignment-scoped mutation attempts.

## Capability Enforcement

- Sidecar evaluates capability grants before forwarding node-originated operations.
- Enforcement applies across artefact reads/writes, feedback actions, legal operations, and telemetry submission.
- Stamp authority is exact-scope (`STAMP:artefact/<kind>/<stamp-name>`) and respects write-once-per-version behaviour.
- Topology/config discovery paths require explicit `READ:flow` authorisation.

## Service Brokering Contract

- Sidecar -> Operator for routing instruction submission and control-plane mutation requests.
- Sidecar -> Archivist for artefact bytes, version history, feedback, and stamp provenance operations.
- Sidecar -> Librarian for law retrieval and governance context queries.
- Sidecar -> citation and telemetry surfaces for evidence capture and observability signals.
- Sidecar never transfers control-plane ownership to node code.

## Failure and Fail-Closed Behaviour

- Missing capability -> immediate denied operation with structured error.
- Unavailable dependency (Operator/Archivist/Librarian) -> fail closed on affected governance path.
- Invalid instruction shape or out-of-scope mutation -> reject and preserve current control-plane state.
- Assignment timeout/inactivity paths return explicit failure outcomes; silent success is prohibited.

## Health, Activity, and Audit Signals

- Sidecar exposes health/readiness state for runtime selection and pod liveness workflows.
- Assignment activity signals support timeout supervision and operational triage.
- Sidecar emits audit-visible boundary events for denied operations, state submissions, and failure decisions.
- Observability signals from Sidecar are mandatory runtime outputs, not optional instrumentation.

## Sidecar Invariants

- Sidecar is the sole authenticated mediation path for node-originated runtime operations.
- Capability enforcement is deterministic and deny-by-default.
- Assignment scope is strict and cannot cross Workitem boundaries.
- Governance-integrity failures never fail open.
- Sidecar does not own control-plane persistence; Operator remains final authority.
