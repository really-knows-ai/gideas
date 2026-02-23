# Implementation Plan: Child Workitems, NodeGroups, and Codification

## Overview

This plan introduces three orthogonal platform primitives and one applied use case built on top of them. The primitives are general-purpose extensions to Foundry Flow that compose together to enable parallel work decomposition, sub-topology boundaries, and Workitem lifecycle observability.

**Background:** The original goal was to implement Codification Services for translating natural language law goals into formal representations (inspired by the AWS neurosymbolic verification paper). Through design iteration, we determined that codification should not be a special-cased service-to-service RPC but should instead use the normal Workitem processing model via fan-out/fan-in. This led to three platform primitives that are independently useful beyond codification.

### The Three Primitives

| Primitive | Purpose | Depends On |
|-----------|---------|------------|
| **Child Workitems** | Parent-child Workitem relationships with scoped cross-Workitem access | -- |
| **Workitem Lifecycle Event Bus Channel** | Operator publishes phase transitions; nodes subscribe filtered by parent | Child Workitems (uses `parent_workitem_id`) |
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

- [ ] **`specs/02-flow/04-system-services.md`** -- Workitem lifecycle Event Bus channel
  - Add `WORKITEM` channel to Event Bus channels (alongside TELEMETRY, AUDIT, FRICTION)
  - Define event shape: `workitem.phase_changed` with `workitem_id`, `parent_workitem_id`, `phase`, `node_id`
  - Define retention configuration for the new channel

- [ ] **`specs/02-flow/05-configuration.md`** -- NodeGroups and child contract config
  - Add NodeGroups section: inline on FoundryFlow, entry/exit contracts per group, node membership
  - Define routing isolation rules: internal routing within groups, entry-bound bridging
  - Define node-level child Workitem contracts on FoundryNode (`childWorkitems.entryContract`, `childWorkitems.exitContract`)
  - Update behavioural invariants list

- [ ] **`specs/05-reference/crds.md`** -- CRD field definitions
  - Workitem: `ParentWorkitemID` field, `flow.gideas.io/parent` label
  - FoundryFlow: `nodeGroups` map with `NodeGroup` type (entryContracts, exitContracts, nodes)
  - FoundryNode: `childWorkitems` section with optional entry/exit contracts
  - EventBusRetention: `workitemDuration`, `workitemSize`

- [ ] **`specs/05-reference/grpc-api.md`** -- New RPCs and extensions
  - `CreateChildWorkitem` RPC on OperatorService
  - `RouteChild` RPC on OperatorService
  - `GetChildren` RPC on OperatorService
  - Archivist cross-Workitem reads: optional `target_workitem_id` on `GetArtefact`, `ListArtefacts`
  - Event Bus `WORKITEM` channel in `EventChannel` enum
  - `SubscribeFilter` extended with `parent_workitem_id`

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

- [x] **`proto/flow/v1/eventbus.proto`** -- WORKITEM channel
  - Add `WORKITEM` to `EventChannel` enum
  - Add `parent_workitem_id` to `FlowEvent` message
  - Add `parent_workitem_id` to `SubscribeFilter`

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
  - Add `WorkitemDuration` and `WorkitemSize` to `EventBusRetention`

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

## Phase 6: Event Bus WORKITEM Channel

**Goal:** Operator publishes Workitem lifecycle events; nodes can subscribe filtered by parent.

### Checklist

- [ ] **Event Bus implementation** -- Add `WORKITEM` channel support
  - Register the channel in the Event Bus server (same pattern as TELEMETRY, AUDIT, FRICTION)
  - Apply retention config from `EventBusRetention.WorkitemDuration` / `WorkitemSize`

- [ ] **Operator lifecycle publishing**
  - In `workitem_controller.go`, on every phase transition, publish `workitem.phase_changed` to the `WORKITEM` channel
  - Event attributes: `workitem_id`, `parent_workitem_id`, `phase`, `node_id`, `flow_id`
  - Use the same fire-and-forget pattern as existing audit publishing

- [ ] **Event Bus subscribe filter**
  - Extend `SubscribeFilter` handling to support `parent_workitem_id` filter
  - Only return events where the attribute matches

- [ ] **Tests** -- Unit tests for event publishing and filtered subscription

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
| Phase 2: Proto Changes | Complete | All 4 proto files updated, Go code generated, all tests pass |
| Phase 3: CRD Type Changes | Complete | All 4 type files updated, manifests regenerated, deepcopy generated, tests pass |
| Phase 4: Operator -- Child Workitem RPCs | Complete | CreateChildWorkitem, RouteChild, GetChildren RPCs + completion guard + tests |
| Phase 5: Operator -- NodeGroup Validation | Not Started | |
| Phase 6: Event Bus WORKITEM Channel | Not Started | |
| Phase 7: Archivist Cross-Workitem Reads | Not Started | |
| Phase 8: Sidecar Parent-Child Authorization | Not Started | |
| Phase 9: SDK | Not Started | |
| Phase 10: Tests and Quality Gates | Not Started | |
