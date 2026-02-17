# Atomic Foundry Flow: Architecture Concepts

## 1. Executive Summary

The **Foundry Flow** is a self-contained operating system for governed work. It runs entirely within a single Kubernetes Namespace and functions as a self-contained unit, using local `etcd` CRDs for state and pluggable storage for artefacts.

* **State:** Workflow state (Laws, Workitems, Passport Stamps) is stored as Kubernetes **Custom Resources (CRDs)**.
* **Storage:** Artefact content is offloaded to **The Archivist**, with pluggable backends (local PVC or cloud object storage like S3/GCS).
* **Topology:** The **Namespace** defines the Flow boundaries. **One namespace = one Flow** (singleton).
* **Logic:** A **Flow Operator** acts as a generic **State Router** and **Terminal Guard**.

---

## 2. Six-Plane Architecture

Foundry Flow separates concerns into six distinct planes:

```
┌─────────────────────────────────────────────────────────────────────────────-┐
│                           MANAGEMENT PLANE                                   │
│        Helm · CRDs · Prometheus · Grafana · Retention Policies               │
├─────────────────────────────────────────────────────────────────────────────-┤
│   ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────────────┐   │
│   │  CONTROL PLANE   │  │ GOVERNANCE PLANE │  │    FEDERATION PLANE      │   │
│   │  Flow Operator   │  │  Librarian       │  │  Governance Operator     │   │
│   │  Flow Monitor│  │  Citation Proc.  │  │  Treaties                │   │
│   │  Thrash Guard    │  │  Assay Node      │  │  Export/Import           │   │
│   └────────┬─────────┘  └────────┬─────────┘  └────────────┬─────────────┘   │
│   ┌────────┴─────────────────────┴─────────────────────────┴─────────────┐   │
│   │                         SECURITY PLANE                               │   │
│   │    Sidecar · ServiceAccount Tokens · mTLS Certs · Passport Stamps    │   │
│   └──────────────────────────────┬───────────────────────────────────────┘   │
│   ┌──────────────────────────────┴───────────────────────────────────────┐   │
│   │                           DATA PLANE                                 │   │
│   │              Nodes · Archivist · Artefacts · Feedback                │   │
│   └──────────────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────────────-┘
```

### 2.1 Plane Definitions

| Plane | Purpose | Key Components |
|-------|---------|----------------|
| **Management** | Configuration, lifecycle, observability | Helm, CRDs, Prometheus |
| **Control** | Work assignment, routing decisions | Flow Operator, Flow Monitor |
| **Data** | Work execution, artefact storage | Nodes, Archivist |
| **Security** | Identity, auth, cryptographic trust | Sidecar, tokens/certs, stamps |
| **Governance** ⭐ | Legal lifecycle, precedent, resistance | Librarian, Assay, Hearings |
| **Federation** | Cross-flow trust, collaboration | Governor, Treaties, Export/Import |

### 2.2 The Governance Plane: Foundry's Differentiator

Standard workflow systems provide Management, Control, Data, and Security planes. **The Governance Plane is what makes Foundry a governed runtime.**

- **Laws are discovered**, not just configured. Tier 1 Findings emerge from work.
- **Precedent accumulates**. The Citation Processor tracks which laws are useful.
- **Constitutional resistance** is measurable. The Flow Monitor quantifies governance "pain."
- **Judicial review** is built-in. The Assay Node deliberates disputes with jury profiles.

---

## 3. Implementation Decisions (v1)

| Decision | Choice | Rationale |
|----------|--------|----------|
| **Language** | Go | Standard for K8s operators |
| **Federation** | Atomic only | Multi-flow trust deferred to v2 |
| **Persistence** | Hybrid | etcd for control plane, SQLite for query engine |
| **CRD Types** | Hand-written Go structs | Kubebuilder annotations |

### 3.1 v1 vs v2 Roadmap

**v1 (Current):** Single-namespace, federation-agnostic:
- One namespace = one isolated Foundry Flow
- Security relies on Kubernetes ServiceAccount tokens
- No inter-flow trust relationships

**v2 (Planned):** Governance Operator as trust bridge:
- Governor-issued mTLS certificates for Nodes
- Tier 4 Federal Law sync
- Cross-flow signature validation

### 3.2 Persistence Layer Split

| Layer | Technology | Data |
|-------|------------|------|
| **Control Plane** | etcd (CRDs) | Workitems, Laws, FoundryFlow, FoundryNode |
| **Query Engine** | SQLite (sqlite-vec) | Embeddings, citation_ledger, friction_ledger |
| **Blob Store** | PVC (Archivist) | Artefact content (bytes) |

### 3.3 Sequential Processing Model

**The Flow is strictly sequential at the orchestration level.** A Workitem can only be assigned to ONE node at a time.

**Why:**
- `Workitem.status.currentAssignee` is a scalar string, not an array
- Atomic state ownership prevents race conditions
- The Operator's routing loop is linear

**Handling Parallel Work Requirements:**

| Pattern | Description |
|---------|-------------|
| **Fat Node** | Single node uses internal concurrency to query multiple services |
| **Deterministic Chain** | SecurityNode → LegalNode → BrandNode (serialized) |

**Anti-Pattern:** Do NOT use `EXPORT` for fan-out. Export creates a new Workitem lifecycle.

**The Flow is a relay race, not a scrum. One baton, one runner.**

---

## 4. Related Documents

- [00a_architecture_configuration.md](./00a_architecture_configuration.md) - Bootstrap, cross-flow collaboration, FoundryFlow CRD
