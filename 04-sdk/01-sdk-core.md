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

Clarify that `complete()` is exit-bound and validated by Operator against configured exit contracts.

## Error Taxonomy and Recovery

Document common error classes, retry posture, and which failures are terminal versus retriable.

## Concurrency and Idempotency Expectations

Set expectations for replay-safe handler behaviour and deterministic outputs under retries.

## Core Invariants

Capture rules that all domain-specific SDK surfaces inherit.
