# Sidecar Boundary

## Goal

- Define Sidecar as the non-optional authentication and mediation layer between node code and Flow runtime services.
- Specify transport-security and identity-brokering responsibilities at the SDK boundary.
- Keep policy and authorisation authority with Operator, Archivist, and Librarian.

## Sidecar as a Mandatory Runtime Component

- Every node pod includes a Sidecar that brokers all authenticated runtime operations.
- Node containers do not hold Flow runtime identity credentials and cannot call system services directly.
- Sidecar availability is a prerequisite for SDK communication with Flow runtime services.
- Node code may call external business services over the data-plane network path, but this never grants direct access to Flow runtime services.
- Bypass paths are invalid by design, even when network connectivity exists.

## Identity and Trust Mediation

- Sidecar holds runtime identity material and presents authenticated service calls on behalf of the node.
- Sidecar binds outgoing requests to node identity and Workitem metadata.
- Stamp and governance actions are mediated through Sidecar so trust-chain evidence remains verifiable.
- Cross-flow provenance verification and local authority remain separate concepts at the boundary.

## Workitem Scoping at the SDK Boundary

- SDK calls include Workitem-scoped metadata that Sidecar forwards to target services.
- Operator, Archivist, and Librarian decide request admissibility from current Workitem state and node identity.
- Requests missing required Workitem scope metadata are rejected by the target service.
- Sidecar does not create a separate assignment object; Workitem state is the source of truth.

## Service Authorisation

- Sidecar authenticates and proxies requests; it does not make final authorisation decisions.
- Operator authorises control-plane mutations and routing/completion actions.
- Archivist authorises artefact, feedback, and stamp operations, including Contempt Guard checks.
- Librarian authorises law-read and law-write operations.
- Capability grants from FoundryNode are evaluated by target services.

## Service Brokering Contract

- Sidecar -> Operator for routing instruction submission and control-plane mutation requests.
- Sidecar -> Archivist for artefact bytes, version history, feedback, and stamp provenance operations.
- Sidecar -> Librarian for law retrieval and governance context queries.
- Sidecar -> telemetry surfaces for mediation and transport observability.
- Sidecar never transfers control-plane ownership to node code.

## Failure and Fail-Closed Behaviour

- Sidecar authentication failure rejects the request before proxying.
- Service-side authorisation denial returns structured error and no state change.
- Unavailable dependency (Operator/Archivist/Librarian) -> fail closed on affected governance path.
- Invalid instruction shape or out-of-scope mutation is rejected by the authoritative service.
- Timeout and thrash outcomes are decided by Operator guard logic.

## Health, Activity, and Telemetry Signals

- Sidecar exposes health/readiness state for runtime selection and pod liveness workflows.
- Sidecar emits operational telemetry and logs for mediation outcomes and transport failures.
- Authoritative governance audit for state changes is emitted by the service that accepted, rejected, or applied the change.
- Observability signals from Sidecar are mandatory runtime outputs, not optional instrumentation.

## Sidecar Invariants

- Sidecar is the sole authenticated mediation path for node-originated runtime operations.
- Sidecar is an authentication and mediation boundary, not a policy authority.
- Authorisation is service-owned and enforced by Operator, Archivist, and Librarian.
- Workitem state (`assignedNode`, lifecycle) remains the source of truth for request admissibility.
- Governance-integrity failures never fail open.
- Sidecar does not own control-plane persistence; Operator remains final authority.
