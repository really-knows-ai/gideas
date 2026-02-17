# Foundry Node: Configuration CRD Schema

## 1. The Unified Capabilities Model

We use Resource URN syntax to define granular permissions. Capability strings follow the grammar:

```
capability := verb ':' resource [ '/' kind ]
verb       := 'READ' | 'WRITE' | 'INSPECT' | 'APPROVE' | 'CREATE' | 'EXPORT' | 'LISTEN' | 'QUEUE'
resource   := 'artefact' | 'law' | 'workitem' | 'telemetry' | 'topology' | 'flow' | 'server'
kind       := '*' | identifier  ;; '*' matches all kinds, identifier for specific kinds
```

Examples:
- `WRITE:artefact/petition-draft` - Write specific artefact kind
- `WRITE:artefact/*` - Write any artefact kind
- `READ:law` - Read laws (no kind qualifier)
- `INSPECT:artefact/*` - Apply inspection stamps to any artefact
- `APPROVE:artefact/*` - Apply approval stamps to any artefact (requires laws)
- `EXPORT` - Export capability (no resource qualifier)
- `QUEUE:server` - Enable Federated Queue Mesh
- `LISTEN:telemetry` - Subscribe to telemetry events

**Knowledge Access Capabilities (Read-Only):**
* **`READ:law`**: Access to the Law Cache. Enables `SearchLibrary` RPC.
* **`READ:topology`**: Access to the Node Graph. Enables `GetTopology` RPC.
* **`READ:flow`**: Access to Flow Config. Enables `GetFlowConfig` RPC.
* **`READ:workitem`**: Access to Workitem Cache. Enables `GetWorkitem` RPC.

**Stamp Capabilities:**
* **`INSPECT:artefact/{kind}`**: Apply `inspection` stamps to artefacts. Lightweight review markers.
* **`APPROVE:artefact/{kind}`**: Apply `approval` stamps to artefacts. Governance certifications requiring law citations.

**Telemetry Sensory Loop:**
* **`LISTEN:telemetry`**: Authorizes Sidecar to subscribe to FlowMonitor.

**Queue Operations:**
* **`QUEUE:server`**: Enables the internal gRPC `QueuePeer` service for Federated Queue Mesh.

---

## 2. `FoundryNode` CRD Schema

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: security-quench
spec:
  image: "registry/nodes/security-scanner:v2.0"
  roles:
    - "security-reviewer"
    - "vulnerability-scanner"
  timeout: "60s"
  concurrency: 1
  
  storage:
    size: "1Gi"
    mountPath: "/data"
  
  volumes:
    - name: diplomatic-pouch
      emptyDir:
        sizeLimit: "10Gi"
  
  volumeMounts:
    - name: diplomatic-pouch
      mountPath: /var/run/foundry/pouch
  
  capabilities:
    - "READ:artefact/python-source"
    - "INSPECT:artefact/python-source"
    - "LISTEN:telemetry"
  
  subscriptions:
    - "foundry.cost.*"
    - "foundry.legal.citation"
  
  outputs:
    - name: "pass"
      targetRole: "appraiser"
    - name: "fail"
      targetRole: "refiner"
    - name: "deadlock"
      target: "assay-node"
  
  isTerminal: false
  terminalContract: ""
  
  env:
    - name: SCAN_DEPTH
      value: "comprehensive"
```

> Versioning Note: This specification standardizes examples on `flow.gideas.io/v1`.
> Where prior documents referenced `v2`, features are equivalent for the purposes
> of this contract unless explicitly documented in
> governance_spec/08_versioning_and_migration.md.

### 2.1 Field Reference

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `image` | string | (required) | Container image for the node |
| `roles` | []string | `[]` | Capabilities this node provides |
| `timeout` | duration | `30s` | Workitem execution deadline (see `FoundryFlow.spec.governance.standardNodeTimeout`) |
| `capabilities` | []string | `[]` | URN-based permissions |
| `subscriptions` | []string | `[]` | Telemetry event patterns |
| `outputs` | []Output | `[]` | Named routing channels |
| `isTerminal` | bool | (auto-derived) | Auto-derived from `outputs` |
| `terminalContract` | string | `""` | Required if terminal |
| `env` | []EnvVar | `[]` | Environment variables |
| `storage` | StorageSpec | (none) | PVC configuration |
| `volumes` | []Volume | `[]` | Raw K8s volumes |
| `volumeMounts` | []VolumeMount | `[]` | Volume mounts |
| `concurrency` | int | `1` | Max concurrent workitems per pod |

### 2.2 Concurrency Behavior

The `concurrency` field controls how many workitems a single pod can process simultaneously.

| Value | Behavior |
|-------|----------|
| `1` (default) | Sequential processing. Pod handles one workitem at a time. |
| `N > 1` | Concurrent processing. Pod can handle up to N workitems in parallel. Node handlers MUST be thread-safe. |
| `0` | Unlimited concurrency. Pod accepts all assigned workitems without capacity limit. Use with caution. |

When `concurrency` is omitted from the CRD, the Operator defaults to `1` for safe, sequential processing. The Sidecar's readiness probe (`/readyz`) returns `503 Service Unavailable` when `activeWorkitemCount >= concurrency`, preventing the Operator from assigning additional work.

### 2.3 Terminal Node Auto-Detection

The Operator automatically derives `isTerminal`:
- If `outputs: []` (empty): Node is terminal. Set `terminalContract` (required).
- If `outputs` has entries: Node is routing.

> **⚠️ CRITICAL: Outputs are Stateless Routing Labels**
>
> Node outputs are strictly **routing instructions**, not **validation results**. An output named `"approved"` does not make the Workitem "valid."

---

## 3. Storage Configuration

```yaml
spec:
  storage:
    size: "1Gi"
    storageClass: "standard"
    mountPath: "/data"
    accessMode: "ReadWriteOnce"
```

**Deployment Impact:** When `spec.storage` is defined, the Operator deploys the node as a **StatefulSet**.

**QUEUE:server Requirement:** Nodes with `capabilities: ["QUEUE:server"]` MUST define `spec.storage`.

---

## 4. Volume Configuration

```yaml
spec:
  volumes:
    - name: diplomatic-pouch
      emptyDir:
        sizeLimit: "10Gi"
    - name: app-config
      configMap:
        name: node-config
  
  volumeMounts:
    - name: diplomatic-pouch
      mountPath: /var/run/foundry/pouch
    - name: app-config
      mountPath: /etc/config
      readOnly: true
```

**Operator Behavior:** When `volumeMounts` are specified, the Operator injects the **same mounts into the Sidecar container**.

### 4.1 The Diplomatic Pouch Pattern

The `diplomatic-pouch` volume is a recommended pattern for nodes that need to exchange large temporary files with the Sidecar during export/import operations. It provides a shared ephemeral storage space that is automatically cleaned up when the pod terminates.

**Use Cases:**
- Export bundle assembly before transmission
- Import bundle extraction before processing
- Large artefact staging during cross-flow collaboration

The Sidecar automatically detects volumes mounted at `/var/run/foundry/pouch` and uses them for bundle operations instead of in-memory buffers.

---

## 5. Related Documents

- [03a_configuration_patterns.md](./03a_configuration_patterns.md) - Routing, roles, governance patterns
