# Cross-Flow Collaboration: Treaty Trust Model

## 1. What is a Treaty?

A **Treaty** is a receiver-side configuration that grants import permission to a foreign Flow. It answers the question: *"Do I allow this stranger to enter?"*

**Key Properties:**
- **Unilateral:** A Treaty is an import permit. The receiver grants trust; the sender requires only the endpoint URL.
- **Receiver-Side:** Only the receiving Flow configures Treaties. Senders just need the endpoint URL.
- **CA-Based:** Trust is established by pinning the foreign Operator's CA certificate.

## 2. Treaty vs Federation

| Mode | Trust Anchor | Configuration | Use Case |
|------|--------------|---------------|----------|
| **Treaty** | Pinned CA per peer | Explicit whitelist | Atomic Flows collaborating without shared Governor |
| **Federation** | State Root CA | Implicit (all siblings trusted) | Sibling Flows under same Governor |

**Federation is essentially a "Global Treaty"** where every Flow trusts the State Root CA, allowing automatic acceptance of requests from any sibling.

## 3. Treaty Configuration

Treaties are configured in the `FoundryFlow` spec or via Helm values (standardized on `flow.gideas.io/v1`):

```yaml
# FoundryFlow spec (v1)
spec:
  treaties:
    - name: "vendor-flow-alpha"
      caCert: |
        -----BEGIN CERTIFICATE-----
        MIIFazCCA1OgAwIBAgIUZ...
        -----END CERTIFICATE-----
      allowedSubjects:
        - "export-node"
        - "terminal-exporter"
      # Optional: restrict to specific workitem types
      allowedWorkitemTypes:
        - "vendor-submission-v1"
```

```yaml
# Helm values.yaml
security:
  treaties:
    - name: "vendor-flow-alpha"
      caCert: |
        -----BEGIN CERTIFICATE-----
        MIIFazCCA1OgAwIBAgIUZ...
        -----END CERTIFICATE-----
      allowedSubjects:
        - "export-node"
```

## 4. Treaty Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Unique identifier for this treaty (used in logging, metrics) |
| `caCert` | string | Yes | PEM-encoded CA certificate of the foreign Operator |
| `allowedSubjects` | []string | Yes | CN/SAN patterns allowed to sign bundles (e.g., node names) |
| `allowedWorkitemTypes` | []string | No | Restrict imports to specific workitem types (empty = all) |

## 5. Bidirectional Collaboration

If Flow A wants to **send** to Flow B AND **receive** responses from Flow B, both sides must configure Treaties:

```yaml
# Flow A values.yaml - allows imports FROM Flow B
security:
  treaties:
    - name: "flow-b"
      caCert: "..." # Flow B's Operator CA

# Flow B values.yaml - allows imports FROM Flow A  
security:
  treaties:
    - name: "flow-a"
      caCert: "..." # Flow A's Operator CA
```

**Request/Response Pattern:**
1. **Request (A → B):** Flow B verifies against its Treaty with A
2. **Response (B → A):** Flow A verifies against its Treaty with B

Each direction is an independent Export/Import cycle.

## 6. Identity vs Transport

Treaties handle **Identity** (who can I trust?). Transport configuration (endpoint URLs) is separate.

| Concern | Mechanism |
|---------|-----------|
| **Identity** | Treaty CA verification (Is this signature from a trusted source?) |
| **Transport** | Standard HTTPS/gRPC (How do I reach the Ingress endpoint?) |

The Export Node needs:
- **Transport:** The Ingress endpoint URL (configured in node logic or environment)
- **Transport Only:** The sender requires only network connectivity to the receiver

The Ingress Node needs:
- **Treaty:** CA certificate of allowed senders
- **Exposes Endpoint:** Senders discover the endpoint via configuration or service mesh

## 7. Treaty Operations

### 7.1 Certificate Rotation (Make-Before-Break)

Certificate rotation follows a three-phase process to prevent downtime. The `caCert` field accepts a PEM bundle containing **multiple certificates**.

**Phase 1: Trust New (Add Both CAs)**
```yaml
# Flow B updates Treaty to trust both old AND new CA from Flow A
treaties:
  - name: "flow-a"
    caCert: |
      -----BEGIN CERTIFICATE-----
      ... OLD CA CERTIFICATE ...
      -----END CERTIFICATE-----
      -----BEGIN CERTIFICATE-----
      ... NEW CA CERTIFICATE ...
      -----END CERTIFICATE-----
```

**Phase 2: Rotate Source**
- Flow A rotates its Operator keys and begins signing with the new certificate
- Flow B accepts both old signatures (matches old CA) and new signatures (matches new CA)

**Phase 3: Prune Old**
- Once Flow A has fully rotated (no old signatures in flight), Flow B removes the old CA:
```yaml
treaties:
  - name: "flow-a"
    caCert: |
      -----BEGIN CERTIFICATE-----
      ... NEW CA CERTIFICATE ONLY ...
      -----END CERTIFICATE-----
```

### 7.2 Temporary Revocation

To revoke access:

1. **Remove:** Delete the Treaty entry from `spec.treaties[]`
2. **Apply:** Update the `FoundryFlow` CRD (`kubectl apply` or Helm upgrade)
3. **Effect:** Operator rebuilds trust store; incoming bundles fail with `TREATY_VIOLATION`
4. **Restore:** Re-add the Treaty entry to unpause

### 7.3 Runtime Updates vs Restart

Treaties are managed via the **Operator reconciliation loop**:

1. **Update:** Modify `FoundryFlow` CRD (kubectl apply, Helm upgrade, GitOps)
2. **Reconcile:** Operator detects change in `spec.treaties[]`
3. **Propagate:** Operator rebuilds Treaty CA Trust Store (Secret/ConfigMap)
4. **Reload:** Sidecars load CA bundle on boot

**Caveat:** Unless hot-reload is explicitly implemented, a **rolling restart** of Ingress/Node pods is required for Sidecars to load updated trust anchors. Given the security-critical nature, assume rollout is standard procedure:

```bash
# After Treaty update
kubectl rollout restart deployment/ingress-node -n flow-namespace
```
