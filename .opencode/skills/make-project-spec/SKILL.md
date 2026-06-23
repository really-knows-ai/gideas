---
name: make-project-spec
description: Use when turning a project idea or feature request into an implementation-ready SPEC.md under plans/.
---

# Make Project Spec

Turn a project idea into a reviewed spec at `plans/<project-name>/SPEC.md`. The spec is the source input for `make-phased-plan`, which adds `PLAN.md` and `PHASE_XX.md` files to the same project directory.

## Workflow

### 1. Explore project context

Read `AGENTS.md` and the relevant `specs/` documents to understand the system architecture, conventions, and existing patterns. Check existing plans under `plans/` for prior or related work. Follow the repository's Go, proto, and Kubernetes conventions where they are clear.

If the idea is too large for one project folder, help the user split it into smaller projects and choose the first one to specify.

### 2. Choose the project folder

Create a slug from the project name:

```
plans/<project-name>/
```

Use lowercase words separated by hyphens. If the directory already exists, ask whether to update the existing spec or choose a new slug.

The final spec path is always:

```
plans/<project-name>/SPEC.md
```

### 3. Understand the idea

Ask clarifying questions one at a time. Prefer multiple choice questions when that makes answering easier.

Focus on:
- Purpose and user value
- Scope and explicit non-goals
- Existing systems or files affected (platform services, proto contracts, SDK surfaces, nodes)
- Data flow and interfaces
- Error handling and edge cases
- Constraints and compatibility requirements
- Acceptance criteria and verification signals

### 4. Propose approaches

Before writing the spec, present 2-3 viable approaches with trade-offs and a recommendation. Wait for the user to choose or approve an approach.

### 5. Present the design

Present the design in sections scaled to the project's complexity. Cover:
- Goal
- Scope
- Architecture or implementation shape
- Components and responsibilities
- Data flow
- Error handling
- Testing and verification
- Risks or open questions

Get user approval before writing `SPEC.md`.

### 6. Write SPEC.md

Write `plans/<project-name>/SPEC.md` with these sections:

```
# <Project Title>

## Goal

## Background

## Scope

## Non-Goals

## Requirements

## Design

## Error Handling

## Verification

## Acceptance Criteria

## Open Questions
```

Use concrete, implementation-ready requirements. Avoid placeholders.

If there are no open questions, write `None` under `Open Questions`.

### 7. Self-review the spec

Review the written spec before reporting it:
- Remove placeholders, TODOs, and vague language.
- Resolve contradictions between sections.
- Confirm the scope fits one phased implementation plan.
- Make ambiguous requirements explicit.
- Confirm acceptance criteria are objective pass/fail statements.

Fix issues inline.

### 8. Report the next step

Ask the user to review `plans/<project-name>/SPEC.md`. When they approve it, the next step is to use `make-phased-plan` on the same project directory.

## Output Requirements

- The spec lives at `plans/<project-name>/SPEC.md`.
- The spec is written for a future planning agent, not as a status report.
- Requirements are concrete enough to map into phases.
- Acceptance criteria describe observable completion.
- Verification describes commands, checks, or behaviours that prove the work.

## Common Mistakes

- **Writing to docs/ or specs/**: Project specs for this workflow live under `plans/<project-name>/SPEC.md`.
- **Skipping approach review**: Present approaches and get approval before writing the spec.
- **Leaving open-ended scope**: A spec that spans multiple independent systems should be split into project folders.
- **Using vague requirements**: "Improve UX" is vague. State the exact user-visible behaviour.
- **Skipping user review**: The user reviews `SPEC.md` before `make-phased-plan` consumes it.
