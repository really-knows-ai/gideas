# Reference Flow: The Haiku Flow

**Status:** Reference Implementation
**Purpose:** Validation of the Foundry Flow runtime. Tests deterministic gates, subjective gates, feedback loops, and the complete lifecycle without complex business logic.

## 1. Overview

The Haiku Flow is the "Hello World" of governed AI. It generates a haiku about a given topic, validates it against both deterministic rules (syllable count) and subjective rules (sentiment), and refines until both gates pass.

### Architecture: Smart Gate, Dumb Worker

The flow uses the **Split-Gate Topology** pattern:
- **Workers** (Quench, Appraise, Refine) are "dumb" - they evaluate/transform and route linearly
- **Gates** (Sort nodes) are "smart" - they check feedback state and depth to decide routing

```
┌─────────┐     ┌─────────┐     ┌─────────────┐     ┌───────────┐     ┌─────────────┐     ┌──────────┐
│  Forge  │────▶│ Quench  │────▶│ Sort-Quench │────▶│ Appraise  │────▶│Sort-Appraise│────▶│ Terminal │
│ (Write) │     │(5-7-5?) │     │   (Gate)    │     │(Sentiment)│     │   (Gate)    │     │(Approved)│
└─────────┘     └─────────┘     └──────┬──────┘     └───────────┘     └──────┬──────┘     └──────────┘
                                       │                                      │
                                       │ refine                        refine │
                                       ▼                                      ▼
                                ┌─────────────────────────────────────────────┐
                                │                  Refine                     │
                                │            (Address Feedback)               │
                                └─────────────────────┬───────────────────────┘
                                                      │
                                                      ▼
                                                   Quench (retry)
```

**Key Differences from Single-Sort Pattern:**
- Two physical Sort nodes (`sort-quench`, `sort-appraise`) instead of one
- Each gate knows its position in the flow
- Feedback depth (history length) triggers escalation
- Guestbook is internal to the Sidecar (thrash guard only)

---

## 2. Workitem Type

```yaml
apiVersion: flow.gideas.io/v1
kind: WorkItemType
metadata:
  name: haiku-v1
spec:
  schema:
    type: object
    properties:
      intent:
        type: string
        description: "The subject of the haiku"
    required: ["intent"]
  example:
    intent: "autumn leaves"
```

> **Note:** The field is named `intent` following the platform convention where `intent` serves as the universal "subject line" for all work types.

---

## 3. Governed Artefacts

```yaml
apiVersion: flow.gideas.io/v1
kind: GovernedArtefact
metadata:
  name: haiku-draft
spec:
  # Definition of Done: all three roles must stamp before terminal approval
  requiredRoles:
    - "quench"          # Syllable validation (5-7-5)
    - "appraise"        # Sentiment and form check
    - "final-approver"  # Executive approval
```
```

---

## 4. Node Definitions

### 4.1 Forge Node (Generation)

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: haiku-forge
spec:
  image: "registry/nodes/haiku-forge:v1"
  roles: ["generator"]
  capabilities:
    - "WRITE:artefact/haiku-draft"
  outputs:
    - name: "default"
      targetRole: "quench"
```

**Logic:** Fetch the `input` artefact and generate a haiku. Store as `haiku-draft`.

### 4.2 Quench Node (Deterministic Validation - Worker)

A "dumb worker" - evaluates and routes linearly.

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: haiku-quench
spec:
  image: "registry/nodes/haiku-quench:v1"
  roles: ["quench"]
  capabilities:
    - "READ:artefact/haiku-draft"
    - "INSPECT:artefact/haiku-draft"
  outputs:
    - name: "default"
      target: "sort-quench"    # Always route to gate
