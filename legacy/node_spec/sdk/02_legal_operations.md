# SDK Reference: Legal Operations

> **Version:** 1.0.0  
> **Status:** Implementation Contract

This document defines the legal operations (Law Library) available in the Foundry Node SDK.

---

## 1. RecordFinding

Creates a new Tier 1 Finding in the Law Library.

> **Note:** `RecordFinding` creates markdown-type findings by default. For other content types (e.g., SMT-LIB), use direct Law CRD creation.

### Signature

```go
func (n *NodeContext) RecordFinding(
    ctx context.Context,
    statement string,
    labels map[string]string,
) (string, error)
```

### Parameters

| Name | Type | Required | Constraints | Description |
|------|------|----------|-------------|-------------|
| `statement` | `string` | Yes | Max 1024 chars; non-empty | Natural language law statement (stored as `type: text/markdown`) |
| `labels` | `map[string]string` | No | Max 10 labels; key/value max 64 chars | Metadata for filtering |

### Returns

| Type | Description |
|------|-------------|
| `string` | New Law ID (e.g., `"f-109-use-f-strings"`) |

### Errors

| gRPC Code | Foundry Reason | Condition | Recovery |
|-----------|----------------|-----------|----------|
| `INVALID_ARGUMENT` | `STATEMENT_TOO_LONG` | Statement exceeds 1024 chars | Shorten statement |
| `UNAVAILABLE` | `SERVICE_UNAVAILABLE` | Librarian unreachable | Retry |

> **Note:** `DUPLICATE_FINDING` is NOT returned for Tier 1. Duplicates are detected asynchronously via the Embedding Worker and flagged in the background. See "Eventual Consistency" below.
>
> **Tier Promotion Policy:** Tier 2 → Tier 3 promotion is never automatic. Citation thresholds trigger `ReviewHearing`; Tier 3 requires HITL ratification.

### Behavior (Asynchronous - Write Availability)

Tier 1 Findings prioritize **write availability** over immediate conflict detection:

1. **Validation:** Librarian validates statement length and labels.
2. **ID Generation:** Auto-generates ID from statement (e.g., `f-{seq}-{slug}`).
3. **Write:** Inserts into `active_laws` with `embedding_status='PENDING'`.
4. **Return:** Returns law ID **immediately** (no blocking on embedding provider).
5. **Expiry:** Default 30-day TTL (extended by citations).

**Background Processing:**
- The Embedding Worker picks up pending laws, generates embeddings, and indexes them.
- If a conflict is detected post-commit, the law is marked `dedup_flag='DUPLICATE'`.
- The law becomes visible to `SearchLibrary` only when `embedding_status='READY'`.

### Eventual Consistency Warning

```
┌──────────────────────────────────────────────────────────────────────────────┐
│ ⚠️  TIER 1 FINDINGS ARE EVENTUALLY CONSISTENT                               │
│                                                                              │
│ • RecordFinding() returns SUCCESS immediately (no conflict check)            │
│ • Duplicates may be created during the async window                          │
│ • Duplicates are flagged asynchronously (dedup_flag='DUPLICATE')             │
│ • New findings are invisible to SearchLibrary until indexed                  │
│                                                                              │
│ For immediate conflict detection, use RecordRuling() (Tier 2) which blocks.  │
└──────────────────────────────────────────────────────────────────────────────┘
```

### Example

```go
// RecordFinding returns immediately - no blocking on embedding provider
lawID, err := node.RecordFinding(ctx, 
    "Always use f-strings for string interpolation in Python",
    map[string]string{
        "applies-to": "python",
        "category":   "style",
    },
)
if err != nil {
    // Only infrastructure errors (UNAVAILABLE, INVALID_ARGUMENT)
    return foundry.RouteToOutput("error")
}

// Cite the new law (even though embedding is pending)
err = node.Cite(ctx, lawID)

// Note: The law may be marked as duplicate later by background worker
// This is acceptable for Tier 1 - governance signal consolidates over time
```

---

## 2. SearchLibrary

Queries the Law Library for relevant laws.

### Signature

```go
func (n *NodeContext) SearchLibrary(
    ctx context.Context,
    query *LibraryQuery,
) ([]*Law, error)
```

### Parameters

