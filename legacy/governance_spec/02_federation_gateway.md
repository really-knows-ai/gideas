# Governance Flow Operator: Federation Gateway

## 2.2 The Diplomat (The Gateway)

The Governor functions as the State's diplomatic interface to upstream **Federal G(IDEAS) Instances** (Tier 4 authorities).

### 2.2.1 Federation Client

The Governor maintains persistent gRPC connections to one or more Tier 4 Federal Instances defined in its configuration.

**Configuration Example:**
```yaml
apiVersion: flow.gideas.io/v1
kind: GovernanceFlow
metadata:
  name: state-governance
  namespace: governance-flow
spec:
  federationConfig:
    tier4Authorities:
      - name: "federal-security"
        endpoint: "security-gov.gideas.io:443"
        namespaces: ["security/*", "privacy/*"]
```

### 2.2.2 Law Synchronization Protocol

**Pull Schedule:** The Governor pulls Tier 4 Law packages on a configurable schedule (default: every 6 hours).

**Sync Process:**
1. **Query Federal Library:** List and download packages from Federal Instances.
2. **Version Comparison:** Compare incoming package versions against local cache.
3. **Download & Verify:** Download tarballs, verify signatures, and apply CRDs with label `tier: "4"`.

**Conflict Resolution:**
If two Federal Instances publish conflicting Law, the Governor rejects the sync and emits an alert. Manual resolution is required.

**Propagation to Sibling Flows:**
After syncing, the Governor updates the **State Library Snapshot** (a canonical tar.gz of all Tier 3 + Tier 4 CRDs). Sibling Operators poll this snapshot periodically.
