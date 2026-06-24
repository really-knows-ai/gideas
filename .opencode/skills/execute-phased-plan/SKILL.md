---
name: execute-phased-plan
description: Use when executing a project folder under plans/ that contains SPEC.md, PLAN.md, and PHASE_XX.md files.
---

# Execute Phased Plan

Execute a phased implementation plan from `plans/<project-name>/` in an isolated git worktree. Each phase is implemented, verified, reviewed, and committed before the next phase begins. `SPEC.md` stays available to every agent throughout the work.

## Workflow

### 1. Select the plan

Use the project directory provided by the user. If none is provided, list directories under `plans/` that contain `SPEC.md`, `PLAN.md`, and `PHASE_XX.md` files, then ask which project to execute.

Read:
- `AGENTS.md`
- `PLAN.md`
- Every `PHASE_XX.md` file in phase order
- `SPEC.md`

If the project directory is missing `SPEC.md`, `PLAN.md`, or phase files, stop and ask the user to run the missing prior step.

### 2. Start isolated work

Create a fresh git worktree and development branch before implementation begins.

Use a branch name derived from the project slug, such as:

```
dev/<project-slug>
```

If the branch name already exists, append a numeric suffix (`-02`, `-03`, etc.).

Worktrees live in `.worktrees/` inside the repo root:

```bash
git worktree add -b dev/<project-slug> .worktrees/<project-slug> HEAD
```

All implementation, verification, review, and commits happen inside the new worktree.

### 3. Execute phases in order

For each `PHASE_XX.md`, dispatch an implementer subagent with this prompt:

```
Implement this phase of a phased plan.

**Spec file:**
[path to SPEC.md]

**Plan file:**
[path to PLAN.md]

**Current phase file:**
[path to PHASE_XX.md]

**Prior phase handoff context:**
[brief summary of completed prior phases and their commits, or "None"]

Requirements:
- Read `AGENTS.md`, `SPEC.md`, the plan, and current phase before editing.
- Implement only the current phase.
- Preserve completed prior-phase behaviour.
- Follow the verification steps and acceptance criteria in the current phase.
- Report files changed, verification run, and any unresolved blockers.
```

After the implementer returns, inspect the worktree diff and run the phase verification steps from the phase file. If the phase file omits a needed verification command, choose the narrowest relevant project command and record the choice.

### 4. Review the phase

After verification succeeds, dispatch a reviewer subagent with this prompt:

```
Review this implemented phase.

**Spec file:**
[path to SPEC.md]

**Plan file:**
[path to PLAN.md]

**Current phase file:**
[path to PHASE_XX.md]

**Implementation state:**
Review the current worktree diff and relevant files. Do not rely on git commit history.

Check for:
1. The current phase deliverables are implemented.
2. The implementation satisfies the current phase acceptance criteria.
3. The implementation stays aligned with `SPEC.md`.
4. Prior-phase behaviour remains intact.
5. Tests, verification, and error handling are sufficient for this phase.
6. The work is ready to commit as a complete phase.

Respond with one of:
- "APPROVED" (phase is complete and ready to commit)
- A numbered list of specific issues to fix
```

If the reviewer raises issues, dispatch the implementer again with the reviewer feedback verbatim, then re-run phase verification and review. Maximum two review cycles per phase before stopping and reporting the blocker to the user.

### 5. Commit the phase

When phase verification succeeds and the reviewer approves, commit the phase inside the worktree.

Use a concise phase-specific commit message, such as:

```
implement phase 01 core schema
```

Do not commit unrelated files, secrets, or changes outside the phase scope. If unrelated user changes are present, leave them unstaged and report them.

### 6. Repeat until all phases are complete

Proceed to the next phase only after the prior phase is verified, reviewed, and committed.

Maintain a short handoff summary after each phase:
- Phase completed
- Commit hash
- Key files changed
- Behaviour or interfaces the next phase depends on

Pass this summary to the next phase implementer.

### 7. Run the full quality gate

After every phase is committed, run the repository's full quality gate:

```
go test ./... && make check-fix
```

These two commands are non-negotiable — see `AGENTS.md`. Fix failures through the same implementer-reviewer loop. Commit any final quality-gate fixes separately.

### 8. Final spec-fulfilment review

Dispatch a final reviewer subagent with this prompt:

```
Review the completed implementation against `SPEC.md`.

**Spec file:**
[path to SPEC.md]

**Plan file:**
[path to PLAN.md]

**Phase files:**
[paths to all PHASE_XX.md files, in order]

**Implementation state:**
Review the current worktree and relevant files. Do not review git commit history. Assess the implemented system as it exists now.

Primary question:
Does the implemented system fulfil `SPEC.md`?

Check for:
1. Every spec requirement is implemented.
2. The implemented phases fit together coherently.
3. Edge cases and error handling from the spec are covered.
4. User-facing behaviour matches the spec.
5. Tests and verification provide appropriate confidence.
6. No plan phase left incomplete or contradicted the spec.

Respond with one of:
- "APPROVED" (the implementation fulfils `SPEC.md`)
- A numbered list of specific gaps to fix
```

If the final reviewer raises gaps, dispatch the implementer with the review feedback verbatim, re-run the full quality gate, and re-run the final review. Commit approved final fixes separately. Maximum two final review cycles before stopping and reporting unresolved gaps to the user.

### 9. Report completion

Report:
- Worktree path
- Branch name
- Phase commits
- Final quality-gate command and result
- Final spec-fulfilment review result
- Any unresolved blockers or follow-up work

## Hard Rules

- Start a fresh git worktree and development branch before implementation.
- Keep `SPEC.md` available to every implementer and reviewer subagent.
- Execute phases strictly in order.
- Commit after each approved phase.
- Run a reviewer after each phase.
- Run a final review against `SPEC.md` after all phases are complete.
- The final review evaluates the implementation state, not branch history.
- Stop after two failed review cycles for the same phase or final review.

## Common Mistakes

- **Working in the current checkout**: Phased execution starts in a fresh worktree and branch.
- **Skipping phase commits**: Each approved phase becomes its own commit before the next phase starts.
- **Letting agents work from phase files alone**: Every implementer and reviewer receives `SPEC.md`, the plan, and current phase path.
- **Reviewing git history in the final review**: The final reviewer assesses the current implementation against `SPEC.md`.
- **Running phases in parallel**: Phases are sequential because each phase may depend on prior handoffs.
- **Treating quality gates as optional**: Verification and review gates control whether work advances.
