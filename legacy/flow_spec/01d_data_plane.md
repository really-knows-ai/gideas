# Atomic Foundry Flow: Data Plane

## 1. Overview

The Data Plane is where work actually happens. It comprises the execution environment (Nodes), storage layer (Archivist), and the work artefacts themselves.

### Components

| Component | Role | Plane Boundary |
|-----------|------|----------------|
| **Node Pods** | Execute user-defined logic | Pure Data Plane |
| **Archivist** | Store/retrieve artefact content | Pure Data Plane |
| **Sidecar** | Broker authenticated requests | Security Plane → Data Plane bridge |
| **Artefacts** | The work product (bytes) | Data Plane state |
| **Feedback** | Critique and dispute records | Data Plane state |

---

## 2. Characteristics

### 2.1 Stateless Workers ("Goldfish Memory")

- Node Pods are persistent (boot once, process many Workitems)
- Node memory is stateless (each Workitem execution starts fresh)
- Infrastructure state (LLM models, connection pools) ≠ session state
- If a Workitem loops back, it may hit a different replica

### 2.2 Data Gravity

- Artefact content lives in the Archivist (blob storage)
- Artefact metadata lives in the Workitem CRD (hash, version, passport)
- Large content is stored in the Archivist and referenced by hash

### 2.3 External Access

- Nodes have direct, uninhibited network access to external APIs
- Network security is an infrastructure concern (NetworkPolicies, Service Mesh)
- The Data Plane accesses external services directly

---

## 3. Responsibility Boundaries

| Responsibility | Plane | Handler |
|----------------|-------|----------|
| Work execution | Data Plane | Node Pods |
| Routing decisions | Control Plane | Operator (executes routing instructions from Nodes) |
| Law creation | Governance Plane | Librarian (manages law lifecycle from Node Findings) |
| Authentication | Security Plane | Sidecar (holds credentials, brokers requests) |
| Cross-flow transfer | Federation Plane | Export/Import (creates bundles, handles trust) |

---

## 4. Data Plane Services

### 4.1 Node Services

- Persistent `Deployments` or `StatefulSets` (if storage required)
- Exposed via internal K8s `Services`
- Capabilities defined by `FoundryNode` CRD permissions
- Probes target Sidecar health endpoints

### 4.2 The Archivist

- Pluggable blob storage (PVC, S3, GCS)
- Content-addressable by hash
- Versioned artefact history
- No business logic—pure storage

---

## 5. Error Codes

| Error Code | Condition | Response |
|------------|-----------|----------|
| `INVALID_OUTPUT` | Output name not in Node's config | Workitem → Failed |
| `NODE_NOT_FOUND` | Target node doesn't exist | Workitem → Failed |
| `NOT_TERMINAL_NODE` | Complete() called on non-terminal node | Workitem → Failed |
| `INVALID_TERMINAL_CONTRACT` | Node's terminalContract not in Flow | Workitem → Failed |
| `ARTEFACT_MISSING` | Required artefact not found | Workitem → Failed |
| `VALIDITY_NOT_MET` | Required stamps missing from passport | Workitem → Failed |
| `SIGNATURE_INVALID` | Stamp signature verification failed | Workitem → Failed |
| `CERTIFICATE_CHAIN_INVALID` | Certificate chain doesn't reach State Root | Workitem → Failed |
| `EXECUTION_TIMEOUT` | Execution deadline exceeded, no `timeout` output defined | Workitem → Failed |
| `ENTRY_CONTRACT_FAILED` | Workitem doesn't meet entry requirements | Workitem not created |
| `THRASH_DETECTED` | Node visit count exceeded maxVisits | Workitem → Failed |
| `NO_AVAILABLE_TARGET` | Target role has no ready nodes | Workitem → Failed |
