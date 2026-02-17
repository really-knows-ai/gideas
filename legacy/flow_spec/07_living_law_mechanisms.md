# Atomic Foundry Flow: Living Law Mechanisms

## 4. The "Living Law" Mechanisms

### 4.0 Law Bootstrap (Genesis)

At system startup, the Law Library is empty. Each tier bootstraps differently:

| Tier | Name | Bootstrap Mechanism |
|------|------|---------------------|
| 1 | Finding | Organic discovery via `RecordFinding()` |
| 2 | Ruling | Promoted from Tier 1, or minted by Assay Node |
| 3 | Statute | Genesis Manifests (GitOps) |
| 4 | Federal | Federation sync from upstream |

#### Tier 1 & 2: Organic Discovery

Nodes **cannot** cite non-existent laws. `Cite("non-existent-id")` returns `NOT_FOUND` error.

Laws are discovered via `RecordFinding()`:
1. Node encounters situation requiring a rule (conflict, hallucination, etc.)
2. Node calls `RecordFinding(statement, labels)` → creates Tier 1 Finding
3. Node can now `Cite()` this new law in future turns
4. If enough nodes cite it, Librarian promotes to Tier 2 Ruling

#### Tier 3: Genesis Manifests (The Constitution)

Tier 3 Statutes that must exist *a priori* are seeded via Kubernetes manifests applied during deployment.

```yaml
# helm/laws/statute_001.yaml
apiVersion: flow.gideas.io/v1
kind: Law
metadata:
  name: s-001-human-override
spec:
  type: "text/markdown"
  content: "A Human decision (HITL) always overrides an Agent decision."
  tier: 3
  expiresAt: "2099-01-01T00:00:00Z"  # Effectively immortal
```

**Librarian Foreign Import Logic:**

The Librarian watches `Law` CRDs and handles "foreign imports" (laws created by admin/GitOps):

1. Detects new Tier 3 Law it didn't mint (missing internal provenance)
2. **Adopts** rather than rejects:
   - **Generates vector embedding for the statement (synchronously)**
   - Adds to `active_laws` database
   - Emits `foundry.system.legal_update` to sync Law Search nodes

**Embedding Synchronicity & Timeouts:**
- Embedding is **synchronous**: the Law is not added to the active corpus until the embedding is complete.
- Rationale: Conflict detection is a **hard rejection**. A law cannot be tentatively added and later discovered to conflict; the check must occur before persistence.
- **Implicit Timeout:** No explicit `embeddingTimeout` field, but two timeout layers apply:
  - RPC timeout: The `RecordFinding` RPC inherits the Node handler's context timeout (e.g., 60s). If the embedding provider hangs beyond this, the Sidecar cancels the RPC and returns `DEADLINE_EXCEEDED`.
  - HTTP timeout: Standard Go HTTP clients default to ~30s, preventing indefinite hangs on remote embedding APIs.
- **Scale:** At ~10k laws, vector embedding and conflict detection are trivial (milliseconds). The bottleneck is the remote API call (~20-50ms). Blocking is the correct trade-off for consistency.

**Deployment:** Include genesis manifests in your Helm chart. They're applied before the Flow starts processing workitems.

#### Tier 4: Federation Inheritance

Tier 4 laws are pulled from upstream Federal instances via the Federation Gateway (v2).

---

### 4.2 The Unified Review Loop

All law lifecycle events flow through a single **`ReviewHearing`** process. No law expires without due process.

#### Design Principles

- **No Silent Death:** The Librarian creates a ReviewHearing instead of deleting expired laws
- **Binary Verdict:** Every hearing results in "Status Quo" or "Promote" - the outcome meaning depends on context
- **Unified Triggers:** Expiry and popularity are both questions of *value*, handled by the same mechanism

#### Triggers

The Librarian's scanner creates `ReviewHearing` workitems for two conditions:

| Trigger | Condition | Query |
|---------|-----------|-------|
| **Expiry Warning** | Law approaching expiration | `expiresAt < (Now + reviewWindow)` AND `state != "hearing_pending"` |
| **Popularity** | Citation threshold crossed | `citationCount > threshold` AND `tier < 3` AND `state != "hearing_pending"` |

**ReviewHearing Workitem:**
```yaml
apiVersion: flow.gideas.io/v1
kind: Workitem
metadata:
  name: wi-review-f-101
spec:
  intent: "review_hearing"
  context:
    law_id: "f-101"
    law_content: "Use f-strings for string interpolation"
    law_type: "text/markdown"
    tier: "1"
    reason: "expiry"           # or "popularity"
    citation_count: 47
    expires_at: "2026-01-12T00:00:00Z"
    created_at: "2025-12-10T00:00:00Z"
```

#### Binary Verdict Matrix

The Assay Node renders a binary verdict. The *meaning* of each verdict depends on the law's tier and the trigger:

