# CRD Reference

All custom resources use API group `flow.gideas.io/v1` and are namespace-scoped.

## Resource Inventory

| CRD | Owner | Purpose |
|-----|-------|---------|
| [FoundryFlow](#foundryflow) | Flow Architect / Operator | Flow-wide topology, contracts, governance policy, cross-flow settings |
| [FoundryNode](#foundrynode) | Flow Architect / Operator | Node-local behaviour, capabilities, routing outputs, contract bindings |
| [Workitem](#workitem) | Operator (sole mutator) | Workitem lifecycle state, assignment, artefact references |
| [GovernedArtefact](#governedartefact) | Flow Architect | Artefact kind registration and stamp vocabulary |
| [Law](#law) | Librarian / Assay / nodes | Law goal, representations, tier, lifecycle metadata |
| [Treaty](#treaty) | Flow Architect | Directed cross-flow trust policy |
| [FlowSupportService](#flowsupportservice) | Flow Architect / Operator | Support Service capability declaration and infrastructure |
| [CodificationService](#codificationservice) | Flow Architect / Operator | Codification Service: output format declaration and deployment |

---

## FoundryFlow

The FoundryFlow CRD defines the executable shape of a Flow. The [Operator](../02-flow/01-operator.md) reconciles this resource as the source of truth for all flow-wide behavioural semantics. Detail: [Configuration Semantics](../02-flow/05-configuration.md).

### `spec`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `topology` | `[]TopologyEdge` | yes | Directed graph of routable node connections. Each edge names a source node and target node. Every referenced node must have a corresponding FoundryNode CRD. |
| `entryContracts` | `map[string]Contract` | yes | Named entry contracts. Each contract is a [Contract shape](#contract-shape). At least one entry contract must be defined. |
| `exitContracts` | `map[string]Contract` | yes | Named exit contracts. Each contract is a [Contract shape](#contract-shape). |
| `importNode` | `string` | no | Name of the FoundryNode that receives cross-flow imported Workitems. Must reference an existing entry-bound node. If absent, cross-flow import is disabled. |
| `assay` | `AssayConfig` | yes | Assay hearing configuration. See [Assay configuration](#assay-configuration). |
| `governancePolicy` | `GovernancePolicy` | yes | Governance thresholds and timers. See [governance policy](#governance-policy). |
| `crossFlow` | `CrossFlowConfig` | no | Cross-flow trust and naturalisation settings. See [cross-flow configuration](#cross-flow-configuration). |
| `supportServiceGrants` | `map[string][]string` | no | Maps node names to lists of `USE:support/<service>/<capability>` grants. Supplements per-node capability grants with Flow-level Support Service access control. |

### TopologyEdge

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `source` | `string` | yes | Source node name. |
| `target` | `string` | yes | Target node name. |

### Assay Configuration

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `hearingEntryContract` | `string` | yes | Name of the entry contract bound to Assay for hearing admission. Must reference a key in `entryContracts`. |
| `hearingExitContract` | `string` | yes | Name of the exit contract bound to Assay for hearing completion. Must reference a key in `exitContracts`. |

Assay is a runtime-mandated component — the Operator provisions it from the `AssayConfig` without requiring a separate FoundryNode CRD. Its entry and exit bindings are derived from `hearingEntryContract` and `hearingExitContract`. Its capabilities are fixed by the runtime (not configurable by the Flow Architect) and include `WRITE:law/tier2`, `READ:law`, friction queries, feedback resolution, stamp application for hearing artefacts, and access to all registered [CodificationService](#codificationservice) instances (the Operator automatically grants `USE:support/<name>/encode` for each).

The Operator also provisions a `law-reference` GovernedArtefact kind alongside Assay. Its stamp vocabulary is empty. The `law-reference` artefact's content is a plain-text string containing the target law ID. The hearing entry and exit contracts reference this kind with no stamp requirements.

### Governance Policy

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `maxVisits` | `integer` | yes | Thrash Guard budget. When the aggregate visit count across all nodes exceeds this value, the Workitem fails with `THRASH_BUDGET_EXCEEDED`. |
| `defaultTimeout` | `duration` | yes | Default inactivity timeout for node assignments. Used as the fallback when no node-specific timeout is set in FoundryNode. |
| `maxTimeout` | `duration` | yes | Maximum inactivity timeout for node assignments. No node-specific timeout can exceed this value. Must be >= `defaultTimeout`. |
| `maxFeedbackDepth` | `integer` | yes | Feedback deadlock threshold. When a single feedback item's history depth exceeds this value, the gate node transitions it to `deadlocked`. |
| `frictionThresholds` | `FrictionThresholds` | no | Per-tier friction thresholds that trigger review hearings. |
| `retentionPolicy` | `RetentionPolicy` | no | Retention duration for terminal Workitems before garbage collection. |

### FrictionThresholds

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `tier1ReviewHearing` | `float` | no | Accumulated friction on a Tier 1 Finding that triggers a review hearing. |
| `tier2ReviewHearing` | `float` | no | Accumulated friction on a Tier 2 Ruling that triggers a review hearing. |

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
| `storage` | `StorageConfig` | no | Volume mounts and deployment strategy. Presence of persistent volumes triggers StatefulSet deployment; otherwise ReplicaSet (default). |

### Output

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | `string` | yes | Output channel name. Referenced by `route_to_output` instructions. |
| `target` | `string` | yes | Target node name. Must exist in the Flow topology. |

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
| `WRITE:artefact` | Write artefact content (all kinds). |
| `WRITE:artefact/<kind>` | Write artefact content scoped to a specific kind. |
| `READ:law` | Query laws from the Library. |
| `WRITE:law/tier1` | Write Tier 1 laws (Findings). |
| `WRITE:law/tier2` | Write Tier 2 and below (Rulings, Findings). |
| `WRITE:law/tier3` | Write Tier 3 and below (Local Statutes, Rulings, Findings). |
| `WRITE:law/tier4` | Write Tier 4 and below (State Constitutions and all lower tiers). |
| `WRITE:law/tier5` | Write Tier 5 and below (all tiers). |
| `STAMP:artefact/<kind>/<stamp-name>` | Apply a named stamp to a specific artefact kind. Exact match on both kind and stamp name. |
| `READ:flow` | Query Flow topology and configuration. Enables stamp-to-node mapping discovery. |
| `READ:workitem` | Read Workitem state beyond the current assignment. |
| `READ:feedback` | Read feedback items on artefacts. |
| `WRITE:feedback/new` | Create feedback items (`AddFeedback`). |
| `WRITE:feedback/actioned` | Transition feedback to `actioned` (`ResolveFeedback`). |
| `WRITE:feedback/wont_fix` | Transition feedback to `wont_fix` (`RefuseFeedback`). |
| `WRITE:feedback/rejected` | Transition feedback to `rejected` (`RejectFix`, `RejectRefusal`). |
| `WRITE:feedback/resolved` | Transition feedback to `resolved` (`AcceptFix`, `AcceptRefusal`). |
| `WRITE:feedback/deadlocked` | Transition feedback to `deadlocked` (`DeadlockFeedback`). |
| `USE:support/<service>/<capability>` | Invoke a specific Flow Support Service capability. |

Some operations (such as `ListArtefacts` — listing artefact references on the assigned Workitem) are implicitly available to all nodes by virtue of the assignment scope and do not require explicit capability grants.

Malformed capability strings are rejected at configuration admission. The Operator does not reconcile a FoundryNode with syntactically invalid capabilities.

---

## Workitem

The Workitem CRD carries control-plane state for a unit of work. The [Operator](../02-flow/01-operator.md) is the sole mutator. Nodes interact through [SDK abstractions](../04-sdk/05-sdk-workitems.md), not CRD field paths. Detail: [Workitem Runtime](../02-flow/02-workitem.md).

### `spec`

Immutable after creation.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `intent` | `string` | yes | Human-readable statement of the Workitem's purpose. |
| `priority` | `string` | yes | `low`, `medium`, `high`, or `critical`. Influences scheduling order. |
| `artefacts` | `[]ArtefactRef` | no | Initial artefact references at creation time. The running artefact list is maintained in `status.artefacts`. |

### ArtefactRef

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `id` | `string` | yes | Unique within the Workitem. Fixed once introduced. |
| `kind` | `string` | yes | Governed artefact kind. Immutable for a given `id`. |

### `status`

Managed by the Operator. Nodes do not write to `status` directly.

| Field | Type | Description |
|-------|------|-------------|
| `phase` | `string` | Current lifecycle state: `Pending`, `Running`, `Completed`, `Failed`. |
| `currentAssignee` | `string` | Node currently processing this Workitem. Empty when `Pending`. |
| `previousAssignee` | `string` | Node that last processed this Workitem. |
| `routingInstruction` | `RoutingInstruction` | Most recent routing outcome submitted by the assigned node. |
| `artefacts` | `[]ArtefactRef` | Current artefact reference list. Initialised from `spec.artefacts` at creation. Refs can be added during processing; existing `id`/`kind` pairs are immutable. |
| `thrashCounters` | `map[string]integer` | Per-node visit counts. Hidden from nodes. The Thrash Guard triggers when the aggregate sum exceeds `governancePolicy.maxVisits`. |
| `history` | `[]HistoryEntry` | Chronological record of assignments and transitions. Append-only. |

### RoutingInstruction

| Field | Type | Description |
|-------|------|-------------|
| `type` | `string` | `route_to_output`, `route_to`, or `complete`. |
| `target` | `string` | Output name (for `route_to_output`) or node name (for `route_to`). Empty for `complete`. |

### Absent by Design

These fields do not exist on the Workitem CRD:

| Absent Field | Rationale |
|--------------|-----------|
| `spec.type` / `WorkitemType` reference | Flow admission is contract-bound, not type-gated. |
| `spec.context` / `status.context` | No freeform context bag. Work context is governed artefacts. |
| Feedback | Feedback lives in the [Archivist](../02-flow/04-system-services.md#archivist), scoped to artefact `id`. |
| Stamps / passport | Stamps live in the [Archivist](../02-flow/04-system-services.md#archivist), scoped to artefact `id` and version hash. |
| Version history | Version history lives in the [Archivist](../02-flow/04-system-services.md#archivist). |

---

## GovernedArtefact

The GovernedArtefact CRD registers an artefact kind and declares its stamp vocabulary. Detail: [Data Model](../01-concepts/03-data-model.md#governed-artefacts).

### `spec`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `kind` | `string` | yes | Artefact kind identifier (e.g. `"petition-draft"`, `"haiku"`). Unique within the Flow namespace. |
| `stamps` | `[]string` | no | Stamp vocabulary — the set of stamp names meaningful for this kind (e.g. `["linter", "security-review", "approval"]`). Entry and exit contracts select required stamps from this vocabulary. |
| `retention` | `RetentionPolicy` | no | Version retention policy for artefacts of this kind. |

### RetentionPolicy

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `maxVersions` | `integer` | no | Maximum number of versions to retain per artefact. |
| `maxAge` | `duration` | no | Maximum age of retained versions. |

---

## Law

The Law object is managed by the [Librarian](../02-flow/04-system-services.md#librarian). Tier 1 Findings are created by nodes with `WRITE:law/tier1` capability; Tier 2 Rulings are minted by Assay (with `WRITE:law/tier2`); Tier 3 Local Statutes are applied by the Flow Architect; Tiers 4-5 arrive from the Governance Flow and Federation. Detail: [Data Model](../01-concepts/03-data-model.md#laws), [Governance](../01-concepts/04-governance.md).

### `spec`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `goal` | `string` | yes | Plain-language statement of what the law enforces, stops, or ensures. The law's identity. |
| `representations` | `[]Representation` | yes | One or more typed expressions of the goal. At least one representation is required. |
| `tier` | `integer` | yes | Law tier: `1` (Finding), `2` (Ruling), `3` (Local Statute), `4` (State Constitution), `5` (Federal Accord). |
| `appliesTo` | `[]string` | no | Governed artefact kinds this law applies to. Empty means global — applies to all kinds in the Flow. |
| `ttl` | `duration` | no | Time-to-live. Applicable to Tier 1 and Tier 2 laws. When a law's TTL expires, the Librarian triggers a review hearing. The law remains active during the hearing. Tier 3+ laws have no automatic decay. |

### Representation

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | `string` | yes | MIME type identifying the representation format (e.g. `text/markdown`, `application/smt-lib`, `application/python`, `application/rego`). |
| `content` | `string` | yes | The representation payload. |

### `status`

| Field | Type | Description |
|-------|------|-------------|
| `version` | `string` | Content hash of the current law version. Any mutation to `spec` produces a new hash. |
| `citationCount` | `integer` | Number of times this law has been cited. |
| `frictionAccumulated` | `float` | Total friction attributed to this law. |
| `lastHearing` | `datetime` | Timestamp of the most recent review hearing for this law. |
| `linkedRulings` | `[]string` | IDs of Tier 2 Rulings that superseded or consolidated this law. |

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
| `capabilities` | `[]string` | yes | List of capability names this service exposes (e.g. `["encode"]`). These are "provided capabilities" (what the service offers), not "granted capabilities" (the verb-resource permission grammar used in [FoundryNode](#foundrynode)). |
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

The CodificationService shares the base deployment fields of FlowSupportService (image, deployment strategy, replicas, storage, resources). Its capability is always `encode` — the Operator enforces this implicitly; no `capabilities` field is declared.

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

The Operator reconciles CodificationService CRDs identically to FlowSupportService for deployment lifecycle (pod provisioning, health management, scaling). The Operator automatically grants Assay `USE:support/<name>/encode` for each registered CodificationService instance — no manual `supportServiceGrants` entry is needed for Assay. Other nodes that need direct access to a Codification Service require explicit grants via `supportServiceGrants` on the FoundryFlow.

---

## Contract Shape

Entry and exit contracts share the same shape. A contract is a map of artefact kind to required stamp names.

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
| `map[string][]string` | Each key is a governed artefact kind. Each value is a list of required stamp names. |
| Kind with stamp list | Artefacts of that kind must be present, and each artefact's passport must carry all listed stamps on its current version. |
| Kind with empty list `[]` | Artefacts of that kind must be present. No stamps are required. |
| Empty map `{}` | No artefact requirements. |

If a Workitem contains multiple artefacts of a required kind, all of them must satisfy that kind's requirement.

Entry and exit contracts use the same evaluation semantics. The Operator validates the appropriate contract at the appropriate boundary — entry contracts at admission, exit contracts at completion.

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
| Assay hearing contract references (`hearingEntryContract`, `hearingExitContract`) reference undefined contract names | FoundryFlow | `SCHEMA_VALIDATION_FAILED` |
| Duplicate artefact `id` with different `kind` in a Workitem's artefact list | Workitem | `ARTEFACT_KIND_CONFLICT` |

---

## CRD Invariants

1. `spec` fields on Workitems are immutable after creation.
2. Artefact references use `id` (unique within Workitem) and `kind` (immutable for a given `id`).
3. The Operator is the sole mutator of Workitem `status`.
4. Entry and exit contracts share the same [Contract shape](#contract-shape) and evaluation semantics.
5. Capability enforcement is exact — verb, resource, kind, and stamp name must match the grant.
6. Laws are single objects; any `spec` mutation produces a new content-hash version.
7. Treaty trust is directed — a single CRD represents one direction of trust.
8. No CRD field path reintroduces `WorkitemType`, `spec.type`, `spec.context`, `status.context`, `entryNode`, `terminalContract`, or `terminal` bindings.
9. Invalid configuration is rejected at admission; partial application does not occur.
10. Stamp vocabulary on GovernedArtefact defines which stamp names are meaningful; contracts select from that vocabulary.
