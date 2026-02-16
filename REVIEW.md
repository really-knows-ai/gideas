# Spec Review

Deep review of all spec directories against AGENTS.md axioms, writing principles, and cross-document consistency.

Reviewed directories: `01-concepts/`, `02-flow/`, `03-node/`, `04-sdk/`, `05-reference/`, `README.md`

---

## Critical Issues

Issues that create internal inconsistencies or would confuse implementors. Must be fixed.

### ~~1. Feedback re-refusal semantics inconsistent between data model and SDK~~ RESOLVED

**Files:** `01-concepts/03-data-model.md:295`, `04-sdk/04-sdk-feedback.md:65`
**Criterion:** Cross-Document Consistency

**Resolution:** The state machine was missing the `rejected` -> `wont_fix` transition. Added `rejected` -> `wont_fix` (via `RefuseFeedback()` with structured justification) to the Mermaid diagram, transition table, and prose in `01-concepts/03-data-model.md`, and updated the SDK feedback operations table in `04-sdk/04-sdk-feedback.md` to list `rejected` as a valid "From State" for `RefuseFeedback`. Rewrote the prose at line 295 to clarify that a refiner always retains the right to refuse after rejection, provided they justify the refusal on governance grounds.

---

### ~~2. Export bundle signing identity not specified~~ RESOLVED

**Files:** `01-concepts/01-architecture.md:104`, `02-flow/06-cross-flow.md:17-30`, `05-reference/grpc-api.md`
**Criterion:** Technical Feasibility

**Resolution:** Added an "Export Package Structure" subsection to `02-flow/06-cross-flow.md` specifying: the bundle contains Workitem metadata, artefact content (scoped by exit contract), passport stamps, an Operator-signed package signature, and certificate chain. The Operator signs as the Flow's identity. Verification at import validates chain (State Root for siblings, Treaty `caCert` for non-siblings), `allowedSubjects`, `maxBundleSize`, and entry contract. Updated `01-concepts/01-architecture.md:104` to reference the Operator identity (not "node identities"). Added `ExportWorkitem` and `ImportWorkitem` gRPC methods to the Operator API in `05-reference/grpc-api.md`.

---

### ~~3. Deadlock transition actor ambiguity~~ RESOLVED

**Files:** `01-concepts/03-data-model.md:285-287`, `04-sdk/04-sdk-feedback.md:86-90`, `02-flow/03-nodes-external.md:58`
**Criterion:** Technical Feasibility

**Resolution:** Added `DeadlockFeedback(feedbackId)` as a standard SDK feedback state transition and `GetFeedbackDepth(feedbackId)` as a read method. The gate node queries depth, decides whether to escalate, calls `DeadlockFeedback` to set the status, then routes the Workitem to Assay via a normal routing instruction. Replaced the single `WRITE:feedback` capability with granular `WRITE:feedback/<status>` capabilities (`new`, `actioned`, `wont_fix`, `rejected`, `resolved`, `deadlocked`) — each status transition requires its own capability grant, enforced by the Archivist. Dropped the `ESCALATE` verb entirely — routing to Assay is standard routing, not a special capability. Threshold enforcement (`maxFeedbackDepth`) is gate node logic, not Archivist enforcement. Updated `04-sdk/04-sdk-feedback.md`, `01-concepts/03-data-model.md`, `05-reference/grpc-api.md`, `05-reference/crds.md`, `03-node/02-configuration.md`, `02-flow/03-nodes-external.md`, and `05-reference/glossary.md`.

---

## Significant Issues

Issues that weaken accuracy, violate writing principles, or create ambiguity. Should be fixed.

### ~~4. README.md uses planning voice~~ RESOLVED

**Files:** `README.md:7`
**Criterion:** Writing Principles — No planning voice

**Resolution:** Rewrote the Concepts paragraph. Changed "introduces the core vocabulary:" to "defines" (removes meta-commentary). Changed "covers the four core data types in detail:" to "details" (removes planning-voice enumeration).

