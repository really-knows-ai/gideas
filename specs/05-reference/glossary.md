# Glossary

## Canonical Runtime Terms

### Arbiter

A Judiciary node that resolves deadlocked feedback disputes. Receives Workitems routed by Sort when feedback depth exceeds the configured threshold. Invokes the Jury for multi-agent deliberation, uses the Clerk to draft Tier 2 Rulings, resolves feedback with `linkedRuling`, and routes back to Sort. If the Jury hangs, routes to the Advocate for human escalation. Detail: [Foundry Cycle](../01-concepts/02-foundry-cycle.md#the-judiciary--standard-subsystem), [Nodes](../02-flow/03-nodes-external.md#the-judiciary--standard-subsystem).

### Archivist

The system service that manages artefact lifecycle data — version history, passport stamps, and feedback in an embedded relational database (SQLite in the reference implementation); raw content bytes in a content-addressed blob store. The single source of truth for all artefact provenance. Detail: [System Services](../02-flow/04-system-services.md#archivist).

### Advocate

The Judiciary's HITL (Human-in-the-Loop) node. Receives hung jury escalations from the Arbiter, Tier 3 proposals from the Tribunal for human ratification, and Tier 4-5 appeals. Uses the SDK [HITL pattern](../04-sdk/08-sdk-hitl.md) with `USE:queue/server` capability to expose a persistent queue for human decision. Detail: [Foundry Cycle](../01-concepts/02-foundry-cycle.md#the-judiciary--standard-subsystem), [SDK HITL](../04-sdk/08-sdk-hitl.md).

### Assay

**Superseded.** The former judicial node present in every Flow. Replaced by the [Judiciary](#judiciary) subsystem comprising the Arbiter, Tribunal, and Advocate nodes, and the Jury and Clerk core services. See [Judiciary](#judiciary).

### assignment

The binding of a single Workitem to a single node for processing. A Workitem has exactly one assignee at a time. The Sidecar establishes an assignment session and all SDK calls are automatically scoped to it. Detail: [Operator](../02-flow/01-operator.md), [SDK Core](../04-sdk/01-sdk-core.md).

### Clerk

A core service in the Judiciary subsystem. Handles law drafting and codification coordination — drafts prose representations, discovers and dispatches to Codification Services in parallel, assembles the final law, and calls WriteLaw on the Librarian. Used by the Arbiter and Tribunal. Detail: [System Services](../02-flow/04-system-services.md#clerk).

### Flow

A self-contained workflow runtime in a single Kubernetes namespace. One namespace, one Flow. All state, storage, governance, and execution live within the boundary. Detail: [Conceptual Overview](../01-concepts/00-overview.md).

### Flow Administrator

The human role accountable for runtime reliability, governance integrity, and recovery readiness of a running Flow. Flow Administrators monitor, triage, and recover Flows in production. Distinct from the [Flow Architect](#flow-architect) (who designs the Flow) and the [Operator](#operator-flow-operator) (the Kubernetes controller). Detail: [Operations](../02-flow/07-operations.md).

### Federated Queue Mesh

The horizontal scaling architecture for HITL nodes. Uses Headless Service DNS for peer discovery, scatter-gather for reads, proxy routing for writes, and shared-nothing SQLite persistence. No centralised database. Detail: [SDK HITL](../04-sdk/08-sdk-hitl.md#federated-queue-mesh).

### Flow Architect

The human role that designs and configures a Flow — defining topology, capability grants, contracts, stamp vocabulary, and policy limits through CRD configuration. The Flow Architect chooses which nodes exist, what they can do, and how work routes between them.

### Flow Monitor

The system service that ingests telemetry, friction events, metrics, traces, and audit records. Provides queryable aggregation of friction data across any axis (per-node, per-law, per-tier, per-topology-path). Detail: [System Services](../02-flow/04-system-services.md#flow-monitor-and-friction-surface).

### Flow Support Service

An optional, Flow-Architect-deployed container that exposes gRPC capabilities consumed by nodes (through Sidecar mediation) and by system services (through direct gRPC). Support Services run in the Flow namespace, do not process Workitems, and are declared via the [FlowSupportService CRD](./crds.md#flowsupportservice). Detail: [System Services](../02-flow/04-system-services.md#flow-support-services), [SDK Overview](../04-sdk/00-overview.md#flowsupportservice-base-class).

### FoundryAgent

The SDK's managed inference wrapper for LLM-backed nodes. Provides three behavioural guarantees: automatic heartbeat management during inference execution, schema-first output validation before artefact writes or routing decisions, and atomic per-step cost accounting via `foundry.cost.llm` telemetry events. FoundryAgent is the recommended pattern for all inference workloads and the runtime powering the [Jury](#jury) service's multi-agent deliberation mechanism. Detail: [SDK Agent](../04-sdk/07-sdk-agent.md).

### Librarian

The system service that manages the Flow's body of law (the Library). Stores law objects, serves law queries, runs integration conflict checks, triggers review hearings based on friction thresholds and review TTL expiry, and manages Librarian-to-Librarian replication for cross-flow law synchronisation. Detail: [System Services](../02-flow/04-system-services.md#librarian).

### node

A stateless worker that processes Workitems. Node pods persist for efficiency (model loading, connection pools), but execution state is rebuilt from the Workitem and Archivist each assignment. Nodes interact with runtime services exclusively through the Sidecar. Detail: [Node Overview](../03-node/00-overview.md).

### Operator (Flow Operator)

The Kubernetes controller that reconciles FoundryFlow and FoundryNode CRDs, assigns Workitems to nodes, validates routing outcomes, enforces entry and exit contracts, and manages the Workitem lifecycle state machine. The Operator is the sole authority for Workitem control-plane persistence. Detail: [Operator](../02-flow/01-operator.md).

### routing instruction

The outcome a node returns after processing: `route_to_output` (named output channel), `route_to` (specific node), or `complete` (exit completion). The Operator validates and persists the instruction. Detail: [SDK Core](../04-sdk/01-sdk-core.md), [Workitem](../02-flow/02-workitem.md).

### Sidecar

The Security Plane's presence in the Data Plane. An in-pod proxy that authenticates node requests, injects identity (`node_id`, `workitem_id`, `flow_id`), enforces assignment scoping, and brokers all communication between the node and runtime services. Nodes never call services directly. Detail: [Sidecar](../03-node/01-sidecar.md).

### topology

The directed graph of nodes and routing edges derived from FoundryNode output declarations. Determines how Workitems can move between nodes. Detail: [Configuration](../02-flow/05-configuration.md).

### Thrash Guard

The per-node visit counter map on each Workitem. Each assignment increments the assigned node's counter. When the aggregate sum exceeds `maxVisits`, the Operator fails the Workitem with `THRASH_BUDGET_EXCEEDED`. Detects infrastructure-level routing loops. Hidden from nodes. Detail: [Data Model](../01-concepts/03-data-model.md#thrash-guard).

### Tribunal

A Judiciary node that conducts review hearings on laws. Receives hearing Workitems from the Operator (triggered by the Librarian when friction thresholds or review TTLs are crossed). Invokes the Jury for deliberation, uses the Clerk to promote laws, or routes to the Advocate for Tier 3+ escalation. Detail: [Foundry Cycle](../01-concepts/02-foundry-cycle.md#the-judiciary--standard-subsystem), [Nodes](../02-flow/03-nodes-external.md#the-judiciary--standard-subsystem).

---

## Foundry Cycle Terms

### Appraise (reference arrangement)

The reviewer node. Evaluates artefacts against the Library's body of law and raises feedback. In the reference arrangement, Appraise holds `WRITE:law/tier1` capability and can record Tier 1 Findings. Detail: [Foundry Cycle](../01-concepts/02-foundry-cycle.md#appraise-reviewer).

### Forge (reference arrangement)

The creator node. Generates artefacts seeded by law context queried from the Library. Reads all law tiers for context seeding but does not write laws — it holds no `WRITE:law/tierN` grant in the reference arrangement. Detail: [Foundry Cycle](../01-concepts/02-foundry-cycle.md#forge-creator).

### Foundry Cycle

The reference arrangement — the standard pattern of node roles (Forge, Quench, Appraise, Sort, Refine) demonstrating adversarial creation, validation, review, and refinement. Flow Architects adapt it to their context. The platform enforces behaviour through capabilities and configuration, not node names. Detail: [Foundry Cycle](../01-concepts/02-foundry-cycle.md).

### Quench (reference arrangement)

The deterministic validator node. Runs repeatable, deterministic checks against artefacts (syntax validation, schema compliance, executable law representations). Detail: [Foundry Cycle](../01-concepts/02-foundry-cycle.md#quench-deterministic-validator).

### reference arrangement

The standard node topology (Forge, Quench, Appraise, Sort, Refine) provided by the Foundry Cycle. Distinguished from platform mechanisms, which are universal to every Flow regardless of topology. The Judiciary is not part of the reference arrangement — it is a standard runtime subsystem.

### Refine (reference arrangement)

The refiner node. Addresses feedback by modifying artefacts. Produces new artefact versions, driving the Workitem back through review. In the reference arrangement, Refine holds `WRITE:law/tier1` capability. Detail: [Foundry Cycle](../01-concepts/02-foundry-cycle.md#refine-refiner).

### Sort (reference arrangement)

The gate node. Evaluates Workitem state and routes: unresolved feedback to Refine, deadlocked items to the [Arbiter](#arbiter), missing stamps to the configured stamp provider (Appraise in the reference arrangement), and fully satisfied Workitems to exit completion. Sort is the only node that applies the "approval" stamp in the reference arrangement. Detail: [Foundry Cycle](../01-concepts/02-foundry-cycle.md#sort-gate).

---

## Data and Provenance Terms

### artefact

A governed output — a document, code file, data model, or anything a Flow produces. Versioned, content-addressed, and stored in the Archivist. The Workitem CRD carries no artefact references — the Archivist maintains artefact-to-Workitem associations by `workitem_id`. Version history, stamps, and feedback live in the Archivist. Detail: [Data Model](../01-concepts/03-data-model.md#artefacts).

### artefact kind

A classification string (e.g. `"haiku"`, `"python-source"`) identified by a GovernedArtefact CRD's `metadata.name`. The governed artefact name determines which stamp vocabulary applies, which laws are scoped to it (via `appliesTo`), and which contract requirements reference it.

### content hash

The SHA-256 hash of an artefact's raw content bytes. Serves as the version identifier. Identical content produces the same hash (deduplication); changed content produces a new hash (fresh governance). Stamps are bound to a specific content hash.

### feedback

Structured, threaded annotations on artefacts stored in the Archivist. Each item carries a severity, a lifecycle state, a message, and a history of every action taken. Feedback follows a forced-choice state machine: every disagreement must be justified, and every dispute has a resolution path. Detail: [Data Model](../01-concepts/03-data-model.md#feedback), [SDK Feedback](../04-sdk/04-sdk-feedback.md).

### feedback depth

The number of actions in a single feedback item's history. The gate node uses feedback depth to determine when to transition the item to `deadlocked` and route the Workitem to the [Arbiter](#arbiter).

### friction

A quantitative signal measuring governance cost. Purely additive — callers emit a magnitude and the Flow Monitor aggregates. Generated transparently by feedback (magnitude = depth), [Jury](#jury) deliberation rounds (magnitude = depth ^ (round + 1)), and HITL escalation (magnitude = depth ^ (rounds * 2)). Nodes may also emit friction voluntarily via `AddFriction`. Detail: [Conceptual Overview](../01-concepts/00-overview.md#friction), [Data Model](../01-concepts/03-data-model.md#friction).

### governed artefact

An artefact type registered via a GovernedArtefact CRD, identified by `metadata.name`. The CRD declares the stamp vocabulary — which stamp names are meaningful for that governed artefact. Entry and exit contracts select from this vocabulary. Detail: [Data Model](../01-concepts/03-data-model.md#governed-artefacts).

### HITL (Human-in-the-Loop)

Any point where the system pauses for a human decision. The SDK provides the [HITL pattern](../04-sdk/08-sdk-hitl.md) — managed infrastructure for queue persistence, REST API, and the Federated Queue Mesh. The Judiciary's [Advocate](#advocate) is the concrete HITL node for judicial escalation. User-defined HITL nodes compose the same SDK pattern with domain-specific logic.

### Judiciary

The umbrella term for the judicial subsystem comprising three nodes ([Arbiter](#arbiter), [Tribunal](#tribunal), [Advocate](#advocate)) and two core services ([Jury](#jury), [Clerk](#clerk)). Replaces the former "Assay" node. The Judiciary resolves deadlocked feedback disputes (Arbiter), conducts review hearings (Tribunal), escalates to humans (Advocate), deliberates via multi-agent voting (Jury), and drafts and codifies laws (Clerk). All components are Operator-provisioned runtime invariants. Detail: [Foundry Cycle](../01-concepts/02-foundry-cycle.md#the-judiciary--standard-subsystem), [Nodes](../02-flow/03-nodes-external.md#the-judiciary--standard-subsystem).

### Jury

A core service in the Judiciary subsystem. Multi-agent deliberation engine that runs parallel FoundryAgent instances as jurors, collects votes, applies consensus strategy (SimpleMajority, SuperMajority, Unanimity), and returns a structured verdict. Used by the Arbiter and Tribunal. Detail: [System Services](../02-flow/04-system-services.md#jury).

### passport

The collection of stamps on a specific artefact version. Tracks which governance checkpoints have been satisfied for that content hash. Stored in the Archivist's database, not on the Workitem CRD. Detail: [Data Model](../01-concepts/03-data-model.md#passports-and-stamps).

### QueueManager

The SDK interface for HITL queue operations. Provides `Enqueue`, `GetGlobalQueue`, `GetLocalQueue`, `Claim`, `Release`, `Complete`, and `GetPeers` methods. Available to nodes with the `USE:queue/server` capability. Handles local persistence, peer communication, and proxy routing transparently via the Federated Queue Mesh. Detail: [SDK HITL](../04-sdk/08-sdk-hitl.md#queuemanager-interface).

### stamp

A named governance checkpoint on an artefact's passport. Records the stamp name, the applying node, the content hash, and a cryptographic signature with certificate chain. Stamps are write-once per artefact version — a second application of the same stamp name to the same version is rejected. Detail: [Conceptual Overview](../01-concepts/00-overview.md#stamps), [Data Model](../01-concepts/03-data-model.md#passports-and-stamps).

### stamp vocabulary

The set of stamp names declared by a GovernedArtefact CRD as meaningful for that governed artefact (e.g. `["linter", "security-review", "approval"]`). Entry and exit contracts select required stamps from this vocabulary. The platform attaches no built-in semantics to any stamp name.

### version

A specific snapshot of an artefact's content, identified by its content hash. The Archivist maintains the full version history. When content changes, a new version is created; existing stamps remain with the old version and the new version starts with no stamps.

### Workitem

The unit of work. A Kubernetes CRD with no `spec` block — all mutable state lives in `status`, managed exclusively by the [Operator](../02-flow/01-operator.md). The Workitem carries lifecycle state, assignment ownership, routing instructions, and thrash counters. It does not reference artefacts — the [Archivist](../02-flow/04-system-services.md#archivist) maintains artefact-to-Workitem associations. Assigned to exactly one node at a time. Detail: [Data Model](../01-concepts/03-data-model.md#workitems), [Workitem Runtime](../02-flow/02-workitem.md).

---

## Governance and Legal Terms

### appeal

The mechanism by which the [Advocate](#advocate) escalates conflicts involving Tier 4 or Tier 5 laws to the Governance Flow via the Librarian. The Judiciary cannot directly modify laws above its judicial tier. Detail: [Governance](../01-concepts/04-governance.md#escalation-across-boundaries).

### `appliesTo`

A field on each law listing zero or more governed artefact names the law applies to (e.g. `["haiku"]`, `["haiku", "sonnet"]`). An empty list means the law is global — it applies to all governed artefacts in the Flow. Law conflict detection is scoped by `appliesTo`. Detail: [Data Model](../01-concepts/03-data-model.md#scoping), [CRD Reference](./crds.md#law), [SDK Legal](../04-sdk/03-sdk-legal.md).

### citation

Recording usage of a law during Workitem processing. `Cite` is syntactic sugar around `AddFriction` — each call emits a low-magnitude friction event attributed to the cited law. Detail: [SDK Legal](../04-sdk/03-sdk-legal.md#citation).

### codification

The process of translating a law's natural-language goal into a formal representation (formal logic, executable validator, policy-as-code) through a Codification Service. Detail: [System Services](../02-flow/04-system-services.md#codification-services).

### Codification Service

A specialised Flow Support Service for translating law goals into formal representations. Declared via the [CodificationService CRD](./crds.md#codificationservice), which specifies an `outputFormat` (MIME type) and whose `encode` capability is implicitly enforced by the Operator. Detail: [System Services](../02-flow/04-system-services.md#codification-services).

### contempt guard

The Archivist-enforced mechanism that prevents overriding judicially-linked rulings on feedback items. Once a `linkedRuling` is set, the losing side must accept the verdict — contradictory state transitions return `CONTEMPT_VIOLATION`. Detail: [Data Model](../01-concepts/03-data-model.md#contempt-guard).

### deadlock

The state a feedback item enters when the gate node determines its history depth warrants escalation. The gate node transitions the item to `deadlocked` and routes the Workitem to the [Arbiter](#arbiter) for judicial review. Distinct from the Thrash Guard, which detects infrastructure-level loops across the whole Workitem.

### Federal Accord (Tier 5)

A law synchronised from upstream Federal authorities. Applies across all Governance Flow instances in the network. The highest tier of law. Detail: [Data Model](../01-concepts/03-data-model.md#law-tiers).

### Finding (Tier 1)

An ephemeral law recorded by nodes during Workitem processing. Decays if uncited; can be promoted to a Tier 2 Ruling through a friction-threshold or review TTL hearing. Detail: [Data Model](../01-concepts/03-data-model.md#law-tiers).

### goal

A law's plain-language statement of what it enforces, stops, or ensures. The law's identity. All representations of a law must express the same goal.

### governance hardening

The process by which laws gain enforceability over time. A prose-only Tier 1 Finding can acquire formal logic representations when promoted to Tier 2 via Codification Services. Authority increases through tier promotion; enforceability increases through representation.

### grace period

A time-boxed exemption during Tier 3 law integration conflicts. The old Tier 3 law remains enforced while the incoming higher-tier law is queued. On expiry, the incoming law integrates automatically and the conflicting Tier 3 law is retired. Detail: [Governance](../01-concepts/04-governance.md#grace-period).

### hearing

See [review hearing](#review-hearing) below.

### law

A governance rule with a goal and one or more representations. Persisted as a [Law CRD](./crds.md#law) with a `spec` containing `goal`, `representations`, `tier`, and `appliesTo`, and a `status` containing the content-hash `version`. A single object (not a group of linked objects). Any mutation to any part produces a new version identified by content hash. Scoped to governed artefacts via `appliesTo`. Detail: [Data Model](../01-concepts/03-data-model.md#laws).

### law integration

The protocol by which higher-tier laws are integrated into a Flow's Library. A two-stage process: semantic search (vector similarity) followed by LLM conflict evaluation. Resolution depends on the tier of conflicting local laws. Detail: [Governance](../01-concepts/04-governance.md#law-integration-protocol), [Cross-Flow](../02-flow/06-cross-flow.md).

### Library

A Flow's collective body of law — every law the Flow has discovered, enacted, or inherited. Managed by the Librarian. Nodes query the Library for applicable laws through the SDK.

### linkedRuling

A field on a feedback item set by the [Arbiter](#arbiter) when rendering a verdict. References the Tier 2 Ruling that resolved the dispute. Once set, the contempt guard enforces finality.

### Local Statute (Tier 3)

A persistent law enacted by the Flow Architect. For standalone Flows, applied as a CRD by an administrator. Has no automatic decay. Detail: [Data Model](../01-concepts/03-data-model.md#law-tiers).

### representation

A specific expression of a law's goal — prose, formal logic, executable code, or any other format identified by MIME type. A law can carry multiple representations. Nodes query for representations they can interpret. Adding or removing a representation produces a new law version. Detail: [Data Model](../01-concepts/03-data-model.md#representations).

### review hearing

A judicial proceeding processed as a standard Workitem at the [Tribunal](#tribunal). Triggered by the Librarian when a law's accumulated friction crosses a configured threshold or when a law's age exceeds its tier's configured review TTL. Friction thresholds and review TTLs are configurable per law tier (`tier1` through `tier5`). The law remains active during the hearing. For Tiers 1-2, the Tribunal adjudicates directly with tier-specific verdicts: promote, retire, or demote. For Tiers 3-5, the hearing outcome is a petition to the Flow Architect or Governance Flow via the [Advocate](#advocate). Detail: [Governance](../01-concepts/04-governance.md#decay-and-retirement).

### Ruling (Tier 2)

Binding precedent minted by the [Judiciary](#judiciary) (via the [Clerk](#clerk)) when resolving disputes. Requires a formal review hearing before retirement. Detail: [Data Model](../01-concepts/03-data-model.md#law-tiers).

### State Constitution (Tier 4)

Organisational policy produced by the Governance Flow through the standard Foundry Cycle with HITL ratification. Applies to all Sibling Flows under the Governance Flow. Detail: [Data Model](../01-concepts/03-data-model.md#law-tiers).

### supremacy

The principle that higher-tier law always wins. Absolute — no upward override. A Tier 3 Local Statute cannot override a Tier 4 State Constitution, regardless of creation time.

### tier

A law's level in the five-tier jurisdictional hierarchy. Tier 1 (Finding), Tier 2 (Ruling), Tier 3 (Local Statute), Tier 4 (State Constitution), Tier 5 (Federal Accord). Higher tier carries greater authority. Detail: [Data Model](../01-concepts/03-data-model.md#law-tiers).

### TTL (Review TTL)

Time-to-live. A per-tier expiry window configured on the FoundryFlow's governance policy. When a law's age exceeds its tier's configured review TTL, the Librarian triggers a review hearing. The law remains active during the hearing.

### verdict

The outcome of a review hearing rendered by the [Tribunal](#tribunal). Tier-specific: promote, retire (Tier 1), or demote (Tier 2). Hearings produce a decisive outcome.

---

## Federation and Cross-Flow Terms

### cross-flow stamp authority

The rules governing whether imported stamps satisfy local stamp requirements. Topology-dependent: sibling stamps are authoritative after shared-root chain verification; treaty stamps are provenance-only until naturalisation. Detail: [Cross-Flow](../02-flow/06-cross-flow.md).

### Governance Flow

A dedicated, pre-configured Flow whose governed artefacts are laws. Produces Tier 4 State Constitution laws, synchronises Tier 5 Federal Accords, and serves as the State Root Certificate Authority. Uses the same runtime and CRDs as any other Flow. Detail: [Governance](../01-concepts/04-governance.md#the-governance-flow).

### naturalisation

The process by which imported artefacts and stamps gain local governance standing in a receiving Flow. At treaty boundaries, foreign stamps are preserved for audit but do not satisfy local requirements until the receiving Flow naturalises them. Detail: [Cross-Flow](../02-flow/06-cross-flow.md).

### Sibling Flow

A Flow that shares a Governance Flow (and therefore a State Root CA) with other Flows. Sibling Flows share implicit trust through the common root — imported stamps are authoritative after chain verification when stamp names match. Sibling Flows do not require treaties.

### State Root

The self-signed Root CA keypair held by the Governance Flow. Issues intermediate CA certificates to each Sibling Flow's Operator, establishing a shared trust hierarchy. Detail: [Governance](../01-concepts/04-governance.md#state-root-certificate-authority).

### treaty

A directed trust policy enabling collaboration between Flows that do not share a Governance Flow. Declared via a [Treaty CRD](./crds.md#treaty) — each CRD represents one direction of trust (import or export). Two-way exchange requires two Treaty CRDs. Detail: [Governance](../01-concepts/04-governance.md#treaties), [Cross-Flow](../02-flow/06-cross-flow.md).

---

## Capability and Contract Terms

### capability

A permission granted to a node through the FoundryNode CRD's `capabilities` field. Determines what operations the node is authorised to perform. Enforced by the owning runtime service, not by the SDK or the node. Detail: [Configuration](../02-flow/05-configuration.md), [Nodes](../02-flow/03-nodes-external.md).

### capability syntax

The structured grammar for capability grants: `VERB:RESOURCE[/QUALIFIER]`. Verbs: `READ`, `WRITE`, `STAMP`, `USE`. Examples: `READ:law`, `WRITE:artefact`, `WRITE:feedback/deadlocked`, `STAMP:artefact/haiku/linter`, `USE:support/codify-smt/encode`. Detail: [Configuration](../02-flow/05-configuration.md), [Node Configuration](../03-node/02-configuration.md).

### entry binding

A FoundryNode CRD field (`entry`) that references a named entry contract on the FoundryFlow. Nodes with entry bindings serve as admission points: local Workitem creation, cross-flow import (via `importNode`), and review-hearing intake. Detail: [Configuration](../02-flow/05-configuration.md).

### entry contract

A named set of governed-artefact requirements that a Workitem must satisfy for admission. Defined on the FoundryFlow CRD (`entryContracts`). Enforced at local creation, cross-flow import, and review-hearing intake. Uses the same shape as exit contracts. Detail: [Data Model](../01-concepts/03-data-model.md#entry-and-exit-contracts), [Configuration](../02-flow/05-configuration.md).

### exit binding

A FoundryNode CRD field (`exit`) that references a named exit contract on the FoundryFlow. Only nodes with exit bindings can call `complete()`. The binding is fixed in configuration — the node does not choose which contract to validate. Detail: [Configuration](../02-flow/05-configuration.md).

### exit contract

A named set of governed-artefact requirements that a Workitem must satisfy for completion. Defined on the FoundryFlow CRD (`exitContracts`). Enforced by the Operator when an exit node calls `complete()`. When completion triggers cross-flow export, only governed artefacts listed in the contract are exported. Detail: [Data Model](../01-concepts/03-data-model.md#entry-and-exit-contracts), [Configuration](../02-flow/05-configuration.md).

### import node

The node designated in the FoundryFlow CRD (`importNode`) as the entry point for cross-flow imported Workitems. Must reference a FoundryNode bound to an entry contract. Imported Workitems are created in `Pending` and first-scheduled to this node when capacity allows. Detail: [Configuration](../02-flow/05-configuration.md), [Cross-Flow](../02-flow/06-cross-flow.md).

---

## Superseded Terms

These legacy terms are explicitly out of scope in v1. They must not appear in spec documents except in this superseded-term listing.

| Superseded Term | Replacement | Notes |
|-----------------|-------------|-------|
| `Assay` | Judiciary (Arbiter, Tribunal, Advocate, Jury, Clerk) | Single judicial node decomposed into three nodes and two core services. |
| `WorkitemType` | Entry/exit contracts | Flow admission is not type-gated. |
| `spec.type` | Entry/exit contracts | No Workitem type discriminator exists. |
| `spec.context` / `status.context` | Governed artefacts | No freeform context bag. All work context is represented by explicit Workitem state and governed artefacts. |
| `entryNode` | `importNode` + entry bindings | Import entry is `importNode`; local admission uses entry-bound nodes. |
| `terminalContract` / `terminalContracts` | `exitContracts` + exit bindings | Exit contracts are named on the FoundryFlow; nodes bind to them via `exit`. |
| node `terminal` binding | `exit` binding | Nodes are exit-bound via the `exit` field, not a `terminal` flag. |
| Law Groups (`group` field) | Single-object multi-representation law | A law is one object with a goal and multiple representations, not a group of linked CRDs. |
| `ReviewHearing` CRD | Standard Workitems at the Tribunal | Hearings use standard Workitems with explicit artefacts and contract bindings. |
| Reserved underscore context keys | Governed artefacts | No reserved key namespace for bag-style metadata. |

---

## Glossary Invariants

1. Every term defined here has exactly one canonical definition.
2. Superseded terms must not appear in normative spec prose outside this glossary.
3. Term definitions must remain consistent with [AGENTS.md key decisions](../AGENTS.md) — when a glossary definition and a key decision conflict, the key decision governs.
4. Cross-reference links point to the first normative detail location for each term.
5. British spelling is used for all spec prose (`artefact`, `naturalisation`, `organisation`, `behaviour`). US spelling is reserved for literal external identifiers.
