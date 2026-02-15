# Spec Review

Deep review of all spec directories against AGENTS.md key decisions, writing principles, and cross-document consistency.

Reviewed directories: `01-concepts/`, `02-flow/`, `03-node/`, `04-sdk/`, `05-reference/`

Review status key: `01-concepts/` and `02-flow/` are Drafted/Complete and receive full review. `03-node/01-sidecar.md`, `03-node/02-configuration.md`, `03-node/03-patterns.md`, all `04-sdk/`, and all `05-reference/` are Stub outlines and receive structural/alignment review only. `03-node/00-overview.md` is Drafted and receives full review.

---

## Critical Issues

Issues that contradict key decisions or create internal inconsistencies. Must be fixed before proceeding.

### 1. Concepts overview refers to Friction Ledger link target that anchors to system-services, not a concepts-level page

**Files:** `01-concepts/00-overview.md:9`
**Criterion:** Key Decisions Compliance — Concepts documents are technology-agnostic
**Key Decision:** Concepts documents are technology-agnostic

Line 9 links directly to `../02-flow/04-system-services.md` for the Friction Ledger. This is acceptable as a cross-link to detail. However, the same document at line 157 links the Flow Monitor to `../02-flow/04-system-services.md#flow-monitor-and-friction-surface` with the phrasing "The Flow Monitor aggregates friction data..." — "Flow Monitor" is a system service name, which is acceptable as a cross-link forward reference from concepts. On re-evaluation, this is consistent with the exception pattern used for Foundry Cycle node names. Downgrading.

**Resolution:** On closer inspection, this pattern is consistent across the concepts docs — they name components and cross-link to detail pages. No change needed. RESOLVED — false positive on re-evaluation.

---

### ~~2. AGENTS.md status table says 00-overview.md is "COMPLETE" but spec structure comment says "✅ COMPLETE" while status table says "Drafted"~~

RESOLVED — Removed "Complete" status entirely from AGENTS.md. The status model is now: non-existent, stub outline, or drafted. Removed ✅ COMPLETE marker from structure comment, changed `01-architecture.md` from "Complete" to "Drafted", removed the "Complete" status term definition, and simplified review discipline to reference "drafted" only.

---

### ~~3. Operator queries Archivist for exit contract validation but Archivist access contract says "nodes never call Archivist directly" — Operator path needs clearer documentation~~

RESOLVED — Added Operator's direct query path to the Archivist Access Contract section in `02-flow/04-system-services.md`, distinguishing it from the Sidecar-mediated node access pattern.

---

## Significant Issues

### ~~4. Citation Processor mentioned in grpc-api stub within Librarian API scope~~

RESOLVED — Separated Citation Processor API into its own section in `05-reference/grpc-api.md`, distinct from Librarian API. Citation operations (recording, aggregation, threshold queries) are owned by the Citation Processor, not the Librarian.

---

### 5. Writing principle violation: meta-commentary "the following table summarises" pattern in concepts architecture

**Files:** `01-concepts/01-architecture.md:54`
**Criterion:** Writing Principles — No meta-commentary

Line 54 reads: "Configuration resources define the Flow's desired state, a metrics pipeline provides monitoring and dashboards, and retention policies handle housekeeping." This is acceptable descriptive prose. However, checking further — the document uses section heading "Responsibility Boundaries" at line 111 followed immediately by "Each concern in the system maps to exactly one plane. When a Node executes work, it operates in the Data Plane..." at line 113. This is borderline but reads as natural explanation rather than meta-commentary.

On thorough review of the architecture document, it is clean of meta-commentary. RESOLVED — false positive.

---

### ~~6. Writing principle violation: planning voice "A Workitem has two data surfaces" in data model~~

RESOLVED — Rewritten to "A Workitem separates an immutable declaration surface from a mutable runtime surface." in `01-concepts/03-data-model.md:11`. Removes the counting pattern.

---

### ~~7. Writing principle violation: planning voice in governance document legal metaphor~~

RESOLVED — Removed "Each branch of government has a clear institutional counterpart in the runtime." from `01-concepts/04-governance.md:9`. The table now presents itself without meta-commentary.

---

### ~~8. Concepts overview uses "container images" which is technology-specific vocabulary~~

RESOLVED — "Container images" accepted as Kubernetes platform vocabulary (nodes are containers, and the platform is Kubernetes-native). No change needed.

---

### ~~9. Foundry Cycle document uses "Docker containers" and `FROM gideas/sort-node` syntax~~

RESOLVED — Removed the `FROM gideas/sort-node` Dockerfile example from `01-concepts/02-foundry-cycle.md:5`. Replaced with technology-agnostic phrasing. "Container images" retained as accepted Kubernetes vocabulary.

---

### ~~10. Missing cross-link: Foundry Cycle document does not link to data-model for "entry and exit contracts" on first mention~~

