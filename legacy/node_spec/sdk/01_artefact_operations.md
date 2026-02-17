# SDK Reference: Artefact Operations

> **Version:** 1.0.0  
> **Status:** Implementation Contract

This document defines the artefact operations available in the Foundry Node SDK.

---

## 1. StoreArtefact

Stores content in the Archivist and registers it in the Workitem's artefact registry.

### Signature

```go
func (n *NodeContext) StoreArtefact(
    ctx context.Context,
    kind string,      // GovernedArtefact kind (e.g., "petition-draft")
    name string,      // Filename (e.g., "petition_draft.md")
    content []byte,   // Raw content bytes
) (*ArtefactVersion, error)
```

### Parameters

| Name | Type | Required | Constraints | Description |
|------|------|----------|-------------|-------------|
| `ctx` | `context.Context` | Yes | — | Request context; inherits handler timeout |
| `kind` | `string` | Yes | Must match `GovernedArtefact.metadata.name` | Artefact type classification |
| `name` | `string` | Yes | Max 255 chars; valid filename | Human-readable identifier |
| `content` | `[]byte` | Yes | Max 100MB (configurable) | Raw artefact content |

### Returns

```go
type ArtefactVersion struct {
    Kind    string    // Echo of input kind
    Name    string    // Echo of input name
    Version string    // SHA256 hash of content (e.g., "sha256:abc123...")
}
```

### Errors

| gRPC Code | Foundry Reason | Condition | Recovery |
|-----------|----------------|-----------|----------|
| `PERMISSION_DENIED` | `CAPABILITY_DENIED` | Node lacks `WRITE:artefact/{kind}` | Add capability to FoundryNode spec |
| `INVALID_ARGUMENT` | `ARTEFACT_TOO_LARGE` | Content exceeds size limit | Reduce content; use chunked storage |
| `INVALID_ARGUMENT` | `INVALID_KIND` | Kind not registered as GovernedArtefact | Create GovernedArtefact CRD |
| `UNAVAILABLE` | `SERVICE_UNAVAILABLE` | Archivist unreachable | Retry with exponential backoff |
| `RESOURCE_EXHAUSTED` | `STORAGE_FULL` | PVC at capacity | Alert ops; expand storage |

### Behavior

1. **Content Hash:** Sidecar computes `SHA256(content)` to generate version identifier.
2. **Deduplication:** If identical content already exists, returns existing version (no duplicate storage).
3. **CRD Update:** Sidecar patches `Workitem.status.artefacts[]` with new version entry.
4. **Passport Clear:** Storing a new version clears any existing passport stamps (provenance reset).

### Example

```go
draft := []byte("# Petition\n\nAdd dark mode to dashboard...")

version, err := node.StoreArtefact(ctx, "petition-draft", "petition_draft.md", draft)
if err != nil {
    if foundry.IsError(err, foundry.CAPABILITY_DENIED) {
        log.Error("Node not authorized to write petition-draft artefacts")
        return foundry.RouteToOutput("error")
    }
    return foundry.RouteToOutput("error")
}

log.Info("Stored artefact", "version", version.Version)
```

---

## 2. FetchArtefact

Retrieves the latest version of an artefact from the Archivist.

### Signature

```go
func (n *NodeContext) FetchArtefact(
    ctx context.Context,
    kind string,      // GovernedArtefact kind
) ([]byte, error)
```

### Parameters

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `ctx` | `context.Context` | Yes | Request context |
| `kind` | `string` | Yes | Artefact type to fetch |

### Returns

| Type | Description |
|------|-------------|
| `[]byte` | Raw artefact content (latest version) |

### Errors

| gRPC Code | Foundry Reason | Condition | Recovery |
|-----------|----------------|-----------|----------|
| `NOT_FOUND` | `ARTEFACT_NOT_FOUND` | Kind not in `Workitem.status.artefacts[]` | Store artefact first |
| `PERMISSION_DENIED` | `CAPABILITY_DENIED` | Node lacks `READ:artefact/{kind}` | Add capability |
| `DATA_LOSS` | `ARTEFACT_CORRUPTED` | Hash mismatch on retrieved content | Alert ops; re-store artefact |
| `UNAVAILABLE` | `SERVICE_UNAVAILABLE` | Archivist unreachable | Retry |

### Behavior

1. **Latest Version:** Always returns the most recent version by `createdAt` timestamp.
2. **Hash Verification:** Sidecar verifies `SHA256(content) == stored_hash` after fetch.
3. **Read-Through:** Sidecar caches content for the duration of the handler (not across workitems).

