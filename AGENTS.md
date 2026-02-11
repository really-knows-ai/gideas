# AGENTS.md

## Project

This repository contains the technical specification for **Foundry Flow** — a governed workflow runtime on Kubernetes that orchestrates work through adversarial cycles of creation, validation, review, and refinement.

The spec is being rewritten from scratch. Legacy material (earlier spec drafts, foundational papers, CRDs) lives in `legacy/` and serves as read-only source material. The new spec lives at the repository root.

## Goal

Produce a clean, coherent, GitHub-style specification that:

- Has a clear reading order: Concepts → Flow → Node → Reference
- Uses consistent terminology throughout (defined in the glossary)
- Eliminates duplication — one source of truth per concept
- Is informed by the foundational theory in `legacy/papers/` but implements the details from the legacy specs
- Is **v1** — the complete spec with no v1/v2 split
- Includes the **Governance Flow** as a first-class part of the spec

## Spec Structure

```text
/
├── AGENTS.md
├── README.md                    # Entry point, navigation (write last)
│
├── 01-concepts/                 # Helicopter view — read first
│   ├── 00-overview.md           # ✅ COMPLETE
│   ├── 01-architecture.md       # Six-plane architecture, design principles
│   ├── 02-data-model.md         # Workitems, Artefacts, Laws, Feedback (detail)
│   └── 03-governance.md         # Law tiers, precedent, the legal metaphor (detail)
│
├── 02-flow/                     # The Platform — assumes nodes exist
│   ├── 00-overview.md
│   ├── 01-operator.md
│   ├── 02-workitem.md
│   ├── 03-nodes-external.md
│   ├── 04-system-services.md
│   ├── 05-configuration.md
│   ├── 06-cross-flow.md
│   └── 07-operations.md
│
├── 03-node/                     # Building Nodes — the developer perspective
│   ├── 00-overview.md
│   ├── 01-sidecar.md
│   ├── 02-sdk-core.md
│   ├── 03-sdk-artefacts.md
│   ├── 04-sdk-legal.md
│   ├── 05-sdk-feedback.md
│   ├── 06-sdk-workitems.md
│   ├── 07-sdk-telemetry.md
│   ├── 08-configuration.md
│   └── 09-patterns.md
│
├── 04-reference/                # Quick lookup
│   ├── crds.md
│   ├── grpc-api.md
│   ├── error-catalog.md
│   └── glossary.md
│
└── legacy/                      # Source material (read-only reference)
    ├── papers/                  # Foundational theory (5 files)
    ├── flow_spec/               # Legacy Flow runtime spec (~35 files)
    ├── node_spec/               # Legacy Node runtime spec (~18 files)
    ├── governance_spec/         # Legacy governance spec (~11 files)
    ├── crds/                    # Legacy CRD YAML definitions
    ├── PolymorphicLaw.md        # Polymorphic law envelope paper
    ├── PROGRESS.md              # Original session notes
    └── Tier5.md                 # 5-tier law hierarchy design rationale
```

### Reading Order

1. **Concepts** — What Foundry Flow is and why it exists
2. **Flow** — The platform (audience: operators and admins)
3. **Node** — Building nodes (audience: developers)
4. **Reference** — Look things up

## Current Status

| Document | Status |
|----------|--------|
| `01-concepts/00-overview.md` | Complete |
| `01-concepts/01-architecture.md` | Complete |
| `01-concepts/02-data-model.md` | Complete |
| `01-concepts/03-governance.md` | Complete |
| Everything else | Not started |

`README.md` should be written last, once the spec is complete.

## Writing Principles

### Define things on their own terms

Affirmative, confident, direct. Never define a system by what it isn't. The reader has no baggage to unpack — do not assume they are bringing preconceptions that need correcting.

Bad: "Unlike traditional CI/CD pipelines, Foundry Flow doesn't just run tasks sequentially."
Good: "Foundry Flow orchestrates work through adversarial cycles of creation, validation, review, and refinement."

