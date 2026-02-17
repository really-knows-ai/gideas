# Atomic Foundry Flow: Data Model - Feedback & Law

## 1. Passport Stamp Structure

Passport stamps are stored in the **Archivist** as metadata alongside artefact content. Each artefact version has its own passport. Stamps are retrieved via `GetArtefactMetadata()`.

The passport functions as a **Role-Centric Map** where `role` is the unique key. A new stamp for an existing role **overwrites** the previous entry (Last-Write-Wins).

### 1.1 Design Principle: Role-Centric Validation

Governance checks validate that a role is satisfied. The specific node identity is recorded for audit purposes.

**Deduplication:**
```
1. Node-A (Role: linter) stamps → Passport: [{ role: "linter", node: "node-a" }]
2. Node-B (Role: linter) stamps → Passport: [{ role: "linter", node: "node-b" }]  // OVERWRITES
```

### 1.2 Stamp Types

| Type | Purpose | Applied By | Laws Required |
|------|---------|------------|---------------|
| `inspection` | Records that a node has reviewed the current artefact version | Quench, Appraise | No |
| `approval` | Certifies the artefact meets governance requirements | Sort | Yes |

Both stamp types are invalidated when the artefact content changes (hash mismatch).

### 1.3 Stamp Fields

| Field | Type | Description |
|-------|------|-------------|
| `role` | string | **Primary Key.** The role being asserted |
| `type` | string | Stamp type: `inspection` or `approval` |
| `node` | string | Name of the node (for audit) |
| `timestamp` | datetime | When the stamp was created |
| `hash` | string | Content hash of the artefact at stamp time |
| `signature` | string | RSA signature |
| `certificateChain` | []string | PEM-encoded certificates |
| `laws` | []LawCitation | Law citations (required for `approval` type) |

---

## 2. Feedback Model (Threaded, Artefact-Scoped)

### 2.1 Design: Smart Gate, Dumb Worker

| Failure Type | Detection | Response |
|--------------|-----------|----------|
| **Infrastructure Thrashing** | `guestbook` visit counts | Fail workitem |
| **Semantic Disagreement** | `feedback.history` depth | Escalate to Assay |

### 2.2 Feedback Schema

```yaml
feedback:
  - id: "fb-101"
    target: "haiku-draft"
    source: "haiku-appraise"
    severity: "MEDIUM"
    state: "pending"
    message: "Haiku is too cheerful."
    history:
      - timestamp: "2026-01-04T14:30:00Z"
        author: "haiku-appraise"
        role: "appraiser"
        action: "opened"
        message: "Haiku is too cheerful."
      - timestamp: "2026-01-04T14:35:00Z"
        author: "haiku-refine"
        role: "refiner"
        action: "fixed"
        message: "Revised to include autumn imagery."
```

### 2.3 Feedback States

| State | Description |
|-------|-------------|
| `pending` | Issue raised, not yet addressed |
| `actioned` | Refine node addressed it |
| `wont-fix` | Refine node refused with justification |
| `disputed` | Gate node escalated (history depth exceeded) |
| `rejected` | Assay ruled against Refiner (Judicial Mandate) |
| `resolved` | Assay ruled for Refiner (closed) |

### 2.4 Fatigue Detection

```go
func (n *SortNode) Assigned(ctx context.Context, item Workitem) Result {
    maxDepth := n.Config.MaxFeedbackDepth
    
    for _, fb := range filterFeedback(item.Status.Feedback, "pending") {
        if len(fb.History) > maxDepth {
            n.UpdateFeedbackState(ctx, fb.ID, "disputed")
            return RouteToOutput("deadlock")
        }
    }
    
    if len(filterFeedback(item.Status.Feedback, "pending")) > 0 {
        return RouteToOutput("refine")
    }
    
    return RouteToOutput("done")
}
```

**Per-Feedback-Item Check:** The check is semantic: "Are we arguing in circles about *this specific point*?"

