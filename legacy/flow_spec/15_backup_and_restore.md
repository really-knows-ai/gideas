# Foundry Flow: Backup and Restore Procedures

**Status:** v1 Specification

## 1. Overview

This document provides the operational procedures for performing routine backups and restorations of the stateful components in Foundry Flow: the **Librarian** and the **Archivist**.

## 2. Librarian Backup

The Librarian's state is stored in a SQLite database on a PersistentVolumeClaim (PVC). Backups are performed by taking a snapshot of this PVC.

### Backup Procedure

1.  **Identify the PVC** used by the Librarian pod.
2.  **Use a Kubernetes-native backup tool** (e.g., Velero, Kasten) to take a snapshot of the PVC. This is the recommended approach.
3.  **Alternatively, for manual backups:**
    a.  `kubectl exec` into the Librarian pod.
    b.  Use the `sqlite3` command-line tool to create a backup of the database file:
        ```bash
        sqlite3 /data/foundry.db ".backup /backup/foundry-$(date +%F-%T).db"
        ```
    c.  `kubectl cp` the backup file from the pod to a safe location.

## 3. Librarian Restore

Restoring the Librarian involves replacing the existing PVC with a new one created from a backup.

### Restore Procedure

1.  **Scale down the Librarian deployment** to 0 replicas.
2.  **Restore the PVC** from the desired backup using your backup tool.
3.  **Scale up the Librarian deployment** to 1 replica.
4.  The Librarian will start up using the restored database.

## 4. Archivist Backup

The Archivist stores its data in an S3-compatible object store. Backups are managed using the object storage provider's native tools.

### Backup Procedure

1.  **Enable versioning** on the S3 bucket used by the Archivist. This provides protection against accidental deletions.
2.  **Configure cross-region replication** for the bucket. This provides disaster recovery capabilities.
3.  **Take regular snapshots** of the bucket if your object storage provider supports this feature.

## 5. Archivist Restore

Restoring the Archivist involves pointing it to a restored S3 bucket.

### Restore Procedure

1.  **Restore the S3 bucket** from your backup.
2.  **Update the Archivist deployment** to point to the restored bucket.
3.  **Perform a rolling restart** of the Archivist pods.
