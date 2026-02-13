# Foundry Flow: Disaster Recovery Procedures

**Status:** v1 Specification

## 1. Overview

This document provides disaster recovery (DR) procedures for the stateful components of Foundry Flow. The goal is to restore the system to a consistent state after a failure, such as data corruption or infrastructure loss.

### 1.1 Responsibility Model

Foundry Flow manages backups for its own internal databases. Other stateful components rely on standard Kubernetes and cloud infrastructure practices.

| Component | Data Store | Backup Responsibility |
|---|---|---|
| **Librarian** | sqlite-vec (Laws, embeddings) | **Foundry Flow Backup Service** |
| **Archivist** | SQLite (Metadata index) | **Foundry Flow Backup Service** |
| **Archivist** | Artefact content (blobs) | Cloud provider (S3/GCS) or Storage administrator (PVC) |
| **CRDs** | Kubernetes etcd (Workitems, Nodes, Laws) | Kubernetes cluster administrator |
| **HITL Queue** | SQLite (per-node) | Ephemeral; rebuilt from Workitem CRDs on failure |
| **Audit Log** | External log aggregator (Splunk, Loki) | Operator's log aggregation platform |

## 2. Backup Service

The **Backup Service** is the central component responsible for backing up the Librarian and Archivist databases. It runs as a standalone deployment and calls a standard gRPC endpoint on these services to stream a consistent, live snapshot.

### 2.1 Backup Schedule and Retention

Backups are taken on a configurable schedule (default: hourly) and retained according to a tiered policy.

| Tier | Retention (Default) | Purpose |
|---|---|---|
| Hourly | 24 snapshots | Granular recovery for recent incidents |
| Daily | 7 snapshots | Short-term rollback |
| Weekly | 4 snapshots | Medium-term rollback |
| Monthly | 12 snapshots | Long-term compliance and audit |

### 2.2 Storage Destination

Snapshots are written to a configurable storage backend: a local PVC, or cloud object storage (S3, GCS, Azure Blob). See `02e_backup_service.md` for configuration details.

## 3. Librarian Recovery

The Librarian's state is stored in a sqlite-vec database on a PersistentVolumeClaim (PVC). Recovery involves restoring this database from a snapshot taken by the Backup Service.

### 3.1 Recovery Procedure

1.  **Identify the desired snapshot** from the Backup Service's storage destination. Snapshots are named with timestamps (e.g., `librarian-20260110T140000Z.db`).
2.  **Scale down the Librarian deployment** to 0 replicas.
    ```bash
    kubectl scale deployment librarian --replicas=0 -n <flow-namespace>
    ```
3.  **Copy the snapshot file** into the Librarian's PVC, replacing the existing database file. The exact method depends on the PVC's access mode (e.g., using a temporary pod to mount the PVC).
4.  **Scale up the Librarian deployment** to 1 replica.
    ```bash
    kubectl scale deployment librarian --replicas=1 -n <flow-namespace>
    ```
5.  The Librarian will start with the restored state. Any laws created after the snapshot was taken will be lost and must be re-created.

## 4. Archivist Recovery

The Archivist has two distinct data stores: a SQLite metadata index and the artefact content itself (blobs).

### 4.1 Metadata Index Recovery

The metadata index is backed up by the Backup Service. The recovery procedure is identical to the Librarian:

1.  Identify the desired snapshot from the Backup Service's storage destination.
2.  Scale down the Archivist deployment to 0 replicas.
3.  Copy the snapshot file into the Archivist's PVC.
4.  Scale up the Archivist deployment to 1 replica.

### 4.2 Artefact Content Recovery

Artefact content durability depends on the configured storage backend.

| Backend | Recovery |
|---|---|
| `blobstore` (S3, GCS, Azure Blob) | Cloud providers offer 11 nines of durability. Data loss is extremely unlikely. If the bucket is lost, recovery requires re-creating artefacts from their original sources. |
| `filesystem` (PVC) | Durability depends on the underlying storage class. The storage administrator is responsible for backups (e.g., using Velero or storage-level snapshots). |

## 5. HITL Queue Recovery

HITL Node queues are stored in a local SQLite database on each node's PVC. These queues are considered **ephemeral** because they can be rebuilt from the source of truth: the Workitem CRDs.

### 5.1 Recovery Procedure

If a HITL node loses its queue database:

1.  The node will start with an empty queue.
2.  The queue can be repopulated by querying for all Workitems currently assigned to that node's role that are awaiting human decision.
    ```bash
    kubectl get workitems -n <flow-namespace> \
      -l "flow.gideas.io/current-role=<hitl-role>" \
      --field-selector "status.phase=waiting-human"
    ```
3.  An administrative script or the HITL node itself (on startup) can re-ingest these workitems into its local queue.

## 6. CRD (etcd) Recovery

All Foundry Flow CRDs (`FoundryFlow`, `FoundryNode`, `Workitem`, `Law`, etc.) are stored in the Kubernetes API server, which is backed by etcd. Backup and recovery of etcd is a standard Kubernetes administrative task.

**Recommendation:** Use a cluster backup tool such as **Velero** to create regular backups of the cluster state, including all CRDs. Refer to the official Kubernetes documentation and your cloud provider's guidance for etcd backup and restore procedures.

## 7. Audit Log Recovery

Audit logs are emitted by the Flow Monitor to stdout and collected by the operator's log aggregation platform (Splunk, Loki, Elasticsearch, etc.). The retention and recovery of audit logs is the responsibility of the operator and is governed by the configuration of their chosen platform.

## 8. Node Upgrade Strategy

Upgrading `FoundryNode` images without losing in-flight work is critical for maintaining a healthy system.

### 8.1 Zero-Downtime Rolling Update

1.  **Update the `FoundryNode` CRD** with the new container image tag.
2.  The Operator will automatically perform a **rolling update** of the node's StatefulSet or Deployment.
3.  The **Graceful Termination Protocol** (see `node_spec/01_security_and_health.md`) ensures that pods are terminated only after they have finished processing their active workitems.
4.  New pods will be created with the updated image and will begin accepting new work once they are ready.
