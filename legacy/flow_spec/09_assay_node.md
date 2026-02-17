# Technical Specification: The Assay Node - Configuration & Protocol

**Status:** DRAFT (Version 1.0.0)

## 1. Problem Statement

In the Foundry Flow, subjective deadlocks are inevitable. A `Refine Node` may encounter conflicting feedback (e.g., "Legal says X, Brand says Y") or feedback that contradicts existing constraints. Without an autonomous resolution mechanism, these deadlocks require manual human intervention, which creates a bottleneck, increases cost, and slows down the lifecycle.

## 2. Goal

To implement an autonomous **Judicial Resolution** mechanism that:

1. **Resolves Deadlocks via Consensus:** Empanels a "Jury of Peers" (parallel `FoundryAgents`) to deliberate on disputes using a "Multi-Turn Deliberative Consensus" pattern.
2. **Mints Binding Precedent:** Uses role-based capabilities (`WRITE:law/tier2`) to mint binding `Tier 2 Rulings` that prevent recurrence.
3. **Derives Context from Workitem State:** The Assay Node reconstructs the dispute context directly from the standard `feedback` ledger.

---

## 3. Executive Summary

The **Assay Node** is a standard `FoundryNode` configured with the **Judiciary** role. It operates on the "Pull Model" of justice: it is summoned only when a `Sort Node` detects a deadlock and routes the Workitem to it.

Upon receipt, the Assay Node acts as a **Foreman**:

1. **Triages** the Workitem to identify `disputed` feedback items.
2. **Empanels** a Jury of `FoundryAgent` instances based on the dispute's severity.
3. **Orchestrates** a blind-voting protocol with optional deliberation rounds.
4. **Executes** the verdict by minting a `Law` CRD (if consensus is reached) or escalating to a human authority (if the jury hangs).

---

## 4. Node Configuration

### 4.1 `FoundryNode` CRD

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: assay-node
spec:
  image: "registry/nodes/assay-node:v1.2"
  roles: ["judiciary"]
  timeout: "120s"
  
  capabilities:
    - "READ:workitem"
    - "READ:law"
    - "WRITE:law/tier2"
    - "WRITE:law/tier1"
    - "ESCALATE:governance"
  
  outputs:
    - name: "resolved"
      target: "$sender"
    - name: "escalate"
      target: "hitl-node"
```

### 4.2 Retirement Hearings (Special Case)

The Assay Node handles two types of workitems:

1. **Dispute Resolution** (primary): Adjudicates `disputed` feedback items via jury.
2. **Retirement Hearings** (special): Reviews expired Tier 2 Rulings for sustainability.

**Retirement Hearing Handler:**

```typescript
if (workitem.spec.context["hearing_type"] === "retirement_hearing") {
    const law = workitem.spec.context;
    const daysSinceLastCitation = (now() - law.last_cited_at) / 86400;
    
    if (daysSinceLastCitation < 30 && law.citation_count > 10) {
        await Node.governance.sustainRuling(law.law_id, "+90d");
        return Result.Complete({ decision: "sustain" });
    } else if (law.citation_count >= 5) {
        await Node.governance.demoteLaw(law.law_id);
        return Result.Complete({ decision: "demote" });
    } else {
        await Node.governance.retireLaw(law.law_id, { reason: "obsolete" });
        return Result.Complete({ decision: "retire" });
    }
}
```

**Note:** Retirement hearings do NOT use the jury mechanism. They use simple heuristics based on usage data.

---

## 5. Codification Phase

### 5.1 Parsing and Separation

During Tier 1 → Tier 2 promotion, the Assay Node **codifies** the original statement by separating:
- **Subjective parts**: Qualitative rules requiring human/LLM judgment (e.g., "must be happy", "beautiful code")
- **Deterministic parts**: Objective constraints expressible as formal logic (e.g., "no sausage", "max 100 lines")

### 5.2 Codification Process

```typescript
interface CodificationResult {
  group: string;              // Shared group ID: "lg-7729"
  subjective: {
    content: string;          // Markdown-formatted subjective rule
    type: "text/markdown";
  };
  deterministic?: {           // Optional: omitted if no codifiable parts
    content: string;          // SMT-LIB constraints
    type: "application/smt-lib";
  };
}

function codifyStatement(statement: string): CodificationResult {
  // LLM analyzes statement structure
  const analysis = await detectComponents(statement);

  // Extract subjective portions
  const subjectiveContent = extractSubjective(analysis);

  // Generate deterministic constraints if applicable
  let deterministicContent: string | undefined;
  if (hasCodifiableComponents(analysis)) {
    deterministicContent = await generateSMTLib(analysis);
  }

  // Generate unique group ID
  const groupId = `lg-${generateId()}`;

  return {
    group: groupId,
    subjective: {
      content: subjectiveContent,
      type: "text/markdown",
    },
    deterministic: deterministicContent ? {
      content: deterministicContent,
      type: "application/smt-lib",
    } : undefined,
  };
}
```

### 5.3 Example Codification

**Input (Tier 1 Finding):**
```
"Poetry must be happy and optimistic, and must not contain the word 'sausage'"
```

**Output (Law Group):**

*Law A (Subjective):*
```yaml
spec:
  type: "text/markdown"
  content: "Poetry must be happy and optimistic in tone."
  group: "lg-7729"
  tier: 2
  appliesTo: "poetry"
