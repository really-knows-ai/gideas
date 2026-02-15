# Error Catalog

All runtime errors use structured responses with stable error codes. Errors originate from two layers: the [Sidecar](../03-node/01-sidecar.md) (local validation) and runtime services (authoritative enforcement). Both layers use the same error shape.

## Error Model

Every error carries:

| Field | Type | Description |
|-------|------|-------------|
| `code` | `string` | Stable error identifier. Does not change across releases. |
| `message` | `string` | Human-readable description of the failure. |
| `grpc_status` | `integer` | gRPC status code. |
| `retryable` | `bool` | Whether the error is transient and safe to retry with backoff. |

The SDK provides two classification utilities:

| Utility | Behaviour |
|---------|-----------|
| `IsRetryable(err)` | Returns `true` for transient failures where the stable error code is `SERVICE_UNAVAILABLE`. Checks the stable code, not the gRPC status — `TIMEOUT_EXCEEDED` uses gRPC `DEADLINE_EXCEEDED` but is not retryable. Safe to retry with exponential backoff. |
| `IsError(err, code)` | Returns `true` if the error matches a specific stable error code. |

Errors produce no state change. A rejected operation leaves the system in its prior state.

---

## Control-Plane Guard Errors

Emitted by the [Operator](../02-flow/01-operator.md) when routing, lifecycle, or completion guards fail.

| Code | gRPC Status | Cause | Caller Response |
|------|-------------|-------|-----------------|
| `EXIT_NOT_BOUND` | `FAILED_PRECONDITION` | `Complete()` called from a node that is not bound to an exit contract. | Do not retry. Review node configuration — add the required exit contract binding. |
| `ENTRY_NOT_BOUND` | `FAILED_PRECONDITION` | `CreateWorkitem` called from a node without an entry binding, or Workitem admission attempted at a node that is not entry-bound. | Do not retry. Review node configuration — add the required entry contract binding. |
| `INVALID_ROUTE` | `FAILED_PRECONDITION` | Routing instruction references an output name not configured on the node, or a target node that does not exist in the Flow topology. | Do not retry. Fix the routing logic or update node/Flow configuration. |
| `THRASH_BUDGET_EXCEEDED` | `FAILED_PRECONDITION` | The Workitem's aggregate visit count across all nodes exceeds the configured `maxVisits` in [governance policy](./crds.md#governance-policy). The Workitem transitions to `Failed`. | Terminal for this Workitem. Investigate the routing loop. Review topology and governance policy thresholds. |
| `TIMEOUT_EXCEEDED` | `DEADLINE_EXCEEDED` | The node's inactivity timer expired. No SDK call or explicit `Heartbeat()` was received within the configured timeout window. | The Sidecar cancels the handler context and reports the failure. The Operator transitions the Workitem per its failure policy. For long-running workloads, use explicit `Heartbeat()` calls or the FoundryAgent pattern. |
| `CONTRACT_VIOLATION` | `FAILED_PRECONDITION` | Entry contract requirements not satisfied (at admission) or exit contract requirements not satisfied (at completion). Artefacts of a required kind are missing, or required stamps are not present on the current version. | Do not retry without addressing the missing artefacts or stamps. Route the Workitem to nodes that can provide the missing governance state. |
| `ASSIGNMENT_SCOPE_VIOLATION` | `FAILED_PRECONDITION` | An SDK operation attempted to access or mutate state outside the current Workitem assignment. The Sidecar rejected the request before it reached a service. | Do not retry. This indicates a bug in handler code — the handler is attempting to operate on a foreign Workitem. |

---

## Capability and Authorisation Errors

Emitted by runtime services when the requesting node lacks the required permission.

