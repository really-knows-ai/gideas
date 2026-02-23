# gRPC API Reference

All runtime services expose gRPC APIs. Node-originated calls are mediated by the [Sidecar](../03-node/01-sidecar.md), which authenticates, injects identity context, and proxies to the owning service. Inter-service calls use direct service-to-service gRPC.

## Service Inventory

| Service | Responsibility | Primary Consumers |
|---------|---------------|-------------------|
| [Operator](#operator-api) | Workitem lifecycle, routing, assignment, entry/exit contract enforcement | Sidecar (on behalf of nodes) |
| [Archivist](#archivist-api) | Artefact content, versions, stamps, feedback | Sidecar (on behalf of nodes), Operator |
| [Librarian](#librarian-api) | Law storage, retrieval, integration, hearing triggers | Sidecar (on behalf of nodes), Operator |
| [Flow Monitor](#flow-monitor-api) | Pipeline adapter for metrics export (Prometheus) and audit log emission (stdout) | Subscribes to Flow Event Bus |
| [Flow Event Bus](#flow-event-bus-api) | Durable event distribution across telemetry, audit, friction, and workitem channels | Sidecar (publish), all services (publish/subscribe) |
| [Friction Ledger](#friction-ledger-api) | Friction aggregation, threshold evaluation, friction queries | Sidecar (on behalf of nodes), Librarian, Friction Ledger publishes to Bus |
| [Jury](#jury-api) | Multi-agent deliberation engine | Arbiter, Tribunal (via Sidecar) |
| [Clerk](#clerk-api) | Law drafting and codification coordination | Arbiter, Tribunal (via Sidecar) |
| [Sidecar](#sidecar-mediated-sdk-paths) | Authentication proxy, identity injection, local validation | Node handlers (via SDK) |
| [Support Services](#support-service-api) | Pluggable capabilities (e.g. codification) | Sidecar (on behalf of nodes), system services |
| [QueuePeer](#queuepeer-api) | Federated Queue Mesh inter-pod communication | HITL node replicas (peer-to-peer) |

---

## Operator API

The Operator API handles Workitem control-plane mutations. All node-facing methods are reached through the Sidecar; the Operator also exposes internal methods for service-to-service coordination.

### Node-Facing Methods (via Sidecar)

| Method | Request | Response | Description |
|--------|---------|----------|-------------|
| `SubmitResult` | `workitem_id`, `routing_instruction` | `accepted` or structured error | Submits the handler's routing instruction. The Operator validates routing guards and applies the lifecycle transition. |
| `CreateWorkitem` | (none) | `workitem_id` or structured error | Creates a new Workitem in `Pending`. The creating node must be entry-bound. The Operator validates the bound entry contract against artefact state in the Archivist. |
| `CreateChildWorkitem` | (none) | `child_workitem_id` or structured error | Creates a child Workitem in `Pending` with `parentWorkitemID` set to the caller's current Workitem. The Operator applies the `flow.gideas.io/parent` label. Requires `CREATE:workitem/child` capability. Identity comes from Sidecar-injected metadata — the request body is empty. |
| `RouteChild` | `child_workitem_id`, `routing_instruction` | `accepted` or structured error | Submits a routing instruction for a child Workitem. The Operator validates that the child's `parentWorkitemID` matches the caller's current Workitem, that the child is in `Pending` state (not yet routed), and that the routing target exists. If the creating node has `childWorkitems.entryContract` configured, the Operator validates the child's artefact state against that contract before routing. |
| `GetChildren` | (none) | `repeated ChildWorkitemStatus` | Returns the current state of all child Workitems for the caller's parent Workitem. The Operator queries by `flow.gideas.io/parent` label and includes artefact references from the Archivist. Identity comes from Sidecar-injected metadata — the request body is empty. |
| `GetFlowTopology` | (none) | `self`, `nodes`, `exit_contract` | Returns the Flow topology visible to the calling node. Requires `READ:flow` capability. The Sidecar injects node identity; the Operator resolves the calling node's outputs, all peer nodes with capabilities, and the bound exit contract (if exit-bound). Identity comes from Sidecar-injected metadata — the request body is empty. |

### Service-Facing Methods

| Method | Request | Response | Description |
|--------|---------|----------|-------------|
| `CreateHearingWorkitem` | `law_id` | `workitem_id` | Creates a review hearing Workitem for [Tribunal](../02-flow/03-nodes-external.md#the-judiciary--standard-subsystem) processing. The Operator creates a `law-reference` artefact from the supplied `law_id` and admits the Workitem via the Tribunal's bound hearing entry contract. Called by the Librarian when friction thresholds or review TTL expiry trigger a review hearing. |
| `ExportWorkitem` | `workitem_id` | `export_package` | Assembles an export package from the completed Workitem: artefact content (scoped by exit contract), passport stamps, Workitem metadata, and provenance chain. The Operator signs the package with the Flow's identity material and includes the certificate chain. |
| `ImportWorkitem` | `export_package`, `treaty_name?` | `workitem_id` or structured error | Validates and materialises a Workitem from an export package. Verifies the package signature against the certificate chain (State Root for siblings, Treaty `caCert` for non-siblings), enforces `allowedSubjects` and `maxBundleSize` from the Treaty if applicable, validates the materialised Workitem against the configured `importNode`'s entry contract, and creates the Workitem in `Pending`. |

### Routing Instruction Shape

| Field | Type | Description |
|-------|------|-------------|
| `type` | `string` | `route_to_output`, `route_to`, or `complete`. |
| `target` | `string` | Output name (for `route_to_output`), node name (for `route_to`), or empty (for `complete`). |

### ChildWorkitemStatus Shape

| Field | Type | Description |
|-------|------|-------------|
| `workitem_id` | `string` | Child Workitem identifier. |
| `phase` | `string` | Current lifecycle state: `Pending`, `Running`, `Completed`, `Failed`. |
| `current_assignee` | `string` | Node currently assigned to the child. Empty when `Pending`. |
| `artefacts` | `repeated ArtefactRef` | Artefact references (`id`, `governed_artefact`) associated with the child in the Archivist. |

### Validation and Error Responses

| Condition | Error | gRPC Status |
|-----------|-------|-------------|
| Output name not in node's configured outputs | `INVALID_ROUTE` | `FAILED_PRECONDITION` |
| Target node does not exist as a FoundryNode | `INVALID_ROUTE` | `FAILED_PRECONDITION` |
| `complete` from non-exit node | `EXIT_NOT_BOUND` | `FAILED_PRECONDITION` |
| Exit contract not satisfied | `CONTRACT_VIOLATION` | `FAILED_PRECONDITION` |
| Thrash budget exceeded | `THRASH_BUDGET_EXCEEDED` | `FAILED_PRECONDITION` |
| Entry contract not satisfied (CreateWorkitem) | `CONTRACT_VIOLATION` | `FAILED_PRECONDITION` |
| Creating node not entry-bound | `ENTRY_NOT_BOUND` | `FAILED_PRECONDITION` |
| Imported Workitem does not satisfy import node's entry contract | `IMPORT_ADMISSION_FAILED` | `FAILED_PRECONDITION` |
| Parent `Complete()` with non-terminal children | `CHILDREN_NOT_TERMINAL` | `FAILED_PRECONDITION` |
| Child Workitem not owned by caller's current Workitem | `CHILD_NOT_OWNED` | `FAILED_PRECONDITION` |
| Write or re-route on a child that has already been routed | `CHILD_ALREADY_ROUTED` | `FAILED_PRECONDITION` |
| Missing `CREATE:workitem/child` capability | `CAPABILITY_DENIED` | `PERMISSION_DENIED` |
| Routing from outside a group to a non-entry-bound node inside the group | `GROUP_ROUTING_DENIED` | `FAILED_PRECONDITION` |
| Root Workitem routed to group entry node without satisfying group entry contract | `GROUP_ENTRY_VIOLATION` | `FAILED_PRECONDITION` |

---

## Archivist API

The Archivist API manages artefact lifecycle and provenance. All node-facing methods are reached through the Sidecar. The Operator also exposes service-facing methods for contract validation.

### Service-Facing Methods

| Method | Request | Response | Description |
|--------|---------|----------|-------------|
| `QueryArtefactState` | `workitem_id`, `governed_artefacts[]` | `artefact_states[]` | Returns artefact presence and stamp state for exit contract validation. Called by the Operator's own reconciliation loop. |

### Artefact Content and Version Methods

| Method | Request | Response | Description |
|--------|---------|----------|-------------|
| `GetArtefact` | `workitem_id`, `artefact_id`, `target_workitem_id?` | `content`, `version_hash`, `governed_artefact` | Returns the latest version's content bytes. Sidecar verifies `SHA256(content) == version_hash`. When `target_workitem_id` is set, reads from a child Workitem — the Archivist validates that the caller's Workitem is the parent of the target and that the target is in `Completed` state. |
| `GetArtefactVersion` | `workitem_id`, `artefact_id`, `version_hash` | `content` | Returns content bytes for a specific version by hash. |
| `GetArtefactMetadata` | `workitem_id`, `artefact_id` | `version_history[]`, `stamps[]` | Returns version history and current passport without content bytes. |
| `ListArtefacts` | `workitem_id`, `target_workitem_id?` | `artefact_refs[]` | Returns all artefacts (`id`, `governed_artefact`) associated with the Workitem. The Archivist is the source of truth for artefact-to-Workitem relationships. When `target_workitem_id` is set, lists artefacts from a child Workitem — same parent-child and completion validation as `GetArtefact`. |
| `StoreArtefact` | `workitem_id`, `artefact_id`, `governed_artefact`, `content`, `content_hash`\* | `version_hash`, `is_new_version` | Stores content bytes and creates a version record. Returns the confirmed version hash and whether a new version was created. \*`content_hash` is Sidecar-computed, not node-supplied. |

### Stamp Methods

| Method | Request | Response | Description |
|--------|---------|----------|-------------|
| `GetStamps` | `workitem_id`, `artefact_id` | `stamps[]` | Returns all stamps on the artefact's current version. Each stamp includes name, applying node, content hash, signature, and certificate chain. |
| `HasStamp` | `workitem_id`, `artefact_id`, `stamp_name` | `exists` (bool) | Returns whether the named stamp exists on the current version. |
| `StampArtefact` | `workitem_id`, `artefact_id`, `stamp_name`, `signature`\*, `cert_chain`\* | `stamp_record` | Applies a named stamp. \*`signature` and `cert_chain` are Sidecar-injected from the node's identity material. The Archivist validates: (1) `STAMP:artefact/<name>/<stamp-name>` capability, (2) stamp has not already been applied to this version (write-once). |

### Feedback Methods

| Method | Request | Response | Description |
|--------|---------|----------|-------------|
| `AddFeedback` | `workitem_id`, `artefact_id`, `severity`, `message`, `version_hash`\* | `feedback_id` | Creates a feedback item in `new` state, tagged to the artefact's current version. \*`version_hash` is Sidecar-resolved from the latest version at call time. Transparently emits `AddFriction` with magnitude = feedback depth. |
| `GetFeedback` | `workitem_id`, `artefact_id` | `feedback_items[]` | Returns all feedback items for the artefact across all versions. |
| `HasUnresolvedFeedback` | `workitem_id`, `artefact_id` | `has_unresolved` (bool) | Returns `true` if any feedback item is in a non-`resolved` state. |
| `ResolveFeedback` | `workitem_id`, `feedback_id`, `message` | `updated_item` | Transitions feedback from `new` or `rejected` to `actioned`. |
| `RefuseFeedback` | `workitem_id`, `feedback_id`, `justification` | `updated_item` | Transitions feedback from `new` or `rejected` to `wont_fix`. Requires structured justification (`citation` with `citation_ids[]` or `novel_argument` with `argument`). |
| `AcceptFix` | `workitem_id`, `feedback_id` | `updated_item` | Transitions feedback from `actioned` to `resolved`. |
| `RejectFix` | `workitem_id`, `feedback_id`, `message` | `updated_item` | Transitions feedback from `actioned` to `rejected`. |
| `AcceptRefusal` | `workitem_id`, `feedback_id` | `updated_item` | Transitions feedback from `wont_fix` to `resolved`. |
| `RejectRefusal` | `workitem_id`, `feedback_id`, `message` | `updated_item` | Transitions feedback from `wont_fix` to `rejected`. |
| `GetFeedbackDepth` | `workitem_id`, `feedback_id` | `depth` (integer) | Returns the current history depth (number of transitions) for the specified feedback item. |
| `DeadlockFeedback` | `workitem_id`, `feedback_id` | `updated_item` | Transitions feedback from any non-resolved, non-deadlocked state to `deadlocked`. Requires `WRITE:feedback/deadlocked` capability. The Archivist validates capability, from-state, and contempt guard (items with `linkedRuling` cannot be deadlocked); deadlock determination is gate node logic, not Archivist enforcement. |
| `LinkRuling` | `workitem_id`, `feedback_id`, `law_id`, `target_state` | `updated_item` | Links a judiciary ruling to a deadlocked feedback item, atomically setting `linked_ruling` and transitioning the feedback to `target_state` (`wont_fix` or `rejected`). Requires `WRITE:feedback/link-ruling` capability. Enforces contempt guard: feedback must be in `deadlocked` state and must not already have a linked ruling. Records a `link_ruling` feedback event for audit trail continuity. |

### Archivist Error Responses

| Condition | Error | gRPC Status |
|-----------|-------|-------------|
| Missing `READ:artefact` capability | `CAPABILITY_DENIED` | `PERMISSION_DENIED` |
| Missing `WRITE:artefact` capability | `CAPABILITY_DENIED` | `PERMISSION_DENIED` |
| Missing `STAMP:artefact/<name>/<stamp>` capability | `CAPABILITY_DENIED` | `PERMISSION_DENIED` |
| Missing `READ:feedback` or `WRITE:feedback/<status>` capability | `CAPABILITY_DENIED` | `PERMISSION_DENIED` |
| Stamp already applied to this version | `STAMP_ALREADY_APPLIED` | `ALREADY_EXISTS` |
| Content hash mismatch on read | `ARTEFACT_CORRUPTED` | `DATA_LOSS` |
| Existing `id` with different `governed_artefact` | `ARTEFACT_KIND_CONFLICT` | `INVALID_ARGUMENT` |
| Invalid feedback state transition | `INVALID_STATE_TRANSITION` | `FAILED_PRECONDITION` |
| Attempt to override a judicially-linked ruling | `CONTEMPT_VIOLATION` | `FAILED_PRECONDITION` |
| Feedback ID not found | `FEEDBACK_NOT_FOUND` | `NOT_FOUND` |
| Message exceeds 1024 characters | `MESSAGE_TOO_LONG` | `INVALID_ARGUMENT` |

---

## Librarian API

The Librarian API manages the Flow's body of law. Node-facing methods are reached through the Sidecar. The Librarian also exposes inter-service methods for the Operator and for cross-flow replication.

### Node-Facing Methods (via Sidecar)

| Method | Request | Response | Description |
|--------|---------|----------|-------------|
| `QueryLaws` | `filter` (optional) | `laws[]` | Returns laws matching the filter. Three modes: (1) no filter — all laws, (2) `governed_artefact` — laws whose `appliesTo` includes the governed artefact plus global laws, (3) `governed_artefact` + `representation_type` — same governed artefact filter plus at least one representation of the requested MIME type. All modes return full law objects. |
| `RecordFinding` | `goal`, `applies_to[]`, `representations[]` | `law_id` | Creates a Tier 1 Finding. Write-availability-first: returns immediately with a law identifier. Indexing and duplicate detection are asynchronous. |

### Service-Facing Methods

| Method | Request | Response | Description |
|--------|---------|----------|-------------|
| `GetLaw` | `law_id` | `law` | Returns the full law object by identifier. Used by Judiciary nodes for hearing evidence retrieval. |
| `WriteLaw` | `law` | `law_id`, `version_hash` | Persists a law (Tier 2 Ruling minted by the [Clerk](../02-flow/04-system-services.md#clerk), Tier 3+ applied by administrator or Governance Flow). During hearing processing, the law is created in an inactive state pending hearing completion. |
| `RetireLaw` | `law_id` | `acknowledged` | Removes a law from the active Library. History is preserved in the audit log. |
| `ReplicateLaws` | `laws[]`, `source_flow_id` | `integration_results[]` | Receives higher-tier laws from a remote Librarian for integration. Triggers the two-stage conflict protocol. |
| `ApplyLifecycleAction` | `law_id`, `verdict` | `acknowledged` | Applies the outcome of a review hearing (promote, retire, demote) to the specified law. Called by the Operator after Tribunal hearing completion. This action activates any law created during the hearing. |

### Librarian Error Responses

| Condition | Error | gRPC Status |
|-----------|-------|-------------|
| Missing `READ:law` capability | `CAPABILITY_DENIED` | `PERMISSION_DENIED` |
| Missing `WRITE:law/tier1` capability | `CAPABILITY_DENIED` | `PERMISSION_DENIED` |
| Cited law does not exist or is retired | `LAW_NOT_FOUND` | `NOT_FOUND` |
| Finding goal exceeds maximum length | `MESSAGE_TOO_LONG` | `INVALID_ARGUMENT` |
| Librarian service unavailable | `SERVICE_UNAVAILABLE` | `UNAVAILABLE` |
| Certificate chain invalid on replicated laws | `TRUST_CHAIN_INVALID` | `PERMISSION_DENIED` |
| No Treaty configured for the required direction | `TREATY_NOT_FOUND` | `NOT_FOUND` |

---

## Flow Monitor API

The Flow Monitor exposes no gRPC service definition and accepts no direct gRPC calls. It is an internal subscriber of the Flow Event Bus (telemetry and audit channels) and exposes only an HTTP `/metrics` endpoint for Prometheus scraping and JSON Lines on stdout for audit log pipelines.

The Flow Monitor is a stateless pipeline adapter. It does not persist events, serve query APIs, or accept direct ingestion calls. It may persist a lightweight replay checkpoint (last-seen sequence number per channel) to avoid delivery gaps across restarts; this is not an event store. Event buffering and delivery guarantees are Flow Event Bus concerns.

### Flow Monitor Error Responses

| Condition | Error | gRPC Status |
|-----------|-------|-------------|
| Flow Monitor unavailable | `SERVICE_UNAVAILABLE` | `UNAVAILABLE` |

---

## Flow Event Bus API

The Flow Event Bus distributes runtime events across four channels. Producers publish events;
consumers subscribe to filtered streams. All events are persisted to a SQLite append-only log
before fan-out.

### Publish Methods

| Method | Request | Response | Description |
|--------|---------|----------|-------------|
| `Publish` | `channel`, `event` | `acknowledged`, `sequence` | Publishes an event to the specified channel (`TELEMETRY`, `AUDIT`, `FRICTION`, or `WORKITEM`). Write-ahead — the producer receives acknowledgement with the assigned sequence number after the event is persisted to the log. |

### Subscribe Methods

| Method | Request | Response | Description |
|--------|---------|----------|-------------|
| `Subscribe` | `channel`, `filter`, `last_sequence?` | `stream FlowEvent` | Opens a server-side stream of events matching the channel and optional filter. If `last_sequence` is provided, the Bus replays events from that sequence number (if still within the retention window) before switching to live delivery. The stream remains open until the client disconnects. Filters support event type and law_id for telemetry channel events. |

### Event Shape

| Field | Type | Description |
|-------|------|-------------|
| `event_id` | `string` | Unique event identifier. |
| `sequence` | `uint64` | Monotonically increasing sequence number within the channel. Used for replay positioning. |
| `channel` | `EventChannel` | `TELEMETRY`, `AUDIT`, `FRICTION`, or `WORKITEM`. |
| `event_type` | `string` | Event type identifier (e.g. `friction`, `foundry.cost.llm`, `audit.artefact.stamped`, `friction.threshold_crossed`). |
| `flow_id` | `string` | Flow identifier. |
| `node_id` | `string` | Emitting node (empty for service-originated events). |
| `workitem_id` | `string` | Associated Workitem (empty for law-lifecycle audit events). |
| `parent_workitem_id` | `string` | Parent Workitem ID if the associated Workitem is a child (empty for root Workitems and non-Workitem events). Present on `WORKITEM` channel events for filtering. |
| `timestamp` | `Timestamp` | Event timestamp. |
| `trace_id` | `string` | Distributed trace context identifier. Injected by the Sidecar from the active trace. Empty if tracing is not configured. |
| `attributes` | `map<string, string>` | Event-specific key-value attributes. For friction events: `law_ids` (comma-separated), `magnitude`. For audit events: `action`, `resource_id`. For threshold-crossing events: `law_id`, `tier`, `accumulated_friction`, `threshold`. |
| `payload` | `bytes` | Optional structured payload (max 64 KB). |

### Subscribe Filter

| Field | Type | Description |
|-------|------|-------------|
| `event_type` | `string` | Optional: filter to specific event type. |
| `law_id` | `string` | Optional: filter to events attributed to a specific law. For events carrying a comma-separated `law_ids` attribute (e.g. friction events), the filter matches if the filter value appears as an exact element after splitting on commas — not a substring match. |
| `parent_workitem_id` | `string` | Optional: filter to events for child Workitems of a specific parent. Used with the `WORKITEM` channel to observe child lifecycle events during fan-out/fan-in processing. |

### Flow Event Bus Error Responses

| Condition | Error | gRPC Status |
|-----------|-------|-------------|
| Flow Event Bus unavailable | `SERVICE_UNAVAILABLE` | `UNAVAILABLE` |
| Requested sequence number is beyond retention window | `SEQUENCE_EXPIRED` | `OUT_OF_RANGE` |

---

## Friction Ledger API

The Friction Ledger aggregates friction events and serves friction queries. Node-facing methods
are reached through the Sidecar. The Librarian and other services use direct service-to-service
gRPC.

### Query Methods

| Method | Request | Response | Description |
|--------|---------|----------|-------------|
| `QueryFriction` | `filter` (by `law_id`, `node_id`, `workitem_id`, `tier`, `time_range`) | `friction_aggregates[]` | Returns aggregated friction data across the requested axes. Used by the Tribunal for hearing evidence and by the Librarian for catch-up on startup/reconnection. |

### Friction Ledger Error Responses

| Condition | Error | gRPC Status |
|-----------|-------|-------------|
| Friction Ledger unavailable | `SERVICE_UNAVAILABLE` | `UNAVAILABLE` |

---

## Sidecar-Mediated SDK Paths

The [Sidecar](../03-node/01-sidecar.md) abstracts all transport — the node sees SDK calls, and the Sidecar authenticates, injects identity, and proxies to the owning service:

```mermaid
sequenceDiagram
    participant ND as Node Handler
    participant SC as Sidecar
    participant SV as Runtime Service

    ND->>SC: SDK call
    SC->>SC: Validate scope and parameters
    SC->>SC: Inject identity (node_id, workitem_id, flow_id)
    SC->>SV: Authenticated gRPC request
    SV->>SV: Validate capability and authorise
    SV-->>SC: Response or structured error
    SC-->>ND: SDK result or structured error
```

### Identity Injection

Every outgoing request from the Sidecar carries:

| Field | Source | Description |
|-------|--------|-------------|
| `node_id` | Sidecar identity material | The node's identity. |
| `workitem_id` | Current assignment | The Workitem being processed. |
| `flow_id` | Sidecar identity material | The Flow this node belongs to. |

Nodes cannot override or spoof these fields. The Sidecar is the sole authority for runtime attribution on node-originated requests.

### Authorisation Split

| Layer | Responsibility |
|-------|---------------|
| Sidecar | Scope validation (assignment boundaries), parameter validation (malformed requests), authentication (identity material). |
| Runtime service | Capability enforcement, state machine validation, write-once enforcement, contempt guard. |

The Sidecar catches invalid requests early. The owning service makes authoritative governance decisions.

### Sidecar-Local Operations

| Method | Request | Response | Description |
|--------|---------|----------|-------------|
| `Heartbeat` | `workitem_id` | `acknowledged` | Resets the Sidecar's inactivity timer. Implicit heartbeats occur on every SDK call; this method provides an explicit signal for long-running computation. The Sidecar propagates activity timestamps to the Operator, throttled to avoid excessive writes. |
| `PauseTimer` | `workitem_id` | `acknowledged` | Suspends the Sidecar's inactivity timer for the specified Workitem assignment. The timer remains suspended until `ResumeTimer` is called or the handler returns. Used by [HITL nodes](../04-sdk/08-sdk-hitl.md) to park Workitems while awaiting external input without triggering timeout. The Workitem remains in `Running` state — this is a Sidecar-local mechanism. |
| `ResumeTimer` | `workitem_id` | `acknowledged` | Resumes the Sidecar's inactivity timer after a `PauseTimer` call. The timer resets to the full timeout window on resume. |

### Sidecar-Mediated Telemetry

These methods were previously on `FlowMonitorService`. They now live on `SidecarService` because the Sidecar is the translation boundary that wraps SDK calls into FlowEvent publishes to the Flow Event Bus.

| Method | Request | Response | Description |
|--------|---------|----------|-------------|
| `AddFriction` | `magnitude` (double), `law_ids` (repeated string) | `acknowledged` | Enforces `WRITE:friction` capability. Injects Sidecar-authoritative identity (`flow_id`, `workitem_id`, `node_id`). Wraps as a FlowEvent and publishes to the Flow Event Bus friction channel. Non-blocking from the caller's perspective. |
| `RecordTelemetry` | `event_type` (string), `payload` (bytes, max 64 KB) | `acknowledged` | Injects Sidecar-authoritative identity (`flow_id`, `workitem_id`, `node_id`). Wraps as a FlowEvent and publishes to the Flow Event Bus telemetry channel. Non-blocking from the caller's perspective. |

### Sidecar Error Responses

| Condition | Error | gRPC Status |
|-----------|-------|-------------|
| Request targets a Workitem outside the current assignment | `ASSIGNMENT_SCOPE_VIOLATION` | `FAILED_PRECONDITION` |
| Identity material expired or invalid | `IDENTITY_EXPIRED` | `UNAUTHENTICATED` |
| Node inactivity timer exceeded | `TIMEOUT_EXCEEDED` | `DEADLINE_EXCEEDED` |
| Missing `WRITE:friction` capability (AddFriction) | `CAPABILITY_DENIED` | `PERMISSION_DENIED` |
| Payload exceeds 64 KB (RecordTelemetry) | `PAYLOAD_TOO_LARGE` | `INVALID_ARGUMENT` |

---

## Jury API

The [Jury](../02-flow/04-system-services.md#jury) is a generic deliberation engine. It receives a question (string) and evidence (markdown bundle) — not domain-specific structured data. The calling node (Arbiter or Tribunal) is responsible for framing the question and assembling evidence. This keeps the Jury reusable with no Foundry-domain coupling. It is consumed through Sidecar-mediated calls.

### Deliberation Methods

| Method | Request | Response | Description |
|--------|---------|----------|-------------|
| `Deliberate` | `question`, `evidence`, `allowed_outcomes`, `consensus_strategy`, `max_rounds`, `jury_size` | `DeliberateResponse` | Empanels a diverse panel of FoundryAgent jurors, runs blind voting with optional deliberation rounds feeding anonymised peer arguments, applies the consensus strategy, and returns the outcome with per-juror justifications. A hung jury is a valid outcome (`hung=true`), not an error. |

### Deliberate Request

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `question` | `string` | yes | What the jury is deliberating. Framed by the calling node (e.g., "Should the reviewer's feedback be upheld, or should the refiner's refusal stand?"). |
| `evidence` | `string` | yes | Structured markdown evidence bundle assembled by the calling node (feedback history, artefact excerpts, cited laws, friction summary). |
| `allowed_outcomes` | `repeated string` | yes | Valid vote values (e.g., `["favour_refiner", "favour_reviewer"]`). The Jury builds the output JSON Schema dynamically from these values, ensuring juror votes are always valid outcomes. |
| `consensus_strategy` | `ConsensusStrategy` | yes | `SIMPLE_MAJORITY` (>50%), `SUPER_MAJORITY` (>=66%), or `UNANIMITY` (100%). Proto enum. |
| `max_rounds` | `int32` | yes | Maximum deliberation rounds before declaring a hung jury. Each round after the first feeds anonymised peer arguments back to jurors. |
| `jury_size` | `int32` | yes | Number of jurors to empanel from the pool. The Jury owns juror construction — 5 distinct personality types with different judicial philosophies. Callers specify count, not profiles. If `jury_size <= 5`, each type appears at most once. |

### Deliberate Response

| Field | Type | Description |
|-------|------|-------------|
| `outcome` | `string` | The winning outcome (one of `allowed_outcomes`). Empty if `hung` is true. |
| `justifications` | `repeated JurorJustification` | Per-juror vote and reasoning from the final round. Each entry contains `juror_id` (opaque, e.g., `"textualist-0"`), `outcome` (the juror's vote), and `reasoning` (the juror's justification). No numerical confidence — confidence is reflected structurally (hung vs. unanimous). |
| `rounds_used` | `int32` | Number of deliberation rounds executed. |
| `hung` | `bool` | `true` if consensus was not reached after `max_rounds`. A hung jury is a valid response, not an error — the calling node decides how to handle it (e.g., escalate to Advocate). |

### Jury Error Responses

| Condition | Error | gRPC Status |
|-----------|-------|-------------|
| Jury service unavailable | `SERVICE_UNAVAILABLE` | `UNAVAILABLE` |
| Juror inference failure | `JURY_INFERENCE_FAILED` | `INTERNAL` |

---

## Clerk API

The [Clerk](../02-flow/04-system-services.md#clerk) is a core service that handles law drafting and codification. It is consumed by the [Arbiter](../02-flow/03-nodes-external.md#the-judiciary--standard-subsystem) and [Tribunal](../02-flow/03-nodes-external.md#the-judiciary--standard-subsystem) through Sidecar-mediated calls.

### Drafting Methods

| Method | Request | Response | Description |
|--------|---------|----------|-------------|
| `DraftLaw` | `verdict`, `goal`, `tier`, `applies_to` | `law` | Drafts a prose representation, discovers and dispatches to all ready Codification Services in parallel, assembles the final law with prose + successfully returned formal representations, and calls `WriteLaw` on the Librarian to persist. |

### DraftLaw Request

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `verdict` | `Verdict` | yes | The Jury's verdict providing the decision basis for the law. |
| `goal` | `string` | yes | Plain-language statement of what the law enforces, stops, or ensures. |
| `tier` | `integer` | yes | Law tier (typically `2` for Rulings). |
| `applies_to` | `[]string` | no | Governed artefact names. Empty for global. |

### DraftLaw Response

| Field | Type | Description |
|-------|------|-------------|
| `law_id` | `string` | Identifier of the persisted law. |
| `version_hash` | `string` | Content hash of the new law version. |
| `representations` | `[]Representation` | The representations successfully assembled (prose + any codified). |
| `codification_declines` | `[]string` | Codification Service names that declined to encode the law's goal (logged, not blocking). |

### Clerk Error Responses

| Condition | Error | gRPC Status |
|-----------|-------|-------------|
| Clerk service unavailable | `SERVICE_UNAVAILABLE` | `UNAVAILABLE` |
| Librarian WriteLaw failed | `LAW_WRITE_FAILED` | `INTERNAL` |

---

## QueuePeer API

The QueuePeer gRPC service enables inter-pod communication within the [Federated Queue Mesh](../04-sdk/08-sdk-hitl.md#federated-queue-mesh). It is used by HITL node replicas for scatter-gather reads and proxy writes.

### Peer Methods

| Method | Request | Response | Description |
|--------|---------|----------|-------------|
| `GetLocalQueue` | `filter` | `items[]` | Returns queue items from the local shard's `queue.db`. Used by scatter-gather reads. |
| `ClaimItem` | `workitem_id` | `item` or error | Claims a `waiting` item on the local shard. Returns `QUEUE_ITEM_ALREADY_CLAIMED` if already claimed. |
| `ReleaseItem` | `workitem_id` | `item` | Releases a `claimed` item back to `waiting` on the local shard. |
| `CompleteItem` | `workitem_id` | `acknowledged` | Deletes a `claimed` item from the local shard (decision made). |

### QueuePeer Error Responses

| Condition | Error | gRPC Status |
|-----------|-------|-------------|
| Owning shard unavailable | `QUEUE_UNAVAILABLE` | `UNAVAILABLE` |
| Invalid state transition (e.g., release or complete on a non-claimed item) | `QUEUE_ITEM_INVALID_STATE` | `FAILED_PRECONDITION` |
| Item not found on this shard | `QUEUE_ITEM_NOT_FOUND` | `NOT_FOUND` |
| Item already claimed | `QUEUE_ITEM_ALREADY_CLAIMED` | `ALREADY_EXISTS` |

---

## Support Service API

[Flow Support Services](../02-flow/04-system-services.md#flow-support-services) expose custom gRPC capabilities. The API shape is extensible — each service defines its own methods.

### Consumption Paths

| Consumer | Path | Authorisation |
|----------|------|---------------|
| Nodes | Sidecar-mediated | `USE:support/<service>/<capability>` grant on the node. |
| System services | Direct service-to-service gRPC | Flow configuration discovery. |

### Codification Service API

Each [CodificationService](./crds.md#codificationservice) exposes a single `Encode` method:

| Method | Request | Response | Description |
|--------|---------|----------|-------------|
| `Encode` | `law` (Law object) | `representation` (Representation) | Translates the law's goal into the service's declared `outputFormat`. The service receives the full law object (goal, existing representations, tier, metadata) and returns a single typed representation. The output MIME type matches the `outputFormat` declared in the service's CRD. |

### Health Endpoints

All Support Services implement:

| Endpoint | Description |
|----------|-------------|
| `healthz` | Liveness probe. Returns healthy when the service process is running. |
| `readyz` | Readiness probe. Returns ready when the service can accept requests. |

### Support Service Error Responses

| Condition | Error | gRPC Status |
|-----------|-------|-------------|
| Missing `USE:support/<service>/<capability>` grant | `CAPABILITY_DENIED` | `PERMISSION_DENIED` |
| Support Service unavailable | `SERVICE_UNAVAILABLE` | `UNAVAILABLE` |

---

## API Invariants

1. All node-originated requests transit the Sidecar. No node calls a runtime service directly.
2. Identity context (`node_id`, `workitem_id`, `flow_id`) is Sidecar-injected and cannot be overridden by node code.
3. Capability enforcement is performed by the owning service, not by the Sidecar or the SDK.
4. All errors use structured responses with stable error codes from the [Error Catalogue](./error-catalogue.md).
5. Telemetry ingestion failures do not block or fail work execution.
6. State-mutating operations return structured errors with no state change on rejection.
7. gRPC status codes map to error categories: `PERMISSION_DENIED` for capability failures, `FAILED_PRECONDITION` for guard violations, `NOT_FOUND` for missing resources, `ALREADY_EXISTS` for write-once violations, `UNAVAILABLE` for transient service failures, `INVALID_ARGUMENT` for malformed input, `DATA_LOSS` for integrity failures, `DEADLINE_EXCEEDED` for timeout failures, `UNAUTHENTICATED` for identity failures.
8. Inter-service calls (Operator-Archivist, Librarian-Friction Ledger) use the same error model as node-facing calls.
9. Configuration errors (`INVALID_CAPABILITY`, `UNKNOWN_CONTRACT`, `IMPORT_NODE_INVALID`, `SCHEMA_VALIDATION_FAILED`) are caught at CRD admission time and do not appear in runtime gRPC responses. See [Error Catalogue](./error-catalogue.md#configuration-and-validation-errors).

---

## Default Service Ports

These are the default port assignments for the reference implementation. All gRPC services use plaintext transport in the reference implementation; production deployments should use mTLS.

| Service | Port | Protocol |
|---------|------|----------|
| Sidecar | 50051 | gRPC |
| Operator | 50052 | gRPC |
| NodeService (SDK server) | 50053 | gRPC |
| Archivist | 50054 | gRPC |
| Flow Event Bus | 50056 | gRPC |
| Friction Ledger | 50057 | gRPC |
| Librarian | 50058 | gRPC |
| Jury | 50059 | gRPC |
| Clerk | 50060 | gRPC |
| Flow Monitor | 2112 | HTTP (`/metrics`) |