```go
type LibraryQuery struct {
    SemanticQuery string            // Natural language query (triggers vector search)
    Labels        map[string]string // Exact label match filter
    Tier          int               // Filter by tier (0 = all tiers)
    Type          string            // MIME type filter (e.g., "application/smt-lib", "text/markdown")
    Group         string            // Find all laws in a group (e.g., "lg-7729")
    Limit         int               // Max results (default: 10, max: 100)
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `SemanticQuery` | `string` | No* | Semantic search query |
| `Labels` | `map[string]string` | No* | Label filter (AND logic) |
| `Tier` | `int` | No | 1=Finding, 2=Ruling, 3=Statute, 4=Federal |
| `Type` | `string` | No | MIME type filter (e.g., "application/smt-lib", "text/markdown") |
| `Group` | `string` | No | Group ID filter (finds all laws in group) |
| `Limit` | `int` | No | Result limit |

*At least one of `SemanticQuery`, `Labels`, `Type`, or `Group` must be provided.

### Returns

```go
type Law struct {
    ID            string
    Tier          int
    Type          string            // MIME type (e.g., "text/markdown", "application/smt-lib")
    Content       string            // The actual payload/code
    Group         string            // Optional group ID for linked laws
    AppliesTo     string            // Target context (e.g., "python", "")
    Labels        map[string]string // Additional metadata labels
    ExpiresAt     time.Time
    CitationCount int
    CreatedAt     time.Time
    CreatedBy     string  // Node that created (for Findings)
}
```

### Query Modes

| Mode | Condition | Behavior |
|------|-----------|----------|
| **Label-only** | `SemanticQuery` empty | SQL WHERE on labels |
| **Semantic-only** | `Labels` empty | Vector similarity search |
| **Hybrid** | Both provided | Filter by labels, then rank by similarity |
| **Type-filtered** | `Type` set | Filter by MIME type (e.g., "application/smt-lib") |
| **Group-aware** | `Group` set | Find all laws in a specific group |

### Errors

| gRPC Code | Foundry Reason | Condition | Recovery |
|-----------|----------------|-----------|----------|
| `INVALID_ARGUMENT` | `QUERY_EMPTY` | Neither semantic nor labels provided | Provide at least one query parameter |
| `UNAVAILABLE` | `SERVICE_UNAVAILABLE` | Law Search service unavailable | Retry |

### Example

```go
// Find Python naming conventions
laws, err := node.SearchLibrary(ctx, &foundry.LibraryQuery{
    SemanticQuery: "variable naming conventions",
    Labels: map[string]string{
        "applies-to": "python",
    },
    Tier:  0,  // All tiers
    Limit: 5,
})
if err != nil {
    return foundry.RouteToOutput("error")
}

for _, law := range laws {
    log.Info("Found law", "id", law.ID, "statement", law.Statement)
}
```

### Advanced: Tier-Specific Searches

```go
// Search only Statutes (Tier 3) - organizational standards
statutes, _ := node.SearchLibrary(ctx, &foundry.LibraryQuery{
    SemanticQuery: "error handling",
    Tier:          3,
    Limit:         10,
})

// Search only Rulings (Tier 2) - judicial precedent
rulings, _ := node.SearchLibrary(ctx, &foundry.LibraryQuery{
    Labels: map[string]string{"domain": "security"},
    Tier:   2,
})
```

---

## 3. Cite

Records a citation of a Law for the current Workitem.

### Signature

```go
func (n *NodeContext) Cite(
    ctx context.Context,
    lawID string,
) error
```

### Parameters

| Name | Type | Required | Description |
|------|------|----------|-------------|
| `lawID` | `string` | Yes | Law ID to cite (e.g., `"f-109-use-f-strings"`) |

### Behavior

1. **Validation:** Verifies law exists and is not expired.
2. **Citation Ledger:** Increments citation count for the law.
3. **Telemetry:** Emits `foundry.legal.citation` event.
4. **Lease Extension:** For Tier 1 Findings, resets expiry to +30 days.

### Errors

| gRPC Code | Foundry Reason | Condition | Recovery |
|-----------|----------------|-----------|----------|
| `NOT_FOUND` | `LAW_NOT_FOUND` | Law ID doesn't exist | Use `RecordFinding` to create |
| `FAILED_PRECONDITION` | `LAW_EXPIRED` | Law has expired | Create new Finding |

### Example

```go
// Cite a law that guided our decision
err := node.Cite(ctx, "f-109-use-f-strings")
if err != nil {
    if foundry.IsError(err, foundry.LAW_NOT_FOUND) {
        // Law doesn't exist - create it
        lawID, _ := node.RecordFinding(ctx, 
            "Always use f-strings for string interpolation",
            nil,
        )
        node.Cite(ctx, lawID)
    }
}
```

### Pattern: Search-and-Cite

```go
// Common pattern: search for relevant laws, cite what applies
func applyRelevantLaws(ctx context.Context, node foundry.NodeContext, topic string) error {
    laws, err := node.SearchLibrary(ctx, &foundry.LibraryQuery{
        SemanticQuery: topic,
        Limit:         5,
    })
    if err != nil {
        return err
    }
    
    for _, law := range laws {
        // Cite each law that guided our decision
        if err := node.Cite(ctx, law.ID); err != nil {
            log.Warn("Failed to cite law", "id", law.ID, "err", err)
        }
    }
    return nil
}
```

---

## Node-Specific Guidance

### Quench Node Integration

Quench nodes should query for **deterministic laws** (SMT-LIB) and execute them against the artefact:

```go
type QuenchConfig struct {
    SolverPath string  // Path to Z3 or compatible solver
}

