---
name: special-fixer
description: Fix items from a review checklist (REVIEW_ITEMS.md) produced by special-review. Groups items by target file, then dispatches one implementer per file group in an isolated git worktree so implementers can run in parallel without clashing on the shared working tree. Each implementer verifies the claim before acting — if they disagree they mark [~] wont-fix. After implementers finish, merges the worktree branches back into main, dispatching implementers to resolve any conflicts. Reads LEARNINGS.md from the companion review directory and provides it to implementers.
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

## Classification Tags

Review items use three prefixes to signal what kind of resolution is needed:

| Tag | Meaning | Implementation |
|-----|---------|---------------|
| `[FIX]` | Plan text is wrong — must be corrected, not just annotated. The instructions as written would mislead or produce broken code. | Edit the plan document to correct the error. |
| `[PONYTAIL]` | Deliberate simplification with a known ceiling. The plan is correct in intent but trades off completeness for simplicity. | Add a `ponytail:` comment documenting the known ceiling and upgrade path per AGENTS.md convention. If the implementer determines the ceiling is actually a SPEC divergence (the criteria mandates the behaviour, the code does not implement it), the item was misclassified — treat it as `[FIX]` and either update the SPEC or update the code, or mark `[~]` wont-fix citing the SPEC mismatch. |
| `[IMPL-NOTE]` | Plan is correct but incomplete — the implementer needs additional context at coding time that the plan doesn't provide. | Add a note, annotation, or cross-reference to the plan document for the implementer's benefit. |

