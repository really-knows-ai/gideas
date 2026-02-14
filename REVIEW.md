# Spec Review

Deep review of all spec directories against AGENTS.md key decisions, writing principles, and cross-document consistency.

Reviewed directories: `01-concepts/`, `02-flow/`, `03-node/`, `04-sdk/`, `05-reference/`

Review discipline applied: full semantic and writing-principle review for Drafted/Complete files; structural correctness, key-decision alignment, impossible mechanics, and broken/missing links only for Stub outline files.

---

## Critical Issues

Issues that contradict key decisions or create internal inconsistencies. Must be fixed before proceeding.

### ~~1. Operator queries Archivist for exit-contract validation — unclear authority path~~ RESOLVED

**Files:** `02-flow/01-operator.md:145-148`, `02-flow/02-workitem.md:160-167`, `02-flow/04-system-services.md:214`
**Criterion:** Technical Feasibility
**Key Decision:** Workitem control-plane ownership and SDK boundary

The Operator is shown querying the Archivist directly for stamp and artefact state during exit-contract validation (sequence diagrams in both `01-operator.md` and `02-workitem.md` show `OP->>AR: query artefact state for bound contract`). The inter-service contracts section in `04-system-services.md:214` confirms "Operator <-> Archivist: completion validation queries and artefact presence checks." This is architecturally sound. However, the key decision on "Workitem control-plane ownership and SDK boundary" states that the artefact provenance flow is "Sidecar -> Archivist" and that "Archivist is the sole persistence authority for artefact provenance." The Operator-to-Archivist path for validation queries is never explicitly established as a platform mechanism in the concepts documents or the key decisions. This creates ambiguity: is this a direct service-to-service call, or does it go through some mediation layer?

**Suggested fix:** Add a brief clarifying statement in `02-flow/01-operator.md` (or `02-flow/04-system-services.md`) that the Operator has a direct service-level query path to the Archivist for contract validation purposes, distinct from the Sidecar-mediated node access path. This is an Operator-to-Archivist service boundary, not a node-to-Archivist boundary.

**Resolution:** Added comprehensive paragraph in `02-flow/01-operator.md` Role and Boundaries section establishing the full Operator-to-Archivist service boundary: contract validation (entry + exit), artefact presence checks, and retention/cleanup coordination. Links to inter-service contracts in System Services. The key decision's Sidecar mediation applies to nodes, not to the Operator.

---

### ~~2. Codification Services referenced but never defined~~ RESOLVED

**Files:** `01-concepts/00-overview.md:88`, `01-concepts/03-data-model.md:395`, `01-concepts/04-governance.md:75`, `02-flow/04-system-services.md` (entire file)
**Criterion:** Completeness vs AGENTS.md / Cross-Document Consistency
**Key Decision:** Laws are single objects with multiple representations

"Codification Services" is referenced in three concepts documents as the mechanism that translates a law's goal into new representations (e.g., prose to formal logic) during promotion. The AGENTS.md key decision on laws states: "A prose-only Tier 1 Finding can gain a formal logic representation when promoted to a Tier 2 Ruling via Codification Services." However, `02-flow/04-system-services.md` — the document that should define all system services — never mentions Codification Services. It is not listed in the service landscape, has no section, and has no inter-service contract. The governance doc (`01-concepts/04-governance.md:75`) says "Representation lifecycle responsibilities are defined in System Services" but System Services does not define them.

**Suggested fix:** Either (a) add a Codification Services section to `02-flow/04-system-services.md` defining it as a service or a Librarian sub-capability, or (b) fold codification into the Librarian's responsibilities in `02-flow/04-system-services.md` and update the concepts references to point to the Librarian rather than a standalone "Codification Services" name.

