---
name: special-review
description: Review requested files against provided criteria and produce a flat checklist of issues. Verifies and prunes prior resolved/wont-fix items upfront, captures learnings, then runs a full fresh review with parallel subagents. Deduplicates against learnings and prior open items.
---

# Special Review

Review requested files against provided criteria. Produce a flat checklist of
issues at the given output path. Make no severity judgements, no ranking, no
blocker-versus-non-blocker classification — if something diverges from the
provided criteria, it goes in the list.

Every invocation is a **full-rigour fresh review**. Prior resolved (`[x]`) and
wont-fix (`[~]`) items are verified and pruned *before* the fresh review runs,
so the review always starts from a clean base.  Reviewers are provided with
`LEARNINGS.md` so they do not re-flag already-documented patterns.

## ⚠️ Pre-flight: READ THIS FIRST

**Before any tool call**, answer these three questions in your initial response:

1. Does REVIEW.md contain `[x]` or `[~]` items? → If yes, go to **Step 2**.
   Do NOT read any plan files or code — only read REVIEW.md and LEARNINGS.md.
2. Does the user's request include files, criteria, and an output path? →
   If missing, ask. Do NOT proceed without them.
   - **Implicit output path:** If the user references a project directory
     (e.g. "cartographer") and `plans/<project>/REVIEW.md` already exists,
     that file is the implicit output path. Do not ask.
3. Have you already read any target file (plan files, source code, etc.)? →
   If yes, stop. You have violated the skill. Report the error to the user.

### Golden Rule

**Never read target files yourself.** The subagents read them. Your job is
dispatch, not investigation. If you find yourself reading anything other than
REVIEW.md, LEARNINGS.md, or the criteria document, stop — you are violating
the skill.

## Workflow

### 1. Read REVIEW.md and determine what needs reviewing

The existing REVIEW.md is the starting point.  If the output file path
points to an existing REVIEW.md, read it:

- **If REVIEW.md contains `[x]` or `[~]` items** — those claims need
  verifying first.  Proceed to Step 2 (verify and prune).  Do NOT gather
  criteria or target file lists yet; the verification subagents only need
  the file references in each item, not the full target set.
- **If REVIEW.md has only `[ ]` and `[!]` items, or REVIEW.md does not
  exist** — there is nothing to verify.  Skip to Step 3 (gather inputs for
  fresh review).

Read the companion `LEARNINGS.md` if it exists (same directory as
REVIEW.md, named `LEARNINGS.md`).

Parse the actual checklist entries in REVIEW.md (not the summary header
counts, which may be stale):

- `- [ ]` — open, not yet addressed
- `- [x]` — previously fixed and approved
- `- [~]` — previously marked wont-fix with a justification
- `- [!]` — previously re-opened (a prior review found a resolved or
  wont-fix item was no longer valid)

### 2. Verify and prune existing review (separate phase)

This is a **separate phase** from the fresh review below.  It runs only when
REVIEW.md contains `[x]` or `[~]` items.

**Only the items listed in the file matter** — ignore the summary header
counts.  If the file lists 10 items but the header says 233, you verify
the 10 items that are actually present.

**How to dispatch verification subagents:**
Use `task(subagent_type: "reviewer")` for each item (or small batch of
related items).  The verification subagent does NOT run a full review of
the target files — it reads only the specific file(s) and line(s) referenced
by each item to check whether the claim still holds.  Do NOT read the target
files yourself in this step.

Each verification subagent receives this prompt:

```
Verify whether the following prior review item(s) still hold against the
current code.  This is not a fresh review of the whole file — you are only
checking whether the specific claim(s) below remain valid.

**Target files to read:**
[list of files referenced by the item(s)]

**Prior item(s) to verify:**

1. `[x] <file>:<line> — <description>`
   - Previously marked fixed.  Check whether the divergence described
     is still absent from the current code.
2. `[~] <file>:<line> — <description>`
   - Previously marked wont-fix with justification: <justification>.
     Check whether that justification is still valid given the current
     code state.

**Rules:**
- Read the relevant lines in the target files to determine current state.
- For each item, report one of:
  - **Verified** — the claim still holds (fix is still in place /
    justification is still valid).
  - **Re-opened** — the claim no longer holds, with a specific
    explanation of what changed.
- Be precise: reference the file and line numbers you checked.

**Output format:**
For each verified item:
`VERIFIED <state> <file>:<line> — <description>`

For each re-opened item:
`REOPENED <file>:<line> — <description>
  - Re-opened: <specific reason the claim no longer holds.>`
```

