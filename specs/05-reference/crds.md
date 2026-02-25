# CRD Reference

All custom resources use API group `flow.gideas.io/v1` and are namespace-scoped.

## Resource Inventory

| CRD | Owner | Purpose |
|-----|-------|---------|
| [FoundryFlow](#foundryflow) | Flow Architect / Operator | Flow-wide contracts, governance policy, cross-flow settings |
| [FoundryNode](#foundrynode) | Flow Architect / Operator | Node-local behaviour, capabilities, routing outputs, contract bindings |
| [Workitem](#workitem) | Operator (sole mutator) | Workitem lifecycle state, assignment, routing |
| [GovernedArtefact](#governedartefact) | Flow Architect | Governed artefact registration and stamp vocabulary |
| [Law](#law) | Librarian / Judiciary / nodes | Law goal, representations, tier, lifecycle metadata |
| [Treaty](#treaty) | Flow Architect | Directed cross-flow trust policy |
| [FlowSupportService](#flowsupportservice) | Flow Architect / Operator | Support Service capability declaration and infrastructure |
| [CodificationService](#codificationservice) | Flow Architect / Operator | Codification Service: output format declaration and deployment |

---

## FoundryFlow

The FoundryFlow CRD defines the executable shape of a Flow. The [Operator](../02-flow/01-operator.md) reconciles this resource as the source of truth for all flow-wide behavioural semantics. Detail: [Configuration Semantics](../02-flow/05-configuration.md).

### `spec`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `entryContracts` | `map[string]Contract` | yes | Named entry contracts. Each contract is a [Contract shape](#contract-shape). At least one entry contract must be defined. |
| `exitContracts` | `map[string]Contract` | yes | Named exit contracts. Each contract is a [Contract shape](#contract-shape). |
| `nodeGroups` | `map[string]NodeGroup` | no | Named NodeGroups defining sub-topology boundaries. Each key is a group name; each value is a [NodeGroup shape](#nodegroup-shape). See [NodeGroups](../02-flow/05-configuration.md#nodegroups). |
| `importNode` | `string` | no | Name of the FoundryNode that receives cross-flow imported Workitems. Must reference an existing entry-bound node. If absent, cross-flow import is disabled. |
| `governancePolicy` | `GovernancePolicy` | yes | Governance thresholds and timers. See [governance policy](#governance-policy). |
| `crossFlow` | `CrossFlowConfig` | no | Cross-flow trust and naturalisation settings. See [cross-flow configuration](#cross-flow-configuration). |
| `eventBus` | `EventBusConfig` | no | Flow Event Bus configuration. See [Event Bus configuration](#event-bus-configuration). |

### The Judiciary

The Judiciary is a runtime-mandated subsystem — the Operator provisions it without requiring separate FoundryNode CRDs. It comprises three nodes and two core services:

**Judiciary Nodes:**

| Node | Role | Provisioning |
|---|---|---|
| **Arbiter** | Resolves deadlocked feedback disputes. Invokes the Jury for deliberation, uses the Clerk to draft rulings, resolves feedback with `linkedRuling`, routes back to Sort. | Operator-provisioned |
| **Tribunal** | Conducts review hearings on laws. Receives hearing Workitems, invokes the Jury for deliberation, uses the Clerk (promote) or routes to Advocate (escalate). | Operator-provisioned |
| **Advocate** | Human escalation point. Receives hung jury escalations and Tier 3+ proposals. Uses the SDK [HITL pattern](../04-sdk/08-sdk-hitl.md) with `USE:queue/server` capability. | Operator-provisioned |

**Judiciary Services:**

| Service | Role | Provisioning |
|---|---|---|
| **Jury** | Multi-agent deliberation engine. Runs parallel FoundryAgent instances, collects votes, applies consensus strategy, returns structured verdict. | Operator-provisioned |
| **Clerk** | Law drafting and codification coordination. Drafts prose, dispatches to Codification Services, assembles law, calls WriteLaw. | Operator-provisioned |

The Judiciary's capabilities are fixed by the runtime (not configurable by the Flow Architect). The Arbiter and Tribunal hold `WRITE:law/tier2`, `READ:law`, `WRITE:friction`, feedback resolution capabilities, stamp application for hearing artefacts, and access to all registered [CodificationService](#codificationservice) instances (the Operator internally manages their `USE:support/<name>/encode` capability for each). The Advocate holds `USE:queue/server` and `spec.storage` for HITL queue persistence, and is deployed as a StatefulSet with a Headless Service.

The Operator also provisions a `law-reference` GovernedArtefact alongside the Tribunal. Its stamp vocabulary is empty. The `law-reference` artefact's content is a plain-text string containing the target law ID. The Tribunal's hearing entry contract requires a single `law-reference` artefact; its hearing exit contract requires it to still be present.

### Governance Policy

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `maxVisits` | `integer` | yes | Thrash Guard budget. When the aggregate visit count across all nodes exceeds this value, the Workitem fails with `THRASH_BUDGET_EXCEEDED`. |
| `defaultTimeout` | `duration` | yes | Default inactivity timeout for node assignments. Used as the fallback when no node-specific timeout is set in FoundryNode. |
| `maxTimeout` | `duration` | yes | Maximum inactivity timeout for node assignments. No node-specific timeout can exceed this value. Must be >= `defaultTimeout`. |
| `frictionThresholds` | `FrictionThresholds` | no | Per-tier friction thresholds that trigger review hearings. |
| `reviewTTLs` | `ReviewTTLs` | no | Per-tier time-to-live durations that trigger review hearings. |
| `retentionPolicy` | `RetentionPolicy` | no | Retention duration for terminal Workitems before garbage collection. |

### FrictionThresholds

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `tier1` | `float` | no | Accumulated friction threshold for Tier 1 laws (Findings). |
| `tier2` | `float` | no | Accumulated friction threshold for Tier 2 laws (Rulings). |
| `tier3` | `float` | no | Accumulated friction threshold for Tier 3 laws (Local Statutes). |
| `tier4` | `float` | no | Accumulated friction threshold for Tier 4 laws (State Constitutions). |
| `tier5` | `float` | no | Accumulated friction threshold for Tier 5 laws (Federal Accords). |

When a law's accumulated friction crosses its tier's configured threshold, the Librarian triggers a review hearing. For Tiers 1-2, the [Tribunal](../02-flow/03-nodes-external.md#the-judiciary--standard-subsystem) adjudicates directly. For Tiers 3-5, the hearing outcome is a petition to the Flow Architect or Governance Flow.

### ReviewTTLs

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `tier1` | `duration` | no | Time-to-live for Tier 1 laws (Findings). |
| `tier2` | `duration` | no | Time-to-live for Tier 2 laws (Rulings). |
| `tier3` | `duration` | no | Time-to-live for Tier 3 laws (Local Statutes). |
| `tier4` | `duration` | no | Time-to-live for Tier 4 laws (State Constitutions). |
| `tier5` | `duration` | no | Time-to-live for Tier 5 laws (Federal Accords). |

When a law's age exceeds its tier's configured TTL, the Librarian triggers a review hearing. The law remains active during the hearing. Like friction thresholds, Tiers 1-2 hearings are adjudicated by the [Tribunal](../02-flow/03-nodes-external.md#the-judiciary--standard-subsystem); Tiers 3-5 produce petitions.

### RetentionPolicy

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `maxAge` | `duration` | no | Maximum age of terminal Workitems before garbage collection. |

### Cross-Flow Configuration

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `stateRootCA` | `string` | no | PEM-encoded State Root CA certificate. Present when the Flow operates under a Governance Flow. |
| `naturalisation` | `NaturalisationConfig` | no | Policy for naturalising imported artefacts and stamps at treaty boundaries. |

### NaturalisationConfig

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `autoNaturalise` | `bool` | no | When `true`, imported stamps from sibling Flows are automatically authoritative after chain verification. Default `true` for sibling Flows. |
| `requireLocalStamps` | `[]string` | no | List of local stamp names that must be applied to imported artefacts during naturalisation at treaty boundaries. |

### Event Bus Configuration

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `retention` | `EventBusRetention` | no | Per-channel retention configuration. |

### EventBusRetention

Retention is configured as a map of channel name to retention policy. Each key is a channel string (e.g. `"telemetry"`, `"audit"`, `"friction"`, `"workitem"`); each value specifies duration and/or size limits. The Bus is channel-agnostic — adding retention for a new channel requires only a map entry, not a schema change.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `<channel>` | `EventBusRetentionPolicy` | no | Per-channel retention policy. Each entry is keyed by channel name. |

#### EventBusRetentionPolicy

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `duration` | `string` | no | Duration-based retention (e.g. `"24h"`, `"168h"`). Go `time.Duration` format. |
| `size` | `string` | no | Size-based retention (e.g. `"100MB"`, `"1GB"`). Byte-count string with unit suffix. |

When both duration and size are specified for a channel, the Event Bus evicts when either limit is exceeded (whichever triggers first).

```yaml
# Example: per-channel retention configuration
eventBus:
  retention:
    telemetry:
      duration: "24h"
      size: "100MB"
    audit:
      duration: "168h"
      size: "1GB"
    friction:
      duration: "72h"
    workitem:
      duration: "24h"
```

The Operator serialises the retention map as a single `EVENT_BUS_RETENTION_CONFIG` JSON environment variable for the Event Bus deployment.

### `status`

| Field | Type | Description |
|-------|------|-------------|
| `phase` | `string` | Reconciliation state: `Initialising`, `Ready`, `Degraded`, `Failed`. |
| `conditions` | `[]Condition` | Standard Kubernetes conditions for reconciliation health. |

---

## FoundryNode

The FoundryNode CRD defines node-local behaviour, permission envelope, and routing topology. The [Operator](../02-flow/01-operator.md) reconciles this resource against Flow-level constraints. Detail: [Node Configuration](../03-node/02-configuration.md).

### `spec`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `image` | `string` | yes | Container image for the node. |
| `outputs` | `[]Output` | no | Named routing outputs. Each output maps a name to a target node. |
| `capabilities` | `[]string` | no | Capability grant strings. Grammar: `VERB:RESOURCE[/QUALIFIER]`. See [capability syntax](#capability-syntax). |
| `entry` | `string` | no | Name of the entry contract this node is bound to. Must reference a key in the FoundryFlow's `entryContracts`. |
| `exit` | `string` | no | Name of the exit contract this node is bound to. Must reference a key in the FoundryFlow's `exitContracts`. Grants `complete()` eligibility. |
| `timeout` | `duration` | no | Inactivity timeout for assignments to this node. Cannot exceed `governancePolicy.maxTimeout` on the FoundryFlow. Falls back to `governancePolicy.defaultTimeout` if unset. |
| `concurrency` | `integer` | no | Maximum concurrent Workitem assignments per pod. Default `1`. Value `0` means unlimited. |
| `childWorkitems` | `ChildWorkitemConfig` | no | Optional child Workitem contract configuration. See [child Workitem contracts](#childworkitemconfig). |
| `storage` | `StorageConfig` | no | Volume mounts and deployment strategy. Presence of persistent volumes triggers StatefulSet deployment; otherwise ReplicaSet (default). |

### Output

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | `string` | yes | Output channel name. Referenced by `route_to_output` instructions. |
| `target` | `string` | yes | Target node name. Must reference an existing FoundryNode in the namespace. |

### StorageConfig

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `volumes` | `[]VolumeMount` | no | Volume mount declarations. Injected into both node and Sidecar containers. |
| `deploymentStrategy` | `string` | no | `ReplicaSet` (default) or `StatefulSet`. |

### Capability Syntax

Capability grants follow a `VERB:RESOURCE[/QUALIFIER]` grammar:

| Pattern | Description |
|---------|-------------|
| `READ:artefact` | Read artefact content and metadata. |
| `WRITE:artefact` | Write artefact content (all governed artefacts). |
| `WRITE:artefact/<name>` | Write artefact content scoped to a specific governed artefact. |
| `READ:law` | Query laws from the Library. |
| `WRITE:law/tier1` | Write Tier 1 laws (Findings). |
| `WRITE:law/tier2` | Write Tier 2 and below (Rulings, Findings). |
| `WRITE:law/tier3` | Write Tier 3 and below (Local Statutes, Rulings, Findings). |
| `WRITE:law/tier4` | Write Tier 4 and below (State Constitutions and all lower tiers). |
| `WRITE:law/tier5` | Write Tier 5 and below (all tiers). |
| `WRITE:friction` | Emit friction events (`AddFriction`, `Cite`). Enforced by the Sidecar. |
| `STAMP:artefact/<name>/<stamp-name>` | Apply a named stamp to a specific governed artefact. Exact match on both governed artefact name and stamp name. |
| `READ:flow` | Query Flow configuration and node routing graph. Enables stamp-to-node mapping discovery. |
| `READ:workitem` | Read Workitem state beyond the current assignment. |
| `READ:feedback` | Read feedback items on artefacts. |
| `WRITE:feedback/new` | Create feedback items (`AddFeedback`). |
| `WRITE:feedback/actioned` | Transition feedback to `actioned` (`ResolveFeedback`). |
| `WRITE:feedback/wont_fix` | Transition feedback to `wont_fix` (`RefuseFeedback`). |
| `WRITE:feedback/rejected` | Transition feedback to `rejected` (`RejectFix`, `RejectRefusal`). |
| `WRITE:feedback/resolved` | Transition feedback to `resolved` (`AcceptFix`, `AcceptRefusal`). |
| `WRITE:feedback/deadlocked` | Transition feedback to `deadlocked` (`DeadlockFeedback`). |
| `USE:support/<service>/<capability>` | Invoke a specific Flow Support Service capability. |
| `USE:queue/server` | Enables HITL queue features: persistent queue, REST API, Federated Queue Mesh. Requires `spec.storage`. Triggers StatefulSet deployment and Headless Service creation. See [SDK HITL](../04-sdk/08-sdk-hitl.md). |
| `CREATE:workitem/child` | Create child Workitems from the currently assigned parent Workitem. See [Child Workitems](../02-flow/02-workitem.md#child-workitems). |

Some operations (such as `ListArtefacts` — listing artefacts associated with the assigned Workitem via the Archivist) are implicitly available to all nodes by virtue of the assignment scope and do not require explicit capability grants.

Malformed capability strings are rejected at configuration admission. The Operator does not reconcile a FoundryNode with syntactically invalid capabilities.

---

## Workitem

The Workitem CRD is a pure control-plane state machine for a unit of work. It carries lifecycle state, assignment ownership, routing outcomes, and loop-detection counters. The [Operator](../02-flow/01-operator.md) is the sole mutator. Nodes interact through [SDK abstractions](../04-sdk/05-sdk-workitems.md), not CRD field paths. Detail: [Workitem Runtime](../02-flow/02-workitem.md).

The Workitem CRD has no `spec` block. It is created by the Operator and all mutable state lives in `status`.

### `status`

Managed by the Operator. Nodes do not write to `status` directly.

| Field | Type | Description |
|-------|------|-------------|
| `phase` | `string` | Current lifecycle state: `Pending`, `Running`, `Completed`, `Failed`. |
| `currentAssignee` | `string` | Node currently processing this Workitem. Empty when `Pending`. |
| `parentWorkitemID` | `string` | Workitem ID of the parent, if this is a child Workitem. Empty for root Workitems. Set at creation time and immutable. The Operator also applies a `flow.gideas.io/parent` label with this value for efficient querying. |
| `routingInstruction` | `RoutingInstruction` | Most recent routing outcome submitted by the assigned node. |
| `thrashCounters` | `map[string]integer` | Per-node visit counts. Hidden from nodes. The Thrash Guard triggers when the aggregate sum exceeds `governancePolicy.maxVisits`. |

### RoutingInstruction

| Field | Type | Description |
|-------|------|-------------|
| `type` | `string` | `route_to_output`, `route_to`, or `complete`. |
| `target` | `string` | Output name (for `route_to_output`) or node name (for `route_to`). Empty for `complete`. |

---

## GovernedArtefact

The GovernedArtefact CRD registers a governed artefact and declares its stamp vocabulary. The governed artefact is identified by `metadata.name` (e.g. `"petition-draft"`, `"haiku"`), which must be unique within the Flow namespace. Detail: [Data Model](../01-concepts/03-data-model.md#governed-artefacts).

### `spec`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `stamps` | `[]string` | no | Stamp vocabulary — the set of stamp names meaningful for this governed artefact (e.g. `["linter", "security-review", "approval"]`). Entry and exit contracts select required stamps from this vocabulary. |

---

## Law

The Law object is managed by the [Librarian](../02-flow/04-system-services.md#librarian). Tier 1 Findings are created by nodes with `WRITE:law/tier1` capability; Tier 2 Rulings are minted by the [Judiciary](../02-flow/03-nodes-external.md#the-judiciary--standard-subsystem) via the [Clerk](../02-flow/04-system-services.md#clerk) (with `WRITE:law/tier2`); Tier 3 Local Statutes are applied by the Flow Architect; Tiers 4-5 arrive from the Governance Flow and Federation. Detail: [Data Model](../01-concepts/03-data-model.md#laws), [Governance](../01-concepts/04-governance.md).

### `spec`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `goal` | `string` | yes | Plain-language statement of what the law enforces, stops, or ensures. The law's identity. |
| `representations` | `[]Representation` | yes | One or more typed expressions of the goal. At least one representation is required. |
| `tier` | `integer` | yes | Law tier: `1` (Finding), `2` (Ruling), `3` (Local Statute), `4` (State Constitution), `5` (Federal Accord). |
| `division` | `string` | no | Governance division this law belongs to (e.g. `"security"`, `"architecture"`, `"style"`). Empty means unset — consumers treat unset as `"general"`. Used to partition laws for division-aware review fan-out. Changing a law's division produces a new version. |
| `appliesTo` | `[]string` | no | Governed artefact names this law applies to. Empty means global — applies to all governed artefacts in the Flow. |

### Representation

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | `string` | yes | MIME type identifying the representation format (e.g. `text/markdown`, `application/smt-lib`, `application/python`, `application/rego`). |
| `content` | `string` | yes | The representation payload. |

### `status`

| Field | Type | Description |
|-------|------|-------------|
| `version` | `string` | Content hash of the current law version. Any mutation to `spec` produces a new hash. |

### Versioning

Any mutation to any part of the law — goal, representations, or metadata — produces a new version identified by content hash. Representations are not independently versioned. The Librarian preserves the full version history.

---

## Treaty

The Treaty CRD defines a directed trust policy for cross-flow collaboration between non-sibling Flows. Detail: [Cross-Flow](../02-flow/06-cross-flow.md), [Governance](../01-concepts/04-governance.md#treaties).

### `spec`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `remoteName` | `string` | yes | Identifier of the remote Flow. |
| `direction` | `string` | yes | `import` (this Flow receives from remote), `export` (this Flow sends to remote). Bidirectional exchange requires two Treaty CRDs. Note: a Treaty with `direction: import` in Flow B corresponds to "a Treaty from Flow A to Flow B" in cross-flow descriptions. |
| `caCert` | `string` | yes | PEM-encoded CA certificate of the remote Flow's trust root. Used for chain verification of imported stamps and packages. |
| `allowedSubjects` | `[]string` | no | Permitted identity subjects on imported certificates. If empty, all subjects under the CA are accepted. |
| `maxBundleSize` | `string` | no | Maximum size of export/import bundles. |

---

## FlowSupportService

The FlowSupportService CRD declares an optional, Flow-Architect-deployed service container. Detail: [System Services](../02-flow/04-system-services.md#flow-support-services).

### `spec`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `image` | `string` | yes | Container image for the Support Service. |
| `providesCapabilities` | `[]string` | yes | Capability names this service exposes (e.g. `["encode"]`). Nodes consume these via `USE:support/<service>/<capability>` grants on their [FoundryNode](#foundrynode) `capabilities` field. |
| `deploymentStrategy` | `string` | no | `ReplicaSet` (default) or `StatefulSet`. |
| `minReplicas` | `integer` | no | Minimum replica count. Default `0`, allowing scale-to-zero. Stateful services or services that cannot scale to zero override this. |
| `storage` | `StorageConfig` | no | Volume mounts and PVC declarations. |
| `resources` | `ResourceRequirements` | no | CPU and memory resource limits and requests. |

### `status`

| Field | Type | Description |
|-------|------|-------------|
| `phase` | `string` | `Initialising`, `Ready`, `Degraded`, `Stopped`. |
| `availableReplicas` | `integer` | Current number of ready replicas. |
| `conditions` | `[]Condition` | Standard Kubernetes conditions. |

Specialised CRDs (e.g. [CodificationService](#codificationservice)) share the same base deployment fields with subtype-specific additions.

---

## CodificationService

The CodificationService CRD declares a [Codification Service](../02-flow/04-system-services.md#codification-services) — a specialised Flow Support Service that translates law goals into formal representations. Each CodificationService instance produces exactly one representation type, declared via `outputFormat`.

The CodificationService shares the base deployment fields of FlowSupportService (image, deployment strategy, replicas, storage, resources). Its provided capability is always `encode` — the Operator enforces this implicitly; no `providesCapabilities` field is declared.

### `spec`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `image` | `string` | yes | Container image for the Codification Service. |
| `outputFormat` | `string` | yes | MIME type of the representation this service produces (e.g. `application/smt-lib`, `application/rego`, `application/python`). Exactly one output format per service instance. |
| `deploymentStrategy` | `string` | no | `ReplicaSet` (default) or `StatefulSet`. |
| `minReplicas` | `integer` | no | Minimum replica count. Default `0`, allowing scale-to-zero. |
| `storage` | `StorageConfig` | no | Volume mounts and PVC declarations. |
| `resources` | `ResourceRequirements` | no | CPU and memory resource limits and requests. |

### `status`

| Field | Type | Description |
|-------|------|-------------|
| `phase` | `string` | `Initialising`, `Ready`, `Degraded`, `Stopped`. |
| `availableReplicas` | `integer` | Current number of ready replicas. |
| `conditions` | `[]Condition` | Standard Kubernetes conditions. |

The Operator reconciles CodificationService CRDs identically to FlowSupportService for deployment lifecycle (pod provisioning, health management, scaling). The Operator internally manages the Judiciary's `USE:support/<name>/encode` capability for each registered CodificationService instance. Other nodes that need direct access to a Codification Service require an explicit `USE:support/<name>/encode` grant on their FoundryNode `capabilities`.

---

## Contract Shape

Entry and exit contracts share the same shape. A contract is a map of governed artefact name to required stamp names.

```yaml
# Example: exit contract requiring governed artefacts with specific stamps
approved:
  petition-draft:
    - linter
    - security-review
    - approval
  audit-log: []
```

| Structure | Validation |
|-----------|------------|
| `map[string][]string` | Each key is a governed artefact name (matching a GovernedArtefact's `metadata.name`). Each value is a list of required stamp names. |
| Name with stamp list | Artefacts of that governed artefact must be present, and each artefact's passport must carry all listed stamps on its current version. |
| Name with empty list `[]` | Artefacts of that governed artefact must be present. No stamps are required. |
| Empty map `{}` | No artefact requirements. |

If a Workitem contains multiple artefacts of a required governed artefact, all of them must satisfy that governed artefact's requirement.

Entry and exit contracts use the same evaluation semantics. The Operator validates the appropriate contract at the appropriate boundary — entry contracts at admission, exit contracts at completion.

---

## NodeGroup Shape

NodeGroups define sub-topology boundaries within a Flow. Each NodeGroup is a named entry in the FoundryFlow's `nodeGroups` map.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `nodes` | `[]string` | yes | FoundryNode names belonging to this group. Each node must exist as a FoundryNode in the namespace. A node can belong to at most one group. |
| `entryContracts` | `map[string]Contract` | no | Named entry contracts for the group boundary. Validated when a root Workitem is routed to an entry-bound node within the group from outside. Uses the same [Contract shape](#contract-shape). |
| `exitContracts` | `map[string]Contract` | no | Named exit contracts for the group boundary. Validated when a Workitem exits the group via an exit-bound node. Uses the same [Contract shape](#contract-shape). |

```yaml
# Example: codification NodeGroup
nodeGroups:
  codification:
    nodes:
      - codify-smt
      - codify-rego
      - codify-collector
    entryContracts:
      codify-input:
        codification-input: []
    exitContracts:
      codify-output:
        codification-output: []
```

---

## ChildWorkitemConfig

Optional child Workitem contract configuration on FoundryNode. These are developer-side validation aids for structured fan-out patterns. Detail: [Child Workitem Contracts](../02-flow/05-configuration.md#child-workitem-contracts).

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `entryContract` | `Contract` | no | Contract validated when a child Workitem is routed (via `RouteChild`). Ensures the child has been populated with expected artefacts before processing. Uses the same [Contract shape](#contract-shape). |
| `exitContract` | `Contract` | no | Contract validated when a child Workitem calls `Complete()`. Elevates the child's simple completion to contract-validated completion. Uses the same [Contract shape](#contract-shape). |

```yaml
# Example: FoundryNode with child Workitem contracts
spec:
  childWorkitems:
    entryContract:
      codification-input: []
    exitContract:
      codification-output: []
```

---

## Validation and Rejection Rules

The Operator rejects invalid configuration at admission time. Partial application never occurs.

| Rejection condition | Affected CRD | Error |
|---------------------|--------------|-------|
| Routing target references a node that does not exist | FoundryFlow, FoundryNode | `SCHEMA_VALIDATION_FAILED` |
| `importNode` references a nonexistent or non-entry-bound node | FoundryFlow | `IMPORT_NODE_INVALID` |
| Entry or exit binding references a contract name not defined on the FoundryFlow | FoundryNode | `UNKNOWN_CONTRACT` |
| Capability string uses invalid verb, missing qualifier, or unknown syntax | FoundryNode | `INVALID_CAPABILITY` |
| Exit binding present without a valid contract reference | FoundryNode | `SCHEMA_VALIDATION_FAILED` |
| Node timeout exceeds Flow-level `maxTimeout` | FoundryNode | `SCHEMA_VALIDATION_FAILED` |
| `maxTimeout` is less than `defaultTimeout` | FoundryFlow | `SCHEMA_VALIDATION_FAILED` |
| Stamp name in a contract is not declared in the GovernedArtefact's stamp vocabulary | FoundryFlow | `SCHEMA_VALIDATION_FAILED` |
| Duplicate artefact `id` with different `governed_artefact` for the same Workitem | Archivist | `ARTEFACT_KIND_CONFLICT` |
| NodeGroup references a node that does not exist as a FoundryNode | FoundryFlow | `SCHEMA_VALIDATION_FAILED` |
| A node appears in more than one NodeGroup | FoundryFlow | `SCHEMA_VALIDATION_FAILED` |
| NodeGroup contract stamp name not declared in GovernedArtefact stamp vocabulary | FoundryFlow | `SCHEMA_VALIDATION_FAILED` |
| Child Workitem contract stamp name not declared in GovernedArtefact stamp vocabulary | FoundryNode | `SCHEMA_VALIDATION_FAILED` |

---

## CRD Invariants

1. The Workitem CRD has no `spec` block. All state is Operator-managed in `status`.
2. Artefact identity (`id` unique within Workitem, `governed_artefact` immutable for a given `id`) is enforced by the Archivist, not the Workitem CRD.
3. The Operator is the sole mutator of Workitem `status`.
4. Entry and exit contracts share the same [Contract shape](#contract-shape) and evaluation semantics.
5. Capability enforcement is exact — verb, resource, governed artefact name, and stamp name must match the grant.
6. Laws are single objects; any `spec` mutation produces a new content-hash version.
7. Treaty trust is directed — a single CRD represents one direction of trust.
8. Invalid configuration is rejected at admission; partial application does not occur.
9. Stamp vocabulary on GovernedArtefact defines which stamp names are meaningful; contracts select from that vocabulary.
10. Child Workitem `parentWorkitemID` is immutable after creation. The `flow.gideas.io/parent` label is set by the Operator at creation time.
11. NodeGroups are inline on FoundryFlow. A node belongs to at most one group.