RESOLVED — False positive on re-examination. The first mention of "exit contract" at `01-concepts/02-foundry-cycle.md:32` already contains the link `[exit contract](./03-data-model.md#entry-and-exit-contracts)`.

---

### ~~11. Missing cross-link: concepts overview does not link to Governance document on first "Governance Flow" mention~~

RESOLVED — Added link to `[Governance Flow](./04-governance.md)` on first mention at `01-concepts/00-overview.md:86`.

---

### 12. Archivist description in concepts architecture uses "SQLite" — technology-specific in concepts doc

**Files:** `01-concepts/01-architecture.md:168`
**Criterion:** Key Decisions Compliance — Concepts documents are technology-agnostic
**Key Decision:** Concepts documents are technology-agnostic

The Hybrid Persistence table at line 168 uses "Embedded database — Archivist" (good), but let me verify... Actually, reviewing lines 162-170, the architecture doc uses "Embedded database" for Librarian and Citation Processor, "Metrics pipeline" for Flow Monitor, and "Content-addressed store" for blob store. The Archivist entry says "Embedded database — Archivist" for provenance. This is technology-agnostic. No violation found.

RESOLVED — false positive. The architecture doc correctly uses technology-agnostic terms.

---

### ~~13. Concepts data model uses "GovernedArtefact CRD" YAML example with `apiVersion: flow.gideas.io/v1`~~

RESOLVED — Replaced the YAML CRD example in `01-concepts/03-data-model.md` with a conceptual description and a cross-link to the CRD Reference for the schema. The stamp vocabulary concept is now described without implementation-level YAML.

---

### ~~14. Concepts data model includes capability syntax table with exact capability strings~~

RESOLVED — Replaced the capability syntax table in `01-concepts/03-data-model.md` with a conceptual description of capability-gated access. Exact capability grant syntax now deferred to `02-flow/05-configuration.md` and `02-flow/03-nodes-external.md`.

---

### ~~15. Concepts data model contract YAML example is implementation detail~~

RESOLVED — Replaced the entry/exit contract YAML example in `01-concepts/03-data-model.md` with a conceptual description of contract semantics and cross-links to Flow Configuration and CRD Reference for the schema.

---

### ~~16. Concepts data model stamp fields table includes "signature" and "certificateChain" — implementation detail~~

RESOLVED — Replaced the typed stamp fields table in `01-concepts/03-data-model.md` with a conceptual bullet list describing what a stamp records. Precise field schema deferred to CRD Reference.

---

### ~~17. Writing principle violation: "Canonical state tokens in API/schema surfaces are:" is meta-commentary~~

RESOLVED — Removed the introductory sentence "Canonical state tokens in API/schema surfaces are:" from `01-concepts/03-data-model.md:277`. The table now follows naturally from the surrounding prose.

---

### 18. Operator document line 82 uses "terminal state" phrasing near old "terminal" concept

**Files:** `02-flow/02-workitem.md:82`
**Criterion:** Cross-Document Consistency
**Key Decision:** Legacy terms superseded — `terminalContract`/`terminalContracts` and node `terminal` bindings

The Workitem document at line 82 uses "terminal state" in the routing instruction context. While "terminal state" (meaning Completed/Failed) is distinct from the superseded "terminal" binding/contract terminology, the proximity could cause confusion. However, "terminal state" is standard state-machine vocabulary and is used consistently across the spec to mean "final, non-reversible state." This is not a violation.

RESOLVED — "terminal state" is standard state-machine vocabulary, distinct from the superseded "terminal" contract/binding terminology.

---

### 19. Missing Support Service CRD in concepts architecture — architecture document mentions Support Services but no CRD is mentioned

**Files:** `01-concepts/01-architecture.md:74`
**Criterion:** Completeness vs AGENTS.md
**Key Decision:** Flow Support Services are pluggable service containers

The architecture document mentions Support Services at line 74 but does not mention their CRD. This is appropriate for concepts — CRD details belong in `02-flow/`. No issue.

RESOLVED — concepts correctly stays high-level.

---

### 20. Feedback lifecycle diagram transition from rejected to wont_fix missing — rejected can only go to actioned or deadlocked

**Files:** `01-concepts/03-data-model.md:290-311`
**Criterion:** Technical Feasibility

The feedback lifecycle state diagram (lines 290-311) shows transitions from `rejected`: to `actioned` (via `ResolveFeedback()`) and to `deadlocked` (gate node detects excessive depth). The prose at line 341 says "A rejected item returns to the refining node for compliance — re-refusal is not permitted." This means from `rejected`, the only valid transition is to `actioned` (fix it) or `deadlocked` (gate node escalation). This is consistent. The diagram does not show `rejected -> wont_fix`, which matches the prose: re-refusal is not permitted.

