# Cross-Flow Collaboration: Operations

## 1. Operator Responsibilities

### 1.1 Treaty Validation

The Flow Operator loads Treaties from the `FoundryFlow` spec and provides them to Sidecars:

1. **On FoundryFlow reconcile:** Parse `spec.treaties[]` and build CA trust store
2. **On Sidecar boot:** Inject Treaty CA bundle into Sidecar configuration
3. **On Import request:** Sidecar validates signature against injected CA bundle

### 1.2 Export Capability Validation

The Operator enforces the `EXPORT` + `isTerminal` coupling:

```go
// Admission webhook validation
func validateFoundryNode(node *FoundryNode) error {
    hasExport := contains(node.Spec.Capabilities, "EXPORT")
    isTerminal := len(node.Spec.Outputs) == 0 || node.Spec.IsTerminal
    
    if hasExport && !isTerminal {
        return fmt.Errorf("EXPORT capability requires isTerminal: true")
    }
    return nil
}
```

### 1.3 Duplicate Import Detection

The Operator maintains an **Import Ledger** (SQLite) to enforce strict deduplication. See Import documentation for the full schema.

On Import, the Sidecar queries this ledger via the Operator. If a matching `sourceFlow + sourceWorkitemId` entry exists, the request is immediately rejected with `ALREADY_IMPORTED`.

---

## 2. Error Catalog

| Error Code | gRPC Status | Cause | Resolution |
|------------|-------------|-------|------------|
| `BUNDLE_CORRUPT` | `INVALID_ARGUMENT` | Bundle cannot be unpacked | Sender: regenerate bundle |
| `HASH_MISMATCH` | `INVALID_ARGUMENT` | Artefact hash doesn't match manifest | Sender: regenerate bundle |
| `TREATY_VIOLATION` | `PERMISSION_DENIED` | Signature CA not in Treaties | Admin: add Treaty for sender |
| `UNAUTHORIZED_SUBJECT` | `PERMISSION_DENIED` | Signer CN not in `allowedSubjects` | Admin: update Treaty |
| `ALREADY_IMPORTED` | `ALREADY_EXISTS` | Bundle already imported | None (idempotent rejection) |
| `EXPORT_FAILED` | `UNAVAILABLE` | Target Ingress unreachable | Retry or check endpoint |
| `EXPORT_NOT_TERMINAL` | `FAILED_PRECONDITION` | Node has EXPORT but not terminal | Fix FoundryNode spec |

---

## 3. Telemetry Events

| Event | Emitter | Payload | Description |
|-------|---------|---------|-------------|
| `foundry.export.started` | Sidecar | `{ workitemId, targetFlow, targetEndpoint }` | Export initiated |
| `foundry.export.completed` | Sidecar | `{ workitemId, targetFlow, bundleSize, duration_ms }` | Export successful |
| `foundry.export.failed` | Sidecar | `{ workitemId, targetFlow, error }` | Export failed |
| `foundry.import.started` | Sidecar | `{ sourceFlow, sourceWorkitemId, bundleSize }` | Import initiated |
| `foundry.import.completed` | Sidecar | `{ sourceFlow, sourceWorkitemId, localWorkitemId, duration_ms }` | Import successful |
| `foundry.import.rejected` | Sidecar | `{ sourceFlow, sourceWorkitemId, reason }` | Import rejected |

---

## 4. Security Considerations

### 4.1 Bundle Encryption

**Bundles are signed, not encrypted.**

| Layer | Mechanism | Responsibility |
|-------|-----------|----------------|
| **Integrity** | RSA signature over manifest | Export Node |
| **Transport** | TLS 1.3 (HTTPS/gRPC) | Network |
| **At Rest** | PVC encryption, etcd encryption | Infrastructure |

**Rationale:** Encryption at the bundle level would require key exchange between Flows, adding complexity. Since bundles travel over TLS and are stored on encrypted infrastructure, application-layer encryption provides minimal additional security for significant implementation cost.

### 4.2 Treaty Rotation

When rotating a foreign Operator's CA certificate:

1. Add the **new** CA cert to the Treaty (temporarily allowing both)
2. Wait for the foreign Operator to rotate their node certificates
3. Remove the **old** CA cert from the Treaty

### 4.3 Subject Restrictions

Always specify `allowedSubjects` to limit which nodes can sign bundles:

```yaml
# Good: specific nodes
allowedSubjects: ["export-node", "terminal-exporter"]

# Bad: allows any node from the foreign Flow
allowedSubjects: ["*"]
```

### 4.4 Workitem Type Restrictions

Use `allowedWorkitemTypes` to prevent unexpected workitem types:

```yaml
treaties:
  - name: "vendor-flow"
    caCert: "..."
    allowedSubjects: ["export-node"]
    allowedWorkitemTypes:
      - "vendor-submission-v1"
      # Rejects: "admin-override-v1", "internal-audit-v1"
```

### 4.5 Bundle Size Limits

Configure maximum bundle size to prevent resource exhaustion:

```yaml
# Helm values.yaml
security:
  treaties:
    - name: "vendor-flow"
      caCert: "..."
      maxBundleSize: "5Gi"  # Reject bundles larger than 5GB
```
