# Atomic Foundry Flow: Operator Overview

## 1. Data Gravity Principle

**Workitems are immutable residents of their namespace.** This is the foundational principle of Flow sovereignty.

A `Workitem` CRD belongs exclusively to its namespace. Cross-flow collaboration uses **Copy-on-Write** semantics:

- The original Workitem stays in Flow A (completed/archived)
- A new Workitem is created in Flow B (born from import)
- Data is copied to the new Flow

**What this means for routing:**

| Pattern | Scope | Mechanism |
|---------|-------|-----------|
| `RouteToOutput("pass")` | **Local** | Routing within the Flow |
| `RouteTo("node-name")` | **Local** | Direct routing within the Flow |
| Cross-flow collaboration | **Export/Import** | Separate workitem lifecycle (see below) |

See [cross_flow_collaboration/](cross_flow_collaboration/) for the Export/Import pattern.

---

## 2. The Flow Operator

The primary controller that manages the Flow's routing and validation. The Flow Operator is a **monolithic controller** that reconciles both `FoundryFlow` (topology/routing) and `FoundryNode` (infrastructure) resources.

### 2.1 FoundryNode Reconciliation

When a `FoundryNode` CRD is created or updated, the Operator generates corresponding Kubernetes resources:

1. **Deployment OR StatefulSet:** Based on `spec.storage` (see below)
2. **Service:** Internal service for node-to-node communication
3. **PVC:** If `spec.storage` is defined
4. **Probes:** Liveness, readiness, and startup probes targeting Sidecar health endpoints
5. **Grace Period:** `terminationGracePeriodSeconds = 2 × spec.timeout`

### 2.2 Deployment vs StatefulSet

The Operator selects the workload type based on storage requirements:

| Condition | Workload Type | Reason |
|-----------|---------------|--------|
| `spec.storage` **undefined** | `Deployment` | Stateless, interchangeable replicas |
| `spec.storage` **defined** | `StatefulSet` | Stable identity, stable storage binding |

**Why StatefulSet for Storage:**
- **Stable Identity:** Pods get ordered names (`node-0`, `node-1`, ...)
- **Stable Storage:** `node-0` always mounts `pvc-node-0`, even after restart
- **Use Cases:** HITL nodes with SQLite queues, Embassy nodes with export queues

```yaml
# Stateless node → Deployment
kind: FoundryNode
metadata:
  name: lint-node
spec:
  image: "registry/nodes/linter:v1.0"
  # No spec.storage - deployed as Deployment

---

# Stateful node → StatefulSet
kind: FoundryNode
metadata:
  name: hitl-reviewer
spec:
  image: "registry/nodes/hitl:v1.0"
  storage:
    size: "10Gi"
    mountPath: "/data"
  # spec.storage present - deployed as StatefulSet
```

### 2.3 Auto-Terminal Detection

If `spec.outputs` is **empty** (length 0), the Operator **automatically sets `isTerminal: true`**. This simplifies configuration:

- **If you define outputs:** The node is a routing node. `isTerminal` is false.
- **If you don't define outputs:** The node is a terminal node. `isTerminal` is auto-detected as true.

**Validation Rule:** If `spec.outputs` is empty AND `spec.terminalContract` is not set, the Operator rejects the FoundryNode with validation error: `TERMINAL_NODE_REQUIRES_CONTRACT`.

---

## 2.5 Truth-to-Index Sync Loop (Law CRDs)

The Operator enforces the **Single Source of Truth** principle for Law CRDs: `spec` is authoritative, `metadata.labels` are derived.

### 2.5.1 Sync Logic

The Operator watches all `Law` CRDs for create/update events and ensures labels match spec:

```go
func (r *LawReconciler) syncLabelsFromSpec(law *flowv1.Law) error {
    if law.ObjectMeta.Labels == nil {
        law.ObjectMeta.Labels = make(map[string]string)
    }

    // Tier: spec.tier → label
    law.ObjectMeta.Labels["flow.gideas.io/tier"] = fmt.Sprintf("%d", law.Spec.Tier)

    // Applies To: spec.appliesTo → label (empty becomes "")
    if law.Spec.AppliesTo != "" {
        law.ObjectMeta.Labels["flow.gideas.io/applies-to"] = law.Spec.AppliesTo
    }

    // Group: spec.group → label (optional)
    if law.Spec.Group != "" {
        law.ObjectMeta.Labels["flow.gideas.io/group"] = law.Spec.Group
    }

    // Type: spec.type → sanitized label
    // Sanitization: Replace "/" with "." for k8s label compatibility
    // Example: application/smt-lib → application.smt-lib
    sanitizedType := strings.ReplaceAll(law.Spec.Type, "/", ".")
    law.ObjectMeta.Labels["flow.gideas.io/type"] = sanitizedType

    return nil
}
```

