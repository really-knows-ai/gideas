# SDK Legal

The legal SDK surface provides law retrieval, citation, and finding creation operations. All legal operations are routed through the [Sidecar](../03-node/01-sidecar.md) to the [Librarian](../02-flow/04-system-services.md#librarian), which owns law storage, retrieval, and lifecycle management.

## Law Retrieval and Selection

Nodes query the [Librarian](../02-flow/04-system-services.md#librarian) for applicable laws through the [Sidecar](../03-node/01-sidecar.md). The Sidecar proxies the request to the Librarian and enforces the `READ:law` capability gate — a node without this capability cannot query the Library.

### Query Modes

The SDK exposes law retrieval with distinct query modes:

- **All laws** — no filter. Returns every law in the Flow's Library.
- **By governed artefact name** — caller provides a governed artefact name (e.g., `"haiku"`). Returns laws whose `appliesTo` includes the queried name, plus all global laws (empty `appliesTo`).
- **By governed artefact name + representation type** — caller provides a governed artefact name and a representation type. Same name filter as above, plus the law must have at least one representation of the requested type. Laws without a matching representation type are excluded.

All query modes return full law objects — goal, all representations, tier, and lifecycle metadata. Filters gate which laws are included in the result; they never strip representations from returned objects. The node sees the whole law and picks the representation it uses.

### Reference Arrangement Usage

In the [reference arrangement](../01-concepts/02-foundry-cycle.md), each node type queries the Library differently:

- **Forge** queries by governed artefact name to seed its generation context with all applicable governance.
- **Quench** queries by governed artefact name + executable representation type to find laws it can run as deterministic checks.
- **Appraise** queries by governed artefact name + prose representation type to find laws a review panel can evaluate subjectively.
- **Refine** queries by governed artefact name to review all applicable law alongside feedback.

These are conventions of the reference arrangement. Any node with `READ:law` can use any query mode.

## Citation

`Cite(law_ids)` records that a node used one or more laws during processing. It is a convenience wrapper around [`AddFriction`](./06-sdk-telemetry.md#addfriction-node-context) that emits a fixed, low magnitude of friction attributed to the specified laws.

The SDK surface accepts a single parameter:

- `law_ids` (`[]string`, one or more) — the laws the node used.

The Sidecar injects all identity context (`node_id`, `workitem_id`, `namespace`) and the fixed citation magnitude. The node cannot override the magnitude — the signal is frequency of use, not caller-weighted importance.

Every `Cite` call produces an `AddFriction` event with the cited law identifiers. The [Friction Ledger](../02-flow/04-system-services.md#friction-ledger) aggregates these events alongside all other friction. The [Friction Watcher](../01-concepts/02-foundry-cycle.md#friction-watcher) node subscribes to the Friction Ledger's threshold-crossing signals to determine when friction-threshold [review hearings](../02-flow/04-system-services.md#hearing-lifecycle-as-cross-component-protocol) should be triggered.

Requires `WRITE:friction` capability — the underlying mechanism is friction emission through the [Flow Event Bus](../02-flow/04-system-services.md#flow-event-bus).

## Finding Creation

Nodes with the `WRITE:law/tier1` capability can record Tier 1 [Findings](../01-concepts/03-data-model.md#law-tiers) — observations that emerge from work.

| Operation | Parameters |
|-----------|-----------|
| `RecordFinding(goal, appliesTo, representations)` | `goal` (string) — plain-language statement of what the law enforces, stops, or ensures. `appliesTo` (`[]string`) — governed artefact names this law applies to; empty for global. `representations` (`[]Representation`) — at least one representation is required (typically prose). Each representation has a `type` (MIME type) and `content` (payload). |

`RecordFinding` returns immediately with a law identifier. The write is eventually consistent — the new Finding is available for writes immediately but may not appear in `QueryLaws` results until the [Librarian](../02-flow/04-system-services.md#librarian) has indexed it. Duplicate detection is asynchronous: the Librarian runs background conflict checks against existing laws using [semantic search and LLM evaluation](../02-flow/04-system-services.md#librarian). Duplicate Findings are merged or retired without node involvement.

This write-availability-first design means nodes can record Findings as they discover patterns without blocking on indexing or conflict resolution. The Librarian handles deduplication and integration asynchronously.

Tier 1 Findings decay when their tier's configured review TTL expires. Findings that accumulate [friction through citation](#citation) persist and can be promoted to Tier 2 Rulings through [review hearings](../02-flow/04-system-services.md#hearing-lifecycle-as-cross-component-protocol).

### Authority Boundaries

Finding creation is the only law-writing operation available through the node SDK. The authority boundary is strict:

| Tier | Write Authority | SDK Available |
|------|----------------|---------------|
| 1 — Finding | Nodes with `WRITE:law/tier1` | Yes — `RecordFinding` |
| 2 — Ruling | [Judiciary](../02-flow/03-nodes-external.md#the-judiciary--standard-subsystem) (via the [Clerk cycle](../01-concepts/02-foundry-cycle.md#clerk-cycle) and [law-applicator](../01-concepts/02-foundry-cycle.md#clerk-cycle), `WRITE:law/tier2`) | No |
| 3 — Local Statute | Flow Architect (human-administered or local legislative cycle) | No |
| 4 — State Constitution | [Federation](../01-concepts/04-governance.md#authority-publisher-roles) (state-level authority publisher Flow) | No |
| 5 — Federal Accord | Federation | No |

The [Judiciary](../02-flow/03-nodes-external.md#the-judiciary--standard-subsystem) creates Tier 2 Rulings when the Arbiter resolves deadlocked disputes and when the Tribunal adjudicates [review hearings](../02-flow/04-system-services.md#hearing-lifecycle-as-cross-component-protocol), using the [Clerk cycle](../01-concepts/02-foundry-cycle.md#clerk-cycle) to draft petitions and fan out to [Codification nodes](../01-concepts/02-foundry-cycle.md#clerk-cycle) for formal representations. Tier 3 changes pass through HITL ratification inside the Clerk cycle. Tier 4-5 conflicts are escalated as Embassy-mediated `law-petition`s to the authority Flow selected by the [Federation](../01-concepts/04-governance.md#higher-authority-escalation). These are Judiciary-internal judicial operations, not SDK methods available to node handlers.

## Representation-Aware Usage

A [law](../01-concepts/03-data-model.md#laws) is a single object with one goal and one or more representations. Every query mode returns the full law object — all representations included. The node decides which representation to consume.

A prose representation and an executable representation of the same law enforce the same goal. Different nodes consume the same law through different lenses:

- A review node reads the prose representation to evaluate subjective compliance.
- A validation node runs the executable representation as a deterministic check.
- A generation node reads the prose representation to understand constraints.

Nodes should not assume that every law carries a specific representation type. Query by governed artefact name + representation type to find laws that have the representations the node can interpret. Laws without a matching representation are excluded from the result, not returned with empty representation lists.

[Governance hardening](../01-concepts/04-governance.md#precedent) adds representations over time. A prose-only Tier 1 Finding gains a formal logic representation when promoted to a Tier 2 Ruling through a [Codification Service](../02-flow/04-system-services.md#codification-services). The goal stays the same; enforceability increases. A node that runs deterministic checks can only use laws that have executable representations, and automatically picks up newly hardened laws as they gain those representations.

## Capability and Authorisation Semantics

Legal operations map to capability requirements:

| Operation | Required Capability | Enforcing Service |
|-----------|-------------------|-------------------|
| `QueryLaws` (all modes) | `READ:law` | Librarian |
| `RecordFinding` | `WRITE:law/tier1` | Librarian |

[`Cite`](./06-sdk-telemetry.md#addfriction-node-context) is not a legal operation — it is SDK sugar over [`AddFriction`](./06-sdk-telemetry.md#addfriction-node-context), which requires `WRITE:friction` and is enforced by the Sidecar. See [Friction Emission Contract](./06-sdk-telemetry.md#friction-emission-contract).

Missing capabilities produce a `CAPABILITY_DENIED` error from the Librarian, forwarded through the Sidecar as a structured error. The node does not learn what capabilities it lacks — the error indicates the operation was denied, not which specific grant is missing.

## Failure Behaviour

| Error | Cause | Retry |
|-------|-------|-------|
| `CAPABILITY_DENIED` | Node lacks `READ:law` or `WRITE:law/tier1` | No — permanent until configuration changes |
| `LAW_NOT_FOUND` | Cited law has been retired or does not exist | No — the law is gone |
| `SERVICE_UNAVAILABLE` | Librarian is temporarily unreachable | Yes — transient, use exponential backoff |
| `MESSAGE_TOO_LONG` | Finding goal exceeds maximum length | No — reduce content length |

`RecordFinding` failures do not affect the current Workitem's processing — they are a governance side effect. A node that fails to record a Finding can continue its primary work and retry the Finding later or accept the loss.

`QueryLaws` failures prevent the node from loading its governance context. Whether this is blocking depends on the node's design — a deterministic validator cannot proceed without laws to check against, while a generator may be able to proceed with degraded governance context and emit appropriate friction to signal the gap.

## Legal SDK Invariants

1. `READ:law` is required for all law queries and citations.
2. `WRITE:law/tier1` is required to record Tier 1 Findings. No other tier is writable through the SDK.
3. Query results return full law objects — filters gate inclusion, never strip representations.
4. `Cite` emits friction at a fixed magnitude. The node cannot override the magnitude.
5. `RecordFinding` is eventually consistent — the Finding is writable immediately but queryable after indexing.
6. Duplicate Finding detection is asynchronous and Librarian-owned.
7. Law objects are immutable from the SDK's perspective. Any mutation produces a new version with a new content hash. The node reads laws; the Librarian manages lifecycle.
8. Governance hardening (adding representations) is a [Codification Service](../02-flow/04-system-services.md#codification-services) operation triggered by the [Clerk cycle](../01-concepts/02-foundry-cycle.md#clerk-cycle) via fan-out to Codification nodes, not a node SDK operation.
