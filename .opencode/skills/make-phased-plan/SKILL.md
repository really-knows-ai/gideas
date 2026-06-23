---
name: make-phased-plan
description: Use when a project folder under plans/ contains SPEC.md and needs PLAN.md plus PHASE_XX.md files.
---

# Make Phased Plan

Turn `plans/<project-name>/SPEC.md` into a phased implementation plan. The resulting `PLAN.md` and `PHASE_XX.md` files live in the same project directory and are designed for execution in a new session with the `execute-phased-plan` skill.

## Workflow

### 1. Select a project spec

Use the project directory provided by the user. If none is provided, list directories under `plans/` that contain `SPEC.md` and ask which project to plan. If exactly one project directory contains `SPEC.md`, use it.

Read `AGENTS.md` and `SPEC.md` in full. Understand the scope, deliverables, and constraints before planning. If the chosen project directory does not contain `SPEC.md`, stop and ask the user to run `make-project-spec` first or provide the correct project directory.

### 2. Use the project directory

Write the generated plan files beside the spec:

```
plans/<project-name>/SPEC.md
plans/<project-name>/PLAN.md
plans/<project-name>/PHASE_XX.md
```

If `PLAN.md` or `PHASE_XX.md` files already exist, ask whether to replace them or stop.

### 3. Propose phase breakdowns to the user

Analyse the spec and present the user with at least two distinct ways to break the work into phases. Each option must include:

- Phase names with one-line descriptions
- The reasoning behind the breakdown (why this structure fits the spec)
- The dependency ordering this creates
- A brief "execution narrative" — what the implementing session would build, in sequence

**Common breakdown strategies to consider (tailored for Foundry Flow):**

- **By architectural layer**: Proto contracts first, then operator/controller logic, then SDK surfaces, then node implementations. Suits work that extends the platform.
- **By service or node**: Each phase delivers a complete service or node. Suits adding new runtime components.
- **By risk or complexity**: Foundational or risky work first, then progressively simpler layers. Suits projects with uncertain technical ground.
- **By dependency chain**: The most depended-on module first, then consumers. Suits SDK or proto contract work.
- **Vertical slice**: Each phase crosses all layers for a narrow use case. Suits projects that need end-to-end validation early.

Present the options clearly to the user, labelling them Option A, Option B, etc. Ask which breakdown they prefer, and whether they want any adjustments. Do not proceed until the user confirms a phase breakdown.

### 4. Draft phases in parallel

Once the user confirms a phase breakdown, dispatch one implementer subagent per phase, in parallel. Each implementer receives the full spec and their single phase's description. Use this prompt per implementer:

```
You are drafting a single PHASE_XX.md file. Read the spec below and the phase description, then produce the phase file.

**Spec:**
[full spec content]

**Phase number and title:**
[phase number and name from the agreed breakdown]

**Phase goal and description:**
[the one-line description and any extra detail the user provided]

**Requirements:**
- The phase must be independently testable and reviewable.
- Deliverables must be concrete and verifiable — not vague milestones.
- State: goal, deliverables, verification steps, and acceptance criteria.
- Include handoff instructions — state what this phase depends on from prior ones, and what subsequent phases depend on from this one.

**Output format:**
Return the full content of the phase file, starting with a level-1 heading of the phase title.
```

Wait for all implementers to finish before proceeding.

### 5. Write PLAN.md and phase files

Write `PLAN.md` to the project directory. PLAN.md contains:
- Plan title and `SPEC.md` reference
- Execution method: `execute-phased-plan`
- Ordered list of phases with one-line descriptions (from the agreed breakdown)
- A "How to Execute" section instructing the executing session to load the `execute-phased-plan` skill and follow it

Write each `PHASE_XX.md` from the implementer outputs. Use the numbering from the agreed breakdown.

Reviewers receive file paths, not pasted plan or spec content.

Confirm all files are written by listing the directory contents.

### 6. Review each phase with @reviewer

Dispatch one reviewer subagent per phase, in parallel. Wait for all phase review results before proceeding. Each reviewer receives only the phase file path. Do not pass the spec to individual phase reviewers.

If any phase review returns issues, record which phases failed. Do not proceed to the holistic review — go to the iteration step instead.

Use this prompt for each phase review:

```
Review this phase file for internal coherence and execution clarity:

**Phase file:**
[path to PHASE_XX.md]

Focus on:
1. The phase goal is clear and matches its deliverables.
2. Deliverables are concrete and verifiable.
3. Acceptance criteria are objective pass/fail conditions.
4. Dependencies and handoff instructions are clear within the phase.
5. Ambiguities are surfaced as clarifying questions.

Do NOT flag:
- Whether a helper function pre-exists in the codebase — the executing agent
  discovers this during implementation.
- The exact file a helper should live in — the agent follows project conventions.
- Which line in an existing file to modify — the agent reads the code before
  editing.
- Source-versus-dist path confusion — the agent works with source files and
  follows the project's build tooling.
- Whether a test file already exists — the agent creates what is needed and
  follows existing test patterns.

Flag ONLY when a phase is missing a deliverable, has contradictory
instructions, or leaves a contract gap that would prevent an agent from
starting. Contract gaps include: underspecified function signatures that
cross phase boundaries, unclear who owns a responsibility, or missing
error-handling rules for a named scenario.

Do not review this phase against the original spec. Focus on whether this phase is internally coherent, testable, and clear enough to execute.

Respond with one of:
- "APPROVED" (phase is internally coherent and execution-ready)
- A numbered list of specific issues or clarifying questions to resolve
```

