# Spec Review

Deep review of all spec directories against AGENTS.md foundational axioms, writing principles, and cross-document consistency.

Reviewed directories: `01-concepts/`, `02-flow/`, `03-node/`, `04-sdk/`, `05-reference/`, and root `README.md`

---

## Critical Issues

Issues that create internal inconsistencies that would confuse implementors. Must be fixed before proceeding.

### ~~1. Error Catalogue title uses American spelling "Catalog"~~ RESOLVED

**Files:** `05-reference/error-catalogue.md:1`
**Criterion:** Cross-Document Consistency — Terminology

The file is named `error-catalogue.md` (British spelling, consistent with the rest of the spec), but the H1 heading reads `# Error Catalog` (American spelling). The glossary at `05-reference/glossary.md:270` establishes British spelling as the canonical convention: "British spelling is used for all spec prose (`artefact`, `naturalisation`, `organisation`, `behaviour`)."

**Suggested fix:** Change `# Error Catalog` to `# Error Catalogue` on line 1 of `05-reference/error-catalogue.md`.

**Resolution:** Changed H1 to `# Error Catalogue` to match British spelling convention.

---

### ~~2. Workitem lifecycle diagram uses `\n` instead of `<br/>` for Mermaid line breaks~~ RESOLVED

**Files:** `01-concepts/03-data-model.md:36`
**Criterion:** Writing Principles — Mermaid line breaks

The Workitem lifecycle `stateDiagram-v2` block on line 36 uses `timeout()<br/>thrash()<br/>error()` — however, the `stateDiagram-v2` syntax uses `<br/>` which is correct for state diagram labels. This is actually correct.

However, the *feedback lifecycle* diagram at `01-concepts/03-data-model.md:249` uses `RefuseFeedback()<br/>with Justification` in the state diagram — this is also correct.

*Withdrawn — on closer inspection, the Mermaid diagrams consistently use `<br/>` throughout.*

---

### ~~3. Hearing verdict table for Tier 1 is incomplete — missing Retire option text alignment with governance doc~~ RESOLVED

**Files:** `01-concepts/04-governance.md:30-36`, `02-flow/04-system-services.md:277-278`
**Criterion:** Cross-Document Consistency — Duplicate Information

In `01-concepts/04-governance.md:30-36`, the Tier 1 review hearing verdict table shows only **Promote** as a verdict. The text at line 36 says: "A Finding that does not accumulate enough friction to trigger a review hearing will enter a review hearing when its TTL expires." — this implies only two paths exist (friction-promoted or TTL-expired), but the Tier 1 hearing verdict table at `04-governance.md:82-85` later shows both **Retire** and **Promote** for Tier 1, and `02-flow/04-system-services.md:277` confirms "Tier 1 under review: `Promote` (mint Tier 2 Ruling) or `Retire`."

The verdict table at line 30-36 only lists **Promote**, creating a misleading impression that Tier 1 review hearings can only promote. A reader encountering this table first would not know that Retire is also an option.

**Suggested fix:** Add the Retire verdict to the table at `04-governance.md:33-35` so it matches the complete table at lines 82-85 and the system services description. Or remove the partial table and simply forward-reference the complete table in the "Decay and Retirement" section.

---

## Significant Issues

Issues that weaken accuracy, violate writing principles, or create ambiguity. Should be fixed.

### 4. Planning voice — "Four foundational axioms" in AGENTS.md heading

**Files:** `AGENTS.md:70`
**Criterion:** Writing Principles — No planning voice

The heading reads "### Four foundational axioms" — this enumerates a count ("there are four axioms"). While AGENTS.md is itself guidance rather than spec prose, this pattern could propagate. The axioms should speak for themselves.

**Suggested fix:** Change to `### Foundational Axioms` and let the numbered list convey the count.

---

### 5. Meta-commentary in README.md concepts paragraph

**Files:** `README.md:7`
**Criterion:** Writing Principles — No meta-commentary

The concepts paragraph reads: "The [Conceptual Overview](01-concepts/00-overview.md) defines Flows, Workitems, nodes, artefacts, passports, stamps, feedback, and friction. [Architecture](01-concepts/01-architecture.md) defines the six-plane structure..." — this is structural narration that announces what each document defines rather than presenting the concepts directly.

