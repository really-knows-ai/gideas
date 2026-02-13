# Error Catalog

## Goal

Define stable runtime error identifiers, meanings, and caller response guidance.

## Error Model

Define error identity stability, actor attribution, and status-code mapping conventions.

## Control-Plane Guard Errors

List routing, lifecycle, and completion guard failures emitted by Operator boundaries.

## Capability and Authorisation Errors

List capability and permission failures emitted by authoritative runtime services.

## Governance and Finality Errors

Define governance-specific failures, including judicial finality violations such as `CONTEMPT_VIOLATION`.

## Configuration and Validation Errors

List configuration admission and schema validation failures across CRD and API boundaries.

## Cross-Flow and Trust Errors

List errors related to import/export, trust-chain verification, and naturalisation boundaries.

## Caller Response Guidance

Provide expected retry/abort/escalate handling per error class.

## Error Invariants

Capture non-negotiable error semantics that must remain stable across releases.
