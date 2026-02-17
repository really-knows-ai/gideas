# Foundry Flow: Error Code Catalog

**Status:** v1 Specification

This document provides the canonical catalog of all error codes in the Foundry Flow system, their sources, and recovery strategies. It is the single source of truth for error handling.

## Error Code Structure

Errors are returned via gRPC status codes with additional detail in a custom `Foundry-Reason` header and the `message` field.

**Format:** `{GRPC_CODE} / {FOUNDRY_REASON}`

## 1. Routing & Execution Errors (Operator)

| Foundry Reason | gRPC Code | Source | Description | Recovery |
|---|---|---|---|---|
| `INVALID_OUTPUT` | `INVALID_ARGUMENT` | Router Guard | Output name not in Node's `outputs[]` | Fix FoundryNode spec or handler return value |
| `NODE_NOT_FOUND` | `NOT_FOUND` | Router Guard | Target node does not exist | Check FoundryNode CRD deployment |
| `NO_AVAILABLE_TARGET` | `FAILED_PRECONDITION` | Router Guard | Target role has no ready nodes | Scale up target nodes; check readiness probes |
| `ROUTE_LOOP_DETECTED` | `ABORTED` | Thrash Guard | Self-referential routing detected | Fix flow topology |
| `THRASH_DETECTED` | `ABORTED` | Thrash Guard | Node visit count exceeded `maxVisits` | Fix routing loop in flow topology |
| `EXECUTION_TIMEOUT` | `DEADLINE_EXCEEDED` | Reaper Loop | Workitem exceeded `executionDeadline` | Add `timeout` output for escalation; optimize handler |
| `HEARTBEAT_TIMEOUT` | `DEADLINE_EXCEEDED` | Sidecar | Node didn't heartbeat within timeout | Investigate node handler for hangs or long-running tasks |
| `HANDLER_PANIC` | `INTERNAL` | Sidecar | Node handler crashed | Fix handler code; check logs |

## 2. Terminal Validation Errors (Operator)

| Foundry Reason | gRPC Code | Source | Description | Recovery |
|---|---|---|---|---|
| `NOT_TERMINAL_NODE` | `FAILED_PRECONDITION` | Terminal Guard | `Complete()` called on non-terminal node | Set `isTerminal: true` in FoundryNode spec |
| `ARTEFACT_MISSING` | `FAILED_PRECONDITION` | Terminal Guard | Required artefact not present | Store missing artefact before completing |
| `VALIDITY_NOT_MET` | `FAILED_PRECONDITION` | Terminal Guard | Required stamps missing | Stamp artefacts with required roles |
| `SIGNATURE_INVALID` | `PERMISSION_DENIED` | Terminal Guard | Stamp signature verification failed | Re-stamp; check certificate chain |
| `CERTIFICATE_EXPIRED` | `UNAUTHENTICATED` | Terminal Guard | Signing certificate has expired | Trigger certificate renewal |
| `CERTIFICATE_CHAIN_INVALID` | `UNAUTHENTICATED` | Terminal Guard | Certificate chain doesn't reach State Root | Ensure node has correct operator-issued cert |

## 3. Artefact Errors (Sidecar/Archivist)

| Foundry Reason | gRPC Code | Source | Description | Recovery |
|---|---|---|---|---|
| `ARTEFACT_NOT_FOUND` | `NOT_FOUND` | Sidecar | Artefact not in Workitem's `status.artefacts[]` | Store artefact first |
| `VERSION_NOT_FOUND` | `NOT_FOUND` | Sidecar | Specified version of artefact doesn't exist | Check `GetArtefactMetadata().versions` |
| `ARTEFACT_TOO_LARGE` | `INVALID_ARGUMENT` | Archivist | Content exceeds size limit | Reduce content size |
| `ARTEFACT_CORRUPTED` | `DATA_LOSS` | Archivist | Content hash doesn't match stored hash | Re-fetch; investigate storage corruption |
| `STORAGE_FULL` | `RESOURCE_EXHAUSTED` | Archivist | PVC at capacity | Alert ops; expand storage |

## 4. Legal/Library Errors (Librarian)

