# Governance Flow Operator: Security Model

## 4. Security Model

### 4.1 Threat Model

**Assets Protected:**
* `state-root.key`: If compromised, the entire State trust collapses.
* Tier 3 Law CRDs: If corrupted, all Sibling Flows may execute malicious policy.

**Attack Vectors:**
* **Insider Threat:** Malicious admin with Kubernetes RBAC access.
* **Supply Chain:** Compromised Operator image.
* **Network:** Man-in-the-middle attacks on CSR signing traffic.

**Mitigations:**
* **RBAC:** Strict least-privilege roles.
* **Image Signing:** Governor image must be signed by Foundry maintainers.
* **mTLS:** All gRPC traffic encrypted and authenticated.
* **Audit Logging:** All certificate issuance and Law writes logged.

### 4.2 Trust Assumptions

The Governor assumes:
1. **Kubernetes RBAC is correctly configured.**
2. **The underlying Kubernetes cluster is trusted.**
3. **Federal Instances are authentic.**
