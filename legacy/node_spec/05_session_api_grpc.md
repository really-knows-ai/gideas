# Foundry Node: Session API (gRPC)

## 5.2 Session API Contract (Node ↔ Sidecar gRPC Interface)

The Node SDK communicates with the Sidecar via bidirectional gRPC. The Sidecar implements the **Producer API** (Node calls Sidecar), and the Node implements the **Consumer API** (Sidecar calls Node).

### Producer API (Node → Sidecar)

```protobuf
service SidecarSession {
  // Artefact operations
  rpc StoreArtefact(StoreArtefactRequest) returns (StoreArtefactResponse);
  rpc FetchArtefact(FetchArtefactRequest) returns (FetchArtefactResponse);
  rpc GetArtefactMetadata(ArtefactMetadataRequest) returns (ArtefactMetadataResponse);
  rpc StampArtefact(StampRequest) returns (Empty);
  
  // Law operations
  rpc RecordFinding(NewFinding) returns (Empty);
  rpc SearchLibrary(LibraryQuery) returns (stream Law);
  rpc Cite(CitationRequest) returns (Empty);
  
  // Feedback operations (threaded, artefact-scoped)
  rpc AddFeedback(AddFeedbackRequest) returns (FeedbackID);
  rpc ResolveFeedback(ResolveFeedbackRequest) returns (Empty);
  rpc RejectFix(RejectFixRequest) returns (Empty);
  rpc UpdateFeedbackState(FeedbackStateUpdate) returns (Empty);
  
  // Workitem operations
  rpc CreateWorkitem(WorkitemSpec) returns (WorkitemID);
  rpc CompleteWorkitem(WorkitemResult) returns (Empty);
  rpc ValidateArtefact(ValidateRequest) returns (ValidateResponse);
  
  // Telemetry & Health
  rpc RecordTelemetry(TelemetryEvent) returns (Empty);
  rpc Heartbeat(Empty) returns (Empty);
  
  // Topology
  rpc GetNodesByRole(RoleQuery) returns (NodeList);
}

// ─────────────────────────────────────────────────────────────────────────────
// Telemetry Messages
// ─────────────────────────────────────────────────────────────────────────────

message TelemetryEvent {
  string type = 1;              // Event type identifier
  map<string, string> context = 2;  // Context (e.g., workitem_id)
  bytes payload = 3;            // JSON-encoded payload
}

// Friction Report Payload (for type: "foundry.friction.report")
// {
//   "value": 5.0,
//   "operation": "ADD",         // LOG | ADD | MULTIPLY | SET
//   "reason": "LLM hallucination loop detected",
//   "attribution": "node-logic",
//   "tags": { "law_id": "f-105", "model": "gpt-4" }  // Arbitrary dimensions
// }

enum FrictionOperation {
  FRICTION_LOG = 0;             // Score = Score + log(1 + Value)
  FRICTION_ADD = 1;             // Score = Score + Value
  FRICTION_MULTIPLY = 2;        // Score = Score × Value  
  FRICTION_SET = 3;             // Score = Value (override)
}

// ─────────────────────────────────────────────────────────────────────────────
// Artefact Messages
// ─────────────────────────────────────────────────────────────────────────────

message StoreArtefactRequest {
  string kind = 1;              // References GovernedArtefact
  string name = 2;              // e.g., "source.py"
  bytes content = 3;
  // workitem_id implicit from session context
}

message StoreArtefactResponse {
  string kind = 1;
  string name = 2;
  string version = 3;           // SHA256 hash of this version
}

message FetchArtefactRequest {
  string kind = 1;
  string name = 2;
  string version = 3;           // Optional - empty = latest
}

message FetchArtefactResponse {
  string kind = 1;
  string name = 2;
  string version = 3;           // Hash of returned version
  bytes content = 4;
}

message ArtefactMetadataRequest {
  string kind = 1;
  string name = 2;
}

message ArtefactMetadataResponse {
  string kind = 1;
  string name = 2;
  string latest_version = 3;
  repeated ArtefactVersion versions = 4;
}

message ArtefactVersion {
  string hash = 1;
  string created_at = 2;        // RFC3339 timestamp
  string created_by_node = 3;
}

message StampRequest {
  string kind = 1;              // Max 256 chars.
  string name = 2;              // Max 256 chars.
  string comment = 3;           // Max 1024 chars.
  // Stamps latest version. Applies ALL roles from FoundryNode.spec.roles[]
  // No role selection - a single Stamp() call applies full node authority
}

// ─────────────────────────────────────────────────────────────────────────────
// Feedback Messages (Threaded, Artefact-Scoped)
// ─────────────────────────────────────────────────────────────────────────────

message AddFeedbackRequest {
  string target = 1;            // Artefact name being critiqued (required). Max 256 chars.
  string message = 2;           // Feedback content. Max 4096 chars.
  string severity = 3;          // LOW | MEDIUM | HIGH | SEVERE
  // Creates feedback with initial "opened" event in history
}

message FeedbackID {
  string id = 1;
}

message ResolveFeedbackRequest {
  string feedback_id = 1;
  string message = 2;           // Resolution message. Max 4096 chars.
  // Sets state to "actioned", appends FeedbackEvent{action: "fixed"}
}

message RejectFixRequest {
  string feedback_id = 1;
  string message = 2;           // Rejection reason. Max 4096 chars.
  // Sets state back to "pending", appends FeedbackEvent{action: "rejected"}
}

message FeedbackStateUpdate {
  string feedback_id = 1;
  string state = 2;             // pending | actioned | wont-fix | disputed | rejected | resolved
}

// ─────────────────────────────────────────────────────────────────────────────
// Validation Messages
// ─────────────────────────────────────────────────────────────────────────────

message ValidateRequest {
  string artefact_kind = 1;
  string required_state = 2;    // "present" or "valid"
}

message ValidateResponse {
  bool valid = 1;
  repeated string missing_roles = 2;  // If state=valid but invalid, which roles are missing
}

// ─────────────────────────────────────────────────────────────────────────────
// Routing Messages
// ─────────────────────────────────────────────────────────────────────────────

message WorkitemResult {
  oneof result {
    string route_to_output = 1;  // Named output from Node config
    string route_to = 2;         // Direct node name
    bool complete = 3;           // Terminal completion
  }
}
```

