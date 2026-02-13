# 04-sdk Plan

## Section Purpose

`04-sdk/` defines the external developer interface used by node implementors. It specifies assignment-scoped APIs, behavioural guarantees, capability boundaries, and error semantics.

## Goals

- Give node developers a complete, practical programming contract.
- Keep SDK semantics aligned with Sidecar mediation and service/Operator authorisation boundaries.
- Separate API intent from transport and schema detail (which lives in reference docs).
- Ensure all SDK behaviours are testable and operationally observable.

## Planned Files

1. `04-sdk/00-overview.md`
2. `04-sdk/01-sdk-core.md`
3. `04-sdk/02-sdk-artefacts.md`
4. `04-sdk/03-sdk-legal.md`
5. `04-sdk/04-sdk-feedback.md`
6. `04-sdk/05-sdk-workitems.md`
7. `04-sdk/06-sdk-telemetry.md`

## File-by-File Scope

## `00-overview.md`

- SDK role in the runtime architecture.
- Assignment scoping model and trust assumptions.
- Relationship to Sidecar, Operator, Archivist, and Librarian.

## `01-sdk-core.md`

- Core types and handler lifecycle.
- Routing instruction contract and completion semantics.
- Common errors and retry posture.

## `02-sdk-artefacts.md`

- Artefact identity (`id`, `kind`) behaviour.
- Read/write/version interactions and no-op semantics.
- Passport and stamp access patterns.

## `03-sdk-legal.md`

- Law retrieval and context use.
- Citation operations and evidence capture.
- Tier 1 finding recording constraints.

## `04-sdk-feedback.md`

- Feedback creation, updates, and resolution states.
- Deadlock and judicial-link constraints.
- Query surfaces used by gate logic.

## `05-sdk-workitems.md`

- Assignment-scoped Workitem access.
- Local Workitem creation and entry-admission semantics.
- Constraints around lifecycle mutation authority.

## `06-sdk-telemetry.md`

- Telemetry and friction emission APIs.
- Required attribution fields and aggregation expectations.
- Operational guidance for useful instrumentation.

## Writing Order

1. `00-overview.md`
2. `01-sdk-core.md`
3. `02-sdk-artefacts.md`
4. `04-sdk-feedback.md`
5. `03-sdk-legal.md`
6. `05-sdk-workitems.md`
7. `06-sdk-telemetry.md`

## Consistency Checklist

- SDK calls remain assignment-scoped and Sidecar-mediated.
- SDK does not expose direct control-plane field mutation.
- Artefact provenance remains Archivist-owned.
- Stamp authority remains capability-scoped and write-once per version.
- Laws and hearing flows remain consistent with governance decisions.