**Suggested fix:** Rewrite the concepts paragraph to introduce the domain directly: describe what the system is and does, with natural inline links, rather than listing what each document "defines."

---

### 6. Planning voice — "six-plane architecture" count in Architecture heading area

**Files:** `README.md:7`, `01-concepts/01-architecture.md:7`
**Criterion:** Writing Principles — No planning voice

Multiple references enumerate the planes: "the six-plane structure" (README.md:7), "Six-plane architecture" (in AGENTS.md:17). The architecture document at `01-concepts/01-architecture.md` itself does not have the counting problem — it presents the planes through a diagram and then describes each one. But the README and AGENTS.md both announce the count before the reader encounters the planes.

**Suggested fix:** In README.md line 7, change "defines the six-plane structure and responsibility boundaries" to "defines the architectural planes and responsibility boundaries." In AGENTS.md line 17, change "Six-plane architecture" to "Architecture" in the comment.

---

### 7. Meta-commentary in Flow Runtime Overview runtime composition section

**Files:** `02-flow/00-overview.md:7-17`
**Criterion:** Writing Principles — No meta-commentary / Show, don't scaffold

The runtime composition section opens with a bulleted list that reads like a table of contents — "The Flow Operator reconciles configuration...", "External and reference nodes execute work...", "System services provide...". This is structural narration listing what each sub-document covers rather than describing the runtime's behaviour directly.

**Suggested fix:** Rewrite the composition section to describe what the runtime does as a coherent paragraph or series of statements, with links embedded naturally, rather than listing document summaries.

---

### 8. Missing cross-link: first mention of "Workitem" in 01-concepts/01-architecture.md

**Files:** `01-concepts/01-architecture.md:60`
**Criterion:** Writing Principles — Cross-link aggressively

Line 60 mentions "Workitem CRDs" as first use in the document but links to `./03-data-model.md#workitems`. This is correct. However, line 3 mentions "Flow" linking to `./00-overview.md` — which is correct. The first mention of "Sidecar" at line 80 links correctly. Good cross-linking here.

*Withdrawn — cross-linking is adequate in this file.*

---

### 9. Inconsistent Mermaid `<br/>` usage in Assay `sequenceDiagram` note lines

**Files:** `02-flow/04-system-services.md:208`
**Criterion:** Writing Principles — Mermaid line breaks

The Codification Services sequence diagram at `02-flow/04-system-services.md:208` uses `assemble Ruling<br/>(prose + formal representations)` — this uses `<br/>` correctly. Checked other Mermaid diagrams throughout — they all use `<br/>` consistently.

*Withdrawn — Mermaid line breaks are consistent.*

---

### 10. `DeadlockFeedback` transitions from `new` state are missing in the feedback state machine

**Files:** `01-concepts/03-data-model.md:242-292`, `04-sdk/04-sdk-feedback.md:33-55`
**Criterion:** Technical Feasibility — State Machine Completeness

The feedback lifecycle diagram shows `DeadlockFeedback()` can transition from `wont_fix` and `rejected` to `deadlocked`. However, there is no transition from `new` to `deadlocked`. This is correct by design — a feedback item needs to have gone through at least one argue-reject cycle before it can be deadlocked. The gate node checks history depth, and a `new` item has depth 0 or 1, below any reasonable `maxFeedbackDepth`.

*Withdrawn — this is correct by design.*

---

### 11. README.md table descriptions use meta-commentary pattern

**Files:** `README.md:13-58`
**Criterion:** Writing Principles — No meta-commentary / Show, don't scaffold

The tables at README.md lines 13-58 are preceded by section headings and then directly present the tables — the tables themselves describe documents ("Runtime composition, execution loop, and platform invariants"). Since the README is explicitly a navigation entry point (AGENTS.md:12 says "Entry point, navigation (write last)"), some structural narration is appropriate here.

*Withdrawn — the README's role as a navigation document justifies this pattern.*

---

### 12. The Librarian link is bare in several first mentions