### Example

```go
content, err := node.FetchArtefact(ctx, "petition-draft")
if err != nil {
    if foundry.IsError(err, foundry.ARTEFACT_NOT_FOUND) {
        // Expected for new workitems - create initial draft
        return createInitialDraft(ctx, node, workitem)
    }
    return foundry.RouteToOutput("error")
}

draft := string(content)
```

---

## 3. FetchArtefactByName

Retrieves an artefact by kind and name.

### Signature

```go
func (n *NodeContext) FetchArtefactByName(
    ctx context.Context,
    kind string,
    name string,
) ([]byte, error)
```

### Parameters

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `ctx` | `context.Context` | Yes | Request context |
| `kind` | `string` | Yes | Artefact type |
| `name` | `string` | Yes | Artefact filename |

### Errors

Same as `FetchArtefact`.

---

## 4. FetchArtefactVersion

Retrieves a specific version of an artefact.

### Signature

```go
func (n *NodeContext) FetchArtefactVersion(
    ctx context.Context,
    kind string,
    name string,
    version string,   // SHA256 hash (e.g., "sha256:abc123...")
) ([]byte, error)
```

### Parameters

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `ctx` | `context.Context` | Yes | Request context |
| `kind` | `string` | Yes | Artefact type |
| `name` | `string` | Yes | Artefact filename |
| `version` | `string` | Yes | Exact version hash |

### Errors

Same as `FetchArtefact`, plus:

| gRPC Code | Foundry Reason | Condition | Recovery |
|-----------|----------------|-----------|----------|
| `NOT_FOUND` | `VERSION_NOT_FOUND` | Specified version doesn't exist | Check `GetArtefactMetadata().versions` |

---

## 5. GetArtefactMetadata

Retrieves metadata about an artefact without fetching content. The passport (stamps) is fetched from the Archivist via `GetPassport()`.

### Signature

```go
func (n *NodeContext) GetArtefactMetadata(
    ctx context.Context,
    kind string,
) (*ArtefactMetadata, error)
```

### Returns

```go
type ArtefactMetadata struct {
    Kind          string
    Name          string
    LatestVersion string
    Versions      []VersionInfo
    Passport      []Stamp      // Stamps on latest version only
}

type VersionInfo struct {
    Hash          string
    CreatedAt     time.Time
    CreatedByNode string
}

type Stamp struct {
    Role             string
    Type             string    // "inspection" or "approval"
    Node             string
    Timestamp        time.Time
    Hash             string    // Content hash at stamp time
    Signature        string    // Base64-encoded RSA signature
    CertificateChain []string  // PEM-encoded certificates
    Laws             []LawCitation // Present for "approval" type
}

type LawCitation struct {
    ID      string
    Version string
}
```

### Example

```go
meta, err := node.GetArtefactMetadata(ctx, "petition-draft")
if err != nil {
    return foundry.RouteToOutput("error")
}

// Check if artefact has required stamps
hasSecurityStamp := false
for _, stamp := range meta.Passport {
    if stamp.Role == "security-reviewer" {
        hasSecurityStamp = true
        break
    }
}
```

---

### 6. StampArtefact
Signs an artefact's latest version with the node's cryptographic identity for a specific role.

### Signature

```go
func (n *NodeContext) StampArtefact(
    ctx context.Context,
    kind string,
    stampType string,  // "inspection" or "approval"
    role string,       // Required if node has multiple roles; optional if single role
    laws []LawCitation, // Required for "approval" type; ignored for "inspection"
) error
```

### Parameters

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `ctx` | `context.Context` | Yes | Request context |
| `kind` | `string` | Yes | Artefact type to stamp |
| `stampType` | `string` | Yes | `"inspection"` (review marker) or `"approval"` (governance certification) |
| `role` | `string` | Conditional | Role to stamp as (see Ambiguity Guard) |
| `laws` | `[]LawCitation` | Conditional | Law citations (required for `approval` type) |

### Ambiguity Guard

The `role` parameter enforces explicit authority assertion:

| Node Roles | `role` Provided | Behavior |
|------------|-----------------|----------|
| Single role | No | Defaults to that role (convenience) |
| Single role | Yes | Validates role matches; stamps |
| Multiple roles | No | Returns `AMBIGUOUS_ROLE` error |
| Multiple roles | Yes | Validates role is in node's roles; stamps |

> **Design Principle:** Nodes must explicitly declare which "hat" they are wearing. No shotgun stamping.

### Stamp Types

