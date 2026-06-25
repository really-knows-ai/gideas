---
name: ponytail-audit
description: >
  Whole-repo audit for over-engineering. Like ponytail-review, but scans the
  entire codebase instead of a diff: writes a ranked checklist to
  plans/AUDIT.md of what to delete, simplify, or replace with stdlib/native
  equivalents. Use when the user says "audit this codebase", "audit for
  over-engineering", "what can I delete from this repo", "find bloat", or calls
  ponytail-audit. One-shot report, does not apply fixes.
---

ponytail-review, repo-wide. Scan the whole tree instead of a diff. Rank
findings biggest cut first. Write output to `plans/AUDIT.md` as a structured
checklist consumable by `ponytail-fix`.

## Tags

Same as ponytail-review:

- `delete:` dead code, unused flexibility, speculative feature. Replacement: nothing.
- `stdlib:` hand-rolled thing the standard library ships. Name the function.
- `native:` dependency or code doing what the platform already does. Name the feature.
- `yagni:` abstraction with one implementation, config nobody sets, layer with one caller.
- `shrink:` same logic, fewer lines. Show the shorter form.

## Hunt

Deps the stdlib or platform already ships, single-implementation interfaces,
factories with one product, wrappers that only delegate, files exporting one
thing, dead flags and config, hand-rolled stdlib.

## Output

Write findings to `plans/AUDIT.md` as a ranked checklist. Format each finding
as a markdown checklist item with the tag as a heading:

```markdown
### `<tag>:` `<one-line summary>`

Description of the finding, including file paths and estimated savings.

- [ ] `path/to/file.go:L<N>` — what to change and why
- [ ] `path/to/other.go:L<N>` — what to change and why
```

End with:

```markdown
---

**Net estimate: ~-<N> lines, ~-<M> deps.** All replacements use existing stdlib
or in-repo consolidation. No new dependencies introduced.
```

If nothing to cut: `Lean already. Ship.`

## Notes

- `plans/` is gitignored. AUDIT.md is never committed.
- After writing AUDIT.md, invoke `ponytail-fix` to work through items.
- Merge into an existing AUDIT.md if one exists: preserve completed (✅)
  items and append new findings.
- Scope: over-engineering and complexity only. Correctness bugs, security
  holes, and performance are explicitly out of scope. Route them to the
  `@reviewer` subagent instead. Lists findings, applies nothing. One-shot.