| Foundry Reason | gRPC Code | Source | Description | Recovery |
|---|---|---|---|---|
| `LAW_NOT_FOUND` | `NOT_FOUND` | Librarian | Law ID does not exist | Use `RecordFinding` to create; check spelling |
| `LAW_EXPIRED` | `FAILED_PRECONDITION` | Librarian | Law has expired | Create new Finding |
| `DUPLICATE_FINDING` | `ALREADY_EXISTS` | Librarian | Semantically identical law already exists | Use existing law ID instead |
| `STATEMENT_TOO_LONG` | `INVALID_ARGUMENT` | Librarian | Statement exceeds 1024 characters | Shorten statement |
| `CONFLICT_DETECTED` | `ABORTED` | Librarian | New law conflicts with higher-tier law | Revise statement or cite conflicting law |
| `QUERY_EMPTY` | `INVALID_ARGUMENT` | Librarian | Search query has no parameters | Provide at least one query parameter |

## 5. Feedback Errors (Sidecar)

| Foundry Reason | gRPC Code | Source | Description | Recovery |
|---|---|---|---|---|
| `FEEDBACK_NOT_FOUND` | `NOT_FOUND` | Sidecar | Feedback ID not in Workitem | Check `status.feedback[]` |
| `MESSAGE_TOO_LONG` | `INVALID_ARGUMENT` | Sidecar | Feedback message exceeds 1024 chars | Use Store & Link pattern |
| `INVALID_STATE` | `FAILED_PRECONDITION` | Sidecar | Feedback not in required state for operation | Check feedback state machine |
| `CONTEMPT_VIOLATION` | `PERMISSION_DENIED` | Contempt Guard | Attempt to `wont-fix` item with `linkedRuling` | Comply with judicial mandate; fix the issue |
| `JUSTIFICATION_REQUIRED` | `INVALID_ARGUMENT` | Sidecar | `wont-fix` without citation or novel_argument | Provide justification |

## 6. Identity & Permission Errors

| Foundry Reason | gRPC Code | Source | Description | Recovery |
|---|---|---|---|---|
| `CAPABILITY_DENIED` | `PERMISSION_DENIED` | Sidecar | Node lacks required capability | Check FoundryNode `capabilities[]` |
| `AMBIGUOUS_ROLE` | `INVALID_ARGUMENT` | Sidecar | Node has multiple roles but none specified | Specify `role` parameter |
| `INVALID_ROLE` | `INVALID_ARGUMENT` | Sidecar | Requested role not in Node's roles | Use a role from FoundryNode.spec.roles |
| `INVALID_STAMP_TYPE` | `INVALID_ARGUMENT` | Sidecar | Stamp type is not `inspection` or `approval` | Use valid stamp type |
| `LAWS_REQUIRED` | `INVALID_ARGUMENT` | Sidecar | `approval` stamp without law citations | Provide law citations for approval stamps |
| `CSR_VALIDATION_FAILED` | `INVALID_ARGUMENT` | Governor | CSR doesn't meet crypto requirements | Fix CSR; check key size and subject |
| `NAMESPACE_MISMATCH` | `PERMISSION_DENIED` | Governor | Operator not in claimed namespace | Deploy to correct namespace |
| `ATTESTATION_INVALID` | `UNAUTHENTICATED` | Governor | ServiceAccount token invalid | Check RBAC; verify SA exists |

## 7. Cross-Flow Federation Errors

| Foundry Reason | gRPC Code | Source | Description | Recovery |
|---|---|---|---|---|
| `EXPORT_NOT_TERMINAL` | `FAILED_PRECONDITION` | Export Guard | Node lacks `isTerminal: true` | Fix FoundryNode spec |
| `EXPORT_NO_CAPABILITY` | `PERMISSION_DENIED` | Export Guard | Node lacks `EXPORT` capability | Add capability to FoundryNode spec |
| `EXPORT_TARGET_UNREACHABLE` | `UNAVAILABLE` | Export Queue | Target endpoint is down | Retry; check network connectivity |
| `EXPORT_REJECTED` | `PERMISSION_DENIED` | Export Queue | Target rejected the bundle | Check target logs for reason |
| `BUNDLE_CORRUPT` | `INVALID_ARGUMENT` | Import Guard | Bundle cannot be unpacked | Sender: regenerate bundle |
| `BUNDLE_TOO_LARGE` | `RESOURCE_EXHAUSTED` | Import Guard | Exceeds Treaty size limit | Increase limit in Treaty or reduce bundle size |
| `TREATY_VIOLATION` | `PERMISSION_DENIED` | Import Guard | Signer CA not trusted | Admin: add Treaty for sender |
| `UNAUTHORIZED_SUBJECT` | `PERMISSION_DENIED` | Import Guard | Signer CN not in `allowedSubjects` | Admin: update Treaty |
| `ALREADY_IMPORTED` | `ALREADY_EXISTS` | Import Guard | Bundle was previously imported | None (idempotent rejection) |
| `WORKITEM_TYPE_NOT_ALLOWED` | `PERMISSION_DENIED` | Import Guard | Type restricted by Treaty | Admin: update Treaty |

