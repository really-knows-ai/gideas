# System Services: The Librarian

> **Plane:** Governance ⭐

The sole authority for "Legal Lifecycle Management." It maintains the `citation_ledger` and `active_laws` database. It handles law lifecycle via the **Unified Review Loop**.

## 1. Review Hearing Triggers

The Librarian runs continuous scans to detect laws requiring review:

```go
// Expiry Scanner (runs every hour)
SELECT * FROM active_laws 
WHERE expires_at < (NOW() + interval '72 hours')
  AND state != 'hearing_pending'
  AND tier IN ('1', '2');

// Citation Scanner (runs on each citation increment)
// Checks if law crossed threshold AND isn't already in hearing
```

**On Trigger:**
1. Sets `law.state = "hearing_pending"` to prevent duplicate hearings
2. Creates `ReviewHearing` Workitem with context (law_id, tier, reason, citation_count)
3. Routes to Assay Node for binary verdict

## 2. Smart Promotion (Consolidation)

When a Tier 1 Finding (`f-101`) triggers a ReviewHearing due to popularity, the Librarian bundles similar laws:

1. **Search:** Performs a semantic search for other Tier 1 Findings with similarity > `0.85`.
2. **Bundle:** Creates `ReviewHearing` Workitem containing `f-101` and all similar candidates (e.g., `f-102`, `f-105`).
3. **Instruction:** "Review these related findings. Synthesize them into a single authoritative Tier 2 Ruling."
4. **Result:** If "Promote" verdict, the Assay Node mints one Ruling; the Librarian retires the entire bundle as `superseded`.

## 3. Scaling & Availability (Strict Singleton)

The Librarian is a **Strict Singleton**. Only one replica may be active (`replicas: 1`).

| Constraint | Reason |
|------------|--------|
| Single Writer | `sequence_id` ordering requires atomic increment from single source |
| Merkle Integrity | `state_merkle_root` calculation must be deterministic and sequential |
| Split-Brain Prevention | Multiple writers would corrupt the immutable legal ledger |

### 3.1 High Availability: Fail-Fast and Fast Restart

HA is achieved via a single replica with a PersistentVolumeClaim and a **fast restart** recovery model.

#### 3.1.1 Fail-Fast Behavior

If the Librarian is unavailable, all nodes that depend on it MUST **fail fast**. Fresh data is required for legal and compliance workflows where outdated law data poses compliance risk.

- **Node Health Checks:** Before processing a workitem, nodes should perform a health check against the Librarian. If it fails, the node should refuse to process the workitem and return an error.
- **SDK Behavior:** The `searchLibrary()` and other legal operations in the SDK will return a `SERVICE_UNAVAILABLE` error if the Librarian cannot be reached.

#### 3.1.2 Fast Restart Requirement

To minimize downtime, the Librarian implementation MUST be optimized for a fast restart. The target is **<10 seconds from pod start to serving traffic**.

This includes:
- **Minimal boot-time initialization:** Defer non-essential tasks until after the service is live.
- **Optimized database connection:** Use connection pooling and fast schema validation.
- **Efficient catch-up mode:** The process of replaying mutation logs from the Archivist must be highly optimized.

## 4. WAL Publisher & Sequence Authority

The Librarian operates in Write-Ahead Log (WAL) mode. Every 60 minutes, it exports a "Cold Snapshot" (a `sqlite-vec` file) to the Archivist. For every state mutation (Create/Update/Delete/Promote), it:

1. Commits to local SQLite.
2. Assigns a strictly increasing `sequence_id` (int64) and a `state_merkle_root` (SHA256).
3. Emits a `foundry.system.legal_update` telemetry event (Hot Path).
4. Pushes the mutation log to the Archivist (Cold Path).

---

## 5. Embedding Pipeline

The embedding strategy differs by law tier to balance **consistency** (Tier 2/3) with **availability** (Tier 1).

| Tier | Strategy | Conflict Detection | Rationale |
|------|----------|-------------------|-----------|
| **Tier 1 (Finding)** | **Asynchronous** | Post-commit | High volume, ephemeral; availability > consistency |
| **Tier 2 (Ruling)** | **Synchronous** | Pre-commit | Binding precedent; must guarantee uniqueness |
| **Tier 3 (Statute)** | **Synchronous** | Pre-commit | Organizational policy; must guarantee uniqueness |