| Code | gRPC Status | Cause | Caller Response |
|------|-------------|-------|-----------------|
| `CAPABILITY_DENIED` | `PERMISSION_DENIED` | The node does not hold the required capability for the requested operation. The owning service checked the node's capability grants (injected by the Sidecar) and denied the request. | Do not retry — permanent until configuration changes. The error indicates the operation was denied; it does not reveal which specific grant is missing. Review the node's capability grants in the [FoundryNode](./crds.md#foundrynode) CRD. |
| `IDENTITY_EXPIRED` | `UNAUTHENTICATED` | The Sidecar's identity material (certificate or token) has expired or is invalid. All requests fail until the identity is renewed. | The Sidecar handles certificate renewal automatically. If renewal fails, the node pod requires restart or Operator intervention. |

---

## Governance and Finality Errors

Emitted by the [Archivist](../02-flow/04-system-services.md#archivist) when governance constraints are violated.

| Code | gRPC Status | Cause | Caller Response |
|------|-------------|-------|-----------------|
| `CONTEMPT_VIOLATION` | `FAILED_PRECONDITION` | The handler attempted a feedback state transition that contradicts an [Assay](../01-concepts/02-foundry-cycle.md#assay-judiciary--standard-component)-linked judicial ruling. Once `linkedRuling` is set on a feedback item, the losing side must accept the verdict. | **Permanent rejection. Do not retry.** The ruling is not a suggestion. The handler must comply: call `AcceptRefusal()` (if the refiner won) or `ResolveFeedback()` then `AcceptFix()` (if the reviewer won). See [contempt guard](../01-concepts/03-data-model.md#contempt-guard). |
| `STAMP_ALREADY_APPLIED` | `ALREADY_EXISTS` | The named stamp has already been applied to this artefact version. Stamps are write-once per content hash. | Do not retry — the stamp already exists. If independent sign-off is needed from different actors, define different stamp names in the [GovernedArtefact](./crds.md#governedartefact) stamp vocabulary. |
| `INVALID_STATE_TRANSITION` | `FAILED_PRECONDITION` | The requested feedback state transition is not permitted from the item's current state. The Archivist enforces the [feedback state machine](../01-concepts/03-data-model.md#feedback-lifecycle) — only explicitly listed transitions are valid. | Do not retry. Check the feedback item's current state and use the correct transition operation. |
| `ARTEFACT_CORRUPTED` | `DATA_LOSS` | The SHA-256 hash of retrieved artefact content does not match the stored version hash. The Sidecar detected the mismatch on read. | Do not use the content. Report the corruption through telemetry. This indicates a storage integrity issue requiring operational investigation. |
| `ARTEFACT_KIND_CONFLICT` | `INVALID_ARGUMENT` | An operation referenced an existing artefact `id` with a different `kind` than previously established. Artefact kind is immutable for a given `id` within a Workitem. | Do not retry. The artefact `id` is already bound to a different kind. Use a different `id` for the new artefact, or use the existing kind. |

---

## Configuration and Validation Errors

Emitted at CRD admission time by the Operator, or at request time when configuration state is inconsistent.

| Code | gRPC Status | Cause | Caller Response |
|------|-------------|-------|-----------------|
| `INVALID_CAPABILITY` | `INVALID_ARGUMENT` | A capability string in the FoundryNode CRD uses an invalid verb, is missing a required qualifier, or has unknown syntax. | Fix the capability string. The Operator does not reconcile a FoundryNode with syntactically invalid capabilities. See [capability syntax](./crds.md#capability-syntax). |
| `UNKNOWN_CONTRACT` | `INVALID_ARGUMENT` | A node's entry or exit binding references a contract name not defined in the FoundryFlow's `entryContracts` or `exitContracts`. | Fix the binding to reference an existing contract, or add the contract to the FoundryFlow CRD. |
| `IMPORT_NODE_INVALID` | `FAILED_PRECONDITION` | The `importNode` field references a node that does not exist, or the referenced node is not bound to an entry contract. | Fix the `importNode` reference or add an entry binding to the target node. Cross-flow import is rejected until this is resolved. |
| `SCHEMA_VALIDATION_FAILED` | `INVALID_ARGUMENT` | CRD admission validation failed — missing required fields, invalid field types, constraint violations, or structural inconsistencies. | Fix the CRD content. The Operator provides a descriptive message identifying the specific validation failure. |

---

## Cross-Flow and Trust Errors

Emitted during cross-flow exchange, trust verification, and law integration.

| Code | gRPC Status | Cause | Caller Response |
|------|-------------|-------|-----------------|
| `TRUST_CHAIN_INVALID` | `PERMISSION_DENIED` | The certificate chain on an imported stamp or package does not validate against the expected trust root (State Root CA for siblings, Treaty CA for non-siblings). | Reject the import. Investigate certificate configuration. This may indicate an expired certificate, a misconfigured trust root, or a security incident. |
| `TREATY_NOT_FOUND` | `NOT_FOUND` | A cross-flow operation was attempted between non-sibling Flows with no Treaty configured for the required direction. | Configure a Treaty CRD for the required direction before attempting the exchange. |
| `NATURALISATION_REQUIRED` | `FAILED_PRECONDITION` | Imported stamps from a treaty crossing do not carry local governance authority. The stamps are preserved for provenance but do not satisfy local stamp requirements. | Process the Workitem through the local governance loop. The normal routing cycle drives the Workitem to nodes configured to provide local stamps. |
| `IMPORT_ADMISSION_FAILED` | `FAILED_PRECONDITION` | The imported Workitem does not satisfy the import node's bound entry contract. | The receiving Flow rejected the import. Review the import node's entry contract requirements and the exported artefact state. |

---

## Data Errors

Emitted when referenced data is missing or invalid.

| Code | gRPC Status | Cause | Caller Response |
|------|-------------|-------|-----------------|
| `FEEDBACK_NOT_FOUND` | `NOT_FOUND` | The specified feedback ID does not exist on the artefact. | Verify the feedback ID. The item may have been addressed by another assignment or may reference a different artefact. |
| `LAW_NOT_FOUND` | `NOT_FOUND` | A cited law has been retired or does not exist in the Library. | The law is gone. Do not retry. If the citation was used for justification, the handler may need to find alternative governance support or propose a novel argument. |
| `MESSAGE_TOO_LONG` | `INVALID_ARGUMENT` | A feedback message exceeds 1024 characters, or a Finding goal exceeds the maximum length. | Reduce content length. For detailed analysis, use the Store & Link pattern — store the full analysis as an artefact and reference it in the message. |

---

## Transient Errors

Emitted when a service is temporarily unreachable.

| Code | gRPC Status | Cause | Caller Response |
|------|-------------|-------|-----------------|
| `SERVICE_UNAVAILABLE` | `UNAVAILABLE` | A runtime service (Archivist, Librarian, Flow Monitor, Support Service) is temporarily unreachable. | Retry with exponential backoff. `IsRetryable(err)` returns `true` for this error. If the service remains unavailable after retry budget exhaustion, the handler should fail the assignment or route to a fallback path. |

---

## Caller Response Guidance

| Error Family | Retryable | Recommended Response |
|--------------|-----------|---------------------|
| Control-plane guard (`EXIT_NOT_BOUND`, `ENTRY_NOT_BOUND`, `INVALID_ROUTE`, `CONTRACT_VIOLATION`, `ASSIGNMENT_SCOPE_VIOLATION`) | No | Fix configuration or handler logic. Do not retry. |
| Thrash and timeout (`THRASH_BUDGET_EXCEEDED`, `TIMEOUT_EXCEEDED`) | No | Terminal or Operator-managed. Investigate root cause. |
| Capability (`CAPABILITY_DENIED`) | No | Review node capability grants. |
| Governance finality (`CONTEMPT_VIOLATION`) | No | Comply with the ruling. |
| Write-once (`STAMP_ALREADY_APPLIED`) | No | Stamp exists. Proceed. |
| State machine (`INVALID_STATE_TRANSITION`) | No | Check current state, use correct operation. |
| Data integrity (`ARTEFACT_CORRUPTED`) | No | Report and investigate. |
| Identity conflict (`ARTEFACT_KIND_CONFLICT`) | No | Use correct `id`/`kind` pairing. |
| Configuration (`INVALID_CAPABILITY`, `UNKNOWN_CONTRACT`, `IMPORT_NODE_INVALID`, `SCHEMA_VALIDATION_FAILED`) | No | Fix CRD configuration. |
| Cross-flow trust (`TRUST_CHAIN_INVALID`, `TREATY_NOT_FOUND`, `NATURALISATION_REQUIRED`, `IMPORT_ADMISSION_FAILED`) | No | Fix trust configuration or process through local governance. |
| Missing data (`FEEDBACK_NOT_FOUND`, `LAW_NOT_FOUND`) | No | Resource is absent. Adapt logic. |
| Content limit (`MESSAGE_TOO_LONG`) | No | Reduce content length. |
| Transient (`SERVICE_UNAVAILABLE`) | Yes | Retry with exponential backoff. |

---

## Error Invariants

1. Every error carries a stable `code` that does not change across releases.
2. Errors produce no state change — a rejected operation leaves the system in its prior state.
3. `CONTEMPT_VIOLATION` is a permanent rejection. No retry, no override, no exemption.
4. Capability denials do not reveal which specific grant is missing.
5. Telemetry emission failures do not produce errors visible to the handler — they are absorbed by the Sidecar.
6. Configuration errors are caught at CRD admission time. Runtime services do not encounter malformed configuration.
7. `IsRetryable` returns `true` only for errors with stable code `SERVICE_UNAVAILABLE`. It checks the stable error code, not the gRPC status code.
8. gRPC status codes follow a consistent mapping: `PERMISSION_DENIED` for capability failures, `FAILED_PRECONDITION` for guard violations, `NOT_FOUND` for missing resources, `ALREADY_EXISTS` for write-once violations, `UNAVAILABLE` for transient failures, `INVALID_ARGUMENT` for malformed input, `DATA_LOSS` for integrity failures.