### 7. Review the full plan with @reviewer

This step MUST NOT run until every phase review has returned APPROVED. Confirm that zero phase reviews have outstanding issues before proceeding. Only then dispatch a holistic reviewer.

Dispatch a subagent using the platform's subagent mechanism, such as `@reviewer` or `subagent_type: "reviewer"`, with this prompt:

```
Review this phased implementation plan holistically for coherence across phases and alignment with `SPEC.md`.

**Spec file:**
[path to SPEC.md]

**Plan file:**
[path to PLAN.md]

**Phase files:**
[paths to all PHASE_XX.md files, in order]

Check for:
1. Every requirement in the spec maps to at least one phase deliverable.
2. The phases fit together into a coherent implementation sequence.
3. Phases are ordered by dependency with clear handoff instructions.
4. PLAN.md states execution uses the `execute-phased-plan` skill.
5. Phase boundaries are clear and avoid duplicated or missing work.
6. The plan covers edge cases and error handling mentioned in the spec.
7. The full set of files is sufficient for a new session to execute the plan.

Respond with one of:
- "APPROVED" (plan meets spec requirements)
- A numbered list of specific issues to fix
```

### 8. Iterate or finalise

If every phase reviewer and the holistic reviewer return "APPROVED", proceed to reporting.

If any reviewer raises issues, partition the issues by phase file. For each phase file with outstanding issues, dispatch one @implementer in parallel, each receiving only their phase's issues and file path. Use this prompt per implementer:

```
Revise this phase file to resolve the issues listed below. Read the current file, address each issue, and return the full revised content.

**Phase file:**
[path to affected PHASE_XX.md]

**Issues to resolve:**
[numbered list of issues specific to this phase]

**Spec file:**
[path to SPEC.md]

Return the full revised content of the phase file.
```

Wait for all parallel implementers to finish. Update each phase file with its revised content. If the holistic reviewer raised issues that are not specific to any single phase file, dispatch a single @implementer to fix the PLAN.md and affected phase files.

After all implementers complete, re-review the affected phase files in parallel (repeat step 6 for those files only). If all phase re-reviews pass, proceed to the holistic review (step 7). If any phase re-review returns issues, repeat this iteration step.

Continue cycling until all phases pass review, or the user asks to stop.

### 9. Report the result

Confirm the final files exist by listing the directory contents.

Report to the user:
- The project directory path
- Number of phases
- Confirmation that each phase review and the holistic review passed, or the unresolved review issues
- Confirmation that execution should use a new session with the `execute-phased-plan` skill

## Output file requirements

### PLAN.md

Contains:
- Plan title and `SPEC.md` reference
- Execution method: `execute-phased-plan`
- Ordered list of phases with one-line descriptions
- "How to Execute" section instructing the executing session to load the `execute-phased-plan` skill and follow it

### PHASE_XX.md

Each phase file contains:
- Phase title and goal
- Deliverables (concrete, verifiable)
- Verification steps (commands, assertions, test outcomes)
- Acceptance criteria (pass/fail conditions)
- Dependencies on prior phases (if any)

## Common mistakes

- **Committing plans/**: The `plans/` directory is untracked. Do not stage or commit anything under `plans/`.
- **Using `plans/specs/`**: Specs for this workflow live at `plans/<project-name>/SPEC.md`.
- **One giant plan file**: The plan must be split into `PLAN.md` plus individual `PHASE_XX.md` files. A single file defeats phased execution.
- **Omitting reviewers**: Phase-by-phase @reviewer checks and the final holistic @reviewer check are mandatory. A plan without both review levels remains a draft.
- **Pasting specs into phase reviews**: Individual phase reviewers receive only the phase file path. They focus on internal coherence and clarifying questions.
- **Pasting file contents into holistic reviews**: The holistic reviewer receives file references for the spec, PLAN.md, and all phase files.
- **Vague phase deliverables**: "Implement the API" is not a deliverable. "Create `proto/flow/v1/foo.proto` with CreateFoo and GetFoo RPCs" is.
- **Missing handoff instructions**: Each phase must state what it depends on from prior phases. The executing session needs this context.
- **Ignoring the execution method**: PLAN.md must explicitly state that execution uses the `execute-phased-plan` skill. Without this instruction, the executing session may attempt a different approach.