| Tier | Trigger | Verdict: "Status Quo" | Verdict: "Promote" |
|------|---------|----------------------|-------------------|
| **Tier 1** | **Expiry** | **Expire** - Delete the Finding | **Mint Tier 2** - Promote to Ruling |
| **Tier 1** | **Citation** | **Sustain** - Reset TTL to +30d | **Mint Tier 2** - Promote to Ruling |
| **Tier 2** | **Expiry** | **Demote** - Downgrade to Tier 1 | **Propose Tier 3** - HITL ratification |
| **Tier 2** | **Citation** | **Sustain** - Reset TTL to +90d | **Propose Tier 3** - HITL ratification |

#### Verdict Execution

**Status Quo Outcomes:**

```typescript
// Assay Node verdict execution
if (verdict === "status_quo") {
    switch (context.tier + "_" + context.reason) {
        case "1_expiry":
            // Finding has no value - let it die
            await Node.governance.retireLaw(context.law_id, { reason: "expired" });
            break;
            
        case "1_popularity":
        case "2_popularity":
            // Valuable but not promotion-worthy - sustain
            const ttl = context.tier === "1" ? "+30d" : "+90d";
            await Node.governance.sustainLaw(context.law_id, ttl);
            break;
            
        case "2_expiry":
            // Ruling declining in relevance - soft landing
            await Node.governance.demoteLaw(context.law_id);
            break;
    }
}
```

**Promote Outcomes:**

```typescript
if (verdict === "promote") {
    if (context.tier === "1") {
        // Tier 1 → Tier 2: Automatic promotion
        await Node.governance.promoteFinding(context.law_id, {
            new_content: synthesizedStatement,
            expires: "+90d"
        });
    } else if (context.tier === "2") {
        // Tier 2 → Tier 3: NOT automatic - requires HITL ratification
        await Node.governance.proposeStatute({
            source_ruling_id: context.law_id,
            proposed_content: synthesizedStatement,
            justification: verdict.justification
        });
        // This emits `governance.proposal` event for HITL queue
    }
}
```

#### Tier 3 Promotion (HITL Required)

Promotion to Tier 3 (Statute) is **never automatic**. Citation thresholds (e.g., `tier2ToTier3`) trigger a **ReviewHearing**; the Assay Node may propose a statute, but a human must ratify.

**Flow:**
1. Assay Node renders "Promote" verdict for Tier 2 Ruling
2. `proposeStatute()` creates a Draft Statute and emits `governance.proposal` telemetry
3. Draft routes to HITL node for human review
4. Human ratifies → Librarian mints Tier 3, retires Tier 2 as "absorbed"
5. Human rejects → Tier 2 Ruling sustained with extended TTL

**Rationale:** Statutes are "constitutional" - they auto-retire conflicting lower laws. This power requires human judgment.

### 4.3 The Conflict Loop (Judicial Review)

When the Assay Node is adjudicating a dispute (via feedback escalation, NOT during review hearings) and discovers conflicting laws in the arguments/citations:

* **Constitutional Defense:** If a Tier 1 or Tier 2 law conflicts with a Tier 3 (Statute) or Tier 4 (Federal) law, the lower-tier law is **Retired Immediately**. No hearing is required to save it; it is invalid code.
* **Civil War (Tier 1 vs Tier 1):** The Jury selects the superior logic. The winner is promoted/sustained; the loser is **Retired (Superseded)**.
* **Clarification (Tier 1 vs Tier 2):** The Tier 2 Ruling is **Amended** to include the nuance from the Tier 1 Finding. The Tier 1 Finding is **Retired (Absorbed)**.

### 4.4 The Propagation Loop (The Nerve Signal)
To ensure consistency across the distributed Law Search nodes, the system employs a **Sequenced Stream** mechanism.

#### Sequence ID & Merkle Root
Every mutation to the Law state is assigned a strictly increasing `sequence_id` (int64). Alongside this, the Librarian calculates a `state_merkle_root` (SHA256), which is a hash of all active Law Hashes. This root serves as the "Audit Checksum" for the entire legal corpus.

#### The "Crash on Drift" Policy
Law Search nodes operate as **Paranoid Replicas**. They must process updates in strict sequential order.
* **Integrity Check:** For every received `foundry.system.legal_update`, the node verifies:
  1. `Event.sequence_id == Local.last_sequence_id + 1`
  2. `Event.merkle_root` matches the local calculation after applying the update.
* **Failure Handling:** If a gap is detected or the Merkle Root mismatches, the node throws a `StateCorruptionError` and terminates. The orchestrator (e.g., Kubernetes) will restart the pod, triggering the **Hybrid Boot Sequence** (Snapshot + Log Replay) to restore a known good state.

It is better for a Search Node to be unavailable (restarting) than to be corrupt (serving repealed or inconsistent laws).
