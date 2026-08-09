---
name: execute-phased-plan
description: Use when executing a project folder under plans/ that contains SPEC.md, PLAN.md, and PHASE_XX.md files.
---

# Execute Phased Plan

Execute a phased implementation plan from `plans/<project-name>/` in an isolated git worktree. Each phase is implemented, verified, reviewed, and committed before the next phase begins. `SPEC.md` and `LEARNINGS.md` (if present) stay available to every agent throughout the work. Phase-level and final reviews use the `special-review` skill; fixes use the `special-fixer` skill.

## Workflow

### 1. Select the plan

Use the project directory provided by the user. If none is provided, list directories under `plans/` that contain `SPEC.md`, `PLAN.md`, and `PHASE_XX.md` files, then ask which project to execute.

Read:
- `AGENTS.md`
- `PLAN.md`
- Every `PHASE_XX.md` file in phase order
- `SPEC.md`
- `LEARNINGS.md` — if it exists, read it in full. It documents known patterns, recurring issues, and deviations to avoid. If it does not exist, note the absence and proceed.

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

The main agent MUST read `LEARNINGS.md` (if it exists) and pass its contents into the implementer prompt as shown below — do not just reference the file path, because the implementer subagent may not have the full project context to locate or interpret it. The key patterns, Known Deviations, and candidate patterns in `LEARNINGS.md` are critical input the implementer needs to avoid repeating previously identified issues.

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

**Learnings from prior review cycles:**
[full contents of LEARNINGS.md, or "None"]

Requirements:
- Read `AGENTS.md`, `SPEC.md`, the plan, and current phase before editing.
- Read the Learnings above in full before implementing. Every pattern and known deviation documented there must be followed — do not reintroduce issues that prior review cycles identified and fixed.
- Implement only the current phase.
- Preserve completed prior-phase behaviour.
- Follow the verification steps and acceptance criteria in the current phase.
- Report files changed, verification run, and any unresolved blockers.
```

After the implementer returns, inspect the worktree diff and run the phase verification steps from the phase file. If the phase file omits a needed verification command, choose the narrowest relevant project command and record the choice.

### 4. Review the phase

After verification succeeds, run a structured review using the `special-review` skill, then fix any issues using the `special-fixer` skill.

#### 4a. Run special-review

Load the `special-review` skill and follow its workflow:

- **Files to review**: All files modified or added in the current worktree diff (the files implementing this phase's deliverables).
- **Criteria**: `plans/<project>/SPEC.md` and `plans/<project>/PHASE_XX.md` — the implementation must satisfy every spec requirement relevant to this phase and meet the phase's acceptance criteria.
- **Output path**: `plans/<project>/phase-XX-review.md` (per-phase review artifact).
- **Prior learnings**: The skill reads `plans/<project>/LEARNINGS.md` automatically from the same directory.

The special-review workflow produces a consolidated checklist of any divergences.

#### 4b. Run special-fixer

If the phase review produced any open items (`- [ ]` or `- [!]` in the review file), load the `special-fixer` skill and follow its workflow:

- **Review file**: `plans/<project>/phase-XX-review.md`
- The skill groups items by target file, dispatches code-file implementers in parallel (each in an isolated worktree), merges the worktree branches back into the base branch, and updates the review checklist with fix/wont-fix results.

#### 4c. Re-verify and re-review

After the fixer completes:
1. If any items were fixed (`- [x]`), re-run the phase verification steps from the phase file.
2. If any items remain open (`- [ ]` or `- [!]`), re-run special-review (step 4a) with the same inputs.
3. If any items were marked wont-fix (`- [~]`), the main agent reads their justifications and decides whether to accept or escalate.

Maximum two review-fix cycles per phase before stopping and reporting the blocker to the user.

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
make verify
```

This command is non-negotiable — see `AGENTS.md`. Fix failures through the same implementer-reviewer loop. Commit any final quality-gate fixes separately.

### 8. Final spec-fulfilment review

After all phases are committed and the full quality gate (step 7) passes, run a final structured review using the `special-review` skill, then fix any remaining issues using the `special-fixer` skill.

#### 8a. Run special-review