**Resolution:** Introduced Flow Support Services as a new architectural concept in AGENTS.md (key decisions), `01-concepts/01-architecture.md` (Data Plane, Responsibility Boundaries), `02-flow/04-system-services.md` (dedicated section with Codification Services subsection), `02-flow/05-configuration.md` (configuration authority model and Support Service configuration), `02-flow/01-operator.md` (reconciliation surface), `02-flow/00-overview.md` (runtime composition), `03-node/00-overview.md` (Runtime Interaction Model), `03-node/01-sidecar.md` (brokering contract), and `04-sdk/00-overview.md` (FlowSupportService base class). Codification Services are the first concrete instance, exposing an `encode` capability consumed by Assay during law promotion. Links in concepts documents updated to point to the new section.

---

### ~~3. Librarian cited as source of law query by Sidecar in gRPC API stub, but Citation Processor also receives Sidecar calls — unclear in service contract~~ RESOLVED

**Files:** `02-flow/04-system-services.md:217`, `02-flow/00-overview.md:30`
**Criterion:** Technical Feasibility / Cross-Document Consistency

The overview diagram (`02-flow/00-overview.md:30`) shows `SC --> CP` (Sidecar to Citation Processor). The inter-service contracts in `04-system-services.md:217` confirm "Sidecar <-> Citation Processor: citation submission and citation evidence query paths." However, the AGENTS.md key decision section does not mention a Sidecar-to-Citation-Processor path — it describes the Sidecar's role only in terms of routing to the Archivist for artefact provenance and to the Operator for control-plane mutations. Nodes submitting citations through the Sidecar to the Citation Processor is architecturally coherent, but the concepts documents (`01-concepts/01-architecture.md`) do not mention this data path in the Data Plane or Security Plane descriptions.

**Suggested fix:** In `01-concepts/01-architecture.md`, mention that the Sidecar also mediates citation submissions to the Citation Processor (alongside Archivist and Librarian calls), or ensure the Governance Plane section acknowledges this path. This is a completeness gap rather than a contradiction, but it could mislead implementors who read the concepts layer and assume the Sidecar's mediation scope is only Archivist + Librarian + Operator.

**Resolution:** Added Citation Processor and Flow Support Services to the Sidecar's service brokering contract in `03-node/01-sidecar.md`. The Sidecar stub now lists all five brokered paths: Operator, Archivist, Librarian, Citation Processor, and Flow Support Services. The concepts architecture document (`01-concepts/01-architecture.md`) was not changed for this specific issue since it describes planes at a higher abstraction level where the Sidecar's brokering detail is not enumerated.

---

## Significant Issues

### 4. Writing principle violation: meta-commentary in multiple documents

**Files:** `01-concepts/01-architecture.md:6`, `01-concepts/02-foundry-cycle.md:27`
**Criterion:** Writing Principles — No meta-commentary

`01-concepts/01-architecture.md:6`: "The internal structure separates into distinct planes, each owning a single concern." — This is structural narration telling the reader how the document is organised rather than letting the planes present themselves.

`01-concepts/02-foundry-cycle.md:27`: "Sort is the central routing hub. It evaluates governance state and routes. Granted the `READ:flow` capability, Sort reads the Flow configuration to discover which nodes can provide which stamps, then applies deliberately simple logic:" — The phrase "deliberately simple logic" is meta-commentary on the design rather than just presenting the logic.

**Suggested fix:** In `01-architecture.md:6`, remove or rephrase to avoid narrating the document structure. In `02-foundry-cycle.md:27`, replace "deliberately simple logic" with just the logic itself (e.g., "then applies its routing rules:").

---

### 5. Writing principle violation: planning voice in `01-concepts/00-overview.md`

**Files:** `01-concepts/00-overview.md:12`
**Criterion:** Writing Principles — No planning voice

The "Foundational Axioms" section at line 12 is presented as a titled block with four labelled axioms. While the axioms themselves are well-written, presenting them under a "Foundational Axioms" heading with bold-label enumeration is planning voice — it tells the reader "here are the axioms" rather than letting the principles emerge from the exposition. The AGENTS.md writing principle says: "Do not write 'these are the eight nouns' or 'four axioms underpin the system.' Let the content present itself."

**Suggested fix:** Consider integrating the axiom content into the surrounding narrative rather than presenting them as a labelled list. Alternatively, if the heading must remain, remove the bold-label format and weave each principle into flowing prose.

