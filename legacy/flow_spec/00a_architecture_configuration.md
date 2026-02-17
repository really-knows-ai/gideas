# Atomic Foundry Flow: Architecture Configuration

## 1. Operator Bootstrap Sequence

The Flow Operator follows a standard Kubernetes operator pattern.

**Bootstrap Order:**

1. **CRD Registration (Cluster-Level):** Apply CRD definitions to the cluster.
2. **Operator Deployment (Namespace-Level):** Deploy the `flow-operator` Deployment.
3. **Flow Configuration (The Trigger):** Apply the `FoundryFlow` CRD.

### 1.1 Helm Chart Structure

```
foundry-flow/
├── Chart.yaml
├── values.yaml
├── crds/
│   ├── foundryflow-crd.yaml
│   ├── foundrynode-crd.yaml
│   └── workitem-crd.yaml
└── templates/
    ├── namespace.yaml
    ├── rbac.yaml
    ├── operator-deployment.yaml
    ├── archivist-statefulset.yaml
    ├── librarian-deployment.yaml
    └── foundryflow-config.yaml
```

**Why Single Chart?**
- One Helm Release = One Namespace = One Flow
- System services share gRPC protocol versions with Operator
- All components deploy together, avoiding partial states

### 1.2 Deployment Commands

```bash
# Deploy the Ideation Flow
helm install flow-ideate ./foundry-flow \
  --create-namespace \
  --namespace flow-ideate

# Platform admin (once per cluster)
helm install foundry-crds ./foundry-crds

# Flow admin (per namespace)
helm install flow-ideate ./foundry-flow --namespace flow-ideate
```

---

## 2. Cross-Flow Collaboration

Multiple Flows can collaborate while respecting sovereignty. **Data Gravity**: Workitems are immutable residents of their namespace.

### 2.1 Collaboration Model: Export → Transfer → Import

```
      [Flow A]                      [Flow B]
   (Sovereign Realm)             (Sovereign Realm)
          │                             │
    [Export Node] ──(HTTPS/gRPC)──▶ [Ingress Node]
          │                             │
     Sign(Root A)                  Verify(Treaty A)
          │                        Stamp(Root B)
          │                             │
     [Completed]                   [New Workitem]
```

| Phase | Location | Action |
|-------|----------|--------|
| **Export** | Flow A | Terminal Node bundles workitem + artefacts into signed bundle |
| **Transfer** | Network | Bundle transmitted via HTTPS/gRPC |
| **Import** | Flow B | Ingress Node validates signature, creates new local Workitem |

### 2.2 Trust Models

| Pattern | Trust Anchor | Use Case |
|---------|--------------|----------|
| **Treaty** | Pinned CA per peer | Atomic Flows collaborating |
| **Federation** | Shared State Root | Sibling Flows under same Governor (v2) |

**Treaty Configuration:**
```yaml
spec:
  treaties:
    - name: "flow-a"
      caCert: |
        -----BEGIN CERTIFICATE-----
        ...
      allowedSubjects: ["export-node"]
```

### 2.3 Chain of Custody Reset (Naturalization)

When artefacts cross Flow boundaries, **foreign stamps are cryptographically ignored**:
- Preserved for audit (`status.context.foreignProvenance`)
- Ingress Node applies local "Naturalization Stamp"
- Local `GovernedArtefact` rules then apply

---

## 3. `FoundryFlow` CRD (The Singleton Configuration)

**Exactly one `FoundryFlow` instance per namespace.**

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryFlow
metadata:
  name: config
  namespace: flow-ideate
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
    - terminal-approved
    - terminal-rejected
  
  federationMode: "Atomic"
  
  governance:
    populusThreshold: 50
    defaultFindingDecay: "30d"
    standardNodeTimeout: "30s"
    maxNodeTimeout: "300s"
  
  retention:
    workitemTTL: "30d"
    artefactRetention:
      maxVersions: 10
      maxAge: "7d"
  
  embeddingConfig:
    provider: "openai"
    model: "text-embedding-3-small"
    batchSize: 100
  
  assayPolicy:
    - name: "standard-dispute"
      matchSeverity: ["LOW", "MEDIUM"]
      consensusStrategy: "SimpleMajority"
      maxRounds: 3
      juryProfile:
        - name: "pragmatist"
          model: "gpt-4o"
          systemPrompt: "..."
  
  security:
    requiredKernelLayer: "sha256:e3b0c44..."
```

### 3.1 Key Fields

| Field | Description |
|-------|-------------|
| `entryContract` | What workitems can enter this flow |
| `terminalContracts` | What must be true to exit |
| `entryNode` | First node in the flow |
| `federationMode` | `Atomic` (v1) or `Federated` (v2) |
| `governance` | Thresholds, timeouts, decay settings |
| `retention` | etcd hygiene policies |
| `assayPolicy` | Jury configuration for disputes |


---

## 4. Upgrade Strategy

Foundry Flow follows semantic versioning and standard Kubernetes deployment patterns.

### 4.1 Versioning Policy

| Version Change | Compatibility | Migration |
|---|---|---|
| **Patch** (1.0.x) | Fully backward-compatible | Rolling update |
| **Minor** (1.x.0) | Backward-compatible; additive CRD changes only | Rolling update |
| **Major** (x.0.0) | May include breaking changes | Migration guide provided |

### 4.2 Component Upgrades

All Foundry Flow components use standard Kubernetes rolling deployments. The Helm chart manages coordinated upgrades across:

- Flow Operator
- System Services (Librarian, Archivist, Flow Monitor, Backup Service)
- CRD definitions

**Upgrade Command:**
```bash
helm upgrade flow-ideate ./foundry-flow --namespace flow-ideate
```

### 4.3 In-Flight Workitem Handling

Workitems that are mid-processing during an upgrade complete normally on the existing code path. New workitems are processed by the upgraded components. The system maintains consistency through:

- Kubernetes Deployment rollout strategy (default: RollingUpdate)
- Sidecar version alignment with node pods
- CRD backward compatibility within minor versions

### 4.4 CRD Schema Evolution

Within a major version, CRD changes are additive only (new optional fields). This ensures existing resources remain valid after upgrade. Breaking schema changes are reserved for major version releases and accompanied by migration tooling.
