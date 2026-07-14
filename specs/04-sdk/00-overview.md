# SDK Overview

The SDK is the programming interface between node handler code and the Foundry Flow runtime. All runtime operations originating from a node — artefact reads and writes, law queries, feedback, stamp applications, routing decisions, and telemetry — pass through the SDK to the [Sidecar](../03-node/01-sidecar.md), which mediates authenticated access to runtime services.

A separate SDK surface, the [`FlowSupportService`](#flowsupportservice-base-class) base class, serves developers building [Flow Support Services](../02-flow/04-system-services.md#flow-support-services). Support Service development does not use the Workitem-scoped handler contract. For inference workloads, the [`FoundryAgent`](./07-sdk-agent.md) wrapper provides a managed handler contract with automatic heartbeat, output validation, and cost accounting.

## SDK Runtime Role

The SDK occupies the boundary between business logic and platform enforcement. Node handlers call SDK methods to express intent; the [Sidecar](../03-node/01-sidecar.md) authenticates and proxies those calls to the service that owns the affected state; the owning service authorises and persists the result.

```mermaid
flowchart LR
    ND["Node Handler"] --> SDK["SDK"]
    SDK --> SC["Sidecar"]
    SC --> OP["Operator<br/>lifecycle and routing"]
    SC --> AR["Archivist<br/>artefact provenance"]
    SC --> LB["Librarian<br/>law lifecycle"]
    SC --> SS["Support Services<br/>pluggable capabilities"]
    SC --> FM["Flow Monitor<br/>telemetry and friction"]
```

The SDK abstracts transport, identity injection, and service topology from node code. Handlers program against SDK types (`Workitem`, `Artefact`, routing results) and never depend on Kubernetes CRD field paths, gRPC service addresses, or direct service credentials.

## Execution Scope Model

Every handler invocation is scoped to a single [Workitem](../02-flow/02-workitem.md) assignment. The Sidecar establishes an assignment session when the [Operator](../02-flow/01-operator.md) assigns a Workitem to the node, and all SDK calls within that session are automatically scoped to the assigned Workitem.

Domain objects (`Workitem`, `Artefact`, `Feedback`, `LawGroup`, `Law`, `Flow`, `Node`)
carry an internal session reference created by the `Client`. No `context.Context` is
accepted by any user-facing method — the SDK manages gRPC metadata, cancellation,
and retries internally. The only exception is `Infer(ctx, ...)` on the
[Agent](07-sdk-agent.md#model-interface) abstraction, where the caller needs
cancellation control over streaming inference.

Assignment scoping is enforced at every layer:

- **SDK surface** — domain objects carry an internal session reference. Operations are implicitly scoped to the current assignment. No `context.Context` parameter exists.
- **Sidecar** — injects `node_id`, `workitem_id`, and `namespace` into every outgoing request. Requests referencing artefacts or state outside the current assignment are rejected before they reach a service.
- **Runtime services** — validate that incoming requests match the declared assignment context.

When a node is configured for concurrent processing (`concurrency > 1`), each assignment runs an independent session with its own `Workitem` scope, activity timer, and handler context. Thread safety within node code is the developer's responsibility.

## Trust and Authority Boundaries

The SDK expresses intent. It does not persist state, enforce governance, or make authoritative decisions.

| Layer | Responsibility |
|-------|---------------|
| SDK | Intent expression. Structured API for node business logic. |
| Sidecar | Authentication. Identity injection. Local validation (malformed requests, scope violations). |
| Operator | Workitem lifecycle persistence. Routing guard evaluation. Entry and exit contract enforcement. |
| [Archivist](../02-flow/04-system-services.md#archivist) | Artefact provenance persistence. Stamp authorisation (capability + write-once). Feedback state machine enforcement. Contempt guard. |
| [Librarian](../02-flow/04-system-services.md#librarian) | Law storage and retrieval. Law write authorisation. Integration and conflict detection. |
| [Flow Event Bus](../02-flow/04-system-services.md#flow-event-bus) | Durable event distribution (telemetry, audit, friction channels). |
| [Friction Ledger](../02-flow/04-system-services.md#friction-ledger) | Friction aggregation and threshold evaluation. |
| [Flow Monitor](../02-flow/04-system-services.md#flow-monitor) | Metrics export and audit log emission. |
| Support Services | Capability-specific authorisation for pluggable operations. |

Node containers hold no Flow runtime credentials. The Sidecar holds identity material and attaches it to outgoing requests. This strict separation prevents credential leakage into node code and guarantees that all runtime attribution is Sidecar-authoritative.

## SDK Surface Map

The SDK is organised into domain-object surfaces, each backed by a runtime service.
The `Client` is a thin entry point with exactly five methods: `NewClient`, `Close`,
`GetWorkitem`, `GetFlow`, `GetNode`, `GetLaw`, and `RecordFinding`. All other
operations are accessed through domain objects that carry their own internal session.

| Surface | Entry Point | Backing Service | Detail |
|---------|-------------|-----------------|--------|
| [Core](./01-sdk-core.md) | `Workitem` (lifecycle) | Operator (via Sidecar) | Handler contract, routing instructions, heartbeat |
| [Artefacts](./02-sdk-artefacts.md) | `Workitem.GetArtefact()` / `Artefact` | Archivist (via Sidecar) | Content addressing, passport, stamp application |
| [Legal](./03-sdk-legal.md) | `Workitem.GetLawGroups()` / `LawGroup.Law` / `Client.GetLaw()` / `Client.RecordFinding()` | Librarian (via Sidecar) | Query modes, citation friction, Tier 1 writes |
| [Feedback](./04-sdk-feedback.md) | `Workitem.AddFeedback()` / `Feedback` | Archivist (via Sidecar) | State machine, justification, contempt guard |
| [Workitems](./05-sdk-workitems.md) | `Workitem` / `ChildWorkitem` | Operator (via Sidecar) | Assignment-scoped access, child fan-out/fan-in, snapshot semantics |
| [Telemetry](./06-sdk-telemetry.md) | `Workitem.QueryFriction()` + Agent | Flow Monitor (via Sidecar) | Additive friction, identity-injected signals |
| [Agent](./07-sdk-agent.md) | `FoundryAgent` / `Model.Infer(ctx, ...)` | Operator + Flow Monitor (via Sidecar) | Automatic heartbeat, schema validation, atomic cost accounting |
| [HITL](./08-sdk-hitl.md) | `QueueManager` + `Workitem.PauseTimer()` / `Workitem.ResumeTimer()` | Node-local (queue) + Operator (via Sidecar) | `USE:queue/server` capability, persistent queue, escalation |
| [Cross-Flow](./09-sdk-cross-flow.md) | `EntryClient` / `FederationClient` / `EmbassyClient` | Embassy + Operator (via Sidecar) | built-in + flow-authored import type registry, `imported-*` attestation stamps |

All surfaces share the same trust model: SDK calls transit the Sidecar, which authenticates and proxies to the authoritative service.

## FlowSupportService Base Class

[Flow Support Services](../02-flow/04-system-services.md#flow-support-services) are optional, Flow-Engineering-Team-deployed containers that expose gRPC capabilities consumed by nodes (through Sidecar mediation) and by system services (through direct service-to-service calls). The SDK provides `FlowSupportService` as the base class for building these services.

`FlowSupportService` covers:

- **Capability declaration** — the service registers which capabilities it provides (e.g. `encode` for a [Codification Service](../02-flow/04-system-services.md#codification-services)).
- **gRPC endpoint registration** — capabilities are exposed as gRPC methods on the service's endpoint.
- **Health reporting** — mandatory `healthz` and `readyz` endpoints for [Operator](../02-flow/01-operator.md) lifecycle management and pod health checks.
- **Simplified permission model** — Support Services validate capability grants on incoming requests. A node must hold the `USE:support/<service>/<capability>` grant to invoke a capability. The permission model is distinct from the full node capability envelope.

`FlowSupportService` does not include Workitem, Artefact, or routing abstractions. Support Services do not process Workitems and do not participate in Workitem mutation flow or artefact provenance flow.

Specialised subtypes extend `FlowSupportService` with domain-specific contracts. `CodificationService` inherits from `FlowSupportService` and adds the `encode` capability contract for translating law goals into formal representations during [governance hardening](../01-concepts/04-governance.md#precedent).

Note: codification processing uses the Workitem model — the [Codification orchestrator](../01-concepts/02-foundry-cycle.md#codification-nodes) fans out to Codification nodes via child Workitems, and each Codification node invokes its backing Codification Service's `Encode` method. The `FlowSupportService` base class is retained for non-codification support services such as notification relays and external integrations.

## Failure and Error Model

SDK operations produce structured errors with stable error codes. Errors originate from the Sidecar (local validation) and from runtime services (authoritative enforcement):

**Sidecar-local rejections** — caught before the request reaches a service:

- Missing or malformed request parameters.
- Requests outside current Workitem assignment scope.
- Authentication failures (expired or invalid identity material).

**Service-side authorisation denials** — returned through the Sidecar as structured errors with no state change:

- Missing capability for the requested operation.
- Write-once stamp violation (same stamp name on same artefact version).
- Contempt violation (attempt to override a judicially-linked ruling).
- Invalid routing instruction (unresolvable output or target).

The SDK manages `context.Context` internally — domain objects carry a session
reference and use `context.Background()` for gRPC calls. Timeout and retry are
configured at the `Client` level via `WithTimeout` and `WithRetry` options.
`context.Canceled` and `context.DeadlineExceeded` are never exposed to callers;
they are handled internally by the session. The only exception is
`Infer(ctx, ...)` on the [Agent](07-sdk-agent.md#model-interface) abstraction,
which retains `context.Context` for streaming-boundary cancellation.

The SDK does not implement built-in error routing. When an SDK call fails, the handler receives a structured error and decides what failure means in its domain — retry, route elsewhere, or fail the assignment. Error classification utilities (`IsRetryable`, `IsError`) help handlers distinguish transient failures from permanent rejections.

Telemetry emission failures are non-blocking. If the telemetry sink is degraded, the SDK logs a warning and continues processing. Work execution never fails because telemetry delivery failed.

Capability gates are enforced by the owning service, not by the SDK or the node. The SDK does not expose Kubernetes CRD field paths, direct service addresses, or a freeform context bag. No SDK surface provides a `WorkitemType` or `spec.type` discriminator.

Stable error codes and their semantics are catalogued in the [Error Catalogue](../05-reference/error-catalogue.md). Wire-level error mappings are in the [gRPC API Reference](../05-reference/grpc-api.md).

## Relationship to Reference Documents

The SDK documents define behavioural contracts and API semantics. Implementation-level details live in reference documents:

- [gRPC API Reference](../05-reference/grpc-api.md) — wire-level service and method definitions, request/response shapes, status code mappings.
- [CRD Reference](../05-reference/crds.md) — Kubernetes resource schemas, field constraints, validation rules.
- [Error Catalogue](../05-reference/error-catalogue.md) — complete error code inventory, causes, and caller response guidance.
- [Glossary](../05-reference/glossary.md) — canonical term definitions.
- [Cross-Flow SDK surface](./09-sdk-cross-flow.md) — cross-flow import and export. Node handlers see imported Workitems as normal assignments with `imported-*` attestation stamps.