**Configuration (FoundryFlow):**
```yaml
embeddingConfig:
  provider: "ollama"              # "ollama", "openai", "azure", etc.
  endpoint: "http://ollama:11434" # Provider endpoint
  model: "nomic-embed-text"       # Provider-specific model name
  dimensions: 768                 # Must match model output
  conflictThreshold: 0.85         # Similarity score above this = potential conflict
  tier1BatchSize: 50              # Batch size for async embedding worker
  tier1RetryBackoff: "30s"        # Backoff for failed embeddings
```

### 5.1 Tier 1 Finding Flow (Asynchronous - Write Availability)

Tier 1 Findings prioritize **write availability** over immediate conflict detection. The Node gets immediate success; embedding and deduplication happen in the background.

```
Node calls RecordFinding(statement, labels)
        │
        ▼
┌─────────────────────────────────────────────────────────────┐
│ Librarian (Fast Path)                                       │
│ 1. Validate statement length and labels                     │  ~1ms
│ 2. Generate law_id                                          │
│ 3. INSERT into active_laws with embedding_status='PENDING'  │  ~5ms
│ 4. Emit legal_update event (without embedding)              │
│ 5. Return law_id immediately (SUCCESS)                      │
└─────────────────────────────────────────────────────────────┘
        │
        ▼ (Background, async)
┌─────────────────────────────────────────────────────────────┐
│ Embedding Worker (Slow Path)                                │
│ 1. SELECT * FROM active_laws WHERE embedding_status='PENDING' │
│ 2. Batch call embedding provider                            │  ~20-100ms
│ 3. For each embedded law:                                   │
│    a. Search for conflicts (cosine > threshold)             │
│    b. If conflict: SET dedup_flag='DUPLICATE', link to master│
│    c. If no conflict: SET embedding_status='READY'          │
│ 4. Emit legal_update events for indexed laws                │
└─────────────────────────────────────────────────────────────┘
```

**Implications:**
- **No Immediate Conflict Signal:** Node does NOT receive `CONFLICT_DETECTED` for Tier 1. Duplicates are flagged asynchronously.
- **Eventual Consistency:** New Tier 1 laws are invisible to `SearchLibrary` until `embedding_status='READY'`.
- **Deduplication:** Duplicate Tier 1 Findings are marked `dedup_flag='DUPLICATE'` with a link to the "master" law.

**Embedding Worker Resilience:**

| Event | Behavior |
|-------|----------|
| Provider unavailable | Retry with exponential backoff (30s, 60s, 120s, max 5m) |
| Provider rate-limited | Reduce batch size, apply jitter |
| Embedding fails 5× | Mark `embedding_status='FAILED'`, emit alert metric |
| Librarian restart | Worker resumes from `WHERE embedding_status='PENDING'` |

### 5.2 Tier 2/3 Ruling Flow (Synchronous - Write Consistency)

Tier 2 Rulings and Tier 3 Statutes are **binding precedent**. They MUST be unique before commit.

```
Node calls RecordRuling(statement, labels) or Librarian promotes Tier 1
        │
        ▼
┌─────────────────────────────────────────────────────────────┐
│ Librarian (Blocking)                                        │
│ 1. Call embedding provider: embed(statement)                │  ~20-50ms
│ 2. Search sqlite-vec for similar laws (cosine similarity)   │  ~5-20ms
│    - SCOPED BY LABELS (see Label-Scoped Conflict Detection) │
│ 3. If any result > conflictThreshold:                       │
│    - Return CONFLICT_DETECTED with candidate laws           │
│    - Law NOT created                                        │
│ 4. If no conflicts:                                         │
│    - Store law + embedding in sqlite-vec                    │
│    - SET embedding_status='READY'                           │
│    - Assign sequence_id, update Merkle root                 │
│    - Emit legal_update event                                │
│    - Return success                                         │
└─────────────────────────────────────────────────────────────┘
```

**Provider Failure (Tier 2/3):** If the embedding provider is unavailable, `RecordRuling` returns `UNAVAILABLE`. The Node must retry or fail the Workitem. There is no async fallback for binding laws.

### 5.3 Label-Scoped Conflict Detection (Multi-Context Flows)