---

### 5. Assay hearing Workitem creation lacks explicit artefact structure

**Files:** `02-flow/04-system-services.md:215`, `02-flow/03-nodes-external.md:50-56`, `05-reference/crds.md`, `05-reference/grpc-api.md:38`
**Criterion:** Technical Feasibility

Hearing Workitems are described as "standard Workitems with explicit governed artefacts including a `lawId` reference" (`02-flow/04-system-services.md:215`). The gRPC API `CreateHearingWorkitem` method takes `law_id`, `hearing_artefacts[]`, and `hearing_type`.

However, the spec never defines:
- What governed artefact kind(s) hearing Workitems carry
- Whether a `GovernedArtefact` CRD must be registered for hearing artefacts
- What stamps (if any) the hearing entry and exit contracts require
- How the `lawId` reference is structurally carried (as an artefact? as metadata within an artefact?)

Without this, an implementor cannot configure the Assay hearing entry/exit contracts because the artefact kinds and stamp vocabulary are undefined.

**Suggested fix:** Define a standard hearing artefact kind (e.g., `hearing-brief`) in the spec, or state that hearing artefact configuration is Flow-Architect-defined and provide a reference configuration example. Specify how the `lawId` reference is structurally represented within the hearing Workitem's governed artefacts. Add this to either `02-flow/04-system-services.md` or `02-flow/05-configuration.md`.

---

### 6. `rejected` -> `actioned` feedback transition missing constraint documentation

**Files:** `01-concepts/03-data-model.md:286`, `04-sdk/04-sdk-feedback.md:65`
**Criterion:** Cross-Document Consistency

The feedback state machine permits `rejected` -> `actioned` via `ResolveFeedback()`. But the actor column in `01-concepts/03-data-model.md:286` says "Refining node" and the SDK table in `04-sdk/04-sdk-feedback.md:65` says `ResolveFeedback` transitions from `new` or `rejected` to `actioned`.

The issue: `ResolveFeedback` from `new` and from `rejected` have different semantic contexts. From `new`, it means the refiner proactively fixes the issue. From `rejected`, it means the refiner is complying after a rejection. The SDK provides no way for an implementor to distinguish these cases in the call signature, and the spec does not clarify whether the `message` parameter should differ in content expectations.

Additionally, the data model says "A rejected item returns to the refining node for compliance" — but the enforcement mechanism for ensuring only the "refining node" can call `ResolveFeedback` (vs. any node with `WRITE:feedback`) is not specified. Any node with `WRITE:feedback` capability could theoretically call `ResolveFeedback` on a feedback item, regardless of whether it's the original refiner.

**Suggested fix:** Clarify whether feedback transition actor constraints (refining node vs. reviewing node) are enforced at the Archivist level or are conventions. If enforced, define the mechanism (e.g., the Archivist tracks which node created the feedback and which node is the expected refiner). If conventional, state this explicitly so implementors know that node identity is recorded for audit but not enforced for transitions.

---

### 7. Codification Service trigger during promotion not fully specified

**Files:** `02-flow/04-system-services.md:160-185`, `01-concepts/04-governance.md:74`
**Criterion:** Technical Feasibility

`01-concepts/04-governance.md:74` states that when promoted, a Finding can gain new representations through "specialised translation services." `02-flow/04-system-services.md:160-185` describes Codification Services and their `encode` capability.