### 2.5 Justification Structure

```yaml
justification:
  type: "citation"           # or "novel_argument"
  citationIds: ["f-105"]     # If citation
  argument: "..."            # If novel_argument
```

---

## 3. `Law` (The Living Constitution)

### 3.1 CRD Definition

The Law CRD is defined in [crds/Law.yaml](../crds/Law.yaml). Laws support polymorphic payloads via `spec.type` and `spec.content`.

### 3.2 Polymorphic Law Structure

```yaml
apiVersion: flow.gideas.io/v1
kind: Law
metadata:
  name: l-002-no-sausage-text
  labels:
    flow.gideas.io/tier: "2"
    flow.gideas.io/type: "text.markdown"
    flow.gideas.io/group: "lg-7729"
    flow.gideas.io/applies-to: "poetry"
spec:
  tier: 2
  type: "text/markdown"
  content: "Poetry must be happy and optimistic in tone."
  group: "lg-7729"
  appliesTo: "poetry"
status:
  citationCount: 47
  governanceSignal: "Moderate"
  lastLeaseUpdate: "2025-11-22T10:00:00Z"
```

```yaml
apiVersion: flow.gideas.io/v1
kind: Law
metadata:
  name: l-002-no-sausage-code
  labels:
    flow.gideas.io/tier: "2"
    flow.gideas.io/type: "application.smt-lib"
    flow.gideas.io/group: "lg-7729"
    flow.gideas.io/applies-to: "poetry"
spec:
  tier: 2
  type: "application/smt-lib"
  content: |
    (declare-const artefact-content String)
    (assert (str.in_re "sausage" (re.++ (re.* (re.range "a" "z")))))
    (assert (not (str.in_re "sausage" (re.* (re.union (re.range "a" "z") (re.range "A" "Z") (re.range "0" "9") (str.to_re " ")))))
  group: "lg-7729"
  appliesTo: "poetry"
status:
  citationCount: 47
  governanceSignal: "Moderate"
  lastLeaseUpdate: "2025-11-22T10:00:00Z"
```

### 3.3 Supported Content Types

| Type | MIME | Use Case | Consumed By |
|------|------|----------|-------------|
| Text | `text/markdown` | Subjective rules (e.g., "be happy", "beautiful code") | Appraise Nodes |
| SMT-LIB | `application/smt-lib` | Deterministic solvable constraints | Quench Nodes |
| Python | `application/python` | Executable validation logic | Quench Nodes |

### 3.4 Law Groups

Laws can be linked via `spec.group` to represent the "Spirit" (text) and "Letter" (code) of the same rule:

- **Group ID Format**: `lg-[a-z0-9]+` (e.g., `lg-7729`)
- **Relationship**: Laws in the same group share lifecycle
- **Search**: Query by group to find all related laws
- **Repeal**: Deleting one law deletes all laws with matching group ID

### 3.5 Index Labels

The Operator auto-syncs these indexed labels from `spec` to `metadata.labels`:

| Label | Source | Purpose |
|-------|--------|---------|
| `flow.gideas.io/tier` | `spec.tier` | Filter by law tier |
| `flow.gideas.io/applies-to` | `spec.appliesTo` | Filter by target context |
| `flow.gideas.io/group` | `spec.group` | Find related laws |
| `flow.gideas.io/type` | `spec.type` (sanitized) | Filter by content type |

Sanitization replaces `/` with `.` for Kubernetes label compatibility: `application/smt-lib` → `application.smt-lib`

### 3.6 Law Tiers

| Tier | Name | Source | Lifecycle |
|------|------|--------|-----------|
| 1 | Finding | Forge/Refine nodes | Decays if uncited |
| 2 | Ruling | Assay Node | Higher relevance, retirement petition on decay |
| 3 | Statute | State Governance Flow | No decay, human ratification |
| 4 | Federal | Federal G(IDEAS) Instance | Read-only, published by central authority |