Conflict detection is **scoped by the `applies-to` label** to prevent false positives across different contexts.

**Problem:** "Always use strict typing" for Python and "Always use strict typing" for TypeScript have identical semantic embeddings but are distinct laws for different contexts.

**Solution:** The conflict query filters candidates by matching `applies-to` labels:

```sql
-- Conflict search (pseudo-SQL)
SELECT law_id, statement, cosine_similarity(embedding, ?) as score
FROM law_embeddings
WHERE (applies_to IS NULL OR applies_to = ?)  -- Scope to same context
  AND score > 0.85
ORDER BY score DESC
```

**Label Matching Rules:**

| New Law `applies-to` | Existing Law `applies-to` | Conflict Check? |
|----------------------|---------------------------|-----------------|
| `python` | `python` | ✅ Yes - same context |
| `python` | `javascript` | ❌ No - disjoint contexts |
| `python` | (none) | ✅ Yes - global law applies to all |
| (none) | `python` | ✅ Yes - global law must not conflict with specific |
| (none) | (none) | ✅ Yes - both global |

### 5.4 Conflict Handling (Hard Rejection)

Conflict detection is a **hard rejection**. Semantically similar content (85%+ similarity) must cite the existing law. The system enforces this to prevent governance signal dilution across duplicate records.

**Error Response:**
```protobuf
message ConflictDetails {
  repeated CandidateLaw candidates = 1;
}

message CandidateLaw {
  string law_id = 1;
  string statement = 2;
  float similarity_score = 3;
}
```

**Expected Node Behavior:**

When `RecordFinding` returns `CONFLICT_DETECTED`, the Node should pivot to citation:

```go
lawID, err := n.RecordFinding(ctx, statement, labels)
if foundry.IsConflict(err) {
    // Extract conflicting laws from error details
    candidates := foundry.GetConflictCandidates(err)
    
    // Cite the most similar existing law instead
    bestMatch := candidates[0]  // Sorted by similarity
    n.Cite(ctx, bestMatch.LawID)
    
    // Continue processing - the "wisdom" already exists
    return RouteToOutput("default")
}
```

**Design Rationale:** Creating near-duplicate laws dilutes governance signal across multiple records. Citing the existing law pushes it toward the promotion threshold (Tier 2 Ruling), reinforcing consensus rather than fragmenting it.

---

## 6. Law Mutation Operations

The Librarian supports atomic law lifecycle mutations:

```typescript
// Sustain: Extend lease for Tier 2 Ruling
interface SustainRulingRequest {
  law_id: string;
  new_expires: string;  // ISO8601 or relative like "+90d"
}
// Result: expiresAt updated, law remains Tier 2

// Demotion: Soft Landing for expired Tier 2 Rulings
interface DemoteLawRequest {
  law_id: string;
  reason: "retirement_hearing_verdict";
}
// Result: tier label changed to "1", expiresAt reset to +30 days

// Retirement: Mark laws as superseded/absorbed
interface RetireLawRequest {
  law_id: string;
  reason: "superseded" | "absorbed" | "constitutional_violation";
  replaced_by?: string;  // ID of the law that replaces this one
}
// Result: Law marked as inactive, removed from search index

// Amendment: Update existing Ruling without changing ID
interface AmendRulingRequest {
  law_id: string;
  new_type: string;      // Optional: if changing content type
  new_content: string;   // New content payload
  reason: string;
}
// Result: Content updated, version incremented, embedding regenerated

// Batch Retirement: Consolidation cleanup
interface SupersedeMultipleRequest {
  victim_ids: string[];       // Laws to retire
  new_master_id: string;      // The consolidated ruling that replaces them
}
// Result: Atomic batch update, single legal_update event
```

All mutations update the `sequence_id`, recalculate `state_merkle_root`, and emit `foundry.system.legal_update` events.

---

## 6.1 Content Validation by Type

The Librarian validates that `spec.content` matches the expected format for the declared `spec.type`. This prevents malformed laws from entering the corpus.

### 6.1.1 Validation Pipeline

```go
func (l *Librarian) validateContent(content string, mimeType string) error {
    switch mimeType {
    case "text/markdown":
        return validateMarkdown(content)
    case "application/smt-lib":
        return validateSMTLib(content)
    case "application/python":
        return validatePython(content)
    default:
        return fmt.Errorf("unsupported content type: %s", mimeType)
    }
}
```

