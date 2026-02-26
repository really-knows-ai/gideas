---
name: commit-push
description: Commit and push all the changes (update git ignore where needed)
---

## Overview

This skill commits all current changes and pushes them to the remote. It ensures nothing that should be ignored leaks into the commit.

---

## Entry Point

1. Read `AGENTS.md` at the repository root. Internalise the quality gates and project structure.
2. Read `.gitignore` at the repository root.
3. Run `git status` to see all staged, unstaged, and untracked files.

---

## Pre-Commit Checks

### Gitignore Audit

Before staging anything, review every untracked and modified file against these rules:

- **Generated files** must be ignored: `gen/`, `bin/`, `node_modules/`, `go.work.sum`, compiled Go binaries.
- **Editor artifacts** must be ignored: `*.swp`, `*.swo`, `*~`.
- **Secrets and credentials** must never be committed: `.env`, `credentials.json`, `*.pem`, `*.key`, or anything that looks like it contains secrets. Warn the user if any are present.
- **Build output** should not be committed unless it is an intentionally-distributed artifact (e.g. `dist/install.yaml`).

If any file that should be ignored is not covered by `.gitignore`, update `.gitignore` before proceeding. Group new entries under an appropriate comment heading.

### Quality Gates

Run the repository quality gates from `AGENTS.md` before committing:

1. **Tests** — Run `make test-all`. If any test fails, stop and report the failure. Do not commit with failing tests.
2. **Lint** — Run `make check-fix-all`. If any issue is reported, stop and report. Do not commit with lint failures.

If both pass, proceed to commit.

---

## Commit

1. Stage all changes: `git add -A`.
2. Run `git diff --cached --stat` to review what will be committed.
3. Draft a commit message:
   - Summarise the nature of the changes (new feature, enhancement, bug fix, refactor, docs, etc.).
   - Focus on the "why" rather than the "what".
   - Keep it to 1-2 concise sentences.
   - Follow the style of recent commits in the repository (`git log --oneline -10`).
4. Present the commit message and the list of files to the user for confirmation before committing.
5. Once confirmed, create the commit.

---

## Push

1. Check if the current branch tracks a remote branch: `git status -sb`.
2. If the branch tracks a remote, push: `git push`.
3. If the branch does not track a remote, push with upstream tracking: `git push -u origin HEAD`.
4. Report the result — confirm success or surface any push errors.

---

## Rules

- Never force-push unless the user explicitly requests it.
- Never force-push to `main` or `master` — warn the user if they request it.
- Never skip pre-commit hooks (`--no-verify`) unless the user explicitly requests it.
- Never commit files that contain secrets. Warn the user and exclude them.
- Always show the user the commit message and file list before committing. Wait for confirmation.
- If quality gates fail, stop and report. Do not commit broken code.
- If `.gitignore` is updated, include the `.gitignore` change in the same commit.
