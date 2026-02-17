# Foundry Flow: Helm Configuration Reference

## 1. Purpose

This document defines the **Default Values Contract** for the `foundry-flow` Helm chart. It centralizes all configurable parameters scattered across the spec into a single, documented reference.

**Goals:**
1. **Eliminate Magic Numbers:** Define where operational defaults live so operators can tune without recompiling.
2. **Define Infrastructure Constraints:** Provide safe production baselines to prevent OOM kills on Day 1.
3. **Centralize Image Provenance:** Allow air-gapped/enterprise environments to override registries in one place.
4. **Expose Hidden Switches:** Make tunable policies visible to deployers.

---

## 2. Chart Structure

```
foundry-flow/
├── Chart.yaml
├── values.yaml                      # THIS DOCUMENT
├── crds/                            # Optional: included for convenience
│   ├── foundryflow-crd.yaml
│   ├── foundrynode-crd.yaml
│   ├── workitem-crd.yaml
│   ├── workitemtype-crd.yaml
│   ├── governedartefact-crd.yaml
│   ├── treaty-crd.yaml
│   ├── reviewhearing-crd.yaml
│   └── law-crd.yaml
└── templates/
    ├── namespace.yaml
    ├── rbac.yaml
    ├── operator-deployment.yaml
    ├── archivist-statefulset.yaml
    ├── librarian-deployment.yaml
    ├── law-search-deployment.yaml
    ├── flow-monitor-deployment.yaml
    ├── backup-service-deployment.yaml
    └── foundryflow-config.yaml
```

---

## 3. Default Values Reference

The complete `values.yaml` is maintained as a separate file:

📄 **[helm/values.yaml](helm/values.yaml)**

This file contains all configurable parameters with inline documentation. The sections are:

| Section | Purpose |
|---------|---------|
| `global.*` | Image registry, pull policy, secrets |
| `operator.*` | Flow Operator resources, probes, reconciliation |
| `governance.*` | Populus threshold, decay times, conflict threshold |
| `execution.*` | Timeouts, thrash guard limit, concurrency |
| `retention.*` | Workitem TTL, artefact version limits |
| `archivist.*` | Storage backend, hot tier cache |
| `librarian.*` | Law database, snapshot interval |
| `lawSearch.*` | Replica count, catch-up timeout |
| `flowMonitor.*` | Event buffer, flush interval, friction aggregation |
| `embedding.*` | Provider, model, dimensions |
| `sidecar.*` | Injected container config |
| `security.*` | Auth mode, kernel validation, pod security |
| `assay.*` | Jury deliberation defaults |
| `observability.*` | Metrics, tracing, logging |

---

## 4. Parameter Categories

### 4.1 Infrastructure Constraints

These parameters prevent resource exhaustion on Day 1:

| Parameter | Default | Rationale |
|-----------|---------|-----------|
| `operator.resources.limits.memory` | `512Mi` | Scales with active Workitem CRD count |
| `librarian.resources.limits.memory` | `2Gi` | sqlite-vec vector operations are memory-intensive |
| `archivist.storage.size` | `10Gi` | Scales with artefact volume and version history |
| `archivist.hotTier.cacheSize` | `256Mi` | In-memory cache for frequently accessed artefacts |

### 4.2 Execution Limits ("Magic Numbers")

These parameters control workitem execution behavior:

| Parameter | Default | Location in Spec | Notes |
|-----------|---------|------------------|-------|
| `execution.standardNodeTimeout` | `30s` | [00_architecture_overview.md](00_architecture_overview.md) | Per-workitem deadline |
| `execution.maxNodeTimeout` | `300s` | [00_architecture_overview.md](00_architecture_overview.md) | Upper bound on node timeout |
| `execution.maxVisits` | `30` | [01_operator_and_routing.md](01_operator_and_routing.md) | Thrash Guard limit |
| `governance.populusThreshold` | `50` | [00_architecture_overview.md](00_architecture_overview.md) | Citations for Tier 1→2 promotion |
| `governance.conflictThreshold` | `0.85` | [02_system_services.md](02_system_services.md) | Semantic similarity for conflict detection |

### 4.3 Retention & Cleanup

These parameters control etcd hygiene and storage costs:

