# Governance Flow Operator: Overview

> **Plane:** Federation (with Security Plane integration)

> **Stability Note:** The Governance Operator is currently a v3.6.0 MVP design. v1 Foundry Flows are Atomic-only. This spec documents the Governor's architecture for **v2 Federation** (planned). See "Current v1 Role" below.

## 1. Executive Summary & Identity

The **Governance Flow Operator** (the **State Governor**) is the centerpiece of the **Federation Plane**. It serves as the **State Root** for the G(IDEAS) trust hierarchy, functioning as a **trust anchor** and **diplomatic gateway** that enables multiple operational flows to federate under a single State authority.

### Federation Plane Role

The Governor operates at the intersection of Federation and Security planes:

| Responsibility | Plane | Description |
|----------------|-------|-------------|
| Certificate issuance | Security | State Root CA, intermediate cert signing |
| Inter-flow trust | Federation | Annexation Protocol, sibling registration |
| Law authority | Governance + Federation | Tier 3 publication, Tier 4 Federal sync |
| Treaty management | Federation | Cross-state collaboration permits |

### Current v1 Role

In v1, the Governance Operator exists as a **trust anchor blueprint**:
- Operators can deploy the Governor in a dedicated namespace for governance experimentation
- The Governor establishes the State Root CA infrastructure (certificate generation, CSR signing endpoints)
- Inter-flow federation and law authority delegation are planned for v2
- Tier 3 Law publication to a "State Library" is planned for v2
- Federation with Tier 4 Federal instances is planned for v2

**v1 flows are Atomic and self-contained.** The Governor is available for early adoption and testing.

### 1.1 The State Trust Domain

The **State** is defined as a hierarchical Trust Domain where:

* **The Governor is the Root Authority:** The Governor generates and protects the self-signed `state-root.key` and issues certificates to all subordinate operators.
* **Sibling Operators are Intermediate Authorities:** Operational Flow Operators (Ideate, Execute, Sustain, etc.) function as Intermediate CAs within the State hierarchy, inheriting trust from the Governor.
* **Cross-Sibling Trust:** All Sibling Flows share a common Trust Root, enabling cryptographic verification of artefacts across Flow boundaries without direct peer relationships.

**Trust Chain Example:**
```
state-root.crt (Self-signed by Governor)
  └─ ideate-operator.crt (Issued by Governor)
       └─ forge-node.crt (Issued by Ideate Operator)
            └─ artefact-stamp-signature (Signed by Forge Node)
```

Any Node in the State can validate this chain by traversing up to the shared `state-root.crt` Trust Bundle.

### 1.2 Identity & Scope

* **Deployment Model:** One Governor per State (typically one per organization or business unit).
* **Namespace:** Runs in a dedicated Kubernetes namespace (e.g., `governance-flow`).
* **Sovereignty:** The Governor holds exclusive authority over:
  * Certificate issuance within the State trust domain
  * Tier 3 Law publication to the State Library
  * Federation with upstream Tier 4 Federal Instances

**Section 1.2 Identity & Scope (Updated Schema):**

```yaml
spec:
  authority:
    # Determines the Signer Provider implementation
    mode: "Local" # Options: Local (Phase 1), CloudKMS (Phase 2), Vault (Phase 3)
    
    # Configuration for Phase 2 (Azure/AWS/GCP)
    cloudKMS:
      provider: "azure-keyvault" 
      keyId: "https://foundry-vault.vault.azure.net/keys/state-root"
      
    # Configuration for Phase 3 (HashiCorp)
    vault:
      address: "https://vault.internal:8200"
      transitPath: "foundry-sign"
      keyName: "state-root"
```

**Future v2 Contrast with Standard Operator:**

Once v2 Federation is active:

| Capability | Standard Operator (Atomic) | Governor Operator (State) |
|------------|---------------------------|---------------------------|
| Certificate Authority | Intermediate CA (receives cert from Governor) | Root CA (self-signed) |
| Law Authority | Read-only consumer of Tier 3/4 | Write authority for Tier 3 |
| Federation | Client-only (pulls from Governor) | Gateway (pulls Tier 4, publishes Tier 3) |
| Trust Scope | Single Flow | Entire State (all Sibling Flows) |

**In v1:** Standard Operators are fully standalone.
