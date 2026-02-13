# System Services: Law Search

> **Plane:** Governance ⭐

A horizontally scalable service responsible for handling all `SearchLibrary` gRPC calls from Nodes. It operates as a **Paranoid Replica**, ensuring real-time synchronization with the Librarian without the overhead of full database reloads.

## 1. Search Modes

| Mode | Query | Process |
|------|-------|---------|
| Label-only | `{Labels: {"applies-to": "python"}}` | SQL WHERE clause, no embedding |
| Semantic | `{SemanticQuery: "naming conventions"}` | Embed query → sqlite-vec similarity |
| Hybrid | Both fields set | Filter by labels, then rank by similarity |
| **Type-filtered** | `{Type: "application/smt-lib"}` | Filter by MIME type, optionally combined with semantic/label |
| **Group-aware** | `{Group: "lg-7729"}` | Find all laws in a group |

**Semantic Search Flow:**
```
SearchLibrary({SemanticQuery: "variable naming"})
         │
         ▼
 ┌─────────────────────────────────────────────────────────────┐
 │ Law Search                                                  │
 │ 1. Call embedding provider: embed(query)                    │  ~20-50ms
 │ 2. sqlite-vec: SELECT * ORDER BY cosine_similarity DESC     │  ~5-20ms
 │ 3. Return top K results                                     │
 └─────────────────────────────────────────────────────────────┘
```

**Type-Filtered Search Flow:**
```
SearchLibrary({
  Type: "application/smt-lib",
  Labels: {"applies-to": "python"},
  Limit: 10
})
         │
         ▼
 ┌─────────────────────────────────────────────────────────────┐
 │ Law Search                                                  │
 │ 1. WHERE type = 'application/smt-lib'                        │
 │ 2. WHERE applies_to = 'python'                               │
 │ 3. SELECT * ORDER BY ?LIMIT                                  │
 │ 4. Return results (no embedding needed)                      │
 └─────────────────────────────────────────────────────────────┘
```

---

## 2. Hybrid Boot & Drift Guard

1. **Boot (Peer-Assisted Warming):**
   - **Discover Healthy Peer:** On startup, the new replica queries the Headless Service to find a healthy, `SERVING` peer.
   - **Peer Snapshot Stream:** If a healthy peer is found, the new replica initiates a gRPC call to the peer to stream a snapshot of its current `sqlite-vec` database. This is significantly faster than fetching from the Archivist.
   - **Archivist Fallback:** If no healthy peers are available (e.g., on a cold start of the entire service), the replica falls back to fetching the latest "Cold Snapshot" from the Archivist.
2. **Catch-Up:** Calls `Archivist.StreamLogs(since_sequence_id)` to replay mutations missed since the snapshot.
3. **Live:** Subscribes to `foundry.system.legal_update` for real-time updates.
4. **Drift Guard:** Enforces strict `sequence_id` ordering. If `Event.sequence_id != Local.last_sequence_id + 1` or the Merkle Root mismatches, the service **MUST**:
   - Emit a `foundry.system.alert` telemetry event with severity `CRITICAL` before termination.
   - Include payload details: `{ component: "law-search", error: "StateCorruptionError", details: "Merkle Root Mismatch: Expected X, Got Y. Sequence Gap: Local 105, Remote 107." }`.
   - Then throw `StateCorruptionError` and terminate.
   - The orchestrator (e.g., Kubernetes) will restart the pod, triggering the Hybrid Boot Sequence.

**Rationale for "Death Cry" Telemetry:**
- State corruption signals a potential integrity breach (MITM, storage failure, or time-travel attack).
- Relying only on Kubernetes `CrashLoopBackOff` detection hides the *severity* from business logic and telemetry aggregators.
- Emitting a high-severity alert enables the Flow Monitor and external monitoring (e.g., PagerDuty) to trigger immediate escalation rather than generic "pod restarting" warnings.

---

## 3. Readiness-Gated Boot

Law Search uses Kubernetes readiness probes to ensure it never serves stale or incomplete data:

1. **On Boot:** Service starts gRPC server but sets health status to `NOT_SERVING`.
2. **During Catch-Up:** Readiness probe returns `503 Service Unavailable`. Kubernetes removes pod from Service endpoints. Catch-up duration depends on event volume since snapshot (typically seconds to minutes for <10k mutation logs).
3. **After Sync:** Once caught up to the live stream head, flip to `SERVING`. Kubernetes adds pod to endpoints.

**Implications:**
- **Rolling Updates:** Existing replicas continue serving; booting pods are invisible.
- **Cold Start (DR):** No ready pods → `SearchLibrary()` calls timeout. This is intentional: **unavailability is preferred over serving stale/corrupt laws**.
- **Stalled Catch-Up:** If catch-up stalls (network partition, slow Archivist), the pod remains `NOT_SERVING` indefinitely. External monitoring should alert on pods stuck in catch-up for >5 minutes. See Operator SLO recommendations in [06_operational_considerations.md](../governance_spec/06_operational_considerations.md).

**Production Recommendation:** Run **2+ replicas** of Law Search in production. This ensures availability during rolling updates and restarts. Single-replica deployments will experience total law system unavailability during any restart - acceptable for development but not production.

---

## 4. Scaling Behavior (Horizontally Scalable)

Law Search nodes are **Horizontally Scalable**. They operate as read-only replicas consuming the update stream.

| Property | Behavior |
|----------|----------|
| Scaling | Add/remove replicas freely (`replicas: 2+` recommended) |
| State | Each replica maintains independent sqlite-vec copy |
| Consistency | Guaranteed via `sequence_id` ordering and Merkle root validation |
| Load Balancing | Standard Kubernetes Service distributes `SearchLibrary` calls |

**Contrast with Librarian:** Law Search is read-only; it cannot mint, amend, or retire laws. The single-writer constraint applies only to the Librarian.

---

## 5. Vector Indexing for Type and Group

The `sqlite-vec` database indexes the `group` and `type` columns to enable hybrid vector+metadata filtering:

```sql
-- Type and group are indexed for efficient filtering
CREATE INDEX idx_type ON active_laws(type);
CREATE INDEX idx_group ON active_laws(group_id);
CREATE INDEX idx_applies_to ON active_laws(applies_to);
```

**Hybrid Query Example (Type + Semantic):**

```sql
-- Find SMT-LIB laws semantically similar to "no prohibited words"
SELECT law_id, content, type, group_id
FROM active_laws
WHERE type = 'application/smt-lib'
  AND law_id IN (
    SELECT law_id
    FROM law_embeddings
    ORDER BY cosine_similarity(embedding, ?) DESC
    LIMIT 10
  );
```

**Group Query Example:**

```sql
-- Find all laws in group lg-7729
SELECT law_id, type, content
FROM active_laws
WHERE group_id = 'lg-7729'
ORDER BY type;
```

**Type-Only Query Example:**

```sql
-- Find all SMT-LIB laws for Python context
SELECT law_id, content
FROM active_laws
WHERE type = 'application/smt-lib'
  AND applies_to = 'python'
  AND state = 'active';
```
