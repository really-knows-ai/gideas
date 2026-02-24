# Implementation Plan: Child Workitems, NodeGroups, and Codification

## Overview

This plan introduces three orthogonal platform primitives and one applied use case built on top of them. The primitives are general-purpose extensions to Foundry Flow that compose together to enable parallel work decomposition, sub-topology boundaries, and Workitem lifecycle observability.

**Background:** The original goal was to implement Codification Services for translating natural language law goals into formal representations (inspired by the AWS neurosymbolic verification paper). Through design iteration, we determined that codification should not be a special-cased service-to-service RPC but should instead use the normal Workitem processing model via fan-out/fan-in. This led to three platform primitives that are independently useful beyond codification.

### The Three Primitives

| Primitive | Purpose | Depends On |
|-----------|---------|------------|
| **Child Workitems** | Parent-child Workitem relationships with scoped cross-Workitem access | -- |
| **Workitem Lifecycle Event Bus Channel** | Operator publishes phase transitions; nodes subscribe filtered by parent via labels | Child Workitems (uses `parent_workitem_id` label) |
| **NodeGroups** | Sub-topology boundaries within a Flow with entry/exit contracts and routing isolation | -- |

### The Application

| Application | Purpose | Depends On |
|-------------|---------|------------|
| **Codification** | Clerk fans out law codification to parallel Codification nodes, collects formal representations | All three primitives |

---

## Design Decisions Record

Decisions made during planning that inform the implementation:

1. **Child Workitems have simple `Complete()`** -- no exit contract validation. They are internal implementation details, not governed work units. Optional node-level child contracts can be configured on FoundryNode for developers who want validation.

2. **Audit via Event Bus, not response fields** -- decline/failure reasons from Codification Services are emitted as audit events on the Event Bus, not stored as fields on responses. The wire protocol carries only control flow signals (declined: yes/no); the "why" is an audit concern.

3. **Two-agent architecture for codify-smt** -- an Assessor FoundryAgent ("is this goal expressible in SMT-LIB?") and a Generator FoundryAgent ("translate to SMT-LIB"). The assessor gates the generator.

4. **Fan-out-map is not a platform primitive** -- `GetChildren()` provides everything a collecting node needs. How a node developer integrates child Workitem results into the parent is up to them.

5. **GovernedArtefacts are Flow-level CRDs** (namespace-scoped, global to the Flow). They are referenced by contracts at different scopes (Flow, NodeGroup, FoundryNode child contracts) but defined in one place.

6. **NodeGroups are inline on FoundryFlow** -- not a separate CRD, not a field on FoundryNode. The Flow defines topology; nodes don't need to know about grouping.

7. **Route to node, group validates** -- work enters a NodeGroup by routing to a specific entry-bound node within the group. The group's entry contract is validated by the Operator, same as Flow-level entry contracts.

8. **Codification Services become Operator-provisioned nodes** -- configured on the FoundryFlow CRD (similar to how the Judiciary is a mandatory subsystem). The CodificationService CRD is replaced by FoundryFlow codification config.

9. **FlowSupportService CRD is kept** -- for non-codification support services (notification relays, external integrations, etc.).

10. **ChildWorkitem SDK handle** -- `CreateChildWorkitem()` returns a `ChildWorkitem` instance with the same API surface as the parent's Client but scoped to the child. `child.StoreArtefact()`, `child.RouteTo()`, etc.

11. **Generic Event Bus** -- The Event Bus must be channel-agnostic and shape-agnostic. Channels are strings, not a protobuf enum. Adding a new channel or filter dimension must not require code changes, proto regeneration, or rebuilds. Retention is configured per-channel via a map on the CRD, serialized as a single JSON env var. Filtering uses labels (repeated key-value pairs on each event) with server-side AND-match semantics. The `EventChannel` enum, per-channel named retention fields, and named filter predicates (`law_id`, `parent_workitem_id`) introduced in Phase 2/3 are superseded by this design.

12. **Labels for filtering, attributes for metadata** -- Events carry two key-value structures: `labels` (repeated `Label` messages allowing duplicate keys, designed for server-side filtering) and `attributes` (a `map<string, string>` for arbitrary unfiltered metadata). Filtering-relevant dimensions (e.g. `law_id`, `parent_workitem_id`, `phase`) go in labels. Non-filtered metadata (e.g. `magnitude`, `threshold`, `accumulated_friction`) stays in attributes.

13. **Repeated Label message for multi-valued labels** -- Labels use `repeated Label` (not `map<string, string>`) so that a single event can carry multiple values for the same key (e.g. `law_id=law-1` and `law_id=law-2`). The server stores labels in a separate SQLite table indexed on `(key, value)` for efficient filtering. `SubscribeFilter.match_labels` uses AND semantics: every label in the filter must have at least one matching label on the event.