## 8. System & Configuration Errors

| Foundry Reason | gRPC Code | Source | Description | Recovery |
|---|---|---|---|---|
| `SERVICE_UNAVAILABLE` | `UNAVAILABLE` | Various | Service temporarily unavailable | Retry with exponential backoff |
| `RATE_LIMITED` | `RESOURCE_EXHAUSTED` | gRPC Server | Rate limit exceeded | Retry with backoff |
| `STATE_CORRUPTION` | `DATA_LOSS` | Various | Merkle root or sequence mismatch | Service will restart; investigate storage |
| `CONTEXT_CANCELLED` | `DEADLINE_EXCEEDED` | Sidecar | Handler context cancelled | Exit handler gracefully |
| `INVALID_KIND` | `INVALID_ARGUMENT` | Sidecar | Artefact kind not registered as GovernedArtefact | Create GovernedArtefact CRD |
| `INVALID_TYPE` | `INVALID_ARGUMENT` | Sidecar | WorkitemType doesn't exist | Create WorkitemType CRD |
| `INVALID_PRIORITY` | `INVALID_ARGUMENT` | Sidecar | Unknown priority value | Use valid priority |
| `INVALID_SEVERITY` | `INVALID_ARGUMENT` | Sidecar | Unknown severity value | Use valid severity |
| `INVALID_EVENT_TYPE` | `INVALID_ARGUMENT` | Flow Monitor | Event type doesn't start with `foundry.` | Use correct prefix |
| `PAYLOAD_TOO_LARGE` | `INVALID_ARGUMENT` | Flow Monitor | Payload exceeds 64KB | Reduce payload size |
| `INVALID_VALUE` | `INVALID_ARGUMENT` | Flow Monitor | Metric value is negative or NaN | Use positive value |
| `INVALID_TIMEOUT_BOUNDS` | `INVALID_ARGUMENT` | Helm Validation | `standardNodeTimeout > maxNodeTimeout` | Fix values.yaml |
| `INVALID_REPLICA_COUNT` | `INVALID_ARGUMENT` | Helm Validation | `lawSearch.replicas < 1` | Set replicas to at least 1 |
| `STORAGE_CLASS_NOT_FOUND` | `NOT_FOUND` | Operator | Specified storageClass doesn't exist | Create StorageClass or use existing one |
| `EMBEDDING_DIMENSION_MISMATCH` | `FAILED_PRECONDITION` | Librarian | Configured dimensions don't match model output | Align `embedding.dimensions` with model |

## gRPC Code Reference

| Code | Name | Typical Use |
|---|---|---|
| 0 | `OK` | Success |
| 1 | `CANCELLED` | Client cancelled request |
| 3 | `INVALID_ARGUMENT` | Bad input from caller |
| 4 | `DEADLINE_EXCEEDED` | Timeout |
| 5 | `NOT_FOUND` | Resource doesn't exist |
| 6 | `ALREADY_EXISTS` | Duplicate creation |
| 7 | `PERMISSION_DENIED` | Caller lacks permission |
| 8 | `RESOURCE_EXHAUSTED` | Rate limited or quota exceeded |
| 9 | `FAILED_PRECONDITION` | System not in required state |
| 10 | `ABORTED` | Conflict or loop detected |
| 13 | `INTERNAL` | Unexpected server error |
| 14 | `UNAVAILABLE` | Service temporarily unavailable |
| 15 | `DATA_LOSS` | Corruption detected |
| 16 | `UNAUTHENTICATED` | Invalid credentials |

## Recovery Strategies

### Retryable Errors
- `UNAVAILABLE`, `DEADLINE_EXCEEDED`, `RESOURCE_EXHAUSTED` (transient)
- Use exponential backoff: 100ms → 200ms → 400ms → ... (max 30s)

### Non-Retryable Errors
- `INVALID_ARGUMENT`, `NOT_FOUND`, `PERMISSION_DENIED`
- Fix the underlying issue before retrying

### Escalation Errors
- `FAILED_PRECONDITION`, `ABORTED`
- Route to `timeout` or `error` output for handling