However, the spec does not specify:
- Who triggers the codification (does Assay call the Codification Service during the hearing, or does the Librarian trigger it after Assay's promote verdict?)
- Whether codification happens synchronously as part of the promotion hearing or asynchronously after promotion
- What happens if the Codification Service produces a representation that does not faithfully express the goal (who validates semantic fidelity?)

The inter-service contract (`02-flow/04-system-services.md:286`) mentions "Assay (via Sidecar) <-> Codification Services: encode requests during law promotion" — implying Assay triggers codification. But the hearing verdict flow (lines 215-243) shows Assay completing and the Librarian applying lifecycle actions, with no codification step.

**Suggested fix:** Add an explicit codification step in the hearing lifecycle: either within Assay's hearing processing (Assay calls the Codification Service before completing) or as a post-verdict Librarian step. Specify who validates the codification output for semantic fidelity and what happens on codification failure (prose-only promotion as fallback, consistent with the degradation note at line 319).

---

### 8. Cross-link gaps for first mentions of Library in conceptual overview

**Files:** `01-concepts/00-overview.md:54`
**Criterion:** Writing Principles — Cross-link aggressively

The first mention of "Library" in the governance model section (`01-concepts/00-overview.md:54`) reads: "A Flow's Library is its collective body of law — its constitution." The word "Library" is not linked here. It should link to the Librarian's detail in System Services or to the Library entry in the Glossary, as it is being used as a formal system noun for the first time.

Similarly, "Library" at line 58 ("The Library stores all representations as part of a single law object with equal indifference") is also unlinked.

**Suggested fix:** Link the first mention of "Library" in `01-concepts/00-overview.md:54` to `../02-flow/04-system-services.md#librarian`.

---

### 9. Nodes "direct, uninhibited network access" statement potentially misleading

**Files:** `01-concepts/01-architecture.md:72`
**Criterion:** Technical Feasibility

`01-concepts/01-architecture.md:72` states: "Nodes have direct, uninhibited network access to external services." This is stated as an architectural fact, but it depends entirely on the Kubernetes network policy configuration. A cluster administrator could restrict egress.

The statement is technically accurate in that the *Flow platform* does not impose network restrictions — it delegates network security to the infrastructure layer. But "uninhibited" could mislead implementors into thinking no network restrictions can or should apply.

**Suggested fix:** Soften to: "Nodes have direct network access to external services. Network segmentation for external access is an infrastructure concern delegated to Kubernetes NetworkPolicies and service mesh configuration." This is consistent with the phrasing already used in `03-node/01-sidecar.md:157`.

---

### 10. FoundryNode CRD `timeout` constraint description inconsistency

**Files:** `05-reference/crds.md:120`, `03-node/02-configuration.md:41`
**Criterion:** Cross-Document Consistency

`05-reference/crds.md:120` states the FoundryNode `timeout` field: "Cannot exceed `governancePolicy.defaultTimeout` on the FoundryFlow." This means the node timeout must be <= the Flow default timeout.

`03-node/02-configuration.md:41` states: "A node's configured timeout cannot exceed the Flow-level maximum." This says "maximum" rather than "default."

The FoundryFlow CRD only defines `defaultTimeout` in `governancePolicy` — there is no separate `maxTimeout` field. So the "maximum" is the `defaultTimeout`. This is a terminology inconsistency that could confuse implementors: "default" suggests the value used when no override is set, while "maximum" suggests a hard ceiling. They are the same field but described with different semantics.

**Suggested fix:** Align terminology. Either (1) rename the CRD field to `maxTimeout` (breaking change, less desirable) or (2) clarify in both documents that `defaultTimeout` serves a dual role: it is the fallback value when no node-specific timeout is set, and it is also the ceiling that no node-specific timeout can exceed. The CRD reference description at `05-reference/crds.md` should state both roles explicitly.

---

### ~~11. Feedback state machine missing `rejected` -> `wont_fix` transition path after non-Assay rejection~~ RESOLVED

**Files:** `01-concepts/03-data-model.md:258`, `04-sdk/04-sdk-feedback.md:60-72`
**Criterion:** Technical Feasibility

**Resolution:** Resolved as part of Issue #1. The `rejected` -> `wont_fix` transition was added to the state machine, transition table, prose, and SDK operations table. The asymmetry was a bug, not an intentional design choice.

---

## Minor Issues

Low-impact but worth noting for consistency.

### 12. README.md uses "Error Catalogue" while the file is named `error-catalog.md`

**Files:** `README.md:57`
**Criterion:** Cross-Document Consistency — Terminology

`README.md:57` uses "Error Catalogue" (British spelling) while the actual filename is `error-catalog.md` (American spelling). The glossary at `05-reference/glossary.md:266` establishes British spelling for spec prose and reserves US spelling for "literal external identifiers." Since the filename is a literal identifier, `error-catalog.md` is correct. But the display text "Error Catalogue" in the README creates a small mismatch — the reader might search for a file named `error-catalogue.md`.

**Suggested fix:** This is technically correct per the glossary convention (British prose, US identifiers), but consider whether the display text should match the filename for navigability. If keeping the current convention, no change needed. If aligning for clarity, change the display text to "Error Catalog" to match the filename.

---

### 13. Glossary "Appraise" entry placed under "Data and Provenance Terms" instead of "Canonical Runtime Terms"

**Files:** `05-reference/glossary.md:63`
**Criterion:** Cross-Document Consistency

The glossary places "Appraise (reference arrangement)" under the "Data and Provenance Terms" heading, while all other reference arrangement nodes (Forge, Sort, Refine, Quench) are placed under "Canonical Runtime Terms" or "Data and Provenance Terms" inconsistently:
- Forge (line 26): Canonical Runtime Terms
- Appraise (line 63): Data and Provenance Terms
- Quench (line 93): Data and Provenance Terms
- Sort (line 96): Data and Provenance Terms
- Refine (line 44): Canonical Runtime Terms

The reference arrangement nodes should be grouped together under one section for consistency.

**Suggested fix:** Move all five reference arrangement node entries (Forge, Quench, Appraise, Sort, Refine) to a single section — either "Canonical Runtime Terms" (since they are runtime actors) or a new "Reference Arrangement Terms" subsection.

---

### 14. `Nodes` capitalised inconsistently in `01-concepts/01-architecture.md`

**Files:** `01-concepts/01-architecture.md:68-70`
**Criterion:** Cross-Document Consistency — Terminology

`01-concepts/01-architecture.md:68` uses "[Nodes](../03-node/00-overview.md)" (capitalised) as a linked reference, then at line 70 uses "Nodes are stateless workers" (capitalised) and "execution state is rebuilt from the Workitem and Archivist" (no article). The glossary (`05-reference/glossary.md:35`) defines the term as lowercase "node" — consistent with most spec usage.

The conceptual overview (`01-concepts/00-overview.md:19`) uses "Node" capitalised in the bold definition but lowercase "node" in running prose. The architecture document capitalises "Nodes" in some running prose (line 68, 70, 72) but uses lowercase elsewhere.

**Suggested fix:** Standardise on lowercase "node" in running prose per glossary convention. Capitalise only in headings and bold definitions. Apply consistently across `01-concepts/01-architecture.md`.

---

### 15. `01-concepts/00-overview.md` stamps section partially duplicates `03-data-model.md` stamps section

**Files:** `01-concepts/00-overview.md:27-36`, `01-concepts/03-data-model.md:161-197`
**Criterion:** Cross-Document Consistency — Duplication

The conceptual overview defines stamps with a detailed bullet list (name, node, content hash, cryptographic signature) at lines 27-36, then the data model page repeats essentially the same information at lines 186-192. Both are in `01-concepts/`.

The overview version is appropriate for a helicopter introduction. The data model version is the normative detail. The risk is that future edits to one might not propagate to the other.

**Suggested fix:** The overview stamp definition is acceptable as a brief introduction, but consider shortening it to a one-sentence definition with a cross-link to the data model's detailed stamp definition, reducing duplication risk.

---

### 16. `02-flow/00-overview.md` Assay link points to `03-nodes-external.md` not `01-concepts/02-foundry-cycle.md`

**Files:** `02-flow/00-overview.md:43`
**Criterion:** Cross-Document Consistency — Cross-links

Line 43 links Assay to `./03-nodes-external.md#assay-as-standard-component`. This is valid, but the normative conceptual definition of Assay's role is in `01-concepts/02-foundry-cycle.md#assay-judiciary--standard-component`. The Flow overview is the first document in `02-flow/` and could benefit from linking to the conceptual definition rather than the node-boundary definition, since the overview establishes the runtime context.

**Suggested fix:** Consider changing the Assay link in `02-flow/00-overview.md:43` to point to the conceptual definition (`../01-concepts/02-foundry-cycle.md#assay-judiciary--standard-component`) since this is an overview document. The current link is not wrong, but the conceptual link provides better context for a reader entering the Flow layer.

---

---

## Cross-Document Consistency

### Terminology consistency

| Term | Usage | Status |
|------|-------|--------|
| Workitem | Consistently capitalised as one word | OK |
| artefact | Consistently British spelling, lowercase | OK |
| Archivist | Consistently capitalised | OK |
| Librarian | Consistently capitalised | OK |
| Sidecar | Consistently capitalised | OK |
| node | Mostly lowercase; occasional capitalisation in `01-concepts/01-architecture.md` | Minor (#14) |
| Flow | Consistently capitalised | OK |
| stamp | Consistently lowercase | OK |
| passport | Consistently lowercase | OK |
| feedback | Consistently lowercase | OK |
| friction | Consistently lowercase | OK |
| law | Consistently lowercase | OK |
| Finding / Ruling / Local Statute / State Constitution / Federal Accord | Capitalised when referring to specific tier names | OK |
| Foundry Cycle | Consistently capitalised as proper noun | OK |
| Governance Flow | Consistently capitalised | OK |
| Error Catalogue vs error-catalog | British display text vs US filename | Minor (#12) |
| `wont_fix` / Won't Fix | Code token vs display label consistently distinguished | OK |
| `defaultTimeout` / "maximum" | Inconsistent description semantics | Significant (#10) |

### Cross-link coverage

Cross-linking is generally thorough across the spec. Most system nouns are linked on first mention within each document. Notable gaps:

- "Library" in `01-concepts/00-overview.md` first use at line 54 is unlinked (Significant #8)
- Assay link target choice in `02-flow/00-overview.md` could point to conceptual definition (Minor #16)

Forward and backward references between layers are well-maintained:
- `01-concepts/` documents link forward to `02-flow/`, `03-node/`, `04-sdk/`, and `05-reference/`
- `02-flow/` documents cross-reference within the layer and back to concepts
- `03-node/` and `04-sdk/` documents link to their runtime service dependencies in `02-flow/`
- `05-reference/` documents link back to normative prose in earlier layers

### Duplication

Most cross-layer repetition is intentional (each layer restates relevant invariants for its audience) and consistent. Identified duplication:

- Stamp definition in `01-concepts/00-overview.md` vs `01-concepts/03-data-model.md` — within the same layer, higher duplication risk (Minor #15)
- Exit contract semantics repeated across `01-concepts/03-data-model.md`, `02-flow/01-operator.md`, `02-flow/02-workitem.md`, `02-flow/05-configuration.md`, and `05-reference/crds.md` — all consistent
- Feedback state machine duplicated across `01-concepts/03-data-model.md` and `04-sdk/04-sdk-feedback.md` — consistent
- Capability syntax duplicated across `03-node/02-configuration.md` and `05-reference/crds.md` — consistent
- Runtime invariants lists across `02-flow/` documents — consistent and complementary (each document's invariants scope to its concern)

---

## Summary

| Severity | Count | Issues |
|----------|-------|--------|
| **Critical** | 3 | #1, #2, #3 |
| **Significant** | 8 | #4, #5, #6, #7, #8, #9, #10, #11 |
| **Minor** | 5 | #12, #13, #14, #15, #16 |
