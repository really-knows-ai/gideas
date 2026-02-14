# SDK Overview

## Goal

Define the SDK contracts for node developers (Workitem-scoped handler execution) and Support Service developers (capability endpoint implementation), both mediated through Sidecar boundaries while runtime services remain authoritative for authorisation and state changes.

## SDK Runtime Role

Describe where the SDK sits between node handlers, Sidecar mediation, and Operator/Archivist/Librarian authority boundaries.

## Execution Scope Model

Define Workitem-scoped handler execution and the guarantees the SDK provides for per-assignment isolation.

## Trust and Authority Boundaries

Clarify authentication via Sidecar, service-side authorisation, and authoritative mutation ownership by runtime services.

## SDK Surface Map

Introduce the SDK domains: core, artefacts, legal, feedback, Workitems, telemetry, and the `FlowSupportService` base class for Support Service implementations.

## FlowSupportService Base Class

Define the SDK base class for Flow Support Service implementations. Covers capability declaration, gRPC endpoint registration, health reporting, and the simplified permission model distinct from node handler execution. Specialised subtypes (such as `CodificationService`) extend `FlowSupportService` with subtype-specific contracts.

## Failure and Error Model

Set expectations for structured errors, deterministic rejection behaviour, and retry-safe handler design.

## Relationship to Reference Docs

Delegate wire and schema details to gRPC and CRD reference sections.

## SDK Invariants

Capture non-negotiable behavioural rules that all SDK domain docs preserve.