### No planning voice in finished documents

Do not write "these are the eight nouns" or "four axioms underpin the system." Let the content present itself. The document structure should be invisible — readers absorb the concepts, not your outline.

### No meta-commentary

Do not narrate the document's structure to the reader. "In this section we will..." and "the following table summarises..." are planning artifacts that should not survive into the finished text.

### Show, don't scaffold

Use diagrams (Mermaid), tables, and examples where they clarify. But they should feel like natural parts of the explanation, not bolted-on visual aids announced by a sentence.

### Mermaid line breaks

In `flowchart` and `sequenceDiagram` blocks, use `<br/>` for line breaks in node labels and edge labels. Do **not** use `\n` — it renders as literal text in these diagram types. (`stateDiagram-v2` handles `\n` natively and is the exception.)

### Cross-link aggressively

Every concept that has a detail page should link to it on first mention in each document. Use relative markdown links.

## Key Decisions

These decisions have been made and must be preserved across all documents.

### Forge reads laws only

Forge queries the Library for context seeding but does not write laws. It reads all tiers to seed its generation context. Law writing belongs to downstream nodes (Appraise, Refine, Assay).

### Sort stamps approval

Sort is a gate that evaluates state and routes. Its logic:

1. Unresolved feedback on governed artefacts → route to **Refine**
2. Deadlocked feedback → route to **Assay**
3. Missing required stamps → route to the node configured to provide them (**Appraise**, in the reference arrangement)
4. All feedback resolved, all required stamps present → apply the **approval** stamp and **Done**

Sort is the only node that applies the "approval" stamp in the reference arrangement. Any stamp can be granted to any node by the Flow Architect. The reference arrangement makes strong recommendations but does not force the Flow Architect's hand.

The routing targets above (Refine, Assay, Appraise) reflect the reference arrangement. Sort discovers routing targets by reading the Flow configuration — it looks at the missing stamp and routes to the node configured to provide it. A node granted `READ:flow` capability can query the topology to discover stamp-to-node mappings at runtime.

### Stamps are named governance checkpoints

A stamp is a named governance checkpoint on an artefact's passport. The GovernedArtefact CRD defines which stamp names are required (e.g. "linter", "security-review", "approval"). The FoundryNode CRD (configured by the Flow Architect) grants nodes permission to apply specific named stamps to specific artefact kinds via the `STAMP:artefact/<kind>/<stamp-name>` capability.

The system treats all stamps identically — it attaches no special semantics to any stamp name. "Approval", "linter", "security-review" are naming conventions chosen by the Flow Architect. The reference arrangement uses an "approval" stamp applied by Sort as the final gate, but this is convention, not system behaviour.

Stamps are write-once per artefact version. Once a stamp has been applied to a specific content hash, a second node attempting to apply the same stamp name to the same version receives an error. If two different nodes need to sign off independently, define two different stamps.

### Terminal contracts are per governed artefact

A terminal contract defines what a Workitem must carry — which artefacts, with which stamps — to exit the Flow from a specific terminal node. The requirements are specified per governed artefact kind. Each kind entry maps to a list of required stamp names; an empty list means that artefacts of that kind must be present but no stamps are required. A contract with no artefact entries imposes no artefact requirements.

If a Workitem contains multiple artefacts of a required kind, all of them must satisfy that kind's requirement.

When terminal completion triggers cross-flow export, only artefacts whose kinds are listed in the selected terminal contract are exported. An empty contract exports no artefacts (metadata only).

### Terminal nodes and the complete() contract

The FoundryFlow CRD defines named terminal contracts. The FoundryNode CRD marks a node as terminal by referencing a contract by name (e.g., `terminal: "approved"`). Only terminal nodes can call `complete()` — non-terminal nodes calling `complete()` receive an error. When a terminal node calls `complete()`, the Operator validates the Workitem against the referenced terminal contract. The node does not choose which contract to validate — the binding is fixed in configuration.

