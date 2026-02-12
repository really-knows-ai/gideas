---
name: lint-and-fix
description: Run markdown linting from lint/, fix issues, and rerun until clean
---

## Overview

This skill enforces markdown quality after repository changes.

It runs the repository linter from `lint/`, fixes reported issues in markdown files, and repeats until `pnpm lint` exits clean.

---

## Entry Point

1. Read `AGENTS.md` at the repository root.
2. Run `pnpm lint` in `lint/`.
3. If lint passes, report success and stop.
4. If lint fails, collect every reported issue (file, line, rule, and message) before editing.

---

## Fix Loop

Repeat the following loop until lint passes:

1. Open each reported markdown file and apply minimal edits to satisfy lint rules.
2. Preserve technical meaning while fixing style and formatting issues.
3. Avoid broad rewrites unless required for a lint rule.
4. Re-run `pnpm lint` in `lint/`.
5. Continue until the linter reports zero issues.

---

## Rules

- Run lint from `lint/` only.
- Use `pnpm lint` exactly (do not substitute another command).
- Fix only lint violations; do not introduce unrelated content changes.
- Keep terminology and key decisions in `AGENTS.md` intact.
- If a lint message is ambiguous, inspect the affected rule and apply the smallest valid fix.
- Stop only when lint is clean and report the files changed.
