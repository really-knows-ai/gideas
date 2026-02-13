# SDK Workitems

## Goal

Define the SDK's Workitem-facing operations while preserving Operator ownership of control-plane transitions.

## Workitem Read Surface

Describe what Workitem state handlers can inspect through SDK abstractions.

## Assignment-Scoped Action Model

Clarify that SDK actions apply to the currently assigned Workitem only.

## Local Workitem Creation

Define creation/admission semantics and entry-contract requirements for locally created Workitems.

## Routing and Outcome Submission

Specify how handlers return routing outcomes and how Operator validation applies.

## Mutation Authority Boundaries

Clarify which mutations are requestable through SDK and which remain Operator-owned only.

## Cross-Flow Related SDK Paths

Cover SDK constraints relevant to export/import-triggered Workitem handling.

## Workitem SDK Invariants

Capture control-plane safety guarantees that Workitem APIs must preserve.
