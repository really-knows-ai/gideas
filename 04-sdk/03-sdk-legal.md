# SDK Legal

## Goal

Define the SDK surface for law retrieval, citation, and law-adjacent authoring actions.

## Law Retrieval and Selection

Nodes query the [Librarian](../02-flow/04-system-services.md#librarian) for applicable laws through the [Sidecar](../03-node/01-sidecar.md). The Sidecar proxies the request to the Librarian and enforces the `READ:law` capability gate — a node without this capability cannot query the Library.

### Query Modes

The SDK exposes law retrieval with three query modes:

- **All laws** — no filter. Returns every law in the Flow's Library.
- **By artefact kind** — caller provides an artefact kind (e.g., `"haiku"`). Returns laws whose `appliesTo` includes the queried kind, plus all global laws (empty `appliesTo`).
- **By artefact kind + representation type** — caller provides an artefact kind and a representation type. Same kind filter as above, plus the law must have at least one representation of the requested type. Laws without a matching representation type are excluded.

All three modes return full law objects — goal, all representations, tier, and lifecycle metadata. Filters gate which laws are included in the result; they never strip representations from returned objects. The node sees the whole law and picks the representation it uses.

### Reference Arrangement Usage

In the [reference arrangement](../01-concepts/02-foundry-cycle.md), each node type queries the Library differently:

- **Forge** queries by artefact kind to seed its generation context with all applicable governance.
- **Quench** queries by artefact kind + executable representation type to find laws it can run as deterministic checks.
- **Appraise** queries by artefact kind + prose representation type to find laws a review panel can evaluate subjectively.
- **Refine** queries by artefact kind to review all applicable law alongside feedback.

These are conventions of the reference arrangement. Any node with `READ:law` can use any query mode.

## Citation

`Cite(law_ids)` records that a node used one or more laws during processing. It is a convenience wrapper around [`AddFriction`](./06-sdk-telemetry.md#addfriction--node-context) that emits a fixed, low magnitude of friction attributed to the specified laws.

The SDK surface accepts a single parameter:

- `law_ids` (`[]string`, one or more) — the laws the node used.

The Sidecar injects all identity context (`node_id`, `workitem_id`, `flow_id`) and the fixed citation magnitude. The node cannot override the magnitude — the signal is frequency of use, not caller-weighted importance.

Every `Cite` call produces an `AddFriction` event with the cited law identifiers. The [Flow Monitor](../02-flow/04-system-services.md#flow-monitor-and-friction-surface) aggregates these events alongside all other friction. The [Librarian](../02-flow/04-system-services.md#librarian) queries the Flow Monitor for accumulated friction on individual laws to determine when friction-threshold hearings should be triggered.

Requires `READ:law` capability — a node that cannot read laws has no basis for citing them.

## Finding and Ruling Interaction Boundaries

Clarify what node roles may record findings and what remains judicial or human-governed.

## Representation-Aware Usage

Describe how nodes consume multiple law representations while preserving single-goal law identity.

## Capability and Authorisation Semantics

Map legal operations to capability requirements and service-authorised outcomes.

## Failure Behaviour

Document deterministic rejection and retry guidance for legal operations.

## Legal SDK Invariants

Capture governance constraints that legal APIs must preserve.
