---
name: spec-review
description: Deep-review the spec — starts a fresh review of all spec documents against AGENTS.md (producing REVIEW.md), or continues an existing review by walking through unresolved issues with the user
---

## Overview

This skill manages a deep review of the Foundry Flow specification. It has two modes:

1. **Start a new review** — perform a comprehensive review of all spec documents and produce a `REVIEW.md` at the repository root.
2. **Continue an existing review** — pick up where a previous review left off, walking through unresolved issues.

The mode is determined automatically based on the state of `REVIEW.md`.

---

## Entry Point

1. **Read `AGENTS.md`** at the repository root. Internalise the project context, key decisions, writing principles, spec structure, and status table fully before proceeding.

2. **Check for `REVIEW.md`** at the repository root.

   - **If `REVIEW.md` does not exist** → proceed to **Start New Review**.
   - **If `REVIEW.md` exists**, read it and check whether all issues are resolved (every issue heading is ~~struck through~~ or tagged RESOLVED, and no unresolved issues remain):
     - **All issues resolved** → proceed to **Start New Review** (the previous review is complete; a fresh pass is needed).
     - **Unresolved issues remain** → proceed to **Continue Existing Review**.

3. **If starting a new review and a `REVIEW.md` already exists**, ask the user to confirm before overwriting. Explain that the existing review will be replaced with a fresh one. If the user declines, switch to **Continue Existing Review** mode instead.

---

## Start New Review

### Step 1: Discover spec directories

Find all directories at the repository root matching the pattern `[0-9][0-9]-*` (e.g. `01-concepts/`, `02-flow/`). These are the spec directories to review as well as the base `README.md`.

### Step 2: Read all spec documents

For each spec directory, read every `.md` file. Build a mental model of the entire spec as written.

### Step 3: Perform deep review

Review every spec document against the following five criteria. Each criterion should be evaluated independently and thoroughly.

#### Criterion: Writing Principles

Check every spec document against the writing principles:

- **Define things on their own terms** — Flag any negative definitions ("unlike X", "not like Y", "doesn't do Z")
- **No planning voice** — Flag enumerations like "there are four axioms" or "eight nouns describe the system"
- **No meta-commentary** — Flag "in this section we will...", "the following table summarises...", or similar structural narration
- **Show, don't scaffold** — Flag diagrams or tables that are announced rather than presented naturally
- **Mermaid line breaks** — Check that `flowchart` and `sequenceDiagram` blocks use `<br/>` not `\n` for line breaks
- **Cross-link aggressively** — Flag concepts that have detail pages but are not linked on first mention

#### Criterion: Cross-Document Consistency

Check across all spec documents for:

- **Terminology** — Are the same terms used consistently? (e.g. "Workitem" not "work item", "artefact" not "artifact", consistent capitalisation of system nouns)
- **Duplicate information** — Where the same concept appears in multiple documents, do the descriptions agree? Flag contradictions.
- **Cross-links** — Are forward and backward references present and correct? Flag missing cross-links to related documents.
- **Numbering and naming** — Do references to other documents use correct file paths?

#### Criterion: Technical Feasibility

For each mechanism described in the spec, evaluate whether it is implementable.

### Step 4: Classify issues

Assign each issue a severity:

- **Critical** — Creates an internal inconsistency that would confuse implementors. Must be fixed.
- **Significant** — Weakens accuracy, violates writing principles, or creates ambiguity. Should be fixed.
- **Minor** — Low-impact but worth noting for consistency.

### Step 5: Write REVIEW.md

Produce a `REVIEW.md` file at the repository root with the following structure:

```markdown
# Spec Review

Deep review of all spec directories against AGENTS.md key decisions, writing principles, and cross-document consistency.

Reviewed directories: `01-concepts/`, `02-flow/`, ... (list all reviewed)

---

## Critical Issues

Issues that contradict key decisions or create internal inconsistencies. Must be fixed before proceeding.

### 1. [Issue title]

**Files:** `01-concepts/00-overview.md:42`, `01-concepts/02-data-model.md:118`
**Criterion:** Key Decisions Compliance
**Key Decision:** [Which key decision is affected]

[Description of the issue: what the spec says, what it should say, and why this is a problem.]

**Suggested fix:** [Concrete description of the change needed.]

---

## Significant Issues

[Same format as Critical]

---

## Minor Issues

[Same format as Critical]

---

## Cross-Document Consistency

### Terminology consistency

| Term | Usage | Status |
|------|-------|--------|
| ... | ... | ... |

### Cross-link coverage

[Assessment of cross-link completeness]

### Duplication

[Assessment of duplicated content and whether it is consistent]

---

## Summary

| Severity | Count | Issues |
|----------|-------|--------|
| **Critical** | N | #1, #2, ... |
| **Significant** | N | #3, #4, ... |
| **Minor** | N | #5, #6, ... |
```

### Step 6: Present to user

After writing `REVIEW.md`, present a summary to the user:
- Total issue count by severity
- Highlight the critical issues
- Ask if the user wants to start walking through issues now (which transitions to the Continue Existing Review workflow)

---

## Continue Existing Review

### Step 1: Read context

1. **Read `AGENTS.md`** — internalise fully.
2. **Read `REVIEW.md`** — identify all outstanding issues (any issue not marked ~~struck through~~ or tagged RESOLVED).

### Step 2: Walk through issues

For each outstanding issue, one at a time:

1. **Verify**: ready the relevant files and verify it's still a present issue as described.
1. **Present the issue clearly**: what the problem is, which file(s) and line(s) are affected, and what the suggested fix is.
2. **Read the affected file(s)** to understand the current state of the text.
3. **Discuss the fix with the user.** They may agree, disagree, or want to modify the approach.
4. **Once agreed, make the edit.**
5. **Update `REVIEW.md`** to mark the issue as resolved — apply strikethrough to the issue heading and add a RESOLVED tag, then add a brief note of what was changed (following the pattern of already-resolved issues in the existing REVIEW.md).

### Step 3: Finalise

After all issues are addressed:

1. Update the Summary table in `REVIEW.md` to reflect final counts and resolution status.
2. Summarise to the user what was changed across all documents.

---

## Rules

- Never skip an issue without explicit user agreement.
- Follow the writing principles in AGENTS.md when making edits.
- Preserve cross-document consistency — if fixing one document requires a corresponding change in another, flag it and make both changes.
- Do not modify AGENTS.md key decisions unless the user explicitly asks.
- When starting a new review, be thorough — read every line of every spec document. Surface issues are not enough; the review must catch subtle contradictions and drift.
- Issue numbers must be globally unique and sequential within a single REVIEW.md. Do not reuse numbers from previous reviews.
- Every issue must cite specific file paths and line numbers.
- Every issue must reference which review criterion it falls under.