### 6.1.2 Type-Specific Validators

**text/markdown:**
- Validates basic markdown syntax
- Checks for balanced brackets/quotes
- Ensures content length ≤ 8192 chars
- Rejects script injection attempts

**application/smt-lib:**
- Parses SMT-LIB 2.6 grammar
- Validates:
  - `(declare-const ...)`
  - `(assert ...)`
  - `(check-sat)`
- Ensures only supported solvable fragments used
- Rejects undefined symbols

**application/python:**
- Uses `ast.parse()` to validate Python AST
- Checks for:
  - No `import`, `exec`, `eval` statements
  - Only function definitions
  - Returns boolean or validation result
- Rejected code patterns: `__import__`, `open()`, `subprocess`

### 6.1.3 Error Response

```protobuf
message ContentValidationError {
    string error_type = 1;  // "INVALID_MARKDOWN", "INVALID_SMT_LIB", "INVALID_PYTHON"
    string details = 2;     // Line number or syntax error details
}
```

### 6.1.4 Validation Enforcement

| Operation | Validation Point | Failure Action |
|-----------|-----------------|----------------|
| `RecordFinding` | Pre-insert | Return `INVALID_ARGUMENT` with `CONTENT_INVALID` |
| `RecordRuling` | Pre-insert | Return `INVALID_ARGUMENT` with `CONTENT_INVALID` |
| `AmendRuling` | Pre-update | Return `INVALID_ARGUMENT` with `CONTENT_INVALID` |

---

## 7. Storage Schema

Embeddings stored in sqlite-vec alongside law metadata:

```sql
CREATE TABLE active_laws (
  law_id TEXT PRIMARY KEY,
  tier INTEGER NOT NULL,           -- 1=Finding, 2=Ruling, 3=Statute
  type TEXT NOT NULL,              -- MIME type: text/markdown, application/smt-lib, etc.
  content TEXT NOT NULL,           -- The actual payload/code (replaces 'statement')
  labels_json TEXT,
  group_id TEXT,                   -- Optional grouping ID (e.g., lg-7729)
  applies_to TEXT,                 -- Target context (e.g., python, typescript)
  embedding_status TEXT DEFAULT 'PENDING',  -- PENDING, READY, FAILED
  dedup_flag TEXT,                 -- NULL or 'DUPLICATE'
  dedup_master_id TEXT,            -- If duplicate, points to canonical law
  expires_at TIMESTAMP,
  citation_count INTEGER DEFAULT 0,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  sequence_id INTEGER,
  state TEXT DEFAULT 'active'
);

CREATE VIRTUAL TABLE law_embeddings USING vec0(
  law_id TEXT PRIMARY KEY,
  embedding FLOAT[768]  -- Dimensions from config
);

CREATE INDEX idx_embedding_status ON active_laws(embedding_status);
CREATE INDEX idx_tier ON active_laws(tier);
CREATE INDEX idx_group ON active_laws(group_id);  -- NEW: Support group queries
CREATE INDEX idx_type ON active_laws(type);      -- NEW: Support type filtering
CREATE INDEX idx_applies_to ON active_laws(applies_to);  -- NEW: Support context filtering
```

**Embedding Status Values:**

| Status | Meaning | SearchLibrary Visibility |
|--------|---------|-------------------------|
| `PENDING` | Awaiting background embedding | ❌ Invisible |
| `READY` | Embedded and indexed | ✅ Visible |
| `FAILED` | Embedding failed after retries | ❌ Invisible (requires investigation) |

**Cold Snapshots:**
The sqlite-vec file (including embeddings) is exported to Archivist hourly. Law Search nodes boot from this snapshot.


---

## 8. Law Lifecycle State Machine

### 8.1 States

| State | Description |
|-------|-------------|
| `active` | Law is in force and visible to `SearchLibrary` queries |
| `hearing_pending` | Law is under review (promotion, expiry, or challenge hearing) |
| `superseded` | Law has been replaced by a newer, consolidated law |
| `retired` | Law has been explicitly retired (constitutional violation, obsolescence) |

### 8.2 State Machine Diagram

