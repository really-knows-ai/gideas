# Cross-Flow Collaboration: Import Operation

## 1. Overview

`Import()` is an **atomic ingestion primitive**. It unpacks a Foundry Bundle, validates its origin against configured Treaties, and creates a new local Workitem with naturalized artefacts.

## 2. Import Steps

`Import(ctx, bundle)` performs the following atomically:

| Step | Action | Failure |
|------|--------|---------|
| 1. **Unpack** | Extract bundle contents | `BUNDLE_CORRUPT` |
| 2. **Validate Integrity** | Verify manifest hash matches artefact hashes | `HASH_MISMATCH` |
| 3. **Validate Signature** | Check signature against Treaty CA | `TREATY_VIOLATION` |
| 4. **Check Subject** | Verify signer CN is in `allowedSubjects` | `UNAUTHORIZED_SUBJECT` |
| 5. **Check Duplicate** | Reject if `sourceFlow:sourceWorkitemId` already imported | `ALREADY_IMPORTED` |
| 6. **Create Workitem** | Create new Workitem CRD with foreign reference | - |
| 7. **Store Artefacts** | Store all artefacts to local Archivist | - |
| 8. **Stamp Artefacts** | Apply Ingress Node's "Naturalization Stamp" to all artefacts | - |
| 9. **Record Import** | Add entry to Import Ledger | - |
| 10. **Return** | Return new local Workitem ID | - |

## 3. Import Deduplication (Import Ledger)

The Operator maintains a persistent **Import Ledger** to prevent duplicate imports.

**Key:** Composite of `sourceFlow` + `sourceWorkitemId` from the bundle manifest.

**Behavior:**
- Before creating a new Workitem, the Sidecar queries the ledger
- If a matching entry exists → immediate rejection with `ALREADY_IMPORTED`
- If no match → proceed with import, then record the entry

**Ledger Schema (Operator SQLite):**

```sql
CREATE TABLE import_ledger (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    source_flow         TEXT NOT NULL,
    source_workitem_id  TEXT NOT NULL,
    local_workitem_id   TEXT NOT NULL,
    imported_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    ingress_node        TEXT NOT NULL,
    bundle_hash         TEXT NOT NULL,
    
    UNIQUE(source_flow, source_workitem_id)
);

CREATE INDEX idx_ledger_source ON import_ledger(source_flow, source_workitem_id);
```

**Why Operator-Level:**
- Shared across all Ingress Nodes (prevents race conditions)
- Survives Ingress Node restarts
- Enables audit queries ("what have we imported from Flow X?")

## 4. Chain of Custody Reset (Naturalization)

When a bundle is imported, the **foreign stamps are cryptographically ignored**:

- **Reason:** The receiving Flow cannot verify signatures from an unknown Root CA
- **Effect:** Artefacts enter the new Flow "naked" of valid authority
- **Solution:** The Ingress Node applies its own stamp, starting a new chain of custody

**Foreign stamps are preserved** in `Workitem.status.context.foreignProvenance` for audit purposes (human readable), but they do NOT count toward `GovernedArtefact.requiredRoles` (machine validity).

## 5. Trust Policy Patterns

The `GovernedArtefact` configuration determines how much local validation is required:

### Pattern A: "Rubber Stamp" (Trusted Source)

If you completely trust the foreign Flow, the import stamp alone is sufficient:

```yaml
# Ingress Node roles
spec:
  roles: ["importer"]

---
# GovernedArtefact - import stamp = valid
apiVersion: flow.gideas.io/v1
kind: GovernedArtefact
metadata:
  name: imported-report
spec:
  requiredRoles: ["importer"]
```

**Result:** Ingress Node stamps → immediately valid → can flow to Terminal.

### Pattern B: "Border Check" (Distrustful Source)

If you want local re-verification:

```yaml
# Ingress Node roles
spec:
  roles: ["importer"]

---
# GovernedArtefact - requires local security review
apiVersion: flow.gideas.io/v1
kind: GovernedArtefact
metadata:
  name: imported-code
spec:
  requiredRoles:
    - "importer"           # Proves origin
    - "security-reviewer"  # Local verification
```

**Result:** Import stamp proves origin, but artefact is NOT valid until local Security Node stamps it.

## 6. Ingress Node Configuration

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: ingress-from-vendors
spec:
  image: "registry/nodes/ingress:v1.0"
  roles: ["importer"]
  timeout: "30s"
  
  capabilities:
    - "WRITE:artefact/*"
    - "INSPECT:artefact/*"
    - "CREATE:workitem"
  
  outputs:
    - name: "accepted"
      targetRole: "initial-processor"
    - name: "rejected"
      target: "terminal-rejected"
```

## 7. Foreign Reference Linking

The new Workitem includes a reference to its foreign origin:

```yaml
apiVersion: flow.gideas.io/v1
kind: Workitem
metadata:
  name: imported-petition-abc123
spec:
  type: "vendor-submission-v1"
  intent: "Imported: Add dark mode to the dashboard"
status:
  context:
    foreignSource:
      flow: "flow-ideate"
      workitemId: "petition-dark-mode-v1"
      exportedAt: "2026-01-08T14:30:00Z"
      exportNode: "export-node-pod-1"
    foreignProvenance:
      # Original stamps preserved for audit
      petition-draft:
        - { role: "linter", node: "lint-node", timestamp: "..." }
        - { role: "security-reviewer", node: "security-quench", timestamp: "..." }
```
