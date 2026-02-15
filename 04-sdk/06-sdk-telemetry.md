# SDK Telemetry

## Goal

Define SDK telemetry and friction emission semantics for operational visibility and governance cost attribution.

## Telemetry Surface Overview

Describe metrics, traces, logs, and friction emission APIs available to handlers.

## Friction Emission Contract

Friction is additive. Callers emit a magnitude and optional law attribution; the [Flow Monitor](../02-flow/04-system-services.md#flow-monitor-and-friction-surface) aggregates the raw events post-hoc across whatever axes operators need (per-node, per-law, per-tier, per-topology-path). There is no caller-side operation selection — every emission adds to the total.

[`Cite(law_ids)`](./03-sdk-legal.md#citation) is a convenience wrapper that calls `AddFriction` with a fixed citation magnitude and the specified law identifiers. It is the standard way for nodes to record law usage — the signal is frequency of citation, not caller-weighted importance. The accumulated friction on a law is what the [Librarian](../02-flow/04-system-services.md#librarian) uses to evaluate friction-threshold hearing triggers.

[`AddFeedback`](./04-sdk-feedback.md#feedback-friction) transparently emits `AddFriction` with magnitude equal to the feedback depth for that item. The first feedback on an item emits 1, the second 2, the nth n. This escalating cost signal makes the adversarial loop's price visible before deadlock.

These wrappers are additive contributions to the same friction stream. `Cite` records law usage. `AddFeedback` records governance debate cost. Both flow through the same `AddFriction` pipeline and are aggregated by the Flow Monitor alongside any direct `AddFriction` calls nodes make.

### AddFriction — Node Context

When called from a node handler, the Sidecar automatically injects identity context. The node SDK surface accepts only semantic data:

- `magnitude` (`float64`) — how much friction to record.
- `law_ids` (`[]string`, optional) — one or more law identifiers to attribute the friction to.

The Sidecar injects:

- `node_id`
- `workitem_id`
- `flow_id`

The SDK must not accept these identity fields from node code. This strict separation prevents identity spoofing and guarantees that friction is always attributed to the correct runtime context.

### AddFriction — Service Context

When called from a [Flow Support Service](../02-flow/04-system-services.md#flow-support-services) or system service, the caller operates outside a node assignment and must provide its own attribution context:

- `magnitude` (`float64`) — how much friction to record.
- `workitem_id` (`string`) — the Workitem the friction pertains to.
- `node_id` (`string`, optional) — the node the friction pertains to, if attributable.
- `law_ids` (`[]string`, optional) — one or more law identifiers.

The service's `flow_id` is injected from the service's own identity context.

### Recorded Event Shape

Regardless of calling context, every friction event is recorded with the same shape:

| Field | Source (node) | Source (service) |
|-------|--------------|-----------------|
| `flow_id` | Sidecar-injected | Service identity |
| `workitem_id` | Sidecar-injected | Caller-provided |
| `node_id` | Sidecar-injected | Caller-provided (optional) |
| `law_ids` | Caller-provided (optional) | Caller-provided (optional) |
| `magnitude` | Caller-provided | Caller-provided |

## Operational Signal Quality

Define expectations for useful, low-noise instrumentation during normal and failure paths.

## Service and Sidecar Signal Relationship

Clarify that Sidecar telemetry is mediation-level observability, while authoritative mutation audit remains service-owned.

## Failure and Backpressure Behaviour

Describe handling when telemetry sinks are degraded without violating work execution semantics.

## Privacy and Data Minimisation Guidance

Set constraints for telemetry payload content to avoid leaking governed artefact data.

## Telemetry SDK Invariants

Capture non-negotiable observability guarantees and boundaries.