**Files:** `01-concepts/00-overview.md:31`, `01-concepts/00-overview.md:49`
**Criterion:** Writing Principles — Cross-link aggressively

In `01-concepts/00-overview.md:31`, the first mention of "Library" links to `../02-flow/04-system-services.md`. This links to the system services page root, not to the Librarian section specifically. Line 49 also links to `../02-flow/04-system-services.md#librarian` — the more specific anchor.

**Suggested fix:** Update line 31's link to `../02-flow/04-system-services.md#librarian` for specificity: `[Library](../02-flow/04-system-services.md#librarian)` — consistent with the link at line 49.

---

### 13. Inconsistent capitalisation of "Workitem" vs "workitem" in SDK telemetry envelope

**Files:** `04-sdk/06-sdk-telemetry.md:28`
**Criterion:** Cross-Document Consistency — Terminology

The telemetry envelope table at `04-sdk/06-sdk-telemetry.md:28` uses `workitem_id` as a field name (lowercase). This is a field identifier, not prose, so lowercase is appropriate. However, throughout the spec the prose consistently uses "Workitem" (capitalised) when referring to the concept. No issue here — field names vs prose terms are correctly distinguished.

*Withdrawn — field names are appropriately lowercase.*

---

### 14. Assay's hearing exit contract validation could leave orphaned laws

**Files:** `02-flow/04-system-services.md:221`, `02-flow/04-system-services.md:236`
**Criterion:** Technical Feasibility

In the hearing lifecycle at `02-flow/04-system-services.md:235-236`, Assay writes the new law to the Librarian (via `WriteLaw`) and then calls `complete()`. The Operator validates the hearing exit contract and then "Librarian applies resulting law lifecycle actions" (line 236). If the Operator rejects the completion (e.g., the `law-reference` artefact is somehow missing), Assay has already written a new law to the Library. The sequence "write law, then complete" creates a window where the law exists but the hearing Workitem hasn't completed — potentially leaving a law that was written but never ratified by the hearing process.

**Suggested fix:** Add a note clarifying that `WriteLaw` during hearing processing is provisional — the law is created in a pending/inactive state and only activated when the Operator confirms hearing completion via `ApplyLifecycleAction`. Or clarify that the hearing exit contract is structurally guaranteed to succeed (the `law-reference` artefact cannot be removed during processing), making this failure window practically impossible.

---

### 15. `ApplyLifecycleAction` is called by the Operator but not listed in Operator API

**Files:** `02-flow/04-system-services.md:221`, `05-reference/grpc-api.md:129-136`
**Criterion:** Cross-Document Consistency — Cross-links / Technical Feasibility

The hearing lifecycle protocol describes the Operator calling `ApplyLifecycleAction` on the Librarian after hearing completion (`02-flow/04-system-services.md:221`, line 236: "Operator validates Assay's bound hearing exit contract and applies completion state; Librarian applies resulting law lifecycle actions"). The Librarian's service-facing method `ApplyLifecycleAction` is listed in the gRPC API reference (`05-reference/grpc-api.md:136`). However, the Operator API section (`05-reference/grpc-api.md:20-57`) does not list this as an outgoing call or inter-service contract. This is a minor documentation gap — the Operator *calls* this method on the Librarian, it's not an Operator-facing API method. The Librarian API correctly documents it. The inter-service contracts in `02-flow/04-system-services.md:308` also document "Operator <-> Librarian: law lifecycle events, hearing Workitem creation coordination."

*Withdrawn — the API is correctly documented on the Librarian side, which is where it belongs.*

---

### 16. `QueryArtefactState` listed as Operator service-facing method but it's an internal call

**Files:** `05-reference/grpc-api.md:34`
**Criterion:** Cross-Document Consistency

`QueryArtefactState` is listed under Operator API "Service-Facing Methods" but its description says "Called by the Operator's own reconciliation loop against the Archivist." This is confusing — it's not a method *on* the Operator API, it's a method the Operator *calls* on the Archivist. The Archivist API section doesn't list a dedicated `QueryArtefactState` method — the Operator uses the same Archivist API methods but through a direct service path rather than Sidecar mediation.