func (n *QuenchNode) CheckCompliance(ctx context.Context, artefact *Artefact) ([]string, error) {
    // Query only SMT-LIB laws for deterministic checks
    laws, err := node.SearchLibrary(ctx, &foundry.LibraryQuery{
        Type:      "application/smt-lib",
        Labels:    map[string]string{"applies-to": artefact.Type},
        Limit:     100,
    })
    if err != nil {
        return nil, err
    }

    var violations []string
    for _, law := range laws {
        // Execute SMT-LIB constraint
        sat, err := n.executeSMTLib(law.Content, artefact.Content)
        if err != nil {
            return nil, fmt.Errorf("SMT-LIB execution failed: %w", err)
        }

        if !sat {
            violations = append(violations, fmt.Sprintf(
                "Law violated: %s (ID: %s)",
                law.Content, law.ID,
            ))
        }
    }

    return violations, nil
}

func (n *QuenchNode) executeSMTLib(smtConstraint string, artefactContent string) (bool, error) {
    // Create SMT-LIB script with artefact content
    smtScript := fmt.Sprintf(`
        (declare-const artefact-content String)
        (assert (= artefact-content "%s"))
        %s
        (check-sat)
    `, escapeSMTString(artefactContent), smtConstraint)

    // Run Z3 solver
    result, err := n.runZ3(smtScript)
    if err != nil {
        return false, err
    }

    // Check result: "sat" = constraint holds, "unsat" = violation
    return result == "sat", nil
}
```

**Quench Pattern:**
- Query `Type = "application/smt-lib"`
- Execute constraints using Z3 or compatible solver
- Return `unsat` results as violations
- Cite all laws checked

### Appraise Node Integration

Appraise nodes should query for **subjective laws** (markdown) and use LLM evaluation:

```go
func (n *AppraiseNode) EvaluateSubjective(ctx context.Context, artefact *Artefact) (*Evaluation, error) {
    // Query only markdown laws for subjective evaluation
    laws, err := node.SearchLibrary(ctx, &foundry.LibraryQuery{
        Type:      "text/markdown",
        Labels:    map[string]string{"applies-to": artefact.Type},
        Limit:     20,
    })
    if err != nil {
        return nil, err
    }

    // Batch laws for LLM evaluation (reduce API calls)
    lawsText := ""
    for _, law := range laws {
        lawsText += fmt.Sprintf("- Law %s: %s\n", law.ID, law.Content)
    }

    // LLM evaluates artefact against subjective rules
    prompt := fmt.Sprintf(`
        Evaluate the following artefact against these subjective laws:

        %s

        Artefact: %s

        Return: JSON array of violations with law_id and explanation.
    `, lawsText, artefact.Content)

    response, err := n.llm.Evaluate(ctx, prompt)
    if err != nil {
        return nil, err
    }

    var violations []Violation
    json.Unmarshal(response, &violations)

    return &Evaluation{Violations: violations}, nil
}
```

**Appraise Pattern:**
- Query `Type = "text/markdown"`
- **Ignore** `Type = "application/*"` (don't waste bandwidth on deterministic laws)
- Use LLM for subjective evaluation
- Cite all laws referenced in evaluation

---

## Law Tier Reference

| Tier | Name | Source | Default TTL | Lifecycle |
|------|------|--------|-------------|-----------|
| 1 | **Finding** | Node via `RecordFinding` | 30 days | ReviewHearing on expiry or popularity |
| 2 | **Ruling** | Assay Node verdict | 90 days | ReviewHearing on expiry or popularity |
| 3 | **Statute** | HITL ratification only | Never | Manual retirement only |
| 4 | **Federal** | Cross-State treaty | Never | Federation protocol |

**Lifecycle Notes:**
- **Tier 1/2 Expiry:** Triggers ReviewHearing → Binary verdict (Status Quo / Promote)
- **Tier 1/2 Popularity:** Citation threshold crossed → ReviewHearing
- **Tier 3 Creation:** Only via HITL ratification of Tier 2 proposal (never automatic)

See [07_living_law_mechanisms.md](../../flow_spec/07_living_law_mechanisms.md#42-the-unified-review-loop) for the complete Unified Review Loop specification.

---

## Constitutional Hierarchy

When laws conflict, higher tiers override lower:

```
Federal (Tier 4)
    ↓ overrides
Statute (Tier 3)
    ↓ overrides
Ruling (Tier 2)
    ↓ overrides
Finding (Tier 1)
```

Nodes must not cite a lower-tier law to justify violating a higher-tier law.

**Note:** Tier 3 Statutes are only created via HITL ratification of Tier 2 proposals. This ensures human oversight for laws that can auto-retire conflicting lower-tier laws.