Wait for all verification subagents to complete.

**You may NOT proceed to Step 3 until the following three substeps are all
complete.  A reading of the subagent reports is not enough — you must
materially edit the review file and learnings file.**

### 2a. Process results

- Items reported as `VERIFIED` → mark for pruning.
- Items reported as `REOPENED` → change to `- [!]` with the re-opened
  explanation from the subagent.

If any verification subagent reports an unexpected state or ambiguity,
re-read the relevant file manually to resolve.

### 2b. Capture learnings

Scan the verified `[x]` and `[~]` items for recurring patterns that would
be useful for future reviewers.  For each distinct pattern, append an entry
to `LEARNINGS.md` (create it if it does not exist).  A learning entry is a
concrete rule that captures *what was learned*, not a specific finding:

```markdown
## Cross-References

- **No hardcoded line numbers in prose.** Use section headings (`§R6: Operator
  Reconciliation → spec changes`) instead of `R6 lines 389-394`.
- **Cross-references between sections** must be precise.  Referencing the
  wrong section is a common source of stale bugs.
```

Do not add a learning for a pattern that is already documented in
`LEARNINGS.md`.

### 2c. Delete verified items from the review file

Remove all verified `[x]` and `[~]` items from the review.  Re-opened `[!]`
items and pre-existing `[ ]` items remain.  Their resolution history is
preserved in git history.

Only after all three substeps are done should you proceed to Step 3.

After this step, the review file contains only `[ ]` and `[!]` items.

### 3. Gather inputs for the full-rigour review

Before the fresh review can run, three things are needed:

1. **Files to review** — a list of file paths (glob patterns accepted).
   The user typically provides these in their message.  If missing, ask.
2. **Criteria** — what to check for.  This can be a file path (e.g. a
   spec document), inline text, or a description of the review standard.
3. **Output file path** — already known from Step 1.  If it was never
   established, check whether the user referenced a project directory
   with an existing `plans/<project>/REVIEW.md`.  If so, use that as the
   implicit output path.  Otherwise, ask the user.

Read the criteria.  If it is a file path, read the file.  If it is inline
text, use it directly.  If it is a description, treat it as the review
standard.

**Do NOT read the target files here.**  The full-rigour subagents
(Step 4) will read the files they need.  Your job is to partition the
work and dispatch.

If any of the three is missing, ask the user before proceeding.

### 4. Dispatch reviewer subagents — fresh full-rigour review

Break the review into independent units by separating target files or criteria
sections.  Use `task(subagent_type: "reviewer")` to dispatch one subagent per
unit, all in parallel.

**Do NOT read the target files yourself** — the subagents read them.  Your
job is to partition the work and dispatch.  If you already read parts of a
target file (e.g., during verification), the subagents still read the full
file for their complete assessment.

The fresh review is **full rigour** — it applies the provided criteria
comprehensively, as if for the first time.  The existence of prior review
passes does not reduce the depth or scope of this review.  Each subagent
receives this prompt:

```
Full-rigour review: assess these target files against the provided criteria
as if for the first time.  Produce a flat checklist of issues.  Make no
severity judgements — if something diverges from the criteria, it goes in
the list.

**Target files to review:**
[list of one or more file paths]

**Criteria:**
[the relevant criteria for this review unit]

**Prior learnings (do not re-flag documented patterns):**
[contents of LEARNINGS.md — if empty, "None."]

**Rules:**
- No severity labels.  No ranking.  No "blocker" vs "minor."
- If the target does something that contradicts or deviates from the
  criteria, list it as a finding.  Be specific: include file paths and
  line numbers.
- If a criteria requirement is not addressed at all by the target files,
  list that as a finding.
- Do not flag stylistic preferences.  Do not flag patterns that work but
  could be written differently.
- Do not review the criteria itself.  Only review the target files against
  the criteria.
- **Do not re-flag patterns documented in Prior learnings.**  If a learning
  says "no hardcoded line numbers", do not flag stale line numbers.  If a
  learning says "every RPC needs a response type", do not flag missing
  proto types unless the target introduces a NEW RPC not covered by the
  learning.
- This is a full-rigour pass.  Prior review passes do not reduce the depth
  or scope of this review.  Every file and every line is assessed against
  the criteria.

**Output format per finding:**
`- [ ] <file>:<line> — <description of the divergence.>`

If you find no divergences, respond with "No findings."
```

