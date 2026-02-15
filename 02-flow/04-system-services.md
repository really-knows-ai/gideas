# System Services

System services provide the runtime substrate for law lifecycle, artefact lifecycle, governance signals, and operational resilience.

## Service Landscape and Boundaries

Each service owns one primary concern:

- **Librarian**: law storage, retrieval, representation lifecycle, tier integration, and law lifecycle hearing triggers (both friction-threshold and TTL-proximity).
- **Archivist**: artefact lifecycle and provenance beyond Workitem references.
- **Flow Monitor**: telemetry aggregation, friction signal surfacing, and audit stream integration.
- **Backup surfaces**: service-owned backup scope for embedded stores and content stores, coordinated with infrastructure-level backup ownership.
- **Flow Support Services**: optional, Flow-Architect-deployed containers that expose pluggable gRPC capabilities consumed by nodes (via [Sidecar](../03-node/01-sidecar.md) mediation) and system services (directly). Codification Services are the worked example in this spec.

No service duplicates another service's source of truth.

```mermaid
flowchart TD
    OP["Operator"] --> LB["Librarian"]
    OP --> AR["Archivist"]

    SC["Sidecar"] --> LB
    SC --> AR

    SC --> SS["Support Services<br/>(Flow Architect deployed)"]

    LB --> FM["Flow Monitor"]
    AR --> FM
    OP --> FM

    SS --> FM

    LB <-->|"law sync and appeals"| XFL["Cross-flow Librarian"]
```

## Librarian

The Librarian is the law lifecycle service for a Flow.

### Law Model

- A law is one object with one textual goal and one-or-more representations.
- Representations express the same goal in different forms (prose, formal logic, executable forms, and others).
- Any mutation to goal, representations, or lifecycle metadata creates a new whole-law version identified by content hash.
- Representations are not independently versioned laws and are not linked sibling-law objects.

### Retrieval and Serving

Each law carries an `appliesTo` field — a list of zero or more governed artefact kinds. An empty `appliesTo` means the law is global and applies to all artefact kinds in the Flow.

The Librarian serves law queries through the [Sidecar](../03-node/01-sidecar.md) (for nodes) and direct service-to-service gRPC (for system actors). Three query modes are supported:

- **All laws** — no filter. Returns every law in the Flow's Library.
- **By artefact kind** — returns laws whose `appliesTo` includes the queried kind, plus all global laws.
- **By artefact kind + representation type** — same kind filter, plus the law must have at least one representation of the requested type. Laws without a matching representation type are excluded.

All three modes return full law objects (goal, all representations, tier, metadata). Filters gate which laws are included in the result; they never strip representations from returned law objects.

Tier is part of legal authority, but retrieval remains one law body with one identity model — all tiers are returned together.

### Integration and Conflict Checks

When higher-tier laws arrive from cross-flow replication, the Librarian performs a two-stage conflict protocol:

1. Semantic search for candidate contradictions, scoped by `appliesTo` — a law governing `"haiku"` is not conflict-checked against a law governing `"python-source"`. Global laws are conflict-checked against all laws regardless of scope.
2. LLM contradiction evaluation of candidates to determine actual contradiction.

Integration outcomes follow tiered supremacy semantics:

- Conflicting local Tier 1-2 laws retire immediately.
- Conflicting local Tier 3 laws enter HITL-controlled grace period flow when requested.
- On grace expiry, incoming law integrates automatically and conflicting Tier 3 law retires.
- If the LLM evaluator is unavailable or returns an indeterminate result, incoming higher-tier laws remain queued and inactive until evaluation succeeds.

### Law Lifecycle Hearing Triggers

The Librarian owns all hearing trigger emission for law lifecycle events. It monitors two signals and triggers review hearings by requesting Workitem creation through the Operator.