---

## Phase 1: Spec Updates

**Goal:** Update all specification documents to define the three primitives and the codification application. Specs are the authoritative source of truth and must be updated before implementation.

### Checklist

- [ ] **`specs/02-flow/02-workitem.md`** -- Parent-child Workitem relationship
  - Add `ParentWorkitemID` field to Workitem status (Operator-managed)
  - Define child Workitem lifecycle: created by parent-assigned node, simple `Complete()`, no exit contract validation
  - Define completion guard: parent cannot `Complete()` while children are non-terminal
  - Define cross-Workitem artefact access scoped by parent-child relationship
  - Add `ChildWorkitem` handle concept (created by `CreateChildWorkitem`, methods called off it)
  - Update Workitem invariants list

- [ ] **`specs/02-flow/04-system-services.md`** -- Workitem lifecycle Event Bus channel and generic Event Bus design
  - Update Event Bus description: channels are strings (not an enum), the bus is channel-agnostic
  - Describe labels on events: repeated key-value pairs for server-side filtering
  - Describe generic `SubscribeFilter` with `event_type` and `match_labels`
  - Add `workitem` channel to Event Bus channel examples (alongside `telemetry`, `audit`, `friction`)
  - Define event shape: `workitem.phase_changed` with labels `workitem_id`, `parent_workitem_id`, `phase`, `node_id`
  - Define per-channel retention as a map, not per-channel named fields

- [ ] **`specs/02-flow/05-configuration.md`** -- NodeGroups and child contract config
  - Add NodeGroups section: inline on FoundryFlow, entry/exit contracts per group, node membership
  - Define routing isolation rules: internal routing within groups, entry-bound bridging
  - Define node-level child Workitem contracts on FoundryNode (`childWorkitems.entryContract`, `childWorkitems.exitContract`)
  - Update behavioural invariants list

- [ ] **`specs/05-reference/crds.md`** -- CRD field definitions
  - Workitem: `ParentWorkitemID` field, `flow.gideas.io/parent` label
  - FoundryFlow: `nodeGroups` map with `NodeGroup` type (entryContracts, exitContracts, nodes)
  - FoundryNode: `childWorkitems` section with optional entry/exit contracts
  - EventBusRetention: `map[string]RetentionPolicy` with `duration` and `size` per channel

- [ ] **`specs/05-reference/grpc-api.md`** -- New RPCs and extensions
  - `CreateChildWorkitem` RPC on OperatorService
  - `RouteChild` RPC on OperatorService
  - `GetChildren` RPC on OperatorService
  - Archivist cross-Workitem reads: optional `target_workitem_id` on `GetArtefact`, `ListArtefacts`
  - Event Bus: generic string channels, `Label` message, `repeated Label labels` on `FlowEvent`
  - `SubscribeFilter`: `event_type` (convenience) + `repeated Label match_labels` (AND semantics)
  - Remove `EventChannel` enum, `parent_workitem_id` top-level field, named filter fields

- [ ] **`specs/05-reference/error-catalogue.md`** -- New error codes
  - `CHILDREN_NOT_TERMINAL` -- parent `Complete()` with non-terminal children
  - `CHILD_NOT_OWNED` -- operation on a child Workitem not owned by caller's current Workitem
  - `CHILD_ALREADY_ROUTED` -- write to or re-route a child that has already been routed
  - `GROUP_ENTRY_VIOLATION` -- root Workitem routed to group entry node without satisfying group entry contract
  - `GROUP_ROUTING_DENIED` -- routing from outside a group to a non-entry-bound node inside the group

- [ ] **`specs/04-sdk/05-sdk-workitems.md`** -- SDK child Workitem surface
  - `CreateChildWorkitem() (*ChildWorkitem, error)` method on Client
  - `ChildWorkitem` handle: `StoreArtefact`, `StampArtefact`, `RouteTo`, `RouteToOutput`, `Complete`, `ID`
  - `GetChildren(ctx) ([]ChildWorkitemStatus, error)` method on Client
  - `GetChildArtefact(ctx, childID, artefactID)` method on Client
  - `ListChildArtefacts(ctx, childID)` method on Client
  - `WatchChildren(ctx) (<-chan ChildLifecycleEvent, error)` method on Client

- [ ] **`specs/04-sdk/00-overview.md`** -- Update SDK overview
  - Add child Workitem summary to the SDK surface map table
  - Update `FlowSupportService` section to clarify it is kept for non-codification use cases
  - Note that codification uses the Workitem model, not the Support Service model

- [ ] **`specs/01-concepts/01-architecture.md`** -- NodeGroups in architecture
  - Add NodeGroups as a topology concept in the Six-Plane Model overview

