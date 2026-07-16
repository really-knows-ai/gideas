---
name: special-review
description: Review requested files against provided criteria and produce a flat checklist of issues in an output file. No severity judgements — if something diverges from the criteria it goes in the list. Merges new findings with any existing review file, re-verifying completed and wont-fix items. Consolidates the review on each pass by removing resolved items and distilling learnings.
---

# Special Review

Review requested files against provided criteria. Produce a flat checklist of
issues at the given output path. Make no severity judgements, no ranking, no
blocker-versus-non-blocker classification — if something diverges from the
provided criteria, it goes in the list.

If the output file already exists, perform a fresh review of the requested
files, then merge new findings into the existing review.  Previously resolved
items (`[x]`) and wont-fix items (`[~]`) must be re-verified against the current
state.  Items that no longer pass are re-opened with `[!]`.

The special-review also manages a companion `LEARNINGS.md` file alongside
the review output.  On each pass, it consolidates the review by removing
resolved and wont-fix items from the main checklist, and distills key
pattern-level learnings into `LEARNINGS.md`.  These learnings are then fed
to subagents on subsequent passes so they don't re-flag the same class of
issue.

## Workflow

### 1. Gather inputs from the user

The user must provide three things:

1. **Files to review** — a list of file paths (glob patterns accepted).  These
   are the target files assessed against the criteria.
2. **Criteria** — what to check for.  This can be a file path (e.g. a spec
   document), inline text, or a description of the rules to apply.
3. **Output file path** — where to write the checklist.  If it points into
   `plans/<project>/REVIEW.md`, it follows the project review convention.
   Otherwise any writable path is accepted.

If any of the three is missing, ask the user to provide it before proceeding.

### 2. Read the targets, criteria, and learnings

Read every target file the user listed.  If any file does not exist or cannot
be read, report the missing path and stop.

Read the criteria.  If it is a file path, read the file.  If it is inline
text, use it directly.  If it is a description, treat it as the review
standard.

Read the companion `LEARNINGS.md` file if it exists (same directory as the
output file, named `LEARNINGS.md`).  If present, its contents must be
provided to reviewer subagents as prior guidance — reviewers should not
flag issues that are already documented as resolved learnings.

### 3. Read the existing review (if present)

If the output file already exists, read it in full.  Note every item and its
state:

- `- [ ]` — open, not yet addressed
- `- [x]` — previously fixed and approved
- `- [~]` — previously marked wont-fix with a justification
- `- [!]` — previously re-opened (a prior review found a resolved or
  wont-fix item was no longer valid)

### 4. Dispatch reviewer subagents

Break the review into independent units by separating target files or criteria
sections.  Dispatch one `@reviewer` subagent per unit, all in parallel.

Each subagent receives this prompt:

```
Review these target files against the provided criteria.  Produce a flat
checklist of issues.  Make no severity judgements — if something diverges
from the criteria, it goes in the list.

**Target files to review:**
[list of one or more file paths]

**Criteria:**
[the relevant criteria for this review unit]

**Prior learnings (do not re-flag these patterns):**
[contents of LEARNINGS.md, if it exists — otherwise "None."]

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

**Output format per finding:**
`- [ ] <file>:<line> — <description of the divergence.>`

If you find no divergences, respond with "No findings."
```

Wait for all subagents to complete before proceeding.

### 5. Collect and consolidate findings

Gather all findings from the subagents.  This is the **fresh review** result —
a flat list of `- [ ]` items.

### 6. Merge with existing review (if present)

If an existing review file exists, cross-reference the fresh findings against it:

**Verifying `[x]` items:**
For each item previously marked `- [x]`, check whether the divergence it
described still exists in the current code.  Read the relevant files and
verify the fix is still in place.

- If the fix holds → keep `- [x]`.
- If the divergence has returned → change to `- [!]` and add an indented
  explanation: `- Re-opened: <reason the fix no longer holds.>`

**Checking `[~]` items:**
For each item marked `- [~]`, read the wont-fix justification.  Determine
whether the justification is still valid given the current code state.

- If the justification still holds → keep `- [~]`.
- If the justification no longer holds (e.g. the code changed and the
  reason no longer applies) → change to `- [!]` and add:
  `- Re-opened: <reason the justification no longer holds.>`

**Merging new findings:**
For each finding from the fresh review, check whether the existing review
already covers the same issue (by comparing file, line, and description).
Also check whether the finding is covered by a learning in `LEARNINGS.md`.

- If an existing `- [ ]` item matches → keep the existing item (don't
  duplicate).
- If an existing `- [x]` item matches but the divergence is back → change
  to `- [!]` (handled above).
- If the finding matches a learning in `LEARNINGS.md` → do NOT append it.
  The learning already captures this pattern as a known issue.
- If no existing item matches and no learning covers it → append the
  new `- [ ]` item.

**Preserving open items:**
Existing `- [ ]` items that are NOT covered by the fresh review findings
remain as-is — they are still open and unaddressed.

### 7. Consolidate the review and update learnings

After every third pass (or when the user requests consolidation):

**Prune resolved items:** Remove all `- [x]` and `- [~]` items from the
checklist.  Their resolution history is preserved in the git history of the
review file.

**Distill learnings:** Scan the removed `[x]` and `[~]` items for recurring
patterns.  For each pattern that appeared 3+ times across passes, add an
entry to `LEARNINGS.md` (same directory as the review file).  A learning
entry is a concrete rule, not a specific finding:

```
## Cross-References

- **No hardcoded line numbers in prose.** Use section headings (`§R6: Operator
  Reconciliation → spec changes`) instead of `R6 lines 389-394`.
- **Cross-references between sections** must be precise.  Referencing the
  wrong section is a common source of stale bugs.
```

Learnings are fed to reviewer subagents on subsequent passes so they do not
re-flag the same class of issue.

**Update the header:** Replace the review pass counter with a consolidated
header showing total resolved/wont-fix/open counts.

### 8. Write the merged review

Write the complete merged checklist to the output file path.  Include a
header with the review date, files/criteria reviewed, and summary counts:

```markdown
# Special Review

**Date:** <today's date>
**Files reviewed:** <list of target files>
**Criteria:** <criteria summary or file path>

## Summary

| State | Count |
|-------|-------|
| `[x]` Resolved | <count> |
| `[~]` Wont-fix | <count> |
| `[ ]` Open | <count> |

## Open Items

- [ ] ...
```

Do not add commentary, summaries, or recommendations outside the checklist.

### 9. Report to the user

Report:
- Number of new findings added
- Number of `[!]` items re-opened
- Number of `[x]` items verified (still fixed)
- Number of `[~]` items verified (still valid)
- Number of pre-existing `[ ]` items carried forward
- Number of items removed during consolidation (if any)
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
