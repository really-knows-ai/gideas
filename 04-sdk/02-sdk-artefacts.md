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

## Capability-Gated Artefact Actions

Map artefact and stamp operations to capability requirements and service-authorised rejection behaviour.

## Provenance and Audit Expectations

Clarify that authoritative artefact mutation history is emitted by Archivist.

## Artefact Invariants

Capture non-negotiable artefact behaviour guarantees exposed by the SDK.