Wait for all subagents to complete before proceeding.

### 5. Collect, deduplicate, and consolidate findings

**Deduplicate across reviewers:**
Gather all findings from the subagents.  Compare by file, line, and
description.  If the same finding appears in multiple reviewer outputs,
keep only one copy.

**Remove findings covered by LEARNINGS.md:**
For each remaining finding, check whether it matches a pattern already
documented in `LEARNINGS.md`.  If it does, discard it — the learning
already captures the issue.

**Merge with existing open items:**
Take the pre-existing `[ ]` and `[!]` items (carried forward from step 2).
For each finding from the fresh review, check whether it matches an
existing `[ ]` or `[!]` item (by file, line, and description):

- If a matching `[ ]` item exists → keep the existing item (don't duplicate).
- If a matching `[!]` item exists → keep the existing item (don't duplicate).
- If no matching item exists → append the new `- [ ]` item.

Existing `[ ]` and `[!]` items that are NOT re-discovered by the fresh
review remain as-is — they are still open and unaddressed.

### 6. Write the consolidated review

Write the complete checklist to the output file path.  Include a header with
the review date, files/criteria reviewed, and summary counts:

```markdown
# Special Review

**Date:** <today's date>
**Files reviewed:** <list of target files>
**Criteria:** <criteria summary or file path>

## Summary

| State | Count |
|-------|-------|
| `[ ]` Open | <count> |
| `[!]` Re-opened | <count> |

## Open Items

- [ ] ...
```

Do not add commentary, summaries, or recommendations outside the checklist.

### 7. Report to the user

Report:
- Number of `[x]` items verified and pruned (how many were still fixed)
- Number of `[~]` items verified and pruned (how many were still valid)
- Number of `[!]` items re-opened from prior `[x]` or `[~]` claims
- Number of new findings from fresh review
- Number of fresh findings removed because covered by `LEARNINGS.md`
- Number of pre-existing `[ ]` items carried forward
- Number of pre-existing `[!]` items carried forward
- Number of learnings added/updated in `LEARNINGS.md`
- Output file path

## Checklist format rules

Every item follows this structure:

```
- [<state>] <location> — <description>
  - <detail line, if needed>
```

- `<state>` is one of ` ` (open), `x` (resolved), `~` (wont-fix), `!` (re-opened).
- `<location>` is a file path with optional line number (`file.go:42`).
- `<description>` is a single sentence describing the divergence from
  criteria.  No severity words allowed (no "critical," "minor," "blocker,"
  "nice-to-have," etc.).
- Indented sub-lines carry extra detail, findings from a re-review, or
  wont-fix justifications.

## Wont-fix items

When an implementer marks an item as `- [~]`, they must add an indented
justification:

```
- [~] file.go:10 — Uses hand-rolled sort instead of sort.Slice.
  - Wont-fix: the custom sort is required because the comparison is
    stable-across-runs and sort.Slice is not guaranteed stable.
```

The reviewer checks whether the justification is still valid.  If the
justification no longer applies (e.g. the code was refactored and the
hand-rolled sort was replaced by something else entirely), the item is
re-opened.

## Re-opened items

When an `[x]` or `[~]` item is found to no longer be valid, it is changed
to `- [!]` with an explanation:

```
- [!] file.go:10 — Missing error check on os.Create.
  - Re-opened: the error check was present in v1 but was removed in a
    subsequent refactor (line 10 no longer has `if err != nil`).
```

## Boundaries

- This skill only reviews.  It does not fix anything.
- This skill makes no severity judgements.  Every divergence from criteria
  is listed.  The implementer decides what to fix, what to defer, and what
  to mark wont-fix.
- The reviewer subagents are given the same instruction: no severity labels,
  no ranking, just divergences from criteria.
- If the user does not provide all three inputs (files, criteria, output
  path), ask for what's missing — do not guess.
- The output file is always written to the provided path.  It may be
  gitignored (under `plans/`) or tracked — the skill does not commit.
- The companion `LEARNINGS.md` is written to the same directory as the
  review file.