**Suggested fix:** Move `QueryArtefactState` out of the Operator API section. Either add it to the Archivist API as a service-facing method, or clarify in the Operator API section that this represents the Operator's *outgoing* call to the Archivist (not a method exposed by the Operator).

---

### 17. Missing first-mention cross-link for "Flow Architect" in several documents

**Files:** `01-concepts/02-foundry-cycle.md:5`, `02-flow/05-configuration.md:9`
**Criterion:** Writing Principles — Cross-link aggressively

"Flow Architect" is a defined term in the glossary (`05-reference/glossary.md:17-18`) but is never linked to the glossary on first mention in most documents. It appears unlinked in `01-concepts/02-foundry-cycle.md:5` ("Flow Architects are expected to adapt"), `02-flow/05-configuration.md:9`, and many other locations. While "Flow Architect" is self-explanatory, the glossary provides a canonical definition that should be referenced.

**Suggested fix:** Add a cross-link to the glossary entry on first mention of "Flow Architect" in key documents — at minimum in `01-concepts/00-overview.md`, `01-concepts/02-foundry-cycle.md`, and `02-flow/05-configuration.md`.

---

### 18. `ExportWorkitem` and `ImportWorkitem` listed under Operator API but describe cross-flow mechanics not detailed in Operator doc

**Files:** `05-reference/grpc-api.md:35-36`, `02-flow/01-operator.md`
**Criterion:** Cross-Document Consistency — Cross-links

The gRPC API reference lists `ExportWorkitem` and `ImportWorkitem` as Operator service-facing methods (`05-reference/grpc-api.md:35-36`), including detailed descriptions of package signing, certificate chain verification, Treaty enforcement, and entry contract validation. However, the Operator document (`02-flow/01-operator.md`) does not mention these methods or describe the Operator's role in export/import package assembly and verification. The cross-flow document (`02-flow/06-cross-flow.md`) describes the lifecycle but doesn't name these specific API methods.

**Suggested fix:** Add a brief section or paragraph in `02-flow/01-operator.md` referencing the Operator's export/import responsibilities and linking to the gRPC API for method details. Alternatively, add method name references in `02-flow/06-cross-flow.md` to create a clear link between the protocol description and the API surface.

---

### 19. Feedback transition table inconsistency — `DeadlockFeedback` from `new` state

**Files:** `01-concepts/03-data-model.md:286`, `04-sdk/04-sdk-feedback.md:66`
**Criterion:** Cross-Document Consistency

The feedback transition tables consistently show `DeadlockFeedback` only from `wont_fix` and `rejected` states, not from `new` or `actioned`. This is consistent across `01-concepts/03-data-model.md:286-289` and `04-sdk/04-sdk-feedback.md:66`. The state diagram also matches. No issue.

*Withdrawn — consistent across documents.*

---

### 20. Archivist storage described as "SQLite" is normative but may constrain implementation

**Files:** `02-flow/04-system-services.md:95`, `02-flow/00-overview.md:123`, `02-flow/02-workitem.md:117`
**Criterion:** Technical Feasibility

Multiple documents normatively specify "SQLite" as the Archivist's embedded database (`02-flow/04-system-services.md:95`, `02-flow/00-overview.md:123`, `02-flow/02-workitem.md:117`, `01-concepts/01-architecture.md:166`). While SQLite is a reasonable choice for a single-namespace service, this specificity constrains implementation. If the spec intends SQLite as normative (not just illustrative), this is fine. But if future implementations might use a different embedded database, the spec should specify the semantic requirements (embedded, relational, single-writer) rather than the product name.

**Suggested fix:** Decide whether "SQLite" is a normative implementation requirement or an illustrative reference. If normative, no change needed. If illustrative, replace with "embedded relational database" and note SQLite as the reference implementation choice.

---

### 21. Feedback `HIGH` severity description differs subtly between data model and SDK

**Files:** `01-concepts/03-data-model.md:228`, `04-sdk/04-sdk-feedback.md:80`
**Criterion:** Cross-Document Consistency — Duplicate Information

In `01-concepts/03-data-model.md:228`, `HIGH` severity is described as "Functional or security concern — must be addressed". In `04-sdk/04-sdk-feedback.md:80`, `HIGH` is described as "Functional or security concern" (without "must be addressed"). The "must be addressed" qualifier is present in the data model but absent from the SDK.

