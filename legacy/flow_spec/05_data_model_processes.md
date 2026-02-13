# Atomic Foundry Flow: Data Model - Processes

## 3.1 `FoundryFlow` (The Flow Definition)

Defines a directed graph of nodes with entry and terminal contracts.

### Schema

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryFlow
metadata:
  name: petition-flow
spec:
  entryContract:
    workitemTypes:
      - "petition-v1"
    requiredArtefacts:
      - kind: "raw-intent"
        state: "present"
  
  terminalContracts:
    - name: "approved"
      requiredArtefacts:
        - kind: "petition-draft"
          state: "valid"
    - name: "rejected"
      requiredArtefacts:
        - kind: "rejection-notice"
          state: "valid"
  
  entryNode: "forge-node"
  
  nodes:
    - forge-node
    - quench-node
    - sort-node
    - appraise-node
    - refine-node
    - assay-node
    - terminal-approved
    - terminal-rejected
```

### Field Reference

| Field | Type | Description |
|-------|------|-------------|
| `entryContract.workitemTypes` | []string | Allowed WorkItemType names |
| `entryContract.requiredArtefacts` | []ArtefactRequirement | Artefacts required at entry |
| `terminalContracts` | []TerminalContract | Named exit contracts |
| `terminalContracts[].name` | string | Contract identifier (referenced by terminal nodes) |
| `terminalContracts[].requiredArtefacts` | []ArtefactRequirement | Artefacts required to exit via this contract |
| `entryNode` | string | First node to receive work |
| `nodes` | []string | Node names in this Flow |

### ArtefactRequirement

```yaml
kind: "petition-draft"   # References GovernedArtefact.metadata.name
state: "valid"           # "present" (exists) or "valid" (all required stamps present)
```

### Terminal Contract Validation (Binary, Not Partial)

**The validation model is strictly binary.** There is no support for "partial validity" or "subset of stamps."

| State | Validation Logic |
|-------|------------------|
| `present` | Artefact exists (latestVersion is set) - stamps ignored |
| `valid` | Artefact exists AND has stamps from **ALL** roles in `GovernedArtefact.requiredRoles` |

**Why No Partial Stamp Requirements?**

Allowing a terminal contract to specify "I only need Linter stamp, not Security stamp" would create **shadow governance** defined in the routing topology rather than in Governance CRDs. This violates a core principle:

> **Governance (what is valid) is separate from Flow (where it goes).**

The `GovernedArtefact` CRD is the single source of truth for "what does valid mean?" Terminal contracts simply ask "is it valid?" or "does it exist?" - they don't redefine validity.

**The Solution: Multiple Contracts**

If different exit paths have different requirements, define multiple terminal contracts:

```yaml
terminalContracts:
  # Success path: fully validated artefacts required
  - name: "production-ready"
    requiredArtefacts:
      - kind: "petition-draft"
        state: "valid"           # ALL stamps required
      - kind: "audit-log"
        state: "present"         # Just existence

  # Rejection path: archive whatever we have
  - name: "rejected"
    requiredArtefacts:
      - kind: "petition-draft"
        state: "present"         # Archive even if invalid
      - kind: "rejection-report"
        state: "present"
```

This allows the "Rejected" terminal to complete successfully with an invalid draft (which is the point), while "Production-Ready" remains strictly gated.

---

## 3.2 `WorkItemType` (The Work Schema)

Defines the shape of a workitem. Shared across flows. No phases - just a schema.

### Schema

```yaml
apiVersion: flow.gideas.io/v1
kind: WorkItemType
metadata:
  name: petition-v1
spec:
  schema:
    properties:
      intent:
        type: string
        description: "What the petitioner wants to achieve"
      priority:
        type: string
        enum: ["low", "medium", "high"]
      requestedBy:
        type: string
    required:
      - intent
```

### Field Reference

| Field | Type | Description |
|-------|------|-------------|
| `schema` | object | JSON Schema for workitem spec fields |

---

## 3.3 `GovernedArtefact` (The Definition of Done)

Defines the roles required to "stamp" (certify) an artefact. An artefact is **valid** when it bears stamps from all required roles.

### Schema

```yaml
apiVersion: flow.gideas.io/v1
kind: GovernedArtefact
metadata:
  name: petition-draft
spec:
  # The single, immutable "Definition of Done"
  # An artefact is VALID iff it has stamps from ALL these roles
  requiredRoles:
    - "linter"
    - "security-reviewer"
    - "legal-reviewer"
```

### Field Reference

| Field | Type | Description |
|-------|------|-------------|
| `requiredRoles` | []string | Roles that MUST stamp the artefact to achieve validity |

### Validity Semantics

**An artefact is `valid` if and only if:**
- It exists (latestVersion is set), AND
- Its passport contains at least one stamp for EACH role in `requiredRoles`

**An artefact is `present` if:**
- It exists (latestVersion is set), regardless of stamps

**Examples:**

```
GovernedArtefact requires: ["linter", "security-reviewer", "legal-reviewer"]

Scenario 1: Passport has stamps from linter, security-reviewer, legal-reviewer
  → valid: true

Scenario 2: Passport has stamps from linter, security-reviewer (missing legal-reviewer)
  → valid: false

Scenario 3: No passport stamps at all
  → valid: false

Scenario 4: Artefact doesn't exist yet
  → present: false, valid: false
```

### Role-Based Validation

Stamps are validated by **role**. The specific node identity is recorded for audit purposes. This enables:

- **Horizontal Scaling**: Multiple nodes with the same role can stamp interchangeably
- **Cross-Flow Trust**: In federated mode, a stamp from `flow-security/quench-node` is valid in `flow-execute` if both share the State Root CA and the node has role `security-reviewer`

---

## 3.4 Typical Flow Topology

The canonical Foundry Cycle as a directed graph:

```
START → Forge → Quench → Sort₁ → Appraise → Sort₂ → END (approved)
                  ↑        │                  │
                  │        ↓                  │        END (rejected)
                  └───── Refine ←─────────────┤          ↑
                                              │          │
                                              ↓          │
                                           Assay ────────┘
```

### Node Outputs Configuration

| Node | Outputs |
|------|---------|
| Forge | `default` → [quench-node] |
| Quench | `default` → [sort-node-1] |
| Sort₁ | `pass` → [appraise-node], `fail` → [refine-node] |
| Appraise | `default` → [sort-node-2] |
| Sort₂ | `pass` → [terminal-approved], `fail` → [refine-node], `deadlock` → [assay-node] |
| Refine | `default` → [quench-node] |
| Assay | `resolved` → [$sender], `escalate` → [hitl-node] |
