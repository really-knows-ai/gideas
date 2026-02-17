# Foundry Node: Configuration Patterns

## 1. Routing Resolution

```
Node returns RouteToOutput("pass")
        │
        ▼
┌─────────────────────────────────────────────────────────────┐
│ Operator                                                    │
│ 1. Find output "pass" in node config                        │
│ 2. If output.target set:                                    │
│    - Route to that specific node                            │
│ 3. If output.targetRole set:                                │
│    - Find all FoundryNodes with that role                   │
│    - Filter to Ready nodes only                             │
│    - Round-robin select                                     │
│ 4. Assign workitem to selected node                         │
└─────────────────────────────────────────────────────────────┘
```

---

## 2. Terminal Node Example

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: terminal-approved
spec:
  image: "registry/nodes/terminal:v1.0"
  roles: ["final-approver"]
  capabilities:
    - "READ:workitem"
  outputs: []
  isTerminal: true
  terminalContract: "approved"
```

---

## 3. Roles: Dual Purpose

| Purpose | How It's Used |
|---------|---------------|
| **Stamping authority** | ALL roles are applied simultaneously when stamping |
| **Routing target** | Other nodes can route via `targetRole` |

**Multiple Roles - Stamping Behavior:**

```go
// Node has roles: ["linter", "security-reviewer"]
n.Stamp(ctx, "python-source", "main.py", "passed all checks")
// Result: passport contains TWO stamps - one for each role
```

**Design Rationale:** Nodes represent atomic units of authority. A "security-linter" exercises its full authority when stamping.

---

## 4. Reserved Output Names

| Output Name | Behavior |
|-------------|----------|
| `timeout` | Operator routes here on execution deadline exceeded |

---

## 5. Reserved Target Tokens

| Token | Resolves To |
|-------|-------------|
| `$sender` | `workitem.status.previousAssignee` |

**Example:**
```yaml
outputs:
  - name: "needs-clarification"
    target: "$sender"
```

---

## 6. Governance Patterns

### 6.1 The Maker-Checker Pattern

**Self-Correction Model (Single Node):**
```yaml
kind: FoundryNode
metadata:
  name: generator-node
spec:
  roles: ["generator"]
  capabilities:
    - "WRITE:artefact/draft"
    - "INSPECT:artefact/draft"
```

**External Review Model (Maker-Checker):**
```yaml
# Generator: Creates content, does NOT stamp
kind: FoundryNode
metadata:
  name: generator-node
spec:
  roles: ["generator"]
  capabilities:
    - "WRITE:artefact/draft"

---

# Reviewer: Reviews and applies inspection stamp
kind: FoundryNode
metadata:
  name: reviewer-node
spec:
  roles: ["reviewer"]
  capabilities:
    - "READ:artefact/draft"
    - "INSPECT:artefact/draft"
```

---

## 7. Embassy Node Pattern (Cross-Flow I/O)

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: vendor-ingress
spec:
  image: "registry/nodes/embassy:v1.0"
  roles: ["importer"]
  capabilities:
    - "CREATE:workitem"
    - "WRITE:artefact/*"
    - "INSPECT:artefact/*"
  
  volumes:
    - name: diplomatic-pouch
      emptyDir:
        sizeLimit: "10Gi"
  
  volumeMounts:
    - name: diplomatic-pouch
      mountPath: /var/run/foundry/pouch
  
  outputs:
    - name: "accepted"
      targetRole: "processor"
    - name: "rejected"
      targetRole: "auditor"
```

**How it works:**
1. Node receives HTTP request with large bundle
2. Node streams to `/var/run/foundry/pouch/incoming.fb`
3. Node calls `ImportFromPath(ctx, "...")`
4. Sidecar reads from shared volume, validates, stores artefacts
5. Sidecar deletes bundle after successful import
