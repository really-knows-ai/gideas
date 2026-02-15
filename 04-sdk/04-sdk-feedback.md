# SDK Feedback

## Goal

Define SDK operations for creating, updating, and resolving feedback under governance lifecycle constraints.

## Feedback Friction

Every `AddFeedback` call transparently emits an [`AddFriction`](./06-sdk-telemetry.md#addfriction--node-context) event. The node does not call `AddFriction` separately for feedback — the SDK handles it.

The magnitude equals the feedback depth for that item on that artefact. The first feedback on an item emits magnitude 1, the second emits 2, the nth emits n. Friction is attributed to the Workitem and the current node.

This creates a naturally escalating cost signal. Early feedback is cheap. Repeated rounds of disagreement on the same item become progressively more expensive, making the cost of the adversarial loop visible before it reaches deadlock.

The friction emission is transparent and mandatory — nodes cannot suppress it. The node's `AddFeedback` call succeeds or fails on its own merits; the friction event is a side effect recorded by the [Flow Monitor](../02-flow/04-system-services.md#flow-monitor-and-friction-surface).

## Feedback Query Surfaces

Describe retrieval APIs used by handlers and gate logic (including unresolved/deadlock checks).

## Feedback State Transitions

Specify allowed transition requests and required payloads for each operation.

## Refusal and Justification Contract

Define structured justification requirements for `wont_fix` decisions.

## Deadlock and Assay Interaction

Describe deadlock escalation semantics and how handlers should respond to judicial outcomes.

## Contempt Guard Behaviour

Clarify `linkedRuling` finality and `CONTEMPT_VIOLATION` rejection behaviour enforced by Archivist.

## Capability and Error Semantics

Map feedback actions to capabilities, denial modes, and retry guidance.

## Feedback SDK Invariants

Capture non-negotiable lifecycle and finality guarantees.
