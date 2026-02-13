# Foundry Node: Responsibilities

## 6. Summary of Responsibilities

| Component | Responsibility |
| :--- | :--- |
| **Developer** | Writes business logic. Manages persistent connections (pools). **Must explicitly manage long-running operations** by calling `heartbeat()` or using `FoundryAgent` to maintain session liveness. May call `signal_ready()` to delay readiness during heavy initialization. **For `HitlNode` implementations: must configure `spec.storage` if queue persistence is required across Pod restarts.** |
| **Nodes** | Execute logic via Session API. May react to assignments from the Operator and/or proactively generate new Workitems based on internal triggers. Have direct network access to external APIs. |
| **Platform Architect** | Defines `mixins` and Base Images. |
| **Flow Operator** | Verifies Image Lineage. Validates Config vs. Capabilities. Injects `assay` routes. **Ensures `livenessProbe`, `readinessProbe`, and `startupProbe` are correctly configured to eliminate the "Zombie Gap" and protect slow boots.** **Provisions PVCs when `spec.storage` is requested by a node.** **Handles timeout escalation by checking for `timeout` output before marking Workitems as Failed.** |
| **Sidecar** | Enforces `capabilities` (RBAC) at runtime. Brokers all Flow interactions (telemetry, storage, CRD mutations). **Manages health signals, ensuring liveness timeouts only apply while a Workitem is active to prevent "Idle Death."** For Sustain Nodes: subscribes to FlowMonitor and forwards matching events to Node via `DeliverTelemetry` RPC. |
| **Kernel** | Enforces `NET_ADMIN` drops and unprivileged user execution. |

---

## 7. Data Residency

Understanding where data lives is critical for compliance and architecture decisions.

| Data Type | Location | Storage | Notes |
|-----------|----------|---------|-------|
| **Workitem CRDs** | Local Cluster | etcd | Always local. Sovereign to the Flow namespace. |
| **Artefact Content** | Configurable | Archivist (PVC or Blobstore) | May reside in external Object Storage (S3/GCS) if `blobstore` driver configured. |
| **Law Corpus** | Local Cluster | Librarian SQLite + Archivist snapshots | Always local. Embeddings stored alongside law records. |
| **Telemetry** | Local Cluster | Flow Monitor buffer | May be exported to external systems (Prometheus, etc.). |

**Key Implications:**

1. **Workitem Sovereignty:** Regardless of Archivist backend, Workitem metadata (spec, status, routing) remains in-cluster. The CRD is the source of truth for lifecycle.

2. **Artefact Portability:** With `blobstore` driver, artefact content lives in cloud storage. This enables:
   - Multi-region redundancy (S3 Cross-Region Replication)
   - Cost optimization (S3 Intelligent-Tiering)
   - Compliance controls (bucket policies, encryption)

3. **Export/Import Bundles:** When building bundles for cross-flow collaboration, the Archivist abstracts storage location. Sidecars fetch artefacts via the Archivist regardless of whether they're on PVC or S3.

4. **Audit Trail:** The Workitem CRD contains artefact hashes in `status.artefacts[]`. Both artefact content and passport stamps are stored in the Archivist (whether backed by PVC or S3), ensuring provenance data remains within the cluster's control.
