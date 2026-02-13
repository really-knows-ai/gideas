# Spec Review

Deep review of all spec directories against AGENTS.md key decisions, writing principles, and cross-document consistency.

Reviewed directories: `01-concepts/`, `02-flow/`, `03-node/`, `04-sdk/`, `05-reference/`

---

## Critical Issues

Issues that contradict key decisions or create internal inconsistencies. Must be fixed before proceeding.

### ~~1. Feedback lifecycle missing transition: `rejected` to `wont_fix` (re-refusal)~~ RESOLVED

**Files:** `01-concepts/03-data-model.md:339`
**Criterion:** Cross-Document Consistency / Technical Feasibility

The prose at line 339 states: "A rejected item returns to the refining node for compliance — re-refusal is not permitted." However, the state diagram at lines 290-311 does not show any transition from `rejected` back to `wont_fix`. This is consistent — but then the prose at line 339 is the *only* place this prohibition is stated, and it is buried in a paragraph. Meanwhile, the transition table at lines 322-336 only shows `rejected -> actioned` and `rejected -> deadlocked`, which correctly omits `rejected -> wont_fix`. The prohibition is important but never explicitly called out in the transition table's guard conditions, which could lead implementors to miss it.

**Suggested fix:** Add an explicit row to the transition table or a note after it stating: "`rejected -> wont_fix` is prohibited — the refining node must comply with the rejection, not re-refuse." This makes the guard condition explicit in the normative table, not just in prose.

**Resolution:** Added a sentence after the transition table: "These are the only permitted transitions. The Archivist rejects any state change not listed above." This makes the table explicitly exhaustive rather than listing individual prohibitions.

---

### ~~2. Assay link to wrong anchor in `01-concepts/00-overview.md`~~ RESOLVED

**Files:** `01-concepts/01-architecture.md:92`
**Criterion:** Cross-Document Consistency

Line 92 links Assay to `(./00-overview.md)` without an anchor. The overview document does not have a dedicated Assay section heading — Assay is mentioned inline in the Foundry Cycle section and links out to `02-foundry-cycle.md`. The reader clicking this link lands on the full overview page with no guidance on where to find Assay's definition. The canonical Assay definition is in `01-concepts/02-foundry-cycle.md#assay-judiciary--standard-component`.

**Suggested fix:** Change `[Assay](./00-overview.md)` to `[Assay](./02-foundry-cycle.md#assay-judiciary--standard-component)` on line 92 of `01-concepts/01-architecture.md`. Audit all other Assay links in `01-concepts/` for the same issue.

**Resolution:** Changed `[Assay Node](./00-overview.md)` to `[Assay Node](./02-foundry-cycle.md#assay-judiciary--standard-component)` on line 92. No other Assay links in this file target the wrong anchor.

---

### ~~3. `02-flow/03-nodes-external.md` diagram inconsistent with Sort decision order~~ RESOLVED

**Files:** `02-flow/03-nodes-external.md:87-95`
**Criterion:** Key Decisions Compliance
**Key Decision:** Sort stamps approval / Sort's decision order

The flowchart on lines 87-95 shows `Forge -> Quench -> Sort -> Appraise -> Sort`, with Sort routing to Refine on unresolved and to Assay on deadlock. This implies artefacts always pass through Appraise after Sort before returning to Sort — but Sort's decision order (key decision) says Sort first checks for unresolved feedback, then deadlock, then missing stamps, then completion. The diagram makes it look like Appraise is always visited before Sort can make any routing decision, which misrepresents the reference arrangement. The correct flow is: Forge -> Quench -> Sort, then Sort routes to Appraise (for missing stamps), Refine (for unresolved feedback), or Assay (for deadlock).

**Suggested fix:** Revise the diagram to match the canonical cycle topology in `01-concepts/02-foundry-cycle.md:52-66`. The Sort node should be the central routing hub with conditional edges to Appraise, Refine, and Assay — not a linear path through Appraise.

**Resolution:** Removed the duplicated "Reference Arrangement Responsibilities" section (including the incorrect diagram) and "Sort as Reference Gate" section entirely. Replaced with a brief "Reference Arrangement" section that cross-links to `01-concepts/02-foundry-cycle.md` for the canonical topology and retains only platform-mechanism notes (configuration-discovery, deadlock special-casing, `approval` as convention). Also fixed the Assay link in the "Assay as Standard Component" section (was targeting `00-overview.md`, now targets `02-foundry-cycle.md#assay-judiciary--standard-component`).

---

### ~~4. Friction emission aggregation operations not defined as a key concept~~ RESOLVED

