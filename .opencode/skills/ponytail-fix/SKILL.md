---
name: ponytail-fix
description: >
  Fix ponytail audit items from plans/AUDIT.md one at a time. For each item:
  implementer fix → ponytail-review → normal review → quality gate → mark done → commit.
  Cycle until reviewer is happy at each stage. Complements ponytail-audit.
---

# Ponytail Fix

Fix ponytail audit checklist items from `plans/AUDIT.md` (or `plans/PLAN.md` if
`AUDIT.md` does not exist). Each item goes through a strict fix → ponytail-review
→ normal-review → quality gate → commit cycle.

## Workflow

### 1. Read the checklist

Read `AGENTS.md` and `plans/AUDIT.md` (fall back to `plans/PLAN.md` if
`AUDIT.md` does not exist). Find the first incomplete item — an unchecked
checklist entry that is not yet strikethrough + ✅.

If no incomplete items exist, report "All done — nothing to fix."

### 2. Dispatch the implementer

Issue an `@implementer` subagent with a precise description of the fix needed.
Include the full text of the checklist item and the file paths it references.

Requirements for the implementer prompt:
- Read all referenced source files before editing.
- Make the smallest correct change.
- Verify with `go vet` on the affected module(s).
- Return a summary of what changed.

### 3. Ponytail-review the fix

Issue a `@reviewer` subagent with a ponytail-review (over-engineering audit)
of the changed files.

Prompt:
```
Do a ponytail-review (over-engineering audit) of these changes.

Files changed: [list]

Check for:
1. Over-engineering — unnecessary abstraction, speculative flexibility
2. Reinvented wheels — something the stdlib does already
3. Scope creep or unintended changes
4. Whether the change is worth the diff

If clean, say "PONYTALL CLEAN — no over-engineering found."
```

If the reviewer finds issues, fix them (directly or via the implementer
with specific feedback), then re-review. Loop until ponytail-review passes.

### 4. Normal review the fix

Issue a `@reviewer` subagent with a normal correctness review of the changed
files.

Prompt:
```
Do a normal correctness review of these changes.

Files changed: [list]

Check for:
1. Compilation (go vet passes)
2. Correctness — no behavioral changes beyond what's intended
3. Completeness — all references updated, no leftovers
4. Edge cases

If clean, say "REVIEW CLEAN — no issues found."
```

If the reviewer finds issues, fix them and re-review. Loop until normal
review passes.

### 5. Run the quality gate

Run:
```
go test ./[affected_modules]/...
make check-fix
```

Both must pass. Fix failures through the same implementer → review loop.

### 6. Mark the item done

Update `plans/AUDIT.md` (or `plans/PLAN.md`) to mark the item as complete:
wrap it in `~~strikethrough~~` and append ` ✅`.

If the item is `plans/AUDIT.md` and no commit is needed (no code changed),
just update the file and stop.

### 7. Commit

Stage all changed files **except** the plans file (it's gitignored).

```
git add [changed files]
git commit -m "[concise description]"
```

### 8. Report

Report the item fixed, commit hash, and quality gate result.

## Tags (ponytail priority order)

Items in the audit checklist use these tags to indicate what kind of change
is needed:

- `delete:` — dead code, unused flexibility. Remove it.
- `stdlib:` — hand-rolled thing stdlib ships. Use stdlib.
- `native:` — dep or code the platform already does. Use platform.
- `yagni:` — abstraction with one implementation. Inline or remove.
- `shrink:` — same logic, fewer lines. Simplify.

## Hard Rules

- Read the checklist from `plans/AUDIT.md` first; fall back to `plans/PLAN.md`.
- Fix one item at a time. Do not batch.
- Ponytail-review first, then normal review. Both must pass before commit.
- Do not commit the plans file (it's gitignored).
- Run the quality gate after every commit.
- If a reviewer rejects with feedback, fix via the implementer with specific
  feedback, then re-review. Maximum 3 cycles before stopping and reporting.