- [ ] **Spec lint** -- Run `make check-fix` on all updated spec files

---

## Phase 2: Proto Changes

**Goal:** Define the wire protocol for all three primitives.

### Checklist

- [x] **`proto/flow/v1/operator.proto`** -- New RPCs
  - Add `CreateChildWorkitem` RPC (empty request, returns `child_workitem_id`)
  - Add `RouteChild` RPC (`child_workitem_id` + `routing_instruction`, returns `accepted`)
  - Add `GetChildren` RPC (empty request, returns `repeated ChildWorkitemStatus`)
  - Define `ChildWorkitemStatus` message: `workitem_id`, `phase`, `current_assignee`, `repeated ArtefactRef artefacts`

- [x] **`proto/flow/v1/archivist.proto`** -- Cross-Workitem reads
  - Add optional `target_workitem_id` field to `GetArtefactRequest`
  - Add optional `target_workitem_id` field to `ListArtefactsRequest` (if it exists)

- [x] **`proto/flow/v1/eventbus.proto`** -- WORKITEM channel (**Superseded by Phase 6A**)
  - ~~Add `WORKITEM` to `EventChannel` enum~~ → Enum removed; channels become strings
  - ~~Add `parent_workitem_id` to `FlowEvent` message~~ → Removed; moves to labels
  - ~~Add `parent_workitem_id` to `SubscribeFilter`~~ → Removed; replaced by `match_labels`
  - These proto changes will be replaced by the generic Event Bus proto in Phase 6A

- [x] **`proto/flow/v1/clerk.proto`** -- Codification response (future use)
  - Add `repeated string codification_declines` field to `DraftLawResponse`

- [x] **Generate Go code** -- Run proto generation for all updated `.proto` files

---

## Phase 3: CRD Type Changes

**Goal:** Update Kubernetes CRD types to support child Workitems, NodeGroups, and child contracts.

### Checklist

- [ ] **`operator/api/v1/workitem_types.go`** -- Parent-child link
  - Add `ParentWorkitemID string` to `WorkitemStatus`

- [ ] **`operator/api/v1/foundryflow_types.go`** -- NodeGroups
  - Add `NodeGroups map[string]NodeGroup` to `FoundryFlowSpec`
  - Define `NodeGroup` struct: `EntryContracts`, `ExitContracts`, `Nodes`
  - ~~Add `WorkitemDuration` and `WorkitemSize` to `EventBusRetention`~~ → Superseded by Phase 6C: `EventBusRetention` becomes `map[string]RetentionPolicy`

- [ ] **`operator/api/v1/foundrynode_types.go`** -- Child contracts
  - Add `ChildWorkitems *ChildWorkitemConfig` to `FoundryNodeSpec`
  - Define `ChildWorkitemConfig` struct: `EntryContract Contract`, `ExitContract Contract`

- [ ] **Regenerate manifests** -- `make manifests` and `make generate`

---

## Phase 4: Operator -- Child Workitem RPCs

**Goal:** Implement `CreateChildWorkitem`, `RouteChild`, `GetChildren`, and the completion guard.

### Checklist

- [x] **`operator/internal/rpc/operator_server.go`** -- `CreateChildWorkitem`
  - Extract parent Workitem ID from Sidecar-injected context
  - Validate `CREATE:workitem/child` capability
  - Create child Workitem CRD with `ParentWorkitemID` set
  - Add label `flow.gideas.io/parent: <parent-id>` for efficient querying
  - Child starts in `Pending` with no assignee
  - Publish audit event

- [x] **`operator/internal/rpc/operator_server.go`** -- `RouteChild`
  - Validate child's `ParentWorkitemID` matches caller's current Workitem
  - Validate child is in `Pending` (not yet routed)
  - Validate routing instruction target exists (same validation as `SubmitResult`)
  - Apply routing instruction on the child
  - Publish audit event

- [x] **`operator/internal/rpc/operator_server.go`** -- `GetChildren`
  - List Workitems with label `flow.gideas.io/parent: <caller's workitem id>`
  - Query Archivist for each child's artefact refs
  - Return `ChildWorkitemStatus` for each child

- [x] **`operator/internal/rpc/operator_server.go`** -- Completion guard
  - In `SubmitResult` handler for `complete` instructions, before exit contract validation:
  - Query for children (by parent label)
  - If any child is `Pending` or `Running`, reject with `CHILDREN_NOT_TERMINAL`

- [x] **Capability validation** -- Update `foundrynode_controller.go` capability regex to accept `CREATE:workitem/child`

- [x] **Tests** -- Unit tests for all new RPCs and the completion guard

---

## Phase 5: Operator -- NodeGroup Validation