**Suggested fix:** Make the descriptions identical. Either add "must be addressed" to the SDK description or remove it from the data model. The shorter version without "must be addressed" is cleaner since severity signals urgency, not obligation (as explicitly stated in both documents).

---

### 22. Treaty `direction` field describes export/import from the local Flow's perspective but could confuse

**Files:** `05-reference/crds.md:281`
**Criterion:** Technical Feasibility — Clarity

The Treaty CRD's `direction` field is described as `import` (this Flow receives from remote) or `export` (this Flow sends to remote). The governance document describes Treaties as "bilateral agreement... with unidirectional execution" (`01-concepts/04-governance.md:289`). The cross-flow document says "A Treaty from Flow A to Flow B grants one-way import authority" (`02-flow/06-cross-flow.md:91`). The CRD reference's local-perspective naming (`import`/`export`) and the cross-flow document's sender-perspective naming ("Treaty from A to B") could create confusion about whose perspective defines the direction.

**Suggested fix:** Add a clarifying note to the Treaty CRD description or the cross-flow document explicitly reconciling the two perspectives. For example: "A Treaty with `direction: import` in Flow B corresponds to 'a Treaty from Flow A to Flow B' in cross-flow descriptions."

---

### 23. Hearing Workitem `law-reference` artefact content is underspecified

**Files:** `02-flow/04-system-services.md:219`, `05-reference/crds.md:53`
**Criterion:** Technical Feasibility

The hearing lifecycle describes that hearing Workitems "carry a single `law-reference` artefact — a built-in GovernedArtefact kind provisioned by the Operator alongside Assay. The `law-reference` artefact contains the law ID under review." (`02-flow/04-system-services.md:219`). The CRD reference says "The Operator also provisions a `law-reference` GovernedArtefact kind alongside Assay. Its stamp vocabulary is empty." (`05-reference/crds.md:53`). But neither document specifies the format of the `law-reference` artefact's content (is it a plain string law ID? A JSON structure? A specific schema?). The gRPC API shows `CreateHearingWorkitem` takes a `law_id` parameter (`05-reference/grpc-api.md:33`) — the Operator creates the artefact from this. The content format should be specified for implementors.

**Suggested fix:** Add a brief specification of the `law-reference` artefact content format — either as a simple string containing the law ID, or as a structured JSON document. Specify this in the CRD reference alongside the GovernedArtefact definition, or in the system services hearing lifecycle section.

---

### 24. `WRITE:artefact/<kind>` scoped write is described in node configuration but not in CRD capability syntax

**Files:** `03-node/02-configuration.md:46`, `05-reference/crds.md:134-135`
**Criterion:** Cross-Document Consistency

In `03-node/02-configuration.md:46`, the capability `WRITE:artefact/<kind>` is described: "`WRITE:artefact` grants write access to all kinds; `WRITE:artefact/<kind>` scopes to a specific kind." The CRD reference at `05-reference/crds.md:134-135` lists both `WRITE:artefact` (all kinds) and `WRITE:artefact/<kind>` (scoped). These are consistent. Good.

*Withdrawn — consistent across documents.*

---

## Minor Issues

Issues that are low-impact but worth noting for consistency.

### 25. Inconsistent link targets for System Services

**Files:** `01-concepts/00-overview.md:31`, `01-concepts/01-architecture.md:90`
**Criterion:** Cross-Document Consistency — Cross-links