---

### 6. Cross-link gap: Appraise and Refine links in law tiers table point to overview, not foundry-cycle

**Files:** `01-concepts/03-data-model.md:403`
**Criterion:** Writing Principles — Cross-link aggressively / Cross-Document Consistency

The law tiers table at line 403 links Appraise and Refine to `./00-overview.md` but those roles are defined in `./02-foundry-cycle.md`. The overview mentions them briefly but `02-foundry-cycle.md` is the normative detail page.

**Suggested fix:** Change the links in the Tier 1 source column from `[Appraise](./00-overview.md)` and `[Refine](./00-overview.md)` to `[Appraise](./02-foundry-cycle.md#appraise-reviewer)` and `[Refine](./02-foundry-cycle.md#refine-refiner)`.

---

### 7. Technology-specific term "SQLite" appears in `02-flow/00-overview.md` and `02-flow/02-workitem.md`

**Files:** `02-flow/00-overview.md:120`, `02-flow/02-workitem.md:117`
**Criterion:** Cross-Document Consistency

These are `02-flow/` documents where technology-specific terms are appropriate per AGENTS.md ("Technology choices ... belong in `02-flow/`, `03-node/`, `04-sdk/`, and `05-reference/`"). This is not an issue — noting for completeness that the technology terms are correctly scoped to the flow layer.

**No fix required.** (This item is retained to show the review covered this area.)

---

### 8. `02-flow/01-operator.md` references Assay link to `./03-nodes-external.md` without anchor

**Files:** `02-flow/01-operator.md:162`
**Criterion:** Cross-Document Consistency — Cross-links

Line 162 references "[Assay](./03-nodes-external.md)" but the Assay section in that file has the heading "Assay as Standard Component" (anchor `#assay-as-standard-component`). The link works but is imprecise — it lands at the top of the file rather than the Assay section.

**Suggested fix:** Change to `[Assay](./03-nodes-external.md#assay-as-standard-component)`.

---

### 9. `01-concepts/04-governance.md` references "Assay Node" in overview paragraph without link

**Files:** `01-concepts/04-governance.md:1`
**Criterion:** Writing Principles — Cross-link aggressively

Line 1: "a judiciary that resolves disputes" — Assay is mentioned indirectly but not linked. The first explicit mention of Assay in this document is at line 13 in the table, which does link to the foundry-cycle anchor. However, the judiciary concept in the opening sentence could benefit from a link to Assay's detail page for readers entering from this document.

**Suggested fix:** Link "judiciary" in line 1 to `./02-foundry-cycle.md#assay-judiciary--standard-component` or similar.

---

### 10. Hearing Workitem creation — who creates the Workitem is inconsistent

**Files:** `02-flow/04-system-services.md:155-156`, `01-concepts/04-governance.md:30`, `01-concepts/04-governance.md:79`
**Criterion:** Cross-Document Consistency / Key Decisions Compliance
**Key Decision:** Review hearings run as standard Workitems

In `04-system-services.md:155`, step 1 says "Triggering service creates a Workitem for review-hearing processing." Lines 70-71 and 88-89 confirm that Librarian and Citation Processor own the trigger emission. But the step says "creates a Workitem" — does the triggering service create the Workitem CRD directly, or does it request creation through the Operator? The Operator is the authority for Workitem lifecycle. In `01-concepts/04-governance.md:30`, the language is "triggers creation of a Workitem for review-hearing processing, routed to the Assay node" and at line 79 it says "Librarian triggers creation of a Workitem for review-hearing processing." The word "triggers creation" vs "creates" is subtly different. If the triggering service directly creates the CRD, that bypasses Operator authority for Workitem lifecycle inception.