```

**Logic:** 
1. Parse the haiku into three lines.
2. Count syllables per line.
3. If pattern is 5-7-5: Stamp artefact.
4. Else: Add feedback (target: `haiku-draft`) "Line X has Y syllables, expected Z".
5. Always route to `default` (gate decides next step).

### 4.3 Sort-Quench Node (Gate after Quench)

A "smart gate" - checks feedback state and depth.

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: sort-quench
spec:
  image: "registry/nodes/standard-sort:v2"
  roles: ["gate"]
  capabilities:
    - "READ:workitem"
  env:
    - name: MAX_FEEDBACK_DEPTH
      value: "3"
  outputs:
    - name: "done"
      target: "haiku-appraise"  # Proceed to sentiment check
    - name: "refine"
      targetRole: "refiner"
    - name: "deadlock"
      target: "assay-node"
```

**Logic:** Generic gate pattern (see 4.5).

### 4.4 Appraise Node (Subjective Validation - Worker)

A "dumb worker" - evaluates and routes linearly.

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: haiku-appraise
spec:
  image: "registry/nodes/haiku-appraise:v1"
  roles: ["appraiser"]
  capabilities:
    - "READ:artefact/haiku-draft"
    - "INSPECT:artefact/haiku-draft"
  outputs:
    - name: "default"
      target: "sort-appraise"  # Always route to gate
```

**Logic:**
1. Read the haiku.
2. Use LLM to classify sentiment: "melancholy", "cheerful", "neutral", etc.
3. If sentiment is "melancholy": Stamp artefact.
4. Else: Add feedback (target: `haiku-draft`) "Haiku is too [sentiment]. Must evoke melancholy."
5. Always route to `default` (gate decides next step).

### 4.5 Sort-Appraise Node (Gate after Appraise)

A "smart gate" - checks feedback state and depth.

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: sort-appraise
spec:
  image: "registry/nodes/standard-sort:v2"
  roles: ["gate"]
  capabilities:
    - "READ:workitem"
  env:
    - name: MAX_FEEDBACK_DEPTH
      value: "3"
  outputs:
    - name: "done"
      target: "haiku-terminal"  # Proceed to terminal
    - name: "refine"
      targetRole: "refiner"
    - name: "deadlock"
      target: "assay-node"
```

**Logic:** Generic gate pattern:

```go
func (n *SortNode) Assigned(ctx context.Context, item Workitem) Result {
    maxDepth := n.Config.MaxFeedbackDepth  // e.g., 3
    
    // Check for semantic fatigue (dispute depth exceeded)
    for _, fb := range filterFeedback(item.Status.Feedback, "pending") {
        if len(fb.History) > maxDepth {
            n.UpdateFeedbackState(ctx, fb.ID, "disputed")
            return RouteToOutput("deadlock")
        }
    }
    
    // Check for pending feedback
    if len(filterFeedback(item.Status.Feedback, "pending")) > 0 {
        return RouteToOutput("refine")
    }
    
    // All clear - proceed to next stage
    return RouteToOutput("done")
}
```

### 4.6 Refine Node (Revision - Worker)

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: haiku-refine
spec:
  image: "registry/nodes/haiku-refine:v1"
  roles: ["refiner"]
  capabilities:
    - "READ:artefact/haiku-draft"
    - "WRITE:artefact/haiku-draft"
  outputs:
    - name: "default"
      target: "haiku-quench"  # Always re-validate from start
```

**Logic:**
1. Read pending feedback items, note the `target` artefact.
2. Read current haiku and feedback history.
3. Use LLM to rewrite haiku addressing the feedback.
4. Store new version (clears passport).
5. Mark feedback as `actioned` (appends "fixed" event to history).
6. Route to Quench for re-validation.

```go
func (n *RefineNode) Assigned(ctx context.Context, item foundry.Workitem) foundry.Result {
    // Get pending feedback items
    pending := filterFeedback(item.Status.Feedback, "pending")
    if len(pending) == 0 {
        // Nothing to refine - shouldn't happen, but handle gracefully
        return foundry.RouteToOutput("default")
    }
    
    // Build context from feedback history
    for _, fb := range pending {
        // fb.Target tells us WHICH artefact to modify
        content, _ := n.FetchArtefact(ctx, "haiku-draft", fb.Target)
        
        // fb.History gives us the full dispute timeline for LLM context
        prompt := buildRefinePrompt(content, fb.History)
        
        newContent, _ := n.llm.Generate(ctx, prompt)
        n.StoreArtefact(ctx, "haiku-draft", fb.Target, newContent)
        
        // Resolve feedback - appends "fixed" event to history
        n.ResolveFeedback(ctx, fb.ID, "Revised based on feedback")
    }
    
    return foundry.RouteToOutput("default")
}
```

### 4.7 Terminal Node

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: haiku-terminal
spec:
  image: "registry/nodes/standard-terminal:v1"
  roles: ["final-approver"]
  capabilities:
    - "APPROVE:artefact/haiku-draft"
  outputs: []
  isTerminal: true
  terminalContract: "approved"
```