| Parameter | Default | Impact |
|-----------|---------|--------|
| `retention.workitemTTL` | `30d` | etcd size growth |
| `retention.artefacts.maxVersions` | `10` | Archivist PVC usage |
| `retention.artefacts.maxAge` | `7d` | Archivist PVC usage |
| `governance.defaultFindingDecay` | `30d` | Tier 1 law accumulation |

### 4.4 High-Availability Settings

These parameters affect availability and resilience:

| Parameter | Default | Production Recommendation |
|-----------|---------|---------------------------|
| `lawSearch.replicas` | `2` | **Minimum 2** for rolling update availability |
| `operator.replicas` | `1` | Single leader (HA via leader election) |
| `librarian.snapshotInterval` | `60m` | Cold snapshot frequency for DR |

---

## 5. Environment-Specific Overrides

### 5.1 Development Environment

```yaml
# values-dev.yaml
global:
  imagePullPolicy: Always

operator:
  logLevel: debug

execution:
  standardNodeTimeout: "60s"  # More time for debugging

lawSearch:
  replicas: 1  # Acceptable downtime

archivist:
  storage:
    size: "1Gi"

observability:
  tracing:
    enabled: true
    sampleRate: 1.0  # Trace everything in dev
```

### 5.2 Production Environment

```yaml
# values-prod.yaml
global:
  imagePullPolicy: IfNotPresent
  imagePullSecrets:
    - name: prod-registry-secret

operator:
  logLevel: info
  resources:
    limits:
      memory: "1Gi"

lawSearch:
  replicas: 3  # High availability

archivist:
  storage:
    storageClass: "fast-ssd"
    size: "100Gi"

librarian:
  resources:
    limits:
      memory: "4Gi"

security:
  requiredKernelLayer: "sha256:abc123..."  # Lock to verified base image

observability:
  tracing:
    enabled: true
    endpoint: "https://otel-collector.monitoring:4317"
    sampleRate: 0.01  # 1% sampling in prod
```

### 5.3 Air-Gapped / Enterprise Environment

```yaml
# values-airgapped.yaml
global:
  imageRegistry: "internal.corp.com/foundry"
  imagePullSecrets:
    - name: internal-registry-creds

embedding:
  provider: "ollama"
  endpoint: "http://ollama.internal:11434"
  model: "nomic-embed-text"
```

---

## 6. Deployment Commands

```bash
# Development deployment
helm install flow-ideate ./foundry-flow \
  --create-namespace \
  --namespace flow-ideate \
  -f values-dev.yaml

# Production deployment
helm install flow-ideate ./foundry-flow \
  --create-namespace \
  --namespace flow-ideate \
  -f values-prod.yaml

# Upgrade with specific override
helm upgrade flow-ideate ./foundry-flow \
  --namespace flow-ideate \
  --set execution.standardNodeTimeout="45s"

# View computed values
helm get values flow-ideate --namespace flow-ideate

# Dry-run to preview changes
helm upgrade flow-ideate ./foundry-flow \
  --namespace flow-ideate \
  -f values-prod.yaml \
  --dry-run
```

---

## 7. Validation & Constraints

The chart includes validation rules to prevent invalid configurations:

| Constraint | Rule | Error |
|------------|------|-------|
| Timeout bounds | `execution.standardNodeTimeout <= execution.maxNodeTimeout` | `INVALID_TIMEOUT_BOUNDS` |
| Replica minimum | `lawSearch.replicas >= 1` | `INVALID_REPLICA_COUNT` |
| Storage class | If `storageClass` set, must exist in cluster | `STORAGE_CLASS_NOT_FOUND` |
| Embedding dimensions | Must match model output | `EMBEDDING_DIMENSION_MISMATCH` |

---

## 8. Migration Notes

When upgrading from a previous version:

1. **Backup CRDs:** Export all `FoundryFlow`, `FoundryNode`, `Workitem` CRDs before upgrade.
2. **Review Defaults:** New versions may change defaults. Run `helm diff upgrade` to preview.
3. **Stateful Services:** Librarian and Archivist use PVCs. Ensure storage is preserved during upgrade.
4. **CRD Updates:** Apply CRD changes before chart upgrade if schema changed.

```bash
# Pre-upgrade checklist
kubectl get foundryflow,foundrynode,workitem -n flow-ideate -o yaml > backup.yaml
helm diff upgrade flow-ideate ./foundry-flow -n flow-ideate -f values-prod.yaml
```
