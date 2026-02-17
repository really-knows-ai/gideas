# Technical Specification: The Assay Node - Execution & Telemetry

## 1. Phase 4: Execution (The Privileged Step)

The Foreman (Assay Node as Clerk) exercises its `WRITE:law/tier2` capability and records the decision against the disputed feedback.

### 1.1 Mint Law Group (Forking Logic)

When the verdict includes `lawGroup`, the Assay Node mints **multiple linked Laws** instead of a single Tier 2 Ruling. This is called "Forking": the original statement is separated into subjective and deterministic laws sharing a common `group` ID.

```typescript
if (verdict.lawGroup) {
  const lawIds: string[] = [];

  // Mint each law in the group
  for (const lawDef of verdict.lawGroup.laws) {
    const ruling = await Node.governance.mintLawGroupMember({
      type: lawDef.type,
      content: lawDef.content,
      tier: 2,
      group: verdict.lawGroup.groupId,
      appliesTo: lawDef.appliesTo,
      expiresAt: addDays(90).toISOString(),
    });
    lawIds.push(ruling.id);
  }

  // Retire Tier 1 laws being promoted
  for (const oldLawId of verdict.supersedes) {
    await Node.governance.deleteByGroup(oldLawId, {
      reason: "superseded,
      superseded_by: verdict.lawGroup.groupId,
    });
  }

  return Result.RouteToOutput("resolved");
}
```

**Forking Behavior:**
- For each law in `lawGroup.laws`, mint a separate Tier 2 Law CRD
- All laws share the same `spec.group` ID
- The group represents both "Spirit" (text/markdown) and "Letter" (application/smt-lib)
- Deleting one law deletes all laws in the group (see Repeal Logic below)

### 1.2 Backward Compatible Ruling Minting

For rulings without codifiable components, the legacy flow remains:

```typescript
if (verdict.new_ruling) {
  const ruling = await Node.governance.mintRuling({
    type: "text/markdown",
    content: verdict.new_ruling.statement,
    tier: 2,
    appliesTo: verdict.new_ruling.labels,
    expires: "90d"
  });

  for (const oldLawId of verdict.supersedes) {
    await Node.governance.retireLaw(oldLawId, {
      reason: "superseded",
      superseded_by: ruling.id
    });
  }
}
```

### 1.3 Consolidation (For Bundled Tier 1 Findings)

When the verdict includes `resolution_type: "consolidation"`:
1. **Mint** the new Tier 2 Ruling(s) with the synthesized statement (may be law group).
2. **Mark** all old Tier 1 laws in `supersedes[]` as retired.
3. **Prune** from Active Index via `foundry.system.legal_update` event.

### 1.5 For Amendment

When the verdict includes `resolution_type: "amendment"`:
1. **Patch** the existing Tier 2 Ruling content without changing its ID.
2. **Increment** the ruling's version number.
3. **Retire** the Tier 1 Finding that provided the clarification.

```typescript
if (verdict.resolution_type === "amendment") {
    ruling = await Node.governance.amendRuling({
        id: verdict.amendment.target_law_id,
        new_content: verdict.amendment.new_content
    });

    for (const oldLawId of verdict.supersedes) {
        await Node.governance.retireLaw(oldLawId, {
            reason: "absorbed",
            superseded_by: ruling.id
        });
    }
}
```

### 1.6 Handler Implementation (Legacy Path)

The legacy path remains for backward compatibility with non-codified rulings:

```typescript
if (verdict.consensus) {
    let ruling: Law;

    if (verdict.resolution_type === "consolidation") {
        ruling = await Node.governance.mintRuling({
            type: "text/markdown",
            content: verdict.new_ruling.statement,
            tier: 2,
            appliesTo: verdict.new_ruling.labels,
            expires: "90d"
        });

        for (const oldLawId of verdict.supersedes) {
            await Node.governance.retireLaw(oldLawId, {
                reason: "superseded",
                superseded_by: ruling.id
            });
        }
    } else {
        ruling = await Node.governance.mintRuling({
            type: "text/markdown",
            content: verdict.syntheticJustification,
            tier: 2,
            appliesTo: workitem.metadata.labels,
            expires: "90d"
        });
    }

    // Record decision against feedback
    for (const item of disputedItems) {
        if (verdict.status === "resolved") {
            await Node.resolveFeedback(ctx, item.id, {
                state: "resolved",
                linkedRuling: ruling.id,
                justification: {
                    type: "citation",
                    citationIds: [ruling.id],
                    argument: `Closed by Ruling: ${verdict.syntheticJustification}`
                }
            });
        } else {
            await Node.resolveFeedback(ctx, item.id, {
                state: "rejected",
                linkedRuling: ruling.id,
                message: `MANDATORY FIX: ${verdict.syntheticJustification}`
            });
        }
    }
    
    return Result.RouteToOutput("resolved");
}

return Result.RouteToOutput("escalate");
```

