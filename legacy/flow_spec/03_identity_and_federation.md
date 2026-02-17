# Atomic Foundry Flow: Identity and Federation

## 2.4 System Security (Identity Architecture)

The Flow operates on a **Zero-Trust** model. Network reachability is distinct from authorization; access requires valid mTLS credentials.

### 2.4.1 Identity & Federation (The Annexation Protocol)

In **Federated mode**, the Flow Operator acts as an Intermediate Certificate Authority within the State trust hierarchy.

**The Annexation Protocol (Detailed Handshake):**

A Sibling Operator bootstraps trust with the Governor by submitting a Certificate Signing Request and receiving a certificate that anchors it to the State Root.

**Step 1: Keypair Generation (The Subject)**
- **Trigger:** Operator boots with `federationMode: "Federated"` and a valid `governanceEndpoint`.
- **Action:** Operator detects it lacks a signed CA certificate.
- **Result:** Operator generates a local **RSA-2048** keypair (this will later sign Sidecar certificates).

**Step 2: CSR Submission (The Petition)**
- **Endpoint:** Operator calls the Governor's `SignCSR` gRPC endpoint (port **35698**, system services port).
- **Payload:**
  ```protobuf
  message CSRRequest {
    string operator_id = 1;        // e.g., "ideate-operator"
    string namespace = 2;          // e.g., "flow-ideate"
    string csr_pem = 3;            // PEM-encoded CSR with public key
    string attestation_token = 4;  // Kubernetes ServiceAccount JWT
  }
  ```

