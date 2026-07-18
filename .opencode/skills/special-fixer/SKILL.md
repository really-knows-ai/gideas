---
name: special-fixer
description: Fix items from a review checklist produced by special-review. Groups items by target file, then dispatches one implementer per file group to avoid stampedes and reduce redundant reads. Each implementer verifies the claim before acting — if they disagree they mark [~] wont-fix. Reads LEARNINGS.md from the companion review directory and provides it to implementers.
---

# Special Fixer

Fix items from a review checklist (produced by `special-review`).  Items are
grouped by target file, then each file group is handled by one implementer who
processes all items for that file sequentially — first verifying each claim,
then fixing or marking wont-fix.  The implementer treats every claim with
serious skepticism — the default stance is "prove it before you touch
anything."

The implementer also reads the companion `LEARNINGS.md` file (if present)
so they understand established patterns and do not fix things in a way that
violates prior learnings.

The implementer may disagree with the reviewer.  That's expected.  If the
implementer believes the reviewer is wrong, they mark the item `[~]` wont-fix
with a clear justification.  A subsequent review may re-open the item with
`[!]` — at which point the implementer reads the re-opening reason, re-evaluates,
and either accepts it (fix → `[x]`) or holds the line (`[~]` again with an
updated justification).  There is no deadlock-breaking authority — the cycle
continues until one side concedes or a human intervenes.

## Workflow

### 1. Select the review file

The user provides the path to a review checklist.  If they do not, ask for it.
The file must exist and must contain at least one `- [ ]` or `- [!]` item.
If no open items exist, stop and report that there is nothing to fix.

Read the full review file.  Note every item and its state.

Also read the companion `LEARNINGS.md` file if it exists (same directory as
the review file).  Its contents will be provided to implementers so they
understand established patterns and constraints.

Read `AGENTS.md` in the repository root (if it exists) for project-structure
context.  Extract a one-paragraph summary describing what kind of project
this is — e.g., whether it uses a phased-plan workflow under `plans/`,
whether the review targets plan documents or source code, and any relevant
conventions.  This context is passed to implementers.

### 2. Group items by target file

Parse every `- [ ]` and `- [!]` item to identify its **primary target file**
— the first file reference in the item text (e.g., `PHASE_01.md:500-503` →
primary file is `PHASE_01.md`).  Group items by this primary file.

Items with no file reference go into a `general` group.

Before dispatching, validate each item's file+line references:
- If a referenced file does not exist, note it for the implementer.
- If a referenced line number falls outside the file's current length, adjust
  or flag the reference.

Also determine the **file category** for each group:
- **plan files** — files under a `plans/` directory
- **code files** — all other source files

This determines which quality gate applies (see Hard Rules).

### 3. Dispatch one implementer per file group

For each file group, dispatch one `@implementer` subagent.  All implementers
run in parallel.  Each receives this prompt:

```
You are responsible for fixing all review items assigned to <FILE>.
Handle them in the order listed below.

**Project context:**
[one-paragraph summary extracted from AGENTS.md]

**Your file:** <FILE>
**Read-only context files** (read for context, do not edit):
<comma-separated list of secondary files referenced by items in this group>

**Handle these items in order:**

1. [verbatim item 1]
2. [verbatim item 2]
...

**Criteria:**
[criteria from the review header]

**Prior learnings (follow these rules during the fix):**
[contents of LEARNINGS.md, if it exists — otherwise "None."]

**Rules:**
1. Read <FILE> once.  Then process each item in order, applying fixes
   sequentially within the same file.  Do not re-read the file between
   items.
2. For each item, verify the claim first — do not take the reviewer's word
   at face value.
3. If you agree the claim is valid:
   - Apply the minimum fix that resolves the divergence, respecting Prior
     learnings.
   - Report: "Item N — Fixed: <what you changed and why.>"
4. If you disagree with the claim:
   - Do not change any code for that item.
   - Report: "Item N — Wont-fix: <clear explanation with evidence — file
     paths, line numbers, spec sections.>"
5. If two items conflict (e.g., one adds a method, another removes it), do
   not guess.  Report both as `conflict` with an explanation.
6. If an item references additional files outside your primary file, read
   them for context only.  Do not edit them.
7. Make no changes beyond what the items require.  No opportunistic
   refactoring.  No bonus fixes.
8. Run the quality gate after ALL fixes are applied (see below).  Do not
   run it between items.

**Quality gate:**
- If file category is "plan files": no build gate.  Verify only that the
  file parses as valid Markdown (e.g., no unclosed code fences).
- If file category is "code files": run `make check-fix` and `go test ./...`
  (or the relevant subset) on the changed code.
- Report the quality gate outcome after your last item.
```