**`[IMPL-NOTE]` scope:** This tag applies when reviewing **phased plans and specification documents** — it does NOT apply when reviewing actual implementation source code against a spec or plan. In source-code reviews, a divergence is either `[FIX]` (the code doesn't match the spec) or not a finding — there is no "the code is correct but the implementer needs context" category.

When an implementer encounters `[IMPL-NOTE]` on a plan or spec file, the fix is to add clarifying prose to the document — no source code changes are involved.

Unclassified items (no tag prefix) default to `[FIX]` — the plan is assumed wrong.

## Workflow

### 1. Select the review checklist

The user provides the path to a review checklist (`REVIEW_ITEMS.md` from
`special-review`).  If they do not, ask for it.  The file must exist and
must contain at least one `- [ ]` or `- [!]` item.
If no open items exist, stop and report that there is nothing to fix.

Read the full checklist file.  Note every item and its state.

Also read the companion `REVIEW.md` (same directory) for the review criteria
and the `LEARNINGS.md` file if it exists (same directory as the checklist).
`LEARNINGS.md`'s path will be included in the implementer prompt so
implementers read it themselves and follow established patterns and constraints.

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

### 3. Dispatch one implementer per file group (parallel, isolated worktrees)

For each file group, dispatch one `@implementer` subagent.  **Code-file
groups run IN PARALLEL by default**, each in its own isolated git worktree,
so they never clash on the shared working tree.  **Plan-file groups run
SERIALLY** in the main working tree — plan files live under `plans/`,
which is gitignored by design and therefore does not exist in any branch or
worktree, so there is no worktree to isolate them in and nothing to merge.

**Use your judgement:** if code-file groups touch the same module or have interdependent changes, serialise some or all of them to
avoid merge conflicts.  Worktree isolation prevents filesystem clashes but
does not prevent semantic conflicts — serialising reduces merge-phase churn.

Before dispatching, determine each group's file category (from Step 2):

- **code groups** — primary file is tracked source (not under `plans/`).
  Dispatch in parallel; each implementer works in its own worktree.
- **plan groups** — primary file under `plans/` (gitignored).  Dispatch
  in parallel; the implementer works in the main working tree and does not
  commit (the change is not tracked).  Plan files are partitioned by file
  so groups never edit the same file.

Derive a **group slug** from each group's primary file: lowercase the
relative path and replace every non-alphanumeric character with `-`
(`platform/sidecar/server.go` → `platform-sidecar-server-go`).  Assign each
code group a branch and worktree:

- Branch: `fix/special-fixer/<group-slug>`
- Worktree: `.worktrees/special-fixer/<group-slug>`
- Base: the current branch of the main working tree (usually `main`)

If the branch name already exists from a previous run, append `-02` (then
`-03`, …) to both the branch and the worktree path.

#### 3a. Code-group implementer prompt (parallel, worktree-isolated)

Dispatch all code-group implementers in parallel — issue every
`task(subagent_type: "implementer")` call up front and wait for all of
them to complete.  Each receives this prompt:

```
You are responsible for fixing all review items assigned to <FILE>.
Handle them in the order listed below.  You work in an isolated git
worktree so you can run in parallel with other implementers without
clashing on the shared working tree.

**Project context:**
[one-paragraph summary extracted from AGENTS.md]

**Your file:** <FILE>
**Read-only context files** (read for context, do not edit):
<comma-separated list of secondary files referenced by items in this group>

**Isolated worktree setup:**
1. Create your worktree and branch off <BASE>:
   `git worktree add -b <BRANCH> <WORKTREE-PATH> <BASE>`
   If the branch already exists, append `-02` (then `-03`, …) to both the
   branch and the worktree path and retry.
2. Do ALL of your work inside <WORKTREE-PATH> — read <FILE> there, edit it
   there, and run quality gates from there (cd into the worktree first).
3. Context files that live under plans/ (REVIEW.md, LEARNINGS.md, criteria
   docs) are gitignored and NOT present in the worktree — read them from
   the main working tree at their absolute path
   (e.g. /Users/jledrew/platform/plans/<project>/LEARNINGS.md).
4. When all fixes are done and the quality gate passes, commit your work on
   your branch inside the worktree:
   `git add <changed files>` then `git commit -m "fix: resolve review items in <FILE>"`
   Stage and commit only the files your items required you to change.  Do
   not merge and do not push.
5. Report: each item's outcome, the branch name, the worktree path, the
   commit hash, and the quality-gate outcome.

**Handle these items in order:**

1. [verbatim item 1]
2. [verbatim item 2]
...

**Criteria:**
[criteria from REVIEW.md in the review directory]

**Prior learnings (read this file and follow its rules during the fix):**
[path to LEARNINGS.md in the review directory, if it exists — otherwise "None."]

**Rules:**
1. Read <FILE> once.  Then process each item in order, applying fixes
    sequentially within the same file.  Do not re-read the file between
    items.
2. For each item, verify the claim first — do not take the reviewer's word
    at face value.
3. **Classify the fix type based on the item's tag prefix:**
     - `[FIX]` (or unclassified): The plan text is wrong.  Correct it.
     - `[PONYTAIL]`: The plan is intentionally simplified.  Add a `ponytail:`
       comment documenting the known ceiling and upgrade path per AGENTS.md.
       **But first check:** if the ceiling the item describes is actually a
       SPEC divergence (the criteria mandates the behaviour and the code does
       not implement it), the item was misclassified — treat it as `[FIX]`
       and either update the SPEC or update the code, or mark `[~]` wont-fix
       citing the SPEC mismatch.  A `ponytail:` annotation alone is never a
       valid fix for a behaviour the SPEC requires.
     - `[IMPL-NOTE]`: The plan is correct but incomplete.  Add a note,
       prose, a cross-reference, or an annotation for the implementer.
       Only applies to plan/spec documents — for code files, treat as `[FIX]`
       (add missing documentation) or wont-fix (if the code is self-documenting).
4. If you agree the claim is valid:
    - Apply the minimum fix that resolves the divergence, respecting Prior
      learnings and the classification tag.
    - Report: "Item N [TAG] — Fixed: <what you changed and why.>"
5. If you disagree with the claim, or the claim is valid but is a
   "Tier-3" not-worth-fixing nice-to-have:
    - Do not change any code for that item.
    - Report: "Item N [~] — Wont-fix: <clear explanation with the evidence
      — file paths, line numbers, spec sections.>"
    - **Wont-fix on cost-benefit is allowed only for a Tier-3 item** — a
      doc/SPEC wording mismatch or layering note with no behaviour change,
      a re-flag of a trade-off that already carries a `ponytail:`, a
      speculative concern about a provably unreachable path, a niche
      edge-case test protecting a branch with no plausible regression, or
      a cosmetic nit in an untouched file. Your justification must state
      the cost-benefit grounds.
    - For a **Tier-1/Tier-2 item** (a real bug; a broken CLI/API; a
      spec-mandated error code or check-order surfaced wrong; a security /
      trust-boundary / data-loss / accessibility implication; or a missing
      test for a behaviourally significant likely-to-regress SPEC branch),
      wont-fix on cost-benefit grounds is NOT allowed. You must cite
      evidence that the claim is wrong or that the spec contradicts it.
6. If two items conflict (e.g., one adds a method, another removes it), do
    not guess.  Report both as `conflict` with an explanation.
7. If an item references additional files outside your primary file, read
    them for context only.  Do not edit them.
8. Make no changes beyond what the items require.  No opportunistic
    refactoring.  No bonus fixes.
9. Run the quality gate after ALL fixes are applied (see below).  Do not
    run it between items.

**Quality gate:**
- All verification runs through `make` — bare `go`/`buf` and env-prefixed
  commands are denied by the implementer agent's bash policy.  Run
  `make check-fix` plus the relevant test targets (`make test-<service>`
  for the changed service, `make test-sdk`, `make test-operator`, or
  `make verify`).  If a change touches `proto/**`, run `make proto` first
  to regenerate `gen/**` — never hand-edit generated code.
- Report the quality gate outcome after your last item.
```

#### 3b. Plan-group implementer prompt (parallel, main working tree)

Handle plan-group implementers in parallel.  Each receives this prompt:

```
You are responsible for fixing all review items assigned to <FILE>.
Handle them in the order listed below.  <FILE> is a plan document under
plans/, which is gitignored and exists only in the main working tree —
work directly in the main working tree and do NOT commit (the change is
not tracked).

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
[criteria from REVIEW.md in the review directory]

**Prior learnings (read this file and follow its rules during the fix):**
[path to LEARNINGS.md in the review directory, if it exists — otherwise "None."]

**Rules:**
1. Read <FILE> once.  Then process each item in order, applying fixes
    sequentially within the same file.  Do not re-read the file between
    items.
2. For each item, verify the claim first — do not take the reviewer's word
    at face value.
3. **Classify the fix type based on the item's tag prefix:**
     - `[FIX]` (or unclassified): The plan text is wrong.  Correct it.
     - `[PONYTAIL]`: The plan is intentionally simplified.  Add a `ponytail:`
       comment documenting the known ceiling and upgrade path per AGENTS.md.
     - `[IMPL-NOTE]`: The plan is correct but incomplete.  Add a note,
       prose, a cross-reference, or an annotation for the implementer.
4. If you agree the claim is valid:
    - Apply the minimum fix that resolves the divergence, respecting Prior
      learnings and the classification tag.
    - Report: "Item N [TAG] — Fixed: <what you changed and why.>"
5. If you disagree with the claim, or the claim is valid but is a
   "Tier-3" not-worth-fixing nice-to-have:
    - Do not change any code for that item.
    - Report: "Item N [~] — Wont-fix: <clear explanation with the evidence
      — file paths, line numbers, spec sections.>"
6. If two items conflict (e.g., one adds a method, another removes it), do
    not guess.  Report both as `conflict` with an explanation.
7. If an item references additional files outside your primary file, read
    them for context only.  Do not edit them.
8. Make no changes beyond what the items require.  No opportunistic
    refactoring.  No bonus fixes.
9. Run the quality gate after ALL fixes are applied (see below).  Do not
    run it between items.

**Quality gate:**
- Plan files: no build gate.  Verify only that the file parses as valid
  Markdown (e.g., no unclosed code fences).
- Report the quality gate outcome after your last item.
```

Wait for all code-group implementers and all plan-group
implementers to complete before proceeding to the merge phase.

### 4. Merge the worktree branches back into main

Only code groups produced branches; plan-file fixes live directly in the
main working tree and have nothing to merge.  This phase requires `git
merge`, `git worktree`, and `git branch` bash permissions — the coordinator
agent has them.

For each code-group branch, in dispatch order:

1. **Ensure the main working tree is on the base branch:**
   `git checkout <BASE>` (or `git switch <BASE>`).
2. **Merge the branch:**
   `git merge <BRANCH>`
   - A clean fast-forward or merge commit → the branch is in.  Proceed.
   - A conflict → resolve it by dispatching an implementer (see below).
3. **Clean up the merged worktree and branch:**
   `git worktree remove --force <WORKTREE-PATH>`
   `git branch -d <BRANCH>`

If the merge reports conflicts, dispatch ONE implementer to resolve them in
the main working tree (the merge is left in progress there).  Wait for it
to complete, then continue with the next branch:

```
You are resolving merge conflicts from merging branch <BRANCH> into
<BASE> in the main working tree.  A `git merge` is in progress and left
the working tree with conflicts.

**Rules:**
1. Run `git status` to see which files conflict.
2. For each conflicted file, read it and resolve the conflict, preserving
   the intent of BOTH sides — the branch's fixes for its review items and
   the base branch's existing content.  Do not discard either side
   wholesale.
3. After resolving, run the quality gate (`make check-fix` plus the
   relevant `make test-<service>` targets) to confirm the merged result
   still passes.
4. Stage the resolved files: `git add <files>`.
5. Complete the merge commit: `git commit --no-edit` (uses the prepared
   merge message without opening an editor).
6. Report: which files you resolved and how, and the quality-gate outcome.
```

After every branch is merged and every worktree cleaned up, run the
merge-phase quality gate on the merged result in the main working tree:

- `make verify-check` (read-only gate; permitted for the coordinator).  If
  it fails, dispatch an implementer to fix the failures in the main working
  tree, commit the fix, and re-run until green.

### 5. Update the review checklist

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

### 6. Write the updated checklist

Write the modified checklist file back to its original path
(`REVIEW_ITEMS.md`).  Do not add summary text — only update item states and
append the fix/wont-fix detail lines.

### 7. Report to the user

Report:
- Number of items fixed (`[ ]` → `[x]`)
- Number of items marked wont-fix (`[ ]` → `[~]`)
- Number of re-opened items fixed (`[!]` → `[x]`)
- Number of re-opened items held (`[!]` → `[~]`)
- Merge phase: number of worktree branches merged into the base branch,
  any conflicts resolved (and how), any post-merge quality-gate failures
  fixed, and the worktrees cleaned up
- Output file path (`REVIEW_ITEMS.md`)

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
- **The claim is a style preference mislabeled as a divergence.**  A
  reviewer flags a named return, but explicit returns are equally valid.
  Both compile; neither contradicts the criteria.
- **The item is tagged `[PONYTAIL]` but is actually a SPEC divergence.**
  A `ponytail:` is for ceilings the SPEC does not address (e.g., "this
  global lock could become a bottleneck at >10K concurrent transactions"),
  not for ceilings the SPEC already forbids (e.g., "git operations don't
  have deadlines" when the SPEC mandates deadlines).  If the reviewer
  misclassified a SPEC divergence as `[PONYTAIL]`, the implementer marks
  it `[~]` and explains the mismatch — the fix is either to update the SPEC
  or to update the code.  A `ponytail:` annotation alone is not a valid fix
  for a behavior the SPEC requires.
- **The reviewer re-opened with `[!]` but the implementer still disagrees.**
  The implementer reads the re-opening reason, re-evaluates, and either
  concedes (fix it → `[x]`) or holds the line (`[~]` with updated
  justification that addresses the re-opening reason).
- **The claim is a Tier-3, not-worth-fixing finding.**  The reviewer kept an
  item whose true valid divergence is real but whose fix cost exceeds its
  benefit.  The implementer is authorized — even expected — to wont-fix it,
  using the same cost/value tiers `special-review` uses at consolidation to
  decide its own pruning.  Mark it `[~]` with the cost-benefit
  justification.

### Applying special-review's tiers when wont-fixing

`special-review` prunes a class of findings at Step 5c as **Tier 3** (not
worth fixing).  A `[~]` wont-fix by the implementer is the same decision
reached a step later, and should use the same tiers:

- **Tier 1 — must fix (do NOT wont-fix).**  A real bug, a broken CLI/API, a
  spec-mandated error code or check-order surfaced incorrectly, a
  security / trust-boundary / data-loss / accessibility implication, a
  missing test for a behaviourally significant likely-to-regress SPEC
  branch, or anything whose resolution changes observable behaviour.  Mark
  these fixed (`[x]`).  Wont-fixing a Tier 1 item requires a concrete
  contradiction of the claim or of the spec, not cost-benefit.
- **Tier 2 — worth fixing (fix).**  A genuine divergence whose fix is
  bounded and low-risk: ordinary coverage gaps, small deletions, needed
  annotations.  Default to fixing these.
- **Tier 3 — not worth fixing (may wontfix).**  The finding is a
  "nice-to-have" the repo does not need: a doc/SPEC wording mismatch or
  layering note with no behaviour change; re-flagging a trade-off that
  already carries a `ponytail:`; a speculative concern about a provably
  unreachable path; a niche edge-case test protecting a branch with no
  plausible regression; a cosmetic nit in an untouched file. These may be
  marked `[~]` with a one-line cost-benefit justification.

The key rule of thumb: **only wont-fix a Tier 3 item on cost-benefit
grounds.**  Tier 1 and Tier 2 items still need the usual evidence-based
wont-fix (claim wrong, spec contradicts, or fixing causes harm).

## Hard Rules

- Fix exactly the items in the review.  Do not fix things not listed.
- **Code groups default to parallel, each in an isolated worktree.**  Dispatch
  every code-group implementer up front and wait for all to complete, unless
  groups touch the same module or have interdependent changes — in that case,
  serialise them to avoid merge conflicts.  Plan groups dispatch in parallel
  (they are partitioned by file, so never conflict on the same document).
- **Every code group works on its own branch and worktree.**  Never share a
  worktree between groups.  Derive a unique branch/worktree per group slug
  (appending `-02`, `-03`, … on collision) and branch off the base branch.
- **Merge every code-group branch back into the base branch.**  The merge
  phase is mandatory, not optional — a fix that stays on a worktree branch
  is a fix that never lands.  Conflicts are resolved by dispatching an
  implementer; merged worktrees and branches are cleaned up.
- Verify before acting.  Trust the code, not the reviewer.
- `[x]` items left as-is — they were previously resolved and verified by
  `special-review`.  Do not re-fix them unless they were re-opened as `[!]`.
- `[~]` items left as-is — they were previously marked wont-fix and the
  justification was verified by `special-review`.  Do not re-evaluate
  unless they were re-opened as `[!]`.
- **Quality gate depends on file category:**
  - **Plan files** (under `plans/`): no build gate.  Verify the file is
    valid Markdown (no unclosed code fences, broken tables, etc.).
  - **Code files** (all other): run `make check-fix` and the relevant
    `make test-<service>` / `make test-sdk` / `make test-operator` targets
    on changed code before reporting a fix.  All verification goes through
    `make` — the implementer agent denies bare `go`/`buf` and env-prefixed
    commands.  If the change touches `proto/**`, run `make proto` first to
    regenerate `gen/**`.
- Do not push.  Implementers commit on their worktree branches; the merge
  phase merges those branches into the base branch.  Pushing to a remote is
  a separate step.
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
- **Running all code groups serially without reason.**  Code
  groups are the reason worktrees exist — running them one-at-a-time wastes
  the isolation.  Serialise only when groups touch the same module or have
  interdependent changes.
- **Sharing a worktree between groups.**  Two implementers in the same
  worktree re-create the stampede the design eliminates.  One group, one
  branch, one worktree.
- **Working in the main tree for a code group.**  A code-group implementer
  that edits the main working tree instead of its worktree produces
  conflicts at merge time and defeats the isolation.  The worktree is the
  working tree for that group.
- **Forgetting the merge phase.**  If branches are left dangling after the
  implementers finish, the fixes never land in the base branch.  Every
  code-group branch must be merged and its worktree cleaned up.
- **Resolving conflicts by picking one side wholesale.**  A conflicted file
  needs the intent of BOTH sides preserved — the branch's review fixes and
  the base's existing content.  Picking either side loses real work.
- **Trying to worktree-isolate plan files.**  `plans/` is gitignored, so it
  does not exist in any branch or worktree.  Plan groups always run in the
  main working tree and are never committed.
