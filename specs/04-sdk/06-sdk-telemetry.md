# SDK Telemetry

The telemetry SDK surface provides friction emission, custom telemetry events, and operational signal APIs. All telemetry flows through the [Sidecar](../03-node/01-sidecar.md) to the [Flow Event Bus](../02-flow/04-system-services.md#flow-event-bus), which distributes events to subscribers including the [Friction Ledger](../02-flow/04-system-services.md#friction-ledger) (for friction aggregation) and the [Flow Monitor](../02-flow/04-system-services.md#flow-monitor) (for metrics export and audit log emission).

## Telemetry Surface Overview

The SDK emits distinct signal classes:

| Signal | Purpose | Destination |
|--------|---------|-------------|
| Friction | Quantitative governance cost attributed to laws, nodes, and Workitems | Flow Event Bus (telemetry channel) |
| Metrics | Counters, gauges, and histograms for operational observability | Flow Event Bus (telemetry channel) |
| Traces | Distributed trace spans for request-path analysis | Flow Event Bus (telemetry channel) |
| Custom events | Structured domain-specific events emitted by handler code | Flow Event Bus (telemetry channel) |

Custom telemetry events are emitted through `RecordTelemetry(eventType, payload)`:

- `eventType` (string) — identifies the event kind. Use the `foundry.` namespace prefix with sub-namespaces: `foundry.node.*` for node-level events, `foundry.business.*` for domain-specific events, `foundry.debug.*` for diagnostic events.
- `payload` (structured data) — JSON-serializable event data, maximum 64 KB.

The Sidecar wraps every telemetry event in a standard envelope before publishing to the Flow Event Bus's telemetry channel:

| Envelope Field | Source |
|----------------|--------|
| `timestamp` | Sidecar clock |
| `namespace` | Sidecar environment (`FLOW_NAMESPACE`) |
| `node_id` | Sidecar identity |
| `workitem_id` | Current assignment |
| `trace_id` | Distributed trace context |

Telemetry emission is non-blocking. The call returns immediately; delivery to the Flow Event Bus is asynchronous. The handler does not wait for acknowledgement.

## Friction Emission Contract

Friction is additive. Callers emit a magnitude and optional law attribution; the [Friction Ledger](../02-flow/04-system-services.md#friction-ledger) aggregates the raw events post-hoc across whatever axes operators need (per-node, per-law, per-tier, per-topology-path). There is no caller-side operation selection — every emission adds to the total.

[`Cite(law_ids)`](./03-sdk-legal.md#citation) is a convenience wrapper that calls `AddFriction` with a fixed citation magnitude and the specified law identifiers. It is the standard way for nodes to record law usage — the signal is frequency of citation, not caller-weighted importance. The accumulated friction on a law is what the [Friction Ledger](../02-flow/04-system-services.md#friction-ledger) uses to evaluate hearing thresholds and publish threshold-crossing signals to the [Friction Watcher](../01-concepts/02-foundry-cycle.md#friction-watcher) node.

[`AddFeedback`](./04-sdk-feedback.md#feedback-friction) transparently emits `AddFriction` with magnitude equal to the feedback depth for that item. The first feedback on an item emits 1, the second 2, the nth n. This escalating cost signal makes the adversarial loop's price visible before deadlock.

These wrappers are additive contributions to the same friction stream. `Cite` records law usage. `AddFeedback` records governance debate cost. Both flow through the same `AddFriction` pipeline and are aggregated by the Friction Ledger alongside any direct `AddFriction` calls nodes make.

### AddFriction: Node Context

`AddFriction` requires the `WRITE:friction` [capability](../03-node/02-configuration.md#capability-model). The Sidecar enforces this before publishing to the Flow Event Bus. [`Cite`](./03-sdk-legal.md#citation) inherits this requirement — it is sugar over `AddFriction`.

When called from a node handler, the Sidecar automatically injects identity context. The node SDK surface accepts only semantic data:

- `magnitude` (`float64`) — how much friction to record.
- `law_ids` (`[]string`, optional) — one or more law identifiers to attribute the friction to.

The Sidecar injects:

- `node_id`
- `workitem_id`
- `namespace`

The SDK must not accept these identity fields from node code. This strict separation prevents identity spoofing and guarantees that friction is always attributed to the correct runtime context.

### AddFriction: Service Context

When called from a [Flow Support Service](../02-flow/04-system-services.md#flow-support-services) or system service, the caller operates outside a node assignment and must provide its own attribution context:

- `magnitude` (`float64`) — how much friction to record.
- `workitem_id` (`string`) — the Workitem the friction pertains to.
- `node_id` (`string`, optional) — the node the friction pertains to, if attributable.
- `law_ids` (`[]string`, optional) — one or more law identifiers.

The service's namespace is injected from the service's own identity context.

### Recorded Event Shape

Regardless of calling context, every friction event is recorded with the same shape:

| Field | Source (node) | Source (service) |
|-------|--------------|-----------------|
| `namespace` | Sidecar-injected | Service identity |
| `workitem_id` | Sidecar-injected | Caller-provided |
| `node_id` | Sidecar-injected | Caller-provided (optional) |
| `law_ids` | Caller-provided (optional) | Caller-provided (optional) |
| `magnitude` | Caller-provided | Caller-provided |

## Operational Signal Quality

Telemetry is most useful when it captures meaningful state transitions rather than saturating the pipeline with noise.

Guidance for node developers:

- **Emit at decision points** — when the handler makes a routing choice, discovers a governance violation, or encounters an unexpected condition. These are the events operators need to triage issues.
- **Use severity and priority consistently** — align custom event severity with the operational triage model described in [Operations](../02-flow/07-operations.md).
- **Prefer structured payloads** — key-value structured data is queryable. Freeform log strings are not.
- **Avoid high-frequency per-item telemetry** — emitting a telemetry event for every line of an artefact creates noise. Emit per-artefact or per-decision-point summaries instead.

Friction is the primary governance cost signal. Nodes should use `AddFriction` for domain-specific costs that the standard wrappers (`Cite`, `AddFeedback`) do not cover — for example, friction generated by consulting an expensive external service during processing.

### Inference Cost Accounting Convention

The `foundry.cost.llm` event type is the standard convention for recording LLM inference costs. [FoundryAgent](./07-sdk-agent.md) emits this event atomically after each inference step. Nodes that perform inference without FoundryAgent should emit the same event type through `RecordTelemetry` for consistent cost aggregation.

The expected payload includes `model` (model identifier), `input_tokens`, `output_tokens`, and `duration_ms`. Implementations may include additional fields (e.g. `provider`, `cached_tokens`). The payload follows standard custom event conventions: structured, JSON-serializable, subject to the 64 KB limit, and wrapped in the Sidecar's identity envelope.

Atomic per-step emission — rather than batching at handler exit — ensures that interrupted handlers produce accurate cost records reflecting actual work performed. The [Flow Monitor](../02-flow/04-system-services.md#flow-monitor) exports these events via `/metrics` for Prometheus-based per-node, per-model, and per-Workitem cost analysis. See [SDK Agent](./07-sdk-agent.md#atomic-cost-accounting) for the full contract.

## Service and Sidecar Signal Relationship

Telemetry from different runtime layers serves different audiences:

| Layer | Emitter | Signal Type | Audience |
|-------|---------|-------------|----------|
| Node handler | SDK telemetry calls | Custom events, friction, business metrics | Application operators, Flow Architects |
| Sidecar | Sidecar internals | Mediation metrics (request counts, latencies, auth failures) | Platform operators |
| Runtime services | Archivist, Librarian, Operator | Authoritative audit events (version creation, stamp application, law lifecycle, state transitions) | Auditors, compliance |

Authoritative mutation audit is service-owned. The [Archivist](../02-flow/04-system-services.md#archivist) records artefact version creation, stamp application, and feedback transitions. The [Operator](../02-flow/01-operator.md) records lifecycle transitions, routing decisions, and contract evaluations. The [Librarian](../02-flow/04-system-services.md#librarian) records law creation, retirement, and integration events. Nodes do not need to emit supplementary audit telemetry for these operations — the authoritative record is service-owned.

Node telemetry supplements service audit with business context — why a routing decision was made, what domain conditions were observed, what governance gaps were detected. These signals are operationally valuable but not authoritative for compliance purposes.

## Failure and Backpressure Behaviour

Telemetry failures are non-blocking. If the [Flow Event Bus](../02-flow/04-system-services.md#flow-event-bus) is degraded or unreachable:

- `AddFriction`, `Cite`, and `RecordTelemetry` calls return without error. The SDK logs a warning internally.
- Friction events from `AddFeedback` are still emitted by the SDK; if the Flow Event Bus cannot receive them, the Sidecar buffers them locally and retries. The feedback state transition itself (persisted by the Archivist) is unaffected.
- Work execution never fails because telemetry delivery failed.

The Sidecar maintains a bounded internal buffer for outgoing telemetry. If the buffer fills due to sustained backpressure, the oldest events are dropped. The Sidecar emits a counter metric for dropped events so operators can detect telemetry loss.

Signal reliability follows a clear priority: friction events (governance cost) take priority over custom telemetry events (business context) in buffer contention. Friction is the system's core cost signal; custom telemetry is supplementary.

## Privacy and Data Minimisation

Telemetry payloads should not contain governed artefact content. Use identifiers and references rather than embedding artefact bytes, feedback messages, or law text in telemetry events.

Guidance:

- **Reference by ID** — use artefact `id`, feedback `id`, or law `id` in telemetry payloads. Do not embed content.
- **Avoid PII** — telemetry flows to operational systems that may have different access controls than the Archivist. Do not include personally identifiable information in custom events.
- **Respect payload limits** — custom event payloads are capped at 64 KB. This limit exists to prevent accidental content embedding and to protect telemetry pipeline throughput.

## Telemetry SDK Invariants

1. Friction is purely additive — callers emit a magnitude. The only operation is additive emission; multiply, log, and set are not available.
2. Friction emission is mandatory for `Cite` and `AddFeedback`. These are not optional instrumentation — they are runtime outputs.
3. The Sidecar injects identity context for node-originated telemetry. Nodes cannot spoof attribution.
4. Telemetry failures do not block or fail work execution.
5. Custom telemetry events are capped at 64 KB per payload.
6. The Friction Ledger aggregates friction post-hoc via Flow Event Bus subscription. Callers do not control aggregation axes.
7. Authoritative mutation audit is service-owned. Node telemetry supplements but does not replace service audit.
8. Friction events take priority over custom telemetry in buffer contention.
