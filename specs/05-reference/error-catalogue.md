# Error Catalogue

All runtime errors use structured responses with stable error codes. Errors originate from the [Sidecar](../03-node/01-sidecar.md) (local validation) and from runtime services (authoritative enforcement). Both layers use the same error shape.

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
| `INVALID_ROUTE` | `FAILED_PRECONDITION` | Routing instruction references an output name not configured on the node, or a target node that does not exist as a FoundryNode in the namespace. | Do not retry. Fix the routing logic or update node configuration. |
| `THRASH_BUDGET_EXCEEDED` | `FAILED_PRECONDITION` | The Workitem's aggregate visit count across all nodes exceeds the configured `maxVisits` in [governance policy](./crds.md#governance-policy). The Workitem transitions to `Failed`. | Terminal for this Workitem. Investigate the routing loop. Review topology and governance policy thresholds. |
| `TIMEOUT_EXCEEDED` | `DEADLINE_EXCEEDED` | The node's inactivity timer expired. No SDK call or explicit `Heartbeat()` was received within the configured timeout window. | The Sidecar cancels the handler context and reports the failure. The Operator transitions the Workitem per its failure policy. For long-running workloads, use explicit `Heartbeat()` calls or the FoundryAgent pattern. |
| `CONTRACT_VIOLATION` | `FAILED_PRECONDITION` | Entry contract requirements not satisfied (at admission) or exit contract requirements not satisfied (at completion). Artefacts of a required governed artefact are missing, or required stamps are not present on the current version. | Do not retry without addressing the missing artefacts or stamps. Route the Workitem to nodes that can provide the missing governance state. |
| `ASSIGNMENT_SCOPE_VIOLATION` | `FAILED_PRECONDITION` | An SDK operation attempted to access or mutate state outside the current Workitem assignment. The Sidecar rejected the request before it reached a service. | Do not retry. This indicates a bug in handler code — the handler is attempting to operate on a foreign Workitem. |
| `CHILDREN_NOT_TERMINAL` | `FAILED_PRECONDITION` | The handler called `Complete()` on a parent Workitem while one or more child Workitems are still in `Pending` or `Running` state. All children must reach a terminal state (`Completed` or `Failed`) before the parent can complete. | Wait for all child Workitems to reach terminal state. Use `GetChildren()` or `WatchChildren()` to monitor child lifecycle progress. |
| `CHILD_NOT_OWNED` | `FAILED_PRECONDITION` | An operation targeted a child Workitem whose `parentWorkitemID` does not match the caller's current Workitem. Cross-Workitem operations are scoped to the parent-child relationship. | Do not retry. Verify the child Workitem ID. The child may belong to a different parent Workitem. |
| `CHILD_ALREADY_ROUTED` | `FAILED_PRECONDITION` | An attempt to write artefacts to or re-route a child Workitem that has already been routed for processing. Once routed, the child is under normal Workitem assignment and the creating node can no longer mutate it. | Do not retry. The child is already in processing. Read results after the child completes. |
| `GROUP_ENTRY_VIOLATION` | `FAILED_PRECONDITION` | A root Workitem was routed to a group entry node but does not satisfy the group's entry contract. The Operator validated the group entry contract against artefact state in the Archivist and the requirements were not met. | Do not retry without addressing the missing artefacts or stamps required by the group entry contract. |
| `GROUP_ROUTING_DENIED` | `FAILED_PRECONDITION` | Routing from outside a NodeGroup to a non-entry-bound node inside the group. NodeGroups enforce routing isolation — external work can only enter through designated entry nodes. | Do not retry. Route to an entry-bound node within the group instead. |

---

## Capability and Authorisation Errors

Emitted by runtime services when the requesting node lacks the required permission.

