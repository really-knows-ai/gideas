# Glossary

## Canonical Runtime Terms

### Arbiter

A Judiciary orchestration node that resolves deadlocked feedback disputes. Receives child Workitems from the [Facilitator](#facilitator) containing an evidence bundle, fans out to [Juror](#juror) nodes, and tallies verdicts internally. On consensus it either resolves the dispute within existing law or creates a [Clerk cycle](#clerk-cycle) child to draft the required [petition](#petition); on hung outcomes it routes to a [HITL node](#hitl-node). Detail: [Foundry Cycle](../01-concepts/02-foundry-cycle.md#arbiter-path-deadlock-resolution), [Nodes](../02-flow/03-nodes-external.md#the-judiciary--standard-subsystem).

### Archivist

The system service that manages artefact lifecycle data — version history, passport stamps, and feedback in an embedded relational database (SQLite in the reference implementation); raw content bytes in a content-addressed blob store. The single source of truth for all artefact provenance. Detail: [System Services](../02-flow/04-system-services.md#archivist).

### Assay

**Superseded.** The former judicial node present in every Flow. Replaced by the [Judiciary](#judiciary) subsystem comprising orchestration nodes (Arbiter, Tribunal), deliberation nodes ([Juror](#juror)), watcher nodes ([Friction Watcher](#friction-watcher), [TTL Watcher](#ttl-watcher), [petition-outcome-watcher](#petition-outcome-watcher)), and legislative inner cycle nodes ([Clerk cycle](#clerk-cycle), Codification nodes, [Rule Router](#rule-router), [law-applicator](#law-applicator), [HITL node](#hitl-node), [Facilitator](#facilitator)). Cross-flow transfer is handled separately by the [Embassy](#embassy). See [Judiciary](#judiciary).

### assignment

The binding of a single Workitem to a single node for processing. A Workitem has exactly one assignee at a time. The Sidecar establishes an assignment session and all SDK calls are automatically scoped to it. Detail: [Operator](../02-flow/01-operator.md), [SDK Core](../04-sdk/01-sdk-core.md).

### Clerk cycle

The petition drafting and approval cycle in the Judiciary's legislative inner cycle. It is composed of ordinary node instances (`clerk-forge`, `codification`, `clerk-sort`, `clerk-refine`, `clerk-appraise`, `clerk-done-router`, `hitl-appraise`, `hitl-gate`, and [law-applicator](#law-applicator)), not a standalone Clerk service or node. The cycle drafts and revises [petition](#petition) artefacts, fans out to Codification nodes for formal representations, reviews feedback, and then either applies approved T1-3 changes locally or exports approved T4-5 petitions through the [Embassy](#embassy). Detail: [Foundry Cycle](../01-concepts/02-foundry-cycle.md#clerk-cycle), [Nodes](../02-flow/03-nodes-external.md#the-judiciary--standard-subsystem).

### crossFlow.importTypes

A map on the FoundryFlow CRD's `spec.crossFlow` that defines the flow-authored/custom import type extension set for cross-flow Workitem reception. Each key is an import type name; each value specifies a target node (must be entry-bound) and optional per-artefact foreign-stamp requirements. Replaces the former `importNode` field. Built-in system import types such as `law-petition` live in the same effective namespace but are not authored in this map. Detail: [CRDs](./crds.md#cross-flow-configuration).

### Embassy

The operator-provisioned cross-flow boundary node present in every Flow. It handles manifest preflight, package streaming, inbound Workitem materialisation, effective import-type routing (built-in system plus flow-authored), Treaty enforcement, and naturalisation of verified foreign stamps into local `imported-*` attestations. Embassy transfers Workitems such as `law-petition`; it does not distribute published laws, which is a [Federation](#federation) service responsibility. Detail: [Cross-Flow](../02-flow/06-cross-flow.md#embassy), [gRPC API](./grpc-api.md#embassy-api).

### Facilitator

A Judiciary lifecycle node for deadlock resolution. It assembles an evidence bundle, creates a child Workitem for the [Arbiter](#arbiter), suspends while the child runs, then resumes and routes the result back into the parent Flow. Detail: [Foundry Cycle](../01-concepts/02-foundry-cycle.md#arbiter-path-deadlock-resolution), [Nodes](../02-flow/03-nodes-external.md#the-judiciary--standard-subsystem).

### Federation

The control-plane authority that manages inter-Flow membership, trust-root discovery, state groupings, authority publisher roles, petition-routing policy, and published-law distribution. A Federation replaces the former Governance Flow runtime concept: member Flows remain ordinary Flows, while the Federation service governs how T4-T5 authority relationships and publication work across them. Detail: [Governance](../01-concepts/04-governance.md#federation-membership), [Federation](../02-flow/08-federation.md), [gRPC API](./grpc-api.md#federation-api).

### Flow

A self-contained workflow runtime in a single Kubernetes namespace. One namespace, one Flow. All state, storage, governance, and execution live within the boundary. Detail: [Conceptual Overview](../01-concepts/00-overview.md).

### Flow Administrator

The human role accountable for runtime reliability, governance integrity, and recovery readiness of a running Flow. Flow Administrators monitor, triage, and recover Flows in production. Distinct from the [Flow Architect](#flow-architect) (who designs the Flow) and the [Operator](#operator-flow-operator) (the Kubernetes controller). Detail: [Operations](../02-flow/07-operations.md).

### Federated Queue Mesh

The horizontal scaling architecture for HITL nodes. Uses Headless Service DNS for peer discovery, scatter-gather for reads, proxy routing for writes, and shared-nothing SQLite persistence. No centralised database. Detail: [SDK HITL](../04-sdk/08-sdk-hitl.md#federated-queue-mesh).

### Flow Architect

The human role that designs and configures a Flow — defining topology, capability grants, contracts, stamp vocabulary, and policy limits through CRD configuration. The Flow Architect chooses which nodes exist, what they can do, and how work routes between them.

### Flow Monitor

A stateless pipeline adapter that subscribes to the Flow Event Bus's telemetry and audit channels and exports signals to external observability systems: metrics via a `/metrics` endpoint for Prometheus scraping, and audit events as JSON Lines to stdout for log pipeline consumption. The Flow Monitor does not persist events, aggregate friction, or serve query APIs — friction aggregation is owned by the [Friction Ledger](#friction-ledger). Detail: [System Services](../02-flow/04-system-services.md#flow-monitor).

### Friction Ledger

The system service that subscribes to friction events on the Flow Event Bus, maintains running friction aggregates (per-law, per-node, per-tier, per-topology-path) in SQLite, evaluates hearing thresholds, and publishes threshold-crossing signals to the friction channel. Serves `QueryFriction` as a direct gRPC API for point-to-point friction queries. Detail: [System Services](../02-flow/04-system-services.md#friction-ledger).

### Friction Watcher

A Judiciary watcher node. Entry-bound, long-lived process that subscribes to the [Flow Event Bus](../02-flow/04-system-services.md#flow-event-bus) friction channel (via Sidecar) for `friction.threshold_crossed` events. When a law's accumulated friction crosses its tier's configured threshold, creates a hearing Workitem via `CreateWorkitem`, stores a `law-reference` artefact containing the law ID, and routes to the [Tribunal](#tribunal) via its `default` output. Tracks pending hearing law IDs to prevent duplicate hearing creation. Detail: [Foundry Cycle](../01-concepts/02-foundry-cycle.md#friction-watcher), [Nodes](../02-flow/03-nodes-external.md#the-judiciary--standard-subsystem).

### Flow Event Bus

The durable event distribution service in the Control Plane. Receives events from producers, persists them to a SQLite append-only log, and fans them out to all active subscribers across three channels: telemetry, audit, and friction. Detail: [System Services](../02-flow/04-system-services.md#flow-event-bus).

### Flow Support Service

An optional, Flow-Architect-deployed container that exposes gRPC capabilities consumed by nodes (through Sidecar mediation) and by system services (through direct gRPC). Support Services run in the Flow namespace, do not process Workitems, and are declared via the [FlowSupportService CRD](./crds.md#flowsupportservice). Detail: [System Services](../02-flow/04-system-services.md#flow-support-services), [SDK Overview](../04-sdk/00-overview.md#flowsupportservice-base-class).

### FoundryAgent

The SDK's managed inference wrapper for LLM-backed nodes. Provides three behavioural guarantees: automatic heartbeat management during inference execution, schema-first output validation before artefact writes or routing decisions, and atomic per-step cost accounting via `foundry.cost.llm` telemetry events. FoundryAgent is the recommended pattern for all inference workloads, including [Juror nodes](../01-concepts/02-foundry-cycle.md#juror-judicial-agent) in the Judiciary's deliberation topology. Detail: [SDK Agent](../04-sdk/07-sdk-agent.md).

### Librarian

The system service that manages the Flow's body of law (the Library). A pure law store and lifecycle service — stores law objects, serves law queries, runs integration conflict checks for Federation-published laws, manages dispute records, and maintains law version history. Hearing triggers are owned by dedicated watcher nodes ([Friction Watcher](#friction-watcher) and [TTL Watcher](#ttl-watcher)), not the Librarian. Detail: [System Services](../02-flow/04-system-services.md#librarian).

### node

A stateless worker that processes Workitems. Node pods persist for efficiency (model loading, connection pools), but execution state is rebuilt from the Workitem and Archivist each assignment. Nodes interact with runtime services exclusively through the Sidecar. Detail: [Node Overview](../03-node/00-overview.md).

### Operator (Flow Operator)

The Kubernetes controller that reconciles FoundryFlow and FoundryNode CRDs, assigns Workitems to nodes, validates routing outcomes, enforces entry and exit contracts, and manages the Workitem lifecycle state machine. The Operator is the sole authority for Workitem control-plane persistence. Detail: [Operator](../02-flow/01-operator.md).

### Rule Router

A generic CEL-based routing node. It reads Workitem state, evaluates ordered rules, and routes to the first matching output without mutating state. In the judiciary it is used for tier-based Clerk-cycle routing such as `clerk-done-router` and `hitl-gate`. Detail: [Node Patterns](../03-node/03-patterns.md), [Configuration](../02-flow/05-configuration.md#routing-semantics).

### routing instruction

The outcome a node returns after processing: `route_to_output` (named output channel), `route_to` (specific node), or `complete` (exit completion). The Operator validates and persists the instruction. Detail: [SDK Core](../04-sdk/01-sdk-core.md), [Workitem](../02-flow/02-workitem.md).

### Sidecar

The Security Plane's presence in the Data Plane. An in-pod proxy that authenticates node requests, injects identity (`node_id`, `workitem_id`, `namespace`), enforces assignment scoping, and brokers all communication between the node and runtime services. Nodes never call services directly. Detail: [Sidecar](../03-node/01-sidecar.md).

### topology

The directed graph of nodes and routing edges derived from FoundryNode output declarations. Determines how Workitems can move between nodes. Detail: [Configuration](../02-flow/05-configuration.md).

### Thrash Guard

The per-node visit counter map on each Workitem. Each assignment increments the assigned node's counter. When the aggregate sum exceeds `maxVisits`, the Operator fails the Workitem with `THRASH_BUDGET_EXCEEDED`. Detects infrastructure-level routing loops. Hidden from nodes. Detail: [Data Model](../01-concepts/03-data-model.md#thrash-guard).

### Tribunal

A Judiciary orchestration node for review hearings. It receives hearing Workitems created by the [Friction Watcher](#friction-watcher) or [TTL Watcher](#ttl-watcher), assembles law evidence, fans out to [Juror](#juror) nodes, tallies votes internally, and on consensus creates a [Clerk cycle](#clerk-cycle) child Workitem carrying the court's `verdict-context`. The hearing Workitem then completes; the downstream Clerk cycle continues independently. Detail: [Foundry Cycle](../01-concepts/02-foundry-cycle.md#hearing-path), [Nodes](../02-flow/03-nodes-external.md#the-judiciary--standard-subsystem).

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

A quantitative signal measuring governance cost. Purely additive — callers emit a magnitude and the Friction Ledger aggregates. Generated transparently by feedback (magnitude = depth), [Juror](#juror) deliberation rounds (magnitude = depth ^ (round + 1)), and HITL escalation (magnitude = depth ^ (rounds * 2)). Nodes may also emit friction voluntarily via `AddFriction`. Detail: [Conceptual Overview](../01-concepts/00-overview.md#friction), [Data Model](../01-concepts/03-data-model.md#friction).

### governed artefact

An artefact type registered via a GovernedArtefact CRD, identified by `metadata.name`. The CRD declares the stamp vocabulary — which stamp names are meaningful for that governed artefact. Entry and exit contracts select from this vocabulary. Detail: [Data Model](../01-concepts/03-data-model.md#governed-artefacts).

### HITL (Human-in-the-Loop)

Any point where the system pauses for a human decision. The SDK provides the [HITL pattern](../04-sdk/08-sdk-hitl.md) — managed infrastructure for queue persistence, REST API, and the Federated Queue Mesh. The [HITL node](#hitl-node) is the concrete generic config-driven implementation for judicial escalation. User-defined HITL nodes compose the same SDK pattern with domain-specific logic.

### HITL node

A generic config-driven Human-in-the-Loop node. Single image, multiple CRD instances. It is used for hung-jury resolution and for human approval in the Clerk cycle's Tier 3-5 petition path. Uses the SDK [HITL pattern](../04-sdk/08-sdk-hitl.md) with `USE:queue/server` capability. Replaces the old Advocate-specific human boundary with a reusable node pattern. Detail: [SDK HITL](../04-sdk/08-sdk-hitl.md).

### Judiciary

The umbrella term for the judicial subsystem. It comprises orchestration nodes ([Arbiter](#arbiter), [Tribunal](#tribunal)), deliberation nodes ([Juror](#juror)), watcher nodes ([Friction Watcher](#friction-watcher), [TTL Watcher](#ttl-watcher), [petition-outcome-watcher](#petition-outcome-watcher)), and legislative inner-cycle nodes ([Clerk cycle](#clerk-cycle), Codification nodes, [Rule Router](#rule-router), [law-applicator](#law-applicator), [HITL node](#hitl-node), [Facilitator](#facilitator)). The Judiciary resolves deadlocked feedback, conducts review hearings, drafts petitions, and routes approved work by tier; the separate [Embassy](#embassy) boundary node handles T4-T5 `law-petition` transfer. Detail: [Foundry Cycle](../01-concepts/02-foundry-cycle.md#the-judiciary--standard-subsystem), [Nodes](../02-flow/03-nodes-external.md#the-judiciary--standard-subsystem).

### authority publisher

A federation-assigned role that grants an ordinary Flow permission to publish approved local Tier 3 laws outward. State-level authority publishers produce Tier 4 materialisations in subscriber Flows; federation-level authority publishers produce Tier 5 materialisations. Detail: [Governance](../01-concepts/04-governance.md#authority-publisher-roles), [Federation](../02-flow/08-federation.md).

### law-applicator

An action node in the Judiciary's legislative inner cycle. For approved T1-3 petitions it writes or retires laws through the Librarian. For approved T4-5 petitions it creates a [dispute record](../01-concepts/03-data-model.md#dispute-records) linking the `petition_id` to cited law IDs, then routes the Workitem to the [Embassy](#embassy) for `law-petition` export to the appropriate authority Flow. Replaces the former Judiciary Gate. Detail: [Foundry Cycle](../01-concepts/02-foundry-cycle.md#clerk-cycle), [Nodes](../02-flow/03-nodes-external.md#the-judiciary--standard-subsystem).

### law-petition

The built-in system import type for higher-authority petition submission. It exists in the same effective import-type namespace as flow-authored `crossFlow.importTypes`, but it is always present/configured per Flow by the platform rather than authored in YAML. A Flow uses it when the [law-applicator](#law-applicator) routes an approved T4-5 petition to its [Embassy](#embassy), which exports the petition to the authority Flow selected by federation policy or allowed by Treaty policy. It is not used for published-law distribution.

### Juror

A Judiciary deliberation node. Single image with configurable judicial philosophy — loads different agent configurations at fan-out time to maximise diversity. Receives child Workitems with question, evidence, and prior-round reasoning (if retry). Runs a [FoundryAgent](#foundryagent) with the loaded judicial personality and produces a structured verdict artefact (outcome + reasoning). Used by both Arbiter and Tribunal fan-out. Detail: [Foundry Cycle](../01-concepts/02-foundry-cycle.md#juror-judicial-agent), [Nodes](../02-flow/03-nodes-external.md#the-judiciary--standard-subsystem).

### passport

The collection of stamps on a specific artefact version. Tracks which governance checkpoints have been satisfied for that content hash. Stored in the Archivist's database, not on the Workitem CRD. Detail: [Data Model](../01-concepts/03-data-model.md#passports-and-stamps).

### petition-outcome-watcher

A watcher node that monitors Federation publication and rejection outcomes for exported `law-petition`s. It retires dispute records, resumes Workitems held in `pending-hold`, and creates follow-up Clerk-cycle Workitems when an authority rejects a petition. Detail: [Federation](../02-flow/08-federation.md#petition-outcome-watcher), [Nodes](../02-flow/03-nodes-external.md#the-judiciary--standard-subsystem).

### Petition

A structured YAML/Markdown [GovernedArtefact](./crds.md#governedartefact) containing a complete proposed law change set. Drafted by the [Clerk cycle](#clerk-cycle), reviewed within that cycle, and identified by a stable `petition_id` used for cross-flow correlation. A petition carries context plus one or more changes (`create`, `update`, `retire`, `demote`), and non-retire changes may include formal representations produced by Codification nodes. Approved petitions are either applied locally by the [law-applicator](#law-applicator) or exported through the [Embassy](#embassy) as `law-petition`s. Detail: [Foundry Cycle](../01-concepts/02-foundry-cycle.md#petition-artefact), [Data Model](../01-concepts/03-data-model.md#laws).

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

The higher-authority escalation path for T4-T5 conflicts. The local [law-applicator](#law-applicator) creates a dispute record and routes the approved petition to the [Embassy](#embassy), which exports it as a `law-petition` to the authority Flow selected by federation policy. The local Flow does not directly modify external-authority laws. Detail: [Governance](../01-concepts/04-governance.md#higher-authority-escalation).

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

### dispute record

A Library entity representing an active T4-T5 `law-petition` whose authority outcome is still pending. It links a `petition_id` to the cited law IDs under dispute and is retired when the petition outcome is known. Sort uses active dispute records to route affected Workitems to `pending-hold` instead of re-deadlocking them. Detail: [Data Model](../01-concepts/03-data-model.md#dispute-records), [Federation](../02-flow/08-federation.md#petition-outcome-watcher).

### Federal Accord (Tier 5)

A law published by a federation-level authority Flow and distributed by the [Federation](#federation) service to subscriber Flows, where it materialises as Tier 5. The highest authority tier in a member Flow. Detail: [Data Model](../01-concepts/03-data-model.md#law-tiers).

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

A judicial proceeding processed as a standard Workitem at the [Tribunal](#tribunal). Triggered by the [Friction Watcher](#friction-watcher) when a law's accumulated friction crosses a configured threshold, or by the [TTL Watcher](#ttl-watcher) when a law's age exceeds its tier's configured review TTL. The Tribunal assembles evidence, fans out to [Juror](#juror) nodes, tallies internally, and on consensus creates a child Workitem for the [Clerk cycle](#clerk-cycle). That Clerk cycle later applies local changes or exports a `law-petition` through the [Embassy](#embassy), depending on tier. Detail: [Governance](../01-concepts/04-governance.md#decay-and-retirement).

### Ruling (Tier 2)

Binding precedent created within a single Flow by the [Judiciary](#judiciary) from an approved Tier 2 [petition](#petition). The [Clerk cycle](#clerk-cycle) drafts the petition and the [law-applicator](#law-applicator) applies it via the Librarian. Requires a formal review hearing before retirement. Detail: [Data Model](../01-concepts/03-data-model.md#law-tiers).

### State Constitution (Tier 4)

A law published by a state-level authority Flow and distributed by the [Federation](#federation) service to subscriber Flows in the same state, where it materialises as Tier 4. Detail: [Data Model](../01-concepts/03-data-model.md#law-tiers).

### supremacy

The principle that higher-tier law always wins. Absolute — no upward override. A Tier 3 Local Statute cannot override a Tier 4 State Constitution, regardless of creation time.

### tier

A law's level in the five-tier jurisdictional hierarchy. Tier 1 (Finding), Tier 2 (Ruling), Tier 3 (Local Statute), Tier 4 (State Constitution), Tier 5 (Federal Accord). Higher tier carries greater authority. Detail: [Data Model](../01-concepts/03-data-model.md#law-tiers).

### TTL (Review TTL)

Time-to-live. A Tier 1-2 expiry window configured on the FoundryFlow's governance policy. When a Tier 1 Finding or Tier 2 Ruling exceeds its configured review TTL, the [TTL Watcher](#ttl-watcher) node triggers a review hearing. The law remains active during the hearing.

### TTL Watcher

A Judiciary watcher node. Entry-bound, long-lived process that periodically polls the [Librarian](#librarian) via `QueryLaws` for laws whose age exceeds their tier's configured review TTL. On expiry, creates a hearing Workitem via `CreateWorkitem`, stores a `law-reference` artefact containing the law ID, and routes to the [Tribunal](#tribunal) via its `default` output. Tracks pending hearing law IDs to prevent duplicate hearing creation. Per-tier TTL durations are configured via node config. Detail: [Foundry Cycle](../01-concepts/02-foundry-cycle.md#ttl-watcher), [Nodes](../02-flow/03-nodes-external.md#the-judiciary--standard-subsystem).

### verdict

The reasoned decision produced by the [Arbiter](#arbiter) or [Tribunal](#tribunal) after [Juror](#juror) fan-out and internal tally. Verdicts are stored as artefacts and drive the next step in the judicial process: feedback resolution, Clerk-cycle petition drafting, or HITL resolution on hung outcomes.

---

## Federation and Cross-Flow Terms

### cross-flow stamp authority

The rules governing whether imported stamps satisfy local stamp requirements. Foreign stamps never satisfy local contracts directly. The [Embassy](#embassy) verifies required foreign stamps against the federation trust root or Treaty-pinned CA, then emits local `imported-*` attestations that local contracts may recognise. Detail: [Cross-Flow](../02-flow/06-cross-flow.md).

### naturalisation

The process by which imported artefacts and foreign stamps gain local governance standing in a receiving Flow. After verifying the required foreign stamps, the [Embassy](#embassy) applies local `imported-*` attestation stamps. Foreign stamps remain for provenance and audit, while local contracts evaluate the attested local stamps. Detail: [Cross-Flow](../02-flow/06-cross-flow.md).

### Sibling Flow

A Flow that shares membership in at least one federation-defined state with another Flow. Sibling relationships derive from shared state membership and federation policy, not from a dedicated Governance Flow runtime. Federation-member exchange between sibling Flows uses the federation trust root; Treaties are only needed for non-federation exchange.

### State Root

**Superseded.** Replaced by the federation trust root. In the current model, the [Federation](#federation) service holds the root CA and issues intermediate CA certificates to member Flows.

### treaty

A directed trust policy enabling collaboration between Flows that do not share federation membership. Declared via a [Treaty CRD](./crds.md#treaty), each Treaty represents one direction of trust and may constrain which import types the remote Flow may use. Two-way exchange requires two Treaty CRDs. Detail: [Governance](../01-concepts/04-governance.md#higher-authority-escalation), [Cross-Flow](../02-flow/06-cross-flow.md).

---

## Capability and Contract Terms

### capability

A permission granted to a node through the FoundryNode CRD's `capabilities` field. Determines what operations the node is authorised to perform. Enforced by the owning runtime service, not by the SDK or the node. Detail: [Configuration](../02-flow/05-configuration.md), [Nodes](../02-flow/03-nodes-external.md).

### capability syntax

The structured grammar for capability grants: `VERB:RESOURCE[/QUALIFIER]`. Verbs: `READ`, `WRITE`, `STAMP`, `USE`. Examples: `READ:law`, `WRITE:artefact`, `WRITE:feedback/deadlocked`, `STAMP:artefact/haiku/linter`, `USE:support/codify-smt/encode`. Detail: [Configuration](../02-flow/05-configuration.md), [Node Configuration](../03-node/02-configuration.md).

### entry binding

A FoundryNode CRD field (`entry`) that references a named entry contract on the FoundryFlow. Nodes with entry bindings serve as admission points for local Workitem creation, cross-flow import via `crossFlow.importTypes`, and review-hearing intake. Detail: [Configuration](../02-flow/05-configuration.md).

### entry contract

A named set of governed-artefact requirements that a Workitem must satisfy for admission. Defined on the FoundryFlow CRD (`entryContracts`). Enforced at local creation, cross-flow import, and review-hearing intake. Uses the same shape as exit contracts. Detail: [Data Model](../01-concepts/03-data-model.md#entry-and-exit-contracts), [Configuration](../02-flow/05-configuration.md).

### exit binding

A FoundryNode CRD field (`exit`) that references a named exit contract on the FoundryFlow. Only nodes with exit bindings can call `complete()`. The binding is fixed in configuration — the node does not choose which contract to validate. Detail: [Configuration](../02-flow/05-configuration.md).

### exit contract

A named set of governed-artefact requirements that a Workitem must satisfy for completion. Defined on the FoundryFlow CRD (`exitContracts`). Enforced by the Operator when an exit node calls `complete()`. When the Embassy performs cross-flow export, only governed artefacts listed in its bound exit contract are exported. Detail: [Data Model](../01-concepts/03-data-model.md#entry-and-exit-contracts), [Configuration](../02-flow/05-configuration.md).

### import node

**Superseded.** The former single-node cross-flow intake field on the FoundryFlow CRD. Replaced by `crossFlow.importTypes`, which maps each published import type to an entry-bound target node and optional foreign-stamp requirements.

---

## Superseded Terms

These legacy terms are explicitly out of scope in v1. They must not appear in spec documents except in this superseded-term listing.

| Superseded Term | Replacement | Notes |
|-----------------|-------------|-------|
| `Assay` | Judiciary (Arbiter, Tribunal, Embassy, Juror, Facilitator, Clerk cycle, Codification nodes, Rule Router, law-applicator, HITL node, Friction Watcher, TTL Watcher) | Single judicial node decomposed into orchestration, deliberation, watcher, and legislative inner-cycle nodes. |
| `Jury` (service) | Juror nodes + orchestrator-internal tally | Monolithic deliberation service replaced by Juror fan-out with Arbiter/Tribunal tallying verdicts internally. |
| `Clerk` (service) | Clerk cycle + Codification nodes + law-applicator | Monolithic law drafting service replaced by node-based petition drafting, codification, review, and application. |
| `Deliberate()` RPC | Juror fan-out via child Workitems | gRPC deliberation call replaced by externalised Workitem transitions. |
| `DraftLaw()` RPC | Clerk-cycle petition drafting via Workitems | gRPC law drafting call replaced by node-based Clerk-cycle execution. |
| `CreateHearingWorkitem` RPC | Friction Watcher / TTL Watcher nodes using generic `CreateWorkitem` | Judiciary-specific Operator RPC replaced by entry-bound watcher nodes. |
| `WorkitemType` | Entry/exit contracts | Flow admission is not type-gated. |
| `spec.type` | Entry/exit contracts | No Workitem type discriminator exists. |
| `spec.context` / `status.context` | Governed artefacts | No freeform context bag. All work context is represented by explicit Workitem state and governed artefacts. |
| `entryNode` | `crossFlow.importTypes` + entry bindings | Import entry is published per import type; local admission uses entry-bound nodes. |
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