In the reference arrangement, Sort is the only terminal node.

### Friction is systemic heat

Workitems generate friction everywhere they touch — nodes, laws, rework loops, reviewers. The Friction Ledger tracks it and tags it to source (laws, nodes, topology paths) for aggregation and querying. Friction is defined affirmatively as a measurable signal, not defended against the accusation of being "just governance overhead."

### Archivist is the artefact lifecycle service

The Archivist manages all artefact-related data beyond raw content bytes. Its storage is split into two layers:

- **SQLite database** — artefact version history, passport stamps, and feedback. This is the single queryable layer for all artefact provenance.
- **Blob store** (PVC or cloud object storage) — raw artefact content bytes, keyed by content hash.

**Feedback lives in the Archivist, not on the Workitem CRD.** Feedback is scoped to Workitem ID + artefact `id`, and each feedback item is tagged with the artefact version hash it pertains to. All feedback is preserved across versions.

**Passports and stamps live in the Archivist's SQLite**, not as JSON sidecar files in the blob store.

**The Workitem CRD carries artefact references**: `id` and `kind` only. Each artefact has a unique `id` within the Workitem, and multiple artefacts of the same `kind` are supported. The full version history, stamps, and feedback live in the Archivist, keyed by artefact `id`. This keeps the CRD well within etcd's 1.5MB limit regardless of feedback depth or version count.

**The SDK exposes an Artefact object** with methods for querying versions, feedback, and stamps. All queries are routed through the Sidecar to the Archivist; nodes never interact with the Archivist directly.

**Sort uses the SDK** to check feedback state, the same as any other node. `artefact.hasUnresolvedFeedback()` is the interface for routing decisions.

### Concepts documents are technology-agnostic

The `01-concepts/` documents describe architecture, data model, and governance in terms of roles and responsibilities — not products. They say "embedded database", "content-addressed store", "metrics pipeline", and "deployment tooling" rather than naming SQLite, PVC, Prometheus, Helm, gRPC, or Docker. Technology choices are firm decisions (recorded in this file and throughout the key decisions below), but they belong in `02-flow/`, `03-node/`, and `04-reference/` where the audience is operators and developers making implementation decisions. The concepts audience needs to understand *what* each component does and *why* — not *which product* does it.

**Exception: Kubernetes platform vocabulary.** "Kubernetes", "CRD", "namespace", "cluster", and related Kubernetes-native concepts are accepted as foundational domain vocabulary in concepts documents. The spec is explicitly Kubernetes-native — these terms define the platform, not incidental implementation choices. Product names (SQLite, Prometheus, Helm, gRPC, Docker) and methodology names (GitOps) remain excluded from concepts.

### Laws and the Library stay high-level in concepts

The key concept: a law has a textual **goal** (what it enforces/stops/ensures) and one or more **representations** (prose, formal logic, executable code, etc.). The Library stores all representations with equal indifference and leaves interpretation to the nodes. Technical details (MIME types, CRD structure, Kubernetes labels, Codification Services, Librarian embedding pipeline) belong in later documents (`02-flow/04-system-services.md`, `04-reference/crds.md`).

### Laws are single objects with multiple representations

A law is one object, not a group of linked objects. Each law has:

- A **goal** — plain-language statement of what the law enforces, stops, or ensures. This is the law's identity.
- One or more **representations** — different ways of expressing the same goal (prose, formal logic, executable, etc.). Representations must all enforce the same goal.
- A **tier** (1–5) and lifecycle metadata.

Whole-law versioning: any mutation to any part of the law (goal, representations, metadata) produces a new version identified by content hash. Representations are not independently versioned.

Governance hardening means a law gains new representations over time. A prose-only Tier 1 Finding can gain a formal logic representation when promoted to a Tier 2 Ruling via Codification Services, making it deterministically enforceable. The goal stays the same; enforceability increases.

This replaces the earlier "Law Groups" design where separate Law CRDs were linked by a shared group identifier.

### Review hearing verdict schema

