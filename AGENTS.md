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
- Includes the **Governance Flow** (Governor Operator) as a first-class part of the spec

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
    └── PROGRESS.md              # Original session notes
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
3. Missing required inspection stamps → route to **Appraise**
4. All feedback resolved, all inspections present → stamp **approval** and **Done**

Sort is the only node that stamps approval.

### Roles are defined by the terminal contract, granted by the Flow

The terminal contract is the source of truth for what roles exist. The Flow grants nodes permission to stamp as specific roles. "Role" is used narrowly throughout — it means "the capacity in which a node stamps a passport."

### Terminal contracts are per governed artefact

Each artefact's contract specifies required stamps (role + type), or simply that the artefact must be present. Different artefacts can have different requirements.

### Two stamp types: inspection and approval

Stamps are either **inspection** ("I have checked this") or **approval** ("I consider this valid"). The terminal contract specifies both the role AND the type required for each artefact.

### Friction is systemic heat

Workitems generate friction everywhere they touch — nodes, laws, rework loops, reviewers. The Friction Ledger tracks it and tags it to source (laws, nodes, topology paths) for aggregation and querying. Friction is defined affirmatively as a measurable signal, not defended against the accusation of being "just governance overhead."

### Archivist is the artefact lifecycle service

The Archivist manages all artefact-related data beyond raw content bytes. Its storage is split into two layers:

- **SQLite database** — artefact version history, passport stamps, and feedback. This is the single queryable layer for all artefact provenance.
- **Blob store** (PVC or cloud object storage) — raw artefact content bytes, keyed by content hash.

**Feedback lives in the Archivist, not on the Workitem CRD.** Feedback is scoped to Workitem ID + artefact kind, and each feedback item is tagged with the artefact version hash it pertains to. All feedback is preserved across versions.

**Passports and stamps live in the Archivist's SQLite**, not as JSON sidecar files in the blob store.

**The Workitem CRD carries a slim artefact reference**: kind and `latestVersion` hash only. The full version history, stamps, and feedback live in the Archivist. This keeps the CRD well within etcd's 1.5MB limit regardless of feedback depth or version count.

**The SDK exposes an Artefact object** — `workitem.getArtefact("haiku")` — with methods like `getLatestVersion()`, `getVersion(hash)`, `getFeedback()`, `hasUnresolvedFeedback()`, `getPassport()`. All queries are routed through the Sidecar to the Archivist; nodes never interact with the Archivist directly.

**Sort uses the SDK** to check feedback state, the same as any other node. `artefact.hasUnresolvedFeedback()` is the interface for routing decisions.

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

### The Foundry Cycle is the canonical arrangement

Described directly as the standard topology. Not presented as "an applied pattern" or "one possible configuration."

### Governance Flow is in scope

The Governor Operator and its lifecycle are a first-class part of the spec. The `legacy/governance_spec/` directory is a primary source alongside `legacy/flow_spec/` and `legacy/node_spec/`.

### Five-tier law hierarchy

Laws are organised into five tiers of jurisdiction. Higher tier always wins (supremacy is absolute, no upward override):

| Tier | Name | Scope | Authority |
|------|------|-------|-----------|
| 1 | Finding | Single Flow | Nodes (Appraise, Refine, Assay) |
| 2 | Ruling | Single Flow | Assay Node |
| 3 | Local Statute | Single Flow | Flow Operator (human-administered or local legislative cycle) |
| 4 | State Constitution | All Flows in a Governor instance | Governor (State Governance Flow) |
| 5 | Federal Accord | All instances in the network | Federation |

For standalone Flows (no Governor), Tier 3 laws are CRDs applied by an admin. Tiers 4 and 5 do not exist. Under a Governor, the Governance Flow is itself a Flow whose governed artefacts are laws — subject to the same Foundry Cycle as any other Flow.

The full design rationale is in `legacy/Tier5.md`.

### Law integration protocol

When higher-tier laws are pushed to a city Flow (via Librarian-to-Librarian gRPC), the receiving Librarian runs a two-stage conflict check: semantic search (sqlite-vec, configurable similarity threshold) followed by LLM evaluation of actual contradiction. Resolution depends on the tier of the conflicting local law:

- **Tier 1-2 conflicts:** Immediate retirement of the local law. No human intervention.
- **Tier 3 conflicts:** Integration paused, HITL notification. The local statute *must* change (supremacy is not optional), but the city can request a **grace period**. During the grace period the old Tier 3 law remains enforced and the incoming law is queued. On expiry the incoming law integrates automatically and the Tier 3 law is retired — if the city hasn't adapted, their work fails governance checks organically.

Retired laws are deleted as CRDs. The full history is preserved in the audit log.

### Escalation paths and Assay's authority ceiling

Runtime conflicts (discovered during Workitem processing, not at integration time) are resolved based on the tiers involved:

- **Cross-tier conflict:** Supremacy decides immediately. Higher tier wins.
- **Tier 1-2 vs Tier 1-2:** Assay resolves and drafts a new Tier 2 Ruling consolidating the conflicting laws.
- **Tier 3 vs Tier 3:** Assay drafts a *proposal* for a consolidated Tier 3 law. HITL approves or rejects. On rejection, the conflict persists — Assay issues a one-time Tier 2 Ruling for the immediate case, and friction accumulates until the humans act.
- **Tier 4 or Tier 5 involvement:** Assay files an **appeal** to the Governor via the Librarian gRPC channel. The Governor can repeal or amend Tier 4 laws; Tier 5 appeals are escalated to the relevant Federal authority.

Assay can **resolve** at Tier 1-2, **propose** at Tier 3, and **appeal** at Tier 4-5. It cannot directly modify laws above its judicial tier.

### Workitem context — reserved keys

The Workitem `context` map reserves keys starting with an underscore (`_`) for system use. The `01-concepts/02-data-model.md` document states this convention but does not enumerate the reserved keys. `03-node/06-sdk-workitems.md` must define which system keys exist, what they contain, and when they are set.

### This is v1

Complete spec, no v1/v2 split.

### Four foundational axioms

1. **Assume Unreliability** — All agents are fallible. Trust intent, verify execution.
2. **Make Work Auditable** — Every action becomes an immutable, traceable record.
3. **Make the Cost Visible** — Friction is a first-class, quantifiable signal.
4. **Quality is Fixed, Cost is Variable** — The standard is non-negotiable; the system measures the cost of achieving it.

"Roles are Institutions, Not Individuals" was considered and removed — the concept was valid but overloaded the term "role" and is not relevant to the core spec.

## Using Legacy Material

The `legacy/` directory contains the raw source material:

- **`legacy/papers/`** — Five foundational papers. These provide the conceptual "why." Read them to understand the philosophy, but do not copy their prose or structure. The new spec must stand on its own.
- **`legacy/flow_spec/`** — The Flow runtime spec (~35 files). Dense, comprehensive, sometimes contradictory. This is the primary source for `02-flow/`.
- **`legacy/node_spec/`** — The Node runtime spec (~18 files, including sidecar and SDK). Primary source for `03-node/`.
- **`legacy/governance_spec/`** — Governor/Federation spec (~11 files). Primary source for the Governance Flow, law tiers, and precedent.
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