**Files:** `01-concepts/04-governance.md:296`
**Criterion:** Key Decisions Compliance / Cross-Document Consistency
**Key Decision:** Friction is systemic heat

Line 296 introduces aggregation operations for friction — "logarithmic, additive, or multiplicative" — as node-chosen parameters. This is the only place in the entire spec where these aggregation operations are defined. The key decision in AGENTS.md says "Friction is systemic heat" and "the Friction Ledger tracks it and tags it to source," but does not mention aggregation operations. This concept appears in `01-concepts/04-governance.md` (a concepts document) but never surfaces in `02-flow/04-system-services.md` (Flow Monitor / Friction Surface) or `04-sdk/06-sdk-telemetry.md` (where the friction emission API would need to support it).

If this is a decided design, it needs to propagate to the Flow and SDK layers. If it is speculative, it should be removed from the concepts document to avoid creating an obligation that downstream documents do not fulfil.

**Suggested fix:** Either (a) add aggregation operation semantics to `02-flow/04-system-services.md` and the SDK telemetry stub scope, or (b) simplify the concepts prose to describe friction as a quantitative signal without specifying aggregation modes, deferring the operational detail to `02-flow/`.

**Resolution:** Propagated aggregation operations to downstream documents. Added magnitude/aggregation-operation detail and multi-scope aggregation to `02-flow/04-system-services.md` (Flow Monitor friction section). Added aggregation operation scope note to `04-sdk/06-sdk-telemetry.md` (Friction Emission Contract stub). Concepts prose retained as-is.

---

### ~~5. `01-concepts/04-governance.md` uses technology-specific terms~~ RESOLVED

**Files:** `01-concepts/04-governance.md:43`
**Criterion:** Key Decisions Compliance
**Key Decision:** Concepts documents are technology-agnostic

Line 43 references "Law CRDs" — using "CRD" in a concepts document. The key decision states that concepts documents should say "embedded database", "content-addressed store", etc. rather than naming specific technologies. "CRD" is explicitly accepted as Kubernetes platform vocabulary per the exception, but "Law CRDs" refers to a specific implementation detail (laws as CRDs) rather than the platform concept of CRDs. The phrase "CRDs applied by an administrator" at line 43 and "Law CRD" at line 43 should be reframed as configuration resources.

**Suggested fix:** Replace "Law CRDs applied by an administrator" with "law configuration resources applied by an administrator" or "laws applied as CRDs by an administrator" — keeping "CRD" as platform vocabulary but not implying the law *is* a CRD type name. The same pattern appears at lines 97 (04-governance.md) — "Retired laws are deleted as CRDs" — which should say "Retired laws are deleted. The full history is preserved in the audit log." (removing the CRD reference).

**Resolution:** Fixed three occurrences. Line 43: "Law CRDs" changed to "laws" (twice). Line 97: "Retired laws are deleted as CRDs" changed to "Retired laws are deleted." Line 209: "The CRD is deleted" changed to "The local law is retired." All now use technology-agnostic language consistent with the concepts document standard.

---

## Significant Issues

### ~~6. Multiple Assay links target wrong or ambiguous anchors across `01-concepts/`~~ RESOLVED

**Files:** `01-concepts/04-governance.md:49`, `01-concepts/04-governance.md:30`, `01-concepts/03-data-model.md:82`, `01-concepts/03-data-model.md:341`, `01-concepts/03-data-model.md:360`, `01-concepts/03-data-model.md:366`
**Criterion:** Writing Principles (cross-link aggressively)

Multiple files in `01-concepts/` link Assay to `(./00-overview.md)` without an anchor. The overview has no Assay heading. The canonical Assay description is in `02-foundry-cycle.md#assay-judiciary--standard-component`. This creates a pattern of broken-intent links across the concepts section.

**Suggested fix:** Audit all `[Assay](./00-overview.md)` links in `01-concepts/` and replace with `[Assay](./02-foundry-cycle.md#assay-judiciary--standard-component)`.

**Resolution:** Replaced all 10 occurrences of `[Assay](./00-overview.md)` across `01-concepts/04-governance.md` (6 instances) and `01-concepts/03-data-model.md` (4 instances) with `[Assay](./02-foundry-cycle.md#assay-judiciary--standard-component)`.

---

### ~~7. Meta-commentary in `02-flow/00-overview.md` opening paragraph~~ RESOLVED

**Files:** `02-flow/00-overview.md:3-5`
**Criterion:** Writing Principles (no meta-commentary)