| Code | gRPC Status | Cause | Caller Response |
|------|-------------|-------|-----------------|
| `CAPABILITY_DENIED` | `PERMISSION_DENIED` | The node does not hold the required capability for the requested operation. The owning service checked the node's capability grants (injected by the Sidecar) and denied the request. | Do not retry — permanent until configuration changes. The error indicates the operation was denied; it does not reveal which specific grant is missing. Review the node's capability grants in the [FoundryNode](./crds.md#foundrynode) CRD. |
| `IDENTITY_EXPIRED` | `UNAUTHENTICATED` | The Sidecar's identity material (certificate or token) has expired or is invalid. All requests fail until the identity is renewed. | The Sidecar handles certificate renewal automatically. If renewal fails, the node pod requires restart or Operator intervention. |

---

## Governance and Finality Errors

Emitted by the [Archivist](../02-flow/04-system-services.md#archivist) when governance constraints are violated, and by Judiciary nodes during deliberation failures.

| Code | gRPC Status | Cause | Caller Response |
|------|-------------|-------|-----------------|
| `CONTEMPT_VIOLATION` | `FAILED_PRECONDITION` | The handler attempted a feedback state transition that contradicts a judicially-linked ruling. Once `linkedRuling` is set on a feedback item (by the [Arbiter](../02-flow/03-nodes-external.md#the-judiciary--standard-subsystem) after deliberation), the losing side must accept the verdict. | **Permanent rejection. Do not retry.** The ruling is not a suggestion. The handler must comply: call `AcceptRefusal()` (if the refiner won) or `ResolveFeedback()` then `AcceptFix()` (if the reviewer won). See [contempt guard](../01-concepts/03-data-model.md#contempt-guard). |
| `STAMP_ALREADY_APPLIED` | `ALREADY_EXISTS` | The named stamp has already been applied to this artefact version. Stamps are write-once per content hash. | Do not retry — the stamp already exists. If independent sign-off is needed from different actors, define different stamp names in the [GovernedArtefact](./crds.md#governedartefact) stamp vocabulary. |
| `INVALID_STATE_TRANSITION` | `FAILED_PRECONDITION` | The requested feedback state transition is not permitted from the item's current state. The Archivist enforces the [feedback state machine](../01-concepts/03-data-model.md#feedback-lifecycle) — only explicitly listed transitions are valid. | Do not retry. Check the feedback item's current state and use the correct transition operation. |
| `ARTEFACT_CORRUPTED` | `DATA_LOSS` | The SHA-256 hash of retrieved artefact content does not match the stored version hash. The Sidecar detected the mismatch on read. | Do not use the content. Report the corruption through telemetry. This indicates a storage integrity issue requiring operational investigation. |
| `ARTEFACT_KIND_CONFLICT` | `INVALID_ARGUMENT` | An operation referenced an existing artefact `id` with a different `governed_artefact` than previously established. An artefact's governed artefact is immutable for a given `id` within a Workitem. | Do not retry. The artefact `id` is already bound to a different governed artefact. Use a different `id` for the new artefact, or use the existing governed artefact. |
| `DELIBERATION_HUNG` | `FAILED_PRECONDITION` | The internal tally on the Arbiter or Tribunal failed to reach consensus within the configured maximum deliberation rounds. The orchestrating node routes to its `hung` output — the Arbiter routes to the [HITL node](../04-sdk/08-sdk-hitl.md) for human escalation; the Tribunal routes to the HITL node for resolution. | Not directly retriable by nodes. The orchestrating node handles hung verdict routing internally. |
| `JUROR_INFERENCE_FAILED` | `INTERNAL` | A [Juror node's](../01-concepts/02-foundry-cycle.md#the-judiciary--standard-subsystem) inference call failed during deliberation. | Transient — the Juror node may be retried via the fan-out parent. If persistent, the child Workitem fails and the Arbiter or Tribunal accounts for the missing verdict according to its internal tally policy. |
| `LAW_WRITE_FAILED` | `INTERNAL` | The [law-applicator](../01-concepts/02-foundry-cycle.md#law-applicator) failed to persist an approved petition's law changes via the Librarian's `WriteLaw` method. | The law-applicator may retry internally. Persistent failures block the petition from being applied. |
| `QUEUE_ITEM_NOT_FOUND` | `NOT_FOUND` | A queue operation referenced an item that does not exist on the target shard. | Verify the item ID. The item may have been decided or may reside on a different shard. |
| `QUEUE_ITEM_ALREADY_CLAIMED` | `ALREADY_EXISTS` | An attempt to claim a queue item that is already in `claimed` state. | The item is already claimed. Wait for it to be released or decided. |
| `QUEUE_ITEM_INVALID_STATE` | `FAILED_PRECONDITION` | A queue state transition was attempted from an invalid state. For example, deciding or releasing an item that is not in `claimed` state. | Check the item's current state and use the correct operation. |
| `QUEUE_UNAVAILABLE` | `UNAVAILABLE` | The HITL queue or its owning shard is unreachable. | Retry with backoff. The shard may be temporarily down. Items on unavailable shards become visible when the shard recovers. |

---

## Configuration and Validation Errors

Emitted at CRD admission time by the Operator, or at request time when configuration state is inconsistent.

| Code | gRPC Status | Cause | Caller Response |
|------|-------------|-------|-----------------|
| `INVALID_CAPABILITY` | `INVALID_ARGUMENT` | A capability string in the FoundryNode CRD uses an invalid verb, is missing a required qualifier, or has unknown syntax. | Fix the capability string. The Operator does not reconcile a FoundryNode with syntactically invalid capabilities. See [capability syntax](./crds.md#capability-syntax). |
| `UNKNOWN_CONTRACT` | `INVALID_ARGUMENT` | A node's entry or exit binding references a contract name not defined in the FoundryFlow's `entryContracts` or `exitContracts`. | Fix the binding to reference an existing contract, or add the contract to the FoundryFlow CRD. |
| `IMPORT_TYPE_NODE_INVALID` | `INVALID_ARGUMENT` | A `crossFlow.importTypes` entry references a node that does not exist, or the referenced node is not bound to an entry contract. | Fix the import type's `node` reference or add an entry binding to the target node. Cross-flow import for that import type is rejected until this is resolved. |
| `SCHEMA_VALIDATION_FAILED` | `INVALID_ARGUMENT` | CRD admission validation failed — missing required fields, invalid field types, constraint violations, or structural inconsistencies. | Fix the CRD content. The Operator provides a descriptive message identifying the specific validation failure. |

---

## Cross-Flow and Trust Errors

Emitted during cross-flow exchange, trust verification, and law integration.

| Code | gRPC Status | Cause | Caller Response |
|------|-------------|-------|-----------------|
| `TRUST_CHAIN_INVALID` | `PERMISSION_DENIED` | The certificate chain on an imported stamp or package does not validate against the expected trust root (Federation CA for federation-member exchange, Treaty CA for Treaty exchange). | Reject the import. Investigate certificate configuration. This may indicate an expired certificate, a misconfigured trust root, or a security incident. |
| `TREATY_NOT_FOUND` | `NOT_FOUND` | A cross-flow operation was attempted between Flows with no Treaty configured for the required non-federation direction. | Configure a Treaty CRD for the required direction before attempting the exchange. |
| `NATURALISATION_REQUIRED` | `FAILED_PRECONDITION` | Imported stamps from a treaty crossing do not carry local governance authority. The stamps are preserved for provenance but do not satisfy local stamp requirements. | Process the Workitem through the local governance loop. The normal routing cycle drives the Workitem to nodes configured to provide local stamps. |
| `IMPORT_ADMISSION_FAILED` | `FAILED_PRECONDITION` | The imported Workitem does not satisfy the import type node's bound entry contract. | The receiving Flow rejected the import. Review the import type node's entry contract requirements and the exported artefact state. |

---

## Embassy Transfer Errors

Emitted by the [Embassy](./grpc-api.md#embassy-api) during cross-flow Workitem transfer (manifest preflight and package streaming).

| Code | gRPC Status | Cause | Caller Response |
|------|-------------|-------|-----------------|
| `UNKNOWN_IMPORT_TYPE` | `FAILED_PRECONDITION` | The import type specified in the manifest does not exist in the receiving Flow's effective import-type registry (built-in system plus flow-authored types). | Do not retry. The receiving Flow does not accept this import type. Verify the import type name and the receiving Flow's configuration. |
| `HEADER_REJECTED` | `FAILED_PRECONDITION` | The manifest preflight was rejected — the treaty does not permit the import type, the bundle size exceeds limits, or the source identity failed validation. | Do not retry without fixing the rejection cause. Check treaty `allowedImportTypes`, `maxBundleSize`, and identity material. |
| `FOREIGN_STAMP_INVALID` | `PERMISSION_DENIED` | Foreign attestation stamps on the imported package failed chain verification against the expected trust root. | Reject the import. The stamps may be from an untrusted source. Investigate the certificate chain. |
| `PACKAGE_DIGEST_MISMATCH` | `DATA_LOSS` | The SHA-256 digest of the streamed package content does not match the trailer digest. | The package was corrupted in transit. Retry the full transfer. |
| `TREATY_IMPORT_TYPE_DENIED` | `PERMISSION_DENIED` | The Treaty's `allowedImportTypes` does not include the requested import type. | Do not retry. The treaty must be updated to permit this import type. |
| `NATURALISATION_FAILURE` | `FAILED_PRECONDITION` | The Embassy failed to emit the required local `imported-*` attestations after verifying foreign stamps, or the naturalisation process encountered an irrecoverable error. | Do not retry without investigating the import-type stamp requirements, trust material, and the state of the imported artefacts. |

---

## Federation Publication Errors

Emitted by the [Federation service](./grpc-api.md#federation-api) during law publication, conflict resolution, and distribution.

| Code | gRPC Status | Cause | Caller Response |
|------|-------------|-------|-----------------|
| `UNAUTHORISED_PUBLISH` | `PERMISSION_DENIED` | The publisher is not an authorised authority publisher for the target state scope. | Do not retry. The publisher's identity does not have publication rights for this scope. |
| `CONFLICTING_PUBLISHED_LAW` | `FAILED_PRECONDITION` | The submitted law conflicts with an existing published law in the same Federation state scope. | Resolve the conflict by revising the submission or withdrawing the conflicting publication if you control it. The publication is rejected during admission; there is no separate conflict-reject RPC. |
| `UNKNOWN_STATE_SCOPE` | `NOT_FOUND` | The state scope identifier is not recognised by the Federation. | Verify the state scope name. The Federation may need to be configured with this scope. |
| `PUBLICATION_REJECTED` | `FAILED_PRECONDITION` | The publication was rejected by Federation conflict review or policy validation. | Do not retry without addressing the rejection reason. Review the rejection details in the response message. |

---

## Data Errors

Emitted when referenced data is missing or invalid.

| Code | gRPC Status | Cause | Caller Response |
|------|-------------|-------|-----------------|
| `FEEDBACK_NOT_FOUND` | `NOT_FOUND` | The specified feedback ID does not exist on the artefact. | Verify the feedback ID. The item may have been addressed by another assignment or may reference a different artefact. |
| `LAW_NOT_FOUND` | `NOT_FOUND` | A cited law has been retired or does not exist in the Library. | The law is gone. Do not retry. If the citation was used for justification, the handler may need to find alternative governance support or propose a novel argument. |
| `UNKNOWN_GOVERNED_ARTEFACT` | `FAILED_PRECONDITION` | The `governed_artefact` name in a `StoreArtefact` call does not match any GovernedArtefact CRD registered in the Flow. | Register a GovernedArtefact CRD with the required `metadata.name` before storing artefacts of this type. |
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
| Child Workitem guard (`CHILDREN_NOT_TERMINAL`, `CHILD_NOT_OWNED`, `CHILD_ALREADY_ROUTED`) | No | Wait for children to complete, verify ownership, or accept routed state. |
| NodeGroup guard (`GROUP_ENTRY_VIOLATION`, `GROUP_ROUTING_DENIED`) | No | Fix routing to use group entry nodes, or satisfy group entry contract. |
| Thrash and timeout (`THRASH_BUDGET_EXCEEDED`, `TIMEOUT_EXCEEDED`) | No | Terminal or Operator-managed. Investigate root cause. |
| Capability (`CAPABILITY_DENIED`) | No | Review node capability grants. |
| Governance finality (`CONTEMPT_VIOLATION`) | No | Comply with the ruling. |
| Write-once (`STAMP_ALREADY_APPLIED`) | No | Stamp exists. Proceed. |
| State machine (`INVALID_STATE_TRANSITION`) | No | Check current state, use correct operation. |
| Data integrity (`ARTEFACT_CORRUPTED`) | No | Report and investigate. |
| Identity conflict (`ARTEFACT_KIND_CONFLICT`) | No | Use correct `id`/`governed_artefact` pairing. |
| Jury deliberation (`DELIBERATION_HUNG`, `JUROR_INFERENCE_FAILED`) | No | Handled internally by the Judiciary. Hung verdict routes to the HITL node. |
| law-applicator (`LAW_WRITE_FAILED`) | Possibly | law-applicator may retry internally. Persistent failure blocks petition application. |
| Queue (`QUEUE_ITEM_NOT_FOUND`, `QUEUE_ITEM_ALREADY_CLAIMED`, `QUEUE_ITEM_INVALID_STATE`) | No | Verify item state. |
| Queue availability (`QUEUE_UNAVAILABLE`) | Yes | Retry with backoff. Shard may recover. |
| Configuration (`INVALID_CAPABILITY`, `UNKNOWN_CONTRACT`, `IMPORT_TYPE_NODE_INVALID`, `SCHEMA_VALIDATION_FAILED`) | No | Fix CRD configuration. |
| Cross-flow trust (`TRUST_CHAIN_INVALID`, `TREATY_NOT_FOUND`, `NATURALISATION_REQUIRED`, `IMPORT_ADMISSION_FAILED`) | No | Fix trust configuration or process through local governance. |
| Embassy transfer (`UNKNOWN_IMPORT_TYPE`, `HEADER_REJECTED`, `FOREIGN_STAMP_INVALID`, `TREATY_IMPORT_TYPE_DENIED`, `NATURALISATION_FAILURE`) | No | Fix import type configuration, treaty permissions, or identity material. |
| Embassy integrity (`PACKAGE_DIGEST_MISMATCH`) | Yes | Retry the full transfer. |
| Federation publication (`UNAUTHORISED_PUBLISH`, `CONFLICTING_PUBLISHED_LAW`, `UNKNOWN_STATE_SCOPE`, `PUBLICATION_REJECTED`) | No | Fix publisher authorisation, resolve conflicts, or verify state scope. |
| Missing data (`FEEDBACK_NOT_FOUND`, `LAW_NOT_FOUND`) | No | Resource is absent. Adapt logic. |
| Unregistered artefact type (`UNKNOWN_GOVERNED_ARTEFACT`) | No | Register a GovernedArtefact CRD. Configuration issue. |
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
8. gRPC status codes follow a consistent mapping: `PERMISSION_DENIED` for capability failures, `FAILED_PRECONDITION` for guard violations, `NOT_FOUND` for missing resources, `ALREADY_EXISTS` for write-once violations, `UNAVAILABLE` for transient failures, `INVALID_ARGUMENT` for malformed input, `DATA_LOSS` for integrity failures, `DEADLINE_EXCEEDED` for timeout failures, `UNAUTHENTICATED` for identity failures.