```
                                   ┌──────────────────────────────────────────┐
                                   │                                          │
                                   │         ┌─────────────┐                  │
                                   │         │             │                  │
┌─────────────┐   promote()        │    ┌───▶│  superseded │ (Terminal)       │
│             │────────────────────┼────┘    │             │                  │
│   active    │                    │         └─────────────┘                  │
│             │◀───────────────────┤                                          │
└──────┬──────┘   sustain()        │         ┌─────────────┐                  │
       │          demote()         │         │             │                  │
       │                           └────────▶│   retired   │ (Terminal)       │
       │                                     │             │                  │
       │ trigger_hearing()                   └─────────────┘                  │
       │                                           ▲                          │
       ▼                                           │                          │
┌──────────────┐                                   │ retire()                 │
│              │───────────────────────────────────┘                          │
│   hearing_   │                                                              │
│   pending    │───────────────────────────────────────────────────────────────
│              │   verdict: sustain/demote → back to active
└──────────────┘
```

### 8.3 State Transitions

| From | To | Trigger | Guard Conditions | Actions |
|------|-----|---------|------------------|---------|
| `active` | `hearing_pending` | `trigger_hearing()` | Law is Tier 1 or 2; Not already in hearing; Trigger condition met (expiry, citation threshold, challenge) | Create `ReviewHearing` Workitem; Set `state = hearing_pending` |
| `hearing_pending` | `active` | `sustain()` | Hearing verdict is "sustain" | Extend `expires_at`; Reset `state = active`; Emit legal_update |
| `hearing_pending` | `active` | `demote()` | Hearing verdict is "demote"; Law is Tier 2 | Change tier to 1; Reset `expires_at` to +30d; Set `state = active` |
| `hearing_pending` | `superseded` | `promote()` | Hearing verdict is "promote"; Consolidation creates new Tier 2 law | Set `state = superseded`; Set `replaced_by` to new law ID |
| `hearing_pending` | `retired` | `retire()` | Hearing verdict is "retire" | Set `state = retired`; Record reason |
| `active` | `superseded` | `supersede()` | Administrative consolidation (batch retirement) | Set `state = superseded`; Set `replaced_by` |
| `active` | `retired` | `retire()` | Constitutional violation; Obsolescence | Set `state = retired`; Record reason |

### 8.4 Hearing Triggers

| Trigger | Condition | Hearing Type |
|---------|-----------|--------------|
| Citation Threshold | `citation_count >= populusThreshold` (default: 50) | Promotion Review |
| Expiry Warning | `expires_at < NOW() + 72 hours` | Renewal Review |
| Challenge | Node explicitly challenges via `ChallengeLaw()` | Challenge Review |
| Conflict | New law conflicts with existing law | Conflict Resolution |

### 8.5 Terminal States

Both `superseded` and `retired` are terminal states. Laws in these states are removed from the active search index but retained in the ledger for audit purposes. The `replaced_by` field in superseded laws enables traceability to the successor law.

### 8.6 Tier Progression

Laws can progress through tiers via the hearing process:

```
Tier 1 (Finding) ──promote()──▶ Tier 2 (Ruling) ──codify()──▶ Tier 3 (Statute)
       │                              │
       │                              │ demote()
       │                              ▼
       │◀─────────────────────────────┘
```

Tier 3 Statutes are organizational policy and do not expire. They can only be retired through explicit administrative action.


---

## 9. Backup Integration

The Librarian integrates with the central **Backup Service** to provide consistent, live backups of its sqlite-vec database.

### 9.1 Backup Interface

The Librarian implements the `BackupSource` gRPC service defined in `proto/backup.proto`. When the Backup Service calls `StreamDatabaseSnapshot`, the Librarian uses the SQLite Online Backup API to create a consistent snapshot of its database and streams it back in chunks.

This mechanism allows backups to be taken while the Librarian is actively serving requests, with no service interruption.

### 9.2 Restoration

In a disaster recovery scenario, the Librarian database is restored by the cluster administrator using the following procedure:

1. Retrieve the desired snapshot from the Backup Service's storage destination.
2. Scale the Librarian deployment to 0 replicas.
3. Copy the snapshot file to the Librarian's PVC, replacing the existing database file.
4. Scale the Librarian deployment back to 1 replica.

The Librarian will start with the restored state. Any laws created after the snapshot was taken will be lost and must be re-created by the originating nodes.