**Goal:** Implement NodeGroup validation at reconcile time and runtime.

### Checklist

- [ ] **`operator/internal/controller/foundryflow_controller.go`** -- Reconcile-time validation
  - `validateNodeGroups()`: every node in a group must exist as a FoundryNode
  - A node can belong to at most one group
  - Nodes inside a group bind to group contracts (entry/exit fields reference group contract names)
  - Routing outputs from nodes inside a group target nodes in the same group (except entry/exit bridging)
  - Group entry/exit contract stamp references resolve against GovernedArtefacts

- [ ] **`operator/internal/rpc/operator_server.go`** -- Runtime routing validation
  - When routing a root Workitem to an entry-bound group node, validate group entry contract against artefact state
  - When a group exit-bound node calls `Complete()`, validate group exit contract
  - Reject routing from outside a group to a non-entry-bound node inside the group (`GROUP_ROUTING_DENIED`)

- [ ] **`operator/internal/controller/foundrynode_controller.go`** -- Child contract validation
  - If `childWorkitems.entryContract` or `childWorkitems.exitContract` is set, validate stamp references against GovernedArtefacts

- [ ] **Tests** -- Unit tests for NodeGroup reconcile validation and runtime routing

---

## Phase 6: Generic Event Bus Refactoring + WORKITEM Lifecycle Publishing

**Goal:** Make the Event Bus channel-agnostic and shape-agnostic so that adding channels, filter dimensions, or event shapes never requires code changes, proto regeneration, or rebuilds. Then use the generic bus to publish Workitem lifecycle events.

**Motivation:** The original Event Bus design hard-coded channel names as a protobuf enum, retention as per-channel named CRD fields and env vars, and filter predicates as named struct fields. Adding the WORKITEM channel exposed that every new channel or filter dimension requires changes across ~8 modules, proto regen, and full rebuilds. Per AGENTS.md compatibility policy, we remove the old design rather than accumulate backward-compat debt.

### Phase 6A: Proto Changes -- Generic Event Bus

**Goal:** Remove the channel enum, add labels, make filtering generic.

- [ ] **`proto/flow/v1/eventbus.proto`** -- Remove `EventChannel` enum entirely

- [ ] **`proto/flow/v1/eventbus.proto`** -- Add `Label` message
  - `string key = 1`
  - `string value = 2`

- [ ] **`proto/flow/v1/eventbus.proto`** -- Update `FlowEvent` message
  - Field 3 `channel`: change from `EventChannel` to `string`
  - Remove field 12 `parent_workitem_id` (moves to labels)
  - Add field 13 `repeated Label labels`

- [ ] **`proto/flow/v1/eventbus.proto`** -- Update `PublishRequest`
  - Field 1 `channel`: change from `EventChannel` to `string`

- [ ] **`proto/flow/v1/eventbus.proto`** -- Update `SubscribeRequest`
  - Field 1 `channel`: change from `EventChannel` to `string`

- [ ] **`proto/flow/v1/eventbus.proto`** -- Update `SubscribeFilter`
  - Keep field 1 `string event_type` (convenience -- every subscriber uses it)
  - Remove field 2 `string law_id`
  - Remove field 3 `string parent_workitem_id`
  - Add field 4 `repeated Label match_labels` (AND semantics: every label must match)

- [ ] **Generate Go code** -- Run proto generation for all updated `.proto` files

### Phase 6B: Event Bus Server -- Generic Channels, Labels, Filtering

**Goal:** Update the Event Bus server to use string channels, store labels, and filter generically.

- [ ] **`eventbus/internal/store/sqlite/store.go`** -- Schema and type changes
  - Change `Event.Channel` from `int32` to `string`
  - Add `Labels []Label` to `Event` struct (where `Label` is `struct{Key, Value string}`)
  - Update SQLite schema: `channel` column becomes `TEXT`
  - Add `event_labels` table: `channel TEXT, sequence INTEGER, key TEXT, value TEXT`
  - Add index: `CREATE INDEX idx_labels_kv ON event_labels (key, value, channel, sequence)`
  - Update `Insert()` to insert labels into `event_labels` in the same transaction
  - Update `GetSince()` to join and return labels
  - Update `Evict()` to cascade-delete from `event_labels`
  - Update per-channel sequence map from `map[int32]uint64` to `map[string]uint64`
  - Update `loadSequences()` for string channel keys

- [ ] **`eventbus/internal/service/subscriber.go`** -- Generic filtering
  - Change `subscribeFilter` to: `eventType string` + `matchLabels []Label`
  - Update `matchesFilter()`: for each match label, check if the event has at least one label with the same key and value
  - Remove `lawID` field, `containsElement()` function
  - Update `registry.subs` from `map[int32][]*subscriber` to `map[string][]*subscriber`