### Consumer API (Sidecar → Node)

The Consumer API uses an **async handoff** pattern to keep the Sidecar responsive during long-running work.

```protobuf
service NodeRuntime {
  // Telemetry subscription events (for LISTEN:telemetry capability)
  rpc DeliverTelemetry(TelemetryEvent) returns (Empty);
  
  // Work assignment - returns immediately after acknowledgment
  rpc ProcessWorkitem(Workitem) returns (Empty);
}
```

#### ProcessWorkitem Flow (Async Handoff)

The `ProcessWorkitem` RPC is **not blocking** for the duration of work execution:

```
1. PUSH:     Sidecar calls ProcessWorkitem(workitem)
2. ACK:      Node SDK receives message, spawns handler goroutine
3. RETURN:   Node returns Empty immediately (RPC completes)
4. PROCESS:  Node executes handler in background (SDK calls, heartbeats)
5. CALLBACK: Node calls CompleteWorkitem(Result) when done
```

**Why this design:**
- **Liveness:** If Sidecar blocked for 60s execution, it couldn't process heartbeat checks or SIGTERM signals.
- **Responsiveness:** Sidecar remains available to the Operator for cancellation/timeout signals.
- **Resource Management:** gRPC connection isn't held open for long-running compute.

### 5.2.1 The Contempt Guard (API Enforcement of Judicial Finality)

The **Sidecar** enforces judicial finality at the API layer. Once the Assay Node renders a verdict with a `linkedRuling`, that verdict is immutable.

**Enforcement Logic:**

When a Node attempts to call `ResolveFeedback` with `state: "wont-fix"`:

1. **State Inspection:** The Sidecar reads the target feedback item's current state.
2. **Judicial Mandate Detection:** 
   - **IF** `item.state == "rejected"` **AND** `item.linkedRuling` is not empty
   - **THEN** this is a Judicial Mandate - the Assay Node has ruled that this feedback MUST be fixed.
3. **Contempt Rejection:** The Sidecar immediately rejects the API call with `CONTEMPT_VIOLATION` (`PERMISSION_DENIED`).
   - Error message: "Cannot refuse a Judicial Mandate. Feedback [id] is subject to Ruling [linkedRuling]."

**Rationale:** The `rejected` state with a `linkedRuling` is the definitive signal that the item is under a Judicial Order. The Refine Node cannot circumvent the Assay Node's authority by simply re-marking the item as `wont-fix`.

### SearchLibrary Behavior

* **Label-Only Query:** Strict label matching against `Law.metadata.labels`.
* **Semantic-Only Query:** Vector similarity search across all Laws.
* **Hybrid Query:** Filters the vector index by matching labels, then performs cosine similarity ranking.

---

## 5.3 Error Taxonomy

