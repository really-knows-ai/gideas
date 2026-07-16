---
name: special-fixer
description: Fix items from a review checklist produced by special-review. Dispatches implementers per item who verify the claim before acting — if they disagree they mark [~] wont-fix. Items are completed one at a time. Wont-fix items can be re-opened [!] by a subsequent review, and the implementer may [~] them again if they believe the reviewer is wrong. Reads LEARNINGS.md from the companion review directory and provides it to implementers.
---

# Special Fixer

Fix items from a review checklist (produced by `special-review`).  Each item is
handled by an independent implementer who first verifies the claim, then fixes
it or marks it wont-fix.  The implementer treats every claim with serious
skepticism — the default stance is "prove it before you touch anything."

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

### 2. Dispatch implementers per item

For each `- [ ]` and `- [!]` item in the checklist, dispatch one `@implementer`
subagent.  All implementers run in parallel.  Each receives this prompt:

```
Fix or evaluate the following review item.  You must verify the claim before
acting — do not take the reviewer's word at face value.

**Review item:**
[verbatim text of the item, including any indented sub-lines]

**Target files:**
Read the files referenced by the item.  If the item references no specific
files, read the files listed at the top of the review under "Files reviewed."

**Criteria:**
[criteria from the review header]

**Prior learnings (follow these rules during the fix):**
[contents of LEARNINGS.md, if it exists — otherwise "None."]

**Rules:**
1. Verify the claim.  Read the relevant code yourself.  Check whether the
   divergence from criteria actually exists.  Is the reviewer correct?
2. If you agree the claim is valid:
   - Implement the minimum fix that resolves the divergence.
   - Your fix must respect the Prior learnings.  If a learning says "no
     hardcoded line numbers", use section headings in your fix.  If a
     learning says "every RPC needs a response type", add the response
     type definition.
   - Run `make check-fix` and `go test ./...` on the changed code (or the
     relevant subset).  Do not commit.
   - Report: "Fixed: <what you changed and why.> <quality gate result.>"
3. If you disagree with the claim:
   - Do not change any code.
   - Report: "Wont-fix: <clear explanation of why the reviewer's claim is
     incorrect or why fixing it would cause harm.>"  Provide specific
   evidence — file paths, line numbers, test output.
4. If the item was marked `[!]` (re-opened), read the re-opening reason.
   Re-evaluate the claim in light of it.  You may still disagree.
5. If you need more context (files not referenced in the item, spec
   documents), read them before deciding.  Do not guess.
6. Make no changes beyond what the item requires.  No opportunistic
   refactoring.  No bonus fixes.
```

Wait for all implementers to complete before proceeding.

### 3. Update the review checklist

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

### 4. Write the updated review

Write the modified review file back to its original path.  Do not add summary
text — only update item states and append the fix/wont-fix detail lines.

### 5. Report to the user

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
- Run the quality gate (`make check-fix` + `go test ./...`) on changed code
  before reporting a fix.
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