- [ ] **`eventbus/internal/service/eventbus_server.go`** -- Server logic
  - Update `Publish()` validation: channel must be non-empty string (replace `UNSPECIFIED` check)
  - Update `storeEvt` construction to map proto labels to store labels
  - Update `toProto()` to map store labels back to proto labels
  - Update `Subscribe()` filter extraction: read `match_labels` from filter
  - Update `retention` map from `map[int32]RetentionConfig` to `map[string]RetentionConfig`

- [ ] **`eventbus/cmd/main.go`** -- Generic retention loading
  - Replace `loadRetention()`: read a single `EVENT_BUS_RETENTION_CONFIG` env var containing JSON (e.g. `{"telemetry":{"duration":"24h","size":"100MB"},"audit":{"duration":"168h"}}`)
  - Remove per-channel env var parsing and `EventChannel` enum references
  - Remove the `channelEnv` struct and per-channel loop

- [ ] **Tests** -- Update all Event Bus tests
  - `eventbus/internal/store/sqlite/store_test.go`: string channels, label insertion/retrieval
  - `eventbus/internal/service/eventbus_server_test.go`: string channels, label-based filtering, remove enum references

### Phase 6C: CRD Types -- Generic Retention

**Goal:** Replace per-channel named retention fields with a map.

- [ ] **`operator/api/v1/foundryflow_types.go`** -- Replace `EventBusRetention` struct
  - Change from per-channel named fields to: `type EventBusRetention map[string]RetentionPolicy`
  - Add `RetentionPolicy` struct: `Duration string`, `Size string`
  - Remove `TelemetryDuration`, `TelemetrySize`, `AuditDuration`, `AuditSize`, `FrictionDuration`, `FrictionSize`, `WorkitemDuration`, `WorkitemSize` named fields

- [ ] **Regenerate manifests** -- `make manifests` and `make generate`

### Phase 6D: Operator -- Generic Env Var Wiring

**Goal:** Operator passes retention config as a single JSON env var.

- [ ] **`operator/internal/controller/foundryflow_infra.go`** -- Update `eventBusEnvVars()`
  - Replace per-channel `optionalEnv()` calls with a single JSON env var `EVENT_BUS_RETENTION_CONFIG`
  - Serialize `flow.Spec.EventBusConfig.Retention` as JSON

- [ ] **`operator/internal/controller/foundryflow_controller_test.go`** -- Update retention tests
  - Update env var assertions from per-channel named vars to single JSON var

### Phase 6E: All Publishers and Subscribers -- String Channels + Labels

**Goal:** Update every module that publishes to or subscribes from the Event Bus.

- [ ] **Define channel constants** -- String constants (e.g. `"telemetry"`, `"audit"`, `"friction"`, `"workitem"`) used by publishers and subscribers. Each module can define its own or use string literals.

- [ ] **`operator/internal/rpc/operator_server.go`** -- Audit publishing
  - `EVENT_CHANNEL_AUDIT` → `"audit"`
  - No label changes (audit events have no labels currently)

- [ ] **`operator/internal/controller/workitem_controller.go`** -- Audit publishing
  - `EVENT_CHANNEL_AUDIT` → `"audit"` (via `flowv1gen` alias removal)
  - Remove `flowv1gen.EventChannel_EVENT_CHANNEL_AUDIT` reference

- [ ] **`sidecar/internal/proxy/eventbus.go`** -- Friction and telemetry publishing
  - `EVENT_CHANNEL_TELEMETRY` → `"telemetry"`
  - Move `law_ids` CSV from `attributes["law_ids"]` to repeated labels: `{law_id, law-1}, {law_id, law-2}`
  - Keep `magnitude` in attributes (not a filtering dimension)

- [ ] **`frictionledger/internal/service/frictionledger_server.go`** -- Subscribe + publish
  - Subscribe channel: `EVENT_CHANNEL_TELEMETRY` → `"telemetry"`
  - Publish channel: `EVENT_CHANNEL_FRICTION` → `"friction"`
  - Update `processEvent()` to read `law_id` from labels instead of `attributes["law_ids"]` CSV
  - Add `law_id` label on `friction.threshold_crossed` events

- [ ] **`librarian/internal/service/hearing_trigger.go`** -- Subscribe + publish
  - Subscribe channel: `EVENT_CHANNEL_FRICTION` → `"friction"`
  - Publish channel: `EVENT_CHANNEL_AUDIT` → `"audit"`
  - Update event consumption to read `law_id` from labels

- [ ] **`librarian/internal/service/librarian_server.go`** -- Audit publishing
  - `EVENT_CHANNEL_AUDIT` → `"audit"`