### 1.4 Repeal Logic (Delete by Group)

When retiring a law, the Librarian checks if `spec.group` is set. If so, it **deletes all laws with matching group ID** to ensure the "Spirit" and "Letter" are removed together.

```typescript
interface DeleteByGroupRequest {
  law_id: string;
  reason: "superseded" | "absorbed" | "constitutional_violation" | "expired";
  superseded_by?: string;
}

async function deleteByGroup(req: DeleteByGroupRequest): Promise<void> {
  // Get the law to find its group
  const law = await getLaw(req.law_id);
  
  if (law.spec.group) {
    // Delete all laws in the group
    const groupLaws = await listLaws({ group: law.spec.group });
    for (const groupLaw of groupLaws) {
      await retireLaw(groupLaw.id, {
        reason: req.reason,
        superseded_by: req.superseded_by,
      });
    }
  } else {
    // Single law, not grouped
    await retireLaw(req.law_id, {
      reason: req.reason,
      superseded_by: req.superseded_by,
    });
  }
}
```

**Atomic Group Deletion:**
- The deletion of all laws in a group is a single transaction
- If any law deletion fails, the entire operation is rolled back
- Emits a single `foundry.system.legal_update` event for the group

### 1.5 State Semantics

| Verdict | New State | Effect |
|---------|-----------|--------|
| `resolved` (Refiner wins) | `resolved` | Item closed. Original wont-fix is judicially authorized. |
| `rejected` (Appraiser wins) | `rejected` | Judicial Mandate. Contempt Guard blocks future wont-fix. |

---

## 2. Telemetry & Cost Accounting

Since Jurors are `FoundryAgents`, cost tracking is automatic with multi-dimensional attribution.

### 2.1 Cost Attribution Tags

All LLM costs during deliberation are tagged for Prometheus metrics:

| Tag | Value | Purpose |
|-----|-------|---------|
| `phase` | `"deliberation"` | Distinguish from normal node costs |
| `round` | `1`, `2`, ... | Which deliberation round |
| `feedback_id` | `"fb-101"` | Which disputed feedback item |
| `juror` | `"pragmatist"` | Which juror profile |
| `severity` | `"HIGH"` | Severity of the dispute |

**Example telemetry event:**
```json
{
  "type": "foundry.cost.llm",
  "context": { "workitem_id": "wi-123" },
  "payload": {
    "cost_usd": 0.05,
    "model": "gpt-4o",
    "tokens_in": 2500,
    "tokens_out": 800,
    "tags": {
      "phase": "deliberation",
      "round": "2",
      "feedback_id": "fb-101",
      "juror": "risk-manager",
      "severity": "HIGH"
    }
  }
}
```

### 2.2 Prometheus Queries

```promql
# Total cost of adjudicating a specific feedback item
sum(foundry_cost_llm_total{feedback_id="fb-101"})

# Average deliberation cost by severity
avg by (severity) (foundry_cost_llm_total{phase="deliberation"})

# Which juror profiles cost the most?
topk(3, sum by (juror) (rate(foundry_cost_llm_total{phase="deliberation"}[24h])))
```

### 2.3 Friction Impact

The Flow Monitor detects deliberation events and applies the **Judicial Multiplier** to the Workitem's friction score.

---

## 3. Consensus Strategies

The Assay Node's default consensus strategy is configurable and determines when jury consensus is achieved.

| Strategy | Requirement | Voting Rule | Use Case |
|----------|-------------|-------------|----------|
| `SimpleMajority` | >50% agreement | `(agreedCount > jurorCount / 2)` | Low/Medium severity disputes |
| `SuperMajority` | ≥66% agreement | `(agreedCount >= jurorCount * 2/3)` | Important policy decisions |
| `Unanimity` | 100% agreement | `(agreedCount == jurorCount)` | Safety-critical, compliance issues |

Configure in `FoundryFlow.spec.assayPolicy.consensusStrategy` or via Helm `assay.defaultConsensusStrategy`.

---

## 4. Error Handling

| Condition | Response |
|-----------|----------|
| No `disputed` feedback | Workitem → Failed (configuration error) |
| Jury hangs after `maxRounds` | Route to `escalate` (human intervention) |
| `WRITE:law/tier2` denied | Workitem → Failed (capability misconfiguration) |
| Juror inference timeout | Retry with backoff, then escalate |
