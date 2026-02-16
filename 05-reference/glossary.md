# Glossary

## Canonical Runtime Terms

**Archivist**
: The system service that manages artefact lifecycle data — version history, passport stamps, and feedback in an embedded relational database (SQLite in the reference implementation); raw content bytes in a content-addressed blob store. The single source of truth for all artefact provenance. Detail: [System Services](../02-flow/04-system-services.md#archivist).

**Assay**
: The judicial node present in every Flow as a standard runtime component. Assay holds `WRITE:law/tier2` and resolves deadlocked feedback disputes by minting Tier 2 Rulings. It adjudicates review hearings triggered by friction thresholds or TTL expiry. Its authority ceiling is constitutionally bounded: resolve at Tier 2, propose at Tier 3, appeal at Tier 4-5. Assay does not write Tier 1 Findings by convention — its role is judicial, not observational. Detail: [Foundry Cycle](../01-concepts/02-foundry-cycle.md#assay-judiciary--standard-component), [Governance](../01-concepts/04-governance.md#assays-authority-ceiling).

**assignment**
: The binding of a single Workitem to a single node for processing. A Workitem has exactly one assignee at a time. The Sidecar establishes an assignment session and all SDK calls are automatically scoped to it. Detail: [Operator](../02-flow/01-operator.md), [SDK Core](../04-sdk/01-sdk-core.md).

**Flow**
: A self-contained workflow runtime in a single Kubernetes namespace. One namespace, one Flow. All state, storage, governance, and execution live within the boundary. Detail: [Conceptual Overview](../01-concepts/00-overview.md).

**Flow Architect**
: The human operator who designs and configures a Flow — defining topology, capability grants, contracts, stamp vocabulary, and policy limits through CRD configuration. The Flow Architect chooses which nodes exist, what they can do, and how work routes between them.

**Flow Monitor**
: The system service that ingests telemetry, friction events, metrics, traces, and audit records. Provides queryable aggregation of friction data across any axis (per-node, per-law, per-tier, per-topology-path). Detail: [System Services](../02-flow/04-system-services.md#flow-monitor-and-friction-surface).

**Flow Support Service**
: An optional, Flow-Architect-deployed container that exposes gRPC capabilities consumed by nodes (through Sidecar mediation) and by system services (through direct gRPC). Support Services run in the Flow namespace, do not process Workitems, and are declared via their own CRD. Detail: [System Services](../02-flow/04-system-services.md#flow-support-services), [SDK Overview](../04-sdk/00-overview.md#flowsupportservice-base-class).

**Librarian**
: The system service that manages the Flow's body of law (the Library). Stores law objects, serves law queries, runs integration conflict checks, triggers review hearings based on friction thresholds and TTL expiry, and manages Librarian-to-Librarian replication for cross-flow law synchronisation. Detail: [System Services](../02-flow/04-system-services.md#librarian).

**node**
: A stateless worker that processes Workitems. Node pods persist for efficiency (model loading, connection pools), but execution state is rebuilt from the Workitem and Archivist each assignment. Nodes interact with runtime services exclusively through the Sidecar. Detail: [Node Overview](../03-node/00-overview.md).

**Operator** (Flow Operator)
: The Kubernetes controller that reconciles FoundryFlow and FoundryNode CRDs, assigns Workitems to nodes, validates routing outcomes, enforces entry and exit contracts, and manages the Workitem lifecycle state machine. The Operator is the sole authority for Workitem control-plane persistence. Detail: [Operator](../02-flow/01-operator.md).

**routing instruction**
: The outcome a node returns after processing: `route_to_output` (named output channel), `route_to` (specific node), or `complete` (exit completion). The Operator validates and persists the instruction. Detail: [SDK Core](../04-sdk/01-sdk-core.md), [Workitem](../02-flow/02-workitem.md).

**Sidecar**
: The Security Plane's presence in the Data Plane. An in-pod proxy that authenticates node requests, injects identity (`node_id`, `workitem_id`, `flow_id`), enforces assignment scoping, and brokers all communication between the node and runtime services. Nodes never call services directly. Detail: [Sidecar](../03-node/01-sidecar.md).

**topology**
: The directed graph of nodes and routing edges defined in the FoundryFlow CRD. Determines how Workitems can move between nodes. Detail: [Configuration](../02-flow/05-configuration.md).

**Thrash Guard**
: The per-node visit counter map on each Workitem. Each assignment increments the assigned node's counter. When the aggregate sum exceeds `maxVisits`, the Operator fails the Workitem with `THRASH_BUDGET_EXCEEDED`. Detects infrastructure-level routing loops. Hidden from nodes. Detail: [Data Model](../01-concepts/03-data-model.md#thrash-guard).

---

## Foundry Cycle Terms

**Appraise** (reference arrangement)
: The reviewer node. Evaluates artefacts against the Library's body of law and raises feedback. In the reference arrangement, Appraise holds `WRITE:law/tier1` capability and can record Tier 1 Findings. Detail: [Foundry Cycle](../01-concepts/02-foundry-cycle.md#appraise-reviewer).

**Forge** (reference arrangement)
: The creator node. Generates artefacts seeded by law context queried from the Library. Reads all law tiers for context seeding but does not write laws — it holds no `WRITE:law/tierN` grant in the reference arrangement. Detail: [Foundry Cycle](../01-concepts/02-foundry-cycle.md#forge-creator).

**Foundry Cycle**
: The reference arrangement — the standard pattern of node roles (Forge, Quench, Appraise, Sort, Refine) demonstrating adversarial creation, validation, review, and refinement. Flow Architects adapt it to their context. The platform enforces behaviour through capabilities and configuration, not node names. Detail: [Foundry Cycle](../01-concepts/02-foundry-cycle.md).

**Quench** (reference arrangement)
: The deterministic validator node. Runs repeatable, deterministic checks against artefacts (syntax validation, schema compliance, executable law representations). Detail: [Foundry Cycle](../01-concepts/02-foundry-cycle.md#quench-deterministic-validator).

**reference arrangement**
: The standard node topology (Forge, Quench, Appraise, Sort, Refine) provided by the Foundry Cycle. Distinguished from platform mechanisms, which are universal to every Flow regardless of topology. Assay is not part of the reference arrangement — it is a standard runtime component.

**Refine** (reference arrangement)
: The refiner node. Addresses feedback by modifying artefacts. Produces new artefact versions, driving the Workitem back through review. In the reference arrangement, Refine holds `WRITE:law/tier1` capability. Detail: [Foundry Cycle](../01-concepts/02-foundry-cycle.md#refine-refiner).

**Sort** (reference arrangement)
: The gate node. Evaluates Workitem state and routes: unresolved feedback to Refine, deadlocked items to Assay, missing stamps to the configured stamp provider (Appraise in the reference arrangement), and fully satisfied Workitems to exit completion. Sort is the only node that applies the "approval" stamp in the reference arrangement. Detail: [Foundry Cycle](../01-concepts/02-foundry-cycle.md#sort-gate).

---

## Data and Provenance Terms

**artefact**
: A governed output — a document, code file, data model, or anything a Flow produces. Versioned, content-addressed, and stored in the Archivist. The Workitem carries only a reference (`id` and `kind`); version history, stamps, and feedback live in the Archivist. Detail: [Data Model](../01-concepts/03-data-model.md#artefacts).

**artefact kind**
: A classification string (e.g. `"haiku"`, `"python-source"`) declared by a GovernedArtefact CRD. Artefact kind determines which stamp vocabulary applies, which laws are scoped to it (via `appliesTo`), and which contract requirements reference it.

**content hash**
: The SHA-256 hash of an artefact's raw content bytes. Serves as the version identifier. Identical content produces the same hash (deduplication); changed content produces a new hash (fresh governance). Stamps are bound to a specific content hash.

**feedback**
: Structured, threaded annotations on artefacts stored in the Archivist. Each item carries a severity, a lifecycle state, a message, and a history of every action taken. Feedback follows a forced-choice state machine: every disagreement must be justified, and every dispute has a resolution path. Detail: [Data Model](../01-concepts/03-data-model.md#feedback), [SDK Feedback](../04-sdk/04-sdk-feedback.md).

**feedback depth**
: The number of actions in a single feedback item's history. When depth exceeds `maxFeedbackDepth`, the gate node transitions the item to `deadlocked` and routes the Workitem to Assay.

**friction**
: A quantitative signal measuring governance cost. Purely additive — callers emit a magnitude and the Flow Monitor aggregates. Generated transparently by feedback (magnitude = depth), Assay jury rounds (magnitude = depth ^ (round + 1)), and HITL escalation (magnitude = depth ^ (rounds * 2)). Nodes may also emit friction voluntarily via `AddFriction`. Detail: [Conceptual Overview](../01-concepts/00-overview.md#friction), [Data Model](../01-concepts/03-data-model.md#friction).

**governed artefact**
: An artefact kind registered via a GovernedArtefact CRD. The CRD declares the stamp vocabulary — which stamp names are meaningful for that kind. Entry and exit contracts select from this vocabulary. Detail: [Data Model](../01-concepts/03-data-model.md#governed-artefacts).

**HITL** (Human-in-the-Loop)
: Any point where the system pauses for a human decision. Tier 3 law conflicts, Assay deadlock escalations, and Governance Flow ratification all produce HITL notifications. Human intervention is the final authority when automated governance reaches its ceiling.

**passport**
: The collection of stamps on a specific artefact version. Tracks which governance checkpoints have been satisfied for that content hash. Stored in the Archivist's database, not on the Workitem CRD. Detail: [Data Model](../01-concepts/03-data-model.md#passports-and-stamps).

**stamp**
: A named governance checkpoint on an artefact's passport. Records the stamp name, the applying node, the content hash, and a cryptographic signature with certificate chain. Stamps are write-once per artefact version — a second application of the same stamp name to the same version is rejected. Detail: [Conceptual Overview](../01-concepts/00-overview.md#stamps), [Data Model](../01-concepts/03-data-model.md#passports-and-stamps).

**stamp vocabulary**
: The set of stamp names declared by a GovernedArtefact CRD as meaningful for an artefact kind (e.g. `["linter", "security-review", "approval"]`). Entry and exit contracts select required stamps from this vocabulary. The platform attaches no built-in semantics to any stamp name.

**version**
: A specific snapshot of an artefact's content, identified by its content hash. The Archivist maintains the full version history. When content changes, a new version is created; existing stamps remain with the old version and the new version starts with no stamps.

**Workitem**
: The unit of work. A Kubernetes CRD that carries an immutable declaration surface (`spec`) and a mutable runtime surface (`status`). References artefacts by `id` and `kind`. Assigned to exactly one node at a time. Detail: [Data Model](../01-concepts/03-data-model.md#workitems), [Workitem Runtime](../02-flow/02-workitem.md).

---

## Governance and Legal Terms

**appeal**
: The mechanism by which Assay escalates conflicts involving Tier 4 or Tier 5 laws to the Governance Flow via the Librarian. Assay cannot directly modify laws above its judicial tier. Detail: [Governance](../01-concepts/04-governance.md#escalation-across-boundaries).

**`appliesTo`**
: A field on each law listing zero or more governed artefact kinds the law applies to (e.g. `["haiku"]`, `["haiku", "sonnet"]`). An empty list means the law is global — it applies to all artefact kinds in the Flow. Law conflict detection is scoped by `appliesTo`. Detail: [CRD Reference](./crds.md#law), [SDK Legal](../04-sdk/03-sdk-legal.md).

**citation**
: Recording usage of a law during Workitem processing. Each `Cite` call emits a low-magnitude friction event attributed to the cited law. Accumulated citations drive friction-threshold hearings. Detail: [SDK Legal](../04-sdk/03-sdk-legal.md#citation).

**codification**
: The process of translating a law's natural-language goal into a formal representation (formal logic, executable validator, policy-as-code) through a Codification Service. Detail: [System Services](../02-flow/04-system-services.md#codification-services).

**Codification Service**
: A specialised Flow Support Service for translating law goals into formal representations. Extends the `CodificationService` SDK base class and exposes an `encode` capability. Detail: [System Services](../02-flow/04-system-services.md#codification-services).

**contempt guard**
: The Archivist-enforced mechanism that prevents overriding Assay-linked judicial rulings on feedback items. Once a `linkedRuling` is set, the losing side must accept the verdict — contradictory state transitions return `CONTEMPT_VIOLATION`. Detail: [Data Model](../01-concepts/03-data-model.md#contempt-guard).

**deadlock**
: The state a feedback item enters when its history depth exceeds `maxFeedbackDepth`. The gate node transitions the item to `deadlocked` and routes the Workitem to Assay for judicial review. Distinct from the Thrash Guard, which detects infrastructure-level loops across the whole Workitem.

**Federal Accord** (Tier 5)
: A law synchronised from upstream Federal authorities. Applies across all Governance Flow instances in the network. The highest tier of law. Detail: [Data Model](../01-concepts/03-data-model.md#law-tiers).

**Finding** (Tier 1)
: An ephemeral law recorded by nodes during Workitem processing. Carries a configurable TTL. Decays if uncited; can be promoted to a Tier 2 Ruling through a friction-threshold hearing. Detail: [Data Model](../01-concepts/03-data-model.md#law-tiers).

**goal**
: A law's plain-language statement of what it enforces, stops, or ensures. The law's identity. All representations of a law must express the same goal.

**governance hardening**
: The process by which laws gain enforceability over time. A prose-only Tier 1 Finding can acquire formal logic representations when promoted to Tier 2 via Codification Services. Authority increases through tier promotion; enforceability increases through representation.

**grace period**
: A time-boxed exemption during Tier 3 law integration conflicts. The old Tier 3 law remains enforced while the incoming higher-tier law is queued. On expiry, the incoming law integrates automatically and the conflicting Tier 3 law is retired. Detail: [Governance](../01-concepts/04-governance.md#grace-period).

**hearing**
: See **review hearing** below.

**law**
: A governance rule with a goal and one or more representations. A single object (not a group of linked objects). Any mutation to any part produces a new version identified by content hash. Scoped to artefact kinds via `appliesTo`. Detail: [Data Model](../01-concepts/03-data-model.md#laws).

**law integration**
: The protocol by which higher-tier laws are integrated into a Flow's Library. A two-stage process: semantic search (vector similarity) followed by LLM conflict evaluation. Resolution depends on the tier of conflicting local laws. Detail: [Governance](../01-concepts/04-governance.md#law-integration-protocol), [Cross-Flow](../02-flow/06-cross-flow.md).

**Library**
: A Flow's collective body of law — every law the Flow has discovered, enacted, or inherited. Managed by the Librarian. Nodes query the Library for applicable laws through the SDK.

**linkedRuling**
: A field on a feedback item set by Assay when rendering a verdict. References the Tier 2 Ruling that resolved the dispute. Once set, the contempt guard enforces finality.

**Local Statute** (Tier 3)
: A persistent law enacted by the Flow Architect. For standalone Flows, applied as a CRD by an administrator. Has no automatic decay. Detail: [Data Model](../01-concepts/03-data-model.md#law-tiers).

**representation**
: A specific expression of a law's goal — prose, formal logic, executable code, or any other format identified by MIME type. A law can carry multiple representations. Nodes query for representations they can interpret. Adding or removing a representation produces a new law version. Detail: [Data Model](../01-concepts/03-data-model.md#representations).

**review hearing**
: A judicial proceeding processed as a standard Workitem at Assay. Triggered by the Librarian when a law's accumulated friction crosses a configured threshold or when a law's TTL expires. The law remains active during the hearing. Produces tier-specific verdicts: promote, retire, or demote. Detail: [Governance](../01-concepts/04-governance.md#decay-and-retirement).

**Ruling** (Tier 2)
: Binding precedent minted by Assay when resolving disputes. Carries a configurable TTL and requires a formal review hearing before retirement. Detail: [Data Model](../01-concepts/03-data-model.md#law-tiers).

**State Constitution** (Tier 4)
: Organisational policy produced by the Governance Flow through the standard Foundry Cycle with HITL ratification. Applies to all Sibling Flows under the Governance Flow. Detail: [Data Model](../01-concepts/03-data-model.md#law-tiers).

**supremacy**
: The principle that higher-tier law always wins. Absolute — no upward override. A Tier 3 Local Statute cannot override a Tier 4 State Constitution, regardless of creation time.

**tier**
: A law's level in the five-tier jurisdictional hierarchy. Tier 1 (Finding), Tier 2 (Ruling), Tier 3 (Local Statute), Tier 4 (State Constitution), Tier 5 (Federal Accord). Higher tier carries greater authority. Detail: [Data Model](../01-concepts/03-data-model.md#law-tiers).

**TTL**
: Time-to-live. A configurable expiry window on Tier 1 and Tier 2 laws. When a law's TTL expires, the Librarian triggers a review hearing. The law remains active during the hearing.

**verdict**
: The outcome of a review hearing rendered by Assay. Tier-specific: promote, retire (Tier 1), or demote (Tier 2). Hearings produce a decisive outcome — there is no TTL reset.

---

## Federation and Cross-Flow Terms

**cross-flow stamp authority**
: The rules governing whether imported stamps satisfy local stamp requirements. Topology-dependent: sibling stamps are authoritative after shared-root chain verification; treaty stamps are provenance-only until naturalisation. Detail: [Cross-Flow](../02-flow/06-cross-flow.md).

**Governance Flow**
: A dedicated, pre-configured Flow whose governed artefacts are laws. Produces Tier 4 State Constitution laws, synchronises Tier 5 Federal Accords, and serves as the State Root Certificate Authority. Uses the same runtime and CRDs as any other Flow. Detail: [Governance](../01-concepts/04-governance.md#the-governance-flow).

**naturalisation**
: The process by which imported artefacts and stamps gain local governance standing in a receiving Flow. At treaty boundaries, foreign stamps are preserved for audit but do not satisfy local requirements until the receiving Flow naturalises them. Detail: [Cross-Flow](../02-flow/06-cross-flow.md).

**Sibling Flow**
: A Flow that shares a Governance Flow (and therefore a State Root CA) with other Flows. Sibling Flows share implicit trust through the common root — imported stamps are authoritative after chain verification when stamp names match. Sibling Flows do not require treaties.

**State Root**
: The self-signed Root CA keypair held by the Governance Flow. Issues intermediate CA certificates to each Sibling Flow's Operator, establishing a shared trust hierarchy. Detail: [Governance](../01-concepts/04-governance.md#state-root-certificate-authority).

**treaty**
: A bilateral agreement enabling collaboration between Flows that do not share a Governance Flow. Provides explicit trust through a directed trust edge — unidirectional. Two-way exchange requires two treaties. Detail: [Governance](../01-concepts/04-governance.md#treaties), [Cross-Flow](../02-flow/06-cross-flow.md).

---

## Capability and Contract Terms

**capability**
: A permission granted to a node through the FoundryNode CRD's `capabilities` field. Determines what operations the node is authorised to perform. Enforced by the owning runtime service, not by the SDK or the node. Detail: [Configuration](../02-flow/05-configuration.md), [Nodes](../02-flow/03-nodes-external.md).

**capability syntax**
: The structured grammar for capability grants: `VERB:RESOURCE[/QUALIFIER]`. Verbs: `READ`, `WRITE`, `STAMP`, `USE`. Examples: `READ:law`, `WRITE:artefact`, `WRITE:feedback/deadlocked`, `STAMP:artefact/haiku/linter`, `USE:support/codify-smt/encode`. Detail: [Configuration](../02-flow/05-configuration.md), [Node Configuration](../03-node/02-configuration.md).

**entry binding**
: A FoundryNode CRD field (`entry`) that references a named entry contract on the FoundryFlow. Nodes with entry bindings serve as admission points: local Workitem creation, cross-flow import (via `importNode`), and review-hearing intake. Detail: [Configuration](../02-flow/05-configuration.md).

**entry contract**
: A named set of artefact-kind requirements that a Workitem must satisfy for admission. Defined on the FoundryFlow CRD (`entryContracts`). Enforced at local creation, cross-flow import, and review-hearing intake. Uses the same shape as exit contracts. Detail: [Data Model](../01-concepts/03-data-model.md#entry-and-exit-contracts), [Configuration](../02-flow/05-configuration.md).

**exit binding**
: A FoundryNode CRD field (`exit`) that references a named exit contract on the FoundryFlow. Only nodes with exit bindings can call `complete()`. The binding is fixed in configuration — the node does not choose which contract to validate. Detail: [Configuration](../02-flow/05-configuration.md).

**exit contract**
: A named set of artefact-kind requirements that a Workitem must satisfy for completion. Defined on the FoundryFlow CRD (`exitContracts`). Enforced by the Operator when an exit node calls `complete()`. When completion triggers cross-flow export, only artefact kinds listed in the contract are exported. Detail: [Data Model](../01-concepts/03-data-model.md#entry-and-exit-contracts), [Configuration](../02-flow/05-configuration.md).

**import node**
: The node designated in the FoundryFlow CRD (`importNode`) as the entry point for cross-flow imported Workitems. Must reference a FoundryNode bound to an entry contract. Imported Workitems are created in `Pending` and first-scheduled to this node when capacity allows. Detail: [Configuration](../02-flow/05-configuration.md), [Cross-Flow](../02-flow/06-cross-flow.md).

---

## Superseded Terms

These legacy terms are explicitly out of scope in v1. They must not appear in spec documents except in this superseded-term listing.

| Superseded Term | Replacement | Notes |
|-----------------|-------------|-------|
| `WorkitemType` | Entry/exit contracts | Flow admission is not type-gated. |
| `spec.type` | Entry/exit contracts | No Workitem type discriminator exists. |
| `spec.context` / `status.context` | Governed artefacts | No freeform context bag. All work context is represented by explicit Workitem state and governed artefacts. |
| `entryNode` | `importNode` + entry bindings | Import entry is `importNode`; local admission uses entry-bound nodes. |
| `terminalContract` / `terminalContracts` | `exitContracts` + exit bindings | Exit contracts are named on the FoundryFlow; nodes bind to them via `exit`. |
| node `terminal` binding | `exit` binding | Nodes are exit-bound via the `exit` field, not a `terminal` flag. |
| Law Groups (`group` field) | Single-object multi-representation law | A law is one object with a goal and multiple representations, not a group of linked CRDs. |
| `ReviewHearing` CRD | Standard Workitems at Assay | Hearings use standard Workitems with explicit artefacts and contract bindings. |
| Reserved underscore context keys | Governed artefacts | No reserved key namespace for bag-style metadata. |

---

## Glossary Invariants

1. Every term defined here has exactly one canonical definition.
2. Superseded terms must not appear in normative spec prose outside this glossary.
3. Term definitions must remain consistent with [AGENTS.md key decisions](../AGENTS.md) — when a glossary definition and a key decision conflict, the key decision governs.
4. Cross-reference links point to the first normative detail location for each term.
5. British spelling is used for all spec prose (`artefact`, `naturalisation`, `organisation`, `behaviour`). US spelling is reserved for literal external identifiers.