- [ ] **`archivist/internal/service/archivist_server.go`** -- Audit publishing
  - `EVENT_CHANNEL_AUDIT` → `"audit"`

- [ ] **`monitor/internal/subscriber/telemetry.go`** -- Telemetry subscription
  - `EVENT_CHANNEL_TELEMETRY` → `"telemetry"`
  - Update `processFriction()` to read `law_id` from labels instead of `attributes["law_ids"]` CSV
  - Update `processThresholdCrossing()` to read `law_id` from labels

- [ ] **`monitor/internal/subscriber/audit.go`** -- Audit subscription
  - `EVENT_CHANNEL_AUDIT` → `"audit"`

- [ ] **`frictionledger/internal/service/frictionledger_server.go`** -- Update `SubscribeFilter`
  - `SubscribeFilter{EventType: "friction"}` → keep as-is (`event_type` convenience field retained)
  - Remove any `LawId` filter usage (unused today, but clean up references)

- [ ] **`librarian/internal/service/hearing_trigger.go`** -- Update `SubscribeFilter`
  - `SubscribeFilter{EventType: "friction.threshold_crossed"}` → keep as-is

- [ ] **Tests for all updated modules**
  - `sidecar/internal/proxy/eventbus_test.go`
  - `frictionledger/internal/service/frictionledger_server_test.go`
  - `librarian/internal/service/hearing_trigger_test.go`
  - `archivist/internal/service/audit_test.go`
  - `monitor/internal/subscriber/subscriber_test.go`
  - `operator/internal/rpc/audit_test.go`

### Phase 6F: Helm Chart -- Generic Retention

**Goal:** Update the Helm chart to use the generic retention config pattern.

- [ ] **`charts/foundry-flow/templates/eventbus.yaml`** -- Replace per-channel env var blocks
  - Replace individual `EVENT_BUS_RETENTION_<CHANNEL>_DURATION` / `_SIZE` blocks with a single `EVENT_BUS_RETENTION_CONFIG` env var
  - Serialize `.Values.eventbus.retention` as JSON

- [ ] **`charts/foundry-flow/values.yaml`** -- Update retention structure
  - Keep the nested map structure (each key is a channel name with `duration` and `size` subkeys)
  - Add `workitem` channel entry with empty defaults
  - Structure:
    ```yaml
    eventbus:
      retention:
        telemetry:
          duration: ""
          size: ""
        audit:
          duration: ""
          size: ""
        friction:
          duration: ""
          size: ""
        workitem:
          duration: ""
          size: ""
    ```

### Phase 6G: Workitem Lifecycle Publishing

**Goal:** With the generic Event Bus in place, publish Workitem lifecycle events.

- [ ] **`operator/internal/controller/workitem_controller.go`** -- Add `publishLifecycle()` helper
  - Same fire-and-forget pattern as existing `publishAudit()`
  - Channel: `"workitem"`
  - Event type: `"workitem.phase_changed"`
  - Labels: `[{workitem_id, <id>}, {phase, <phase>}, {node_id, <node>}]`
  - If the Workitem has a `ParentWorkitemID`, add label `{parent_workitem_id, <parent_id>}`
  - Attributes: `flow_id` and any additional context

- [ ] **Call `publishLifecycle()` at every phase transition**
  - `reconcilePending()` → on thrash budget failure (phase = `Failed`)
  - `reconcilePending()` → on successful Running transition (phase = `Running`)
  - `reconcileRunning()` → on timeout failure (phase = `Failed`)
  - `reconcileRouting()` → on guard failure (phase = `Failed`)
  - `reconcileRouting()` → on completion (phase = `Completed`)
  - `reconcileRouting()` → on routing to next node (phase = `Pending`, new assignee)

- [ ] **Tests** -- Unit tests for lifecycle publishing

### Phase 6H: Spec Updates

**Goal:** Update specs to reflect the generic Event Bus design.

- [ ] **`specs/02-flow/04-system-services.md`** -- Event Bus is channel-agnostic, labels, generic filtering (already in Phase 1 checklist but may need implementation-informed updates)

- [ ] **`specs/05-reference/grpc-api.md`** -- Updated proto surface: string channels, `Label` message, `repeated Label labels` on `FlowEvent`, generic `SubscribeFilter`

- [ ] **`specs/05-reference/crds.md`** -- Generic retention map on `EventBusRetention`

- [ ] **Spec lint** -- Run `make check-fix` on all updated spec files

### Phase 6I: Tests and Quality Gates

**Goal:** Full quality gate pass after all Phase 6 changes.