Review hearings use tier-specific verdicts. There are three hearing types:

**Citation-threshold hearing** (Tier 1 Finding is heavily cited):

- **Promote** — Finding is minted as a Tier 2 Ruling
- **Retain** — Finding's TTL is reset, stays at Tier 1

**Tier 1 TTL-expiry hearing:**

- **Retire** — Finding is deleted (history preserved in audit log)
- **Promote** — Finding is minted as a Tier 2 Ruling

**Tier 2 TTL-expiry hearing:**

- **Demote** — Ruling drops to Tier 1 Finding (fresh TTL, citation history does not carry over)
- **Promote** — Assay petitions for Tier 3 Statute (HITL ratification required)

### The Foundry Cycle is a reference arrangement

The Foundry Cycle is the reference arrangement — the standard pattern demonstrating how combining deterministic and non-deterministic checks (contextually relevant to the artefact) produces consistent, verifiable outcomes. Flow Architects are expected to adapt it to their context. The standard library provides configurable reference implementations for each node type (Forge, Quench, Appraise, Sort, Refine) as Docker containers. Flow Architects can extend them (e.g., `FROM gideas/sort-node`), adapt them, or implement completely custom nodes.

### Assay is a standard Flow component

Assay is a standard component of every Flow, not a swappable reference implementation. It is built into the runtime as a Flow component. Despite being routable as a node (Workitems can be sent to it for judicial review), it is always present — Flow Architects do not choose whether to include it. Full detail in `02-flow/`.

### Governance Flow is in scope

The Governance Flow and its lifecycle are a first-class part of the spec. The `legacy/governance_spec/` directory is a primary source alongside `legacy/flow_spec/` and `legacy/node_spec/`.

### Five-tier law hierarchy

Laws are organised into five tiers of jurisdiction. Higher tier always wins (supremacy is absolute, no upward override):

| Tier | Name | Scope | Authority |
|------|------|-------|-----------|
| 1 | Finding | Single Flow | Nodes (any with `WRITE:law/finding` capability; Appraise and Refine in the reference arrangement) |
| 2 | Ruling | Single Flow | Assay Node |
| 3 | Local Statute | Single Flow | Flow Architect (human-administered or local legislative cycle) |
| 4 | State Constitution | All Flows in a Governance Flow instance | Governance Flow |
| 5 | Federal Accord | All instances in the network | Federation |

For standalone Flows (no Governance Flow), Tier 3 laws are CRDs applied by an admin. Tiers 4 and 5 do not exist. Under a Governance Flow, the Governance Flow is itself a Flow whose governed artefacts are laws — subject to the same Foundry Cycle as any other Flow.

The full design rationale is in `legacy/Tier5.md`.

### Law integration protocol

When higher-tier laws are pushed to a Sibling Flow (via Librarian-to-Librarian replication), the receiving Librarian runs a two-stage conflict check: semantic search (vector similarity, configurable threshold) followed by LLM evaluation of actual contradiction. Resolution depends on the tier of the conflicting local law:

- **Tier 1-2 conflicts:** Immediate retirement of the local law. No human intervention.
- **Tier 3 conflicts:** Integration paused, HITL notification. The local statute *must* change (supremacy is not optional), but the Sibling Flow can request a **grace period**. During the grace period the old Tier 3 law remains enforced and the incoming law is queued. On expiry the incoming law integrates automatically and the Tier 3 law is retired — if the Sibling Flow hasn't adapted, their work fails governance checks organically.

Retired laws are deleted as CRDs. The full history is preserved in the audit log.

### Escalation paths and Assay's authority ceiling

Runtime conflicts (discovered during Workitem processing, not at integration time) always go to Assay for judicial review. Supremacy heavily informs Assay's decision but does not bypass the judicial process — Assay deliberates on every conflict. Resolution depends on the tiers involved:

