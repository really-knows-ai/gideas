# Foundry Flow: Multi-Tenancy

**Status:** v1 Specification

## 1. Overview

Foundry Flow is inherently multi-tenant by design. Each Flow is deployed to its own Kubernetes namespace, providing natural resource and security isolation without requiring additional configuration.

## 2. Isolation Model

| Isolation Level | Mechanism | Scope |
| :--- | :--- | :--- |
| **Flow** | Kubernetes Namespace | Each Flow operates in its own namespace with dedicated CRDs, services, and storage |
| **Team** | Multiple Namespaces | A team can deploy multiple Flows to separate namespaces |
| **Cluster** | Shared Infrastructure | Multiple teams can share a single Kubernetes cluster |

## 3. Resource Isolation

Standard Kubernetes mechanisms provide resource isolation between Flows:

| Mechanism | Purpose | Example |
| :--- | :--- | :--- |
| **ResourceQuota** | Limit total resources per namespace | `requests.cpu: 10`, `requests.memory: 20Gi` |
| **LimitRange** | Set default and max limits per pod | `default.memory: 512Mi`, `max.memory: 4Gi` |
| **PriorityClass** | Ensure critical Flows get scheduling priority | `system-cluster-critical` for production Flows |

### Example ResourceQuota

```yaml
apiVersion: v1
kind: ResourceQuota
metadata:
  name: flow-quota
  namespace: flow-team-alpha
spec:
  hard:
    requests.cpu: "20"
    requests.memory: 40Gi
    limits.cpu: "40"
    limits.memory: 80Gi
    persistentvolumeclaims: "10"
    pods: "50"
```

## 4. Security Isolation

Security isolation between Flows is achieved through Kubernetes-native mechanisms:

| Mechanism | Purpose |
| :--- | :--- |
| **RBAC** | Restrict which users/service accounts can access each namespace |
| **NetworkPolicy** | Restrict network traffic between namespaces |
| **PodSecurityPolicy / PodSecurityAdmission** | Enforce security standards per namespace |

### Example NetworkPolicy

By default, Flows should not communicate with each other. Cross-flow communication is handled explicitly through the Treaty mechanism.

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: deny-cross-namespace
  namespace: flow-team-alpha
spec:
  podSelector: {}
  policyTypes:
    - Ingress
    - Egress
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: flow-team-alpha
  egress:
    - to:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: flow-team-alpha
    - to:
        - namespaceSelector:
            matchLabels:
              foundry.io/system: "true"  # Allow access to shared services
```

## 5. Shared Services

Certain Foundry Flow services may be shared across multiple Flows for efficiency:

| Service | Sharing Model | Rationale |
| :--- | :--- | :--- |
| **Governor** | Cluster-wide singleton | Single certificate authority for the cluster |
| **Archivist** | Per-Flow or shared | Shared reduces storage costs; per-Flow provides stronger isolation |
| **Embedding Provider** | Cluster-wide | GPU resources are expensive; sharing is cost-effective |

## 6. Noisy Neighbor Prevention

To prevent one Flow from impacting others ("noisy neighbor" problem):

1. **Apply ResourceQuotas** to each Flow namespace to cap resource consumption.
2. **Use LimitRanges** to prevent individual pods from consuming excessive resources.
3. **Monitor per-namespace metrics** using the `namespace` label on Prometheus metrics.
4. **Set PriorityClasses** to ensure critical Flows are scheduled first during resource contention.

## 7. Recommended Namespace Naming Convention

```
flow-{team}-{environment}-{flow-name}
```

Examples:
- `flow-platform-prod-onboarding`
- `flow-ml-staging-model-review`
- `flow-security-prod-vulnerability-triage`