- [ ] **`go test ./...`** across all modules (eventbus, operator, sidecar, frictionledger, librarian, archivist, monitor, sdk, clerk, nodes)
- [ ] **`make check-fix`** -- resolve all lint issues
- [ ] **Verify no module references `EventChannel` enum** -- grep for `EventChannel_` across the codebase (excluding `gen/`)

### Blast Radius Summary

| Module | Files Changed | Nature of Change |
|--------|--------------|-----------------|
| `proto/` | 1 | Schema: remove enum, add labels, string channels |
| `gen/` | Auto-regenerated | -- |
| `eventbus/` | ~5 | Core: store, subscriber, server, main, tests |
| `operator/` | ~5 | CRD types, infra env vars, workitem controller, tests |
| `sidecar/` | ~2 | Proxy + test: string channels, labels |
| `frictionledger/` | ~2 | Server + test: string channels, labels |
| `librarian/` | ~3 | Server, trigger, tests: string channels |
| `archivist/` | ~2 | Server + test: string channels |
| `monitor/` | ~3 | Telemetry, audit, tests: string channels, label consumption |
| `charts/` | 2 | Template + values: generic retention |
| `specs/` | ~3 | Documentation updates |
| **Total** | **~28 files** | |

### Execution Order

6A → 6B → 6C → 6D → 6E → 6F → 6G → 6H → 6I

6A (proto) must go first since everything depends on the generated code. 6B (Event Bus server) should follow immediately since it's the core. 6C-6D (CRD + operator wiring) can follow. 6E (all publishers/subscribers) is the bulk of the work. 6G (lifecycle publishing) is the actual WORKITEM feature, now trivial because the bus is generic. 6H-6I are cleanup and verification.

---

## Phase 7: Archivist Cross-Workitem Reads

**Goal:** Allow a parent-assigned node to read artefacts from completed child Workitems.

### Checklist

- [ ] **Archivist service** -- Extend `GetArtefact` and `ListArtefacts`
  - Accept optional `target_workitem_id`
  - When set, validate that the caller's Workitem is the parent of the target (query Workitem CRD for `ParentWorkitemID`)
  - Read-only access (no cross-Workitem writes via this path)
  - Target child must be in `Completed` state

- [ ] **Sidecar proxy** -- Forward `target_workitem_id` through the Archivist proxy
  - The identity interceptor must allow calls with a `target_workitem_id` that differs from the active session's Workitem ID (when parent-child relationship is valid)

- [ ] **Tests** -- Unit tests for cross-Workitem reads (valid parent-child, invalid, non-completed child)

---

## Phase 8: Sidecar Parent-Child Authorization

**Goal:** The Sidecar authorizes cross-scope operations by validating parent-child relationships.

### Checklist

- [ ] **`sidecar/internal/service/identity_interceptor.go`** -- Authorization logic
  - During child setup (before `RouteChild`): parent node can write artefacts to child Workitem IDs
  - During collection (after child completes): parent-assigned node can read from child Workitem IDs
  - Validate by querying Operator for parent-child relationship (or caching child IDs from `CreateChildWorkitem`)

- [ ] **`sidecar/internal/service/sidecar_server.go`** -- Session tracking
  - Track child Workitem IDs created during the current session (for the setup phase)
  - For the collection phase (different node may hold the parent), validate via Operator query

- [ ] **Tests** -- Unit tests for authorization (valid cross-scope, invalid, routed child write rejection)

---

## Phase 9: SDK

**Goal:** Expose child Workitem operations to node developers.

### Checklist

- [ ] **`sdk/go/client.go`** -- New methods on Client
  - `CreateChildWorkitem(ctx) (*ChildWorkitem, error)`
  - `GetChildren(ctx) ([]ChildWorkitemStatus, error)`
  - `GetChildArtefact(ctx, childWorkitemID, artefactID string) (*flowv1.GetArtefactResponse, error)`
  - `ListChildArtefacts(ctx, childWorkitemID string) ([]*flowv1.ArtefactRef, error)`
  - `WatchChildren(ctx) (<-chan ChildLifecycleEvent, error)` -- wraps Event Bus subscription

- [ ] **`sdk/go/child.go`** -- ChildWorkitem handle (new file)
  - `ChildWorkitem` struct wrapping gRPC clients with child's Workitem ID
  - `ID() string`
  - `StoreArtefact(ctx, artefactID, governedArtefact string, content []byte) error`
  - `StampArtefact(ctx, artefactID, stampName string) error`
  - `RouteTo(ctx, targetNode string) (bool, error)`
  - `RouteToOutput(ctx, outputName string) (bool, error)`
  - `Complete(ctx) (bool, error)` -- simple completion, no target

- [ ] **`sdk/go/child.go`** -- ChildWorkitemStatus and ChildLifecycleEvent types
  - `ChildWorkitemStatus`: `WorkitemID`, `Phase`, `CurrentAssignee`, `Artefacts`
  - `ChildLifecycleEvent`: `WorkitemID`, `Phase`, `NodeID`

