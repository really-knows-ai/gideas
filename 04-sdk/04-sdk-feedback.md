# SDK Feedback

## Goal

Define SDK operations for creating, updating, and resolving feedback under governance lifecycle constraints.

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