Errors are split into two phases based on when validation occurs.

### Phase 1: Synchronous Errors (Sidecar → Node)

These errors are returned directly from SDK method calls. The node can handle them.

| Error Code | gRPC Status | Retryable | Description |
|------------|-------------|-----------|-------------|
| `PERMISSION_DENIED` | `PERMISSION_DENIED` | No | Missing capability for operation |
| `INVALID_ARGUMENT` | `INVALID_ARGUMENT` | No | Bad parameters (missing field, invalid format, invalid $sender token) |
| `INVALID_OUTPUT` | `INVALID_ARGUMENT` | No | Output name not in node config |
| `NOT_FOUND` | `NOT_FOUND` | No | Artefact, law, or feedback doesn't exist |
| `ARTEFACT_CORRUPTED` | `DATA_LOSS` | No | Hash verification failed on fetch |
| `CONFLICT_DETECTED` | `ALREADY_EXISTS` | No | Law with similar statement exists |
| `CONTEMPT_VIOLATION` | `PERMISSION_DENIED` | No | Attempt to wont-fix a `rejected` feedback item with `linkedRuling` |
| `UNAVAILABLE` | `UNAVAILABLE` | Yes | Service temporarily unavailable (Librarian, Archivist) |
| `RESOURCE_EXHAUSTED` | `RESOURCE_EXHAUSTED` | Yes | Storage full, rate limited |

**Example handling:**
```go
_, err := n.StoreArtefact(ctx, req)
if err != nil {
    if foundry.IsError(err, foundry.PERMISSION_DENIED) {
        // Missing WRITE:artefact capability
    }
    if foundry.IsError(err, foundry.RESOURCE_EXHAUSTED) {
        // Retry with backoff
    }
    return foundry.RouteToOutput("error")
}
```

### Phase 2: Async Errors (Operator → Workitem Failed)

These errors occur after the node handler returns. The node cannot handle them - the workitem transitions to `Failed` state.

| Error Code | Description | Recorded In |
|------------|-------------|-------------|
| `NO_AVAILABLE_TARGET` | No ready nodes with targetRole | `workitem.status.failureReason` |
| `TERMINAL_CONTRACT_VIOLATED` | Artefacts don't meet terminal contract | `workitem.status.failureReason` |
| `NOT_TERMINAL` | Called Complete() but node not terminal | `workitem.status.failureReason` |
| `HEARTBEAT_TIMEOUT` | Node didn't heartbeat within timeout | `workitem.status.failureReason` |
| `STATE_CORRUPTION` | Merkle root mismatch, sequence gap | `workitem.status.failureReason` |

### Error Propagation Flow

```
Node returns RouteToOutput("pass")
        │
        ▼
┌─────────────────────────────────────────────────────────────┐
│ Sidecar (Phase 1 - Synchronous)                             │
│ - Output "pass" exists in config?                           │
│   → NO: return INVALID_OUTPUT error to node                 │
│ - Valid? → Write routingInstruction to Workitem CRD         │
└─────────────────────────────────────────────────────────────┘
        │ (Node handler returns successfully)
        ▼
┌─────────────────────────────────────────────────────────────┐
│ Operator (Phase 2 - Async)                                  │
│ - Find target nodes with role                               │
│   → None ready? → workitem.status = Failed                  │
│                   workitem.status.failureReason = "NO_AVAILABLE_TARGET"
│ - Terminal contract check?                                  │
│   → Violated? → workitem.status = Failed                    │
│                 workitem.status.failureReason = "TERMINAL_CONTRACT_VIOLATED"
│ - Valid? → Assign workitem to target node                   │
└─────────────────────────────────────────────────────────────┘
```

### Structured Error Details

For errors that benefit from additional context, the SDK provides typed details:

```go
_, err := n.RecordFinding(ctx, statement, labels)
if foundry.IsError(err, foundry.CONFLICT_DETECTED) {
    details := foundry.GetConflictDetails(err)
    // details.CandidateLaws = ["f-101", "f-102"]
    // details.SimilarityScores = [0.92, 0.87]
}
```

| Error Code | Details Type | Fields |
|------------|--------------|--------|
| `CONFLICT_DETECTED` | `ConflictDetails` | `CandidateLaws []string`, `SimilarityScores []float64` |
| `TERMINAL_CONTRACT_VIOLATED` | `ContractDetails` | `MissingArtefacts []string`, `MissingRoles []string` |
| `NO_AVAILABLE_TARGET` | `TargetDetails` | `RequestedRole string`, `AvailableNodes []string` |


