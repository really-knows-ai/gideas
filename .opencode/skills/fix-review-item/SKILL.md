---
name: fix-review-item
description: Fix the first incomplete checklist item in plans/<project>/REVIEW.md, commit the fix, then stop. Does not commit REVIEW.md — it is gitignored by design.
---

# Fix Review Item

Fix the first incomplete checklist item in `plans/<project>/REVIEW.md`, commit the fix, then stop. Does not commit `REVIEW.md` — it is gitignored by design.

## Workflow

### 1. Select the project and review (always on main)

Determine the project directory under `plans/`. If the user does not provide one, list directories under `plans/` that contain `REVIEW.md` and ask which project to use.

Read `AGENTS.md` and `plans/<project>/REVIEW.md`. Specs, plans, and REVIEW.md live on main regardless of where the implementation lives. If the file does not exist or contains no incomplete items (no `- [ ]` lines), stop and report that there is nothing to fix.

### 2. Locate the implementation

Run `git worktree list` to find a worktree whose branch contains the project name. If one exists, the implementer works in that checkout. If none exists, the implementer works in the main checkout.

Record the path — this is the working directory for steps 3 and 4.

### 3. Find the first incomplete item

Scan `REVIEW.md` for the first line matching `- [ ]`. This is the target item. Read the item's full description — it may span multiple indented lines. Note the item's line number.

Already-resolved items (`- [x]`) and wont-fix items (`- [~]`) are skipped.

### 4. Dispatch the implementer

Issue an `@implementer` subagent with this prompt:

```
Fix the following item from the review checklist.

**Spec file:**
[path to SPEC.md on main, e.g. /path/to/main/plans/<project>/SPEC.md]

**Review item to fix:**
[verbatim text of the item, including any indented sub-lines]

**Working directory:**
[the worktree path, or the main checkout path if no worktree exists]

Requirements:
- Read `AGENTS.md` before editing. Follow the repository's conventions and rules.
- Fix exactly this one review item. Do not change unrelated code.
- When the fix is complete, mark the item as done by changing its `- [ ]` to `- [x]` in REVIEW.md.
- If you disagree with the item and believe it should not be fixed, mark it as `- [~]` for wont-fix and add a clear justification on an indented line below the item. Format the justification as `- Wont-fix: ...`.
- Report what you changed and why.
```

### 5. Commit the fix

When the implementer returns successfully, commit the changes in the implementation checkout. Stage every modified file **except** `plans/<project>/REVIEW.md`. Use a concise commit message describing the specific fix.

```
git add [changed files, excluding REVIEW.md]
git commit -m "fix: [brief description of the fix]"
```

Do not commit `REVIEW.md`. It is gitignored for a reason — it lives outside the implementation worktree and tracks review state separately.

If the implementer marked the item as wont-fix (`- [~]`) rather than fixing it, skip the commit step entirely and report the decision.

### 6. Run the quality gate

After committing, run the repository's quality gate on the changed code:

```
go test ./... && make check-fix
```

If the quality gate fails, fix the failures and amend the commit. Do not proceed with a failing quality gate.

### 7. Report and stop

Report:
- The item that was addressed
- Whether it was fixed or marked wont-fix
- The commit hash (if fixed)
- Quality gate result
- Any unresolved concerns

Stop. Do not proceed to the next item — this skill fixes one item per invocation.

## Hard Rules

- Specs, plans, and REVIEW.md live on main in `plans/<project>/`. Read them from main.
- Run `git worktree list` to find the implementation checkout. If a worktree branch contains the project name, use that checkout. Otherwise use main.
- Fix exactly one item: the first `- [ ]` in `REVIEW.md`.
- Do not commit `REVIEW.md`.
- Items marked `- [x]` or `- [~]` are skipped; only `- [ ]` is actionable.
- If the implementer marks an item as `- [~]` (wont-fix), skip the commit.
- Stop after fixing the one item. Do not continue to the next.

## Common Mistakes

- **Working in the wrong checkout.** The implementer must work in the project's worktree if one exists. Reviewing main when a worktree exists produces a stale implementation.
- **Fixing multiple items in one call.** This skill fixes exactly one item. Run it again for the next item.
- **Committing REVIEW.md.** The review file is gitignored and must not be committed. Stage only the implementation changes.
- **Skipping wont-fix items.** Wont-fix items (`- [~]`) are already decided — skip them.
- **Not reading AGENTS.md.** The implementer must read `AGENTS.md` to follow the repository's conventions.
- **Failing to provide SPEC.md.** The implementer needs the spec for context on the expected behaviour.
- **Skipping the quality gate.** All changes must pass `go test ./... && make check-fix` before stopping.
