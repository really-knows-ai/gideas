# Governance Flow Operator: Operational Considerations

## 5. Operational Considerations

### 5.1 High Availability

The Governor runs as a **High-Availability Deployment** (Default: 2 Replicas) to eliminate single points of failure. The control plane is split into two functional roles:

1.  **The Bureaucrat Plane (Active-Active):**
    *   **Responsibilities:** `SignCSR` (gRPC), `GetSnapshot` (HTTP).
    *   **Mechanism:** Runs on **all** replicas.
    *   **Concurrency:** Uses high-entropy random serial numbers (160-bit) to allow non-coordinated signing.
    *   **Read Consistency:** Each replica builds its own local `snapshot.tar.gz` by watching Kubernetes Law CRDs (using a shared Informer cache).

2.  **The Sovereign Plane (Leader-Elected):**
    *   **Responsibilities:** Federation Sync, Petition Reconciliation, Law Publication.
    *   **Mechanism:** Runs **only on the Leader**.
    *   **Coordination:** Uses `coordination.k8s.io` Leases (`governance-lock`) to elect a leader. If the leader dies, a standby takes over in ~15 seconds.

### 5.2 Disaster Recovery (Updated)

Disaster recovery logic must shift from "backing up bits" to "governing access".

* **Local Mode:** Recovery requires the restoration of the `state-root-keypair` Secret.
* **KMS/Vault Mode:** Recovery is achieved by pointing a new Governor deployment at the existing Key ID. The "backup" is the infrastructure policy (e.g., Azure KeyVault Soft-Delete) rather than a file.