In `01-concepts/00-overview.md:31`, the Library is linked to `../02-flow/04-system-services.md` (root of system services). In `01-concepts/01-architecture.md:90`, the Librarian links to `../02-flow/04-system-services.md` (root again). Meanwhile `01-concepts/00-overview.md:49` correctly links to `../02-flow/04-system-services.md#librarian`. The system services document has clear section anchors (#librarian, #archivist, #flow-monitor-and-friction-surface). Links should use specific anchors.

**Suggested fix:** Update links at `01-concepts/00-overview.md:31` and `01-concepts/01-architecture.md:62`, `01-concepts/01-architecture.md:90` to use the `#librarian` anchor. Similarly, review all links to system services across concepts documents and ensure they target the specific section anchor.

---

### 26. "Foundry Flow" vs "Flow" capitalisation in glossary

**Files:** `05-reference/glossary.md:14-15`
**Criterion:** Cross-Document Consistency — Terminology

The glossary defines "Flow" (not "Foundry Flow") as the canonical term. The project is named "Foundry Flow" but the runtime unit is consistently called "Flow" (capitalised) throughout the spec. The README title is "# Foundry Flow" and uses "Flow" for the runtime. This is consistent.

*Withdrawn — no issue.*

---

### 27. Sort link missing in `02-flow/05-configuration.md` reference arrangement defaults section

**Files:** `02-flow/05-configuration.md:166`
**Criterion:** Writing Principles — Cross-link aggressively

In `02-flow/05-configuration.md:166`, "Sort performs gate routing and final approval checkpoint in the reference arrangement" — "Sort" is not linked to the Foundry Cycle Sort section. Other node names in the same list (Forge at line 163, Quench at 164, Appraise at 165, Refine at 167) are linked. Sort is the only one without a link.

**Suggested fix:** Link Sort to `../01-concepts/02-foundry-cycle.md#sort-gate` at `02-flow/05-configuration.md:166`.

---

### 28. Inconsistent artefact isolation table uses "Physical isolation" description

**Files:** `01-concepts/03-data-model.md:137`
**Criterion:** Writing Principles — Define things on their own terms

The artefact isolation table at `01-concepts/03-data-model.md:137` describes storage layout enforcement as "Physical isolation: content is partitioned by Workitem — cross-Workitem access is structurally impossible at the storage layer". The term "physical" is slightly misleading for a logical partitioning scheme — the content is logically partitioned by Workitem in the Archivist's storage, not necessarily in separate physical storage volumes.

**Suggested fix:** Replace "Physical isolation" with "Structural isolation" or "Logical partition" to more accurately describe the enforcement mechanism.

---

### 29. `FlowSupportService` CRD `capabilities` field uses different pattern than node capabilities

**Files:** `05-reference/crds.md:297`
**Criterion:** Technical Feasibility — Clarity

The `FlowSupportService` CRD has a `capabilities` field described as `[]string` with an example of `["encode"]`. This is a simple capability name list, unlike the verb-resource grammar used for node capabilities (`VERB:RESOURCE[/QUALIFIER]`). The difference is by design — Support Services declare what they *provide*, while nodes declare what they're *permitted to do*. But the shared name "capabilities" across both CRDs with different semantics could confuse implementors.

**Suggested fix:** Add a brief clarifying note in the FlowSupportService CRD description that these are "provided capabilities" (what the service offers), not "granted capabilities" (the verb-resource permission grammar used in FoundryNode). Or rename the field to `providedCapabilities` to disambiguate.

---

### 30. Missing explicit `READ:artefact` capability requirement for Archivist read operations in node configuration

**Files:** `03-node/02-configuration.md:46`
**Criterion:** Cross-Document Consistency

The capability grammar section at `03-node/02-configuration.md:46` lists `READ:artefact` but doesn't explicitly state that it's needed for `GetArtefact`, `GetArtefactVersion`, and `GetArtefactMetadata`. The SDK artefacts document at `04-sdk/02-sdk-artefacts.md:108` clearly maps these operations to `READ:artefact`. The node configuration document focuses on the grammar but could benefit from a forward reference to the SDK artefacts capability table.

**Suggested fix:** Add a note or forward reference from the capability grants section in `03-node/02-configuration.md` to the SDK artefacts capability table at `04-sdk/02-sdk-artefacts.md#capability-gated-actions` for readers looking up what each capability enables.

---

### 31. `ListArtefacts` capability requirement is "Assignment scope (implicit)" in SDK but not mentioned in CRDs

**Files:** `04-sdk/02-sdk-artefacts.md:109`
**Criterion:** Cross-Document Consistency

The SDK artefacts capability table at `04-sdk/02-sdk-artefacts.md:109` lists `ListArtefacts` as requiring "Assignment scope (implicit)" enforced by the Sidecar, with no explicit capability needed. This is a design choice — listing your own Workitem's artefact references doesn't need a capability grant. But this implicit access isn't documented in the CRD capability syntax or the node configuration document. A reader checking what operations a node can perform from the CRD reference alone would not know about this implicit access.

**Suggested fix:** Add a brief note in either the CRD capability syntax section or the node configuration capability grants section stating that `ListArtefacts` (listing artefact references on the assigned Workitem) is implicitly available to all nodes without a specific capability grant.

---

---

## Cross-Document Consistency

### Terminology consistency

| Term | Usage | Status |
|------|-------|--------|
| Workitem | Consistently capitalised as "Workitem" (not "work item" or "WorkItem") throughout all documents | OK |
| artefact | Consistently British spelling "artefact" (not "artifact") throughout all documents | OK |
| Archivist | Consistently capitalised throughout | OK |
| Librarian | Consistently capitalised throughout | OK |
| Sidecar | Consistently capitalised throughout | OK |
| Flow Operator / Operator | Both forms used; "Operator" is the short form. Consistent | OK |
| Flow Architect | Consistently used, not linked to glossary on first mention | Minor — see issue #17 |
| naturalisation | British spelling consistently used | OK |
| behaviour | British spelling consistently used | OK |
| organisation | British spelling consistently used | OK |
| "Catalog" vs "Catalogue" | File named `error-catalogue.md` but H1 reads "Error Catalog" | Issue #1 |
| stamp vocabulary | Consistently used across data model, CRDs, and glossary | OK |
| `wont_fix` vs "Won't Fix" | Canonical token `wont_fix`, display label "Won't Fix" — consistently documented | OK |
| "reference arrangement" | Consistently used to describe the Foundry Cycle standard topology | OK |
| "standard component" | Consistently used for Assay across all documents | OK |

### Cross-link coverage

Cross-linking is generally thorough across the spec. Most first mentions of key concepts are linked to their detail pages. Notable gaps:

- "Flow Architect" is never linked to its glossary definition on first mention (issue #17)
- Some links to System Services point to the page root rather than specific section anchors (issue #25)
- Sort is missing a link in the configuration defaults section (issue #27)

The spec makes good use of both forward references (concepts -> flow/node/sdk) and backward references (sdk -> concepts). The README serves as the navigation hub with links to all major sections.

### Duplication

Content that appears in multiple places is generally consistent. Key areas of controlled duplication:

| Topic | Primary Location | Secondary Location(s) | Consistency |
|-------|-----------------|----------------------|-------------|
| Feedback state machine | `01-concepts/03-data-model.md` | `04-sdk/04-sdk-feedback.md` | Consistent — identical transitions and diagrams |
| Entry/exit contract semantics | `01-concepts/03-data-model.md` | `02-flow/05-configuration.md`, `02-flow/02-workitem.md`, `05-reference/crds.md` | Consistent |
| Workitem lifecycle states | `01-concepts/03-data-model.md` | `02-flow/02-workitem.md` | Consistent diagrams and descriptions |
| Law tier table | `01-concepts/00-overview.md` | `01-concepts/03-data-model.md` | Partial table in overview missing Retire verdict — see issue #3 |
| Routing instructions | `01-concepts/03-data-model.md` | `04-sdk/01-sdk-core.md`, `02-flow/02-workitem.md` | Consistent |
| Capability syntax | `03-node/02-configuration.md` | `05-reference/crds.md` | Consistent |
| Friction formulas | `01-concepts/03-data-model.md` | `01-concepts/04-governance.md`, `02-flow/04-system-services.md` | Consistent |
| Feedback severity descriptions | `01-concepts/03-data-model.md` | `04-sdk/04-sdk-feedback.md` | Minor inconsistency in `HIGH` — see issue #21 |

---

## Summary

| Severity | Count | Issues |
|----------|-------|--------|
| **Critical** | 1 | #1 |
| **Significant** | 11 | #3, #4, #5, #6, #7, #12, #14, #16, #17, #18, #22 |
| **Minor** | 7 | #20, #21, #23, #25, #27, #28, #29, #30, #31 |
