# Atomic Foundry Flow: System Services Overview

## 1. System Services

System Services are the internal components that power the Flow. Each service belongs to a specific plane:

| Service | Plane | Purpose |
|---------|-------|---------|
| **Librarian** | Governance | Law lifecycle, embedding, conflict detection |
| **Law Search** | Governance | Horizontally scalable law query service |
| **Citation Processor** | Governance | Track law usage, trigger promotions |
| **Flow Monitor** | Control | Telemetry aggregation, metrics, audit logging, friction |
| **Archivist** | Data | Artefact blob storage |
| **Backup Service** | Data | Database backup for Librarian and Archivist |
| **Flow Operator** | Control | Routing, state machine, guards |

See the following documents for detailed specifications:
- [02a_librarian.md](02a_librarian.md) - Law lifecycle and embedding
- [02b_law_search.md](02b_law_search.md) - Horizontally scalable query service
- [02c_archivist.md](02c_archivist.md) - Artefact storage backend
- [02e_backup_service.md](02e_backup_service.md) - Database backup service
- [04_flow_monitor.md](04_flow_monitor.md) - Telemetry, metrics, audit logging, and friction

---

## 2. The Assay Node (Governance Plane)

> **Plane:** Governance (Judicial)

A standard `FoundryNode` configured with the **Judiciary** role. It resolves deadlocks and disputes by examining `disputed` feedback items. It uses a Jury Mechanism (parallel `FoundryAgents`) to render binding verdicts via multi-turn deliberative consensus. Verdicts are recorded as Tier 2 Rulings using the `WRITE:law/tier2` capability.

See [09_assay_node.md](./09_assay_node.md) for the full specification.

### 2.1 Routing to Assay

Sort Nodes detect semantic fatigue (via feedback history depth), mark blocking feedback items as `disputed`, then route to Assay via `RouteToOutput("deadlock")`. The Assay Node derives the dispute context by filtering `workitem.status.feedback` for items with `state == "disputed"` and uses the `history` array to understand the full timeline of the dispute.

### 2.2 Assay Outputs

```yaml
kind: FoundryNode
metadata:
  name: assay-node
spec:
  roles: ["judiciary"]
  timeout: "120s"
  capabilities:
    - "READ:workitem"
    - "READ:law"
    - "WRITE:law/tier2"           # Authority to mint Rulings
    - "ESCALATE:governance"
  outputs:
    - name: "resolved"
      target: "$sender"           # Return to sender (Sort node) with ruling
    - name: "escalate"
      target: "hitl-node"         # Hung jury - human intervention required
```

The Operator resolves `$sender` to `workitem.status.previousAssignee` at routing time.