- [ ] **Tests** -- Unit tests for the ChildWorkitem handle and Client methods

---

## Phase 10: Tests and Quality Gates

**Goal:** Ensure all changes pass quality gates per AGENTS.md.

### Checklist

- [ ] **`go test ./...`** across all modules (operator, sidecar, sdk, clerk, nodes, proto)
- [ ] **`make check-fix`** -- resolve all lint issues
- [ ] **`make test-all`** -- full test suite
- [ ] **`make check-all`** -- full lint suite
- [ ] **Integration verification** -- end-to-end test of child Workitem creation, artefact attachment, routing, completion, and parent collection

---

## Future Work: Codification Application

These phases build on the three primitives above and are out of scope for this implementation pass. They are documented here for continuity.

### Codification Phase A: Clerk Refactoring

- Refactor Clerk from a core service to a FoundryNode
- Clerk becomes the fan-out node: drafts prose, creates child Workitems per codification node
- Clerk uses `WatchChildren()` to monitor child completion, then collects formal representations via `GetChildArtefact()`
- Clerk assembles the law (prose + all successful formal representations) and calls `WriteLaw`
- Decline handling: child produces no output artefact (or marks a "declined" artefact); Clerk skips it
- Failure handling: child Workitem fails; Clerk logs and skips
- Audit events emitted to Event Bus for codification outcomes

### Codification Phase B: codify-smt Node

- New node: `nodes/codify-smt/`
- Two FoundryAgents:
  - **Assessor Agent** -- "Is this goal expressible in SMT-LIB?" Structured yes/no output
  - **Generator Agent** -- "Translate this goal into SMT-LIB." Only invoked if assessor says yes
- Receives child Workitem with `codification-input` artefact (law goal)
- Produces `codification-output` artefact (SMT-LIB representation) or completes without output (decline)
- Emits audit event with decline reasoning (assessor output) via Event Bus

### Codification Phase C: Operator Provisioning

- FoundryFlow CRD codification config section (like Judiciary provisioning)
- Operator auto-provisions codification nodes as FoundryNodes
- Operator provisions codification NodeGroup with entry/exit contracts
- Wires routing topology (Clerk -> codification nodes -> fan-in)

### Codification Phase D: CodificationService CRD Removal

- Remove `CodificationService` CRD type and controller (replaced by FoundryFlow config + FoundryNode)
- Update specs to remove CodificationService references
- Keep `FlowSupportService` CRD for non-codification support services

---

## Progress Tracker

| Phase | Status | Notes |
|-------|--------|-------|
| Phase 1: Spec Updates | Complete | All 8 spec files updated, lint clean |
| Phase 2: Proto Changes | Complete | All 4 proto files updated, Go code generated, all tests pass. eventbus.proto changes superseded by Phase 6A. |
| Phase 3: CRD Type Changes | Complete | All 4 type files updated, manifests regenerated, deepcopy generated, tests pass. `EventBusRetention` named fields superseded by Phase 6C. |
| Phase 4: Operator -- Child Workitem RPCs | Complete | CreateChildWorkitem, RouteChild, GetChildren RPCs + completion guard + tests |
| Phase 5: Operator -- NodeGroup Validation | Complete | validateNodeGroups, runtime GROUP_ROUTING_DENIED, child contract validation + tests |
| Phase 6A: Proto -- Generic Event Bus | Not Started | Remove `EventChannel` enum, add `Label` message, string channels, generic `SubscribeFilter` |
| Phase 6B: Event Bus Server | Not Started | String channels, label storage, generic filtering, JSON retention config |
| Phase 6C: CRD Types -- Generic Retention | Not Started | `EventBusRetention` becomes `map[string]RetentionPolicy` |
| Phase 6D: Operator -- Generic Env Vars | Not Started | Single `EVENT_BUS_RETENTION_CONFIG` JSON env var |
| Phase 6E: Publishers/Subscribers | Not Started | ~28 files across 8 modules: string channels + labels |
| Phase 6F: Helm Chart | Not Started | Generic retention env var |
| Phase 6G: Workitem Lifecycle Publishing | Not Started | `publishLifecycle()` in workitem controller |
| Phase 6H: Spec Updates | Not Started | Document generic Event Bus design |
| Phase 6I: Tests and Quality Gates | Not Started | Full cross-module test pass |
| Phase 7: Archivist Cross-Workitem Reads | Not Started | |
| Phase 8: Sidecar Parent-Child Authorization | Not Started | |
| Phase 9: SDK | Not Started | |
| Phase 10: Tests and Quality Gates | Not Started | |
