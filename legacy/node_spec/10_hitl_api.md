# Foundry Node: HITL Node API & Implementations

## 1. Overview

HITL Nodes are SDK-native primitives for workflows requiring human decisions. They extend `AbstractHitlNode` to provide automatic queue management, REST API exposure, and configurable persistence.

### Problem Statement

Without native HITL support, developers must:
- Manually implement blocking logic with channels/mutexes
- Build custom queue management and API exposure
- Handle persistence and Pod restart resilience themselves
- Implement escalation logic for unavailable reviewers

### Solution

The `AbstractHitlNode` base class provides:
- **Automatic Parking Lot:** Workitems block in `Assigned()` until human decision
- **Standard REST API:** Queue inspection and decision submission via `/queue` endpoints
- **Federated Queue Mesh:** Unified "Global Queue" view across scaled replicas (see [11_federated_queue_mesh.md](./11_federated_queue_mesh.md))
- **SDK-Managed Persistence:** Standard SQLite schema at `{storage.mountPath}/queue.db`
- **Escalation Support:** Use `timeout` outputs for automatic escalation chains

---

## 2. API Contract (The Human Interface)

All HITL Nodes expose a standard HTTP API for external tooling (CLI, dashboards, mobile apps).

### 2.1 GET /queue

List workitems waiting for human decision.

**Query Parameters:**
| Parameter | Type | Description |
|-----------|------|-------------|
| `status` | string | Filter by status: `waiting`, `claimed` |
| `limit` | int | Max results (default: 100) |
| `offset` | int | Pagination offset |

**Response:**
```json
{
  "items": [
    {
      "id": "workitem-abc123",
      "enqueuedAt": "2024-01-15T10:30:00Z",
      "status": "waiting",
      "type": "petition-review",
      "summary": "Petition #4521 - License Amendment"
    }
  ],
  "total": 47,
  "limit": 100,
  "offset": 0
}
```

### 2.2 GET /queue/{id}

Fetch full metadata and context for a specific workitem.

**Response:**
```json
{
  "id": "workitem-abc123",
  "enqueuedAt": "2024-01-15T10:30:00Z",
  "status": "claimed",
  "claimedAt": "2024-01-15T10:35:00Z",
  "workitem": {
    "metadata": { "name": "workitem-abc123", "namespace": "petitions" },
    "spec": { "type": "petition-review" },
    "status": {
      "artefacts": [...],
      "feedback": [...]
    }
  },
  "context": {
    "previousDecisions": [...],
    "relatedWorkitems": [...],
    "escalationChain": ["manager-review", "director-review"]
  }
}
```

### 2.3 POST /queue/{id}/claim

Claim a workitem for review (prevents others from deciding).

**ID Semantics:**
- `{id}` is the Workitem ID. The Federated Queue Mesh uses the Workitem ID as the canonical identifier.
- Shard ownership is identified via `shard_id` at the transport layer; REST clients operate purely on Workitem IDs.

**Logic:**
1. Verify `status == waiting`
2. Transition to `status = claimed`
3. Return error if already claimed (`409 Conflict`)

**Response:**
```json
{
  "id": "workitem-abc123",
  "status": "claimed",
  "claimedAt": "2024-01-15T10:35:00Z"
}
```

> **Note:** The HITL Node tracks **status**. Assignment mapping ("who has this item?") is the responsibility of the consuming layer (Dashboard/BFF).

### 2.4 POST /queue/{id}/decide

Submit a decision, unblocking the handler.

**Request (HitlSortNode):**
```json
{
  "output": "approved",
  "comment": "Reviewed and approved per policy X-123"
}
```

**Request (HitlAppraiseNode):**
```json
{
  "output": "needs-revision",
  "feedback": [
    {
      "type": "revision",
      "target": "petition-draft",
      "content": "Section 4.1 contradicts existing statute",
      "severity": "blocking"
    }
  ],
  "comment": "Good progress, needs minor revisions"
}
```

**Response:**
```json
{
  "id": "workitem-abc123",
  "status": "decided",
  "decidedAt": "2024-01-15T11:00:00Z",
  "output": "needs-revision"
}
```

### 2.5 POST /queue/{id}/release

Release a claimed workitem back to the queue (unclaim).

**Response:**
```json
{
  "id": "workitem-abc123",
  "status": "waiting"
}
```

---

## 3. Concrete Implementations

### 3.1 HitlSortNode

A human triage node for classification/routing decisions.

**Use Cases:**
- Petition type classification
- Priority assignment
- Escalation decisions
- Exception handling

**Decision Schema:**
```go
type SortDecision struct {
    Output  string  // Target output channel name
    Comment string  // Audit trail
}
```

**Example CRD:**
```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: petition-triage
spec:
  image: "registry/nodes/hitl-sort:v1.0"
  roles: ["human-classifier"]
  timeout: "4h"
  concurrency: 500
  storage:
    size: "1Gi"
    mountPath: "/data"
  
  outputs:
    - name: "standard"
      targetRole: "standard-processor"
    - name: "expedited"
      targetRole: "expedited-processor"
    - name: "complex"
      target: "specialist-review"
    - name: "timeout"
      target: "supervisor-triage"
```

### 3.2 HitlAppraiseNode

A human review node for feedback collection and quality assessment.

**Use Cases:**
- Document review with feedback
- Code review with comments
- Approval workflows with conditions
- Quality gates with human judgment

**Decision Schema:**
```go
type AppraiseDecision struct {
    Output   string     // Target output channel name
    Feedback []Feedback // Feedback items to attach
    Comment  string     // Audit trail
}

type Feedback struct {
    Type     string  // "revision", "suggestion", "blocker", "approval"
    Target   string  // Artefact kind this feedback applies to
    Content  string  // The feedback content
    Severity string  // "blocking", "advisory"
}
```

**Example CRD:**
```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: legal-review
spec:
  image: "registry/nodes/hitl-appraise:v1.0"
  roles: ["legal-reviewer", "human-appraiser"]
  timeout: "24h"
  concurrency: 100
  storage:
    size: "2Gi"
    mountPath: "/data"
  
  capabilities:
    - "READ:artefact/petition-draft"
    - "READ:law"
  
  outputs:
    - name: "approved"
      targetRole: "next-stage"
    - name: "needs-revision"
      targetRole: "refiner"
    - name: "rejected"
      target: "terminal-rejected"
    - name: "timeout"
      target: "senior-legal-review"
```

---

## 4. Telemetry

HITL nodes emit standard telemetry events:

| Event | When | Payload |
|-------|------|---------|
| `foundry.hitl.enqueued` | Workitem enters queue | `{workitemId, nodeId, queueDepth}` |
| `foundry.hitl.claimed` | Item claimed | `{workitemId, waitTime}` |
| `foundry.hitl.released` | Item unclaimed | `{workitemId, claimDuration}` |
| `foundry.hitl.decided` | Decision submitted | `{workitemId, output, decisionTime}` |
| `foundry.hitl.escalated` | Timeout triggered escalation | `{workitemId, fromNode, toNode, waitTime}` |
| `foundry.hitl.timeout_failed` | Timeout with no escalation route | `{workitemId, nodeId, waitTime}` |

> **Note:** Telemetry tracks status transitions. The consuming layer (Dashboard) correlates decisions with human identity.
