# SDK Feedback

The feedback SDK surface provides operations for creating, transitioning, querying, and resolving [feedback](../01-concepts/03-data-model.md#feedback) on governed artefacts. Feedback lives in the [Archivist](../02-flow/04-system-services.md#archivist), scoped to Workitem ID and artefact `id`. All feedback operations are routed through the [Sidecar](../03-node/01-sidecar.md) to the Archivist; the Archivist enforces state machine validity, capability requirements, and the [contempt guard](#contempt-guard-behaviour).

## Feedback Friction

Every `AddFeedback` call transparently emits an [`AddFriction`](./06-sdk-telemetry.md#addfriction-node-context) event. The node does not call `AddFriction` separately for feedback — the SDK handles it.

The magnitude equals the feedback depth for that item on that artefact. The first feedback on an item emits magnitude 1, the second emits 2, the nth emits n. Friction is attributed to the Workitem and the current node.

This creates a naturally escalating cost signal. Early feedback is cheap. Repeated rounds of disagreement on the same item become progressively more expensive, making the cost of the adversarial loop visible before it reaches deadlock.

The friction emission is transparent and mandatory — nodes cannot suppress it. The node's `AddFeedback` call succeeds or fails on its own merits; the friction event is a side effect published to the [Flow Event Bus](../02-flow/04-system-services.md#flow-event-bus).

## Feedback Query Surfaces

Feedback is accessed through the [Workitem](./05-sdk-workitems.md), [Artefact](./02-sdk-artefacts.md), and [Feedback](./04-sdk-feedback.md) domain objects. All methods manage their own session — no `context.Context` is needed.

| Operation | Returns | Description |
|-----------|---------|-------------|
| `Workitem.AddFeedback(artefactID, canWontFix, message)` | `(string, error)` | Creates a new feedback item on the specified artefact. Returns the feedback ID. |
| `Workitem.GetFeedback(artefactID)` | `([]*Feedback, error)` | Returns all feedback items for the specified artefact as domain `*Feedback` objects. |
| `Workitem.HasUnresolvedFeedback(artefactID)` | `(bool, error)` | True if any feedback item on the artefact is in a non-`resolved` state. |
| `Artefact.GetFeedback()` | `([]*Feedback, error)` | Returns all feedback items for this artefact. |
| `Artefact.HasUnresolvedFeedback()` | `(bool, error)` | True if any feedback for this artefact is unresolved. |
| `Feedback.GetDepth()` | `(int32, error)` | The current history depth (number of transitions) for this feedback item. Requires a server round-trip. |
| `Feedback.LinkRuling(lawID, targetState)` | `error` | Links a judicial ruling to this feedback, enabling the contempt guard. |

Feedback items are tagged with the artefact version hash they were raised against. All feedback is preserved across versions — storing new content does not discard or invalidate existing feedback.

From the gate node's perspective, only `resolved` feedback is settled. Feedback in any other state — `new`, `actioned`, `wont_fix`, `rejected`, `deadlocked` — is unresolved and blocks the Workitem from exiting. An `actioned` item still needs reviewer verification; a `wont_fix` item still needs reviewer acceptance or dispute. `HasUnresolvedFeedback` reflects this definition.

## Feedback State Transitions

Feedback follows a strict state machine. The [Archivist](../02-flow/04-system-services.md#archivist) enforces these transitions — any state change outside this state machine is rejected.

```mermaid
stateDiagram-v2
    [*] --> new : Workitem.AddFeedback()

    new --> actioned : Feedback.Resolve()
    new --> wont_fix : Feedback.Refuse()<br/>with Justification

    actioned --> resolved : Feedback.AcceptFix()
    actioned --> rejected : Feedback.RejectFix()

    wont_fix --> resolved : Feedback.AcceptRefusal()
    wont_fix --> rejected : Feedback.RejectRefusal()
    new --> deadlocked : Feedback.Deadlock()

    actioned --> deadlocked : Feedback.Deadlock()

    wont_fix --> deadlocked : Feedback.Deadlock()

    rejected --> actioned : Feedback.Resolve()
    rejected --> wont_fix : Feedback.Refuse()<br/>with Justification
    rejected --> deadlocked : Feedback.Deadlock()

    deadlocked --> wont_fix : Arbiter verdict<br/>(favours refiner;<br/>Feedback.LinkRuling set)
    deadlocked --> rejected : Arbiter verdict<br/>(favours reviewer;<br/>Feedback.LinkRuling set)

    resolved --> [*]
```

| Operation | From State | To State | Actor | Required Payload |
|-----------|-----------|----------|-------|-----------------|
| `Workitem.AddFeedback(artefactID, canWontFix, message)` | — | `new` | Any node with capability | `canWontFix` (bool), `message` (string, max 1024 chars) |
| `Feedback.Resolve(message)` | `new`, `rejected` | `actioned` | Refining node | `message` describing the fix applied |
| `Feedback.Refuse(justification)` | `new`, `rejected` | `wont_fix` | Refining node | Structured [justification](#refusal-and-justification-contract) (required) |
| `Feedback.AcceptFix()` | `actioned` | `resolved` | Reviewing node | — |
| `Feedback.RejectFix(message)` | `actioned` | `rejected` | Reviewing node | `message` explaining why the fix is inadequate |
| `Feedback.AcceptRefusal()` | `wont_fix` | `resolved` | Reviewing node | — |
| `Feedback.RejectRefusal(message)` | `wont_fix` | `rejected` | Reviewing node | `message` explaining why the refusal is unjustified |
| `Feedback.Deadlock()` | `new`, `actioned`, `wont_fix`, `rejected` | `deadlocked` | Gate node | — |

The **Actor** column describes the expected role in the [reference arrangement](../01-concepts/02-foundry-cycle.md), not an enforced identity constraint. Any node holding the required `WRITE:feedback/<status>` [capability](../03-node/02-configuration.md) can call the corresponding operation. The Archivist validates the capability grant and the from-state; node identity is recorded in each `FeedbackEvent` for audit, not for access control.

The gate node queries feedback depth via `Feedback.GetDepth()` and determines whether escalation is warranted. When the gate node decides to escalate, it calls `Feedback.Deadlock()` to transition the feedback item to `deadlocked`, then returns a routing instruction to send the Workitem to the [Arbiter](../02-flow/03-nodes-external.md#the-judiciary--standard-subsystem). The Archivist validates the `WRITE:feedback/deadlocked` capability, the from-state (any state except `resolved` and `deadlocked`), and the contempt guard (`linkedRuling` blocks deadlock) — deadlock determination is gate node logic, not Archivist enforcement. The Arbiter fans out to [Juror nodes](../01-concepts/02-foundry-cycle.md#juror-judicial-agent) for deliberation, tallies verdicts internally, and on consensus drives a [Clerk cycle](../01-concepts/02-foundry-cycle.md#clerk-cycle) child that drafts the Tier 2 Ruling as a [petition](../01-concepts/02-foundry-cycle.md#petition-artefact). The feedback item is then transitioned to either `wont_fix` (favouring the refiner) or `rejected` (favouring the reviewer), with the `linkedRuling` field set to the Ruling that captures the decision.

Each transition appends a `FeedbackEvent` to the item's history — an append-only chronological record of who acted, what action they took, and what they said.

Severity signals urgency, not authority:

| Severity | Description |
|----------|-------------|
| `LOW` | Minor style or preference issue |
| `MEDIUM` | Quality issue that should be addressed |
| `HIGH` | Functional or security concern |
| `CRITICAL` | Blocking issue, potential data loss |

## Refusal and Justification Contract

When a node calls `RefuseFeedback`, it must provide a structured justification. Unstructured refusals are rejected.

| Type | Fields | Meaning |
|------|--------|---------|
| `citation` | `citationIds[]` | "Existing law supports my position." The node cites specific laws that justify refusing the feedback. |
| `novel_argument` | `argument` (string) | "Here is a new argument." The node proposes reasoning not yet captured in the Library. |

Every refusal creates a traceable governance record — either a link to existing law or a new argument that can itself become a Tier 1 [Finding](../01-concepts/03-data-model.md#law-tiers) if it proves valuable.

The forced-choice structure prevents drive-by refusals. A node cannot dismiss feedback without either pointing to governance that supports its position or articulating a new principle for the record.

## Deadlock and Arbiter Interaction

When the gate node determines that a feedback item's history depth warrants escalation, it calls `DeadlockFeedback(feedbackId)` to transition the item to `deadlocked`, then returns a routing instruction to send the Workitem to the [Arbiter](../02-flow/03-nodes-external.md#the-judiciary--standard-subsystem). The threshold applies per feedback item, not per Workitem — a Workitem can have dozens of feedback items cycling normally while a single contentious item triggers escalation.

The Arbiter fans out to [Juror nodes](../01-concepts/02-foundry-cycle.md#juror-judicial-agent) for multi-agent deliberation and tallies juror verdicts internally. On consensus (or after HITL resolution of a hung jury), the verdict flows into the [Clerk cycle](../01-concepts/02-foundry-cycle.md#clerk-cycle) to draft a Tier 2 Ruling as a [petition](../01-concepts/02-foundry-cycle.md#petition-artefact). The Arbiter renders the verdict:

- **Verdict favours refiner** — the feedback state is set to `wont_fix` with `linkedRuling` pointing to the Tier 2 Ruling drafted through the [Clerk cycle](../01-concepts/02-foundry-cycle.md#clerk-cycle). The reviewing node must call `Feedback.AcceptRefusal()`.
- **Verdict favours reviewer** — the feedback state is set to `rejected` with `linkedRuling` pointing to the Tier 2 Ruling. The refining node must call `Feedback.Resolve()` to comply.

In both cases, [Juror deliberation friction](../01-concepts/03-data-model.md#friction) is emitted: each deliberation round produces friction at magnitude `depth ^ (round + 1)`. If the Arbiter exhausts its deliberation rounds and routes to a [HITL node](../04-sdk/08-sdk-hitl.md) for human intervention, a single friction event is emitted at magnitude `depth ^ (rounds * 2)`.

Handlers should respond to post-Arbiter feedback items by checking `linkedRuling`. A non-empty `linkedRuling` means judicial mandate is in effect and the [contempt guard](#contempt-guard-behaviour) constrains available transitions.

## Contempt Guard Behaviour

Once the [Arbiter](../02-flow/03-nodes-external.md#the-judiciary--standard-subsystem) renders a verdict and sets `linkedRuling` on a feedback item, the [Archivist](../02-flow/04-system-services.md#archivist) enforces judicial finality:

- `wont_fix` with `linkedRuling` — the only valid next transition is `resolved` via `Feedback.AcceptRefusal()`. The reviewing node must accept the verdict. Attempting `Feedback.RejectRefusal()` or `Feedback.Deadlock()` returns `CONTEMPT_VIOLATION`.
- `rejected` with `linkedRuling` — the only valid next transition is `actioned` via `Feedback.Resolve()`, then to `resolved` via `Feedback.AcceptFix()`. The refining node must comply with the ruling. Attempting `Feedback.Refuse()` or `Feedback.Deadlock()` returns `CONTEMPT_VIOLATION`.
- Any state with `linkedRuling` — `Feedback.Deadlock()` is blocked regardless of the item's current state. Re-escalation of a judicially resolved dispute returns `CONTEMPT_VIOLATION`.

`CONTEMPT_VIOLATION` is a permanent rejection, not a retriable error. The ruling is not a suggestion — the losing side must accept the verdict. The contempt guard is enforced by the Archivist regardless of node identity, capability grants, or topology. No node, including the Judiciary nodes, can override a ruling once applied to a feedback item.

## Store and Link Pattern

Feedback messages are capped at 1024 characters. For detailed analysis that exceeds this limit, use the Store & Link pattern:

1. Store the full analysis as an artefact using [`Artefact.Store()`](./02-sdk-artefacts.md#write-and-versioning-operations).
2. Reference the artefact `id` in the feedback message.

The artefact carries the detail; the feedback message carries the pointer. The analysis artefact participates in normal versioning and provenance, making the full reasoning permanently auditable.

## Capability and Error Semantics

Feedback operations map to capability requirements:

| Operation | Required Capability | Enforcing Service |
|-----------|-------------------|-------------------|
| `Workitem.AddFeedback` | `WRITE:feedback/new` | Archivist |
| `Feedback.Resolve` | `WRITE:feedback/actioned` | Archivist |
| `Feedback.Refuse` | `WRITE:feedback/wont_fix` | Archivist |
| `Feedback.AcceptFix`, `Feedback.AcceptRefusal` | `WRITE:feedback/resolved` | Archivist |
| `Feedback.RejectFix`, `Feedback.RejectRefusal` | `WRITE:feedback/rejected` | Archivist |
| `Feedback.Deadlock` | `WRITE:feedback/deadlocked` | Archivist |
| `Workitem.GetFeedback`, `Artefact.GetFeedback`, `Workitem.HasUnresolvedFeedback`, `Artefact.HasUnresolvedFeedback`, `Feedback.GetDepth` | `READ:feedback` | Archivist |

Common error conditions:

| Error | Cause |
|-------|-------|
| `CAPABILITY_DENIED` | Node lacks the required capability for the operation |
| `INVALID_STATE_TRANSITION` | Requested transition is not permitted from the current state |
| `CONTEMPT_VIOLATION` | Attempt to override a judicial ruling (`linkedRuling` set) |
| `FEEDBACK_NOT_FOUND` | Specified feedback ID does not exist on the artefact |
| `MESSAGE_TOO_LONG` | Message exceeds 1024 character limit |

All errors are structured and carry stable error codes. Full error semantics are in the [Error Catalogue](../05-reference/error-catalogue.md).

## Feedback SDK Invariants

1. Feedback is scoped to Workitem ID + artefact `id` and persisted by the [Archivist](../02-flow/04-system-services.md#archivist).
2. Feedback operations are accessed through domain objects (`Workitem`, `Artefact`, `Feedback`). No `context.Context` is accepted.
3. Feedback is tagged with the artefact version hash it was raised against and preserved across versions.
4. The Archivist enforces state machine transitions — only the transitions listed above are permitted.
5. `Feedback.Refuse` requires a structured [justification](#refusal-and-justification-contract) (citation or novel argument).
6. The [contempt guard](#contempt-guard-behaviour) is absolute — `CONTEMPT_VIOLATION` is a permanent rejection.
7. Every `Workitem.AddFeedback` call transparently emits friction at magnitude equal to feedback depth.
8. Only `resolved` is a terminal state; all other states are unresolved from the gate node's perspective.
9. Deadlock detection is per feedback item, not per Workitem.
10. Feedback history is append-only — the investigative record cannot be rewritten.
11. Feedback messages are capped at 1024 characters; detailed analysis uses the Store & Link pattern.
