---
name: implementation-review
description: Review the current repository state against a project's SPEC.md in plans/<project-name>/ and produce a checklist of issues in REVIEW.md. Use when you need a methodical spec-compliance audit of the present codebase.
---

# Implementation Review

Read a project's `SPEC.md` from `plans/<project-name>/`, then dispatch parallel `@reviewer` subagents—one per spec section—to audit the current repository state for compliance. Consolidate findings with any existing `REVIEW.md`, cross-referencing new issues against previously resolved, wont-fix, and open items.

## Workflow

### 1. Read AGENTS.md and the spec (always on main)

Read `AGENTS.md` from the repository root. Read `plans/<project-name>/SPEC.md` from main. Specs, plans, and REVIEW.md live on main regardless of where the implementation lives. If the directory or file does not exist, stop and report the missing path.

### 2. Locate the implementation

Run `git worktree list` from main. If a worktree exists whose branch contains the project name, the implementation lives there. Record its path — reviewers will assess that checkout.

If no matching worktree exists, the implementation is on main. Record the main checkout path.

### 3. Divide the spec into sections

Break `SPEC.md` into its top-level logical sections. Each section becomes an independent review target.

### 4. Read the existing review (if present)

If `plans/<project-name>/REVIEW.md` exists, read it. Note every item and its state:

- `- [ ]` — open, not yet addressed
- `- [x]` — previously fixed and approved
- `- [~]` — previously marked wont-fix with a justification

If no `REVIEW.md` exists, treat the existing checklist as empty.

Do not delete the existing file. The new findings will be consolidated into it.

### 5. Dispatch one reviewer per section

For each spec section, launch an `@reviewer` subagent in parallel with the others. Every subagent receives this prompt:

```
Review the current repository state against this single section of the spec.

**Spec section:**
[verbatim section text]

**Repository:**
Review the checkout at [worktree path, or main path if no worktree exists]. Do not look at git diff, commit history, or branch comparisons. Assess the code as it exists now in that checkout.

**Task:**
- Identify every place where the present implementation does not satisfy this spec section.
- List each deviation as a concrete, actionable item.
- Do not judge severity. If the implementation differs from the spec in any way, that is an issue.
- Do not include a summary, a compliance judgement, or severity ratings.
- If this section is fully satisfied, return exactly: "No issues."
```

### 6. Collect and consolidate

When all subagents return, gather every non-empty finding. If every reviewer returns "No issues." (or equivalent), respond to the user with:

> No issues.

and leave `REVIEW.md` unchanged (do not delete it).

Otherwise, consolidate the new findings with the existing checklist:

#### Cross-reference each new finding against existing items

For each new finding from the reviewers, determine whether it matches an existing item. Every match triggers re-evaluation:

- **Matches a `- [x]` item:** Check whether the original fix still addresses the new finding. If the fix held, leave the item as `- [x]`. If the fix did not hold, revert to `- [ ]` and merge the new feedback with the original item text.
- **Matches a `- [~]` item:** Check whether the original wont-fix justification still covers the new finding. If the justification holds, leave the item as `- [~]`. If invalidated, revert to `- [ ]` and merge the new feedback with the original item text.
- **Matches an open `- [ ]` item:** Merge the new feedback into the existing item. Do not add a duplicate.
- **No match:** Append as a new `- [ ]` item.

#### Preserve unmatched existing items

Existing items with no matching new finding are left as-is — do not change or remove them.

#### Write the consolidated checklist

Write the result back to `plans/<project-name>/REVIEW.md`:

- Each item is a top-level checklist item: `- [ ] ...`, `- [x] ...`, or `- [~] ...`
- Preserve exact wording from reviewers when possible; merge rather than rewrite.
- Do not group, deduplicate beyond the cross-reference rules above, or reclassify items.
- Do not add a summary, a compliance score, or any preamble beyond the checklist itself.
- Do not add severity labels.

## Hard Rules

- Specs, plans, and REVIEW.md live on main in `plans/<project-name>/`. They are the single source of truth.
- Reviewers must assess the implementation checkout — the project worktree if one exists, otherwise main.
- The worktree is identified by running `git worktree list` from main and matching the branch name against the project name.
- Reviewers must assess the **current repository state**, not diffs or history.
- Every spec section gets its own reviewer, and all reviewers run in parallel.
- `REVIEW.md` is a plain checklist. No summaries, no scores, no severity labels, no compliance judgement.
- If there are no issues at all, leave `REVIEW.md` unchanged and reply with "No issues."
- Consolidate new findings into the existing `REVIEW.md`. Do not delete it.
- A new finding that matches an existing item triggers re-evaluation. Check whether the original fix or wont-fix justification still holds against the new finding before changing the item's state.

## Common Mistakes

- **Reviewing the wrong checkout.** If a project worktree exists, review that checkout. If none exists, review main. Treating main as the implementation target when a worktree exists produces a stale review.
- **Looking at git diff instead of the present state.** The review is of the codebase as it exists, not what changed recently.
- **Running reviewers sequentially.** They must run in parallel so each section is audited independently.
- **Deleting the existing REVIEW.md.** Consolidate findings into it; do not replace it.
- **Adding severity or compliance commentary.** The output is a checklist of items to fix, full stop.
- **Duplicating items that already exist.** Cross-reference new findings against the existing checklist and merge when they match.
