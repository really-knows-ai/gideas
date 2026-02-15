# SDK Core

## Goal

Define the core handler and routing contract for node implementations using the SDK.

## Core Types and Interfaces

Specify the primary SDK objects (`Workitem`, `Artefact`, handler context, and routing result types).

## Handler Lifecycle Contract

Describe how handlers are invoked, what they may do during execution, and how they terminate with one routing outcome.

## Routing Instruction Model

Define `route_to_output`, `route_to`, and `complete` semantics and response expectations.

## Completion Semantics

### Blind Completion

The `Complete(route_to string)` method represents a **submission of work**, not a declaration of validity. When a node calls `Complete()`, it is signalling that it has finished its processing and is handing control back to the platform. It makes no assertion about whether the artefact satisfies any governance contract.

### Operator Exit Contract Responsibility

Exit Contract validation happens **after** the SDK call, within the Operator. The SDK must not attempt to "pre-validate" against the contract locally.

If the node calls `Complete()` but the artefact lacks the required stamps (as defined in the Flow's `exitContract`), the Operator will reject the completion and return an error to the node (or fail the Workitem). This ensures that all governance decisions are made in a single, authoritative location — the Operator — and never duplicated in node code.

## Error Taxonomy and Recovery

Document common error classes, retry posture, and which failures are terminal versus retriable.

## Concurrency and Idempotency Expectations

Set expectations for replay-safe handler behaviour and deterministic outputs under retries.

## Core Invariants

Capture rules that all domain-specific SDK surfaces inherit.
