# Foundry Node: Supply Chain

## 3. The Supply Chain: Image Hierarchy

Security is enforced via strict inheritance. All Nodes must be derived from the immutable kernel layer.

### 3.1 The Provenance Contract

Every Node deployment must maintain strict, auditable lineage from the base image through to execution.

**Image Digest Capture (Sidecar Boot-Time):**

When a Node Pod starts, the Sidecar performs provenance capture:

1. **Introspection:** Reads container metadata via Kubernetes Downward API:
   ```yaml
   env:
     - name: NODE_IMAGE_DIGEST
       valueFrom:
         fieldRef:
           fieldPath: status.containerStatuses[?(@.name=="node")].imageID
   ```
2. **Parsing:** Extracts the full digest (e.g., `sha256:abc123...def456`).
3. **Caching:** Stores digest in memory for the Pod's lifetime.
4. **Injection:** Automatically includes `node_image_digest` in every `StampArtefact` operation.

**Purpose:** Distinguishes between identical code versions (e.g., `v1.0` deployed safely vs. `v1.0` with a critical bug). This enables:
- Supply chain audit trails
- Vulnerability tracking (which specific build of `v1.0` was used)
- Rollback verification (confirm exact image in production)

#### 3.1.1 Archivist-Resident Validation

Artefact stamps are stored in the Archivist as metadata alongside artefact content. The Terminal Guard validates stamp authenticity by retrieving passport data from the Archivist.

**Validation Protocol:**

When a Node returns `Complete()`, the Terminal Guard validates terminal contracts:

1. **Extract Stamps:** Fetch Passport Stamps from the Archivist via `GetPassport()` using the artefact hash from `Workitem.status.artefacts[]`.

2. **Check Roles:** For each stamp, verify the `role` field matches one of the `requiredRoles` defined in the `GovernedArtefact`.

3. **Parse Certificate Chain:** Extract the certificate array from `stamp.certificateChain[]`:
   ```yaml
   passport:
     - role: "linter"
       node: "quench-node"
       timestamp: "2026-01-04T14:30:00Z"
       signature: "sig_rsa_4096..."
       certificateChain:
         - <PEM: signing-node.crt>         # Node certificate (leaf)
         - <PEM: issuing-operator.crt>     # Operator certificate (intermediate)
         - <PEM: state-root.crt>           # State Root certificate (root, v2 federation only)
   ```

4. **Verify Signature:**
   ```bash
   # Extract public key from Node certificate (chain[0])
   openssl x509 -in signing-node.crt -pubkey -noout > node.pub
   
   # Verify artefact hash signature
   echo -n "$artefact_hash" | openssl dgst -sha256 -verify node.pub \
     -signature <stamp.signature>
   ```

5. **Validate Certificate Chain:** (v2 Federated mode only)
   ```bash
   # In v1 Atomic mode, chain validation is skipped
   # In v2, traverse chain to State Root
   openssl verify -CAfile /var/run/foundry/trust/state-root.crt \
     -untrusted issuing-operator.crt \
     signing-node.crt
   ```
   
   **Success Condition:** Chain validation returns `OK` and all certificates are within their validity period.

**Design Note:** The Sidecar injects the `node_image_digest` into the stamp struct when `Stamp()` is called, enabling supply chain audit trails within the passport itself.

> Note (Atomic v1): The Flow operates as a sovereign silo. Only stamps created by nodes whose certificates are issued by the local Flow Operator are valid. Cross-flow stamps (issued by a different Operator CA or containing a federated chain) are rejected during Terminal Guard validation (failure reason: `SIGNATURE_INVALID`). Portable provenance requires v2 Federation.

### Layer 0: The Kernel (`foundry/node`)

* **Content:** Alpine/Distroless OS, strict non-root user setup, and the `NET_ADMIN` capability drop configuration.
* **Immutable Marker:** This layer produces a deterministic **RootFS DiffID** (SHA256).
* **Enforcement:** The Flow Operator verifies this hash during `FoundryNode` reconciliation. It inspects the user's container image manifest, extracts the bottom-most layer's DiffID, and rejects the Pod if it doesn't match the known kernel hash.

**Why RootFS DiffID (Not Layer Digest)?**
- **DiffID** refers to the SHA256 hash of the *uncompressed* layer content (the actual filesystem).
- **Layer Digest** refers to the SHA256 hash of the compressed layer in the registry (depends on compression settings, varies by push/pull).
- Using DiffID ensures that the *actual kernel filesystem* is cryptographically verified, even if the image is re-tagged, re-compressed, or mirrored to a different registry.

**Enforcement During Reconciliation:**
1. User creates a `FoundryNode` CRD with `spec.image: "myrepo/my-python-node:v1.0"`.
2. Flow Operator fetches the image manifest from the registry.
3. Operator extracts the config's `diffID` of the bottom layer (the one derived from `FROM foundry/python-node`).
4. Operator compares this `diffID` against the allowed kernel `diffID` (stored in a ConfigMap or hardcoded).
5. If match: approve the FoundryNode.
6. If mismatch: reject validation; Pod is never scheduled.

**Result:** No user can bypass the kernel by modifying the base layer—the filesystem content is the proof.

### Layer 1: The Runtime (`foundry/python-node`, etc.)