```

*Law B (Deterministic):*
```yaml
spec:
  type: "application/smt-lib"
  content: |
    (declare-const artefact-content String)
    (assert (not (str.contains artefact-content "sausage")))
  group: "lg-7729"
  tier: 2
  appliesTo: "poetry"
```

### 5.4 Codification Capabilities

| Statement Type | Can Codify? | Output |
|---------------|-------------|--------|
| "No prohibited words" | ✅ Yes | SMT-LIB string constraints |
| "Max character limit" | ✅ Yes | SMT-LIB length assertions |
| "Required pattern present" | ✅ Yes | SMT-LIB regex checks |
| "Must be beautiful/clean" | ❌ No | Text/markdown only |
| "Must follow naming convention" | ✅ Yes | SMT-LIB name pattern rules |
| "Must have appropriate tone" | ❌ No | Text/markdown only |

### 5.5 Updated Assay Verdict

```typescript
interface AssayVerdict {
  verdict: "resolved" | "escalated";
  resolution_type: "standard" | "consolidation" | "amendment";
  
  // NEW: Law group output
  lawGroup?: {
    groupId: string;
    laws: {
      type: "text/markdown" | "application/smt-lib";
      content: string;
      tier: 2;
      appliesTo: string;
    }[];
  };
  
  // Legacy fields (for backward compatibility with non-codifiable rulings)
  new_ruling?: { statement: string; labels: Record<string, string>; };
  amendment?: { target_law_id: string; new_content: string; };
  supersedes: string[];
}
```

---

## 6. The Assay Policy (The Constitution of the Court)

The "Rules of Order" are defined in the `FoundryFlow` configuration:

```yaml
assayPolicy:
  lawConflictResolution:
    hierarchyStrictness: "Absolute"
  
  - name: "commercial-dispute"
    matchSeverity: ["LOW", "MEDIUM"]
    consensusStrategy: "SimpleMajority"
    maxRounds: 3
    juryProfile:
      - name: "pragmatist"
        model: "gpt-4o"
        systemPrompt: "You are a pragmatic business analyst..."
      - name: "risk-manager"
        model: "gpt-4o"
        systemPrompt: "You are a risk manager..."
      - name: "customer-advocate"
        model: "gpt-4o"
        systemPrompt: "You represent the end user..."

  - name: "safety-incident"
    matchSeverity: ["HIGH", "SEVERE"]
    consensusStrategy: "Unanimity"
    maxRounds: 5
    juryProfile:
      - "ciso-agent"
      - "legal-compliance-agent"
      - "brand-safety-agent"
```

---

## 7. The Jury Protocol

### Phase 1: Triage (Deriving the Case)

* **Evidence:** Filter `workitem.status.feedback` for `state == "disputed"`.
  * If no `disputed` items exist, the Workitem is marked as `Failed` (configuration error).
* **Context:** The `history` array of each disputed feedback item provides the full dispute timeline.
* **Severity:** `MAX(feedback.severity)` determines the `AssayPolicy`.

**Why Hard Failure is Required:** A soft "pass-through" response would create a Zombie Loop: Sort Node re-evaluates the unchanged state, re-escalates to Assay, repeat.

### Phase 2: Deliberation (The Loop)

```typescript
// Input: The Case File
interface CaseFile {
  artefact_name: string;
  artefact_content: string;
  disputed_feedback: {
    id: string;
    target: string;
    severity: string;
    history: FeedbackEvent[];
  }[];
  peer_arguments?: string[];  // Empty in Round 1
}

// Output: The Juror Vote
interface JurorVote {
  status: "resolved" | "rejected";
  justification: string;
  confidence: number;  // 0.0 - 1.0
}

// Output: The Verdict Payload
interface AssayVerdict {
  verdict: "resolved" | "escalated";
  resolution_type: "standard" | "consolidation" | "amendment";
  new_ruling?: { statement: string; labels: Record<string, string>; };
  amendment?: { target_law_id: string; new_statement: string; };
  supersedes: string[];
}
```

### Phase 3: The Foreman's Count

1. **Check Consensus:** Does the vote distribution meet the `consensusStrategy`?
2. **If Consensus Reached:** Synthesise justifications into a Ruling Statement. Proceed to Execution.
3. **If Hung:**
   * Check `round < maxRounds`.
   * **If True:** Re-run Phase 2 with `peer_arguments` populated.
   * **If False:** Escalate via `RouteToOutput("escalate")`.

**Hung Jury Escalation:** Routes to the node defined in the `escalate` output (typically a `hitl-node`). No new workitem is created.

---

## 7. Related Documents

- [09a_assay_execution.md](./09a_assay_execution.md) - Phase 4 execution, telemetry, error handling