Lines 3-5 read: "Foundry Flow runtime for operators and platform administrators is defined by component boundaries, the execution loop, and non-negotiable behaviour invariants. Conceptual foundations remain in [Conceptual Overview]... `02-flow/` is the platform specification for operating a Flow."

This is structural narration — telling the reader what the document is and where other content lives rather than presenting information directly. The second sentence ("Conceptual foundations remain in...") is navigational meta-commentary.

**Suggested fix:** Remove the meta-commentary and start with the Runtime Composition section directly. The cross-links can appear naturally in context rather than as a preamble.

**Resolution:** Removed both meta-commentary paragraphs from `02-flow/00-overview.md`. Retained a clean definitional opening sentence. Applied same fix across all 8 `02-flow/` documents (see issues #8, #9).

---

### ~~8. Meta-commentary in `02-flow/01-operator.md` opening paragraph~~ RESOLVED

**Files:** `02-flow/01-operator.md:3-5`
**Criterion:** Writing Principles (no meta-commentary)

Lines 3-5: "Operator behaviour is grounded in [Architecture]... Operator semantics align with [Flow Runtime Overview]..." This is navigational scaffolding. Every `02-flow/` document opens with a similar alignment statement. These are meta-commentary about where to find related content rather than presenting the Operator's role.

**Suggested fix:** Remove the alignment preambles. Cross-links should appear on first mention of each concept within the document body, not as a navigational header.

**Resolution:** Removed alignment paragraph. Retained the opening definitional sentence with a natural cross-link to Workitems on first mention.

---

### ~~9. Repeated meta-commentary preambles across all `02-flow/` documents~~ RESOLVED

**Files:** `02-flow/02-workitem.md:3-5`, `02-flow/03-nodes-external.md:3-5`, `02-flow/04-system-services.md:3-5`, `02-flow/05-configuration.md:3-5`, `02-flow/06-cross-flow.md:3-5`, `02-flow/07-operations.md:3-5`
**Criterion:** Writing Principles (no meta-commentary)

Every `02-flow/` document opens with a "semantics align with" preamble listing cross-references. This is a consistent pattern of meta-commentary that violates the "no meta-commentary" writing principle. These preambles narrate the document's relationship to other documents rather than presenting content.

**Suggested fix:** Remove the alignment preamble from each document. Introduce cross-links naturally within the document body on first mention of each concept.

**Resolution:** Removed alignment paragraphs from all 6 remaining `02-flow/` documents (02-workitem, 03-nodes-external, 04-system-services, 05-configuration, 06-cross-flow, 07-operations). Each retains a clean definitional opening with natural cross-links on first mention where appropriate.

---

### ~~10. `03-node/00-overview.md` uses "Goal" heading — planning voice~~ RESOLVED

**Files:** `03-node/00-overview.md:3`
**Criterion:** Writing Principles (no planning voice)

The document opens with `## Goal` followed by a description of the document's purpose. This is planning voice — "Node runtime semantics define how Nodes execute..." reads as a document objective statement, not as specification prose. The `01-concepts/` and `02-flow/` drafted documents do not use this pattern.

**Suggested fix:** Remove the "Goal" heading and its content. Start with the "Node Runtime Boundary" section, which is where the actual specification begins. The opening sentence of the Goal section can be reworked into an introductory paragraph if needed. (Note: this is valid feedback because `03-node/00-overview.md` is marked as "Drafted" in the status table, not "Stub outline".)

**Resolution:** Removed the "Goal" heading. Reworked the definitional sentence into a direct opening paragraph. Removed the navigational meta-commentary line.

---

### ~~11. `03-node/01-sidecar.md` uses "Goal" heading and bullet-list-only format — planning voice in a stub~~ RESOLVED

**Files:** `03-node/01-sidecar.md:3-8`
**Criterion:** Writing Principles (no planning voice)
**Status note:** This file is marked "Stub outline" in AGENTS.md. Per review discipline, feedback is limited to structural correctness, key-decision alignment, impossible mechanics, and broken/missing links. The "Goal" heading is structural scaffolding appropriate for a stub, so this issue is limited to noting that the heading should be removed when the document is drafted.

**Suggested fix:** When drafting, remove the "Goal" heading and present content directly. No action needed while in stub status.

**Resolution:** No change needed — stub outline. The "Goal" heading is acceptable scaffolding that will be removed when the document is drafted.

---

### ~~12. Concepts documents reference `02-flow/04-system-services.md` for technology details but sometimes leak technology~~ RESOLVED

**Files:** `01-concepts/03-data-model.md:130`, `01-concepts/03-data-model.md:138`, `01-concepts/03-data-model.md:197`
**Criterion:** Key Decisions Compliance
**Key Decision:** Concepts documents are technology-agnostic

Line 130 says "Archivist's database" (acceptable — describes function, not product). Line 138 says "Sidecar computes the hash" (acceptable — Sidecar is a runtime concept). Line 197 says "Archivist's database" and "scoped to Workitem ID and artefact `id`" — these are acceptable. However, line 168 says `StoreArtefact()` — a specific API method name. This is a technology detail (SDK method name) that belongs in `04-sdk/` rather than `01-concepts/`.

**Suggested fix:** Replace `StoreArtefact()` with a descriptive phrase like "stores the content as a governed artefact in the Workitem" or similar technology-agnostic description.

**Resolution:** Replaced `StoreArtefact()` on line 168 with "stores content as a governed artefact in the Workitem".

---

### ~~13. Missing cross-link: `01-concepts/00-overview.md` references Archivist without linking to the concepts data model~~ RESOLVED

**Files:** `01-concepts/00-overview.md:27`
**Criterion:** Writing Principles (cross-link aggressively)

Line 27 links Archivist directly to `../02-flow/04-system-services.md`. This is a cross-section jump from concepts to flow — the reader is sent to an operator-audience document. The Archivist's role as artefact lifecycle manager is described in `01-concepts/03-data-model.md#artefacts` and `01-concepts/01-architecture.md`. The concepts-level first mention should link to a concepts-level description, with the flow-level detail linked from there.

**Suggested fix:** Change the Archivist link on line 27 to point to `./03-data-model.md#artefacts` or `./01-architecture.md` (Data Plane section) where the Archivist's role is described at the concepts level.

**Resolution:** Changed the Archivist link from `../02-flow/04-system-services.md` to `./03-data-model.md#artefacts`.

---

### ~~14. `01-concepts/03-data-model.md` — "Fatigue" row in Thrash Guard table links to wrong anchor~~ RESOLVED

**Files:** `01-concepts/03-data-model.md:82`
**Criterion:** Cross-Document Consistency

Line 82: the "Fatigue" row's Response column says "Escalate to [Assay](./00-overview.md)". The overview has no Assay anchor. The canonical Assay definition is in `02-foundry-cycle.md#assay-judiciary--standard-component`.

**Suggested fix:** Change to `[Assay](./02-foundry-cycle.md#assay-judiciary--standard-component)`.

**Resolution:** Fixed as part of issue #6's bulk replacement of all `[Assay](./00-overview.md)` links in `01-concepts/03-data-model.md`.

---

### ~~15. `01-concepts/03-data-model.md` — `FoundryNode` link on line 69 goes to `02-flow/03-nodes-external.md`~~ RESOLVED

**Files:** `01-concepts/03-data-model.md:69`
**Criterion:** Key Decisions Compliance
**Key Decision:** Concepts documents are technology-agnostic

Line 69 links `FoundryNode` to `../02-flow/03-nodes-external.md`. The concepts document uses the CRD resource name `FoundryNode` directly — this is acceptable as Kubernetes platform vocabulary. However, this creates a concepts-to-flow cross-section link for a routine reference. Consider linking to the CRD reference or keeping the link but noting it is a forward reference.

**Suggested fix:** The link target is acceptable (it is the closest normative description of node configuration at the flow level). No change required, but note for consistency that other CRD references in concepts link to `05-reference/crds.md`.

**Resolution:** No change needed. The link target is the closest normative description and the usage is consistent with Kubernetes platform vocabulary exceptions.

---

### ~~16. `01-concepts/04-governance.md` — "Retired laws are deleted as CRDs" repeated pattern~~ RESOLVED

**Files:** `01-concepts/04-governance.md:97`, `01-concepts/04-governance.md:209`
**Criterion:** Key Decisions Compliance
**Key Decision:** Concepts documents are technology-agnostic

Line 97: "Retired laws are deleted as CRDs." Line 209: "The CRD is deleted; history is preserved in the audit log." These use "CRD" to describe law deletion — which is an implementation detail. The key decision permits "CRD" as platform vocabulary, but "deleted as CRDs" is describing an implementation action, not a platform concept.

**Suggested fix:** Line 97: "Retired laws are deleted. The full history is preserved in the audit log." Line 209: "The local law is retired; history is preserved in the audit log."

**Resolution:** Fixed as part of issue #5. Both lines updated to technology-agnostic language.

---

### ~~17. `02-flow/04-system-services.md` — Citation Processor missing from Sidecar service diagram~~ RESOLVED

**Files:** `02-flow/04-system-services.md:19-35`
**Criterion:** Cross-Document Consistency

The service landscape diagram on lines 19-35 shows `SC["Sidecar"]` connecting to `LB`, `CP`, and `AR`. However, the inter-service contracts section (lines 211-221) lists `Sidecar <-> Citation Processor: citation submission and citation evidence query paths` — confirming Sidecar connects to Citation Processor. The diagram does show this (`SC --> CP`), so this is actually correct. However, the label `LB["Librarian + Citation Processor<br/>law lifecycle"]` in the `00-overview.md` diagram (line 31) conflates Librarian and Citation Processor into one box, which could mislead readers into thinking they are the same service.

**Suggested fix:** In `02-flow/00-overview.md:31`, split the `LB` box into separate Librarian and Citation Processor boxes to match the service landscape in `04-system-services.md`.

**Resolution:** Split `LB["Librarian + Citation Processor<br/>law lifecycle"]` into two separate boxes: `LB["Librarian<br/>law lifecycle"]` and `CP["Citation Processor<br/>citation tracking"]` with separate Sidecar connections and Flow Monitor connections.

---

### ~~18. `02-flow/01-operator.md` — exit contract validation queries Archivist directly~~ RESOLVED

**Files:** `02-flow/01-operator.md:147-150`
**Criterion:** Technical Feasibility

The sequence diagram on lines 137-151 shows `OP->>AR: query artefact state for bound contract`. This implies the Operator calls the Archivist directly for contract validation. This is architecturally sound (Operator is a control-plane service, not a node — the Sidecar boundary applies to nodes, not to the Operator). However, the data ownership boundaries in `02-flow/00-overview.md:119-123` say "Nodes access artefact and governance state through Sidecar and SDK surfaces; nodes do not call system services directly." The Operator is not a node, so this is technically consistent, but the Operator-to-Archivist call path is never explicitly described as an authorised inter-service contract.

**Suggested fix:** Add "Operator <-> Archivist: completion validation queries" to the inter-service contracts list in `02-flow/04-system-services.md:215`. This is already implied by line 215 but should be an explicit entry.

**Resolution:** No change needed. The inter-service contracts list at `02-flow/04-system-services.md:214` already explicitly includes `Operator <-> Archivist: completion validation queries and artefact presence checks.` The review's reference to "line 215" was off by one.

---

### ~~19. `01-concepts/00-overview.md` — Passport link anchor may not resolve~~ RESOLVED

**Files:** `01-concepts/00-overview.md:33`
**Criterion:** Cross-Document Consistency

Line 33 links to `[stamps](#stamps)` — a local anchor in the same document. The Stamps section starts at line 35 with `### Stamps`. The anchor `#stamps` should resolve correctly. However, the Passport definition references `[stamps](#stamps)` while the Passport concept's canonical detail is at `./03-data-model.md#passports-and-stamps`. The Passport description is self-referential (defines itself via its own section) rather than linking to the data model detail page on first mention.

**Suggested fix:** Add a cross-link to `[passport](./03-data-model.md#passports-and-stamps)` on the first mention of "passport" in the Passport definition, in addition to the local `#stamps` link.

**Resolution:** No change needed. The Passport heading is already linked to `./03-data-model.md#passports-and-stamps` on line 33. The local `[stamps](#stamps)` link correctly references the immediately following section.

---

### ~~20. `02-flow/03-nodes-external.md` — Assay link targets wrong document~~ RESOLVED

**Files:** `02-flow/03-nodes-external.md:125`
**Criterion:** Cross-Document Consistency

Line 125 links Assay to `(../01-concepts/00-overview.md)` — the concepts overview, which has no Assay heading. The canonical Assay section within `02-flow/` is in the same file at `#assay-as-standard-component` (lines 123-138), or in concepts at `../01-concepts/02-foundry-cycle.md#assay-judiciary--standard-component`.

**Suggested fix:** Change to a self-reference `[Assay](#assay-as-standard-component)` or link to the concepts detail.

**Resolution:** Fixed as part of issue #3. The Assay link now targets `../01-concepts/02-foundry-cycle.md#assay-judiciary--standard-component`.

---

## Minor Issues

### ~~21. `01-concepts/00-overview.md` — "Core Concepts" heading is mild planning voice~~ RESOLVED

**Files:** `01-concepts/00-overview.md:24`
**Criterion:** Writing Principles (no planning voice)

"Core Concepts" is a meta-label for the section's content. It announces that the following are "core" concepts rather than just presenting them. This is borderline — section headings by nature categorise content — but it is the kind of scaffolding label the writing principles warn against.

**Suggested fix:** Consider renaming to a more descriptive heading or removing the heading and integrating the definitions more naturally. This is low priority.

**Resolution:** No change. Borderline issue; the heading serves a legitimate organisational function and does not mislead readers.

---

### ~~22. `01-concepts/03-data-model.md` — `apiVersion: flow.gideas.io/v1` is a technology detail in concepts~~ RESOLVED

**Files:** `01-concepts/03-data-model.md:175`
**Criterion:** Key Decisions Compliance
**Key Decision:** Concepts documents are technology-agnostic

The GovernedArtefact YAML example at lines 174-185 includes `apiVersion: flow.gideas.io/v1` and `kind: GovernedArtefact` — these are Kubernetes CRD details. The key decision allows "CRD" as platform vocabulary, and the YAML example illustrates how governed artefact requirements are declared. The apiVersion is a specific implementation detail, but the example is useful for clarity.

**Suggested fix:** This is defensible as illustrative material using accepted platform vocabulary. Consider adding a note like "Illustrated as a Kubernetes CRD; field-level schema in [CRD Reference](../05-reference/crds.md)." Alternatively, present the requirements in a table or plain text format and leave the CRD YAML for `05-reference/crds.md`. Low priority.

**Resolution:** No change. The YAML example uses accepted Kubernetes platform vocabulary and serves clarity. CRD resource declarations are foundational platform concepts, not incidental technology choices.

---

### ~~23. Inconsistent capitalisation of "node" across documents~~ RESOLVED

**Files:** `01-concepts/00-overview.md:29`, `01-concepts/01-architecture.md:62`, `02-flow/00-overview.md:9`
**Criterion:** Cross-Document Consistency (terminology)

"Node" is capitalised as a proper noun in most contexts (e.g., "Nodes are stateless workers"), but some instances use lowercase "node" when referring to the concept generically (e.g., "exit node", "gate node", "import node"). The spec generally capitalises Flow-specific nouns (Workitem, Artefact, Flow, Sidecar), but "node" is inconsistently treated — sometimes capitalised as "Node" and sometimes not.

**Suggested fix:** Establish a convention: capitalise "Node" when referring to a Foundry Flow node as a system noun, lowercase "node" only in compound terms like "exit node" or "gate node" where it is a modifier. Apply consistently. Low priority since the meaning is always clear from context.

**Resolution:** No change. The existing pattern (capitalised "Node" for the system noun, lowercase in compound modifiers like "exit node", "gate node") is a defensible convention and meaning is always clear from context.

---

### ~~24. `01-concepts/03-data-model.md` — feedback `wont_fix` display label pattern is verbose~~ RESOLVED

**Files:** `01-concepts/03-data-model.md:264`, `01-concepts/03-data-model.md:283-284`, `01-concepts/03-data-model.md:317-318`
**Criterion:** Cross-Document Consistency

The canonical token `wont_fix` with display label "Won't Fix" is explained three separate times within the same document: at the field definition (line 264), at the canonical token table (lines 283-284), and at the state description table (line 317). This is mildly duplicative but aids readability for a reference-style document.

**Suggested fix:** No change needed. The repetition serves clarity in a reference document. Note for consistency.

**Resolution:** No change needed. Repetition is appropriate for a reference-style document.

---

### ~~25. `02-flow/00-overview.md` — Librarian and Citation Processor conflated in diagram~~ RESOLVED

**Files:** `02-flow/00-overview.md:31`
**Criterion:** Cross-Document Consistency

Line 31: `LB["Librarian + Citation Processor<br/>law lifecycle"]` combines two distinct services into one diagram box. `02-flow/04-system-services.md` clearly defines them as separate services with distinct responsibilities and separate storage. This diagram simplification could mislead readers.

**Suggested fix:** Split into two separate boxes: `LB["Librarian<br/>law lifecycle"]` and `CP["Citation Processor<br/>citation tracking"]`. (This duplicates issue #17's suggested fix.)

**Resolution:** Fixed as part of issue #17. Diagram now shows separate Librarian and Citation Processor boxes.

---

### ~~26. `01-concepts/02-foundry-cycle.md` — Quench description uses US spelling "behavior"~~ RESOLVED

**Files:** `01-concepts/02-foundry-cycle.md:5`
**Criterion:** Writing Principles (British spelling)

Line 5: "The platform enforces behaviour through capabilities and configuration" — this instance is correct. However, a systematic check is needed to ensure no US spellings slipped through. I did not find any US spelling violations in my review, but flagging the need for a systematic check.

**Suggested fix:** Run a spelling check for US variants (`behavior`, `artifact`, `organization`, `naturalization`) across all spec documents. No confirmed violation found.

**Resolution:** No change needed. No US spelling violations confirmed. The flagged instance was already correct British spelling.

---

### ~~27. `01-concepts/04-governance.md` — Escalation diagram uses `\n` not `<br/>` but in `flowchart`~~ RESOLVED

**Files:** `01-concepts/04-governance.md:254-265`
**Criterion:** Writing Principles (Mermaid line breaks)

The escalation chain diagram at lines 254-265 is a `flowchart` block. Checking node labels: `Node["Node<br/>(discovers conflict)"]` — uses `<br/>`, correct. `Assay["Assay<br/>(local judiciary)"]` — uses `<br/>`, correct. All edge labels use quoted strings without line breaks. This diagram is correctly formatted.

**Suggested fix:** No change needed. Confirmed correct.

**Resolution:** No change needed. Diagram correctly uses `<br/>` in flowchart labels.

---

### ~~28. `01-concepts/03-data-model.md` — Feedback state diagram uses `\n` in `stateDiagram-v2`~~ RESOLVED

**Files:** `01-concepts/03-data-model.md:290-311`
**Criterion:** Writing Principles (Mermaid line breaks)

The feedback lifecycle uses `stateDiagram-v2` with `\n` in transition labels (e.g., `RefuseFeedback()\nwith Justification`). Per the writing principles, `stateDiagram-v2` handles `\n` natively and is the exception. This is correct.

**Suggested fix:** No change needed. Confirmed correct.

**Resolution:** No change needed. `stateDiagram-v2` handles `\n` natively per writing principles.

---

### ~~29. `02-flow/01-operator.md` — "fail assignment path" is vague~~ RESOLVED

**Files:** `02-flow/01-operator.md:159`
**Criterion:** Cross-Document Consistency

Line 159: "assignment exceeds node timeout budget -> fail assignment path." The phrase "fail assignment path" is not defined elsewhere. It should reference the specific transition (`Running -> Failed`) or the error catalog.

**Suggested fix:** Replace "fail assignment path" with "transition Workitem to `Failed` with timeout error" for consistency with the lifecycle model.

**Resolution:** Changed "fail assignment path" to "transition Workitem to `Failed` with timeout error".

---

### ~~30. `02-flow/03-nodes-external.md` — Reference to Assay in "Failure and Retry" section links to own file~~ RESOLVED

**Files:** `02-flow/03-nodes-external.md:164`
**Criterion:** Cross-Document Consistency

Line 164: "governance deadlock routes to [Assay](./03-nodes-external.md) through Sort logic." This is a self-referential link (the file links to itself). While technically valid, it is unusual and suggests the link target should be an anchor within the same file (`#assay-as-standard-component`).

**Suggested fix:** Change to `[Assay](#assay-as-standard-component)`.

**Resolution:** Fixed as a side effect of the issue #3 rewrite. The self-referential link no longer exists; the line now reads "escalated to Assay" without a link (the Assay link appears earlier in the document at the canonical Assay section).

---

---

## Cross-Document Consistency

### Terminology consistency

| Term | Usage | Status |
|------|-------|--------|
| Workitem | Consistently capitalised as proper noun | OK |
| Artefact | British spelling used consistently | OK |
| artefact (lowercase) | Used consistently for generic references | OK |
| Node | Mostly capitalised; lowercase in "exit node", "gate node", "import node" compounds | Minor inconsistency (issue #23) |
| Sidecar | Consistently capitalised | OK |
| Flow | Consistently capitalised | OK |
| Operator / Flow Operator | Consistently used | OK |
| Archivist | Consistently capitalised | OK |
| Librarian | Consistently capitalised | OK |
| Citation Processor | Consistently two words, capitalised | OK |
| Flow Monitor | Consistently capitalised | OK |
| Assay | Consistently capitalised | OK |
| `WorkitemType` | Correctly absent — only appears in prohibitions | OK |
| `spec.type` | Correctly absent — only appears in prohibitions | OK |
| `spec.context` / `status.context` | Correctly absent — only appears in prohibitions | OK |
| `entryNode` | Correctly absent — only appears as superseded term | OK |
| `importNode` | Consistently used | OK |
| `complete()` | Consistently used with parentheses | OK |
| `route_to_output` / `route_to` | Consistently used as code tokens | OK |
| `wont_fix` | Canonical token with "Won't Fix" display label — consistent | OK |
| Stamp | Capitalised when referring to the governance concept; lowercase in compound use | OK |
| Finding / Ruling / Statute | Consistently capitalised as tier names | OK |
| Foundry Cycle | Consistently capitalised; correctly described as "reference arrangement" | OK |

### Cross-link coverage

- **`01-concepts/`** documents cross-link well to each other and forward-link to `02-flow/` and `04-sdk/`. However, several Assay links target `00-overview.md` without an anchor (issues #2, #6, #14).
- **`02-flow/`** documents cross-link extensively to each other and back to `01-concepts/`. Each document includes alignment preambles (issue #9) that provide cross-links, though these should be natural in-body references.
- **`03-node/`** to `04-sdk/` cross-links are present. `03-node/00-overview.md` lists all SDK documents.
- **`04-sdk/`** and **`05-reference/`** are stubs and include structural cross-links in their outlines.

### Duplication

- **Entry/exit contract semantics** are described in `01-concepts/03-data-model.md`, `02-flow/00-overview.md`, `02-flow/01-operator.md`, `02-flow/02-workitem.md`, and `02-flow/05-configuration.md`. All descriptions are consistent in their content. Some redundancy exists across `02-flow/` documents, but this is appropriate for normative spec documents that must stand alone.
- **Sort's decision order** is described in `01-concepts/02-foundry-cycle.md`, `01-concepts/03-data-model.md`, `02-flow/00-overview.md`, `02-flow/03-nodes-external.md`, `02-flow/05-configuration.md`, and `03-node/03-patterns.md`. All agree on the four-step order. The diagram in `02-flow/03-nodes-external.md` is inconsistent (issue #3).
- **Assay authority ceiling** is described consistently in `01-concepts/02-foundry-cycle.md`, `01-concepts/04-governance.md`, `02-flow/00-overview.md`, `02-flow/03-nodes-external.md`, and `02-flow/06-cross-flow.md`. All agree: resolve Tier 1-2, propose Tier 3, appeal Tier 4-5.
- **Workitem lifecycle states** are described consistently across `01-concepts/03-data-model.md` and `02-flow/02-workitem.md`.
- **Invariant lists** appear at the end of every `02-flow/` and `03-node/` document. These are self-consistent and cross-reference each other.

---

## Summary

| Severity | Count | Resolved | Issues |
|----------|-------|----------|--------|
| **Critical** | 5 | 5 | ~~#1~~, ~~#2~~, ~~#3~~, ~~#4~~, ~~#5~~ |
| **Significant** | 15 | 15 | ~~#6~~, ~~#7~~, ~~#8~~, ~~#9~~, ~~#10~~, ~~#11~~, ~~#12~~, ~~#13~~, ~~#14~~, ~~#15~~, ~~#16~~, ~~#17~~, ~~#18~~, ~~#19~~, ~~#20~~ |
| **Minor** | 10 | 10 | ~~#21~~, ~~#22~~, ~~#23~~, ~~#24~~, ~~#25~~, ~~#26~~, ~~#27~~, ~~#28~~, ~~#29~~, ~~#30~~ |
| **Total** | 30 | 30 | **All resolved** |

**Resolution summary:**

- 17 issues fixed with spec edits (code/prose changes)
- 13 issues resolved with no change needed (confirmed correct, defensible, or already addressed by other fixes)

**Files modified:**

- `01-concepts/00-overview.md` — Archivist cross-link fixed
- `01-concepts/01-architecture.md` — Assay link target fixed
- `01-concepts/03-data-model.md` — All Assay links fixed; `StoreArtefact()` replaced with technology-agnostic phrasing; transition table marked as exhaustive
- `01-concepts/04-governance.md` — All Assay links fixed; "Law CRDs" and "deleted as CRDs" replaced with technology-agnostic language
- `02-flow/00-overview.md` — Meta-commentary preamble removed; Librarian/Citation Processor diagram split
- `02-flow/01-operator.md` — Meta-commentary preamble removed; "fail assignment path" clarified
- `02-flow/02-workitem.md` — Meta-commentary preamble removed
- `02-flow/03-nodes-external.md` — Meta-commentary preamble removed; duplicated reference-arrangement sections replaced with cross-link; Assay link fixed
- `02-flow/04-system-services.md` — Meta-commentary preamble removed; friction aggregation operations added
- `02-flow/05-configuration.md` — Meta-commentary preamble removed
- `02-flow/06-cross-flow.md` — Meta-commentary preamble removed
- `02-flow/07-operations.md` — Meta-commentary preamble removed
- `03-node/00-overview.md` — "Goal" heading removed and reworked into opening paragraph
- `04-sdk/06-sdk-telemetry.md` — Friction aggregation scope added to stub
