# SDK Artefacts

The artefact SDK surface provides read, write, versioning, and stamp operations for [governed artefacts](../01-concepts/03-data-model.md#governed-artefacts). All artefact state — version history, passport stamps, feedback, and raw content bytes — is persisted by the [Archivist](../02-flow/04-system-services.md#archivist). The SDK mediates access through the [Sidecar](../03-node/01-sidecar.md); nodes never interact with the Archivist directly.

## Artefact Identity Semantics

Each artefact on a [Workitem](../02-flow/02-workitem.md) is identified by two fields:

| Field | Behaviour |
|-------|-----------|
| `id` | Unique within the Workitem. Fixed once introduced — the same `id` refers to the same artefact for the Workitem's lifetime. Used as the primary key for all Archivist lookups. |
| `governedArtefact` | Immutable for a given `id`. Matches a [GovernedArtefact](../05-reference/crds.md#governedartefact) `metadata.name` declared in the Flow. Determines which [laws](./03-sdk-legal.md), [stamps](#stamp-operations), and [contract requirements](../02-flow/05-configuration.md#entry-and-exit-contract-semantics) apply. |

Multiple artefacts of the same `governedArtefact` are supported — each has a distinct `id`.

The [Archivist](../02-flow/04-system-services.md#archivist) is the single source of truth for artefact-to-Workitem associations. Each artefact records the `workitem_id` it belongs to. The Archivist stores full version history, stamps, and feedback, keyed by `workitem_id` and artefact `id`. This keeps the Workitem CRD small and watchable regardless of version depth or feedback volume.

## Read and Query Operations

Artefact reads are scoped to the current [assignment](./01-sdk-core.md#handler-lifecycle-contract). No parameter exists for targeting artefacts on a different Workitem. All operations are accessed through the `Workitem` domain object, which returns `*Artefact` domain objects that carry their own session reference — no `context.Context` is needed.

### Workitem Entry Points

| Operation | Returns | Description |
|-----------|---------|-------------|
| `Workitem.GetArtefact(governedArtefact)` | `(*Artefact, error)` | Look up artefact by governed artefact kind (e.g. `"haiku"`, `"petition"`). Returns the head version. |
| `Workitem.FindArtefact(artefactID)` | `(*Artefact, error)` | Look up a specific artefact by its unique artefact ID (e.g. `"art-abc-123"`). |

### Artefact Domain Object

The returned `*Artefact` provides read, write, and stamp operations:

| Method | Returns | Description |
|--------|---------|-------------|
| `Artefact.ID()` | `string` | Artefact identifier (no round-trip). |
| `Artefact.GovernedArtefact()` | `string` | Governed artefact name (no round-trip). |
| `Artefact.VersionHash()` | `string` | Current version hash (no round-trip). |
| `Artefact.IsNewVersion()` | `bool` | True if the artefact was created as a new version (no round-trip, set after `Store`). |
| `Artefact.GetContent()` | `([]byte, error)` | Retrieves content from the Archivist. Cached for the handler invocation. |
| `Artefact.GetStamps()` | `([]*Stamp, error)` | Returns all stamps on the current version. |
| `Artefact.HasStamp(name)` | `(bool, error)` | Checks whether the named stamp exists on the current version. |
| `Artefact.GetFeedback()` | `([]*Feedback, error)` | Returns all feedback items for the artefact. |
| `Artefact.HasUnresolvedFeedback()` | `(bool, error)` | True if any feedback is in a non-resolved state. |

The Sidecar verifies content integrity on fetch: `SHA256(content) == storedHash`. A hash mismatch produces an `ARTEFACT_CORRUPTED` error.

The Sidecar caches artefact content for the duration of the handler invocation. Repeated reads of the same version within one handler do not generate additional Archivist requests. The cache is discarded when the assignment completes.

The `Workitem` object returned at handler invocation is a snapshot of state at assignment time. Artefact content, versions, and stamps are fetched from the Archivist on demand through the `Artefact` domain object methods above.

## Write and Versioning Operations

Artefact writes are content-addressed. Every version is identified by a SHA256 hash of its content. Writes are performed through the `Artefact.Store()` domain method.

| Operation | Behaviour |
|-----------|-----------|
| `Artefact.Store(content)` | Writes content to the Archivist for the artefact's `id` and `governedArtefact`. Updates local `VersionHash()` and `IsNewVersion()` state. |

Write outcomes depend on whether the `id` already exists on the Workitem:

| Scenario | Outcome |
|----------|---------|
| New `id` | Archivist stores content, version record, and artefact-to-Workitem association (`id`, `governedArtefact`, `workitem_id`). |
| Existing `id`, same `governedArtefact`, new content | Archivist stores a new version. Workitem reference unchanged. |
| Existing `id`, same `governedArtefact`, identical content | No-op. Content hash matches an existing version — no new version is created. |
| Existing `id`, different `governedArtefact` | **Rejected.** `governedArtefact` is immutable for a given `id`. Returns an identity conflict error. |
| New `id`, unregistered `governedArtefact` | **Rejected.** The `governedArtefact` name must match a [GovernedArtefact](../05-reference/crds.md#governedartefact) CRD registered in the Flow. Returns an unknown governed artefact error. |

The Sidecar computes the content hash before sending the write to the Archivist. The node does not compute or supply hashes.

A new version starts with an empty passport — no stamps carry over from previous versions. Stamps are bound to a specific content hash; changing content invalidates all prior governance sign-off.

```mermaid
sequenceDiagram
    participant HD as Handler
    participant SC as Sidecar
    participant AR as Archivist

    HD->>HD: wi.GetArtefact(governedArtefact)
    HD->>SC: Artefact.Store(content)
    SC->>SC: Compute SHA256 hash
    SC->>AR: Persist content bytes + version record + Workitem association
    AR-->>SC: Version hash confirmed
    SC-->>HD: Version hash + isNewVersion
```

## Stamp Operations

[Stamps](../01-concepts/03-data-model.md#passports-and-stamps) are named governance checkpoints on an artefact's passport. The SDK provides inspection and application operations.

### Stamp Inspection

| Operation | Returns |
|-----------|---------|
| `Artefact.GetStamps()` | Full list of stamps on the artefact's current version. |
| `Artefact.HasStamp(name)` | `true` if the named stamp exists on the current version. |

Stamp inspection methods are factual queries. The SDK exposes what stamps exist, not what they mean. Governance semantics — which stamps are required, whether an artefact is "approved" — belong to the [Operator](../02-flow/01-operator.md) and [exit contract](../02-flow/05-configuration.md#entry-and-exit-contract-semantics) configuration.

Methods that interpret stamp semantics are intentionally absent:

- No `IsValid()`, `IsCompliant()`, or `Satisfies(contract)` — the node does not judge artefact validity.
- No `IsApproved()` or `IsSecurityReviewed()` — stamp names are conventions chosen by the [Flow Engineering Team](../02-flow/05-configuration.md#stamp-grant-and-capability-semantics), not privileged system constants.

### Stamp Application

| Operation | Behaviour |
|-----------|-----------|
| `Artefact.Stamp(name)` | Apply a named stamp to the artefact's current version. |

Stamp application is capability-gated. The node must hold `STAMP:artefact/<governed-artefact-name>/<stamp-name>` for the artefact's governed artefact name and the specific stamp name. The stamp name must also be declared in the artefact's [GovernedArtefact](../05-reference/crds.md#governedartefact) stamp vocabulary — stamp names not in the vocabulary are rejected at configuration admission. The [Archivist](../02-flow/04-system-services.md#archivist) validates the capability grant and records the stamp with the applying node's identity, the artefact's current content hash, and a cryptographic signature from the Sidecar's identity material.

Stamps are write-once per artefact version. Applying the same stamp name to the same content hash a second time — whether from the same node or a different one — produces an error. If two different nodes need to independently sign off on the same artefact, define two different stamp names.

The platform attaches no special semantics to any stamp name. "approval" is a naming convention. The [reference arrangement](../01-concepts/02-foundry-cycle.md) uses an "approval" stamp applied by Sort as the final gate, but this is convention, not system behaviour.

## Capability-Gated Actions

Artefact operations map to capability requirements enforced by the backing service:

| Operation | Required Capability | Enforcing Service |
|-----------|-------------------|-------------------|
| `Workitem.GetArtefact`, `Artefact.GetContent`, `Artefact.GetStamps`, `Artefact.GetFeedback` | `READ:artefact` | Archivist |
| `Artefact.Store` | `WRITE:artefact` or `WRITE:artefact/<governed-artefact-name>` | Archivist |
| `Artefact.Stamp` | `STAMP:artefact/<governed-artefact-name>/<stamp-name>` | Archivist |
| `Law.Attest`, `LawGroup.Attest` | `ATTEST:artefact/<governed-artefact-name>/<stamp-name>` or `ATTEST:artefact/<governed-artefact-name>/law-*` | Archivist |
| Feedback operations | See [SDK Feedback](./04-sdk-feedback.md#capability-and-error-semantics) | Archivist |

Missing capabilities produce a `CAPABILITY_DENIED` error from the service. The Sidecar forwards the denial to the handler as a structured error with no state change.

### Implicit Attestation Model

Law attestation stamps (`law-<lawID>-<repType>`, `lawgrp-<group>`) are not listed in the exit contract. They are computed dynamically from applicable laws at completion time by the operator's `VerifyLawAttestations` check. The `ATTEST:` capability prefix on a node declares what the node can attest to, using the same wildcard matching rules as `STAMP:` capabilities. A node declaring `ATTEST:artefact/haiku/law-*` can attest any law stamp for haiku artefacts. See [Law Attestation Semantics](../02-flow/09-law-attestation.md) for details.

## Provenance and Audit

The [Archivist](../02-flow/04-system-services.md#archivist) is the sole authority for artefact provenance. Version history, stamp records, and feedback state are persisted in the Archivist's SQLite database. Raw content bytes are stored in the blob store, keyed by content hash.

Audit trails for artefact mutations — version creation, stamp application, feedback transitions — are emitted by the Archivist, not by node code. The Archivist records the acting node's identity (injected by the Sidecar), the affected artefact, the operation, and a timestamp. Nodes do not need to emit supplementary audit telemetry for artefact operations; the authoritative record is service-owned.

## Artefact Invariants

1. `id` is unique and stable within a Workitem; `governedArtefact` is immutable for a given `id`.
2. All artefact operations are scoped to the current Workitem assignment. No `context.Context` is passed by the caller — the `Artefact` domain object carries its own session reference.
3. Content is addressed by SHA256 hash. Identical content produces no new version.
4. New versions start with an empty passport. Stamps do not carry over.
5. Stamps are write-once per artefact version per stamp name.
6. Stamp application requires `STAMP:artefact/<governed-artefact-name>/<stamp-name>` capability.
7. The SDK does not expose methods that interpret stamp semantics (no `IsValid`, no `IsApproved`).
8. The [Archivist](../02-flow/04-system-services.md#archivist) is the sole persistence authority for artefact provenance.
9. The Sidecar verifies content integrity on read and computes hashes on write.
10. Artefact content is cached by the Sidecar for the handler invocation duration.