**Friction-threshold triggers:** The Librarian periodically queries the [Flow Monitor](#flow-monitor-and-friction-surface) for accumulated friction attributed to individual laws. When a Tier 1 Finding's friction crosses a configured threshold, the Librarian triggers a review hearing. Thresholds are configurable per law tier in the FoundryFlow [configuration](./05-configuration.md).

**TTL-proximity triggers:** When a law enters a configurable window before its TTL expiry, the Librarian triggers the appropriate review hearing:

- Tier 1 Finding nearing expiry -> request creation of a Workitem for review-hearing processing through the Operator, carrying hearing artefacts including `lawId`.
- Tier 2 Ruling nearing expiry -> request creation of a Workitem for review-hearing processing through the Operator, carrying hearing artefacts including `lawId`.

There is no TTL reset. Hearings produce a decisive outcome — promote, retire, or demote.

Librarian does not adjudicate hearings.

## Archivist

The Archivist is the artefact lifecycle service and authoritative provenance store.

### Storage Split

Archivist storage is normatively split into two layers:

- **SQLite**: artefact version history, passport stamps, and feedback.
- **Blob store**: raw artefact bytes keyed by content hash, typically on fast PVC-backed storage and optionally on cloud object storage.

```mermaid
flowchart LR
    WI["Workitem CRD<br/>artefact id + kind"] --> ARS["Archivist service"]
    SC["Sidecar + SDK"] --> ARS
    ARS --> SQ["Archivist SQLite<br/>versions stamps feedback"]
    ARS --> BL["Blob store (PVC/object)<br/>content by hash"]
```

### Workitem Boundary

- Workitem CRDs carry artefact references only: `id` and `kind`.
- Feedback does not live on Workitem status.
- Passports and stamps do not live on Workitem status.
- Artefact version history does not live on Workitem status.

### Access Contract

- Nodes never call Archivist directly.
- SDK calls are mediated by the [Sidecar](../03-node/01-sidecar.md).
- Query and write operations enforce capability boundaries configured in FoundryNode.
- The [Flow Operator](./01-operator.md) maintains a direct service-level query path to the Archivist for exit contract validation and Workitem lifecycle coordination — this is distinct from the Sidecar-mediated path that nodes use.

## Flow Monitor and Friction Surface

Flow Monitor aggregates runtime observability signals:

- Metrics from Operator, Sidecars, nodes, and services.
- Traces for assignment, routing, service calls, and completion paths.
- Audit event stream for governance-relevant state transitions.

Friction is a first-class signal:

- Friction events carry a magnitude and are purely additive — every emission adds to the total. Aggregation and analysis happen post-hoc.
- Each event is attributed to a Workitem and the emitting node. Callers may optionally tag one or more law identifiers to attribute friction to specific governance rules.
- The Flow Monitor aggregates friction data across multiple axes: per-node, per-law, per-tier, and per-topology-path.
- Friction is not optional instrumentation; it is a mandatory runtime output surface.

Three SDK operations emit friction transparently as mandatory side effects:

- [`Cite(law_ids)`](../04-sdk/03-sdk-legal.md#citation) emits a fixed low magnitude attributed to the cited laws.
- [`AddFeedback`](../04-sdk/04-sdk-feedback.md#feedback-friction) emits magnitude equal to the feedback depth for that item (1, 2, ..., n).
- [Assay jury rounds](../01-concepts/04-governance.md#judicial-review-assay) emit magnitude = depth ^ (round + 1) per round; HITL escalation emits depth ^ (rounds * 2).

These mandatory emissions ensure that governance cost is captured regardless of node implementation choices. Nodes may also call `AddFriction` directly for domain-specific costs.

## Flow Support Services

Flow Support Services are optional containers deployed by the Flow Architect that expose gRPC capabilities to nodes and system services. They run in the Flow namespace — pluggable, replaceable, and Flow-Architect-owned.

Support Services do not process Workitems — they expose gRPC capabilities consumed by nodes and system services through different access paths:

- Nodes consume Support Services through [Sidecar](../03-node/01-sidecar.md) mediation, preserving the platform invariant that nodes never call services directly. Assay is a node and accesses Support Services through its Sidecar.
- System services discover and consume Support Services via the Flow configuration and direct service-to-service gRPC.

Support Services are declared via their own CRD, which specifies:

- The capabilities the service provides (e.g., `encode` for Codification Services).
- Infrastructure configuration: PVC mounts, deployment strategy (ReplicaSet default, StatefulSet as an option), resource limits.
- Health and readiness endpoints (`healthz`/`readyz`).

The [Operator](./01-operator.md) manages Support Service deployments. Default deployment strategy is ReplicaSet with minimum replicas of 0, allowing the Operator to scale services down when unused. Stateful services or services that cannot scale to zero can override the minimum replica count. Support Services must implement standard `healthz`/`readyz` endpoints for Operator health management and pod lifecycle.

The FoundryFlow [configuration](./05-configuration.md) grants consuming nodes access to Support Service capabilities using `USE:support/<service>/<capability>` syntax (e.g., `USE:support/codify-smt/encode`). Support Services use the [SDK](../04-sdk/00-overview.md)'s `FlowSupportService` base class and have a simplified permission model distinct from the full node capability envelope. Specialised subtypes (such as `CodificationService`) extend subtype-specific base classes that inherit from `FlowSupportService`.

Support Services are not required to be stateless. A Codification Service might cache model weights on a PVC; a notification relay might maintain connection pools. Infrastructure state is Support-Service-owned and not part of the Workitem provenance boundary.

Support Services emit context-specific telemetry relevant to their capability. No mandatory generic telemetry schema is imposed beyond standard health signals.

CRD field-level definitions are in [CRD Reference](../05-reference/crds.md).

### Codification Services

Codification Services are a Flow Support Service specialisation for governance hardening. They translate a [law](../01-concepts/03-data-model.md#laws)'s natural-language goal into formal representations — formal logic, executable validators, policy-as-code — increasing enforceability without changing the law's intent.

Codification Services expose an `encode` capability consumed during law promotion:

1. Assay renders a verdict during Tier 1 → Tier 2 promotion, expressing the ruling's goal in natural language.
2. Assay sends the goal to a Codification Service via its Sidecar.
3. The Codification Service returns one or more formal representations alongside the original prose.
4. Assay mints the Tier 2 Ruling as a single law object with multiple representations.

Assay decides what the ruling should be; the Codification Service writes the formal syntax.

Flow Architects can deploy multiple Codification Services for different representation types (e.g., `codify-smt` for formal logic, `codify-rego` for policy-as-code). Assay discovers available Codification Services through [Flow configuration](./05-configuration.md). If no Codification Service is deployed, Assay mints rulings with prose representations only — governance hardening through codification is optional, not a platform requirement.

```mermaid
sequenceDiagram
    participant AS as Assay
    participant SC as Sidecar
    participant CS as Codification Service
    participant LB as Librarian

    AS->>SC: encode(goal, target-format)
    SC->>CS: encode request
    CS-->>SC: formal representation(s)
    SC-->>AS: representation(s)
    AS->>AS: mint Ruling with prose + formal representations
    AS->>SC: write law
    SC->>LB: persist new Tier 2 Ruling
```

## Hearing Lifecycle as Cross-Component Protocol

Hearings are implemented as a protocol across services and runtime actors, not as a standalone hearing service.

Hearing processing uses standard Workitems with explicit governed artefacts and contract bindings. No hearing-specific Workitem subtype or `spec.type` discriminator is introduced.

Trigger ownership is consolidated in the Librarian:

- Friction-threshold trigger (Tier 1 Finding) -> Librarian queries Flow Monitor, detects threshold crossing.
- TTL-proximity trigger (Tier 1 or Tier 2) -> Librarian detects law entering configurable window before TTL expiry.

Execution and adjudication path:

1. Librarian requests hearing Workitem creation through the Operator, supplying hearing artefacts including `lawId`.
2. Operator admits and assigns the hearing Workitem to Assay using Assay's bound hearing entry contract.
3. Assay retrieves the law's friction data from the Flow Monitor (via Sidecar) and legal context from the Librarian.
4. Assay issues a tier-appropriate verdict and calls `complete()`.
5. Operator validates Assay's bound hearing exit contract and applies completion state; Librarian applies resulting law lifecycle actions.

```mermaid
sequenceDiagram
    participant LB as Librarian
    participant OP as Operator
    participant AS as Assay
    participant SC as Sidecar
    participant FM as Flow Monitor

    LB->>OP: create hearing Workitem (lawId artefact)
    OP->>AS: assign hearing via entry binding
    AS->>SC: query friction for law
    SC->>FM: friction query (by law_id)
    FM-->>SC: friction data
    SC-->>AS: friction data
    AS->>SC: query law context
    SC->>LB: law context request
    LB-->>SC: law versions and tiers
    SC-->>AS: law versions and tiers
    AS->>SC: verdict + complete()
    SC-->>OP: verdict + complete()
    OP->>OP: validate Assay hearing exit contract
    OP->>LB: apply lifecycle action
```

Verdict schema is tier-specific:

- **Friction-threshold hearing (Tier 1):** `Promote`.
- **Tier 1 TTL-proximity hearing:** `Retire` or `Promote`.
- **Tier 2 TTL-proximity hearing:** `Demote` or `Promote` (petition for Tier 3 ratification).

Assay considers the law's accumulated friction and goal when rendering verdicts. There is no TTL reset — hearings produce a decisive outcome.

## Backup and Recovery Boundaries

Service backup scope is explicit:

- Librarian embedded stores and indexes: service-owned backup process.
- Archivist SQLite provenance store: service-owned backup process.
- Archivist blob store (PVC-backed or object storage): service-owned backup and restore process consistent with storage backend.

Infrastructure-owned scope remains external to services:

- Kubernetes etcd backup/restore (including Workitem and configuration CRDs) is cluster-admin responsibility.

Recovery ordering must preserve referential integrity:

1. Restore control-plane CRDs (infrastructure domain).
2. Restore Librarian stores.
3. Restore Archivist SQLite provenance.
4. Restore Archivist blob content.
5. Reconcile and verify provenance references and governance continuity.

Detailed runbooks are specified in [Operations](./07-operations.md).

## Inter-Service Contracts

Core call paths are stable:

- Operator <-> Librarian: law lifecycle events, hearing Workitem creation coordination.
- Operator <-> Archivist: completion validation queries and artefact presence checks.
- Sidecar <-> Archivist: artefact read/write/query lifecycle operations.
- Sidecar <-> Librarian: law retrieval and legal-context queries.
- Sidecar <-> Flow Monitor: friction emission, friction queries, and telemetry signals.
- Librarian -> Flow Monitor: friction queries for law lifecycle threshold evaluation.
- Sidecar <-> Support Services: capability-gated operations on Flow-Architect-deployed services.
- Assay (via Sidecar) <-> Codification Services: encode requests during law promotion.
- Assay (via Sidecar) <-> Flow Monitor: friction queries for hearing evidence.
- Services -> Flow Monitor: metrics, traces, and audit events.

Contract failures must return structured errors aligned with [Error Catalog](../05-reference/error-catalog.md).

## Failure and Degradation Semantics

Service outages degrade behaviour predictably:

- Archivist unavailable: artefact mutation and provenance queries fail closed; Workitems cannot progress through affected steps.
- Librarian unavailable: law retrieval and law lifecycle actions fail closed. Hearing trigger evaluation pauses until the Librarian recovers.
- LLM contradiction evaluator unavailable: higher-tier law activation pauses in queued state; integration retries with backoff and raises operational alerts.
- Flow Monitor unavailable: processing continues, but observability coverage degrades, friction queries return errors, and hearing threshold evaluation is blocked until recovery. Alerting is raised.
- Support Service unavailable: operations requiring that service's capability fail closed for the requesting actor. Governance hardening (codification) degrades gracefully — Assay can mint prose-only rulings when Codification Services are unavailable.

Fail-open behaviour is prohibited for governance integrity paths.

## Service Invariants

All deployments preserve these service invariants:

1. Archivist is the source of truth for artefact provenance beyond raw bytes.
2. Workitem CRD stores artefact references only (`id`, `kind`).
3. Laws are single objects with one goal and multiple representations under whole-law versioning.
4. Friction-threshold hearing triggers are emitted by the Librarian based on Flow Monitor queries.
5. TTL-proximity hearing triggers are emitted by the Librarian.
6. Assay evidence retrieval includes friction data from the Flow Monitor.
7. Hearing adjudication remains an Assay responsibility, not a service-local shortcut.
8. Friction is first-class and queryable by source attribution.
9. Backup ownership boundaries are explicit between services and cluster administration.
10. Cross-flow law integration preserves tiered supremacy, grace-period semantics, and audit continuity.
11. Flow Support Services are optional, Flow-Architect-deployed, and do not process Workitems.
12. Codification Services are optional; their absence degrades governance hardening to prose-only rulings.

Node-facing implications of these services are detailed in [SDK Core](../04-sdk/01-sdk-core.md), [SDK Artefacts](../04-sdk/02-sdk-artefacts.md), [SDK Legal](../04-sdk/03-sdk-legal.md), [SDK Feedback](../04-sdk/04-sdk-feedback.md), and [SDK Telemetry](../04-sdk/06-sdk-telemetry.md).