RESOLVED — the diagram and prose are consistent.

---

### ~~21. Hearing Workitem creation — who creates the Workitem?~~

RESOLVED — Aligned Librarian TTL-expiry trigger (lines 75-76) and Citation Processor threshold trigger (line 94) in `02-flow/04-system-services.md` to use "request creation of a Workitem ... through the Operator" phrasing, matching the hearing lifecycle section at line 216. Services request; the Operator creates.

---

### 22. Missing cross-link: Foundry Cycle does not link to SDK on first mention

**Files:** `01-concepts/02-foundry-cycle.md:36`
**Criterion:** Writing Principles — Cross-link aggressively

Line 36 mentions "the SDK" with a link to `../04-sdk/01-sdk-core.md`. This is actually correct — it does link. No issue.

RESOLVED — link is present.

---

### ~~23. Concepts data model references "Codification Services" with link to system-services~~

RESOLVED — Replaced "Codification Services" with "specialised translation services" in `01-concepts/03-data-model.md:350`. Cross-link to system-services retained. Codification Services are Flow Support Services (optional, Flow-Architect-deployed), not system services, reinforcing their exclusion from concepts vocabulary.

---

### ~~24. Concepts overview mentions "Codification Services" with link to system-services~~

RESOLVED — Replaced "through Codification Services" with "through specialised translation services" in `01-concepts/00-overview.md:80`. Cross-link retained.

---

### ~~25. Concepts governance document mentions "Codification Services" in precedent section~~

RESOLVED — Replaced "including Codification Services that translate goals into formal representations" with "including specialised translation services that translate goals into formal representations" in `01-concepts/04-governance.md:73`. Cross-link retained.

---

### ~~26. Concepts overview links to Flow Monitor as `../02-flow/04-system-services.md#flow-monitor-and-friction-surface` — naming system service in concepts~~

RESOLVED — Flow Monitor is a system service (always present, built into the runtime), analogous to Librarian, Archivist, and Citation Processor which were accepted as internal component names in issue #27. Accepted as within the same exception.

---

### 27. Concepts architecture names specific system services: Librarian, Citation Processor, Archivist, Flow Monitor

**Files:** `01-concepts/01-architecture.md:68`, `01-concepts/01-architecture.md:90-91`
**Criterion:** Key Decisions Compliance — Concepts documents are technology-agnostic
**Key Decision:** Concepts documents are technology-agnostic

The architecture document names Librarian, Citation Processor, Archivist, and Flow Monitor throughout. These are Foundry Flow's own system service names — they are not product names or technology choices. The key decision's exclusion list is "SQLite, Prometheus, Helm, gRPC, Docker" and methodology names like "GitOps." The system service names are internal Foundry Flow component names, analogous to the Foundry Cycle node names (Forge, Sort, etc.) which are explicitly accepted in concepts. The architecture document is specifically about describing what each component does.

RESOLVED — Foundry Flow system service names (Librarian, Archivist, Citation Processor, Flow Monitor) are internal component names, not external product names. They are analogous to the accepted Foundry Cycle node names.

---

## Minor Issues

### ~~28. Concepts overview "Core Concepts" heading uses a listing pattern~~

RESOLVED — Rewritten the Core Concepts section in `01-concepts/00-overview.md` from glossary-style bold-term-em-dash-definition format to flowing prose paragraphs ("A **Flow** is...", "A **Workitem** is..."). Content unchanged; presentation is now natural prose.

---

### ~~29. Inconsistent capitalisation: "node" vs "Node" across documents~~

RESOLVED — Applied consistent convention: lowercase "node" for generic and compound-term usage (exit node, gate node, "a node that...", "every node"), capitalised only in CRD names (FoundryNode) and Mermaid diagram labels. Fixed 16 instances across `01-concepts/01-architecture.md`, 2 in `01-concepts/00-overview.md`, and 1 in `01-concepts/03-data-model.md`.

---

### ~~30. Concepts Foundry Cycle says Assay "does not write Tier 1 Findings" but concepts governance only says authority ceiling without restating this~~

RESOLVED — Added explicit Tier 1 row to the authority ceiling table in `01-concepts/04-governance.md`: "Assay does not write Tier 1 Findings. Tier 2 is both the floor and the ceiling of its judicial authority." Now consistent with the Foundry Cycle document's statement.

---

### 31. Mermaid flowchart in concepts overview uses `(( ))` for terminal nodes — rendering check

**Files:** `01-concepts/00-overview.md:147-148`
**Criterion:** Writing Principles — Show, don't scaffold

Lines 147-148 use `D1(( ))` for a done/terminal node in a flowchart. The `(( ))` syntax renders as a double circle in Mermaid, which is a valid terminal symbol. No issue with the syntax itself.