| Type | Purpose | Laws Required |
|------|---------|---------------|
| `inspection` | Records that a node has reviewed the current artefact version | No |
| `approval` | Certifies the artefact meets governance requirements | Yes |

Both stamp types are invalidated when the artefact content changes (hash mismatch).

### Behavior

1. **Type Validation:** Sidecar verifies `stampType` is `"inspection"` or `"approval"`.
2. **Law Requirement:** If `stampType` is `"approval"`, `laws` must be non-empty; otherwise returns `INVALID_ARGUMENT`.
3. **Role Validation:** Sidecar verifies requested `role` is present in `FoundryNode.spec.roles[]`.
4. **Version Lock:** Stamps the **latest** version at time of call.
5. **Hash Capture:** Sidecar records the artefact's content hash at stamp time.
6. **Signature:** Sidecar signs `SHA256(artefact_hash || stamp_type || role || timestamp)` with its private key.
7. **Certificate Chain:** Includes full chain: `[node_cert, operator_cert, ...state_root]`.
8. **Archivist Storage:** Sidecar calls `Archivist.AddStamp()` to persist the stamp alongside the artefact content.
9. **Upsert (Deduplication):** If a stamp for this role already exists, it is **overwritten** (Last-Write-Wins).

### Capability Requirements

| Stamp Type | Required Capability |
|------------|--------------------|
| `inspection` | `INSPECT:artefact/{kind}` |
| `approval` | `APPROVE:artefact/{kind}` |

The Sidecar validates the node has the appropriate capability for the requested stamp type before proceeding.

### Errors

| gRPC Code | Foundry Reason | Condition | Recovery |
|-----------|----------------|-----------|----------|
| `NOT_FOUND` | `ARTEFACT_NOT_FOUND` | Artefact not in workitem | Store artefact first |
| `INVALID_ARGUMENT` | `INVALID_STAMP_TYPE` | `stampType` is not `"inspection"` or `"approval"` | Use valid stamp type |
| `INVALID_ARGUMENT` | `LAWS_REQUIRED` | `stampType` is `"approval"` but `laws` is empty | Provide law citations |
| `INVALID_ARGUMENT` | `AMBIGUOUS_ROLE` | Node has multiple roles but none specified | Specify `role` parameter |
| `INVALID_ARGUMENT` | `INVALID_ROLE` | Requested role not in Node's roles | Use a role from FoundryNode.spec.roles |
| `PERMISSION_DENIED` | `CAPABILITY_DENIED` | Node lacks `INSPECT:artefact/{kind}` or `APPROVE:artefact/{kind}` | Add capability to FoundryNode spec |

### Example

```go
// Inspection stamp - lightweight review marker (no laws required)
err := node.StampArtefact(ctx, "petition-draft", "inspection", "linter", nil)
if err != nil {
    return foundry.RouteToOutput("error")
}

// Approval stamp - governance certification (laws required)
laws := []foundry.LawCitation{
    {ID: "f-109", Version: "v1"},
    {ID: "f-203", Version: "v2"},
}
err = node.StampArtefact(ctx, "petition-draft", "approval", "validator", laws)
if err != nil {
    if foundry.IsError(err, foundry.LAWS_REQUIRED) {
        log.Error("Approval stamps require law citations")
    }
    return foundry.RouteToOutput("error")
}

return foundry.RouteToOutput("approved")
```

---

## 7. ValidateArtefact

Validates artefact state without triggering terminal completion.

### Signature

```go
func (n *NodeContext) ValidateArtefact(
    ctx context.Context,
    kind string,
    requiredState string,  // "present" | "valid"
) (*ValidationResult, error)
```

### Parameters

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `kind` | `string` | Yes | Artefact type to validate |
| `requiredState` | `string` | Yes | `"present"` (exists) or `"valid"` (exists + required stamps) |

### Returns

```go
type ValidationResult struct {
    Valid        bool
    MissingRoles []string  // If state="valid" but invalid
}
```

### Behavior

- `"present"`: Returns valid if artefact exists in `Workitem.status.artefacts[]`.
- `"valid"`: Returns valid if artefact exists AND has all stamps required by `GovernedArtefact.spec.validityConditions`.

### Example

```go
result, err := node.ValidateArtefact(ctx, "petition-draft", "valid")
if err != nil {
    return foundry.RouteToOutput("error")
}

if !result.Valid {
    log.Warn("Artefact missing required stamps", "missing", result.MissingRoles)
    return foundry.RouteToOutput("needs-review")
}
```
