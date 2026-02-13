# SDK Overview

## Goal

Define the SDK contract that node developers use to execute Workitem-scoped logic through Sidecar mediation while runtime services remain authoritative for authorisation and state changes.

## SDK Runtime Role

Describe where the SDK sits between node handlers, Sidecar mediation, and Operator/Archivist/Librarian authority boundaries.

## Execution Scope Model

Define Workitem-scoped handler execution and the guarantees the SDK provides for per-assignment isolation.

## Trust and Authority Boundaries

Clarify authentication via Sidecar, service-side authorisation, and authoritative mutation ownership by runtime services.

## SDK Surface Map

Introduce the SDK domains: core, artefacts, legal, feedback, Workitems, and telemetry.

## Failure and Error Model

Set expectations for structured errors, deterministic rejection behaviour, and retry-safe handler design.

## Relationship to Reference Docs

Delegate wire and schema details to gRPC and CRD reference sections.

## SDK Invariants

Capture non-negotiable behavioural rules that all SDK domain docs preserve.
