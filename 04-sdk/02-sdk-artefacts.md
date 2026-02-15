# SDK Artefacts

## Goal

Define how SDK users read, write, and govern artefacts while Archivist remains the provenance authority.

## Artefact Identity Semantics

Describe stable `id` and immutable `kind` behaviour within a Workitem.

## Read and Query Operations

Specify artefact lookup by ID/kind, version access, and metadata retrieval behaviour.

## Write and Versioning Operations

Define content write semantics, content-hash versioning, and no-op behaviour for unchanged content.

## Passport and Stamp Access

Describe SDK access to passport/stamp data and stamp application request semantics.

### Stamp Inspection API

The Artefact object exposes only **factual inspection** methods regarding stamps. The SDK provides tools for querying what stamps exist, not for judging their meaning.

Required methods:

- `GetStamps() []Stamp`: Returns the full list of stamps on the current version.
- `HasStamp(name string) bool`: Returns `true` if the named stamp exists.

### Negative Constraints (Do Not Implement)

- **No Validation Logic:** Do not include methods like `IsValid()`, `IsCompliant()`, or `Satisfies(contract)`. The node does not judge the artefact; it only inspects it.
- **No Magic Strings:** Do not include helper methods for specific conventions, such as `IsApproved()` or `IsSecurityReviewed()`. Governance semantics belong to the Operator and the Flow definition, not the SDK.

## Capability-Gated Artefact Actions

Map artefact and stamp operations to capability requirements and service-authorised rejection behaviour.

## Provenance and Audit Expectations

Clarify that authoritative artefact mutation history is emitted by Archivist.

## Artefact Invariants

Capture non-negotiable artefact behaviour guarantees exposed by the SDK.
