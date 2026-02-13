# Atomic Foundry Flow: Cross-Flow Collaboration

## 1. Overview

This document specifies how independent Foundry Flows collaborate without violating sovereignty. It defines the **Data Gravity** principle, the **Treaty** trust model, and the **Export/Import** interchange pattern.

**Key Principle:** Workitems are immutable residents of their namespace. Cross-flow operations use **Copy-on-Write** semantics.

---

## 2. Data Gravity

### 2.1 The Principle

A `Workitem` CRD is an **immutable resident** of its namespace. Cross-flow collaboration uses Copy-on-Write semantics.

**Why:**
- **Sovereignty:** Each Flow is a self-contained operating system. Its state (CRDs) belongs to it alone.
- **Consistency:** No distributed transactions across namespace boundaries. No two-phase commits.
- **Auditability:** The complete history of a Workitem exists in one place.

### 2.2 Cross-Flow Collaboration Model

Cross-Flow collaboration follows an **Export → Transfer → Import** lifecycle:

```
      [Flow A]                      [Flow B]
   (Sovereign Realm)             (Sovereign Realm)
          │                             │
    [Export Node] ──(HTTPS/gRPC)──▶ [Ingress Node]
          │                             │
     Sign(Root A)                  Verify(Treaty A)
          │                        Stamp(Root B)
          │                             │
     [Completed]                   [New Workitem]
```

| Phase | Location | Action |
|-------|----------|--------|
| **Export** | Flow A | Terminal Node bundles workitem + artefacts into a signed Foundry Bundle |
| **Transfer** | Network | Bundle transmitted via HTTPS/gRPC to target Ingress endpoint |
| **Import** | Flow B | Ingress Node validates signature, creates new local Workitem, naturalizes artefacts |

**Outcome:**
- **Flow A:** Original Workitem is `Completed` (archived, immutable)
- **Flow B:** New Workitem is `Created` (independent lifecycle begins)
- **State:** Data exists in only one "Active" state at any given moment

### 2.3 Supported Patterns

| Pattern | Mechanism |
|---------|-----------|
| Cross-flow collaboration | Export/Import via signed bundles |
| Trust establishment | Treaty (Atomic) or State Root (Federated) |
| Transport | Direct Node-to-Node HTTPS/gRPC |
| Identity | Governor provides CA hierarchy |