Wait for all implementers to complete before proceeding.

### 4. Update the review checklist

For each implementer result:

- **Fixed:** change `- [ ]` or `- [!]` to `- [x]` on the item's line.
  Append an indented line: `- Fixed: <summary of the change.>`.
- **Wont-fix:** change `- [ ]` or `- [!]` to `- [~]`.  Append an indented
  line: `- Wont-fix: <the implementer's justification.>`.
  If the item was `[!]` (re-opened), the wont-fix justification must also
  address the re-opening reason.  Format:
  ```
  - [!] file.go:10 — Description.
    - Re-opened: <reviewer's reason.>
  ```
  becomes:
  ```
  - [~] file.go:10 — Description.
    - Re-opened: <reviewer's reason.>
    - Wont-fix: <why the implementer still disagrees despite the re-opening reason.>
  ```

### 5. Write the updated review

Write the modified review file back to its original path.  Do not add summary
text — only update item states and append the fix/wont-fix detail lines.

### 6. Report to the user

Report:
- Number of items fixed (`[ ]` → `[x]`)
- Number of items marked wont-fix (`[ ]` → `[~]`)
- Number of re-opened items fixed (`[!]` → `[x]`)
- Number of re-opened items held (`[!]` → `[~]`)
- Output file path

List each item with its outcome and a one-line summary.

## Wont-fix guidance

An implementer marks an item `[~]` when:

- **The claim is factually wrong.**  The reviewer says "variable X is unused"
  but X is used on line 42.  Evidence: file + line number.
- **The claim contradicts a spec requirement.**  The reviewer says "remove
  retry logic" but the spec mandates retries.  Evidence: spec section
  reference.
- **The claim is correct but fixing it would cause harm.**  The reviewer
  says "use sync.Map" but the code is in a hot path where sync.Map's
  overhead matters.  Evidence: benchmark output or documented invariant.
- **The claim is a style preference mislabeled as a divergence.**  The
  reviewer says "use named returns" but the code uses explicit returns —
  both are valid, neither contradicts any criteria.
- **The reviewer re-opened with `[!]` but the implementer still disagrees.**
  The implementer reads the re-opening reason, re-evaluates, and either
  concedes (fix it → `[x]`) or holds the line (`[~]` with updated
  justification that addresses the re-opening reason).

## Hard Rules

- Fix exactly the items in the review.  Do not fix things not listed.
- Verify before acting.  Trust the code, not the reviewer.
- `[x]` items left as-is — they were previously resolved and verified by
  `special-review`.  Do not re-fix them unless they were re-opened as `[!]`.
- `[~]` items left as-is — they were previously marked wont-fix and the
  justification was verified by `special-review`.  Do not re-evaluate
  unless they were re-opened as `[!]`.
- **Quality gate depends on file category:**
  - **Plan files** (under `plans/`): no build gate.  Verify the file is
    valid Markdown (no unclosed code fences, broken tables, etc.).
  - **Code files** (all other): run `make check-fix` and `go test ./...`
    (or the relevant subset) on changed code before reporting a fix.
- Do not commit.  This skill only updates the review checklist and modifies
  source files.  Committing is a separate step.
- No severity judgements in wont-fix justifications.  Just explain why the
  item should not be fixed.
- **Respect Prior learnings.**  If a learning says "no hardcoded line
  numbers", do not introduce new hardcoded line numbers in your fix.

## Common Mistakes

- **Fixing without verifying.**  The implementer must read the code first.
  Blindly accepting the reviewer's claim and applying a fix is the
  cardinal sin of this process.
- **Disagreeing without evidence.**  A wont-fix justification that says
  "I disagree" with no supporting evidence is useless.  Every wont-fix
  must cite a file path, line number, spec section, test output, or other
  concrete evidence.
- **Fixing unrelated things.**  If the item says "rename X to Y" and the
  implementer also renames Z and refactors W, that's wrong.  One item,
  one fix.
- **Re-evaluating `[x]` items.**  Items marked `[x]` were verified by the
  reviewer during the merge step of `special-review`.  The implementer
  only touches them if the reviewer re-opened them as `[!]`.
- **Treating `[!]` as a command.**  A re-opened item is the reviewer saying
  "I checked and the fix no longer holds."  The implementer re-evaluates
  — they may agree (fix) or disagree (wont-fix again).  Neither outcome
  is a failure.
- **Ignoring Prior learnings.**  If a learning says "use section headings
  not line numbers" and the implementer fixes a cross-reference by updating
  the line number, that fix will be rejected on the next review pass.