RESOLVED — valid Mermaid syntax.

---

### 32. Flow overview runtime loop diagram labels are brief but could cross-link more

**Files:** `02-flow/00-overview.md:59-83`
**Criterion:** Writing Principles — Cross-link aggressively

The runtime loop sequence diagram at lines 59-83 does not include any links (diagrams generally do not). The surrounding prose links are adequate. No issue.

RESOLVED — diagrams cannot contain markdown links; surrounding prose provides the links.

---

### 33. AGENTS.md status table lists `01-concepts/01-architecture.md` as "Complete" but review found technology-agnostic concerns

**Files:** `AGENTS.md:89`
**Criterion:** Completeness vs AGENTS.md

The architecture document is marked "Complete" in the status table. Issues #27 (system service names) was resolved as acceptable. However, the document's Hybrid Persistence table at line 162 correctly uses technology-agnostic terms. The document appears to meet the Complete standard.

RESOLVED — the document meets Complete quality bar.

---

---

## Cross-Document Consistency

### Terminology consistency

| Term | Expected usage | Observed | Status |
|------|---------------|----------|--------|
| Workitem | "Workitem" (one word, capitalised) | Consistent across all files | OK |
| artefact | British spelling "artefact" | Consistent across all files | OK |
| node/Node | Lowercase "node" for generic/compound usage | Consistent after fix | OK (issue #29) |
| Sidecar | Capitalised "Sidecar" | Consistent | OK |
| Flow Architect | Capitalised | Consistent | OK |
| Flow Operator / Operator | Capitalised | Consistent | OK |
| Archivist | Capitalised | Consistent | OK |
| Librarian | Capitalised | Consistent | OK |
| Citation Processor | Capitalised | Consistent | OK |
| Flow Monitor | Capitalised | Consistent | OK |
| Assay | Capitalised | Consistent | OK |
| GovernedArtefact | CRD name, correct casing | Consistent | OK |
| FoundryNode | CRD name | Consistent | OK |
| FoundryFlow | CRD name | Consistent | OK |
| WorkitemType | Correctly absent — superseded term | Not reintroduced anywhere | OK |
| spec.context | Correctly absent — superseded term | Not reintroduced anywhere | OK |
| entryNode | Correctly absent — superseded term | Not reintroduced; `importNode` used instead | OK |
| terminalContract | Correctly absent — superseded term | Not reintroduced; entry/exit contracts used | OK |
| behaviour | British spelling | Consistent | OK |
| organisation | British spelling | Consistent | OK |
| naturalisation | British spelling | Consistent | OK |

### Cross-link coverage

Cross-linking is generally strong across `01-concepts/` and `02-flow/`. Most concepts link to their detail pages on first mention. Specific gaps noted in issues #10, #11. The `03-node/`, `04-sdk/`, and `05-reference/` stubs correctly link to upstream documents where they reference concepts.

### Duplication

The spec has intentional repetition of key concepts (e.g., exit contract semantics appear in `01-concepts/03-data-model.md`, `02-flow/01-operator.md`, `02-flow/02-workitem.md`, and `02-flow/05-configuration.md`). In all cases, the descriptions are consistent — they use the same model (per-kind, stamp-name lists, empty list = presence-only, empty contract = no requirements). No contradictory duplications found.

The Foundry Cycle node descriptions in `01-concepts/02-foundry-cycle.md` and their mentions in `02-flow/03-nodes-external.md` and `02-flow/05-configuration.md` are consistent. The reference arrangement qualification is properly applied.

The runtime invariant lists at the end of each `02-flow/` document are intentionally overlapping — they capture invariants relevant to that document's scope. No contradictions across invariant lists.

---

## Summary

| Severity | Count | Issues |
|----------|-------|--------|
| **Critical** | 0 | ~~#2, #3~~ (resolved) |
| **Significant** | 0 | ~~#4, #6, #7, #8, #9, #10, #11, #13, #14, #15, #16, #17, #21, #23, #24, #25, #26~~ (all resolved) |
| **Minor** | 0 | ~~#28, #29, #30~~ (all resolved) |
| **Resolved (false positive)** | 10 | #1, #5, #12, #18, #19, #20, #22, #27, #31, #32, #33 |
| **Resolved (fixed)** | 18 | #2, #3, #4, #6, #7, #9, #11, #13, #14, #15, #16, #17, #21, #23, #24, #25, #28, #29, #30 |
| **Resolved (accepted)** | 3 | #8 (container images), #10 (link already present), #26 (Flow Monitor accepted) |

Note: Issues #23, #24, and #25 are the same violation (Codification Services named in concepts) across three files. Issues #8, #9, #13, #14, #15, and #16 are all instances of the same conceptual concern (technology-specific detail in concepts documents). These were addressed as editorial passes.

All issues from the original review are now resolved.