**Suggested fix:** Clarify in `02-flow/04-system-services.md` that the triggering service requests Workitem creation through the Operator (consistent with Operator being the authoritative engine for Workitem lifecycle transitions, per Operator invariant #1). The triggering service emits the trigger; the Operator creates and admits the Workitem.

---

### 11. GovernedArtefact CRD defines `requiredStamps` but contracts also define per-kind stamp requirements — potential overlap

**Files:** `01-concepts/03-data-model.md:172-191`, `01-concepts/03-data-model.md:84-124`
**Criterion:** Technical Feasibility / Cross-Document Consistency

The GovernedArtefact CRD (line 172-185) defines `requiredStamps` as the stamps an artefact must carry to be "valid." Entry and exit contracts (line 84-124) also define per-kind stamp requirements. The relationship between these two mechanisms is unclear. Does the GovernedArtefact's `requiredStamps` represent the *complete* set of stamps the artefact kind requires, while contracts select subsets? Or are they independent? The exit contract example at line 110-118 shows specific stamps for `petition-draft`, but the GovernedArtefact at line 173-185 shows a *different* set of stamps for the same kind (adds "legal-review"). This suggests contracts and GovernedArtefact requirements are independent, which raises the question: which one is the source of truth for exit validation?

**Suggested fix:** Add a clarifying paragraph in `01-concepts/03-data-model.md` (near the GovernedArtefact section or the contracts section) that explains the relationship. The most likely intended design: GovernedArtefact defines the *full validity standard* for the kind; contracts can specify a *subset* (or the full set) for boundary checks. Exit contracts enforce their own stated requirements, not GovernedArtefact's. GovernedArtefact serves as a governance reference and stamp definition surface. Make this explicit.

---

### 12. Friction aggregation operations — `01-concepts/04-governance.md` introduces logarithmic/additive/multiplicative but `01-concepts/00-overview.md` does not

**Files:** `01-concepts/04-governance.md:296`, `01-concepts/00-overview.md:146-165`
**Criterion:** Cross-Document Consistency

The overview introduces friction as "systemic heat" with a conceptual description and a diagram, but says nothing about aggregation operations. The governance document at line 296 adds detail about "magnitude and the aggregation operation — logarithmic, additive, or multiplicative" and "base friction cost for each Workitem." These details are first introduced in `01-concepts/04-governance.md` rather than in the overview's Friction section. The `02-flow/04-system-services.md:138` also mentions these aggregation operations. This creates a situation where the concepts-level friction description differs in detail between documents.

**Suggested fix:** Either add a brief mention of friction magnitude and aggregation operations to the overview's friction section (with a cross-link to the governance document for the full treatment), or ensure the overview explicitly defers to the governance document for the friction emission model. Currently the overview's friction section is self-contained but incomplete relative to what governance states.

---

### 13. `01-concepts/01-architecture.md` Hybrid Persistence table lists "Laws" in CRD layer

**Files:** `01-concepts/01-architecture.md:165`
**Criterion:** Key Decisions Compliance / Concepts are technology-agnostic

The Hybrid Persistence table at line 165 lists "Laws" under the CRD storage layer alongside Workitems. This is correct — laws are CRDs in the runtime. However, the table also names "Embedded database — Librarian" and "Embedded database — Citation Processor" for their respective storage, which are technology-agnostic terms appropriate for concepts. The table itself names specific storage patterns ("CRDs", "Embedded database", "Content-addressed store", "Metrics pipeline") which are technology-agnostic but arguably at the edge — "CRDs" is accepted Kubernetes vocabulary per AGENTS.md exception, and the rest are generic patterns. No issue here.

**No fix required.** Confirmed compliant with technology-agnostic exception for Kubernetes vocabulary.

---

### 14. Writing principle violation: "Show, don't scaffold" — announced tables

**Files:** `01-concepts/01-architecture.md:163`
**Criterion:** Writing Principles — Show, don't scaffold

Line 163: "State is split across storage layers, each chosen for its access pattern." followed immediately by a table. This is a mild announcement of the table. The principle says tables should "feel like natural parts of the explanation, not bolted-on visual aids announced by a sentence."

**Suggested fix:** Integrate the introductory sentence into the table context or remove the announcement and let the section heading and table speak for themselves.

---

### 15. Missing cross-link: `01-concepts/00-overview.md` references Flow Monitor without linking

**Files:** `01-concepts/00-overview.md:165`
**Criterion:** Writing Principles — Cross-link aggressively

Line 165: "The Flow Monitor aggregates friction data" — links to `../02-flow/04-system-services.md` which is correct. However, the very first mention of "Flow Monitor" in this file is contextualised through the Friction section. Earlier in the document (line 29), "Node" links to `../03-node/00-overview.md`. The Flow Monitor link at line 165 is the first mention and is linked, which is good. No issue.

**No fix required.** First mention is linked.

---

### 16. `02-flow/06-cross-flow.md` duplicates Tier 3 conflict status-quo language differently from `01-concepts/04-governance.md`

**Files:** `02-flow/06-cross-flow.md:146`, `01-concepts/04-governance.md:108`
**Criterion:** Cross-Document Consistency

`01-concepts/04-governance.md:108` says for Tier 3 vs Tier 3: "On rejection, the conflict persists — every future Workitem that hits the same conflict generates another HITL escalation and more friction until the humans act."

`02-flow/06-cross-flow.md:146` says: "If rejected, status quo remains and the same unresolved conflict condition escalates again only when later Workitems encounter it."

The concepts version says "every future Workitem that hits the same conflict generates another HITL escalation" (implying automatic per-Workitem escalation). The flow version says "escalates again only when later Workitems encounter it" (same meaning but less emphatic). These are compatible but use different framing. The concepts version is stronger and more specific.

**Suggested fix:** Align the language in `02-flow/06-cross-flow.md:146` to match the concepts version more closely, or cross-link to the governance document for the authoritative description.

---

### 17. Assay "does not write Tier 1 Findings" — stated in key decisions but not consistently reflected

**Files:** `01-concepts/02-foundry-cycle.md:92`, `02-flow/03-nodes-external.md:94`
**Criterion:** Key Decisions Compliance
**Key Decision:** Escalation paths and Assay's authority ceiling

AGENTS.md states: "It does not write Tier 1 Findings." `02-foundry-cycle.md:92` says "Assay alone mints Tier 2 Rulings" but does not explicitly state Assay cannot write Findings. `03-nodes-external.md:94` states "Assay does not write Tier 1 findings." The concepts document `04-governance.md` does not explicitly state this constraint. While the authority ceiling table in `04-governance.md:115-119` implicitly excludes Tier 1 (Assay's authority starts at Tier 2), the explicit prohibition from the key decision is worth stating once in the concepts layer for clarity.

**Suggested fix:** Add a brief note in `01-concepts/02-foundry-cycle.md` Assay section (around line 46) that Assay does not write Tier 1 Findings — it only mints Tier 2 Rulings.

---

### 18. `01-concepts/03-data-model.md` feedback lifecycle: `rejected -> wont_fix` transition missing from state diagram

**Files:** `01-concepts/03-data-model.md:304`
**Criterion:** Key Decisions Compliance / Technical Feasibility

The feedback lifecycle state machine diagram (lines 291-311) does not show a `rejected -> wont_fix` transition. However, the transition table at lines 322-336 also does not list `rejected -> wont_fix` as a permitted transition. Line 341 states: "A rejected item returns to the refining node for compliance — re-refusal is not permitted." This is consistent — once rejected, the refining node must comply (move to `actioned`), not re-refuse. The state diagram and table agree. No issue here.

**No fix required.** Diagram and table are consistent with the intended design.

---

### 19. Mermaid line break check — `\n` usage in `stateDiagram-v2` blocks

**Files:** `01-concepts/03-data-model.md:36`, `01-concepts/03-data-model.md:291-311`
**Criterion:** Writing Principles — Mermaid line breaks

`stateDiagram-v2` blocks use `\n` for line breaks at lines 36 and 291-311. Per AGENTS.md: "`stateDiagram-v2` handles `\n` natively and is the exception." These are compliant.

Checked all `flowchart` and `sequenceDiagram` blocks across the spec — they use `<br/>` correctly. No violations found.

**No fix required.** All Mermaid line break usage is correct.

---

### 20. `02-flow/04-system-services.md` mentions "Operator <-> Librarian: law lifecycle events, hearing Workitem creation coordination" — Operator creates hearing Workitems?

**Files:** `02-flow/04-system-services.md:213`
**Criterion:** Cross-Document Consistency

This echoes issue #10. The inter-service contract at line 213 says "Operator <-> Librarian: law lifecycle events, hearing Workitem creation coordination." This implies the Operator and Librarian coordinate on hearing Workitem creation, supporting the interpretation that the triggering service requests creation through the Operator. But the hearing lifecycle at line 155 says "Triggering service creates a Workitem" — present-tense active voice implying the service does it directly.

**Suggested fix:** Covered by fix for issue #10.

---

### 21. `01-concepts/00-overview.md` Stamps section is orphaned from cross-link target

**Files:** `01-concepts/00-overview.md:35-46`, `01-concepts/03-data-model.md:195`
**Criterion:** Cross-Document Consistency — Cross-links

The overview defines stamps at lines 35-46 and uses `### Stamps` as the heading. The data model document links to stamps via `#passports-and-stamps`. The overview's "Passport" definition at line 33 links to `./03-data-model.md#passports-and-stamps`. However, within the overview itself, the "Passports and Stamps" section at line 107 links to `[stamps](#stamps)` which resolves within the same document. This is all consistent. No issue.

**No fix required.**

---

### 22. `01-concepts/04-governance.md` "Friction as Governance Signal" section introduces friction emission detail that overlaps SDK Telemetry

**Files:** `01-concepts/04-governance.md:296-300`, `04-sdk/06-sdk-telemetry.md:13`
**Criterion:** Cross-Document Consistency — Duplication

`04-governance.md:296` describes friction emission with magnitude, aggregation operations (logarithmic, additive, multiplicative), and base friction cost per node per Workitem. `04-sdk/06-sdk-telemetry.md:13` (stub outline) repeats the same detail in its friction emission contract description. `02-flow/04-system-services.md:138` also describes these. The detail is consistent across all three but duplicated. When the SDK telemetry document is fully drafted, this triplication needs to be resolved into a single source of truth with cross-links.

**Suggested fix:** When `04-sdk/06-sdk-telemetry.md` is drafted, establish the normative friction emission contract in one location (likely `02-flow/04-system-services.md` or `04-sdk/06-sdk-telemetry.md`) and have others cross-link. For now, the concepts document should describe friction conceptually and defer emission mechanics to the flow/SDK layers. The current level of detail in `04-governance.md:296` (magnitude, aggregation operations) may be too implementation-specific for a concepts document.

---

### 23. Writing principle violation: British spelling — "artifact" not found, but "organisation" confirmed

**Files:** All spec documents
**Criterion:** Writing Principles — British spelling

Checked across all documents: "artefact" is used consistently (not "artifact"). "Organisation" is used consistently. "behaviour" is used consistently. "naturalisation" is used consistently. No British spelling violations found.

**No fix required.**

---

### 24. `01-concepts/03-data-model.md` feedback state token table uses `wont_fix` — consistent but display label "Won't Fix" needs careful treatment

**Files:** `01-concepts/03-data-model.md:283`, `01-concepts/03-data-model.md:317`, `01-concepts/03-data-model.md:341`, `01-concepts/03-data-model.md:351`
**Criterion:** Cross-Document Consistency — Terminology

The document defines the canonical token as `wont_fix` and the display label as "Won't Fix" at line 283. This distinction is maintained consistently throughout the document — every mention of the `wont_fix` state in the narrative uses the backtick-quoted canonical token or explicitly notes "(display label 'Won't Fix')". This is well done. No issue.

**No fix required.**

---

## Minor Issues

### 25. `02-flow/00-overview.md` "Reference Arrangement and Topology Freedom" section uses quotes around node names

**Files:** `02-flow/00-overview.md:84`
**Criterion:** Writing Principles — Cross-Document Consistency

Line 84: '"Forge", "Sort", or "Refine" describe standard responsibilities' — uses double quotes. Throughout the rest of the spec, reference arrangement node names are not quoted but sometimes italicised or just named. Minor inconsistency in formatting convention.

**Suggested fix:** Remove quotes and use the node names directly, consistent with usage elsewhere in the spec.

---

### 26. `01-concepts/01-architecture.md` Federation Plane uses "bilateral" for Treaties but could link to detail

**Files:** `01-concepts/01-architecture.md:104`
**Criterion:** Writing Principles — Cross-link aggressively

Line 104 describes Treaties in detail but does not link "Treaty" to `../02-flow/06-cross-flow.md` on first mention. The link appears later at line 106 ("Details of the export-import protocol are covered in Cross-Flow Collaboration"), but the first mention of "Treaty" at line 104 is unlinked.

**Suggested fix:** Link "Treaty" on first mention at line 104 to `../02-flow/06-cross-flow.md`.

---

### 27. `01-concepts/04-governance.md` references "annexation" in Operator trust responsibilities

**Files:** `01-concepts/04-governance.md:147`, `02-flow/01-operator.md:171`
**Criterion:** Cross-Document Consistency — Terminology

`04-governance.md:147`: "Operator-level onboarding, key management, and certificate lifecycle details are covered in Flow Operator." `02-flow/01-operator.md:171`: "Under a Governance Flow, Operator participates in annexation and receives intermediate authority anchored to the shared State Root." The term "annexation" appears only in `01-operator.md` and nowhere else in the spec. It is not defined in the glossary stub and is not used in any other document. This is a terminology orphan.

**Suggested fix:** Either define "annexation" in the glossary (when drafted) and use it consistently, or replace it with a more descriptive term like "onboarding" or "enrolment" that matches the language in `04-governance.md`.

---

### 28. `02-flow/00-overview.md` Governance Runtime Mechanics section mentions "Assay authority is bounded: resolve Tier 1-2"

**Files:** `02-flow/00-overview.md:97`
**Criterion:** Key Decisions Compliance
**Key Decision:** Escalation paths and Assay's authority ceiling

Line 97 says "resolve Tier 1-2" which could be read as "resolve Tier 1 and Tier 2 conflicts." The key decision states Assay can resolve at Tier 2 by minting Rulings and that it does not write Tier 1 Findings. "Resolve Tier 1-2" could misleadingly imply Assay writes Tier 1 laws. The authority ceiling in `04-governance.md:115-119` correctly shows Tier 2 as the resolve tier. The wording here is technically correct (Assay resolves *conflicts involving* Tier 1-2 laws) but ambiguous.

**Suggested fix:** Change "resolve Tier 1-2" to "resolve conflicts at Tier 1-2 by minting Tier 2 Rulings" for precision.

---

### 29. `03-node/00-overview.md` references "WorkitemType" and "spec.type" in invariant #6

**Files:** `03-node/00-overview.md:99`
**Criterion:** Cross-Document Consistency

Line 99: "Workitems do not use `WorkitemType`, `spec.type`, or a freeform context bag." This is correctly stated as an invariant. It appears in multiple documents (`02-flow/02-workitem.md:171-175`, `02-flow/05-configuration.md:94`, `03-node/02-configuration.md:67`). While slightly repetitive, this is appropriate for invariant lists that each document independently asserts. No issue.

**No fix required.** Repetition in invariant lists is intentional.

---

### 30. `01-concepts/00-overview.md` exit contract section references GovernedArtefact CRD and FoundryNode CRD

**Files:** `01-concepts/00-overview.md:112`
**Criterion:** Key Decisions Compliance — Concepts documents are technology-agnostic

Line 112: "The Flow grants nodes permission to apply specific named stamps via the FoundryNode CRD's capabilities." CRD is accepted Kubernetes vocabulary per the AGENTS.md exception. "FoundryNode CRD" is a specific CRD name. The AGENTS.md exception covers "CRD" as foundational vocabulary, and the GovernedArtefact CRD is used at line 44 as well. These are borderline — CRD names like "GovernedArtefact" and "FoundryNode" are Foundry Flow's own CRDs, not generic Kubernetes concepts. However, since CRDs are the defining configuration mechanism of the platform and the names are integral to understanding the system, this is defensible.

**Suggested fix:** Consider whether CRD-specific names (FoundryNode, GovernedArtefact, FoundryFlow) should be mentioned by name in concepts or described generically (e.g., "the node configuration resource" or "the artefact governance configuration"). Current usage is not wrong but pushes against the technology-agnostic boundary.

---

### 31. `01-concepts/00-overview.md` sequence diagram at line 114 shows Quench stamping "linter" but Quench is described as deterministic validator, not stamper

**Files:** `01-concepts/00-overview.md:126`
**Criterion:** Cross-Document Consistency

The sequence diagram shows `Q->>W: stamp (linter)` — Quench applying a "linter" stamp. `01-concepts/02-foundry-cycle.md:19` describes Quench as performing deterministic validation. There is no explicit statement that Quench stamps artefacts, but the reference arrangement could grant Quench a stamp capability. This is a valid illustration — Quench running a linter and applying the "linter" stamp is a reasonable reference-arrangement configuration. However, it is not explicitly discussed in the Quench role description.

**Suggested fix:** Either add a brief note in `02-foundry-cycle.md` Quench section that in the reference arrangement, Quench may apply deterministic validation stamps (e.g., "linter"), or adjust the diagram to show the stamp being applied by a different node. The current diagram is not wrong but is unexplained.

---

---

## Cross-Document Consistency

### Terminology consistency

| Term | Usage | Status |
|------|-------|--------|
| Workitem | Capitalised, one word throughout | Consistent |
| artefact | British spelling throughout | Consistent |
| behaviour | British spelling throughout | Consistent |
| organisation | British spelling throughout | Consistent |
| naturalisation | British spelling throughout | Consistent |
| Foundry Cycle | Capitalised, two words | Consistent |
| Flow Architect | Capitalised | Consistent |
| Sidecar | Capitalised when referring to the component | Consistent |
| Archivist | Capitalised | Consistent |
| Librarian | Capitalised | Consistent |
| Citation Processor | Capitalised, two words | Consistent |
| Flow Monitor | Capitalised, two words | Consistent |
| Assay | Capitalised, no "Node" suffix except in some places | Mostly consistent; occasionally "Assay Node" vs "Assay" |
| `wont_fix` | Canonical token with display label "Won't Fix" | Consistent |
| annexation | Used only in `02-flow/01-operator.md` | Orphan term (see #27) |
| Codification Services | Capitalised, used in concepts | Defined in flow layer as Flow Support Service specialisation (~~see #2~~) |
| GovernedArtefact | CRD name used in concepts and flow | Consistent but borderline for concepts (see #30) |

### Cross-link coverage

Cross-linking is generally thorough across the drafted documents. Key gaps:

- ~~Codification Services is referenced but has no target page or section (#2)~~ Resolved
- Treaty first mention in architecture doc is unlinked (#26)
- Appraise/Refine in data-model law tiers table link to overview instead of foundry-cycle (#6)

All stub outline documents appropriately link to their peer documents and reference sections.

### Duplication

Duplication is well-managed. The primary concern is:

- Friction emission mechanics (aggregation operations, base cost) appear in three places (#22)
- Exit contract semantics are restated across multiple flow documents — this is intentional per the invariant-list pattern and is consistent
- Hearing lifecycle is described in both concepts and system services — descriptions agree

---

## Summary

| Severity | Count | Issues |
|----------|-------|--------|
| **Critical** | 3 (all resolved) | ~~#1~~, ~~#2~~, ~~#3~~ |
| **Significant** | 9 | #4, #5, #6, #10, #11, #12, #16, #17, #22 |
| **Minor** | 5 | #25, #26, #27, #28, #30, #31 |
| **No fix required** | 7 | #7, #13, #15, #18, #19, #21, #23, #24, #29 |