**Step 3: Validation (The Governor's Check)**
- **K8s Auth:** Governor verifies `attestation_token` is valid and issued for the claimed namespace.
- **Namespace Isolation:** Governor confirms the operator is actually running in the stated namespace.
- **Crypto Validation:** Governor parses the CSR to ensure Subject CN, Key Size (2048+) meet constraints.
- **Result:** Reject if any validation fails; proceed if all pass.

**Step 4: Issuance (The Signature)**
- **Action:** Governor signs the CSR using the **State Root Private Key** (RSA-4096).
- **Validity:** Certificate valid for **365 days** (see renewal policy in [01_certificate_authority.md](../governance_spec/01_certificate_authority.md)).
- **Result:** Governor returns `CSRResponse` containing:
  - **Operational Certificate** (Intermediate CA cert signed by State Root)
  - **State Root CA Certificate** (the trust anchor)

**Step 5: Trust Convergence (The Overwrite)**
- **Action:** Operator stores the Operational Certificate in a local Kubernetes Secret (e.g., `operator-ca-keypair`).
- **Action:** Operator **overwrites** its local Trust Store with the **State Root CA Certificate**.
- **Effect:** The Operator is now a trusted Intermediate CA within the State hierarchy.
- **Implication:** Any certificate issued by this Operator is now verifiable by anyone who trusts the State Root.

**Step 6: Sidecar Issuance (The Downstream)**
- **When:** When Sidecars (Node pods) boot, the Operator issues them mTLS certificates.
- **Signed By:** Operator signs Sidecar certs using its **Intermediate CA** (the certificate received in Step 4).
- **Chain:** Sidecar's certificate chain: `Sidecar → Sibling Operator CA → State Root CA`.
- **Result:** Sidecars now have cryptographic identity valid across the entire State.

### 2.4.2 Sidecar Identity & Artefact Provenance

Sidecars gain persistent cryptographic identity to enable state-wide artefact validation through certificate chains.

**StampArtefact Protocol:**
1. **Hash:** Sidecar recomputes the artefact hash.
2. **Sign:** Sidecar signs the hash using its private key.
3. **Passport Stamp:** Sidecar constructs a stamp including the signature and the full certificate chain (node cert, operator cert, and in v2: state root cert).
4. **Storage:** The stamp is stored in the Archivist as metadata alongside the artefact content.
5. **Passport Clearing:** When a new version of the artefact is stored, the passport is cleared (new stamps required for new version).

**Validation:** Terminal Guard retrieves stamps from the Archivist via `GetPassport()`. Certificate chains enable cross-flow verification in Federated mode (v2).

### Atomic vs Federated Trust (Summary)

- **Atomic v1:** The Flow Operator is the sole trust root. Stamps must originate from nodes issued by the local Operator CA. Certificate-chain traversal is scoped to the local Operator CA.
  - **Trusted Root:** The Operator's self-signed CA certificate (RSA-4096), generated during bootstrap and stored in the Kubernetes Secret `foundry-operator-ca-keypair`.
  - **How Terminal Guard Validates:** Upon Terminal Guard initialization, it loads the Operator CA certificate from this Secret and uses it as the sole trust anchor. All stamp certificates must chain back to this CA; no upstream State Root is recognized.
  
- **Federated v2:** Operators annex into a State trust hierarchy. Stamps may originate from any sibling flow; the Terminal Guard validates by traversing the chain to the State Root.

Design implication: For inter-flow portability of provenance, use v2 Federation. In v1, treat external artefacts as fresh inputs and re-stamp locally (chain-of-custody resets).

---

## 2.5 Treaty Trust Model (Cross-Flow Collaboration)

Treaties enable **Atomic Flows** to collaborate without full Federation. A Treaty is a receiver-side configuration that grants import permission to a specific foreign Flow.

### 2.5.1 Identity vs Transport

Cross-flow collaboration separates two concerns:

| Concern | Mechanism | Configuration |
|---------|-----------|---------------|
| **Identity** | Certificate validation | Treaty CA pinning or State Root (Federation) |
| **Transport** | HTTPS/gRPC | Endpoint URL |

- **Identity:** "Who can I trust?" → Configured via Treaty (Atomic) or State Root (Federated)
- **Transport:** "How do I reach them?" → Standard network (K8s Services, Ingress, external URLs)

### 2.5.2 Treaty Definition

A Treaty is a **unilateral import permit**. The receiver grants trust; the sender requires only the endpoint URL.

- **A trusts B ≠ B trusts A**
- Full bidirectional collaboration requires two independent Treaties

**Structure:**
```yaml
spec:
  treaties:
    - name: "vendor-flow-alpha"      # Unique identifier
      caCert: |                       # Foreign Operator's CA certificate
        -----BEGIN CERTIFICATE-----
        MIIFazCCA1OgAwIBAgIUZ...
        -----END CERTIFICATE-----
      allowedSubjects:                # Which node CNs can sign bundles
        - "export-node"
        - "terminal-exporter"
      allowedWorkitemTypes:           # Optional: restrict workitem types
        - "vendor-submission-v1"
      maxBundleSize: "50Mi"           # Optional: reject oversized bundles
```

### 2.5.3 Treaty vs Federation

| Aspect | Treaty (Atomic) | Federation (v2) |
|--------|-----------------|-----------------|
| **Trust Anchor** | Pinned CA per peer | Shared State Root |
| **Configuration** | Explicit whitelist | Implicit (all siblings trusted) |
| **Use Case** | Cross-organization collaboration | Intra-organization multi-flow |
| **Stamp Validity** | Foreign stamps ignored, require re-stamp | Foreign stamps valid if chain to State Root |

**Federation is essentially a "Global Treaty"** where every sibling Flow automatically trusts all other siblings via the shared State Root CA.

### 2.5.4 Treaty Validation Flow

When an Ingress Node receives a bundle:

```
1. Extract signature from bundle
2. Parse signer certificate
3. For each Treaty in config:
   a. Check if cert chains to Treaty CA
   b. Check if cert CN matches allowedSubjects[]
   c. If allowedWorkitemTypes set, check manifest type
4. If any Treaty validates: ACCEPT
5. If no Treaty validates: REJECT with TREATY_VIOLATION
```

### 2.5.5 Sender Requirements

The **sender** (Export Node) does NOT need a Treaty with the receiver. It only needs:

- **Endpoint URL:** Where to send the bundle
- **Standard TLS:** Server certificate verification

The sender's Sidecar signs the bundle with its node certificate (issued by its local Operator). The receiver validates this signature against its Treaty CA store.

### 2.5.6 Chain of Custody Reset (Naturalization)

When a bundle is imported via Treaty, **foreign stamps are preserved for audit only**:

- **Reason:** The receiving Flow cannot verify signatures from an unknown Root CA
- **Mechanism:** Stamps signed by Foreign CA are preserved for audit but do not count toward `GovernedArtefact.requiredRoles`
- **Solution:** The Ingress Node applies a local "Naturalization Stamp," starting a new chain of custody

This is the same behavior as Atomic v1 handling of any external artefact—the chain-of-custody resets at the Flow boundary.

See [14_cross_flow_collaboration.md](14_cross_flow_collaboration.md) for full specification of Export/Import operations.