**Logic:**
1. Stamp the `haiku-draft` artefact (applies `final-approver` role).
2. Call `Complete()` to signal terminal completion.

---

## 5. Flow Configuration

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryFlow
metadata:
  name: haiku-flow
spec:
  entryContract:
    workitemTypes: ["haiku-v1"]
    requiredArtefacts: []
  
  terminalContracts:
    - name: "approved"
      requiredArtefacts:
        - kind: "haiku-draft"
          state: "valid"
  
  entryNode: "haiku-forge"
  
  nodes:
    - haiku-forge
    - haiku-quench
    - sort-quench
    - haiku-appraise
    - sort-appraise
    - haiku-refine
    - haiku-terminal
    - assay-node
  
  governance:
    standardNodeTimeout: "30s"
```

---

## 6. Example Execution

**Input:**
```yaml
apiVersion: flow.gideas.io/v1
kind: Workitem
metadata:
  name: haiku-autumn-001
spec:
  type: "haiku-v1"
  intent: "autumn leaves"
```

**Iteration 1:**
- Forge generates: "Leaves fall from the trees / Orange and red everywhere / Summer says goodbye"
- Quench: FAIL - Line 2 has 8 syllables ✗. Adds feedback (target: haiku-draft).
- Sort-Quench: Pending feedback exists → routes to Refine

**Iteration 2:**
- Refine rewrites: "Leaves fall from the trees / Red and orange swirling / Summer says goodbye"
- Quench: PASS - 5-7-5 ✓. Stamps artefact.
- Sort-Quench: No pending feedback → routes to Appraise (done)
- Appraise: FAIL - Sentiment is "neutral". Adds feedback (target: haiku-draft).
- Sort-Appraise: Pending feedback exists → routes to Refine

**Iteration 3:**
- Refine rewrites: "Crimson leaves descend / Through the grey October mist / Summer's ghost departs"
- Quench: PASS - 5-7-5 ✓. Stamps artefact.
- Sort-Quench: No pending feedback → routes to Appraise (done)
- Appraise: PASS - Sentiment is "melancholy". Stamps artefact.
- Sort-Appraise: No pending feedback → routes to Terminal (done)
- Terminal: Stamps artefact, calls Complete()

**Correct Trace (Split-Gate Topology):**

| Step | Node | Action | Result |
|------|------|--------|--------|
| 1 | Forge | Generate haiku | Store `haiku-draft`, route default |
| 2 | Quench | Count syllables | FAIL (8 syllables line 2) → add feedback, route default |
| 3 | Sort-Quench | Check feedback | Pending feedback → route `refine` |
| 4 | Refine | Fix syllables | Store v2, mark feedback actioned, route default |
| 5 | Quench | Count syllables | PASS → stamp, route default |
| 6 | Sort-Quench | Check feedback | No pending → route `done` (to Appraise) |
| 7 | Appraise | Check sentiment | FAIL (neutral) → add feedback, route default |
| 8 | Sort-Appraise | Check feedback | Pending feedback → route `refine` |
| 9 | Refine | Fix sentiment | Store v3, mark feedback actioned, route default |
| 10 | Quench | Count syllables | PASS → stamp, route default |
| 11 | Sort-Quench | Check feedback | No pending → route `done` |
| 12 | Appraise | Check sentiment | PASS (melancholy) → stamp, route default |
| 13 | Sort-Appraise | Check feedback | No pending → route `done` (to Terminal) |
| 14 | Terminal | Validate contract | Stamp + Complete() |