- **Cross-tier conflict (Tier 1 vs Tier 2):** Assay resolves. Supremacy heavily informs the outcome — the higher-tier law carries greater authority — but Assay still deliberates. Assay mints a new Tier 2 Ruling consolidating the surviving position. Originals retired.
- **Same-tier conflict (Tier 1 vs Tier 1, or Tier 2 vs Tier 2):** Assay resolves and drafts a new Tier 2 Ruling consolidating the conflicting laws. Originals retired.
- **Tier 1-2 vs Tier 3:** The lower-tier law is retired. If the conflict reveals ambiguity or a gap in the Tier 3 statute, Assay petitions HITL with a proposed clarification or amendment.
- **Tier 3 vs Tier 3:** Assay drafts a *proposal* for a consolidated Tier 3 law and petitions HITL. On rejection, the conflict persists — every future Workitem that hits the same conflict generates another HITL escalation and more friction until the humans act.
- **Tier 4 or Tier 5 involvement:** Assay files an **appeal** to the Governance Flow via the Librarian. The Governance Flow can repeal or amend Tier 4 laws; Tier 5 appeals are escalated to the relevant Federal authority.

Assay can **resolve** at Tier 2 (minting Rulings), **propose** at Tier 3, and **appeal** at Tier 4-5. It does not write Tier 1 Findings. It cannot directly modify laws above its judicial tier.

### Workitem context — reserved keys

The Workitem `context` map reserves keys starting with an underscore (`_`) for system use. The `01-concepts/02-data-model.md` document states this convention but does not enumerate the reserved keys. `03-node/06-sdk-workitems.md` must define which system keys exist, what they contain, and when they are set.

### This is v1

Complete spec, no v1/v2 split.

### Four foundational axioms

1. **Assume Unreliability** — All agents are fallible. Trust intent, verify execution.
2. **Make Work Auditable** — Every action becomes an immutable, traceable record.
3. **Make the Cost Visible** — Friction is a first-class, quantifiable signal.
4. **Quality is Fixed, Cost is Variable** — The standard is non-negotiable; the system measures the cost of achieving it.

## Using Legacy Material

The `legacy/` directory contains the raw source material:

- **`legacy/papers/`** — Five foundational papers. These provide the conceptual "why." Read them to understand the philosophy, but do not copy their prose or structure. The new spec must stand on its own.
- **`legacy/flow_spec/`** — The Flow runtime spec (~35 files). Dense, comprehensive, sometimes contradictory. This is the primary source for `02-flow/`.
- **`legacy/node_spec/`** — The Node runtime spec (~18 files, including sidecar and SDK). Primary source for `03-node/`.
- **`legacy/governance_spec/`** — Governance Flow/Federation spec (~11 files). Primary source for the Governance Flow, law tiers, and precedent.
- **`legacy/crds/`** — CRD YAML definitions. Source for `04-reference/crds.md`.
- **`legacy/PolymorphicLaw.md`** — The polymorphic law envelope concept. Relevant to `02-flow/04-system-services.md` (Librarian).
- **`legacy/PROGRESS.md`** — Session notes from the rewrite process. Contains decisions and clarifications, some of which are superseded by this file.
- **`legacy/Tier5.md`** — Working reference for the 5-tier law hierarchy, integration protocol, escalation paths, and treaty model. Not legacy — this is an active design document that informed the key decisions in this file.

When the legacy material and this file disagree, **this file wins**. In particular, `PROGRESS.md`'s law authority table is stale — it incorrectly lists Forge as writing Tier 1 laws.

## Workflow

1. Read this file fully before starting any work.
2. Read the status table in this file to find files to understand the tone, depth, and style of completed documents.
3. Identify the next document to write by asking the user.
4. Read the relevant legacy source files.
5. Draft the document following the writing principles.
6. **Review all completed spec documents** for consistency with the new material and technical feasibility. Consistency and technical feasibility are non-negotiable — every mechanism described must be implementable, and no two documents should contradict each other. Flag and fix any issues before considering the document complete.
7. Update the status table in this file when a document is complete.