* **Source:** Maintained by the Platform Team.
* **Content:** Language Runtime, Foundry SDK, and the Session Agent.

### Layer 2: User Implementation

* **Source:** Built by Developer Teams.
* **Contract:** `FROM foundry/python-node:v1.2`.

---

## 4. Naturalization: Chain of Custody Reset

When artefacts enter a Flow from an external source (via the Import operation), a **Chain of Custody Reset** occurs. This section defines how foreign provenance is handled.

### 4.1 The Problem

Foreign artefacts may carry stamps from their origin Flow:
- In **Atomic mode:** The local Terminal Guard cannot verify stamps signed by a foreign Operator CA (it only trusts its own Operator)
- In **Federated mode:** Stamps from sibling Flows are valid IF they chain to the shared State Root

The question: How do we accept external artefacts without compromising local validation?

### 4.2 The Solution: Naturalization

**Naturalization** is the process of establishing local provenance for foreign artefacts.

When an Ingress Node imports a bundle:
1. **Foreign stamps are preserved** in `Workitem.status.context.foreignProvenance` (audit trail)
2. **Foreign stamps are NOT counted** toward `GovernedArtefact.requiredRoles` (machine validity)
3. **The Ingress Node applies a local stamp** (the "Naturalization Stamp")
4. **Local validity begins** from the Naturalization Stamp

```yaml
# Example: Imported workitem status
status:
  context:
    foreignSource:
      flow: "vendor-flow-alpha"
      workitemId: "submission-123"
      exportedAt: "2026-01-08T14:30:00Z"
    foreignProvenance:
      # Original stamps - for audit, NOT for validity
      petition-draft:
        - role: "vendor-linter"
          node: "vendor-lint-node"
          timestamp: "2026-01-08T14:00:00Z"
          # Note: signature cannot be verified locally
  
  artefacts:
    - kind: "petition-draft"
      name: "petition_draft.md"
      latestVersion: "sha256:abc123..."
      # Note: Passport stamps (including the Naturalization Stamp) are stored
      # in the Archivist and retrieved via GetArtefactMetadata(). Only locally-verifiable
      # stamps count toward validity; foreign stamps are preserved for audit but ignored.
```

### 4.3 Trust Policy Patterns

The `GovernedArtefact` configuration determines how much local validation is required after naturalization.

#### Pattern A: "Rubber Stamp" (Trusted Source)

If you completely trust the foreign Flow, the import stamp alone makes the artefact valid:

```yaml
# GovernedArtefact configuration
apiVersion: flow.gideas.io/v1
kind: GovernedArtefact
metadata:
  name: trusted-import
spec:
  requiredRoles:
    - "importer"  # Naturalization stamp = valid
```

**Use Case:** Trusted vendor, internal team, or pre-approved source.

**Outcome:** Ingress Node stamps → artefact is immediately valid → can flow directly to Terminal.

#### Pattern B: "Border Check" (Distrustful Source)

If you want local re-verification regardless of foreign stamps:

```yaml
# GovernedArtefact configuration
apiVersion: flow.gideas.io/v1
kind: GovernedArtefact
metadata:
  name: untrusted-import
spec:
  requiredRoles:
    - "importer"           # Proves origin
    - "security-reviewer"  # Local verification required
    - "compliance-checker" # Additional local validation
```

**Use Case:** Unknown vendor, external submission, high-risk content.

**Outcome:** Ingress Node stamps (proves origin), but artefact is NOT valid until local Security Node and Compliance Node also stamp it.

### 4.4 Foreign Stamp Handling in Terminal Guard

The Terminal Guard validation logic handles foreign stamps as follows:

```go
func (tg *TerminalGuard) validateArtefact(artefact Artefact, required GovernedArtefact) bool {
    // Collect valid stamps (those we can cryptographically verify)
    validStamps := []Stamp{}
    
    for _, stamp := range artefact.Passport {
        // Attempt to verify signature against local trust store
        if tg.verifySignature(stamp) {
            validStamps = append(validStamps, stamp)
        }
        // Foreign stamps fail verification silently (preserved but ignored)
    }
    
    // Check if valid stamps cover all required roles
    return tg.coversRequiredRoles(validStamps, required.RequiredRoles)
}
```

**Key Behavior:**
- Foreign stamps fail signature verification (unknown CA)
- They are NOT removed from the passport (preserved for audit)
- They simply don't contribute to validity calculation
- Only locally-verifiable stamps count

### 4.5 Federated Mode Exception

In **v2 Federated mode**, stamps from sibling Flows ARE valid if they chain to the shared State Root:

```go
func (tg *TerminalGuard) verifySignature(stamp Stamp) bool {
    // Build certificate chain from stamp
    chain := stamp.CertificateChain
    
    // Verify chain terminates at trusted root
    switch tg.mode {
    case Atomic:
        // Only local Operator CA is trusted
        return tg.localOperatorCA.Verify(chain)
    case Federated:
        // State Root CA is trusted (accepts sibling stamps)
        return tg.stateRootCA.Verify(chain)
    }
}
```

In Federated mode, "naturalization" may be unnecessary for artefacts from sibling Flows—their stamps are already valid. However, the Ingress Node MAY still apply a local stamp to record the import event.

See [14_cross_flow_collaboration.md](../flow_spec/14_cross_flow_collaboration.md) for the full Export/Import specification.
