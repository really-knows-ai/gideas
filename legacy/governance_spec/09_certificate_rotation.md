# Foundry Flow: Certificate Rotation Procedure

**Status:** v1 Specification

## 1. Overview

This document provides the operational procedure for rotating expired or compromised certificates in a live Foundry Flow deployment. The process is designed to be zero-downtime, ensuring the mTLS trust fabric remains intact throughout the rotation.

## 2. Rotation Triggers

Certificate rotation should be performed when:

- A certificate is nearing its expiry date (e.g., within 30 days).
- A private key is known or suspected to be compromised.
- The cryptographic standards need to be upgraded (e.g., moving from RSA-2048 to RSA-4096).

## 3. Zero-Downtime Rotation Procedure

The key to zero-downtime rotation is to temporarily configure all components to trust **both the old and the new** Certificate Authority (CA) during the transition period.

### Step 1: Generate New CA and Operator Certificates

1.  **Generate a new Root CA key and certificate.** This will be your `ca-new.crt` and `ca-new.key`.
2.  **Generate new certificates for each Operator** (e.g., `operator-a-new.crt`, `operator-b-new.crt`) signed by the **new** Root CA.

### Step 2: Create Combined Trust Bundle

1.  **Create a new ConfigMap** in Kubernetes that contains a trust bundle with **both** the old and new CA certificates.

    ```bash
    cat ca-old.crt ca-new.crt > combined-trust-bundle.crt
    kubectl create configmap combined-trust-bundle --from-file=ca.crt=combined-trust-bundle.crt
    ```

### Step 3: Update All Components to Use Combined Trust Bundle

1.  **Update the `FoundryFlow` CRD** to mount the `combined-trust-bundle` ConfigMap into all system components (Operator, Librarian, Archivist, etc.) and all `FoundryNode` pods.
2.  **Perform a rolling restart** of all system components and nodes. This will ensure they all trust certificates signed by either the old or the new CA.

### Step 4: Roll Out New Operator Certificates

1.  **Update the Kubernetes Secrets** for each Operator to use the new certificates (`operator-a-new.crt`, `operator-a-new.key`).
2.  **Perform a rolling restart** of the Operator pods. They will now present their new certificates, which will be trusted by all other components (because they trust the new CA).

### Step 5: Roll Out New Node Certificates

1.  The Governor will automatically begin issuing new certificates to nodes signed by the new CA as they request them.
2.  A rolling restart of all `FoundryNode` pods will ensure they all receive new certificates.

### Step 6: Remove Old CA from Trust Bundle

1.  Once all components in the system are using certificates signed by the new CA, you can safely remove the old CA from the trust bundle.
2.  **Update the `combined-trust-bundle` ConfigMap** to contain only `ca-new.crt`.
3.  **Perform a final rolling restart** of all components to complete the rotation process.

## 4. Emergency (Compromise) Rotation

In the event of a private key compromise, the procedure is the same, but the timeline is accelerated. The most critical step is to quickly roll out the combined trust bundle and then immediately begin replacing the compromised certificates.