### 2.5.2 Admission Webhook (Prevent Manual Drift)

The Operator implements a validating webhook that rejects manual label modifications:

```go
func (r *LawValidator) validateLabels(ctx AdmissionRequest) AdmissionResponse {
    law := decodeLaw(ctx.Object)
    
    // Extract expected labels from spec
    expectedTier := fmt.Sprintf("%d", law.Spec.Tier)
    expectedType := strings.ReplaceAll(law.Spec.Type, "/", ".")
    
    // Check for drift
    if law.ObjectMeta.Labels["flow.gideas.io/tier"] != expectedTier {
        return Reject("LABEL_DRIFT", "Tier label must match spec.tier")
    }
    
    if law.ObjectMeta.Labels["flow.gideas.io/type"] != expectedType {
        return Reject("LABEL_DRIFT", "Type label must match spec.type (sanitized)")
    }
    
    // Reject any manual modification of flow.gideas.io/* labels
    if ctx.Operation == "UPDATE" {
        oldLaw := decodeLaw(ctx.OldObject)
        if labelDrift(oldLaw, law, "flow.gideas.io/") {
            return Reject("LABEL_DRIFT", "Do not manually modify flow.gideas.io/* labels")
        }
    }
    
    return Allow()
}
```

### 2.5.3 Reconciliation on Create/Update

The reconciliation flow:

1. **Create Event:** Law CRD created → Operator reconciles → `syncLabelsFromSpec()` → Labels set → CRD updated
2. **Update Event:** Law CRD modified → Webhook validates → Operator reconciles → `syncLabelsFromSpec()` → Labels corrected → CRD updated
3. **Drift Detection:** If manual edit bypasses webhook → Next reconcile → Labels auto-corrected

### 2.5.4 Index Labels

These labels are automatically indexed by Elasticsearch/PostgreSQL for efficient queries:

| Label | Source | Query Example |
|-------|--------|---------------|
| `flow.gideas.io/tier` | `spec.tier` | `lables.flow.gideas.io/tier = "2"` |
| `flow.gideas.io/applies-to` | `spec.appliesTo` | `labels.flow.gideas.io/applies-to = "python"` |
| `flow.gideas.io/group` | `spec.group` | `labels.flow.gideas.io/group = "lg-7729"` |
| `flow.gideas.io/type` | `spec.type` (sanitized) | `labels.flow.gideas.io/type = "application.smt-lib"` |

### 2.3.1 Terminal Contract Enforcement Timing

Contract checks occur synchronously when a node returns `Complete()`. The Sidecar validates the terminal contract immediately and returns an error to the node on violation (`TERMINAL_CONTRACT_VIOLATED` with `ContractDetails`). Contract validation is synchronous and immediate.

### 2.4 Export Capability Validation

The `EXPORT` capability is **mutually inclusive** with `isTerminal: true`. Export is a finality event—it completes the local Workitem and creates a new one in the target Flow.

**Validation Rule:** If `capabilities` contains `"EXPORT"`, the node MUST have `isTerminal: true` (either explicitly or via empty `outputs`). If not, the Operator rejects the FoundryNode with validation error: `EXPORT_REQUIRES_TERMINAL`.

```yaml
# Example 1: Routing node (defines outputs)
kind: FoundryNode
metadata:
  name: quench-node
spec:
  outputs:
    - name: "pass"
      targetRole: "appraise"
  # isTerminal is implicitly false

---

# Example 2: Terminal node (no outputs)
kind: FoundryNode
metadata:
  name: terminal-approved
spec:
  outputs: []  # Empty - auto-detected as terminal
  isTerminal: true  # Auto-set by Operator
  terminalContract: "approved"  # REQUIRED when terminal

---

# Example 3: Export terminal node
kind: FoundryNode
metadata:
  name: export-to-execution
spec:
  capabilities:
    - "READ:artefact/*"
    - "EXPORT"                    # Requires isTerminal
  outputs: []                     # Terminal node
  isTerminal: true
  terminalContract: "exported"
```
