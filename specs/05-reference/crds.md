# CRD Reference

All custom resources use API group `flow.gideas.io/v1` and are namespace-scoped.

## Resource Inventory

| CRD | Owner | Purpose |
|-----|-------|---------|
| [FoundryFlow](#foundryflow) | Flow Architect / Operator | Flow-wide contracts, governance policy, cross-flow settings |
| [FoundryNode](#foundrynode) | Flow Architect / Operator | Node-local behaviour, capabilities, routing outputs, contract bindings |
| [Workitem](#workitem) | Operator (sole mutator) | Workitem lifecycle state, assignment, routing |
| [GovernedArtefact](#governedartefact) | Flow Architect | Governed artefact registration and stamp vocabulary |
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
| `entryContracts` | `map[string]Contract` | yes | Named entry contracts. Each contract is a [Contract shape](#contract-shape). At least one entry contract must be defined. |
| `exitContracts` | `map[string]Contract` | yes | Named exit contracts. Each contract is a [Contract shape](#contract-shape). |
| `importNode` | `string` | no | Name of the FoundryNode that receives cross-flow imported Workitems. Must reference an existing entry-bound node. If absent, cross-flow import is disabled. |
| `governancePolicy` | `GovernancePolicy` | yes | Governance thresholds and timers. See [governance policy](#governance-policy). |
| `crossFlow` | `CrossFlowConfig` | no | Cross-flow trust and naturalisation settings. See [cross-flow configuration](#cross-flow-configuration). |

### Assay

Assay is a runtime-mandated component — the Operator provisions it without requiring a separate FoundryNode CRD. Its hearing entry and exit contracts are fixed by the runtime: the entry contract requires a single `law-reference` artefact; the exit contract requires it to still be present. Its capabilities are fixed by the runtime (not configurable by the Flow Architect) and include `WRITE:law/tier2`, `READ:law`, `WRITE:friction`, feedback resolution, stamp application for hearing artefacts, and access to all registered [CodificationService](#codificationservice) instances (the Operator internally manages Assay's `USE:support/<name>/encode` capability for each).

The Operator also provisions a `law-reference` GovernedArtefact alongside Assay. Its stamp vocabulary is empty. The `law-reference` artefact's content is a plain-text string containing the target law ID.

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

When a law's accumulated friction crosses its tier's configured threshold, the Librarian triggers a review hearing. For Tiers 1-2, Assay adjudicates directly. For Tiers 3-5, the hearing outcome is a petition to the Flow Architect or Governance Flow.

### ReviewTTLs

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `tier1` | `duration` | no | Time-to-live for Tier 1 laws (Findings). |
| `tier2` | `duration` | no | Time-to-live for Tier 2 laws (Rulings). |
| `tier3` | `duration` | no | Time-to-live for Tier 3 laws (Local Statutes). |
| `tier4` | `duration` | no | Time-to-live for Tier 4 laws (State Constitutions). |
| `tier5` | `duration` | no | Time-to-live for Tier 5 laws (Federal Accords). |

When a law's age exceeds its tier's configured TTL, the Librarian triggers a review hearing. The law remains active during the hearing. Like friction thresholds, Tiers 1-2 hearings are adjudicated by Assay; Tiers 3-5 produce petitions.

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

The Law object is managed by the [Librarian](../02-flow/04-system-services.md#librarian). Tier 1 Findings are created by nodes with `WRITE:law/tier1` capability; Tier 2 Rulings are minted by Assay (with `WRITE:law/tier2`); Tier 3 Local Statutes are applied by the Flow Architect; Tiers 4-5 arrive from the Governance Flow and Federation. Detail: [Data Model](../01-concepts/03-data-model.md#laws), [Governance](../01-concepts/04-governance.md).

### `spec`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `goal` | `string` | yes | Plain-language statement of what the law enforces, stops, or ensures. The law's identity. |
| `representations` | `[]Representation` | yes | One or more typed expressions of the goal. At least one representation is required. |
| `tier` | `integer` | yes | Law tier: `1` (Finding), `2` (Ruling), `3` (Local Statute), `4` (State Constitution), `5` (Federal Accord). |
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

The Operator reconciles CodificationService CRDs identically to FlowSupportService for deployment lifecycle (pod provisioning, health management, scaling). The Operator internally manages Assay's `USE:support/<name>/encode` capability for each registered CodificationService instance. Other nodes that need direct access to a Codification Service require an explicit `USE:support/<name>/encode` grant on their FoundryNode `capabilities`.

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
