# Governance Flow Operator: Certificate Authority

## 2.1 The Certificate Authority (The Issuer)

The Governor implements the State Root CA, issuing cryptographic identity to all Sibling Flow Operators.

### 2.1.1 Root Key Management (The Signer Abstraction)

The core logic of the Certificate Authority is now decoupled from the key material. The Governor no longer "possesses" a key; it "utilises" a provider.

The Governor interacts with a `Signer` interface. Cryptographic operations (signing CSRs) are delegated to one of three providers based on the `GovernanceFlow` spec:

* **Phase 1: Local Provider (Default):** The Governor generates a self-signed RSA-4096 keypair on boot. It stores this key in a Kubernetes Secret named `state-root-keypair`.
* **Phase 2: Cloud KMS Provider:** The Governor authenticates via Managed Identity (Azure) or IAM (AWS/GCP). It sends CSR hashes to the cloud API for signing. The private key never enters the Governor container.
* **Phase 3: HashiCorp Vault Provider:** The Governor utilises the Vault **Transit Engine**. It performs high-throughput signing operations without the Governor ever seeing the raw private key.

### 2.1.2 CSR Signing Endpoint

The Governor exposes a gRPC endpoint for Sibling Operators to request certificates during their Annexation handshake.

**Service Definition:**
```protobuf
service GovernorAuthority {
  // Sibling Operators submit Certificate Signing Requests
  rpc SignCSR(CSRRequest) returns (CSRResponse);
}

message CSRRequest {
  string operator_id = 1;        // e.g., "ideate-operator"
  string csr_pem = 2;            // PEM-encoded CSR
  string namespace = 3;          // e.g., "flow-ideate"
  string attestation_token = 4;  // Kubernetes ServiceAccount JWT
}
```

**Request Validation:**
1. **Kubernetes Authentication:** Verify the `attestation_token` against the Kubernetes API.
2. **Namespace Isolation:** Confirm the requester's namespace is authorized.
3. **CSR Validation:** Parse the CSR and verify subject CN and key size.

### 2.1.3 Certificate Lifecycle

**Validity Period:** Issued certificates are valid for **365 days**.

**Renewal:** Sibling Operators automatically renew certificates 30 days before expiration by submitting a new CSR.

**Renewal Failure Policy (Critical System Alert):**
- If certificate renewal fails (CSR submission error, Governor unavailable, authentication failure), the Operator retries via its standard reconciliation loop.
- If renewal failures persist for **24 hours** OR the certificate has less than **7 days until expiration**, the Operator **MUST** emit a `foundry.system.alert` telemetry event:
  ```json
  {
    "type": "foundry.system.alert",
    "severity": "CRITICAL",
    "component": "identity-manager",
    "error": "CertificateRenewalFailed",
    "details": "Certificate expires in N days. Renewal attempts failed: [Last error message]",
    "days_remaining": N,
    "operator_id": "ideate-operator"
  }
  ```
- Rationale: Silent renewal failures allow operators to silently expire 30 days later. Early alerting enables proactive remediation (restart Governor, diagnose network, manually force renewal).
- Recommended Action: External monitoring (Prometheus, PagerDuty) should treat `foundry.system.alert` events from `identity-manager` as `P1/SEV1` incidents.

**Revocation:** Revocation is achieved by deleting the Sibling Operator's `FoundryFlow` CRD and propagating a revocation telemetry event.

### 2.1.4 Root CA Bootstrap (Leader Election)

The Governor uses Kubernetes Lease-based leader election to coordinate Root CA provisioning during initial deployment.

**Bootstrap Sequence:**

1. **Leader Election:** On startup, each Governor replica participates in leader election using a Kubernetes Lease object (`governor-leader` in the Governor namespace).
2. **Leader Provisioning:** The elected leader checks for the existence of the `state-root-keypair` Secret.
   - If the Secret exists: read and use it.
   - If the Secret does not exist: generate the Root CA keypair and create the Secret.
3. **Follower Waiting:** Non-leader replicas wait for the `state-root-keypair` Secret to appear (polling with exponential backoff, max 30s).
4. **Ready State:** Once all replicas have access to the Root CA, they transition to ready and can serve CSR signing requests.

**Leader Failure During Bootstrap:**

If the leader dies before completing Root CA provisioning, a new leader is elected and completes the process. The Lease has a 15-second duration with 10-second renewal and 2-second retry period.

**Post-Bootstrap Operation:**

Once the Root CA exists, leader election is no longer required for signing operations. All replicas can sign CSRs concurrently (see Section 2.1.5).

### 2.1.5 Concurrency and High Availability

To support Active-Active signing across multiple Governor replicas:

*   **Random Serial Numbers:** The CA MUST use **20-octet (160-bit) random serial numbers** (RFC 5280). This provides sufficient entropy to prevent collisions without centralized coordination or a shared database.
*   **Shared Key Access:** In Local Provider mode, all replicas mount the same `state-root-keypair` Secret as a read-only volume. In KMS/Vault modes, all replicas authenticate to the same key identifier.

### 2.1.6 Lifecycle Management

**Bootstrap:**
- Initial Operator certificates are generated via an offline root CA or Local Provider.
- Leaf certificates issued to Operators are stored in Kubernetes Secrets.

**Renewal:**
- Operators renew certificates before expiry via CSR against the configured provider.
- Renewal window begins 30 days before expiry; escalate if < 7 days remaining.

**Revocation:**
- Maintain CRL/OCSP endpoints for mTLS modes; sidecars and services check revocation on connect.
- Distribute CRL updates via ConfigMap and trigger hot-reload on change.

**Key Storage:**
- Private keys stored in Kubernetes Secrets with restricted RBAC (Local Provider).
- HSM/KMS-backed storage recommended for production Operators.

**Certificate Profile:**
- X.509 with SANs for service DNS names.
- Extensions: `clientAuth`, `serverAuth`; path length constraints enforced.