Load the `special-review` skill and follow its workflow:

- **Files to review**: All implementation files affected across all phases — the complete worktree implementation state.
- **Criteria**: `plans/<project>/SPEC.md` — does the full implementation satisfy every spec requirement?
- **Output path**: `plans/<project>/REVIEW_ITEMS.md` (the project's canonical review checklist; `plans/<project>/REVIEW.md` holds the criteria + pass log).
- **Prior learnings**: The skill reads `plans/<project>/LEARNINGS.md` automatically.

The special-review workflow produces a consolidated checklist of spec-compliance gaps.

#### 8b. Run special-fixer

If the final review produced any open items, load the `special-fixer` skill and follow its workflow:

- **Review checklist**: `plans/<project>/REVIEW_ITEMS.md`

#### 8c. Re-run quality gate and re-review

After the fixer completes:
1. Re-run the full quality gate (`make verify`).
2. If any items remain open, re-run special-review (step 8a) — it will prune newly-fixed items, verify wont-fix justifications, and produce a fresh checklist.
3. Re-run the quality gate after any fixes.

Maximum two final review cycles before stopping and reporting unresolved gaps to the user.

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
- Keep `SPEC.md` and `LEARNINGS.md` (if it exists) available to every implementer and reviewer subagent.
- The main agent MUST read `LEARNINGS.md` and inline its full contents into implementer prompts — do not delegate reading to subagents. The `special-review` skill reads LEARNINGS.md automatically for its reviewers.
- Execute phases strictly in order.
- Commit after each approved phase.
- Phase reviews use the `special-review` skill (producing a per-phase review file) followed by the `special-fixer` skill (if issues found).
- Run a final review against `SPEC.md` after all phases are complete, using `special-review` + `special-fixer`.
- The final review evaluates the implementation state, not branch history.
- Stop after two failed review-fix cycles for the same phase or final review.

## Common Mistakes

- **Working in the current checkout**: Phased execution starts in a fresh worktree and branch.
- **Skipping phase commits**: Each approved phase becomes its own commit before the next phase starts.
- **Letting agents work from phase files alone**: Every implementer receives `SPEC.md`, the plan, the current phase path, and the full contents of `LEARNINGS.md`.
- **Letting subagents read `LEARNINGS.md` themselves**: The main agent reads `LEARNINGS.md` and inlines its full contents into implementer prompts. Subagents may not locate or interpret it correctly if given only a file path. The `special-review` skill handles its own LEARNINGS.md reading — let it do that.
- **Skipping the special-review → special-fixer loop for phase reviews**: After each phase, always run `special-review` first, then `special-fixer` if issues exist. Do not skip to commit without a structured review artifact.
- **Reviewing against phase files alone**: Phase reviews use `SPEC.md` + `PHASE_XX.md` as criteria, not just the phase file. The implementation must satisfy spec requirements, not just the phase's documented scope.
- **Running the final review without loading `special-review`**: Do not inline-review the final implementation. Load the `special-review` skill and follow its workflow — it handles consolidation, deduplication, and learning pruning.
- **Applying fixes without `special-fixer`**: When review issues exist, load `special-fixer` rather than dispatching ad-hoc implementers. The fixer groups items by file, dispatches code groups in parallel (each in an isolated worktree), merges the branches back, and tags results correctly.
- **Running `special-review` without an output path**: Phase reviews always write to `phase-XX-review.md`; the final review writes to `REVIEW_ITEMS.md` (with criteria + pass log in `REVIEW.md`). Without a distinct path, per-phase review artifacts would overwrite each other.
- **Ignoring `LEARNINGS.md` during implementation**: The learnings document captures patterns identified in prior review cycles. Implementers must cross-reference every relevant learning against their code. Known Deviations document what NOT to flag, so reviewers need them too. Treat `LEARNINGS.md` as co-equal with `SPEC.md` — both constrain what correct implementation looks like.
- **Reviewing git history in the final review**: The final reviewer assesses the current implementation against `SPEC.md`.
- **Running phases in parallel**: Phases are sequential because each phase may depend on prior handoffs.
- **Treating quality gates as optional**: Verification and review gates control whether work advances.
