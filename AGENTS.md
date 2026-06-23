# AGENTS.md

## Project

This repository contains the technical specification and reference implementation for **Foundry Flow** — a governed workflow runtime on Kubernetes.

## Repository Structure

### Documentation (`/specs`)

The authoritative source of truth for the system design.

/specs
├── 01-concepts/        # Helicopter view — read first
├── 02-flow/            # The Platform — assumes nodes exist
├── 03-node/            # Building Nodes — internal runtime architecture
├── 04-sdk/             # SDK — external developer interface
└── 05-reference/       # CRDs, APIs, Errors, Glossary

### Implementation (Source Code)

The "Walking Skeleton" and reference components.

/
├── platform/             # The Runtime Infrastructure
│   ├── operator/         # The Control Plane (Kubebuilder Controller)
│   ├── sidecar/          # The Data Plane (Runtime Host & Proxy)
│   ├── eventbus/         # Event Bus Service
│   ├── frictionledger/   # Friction Ledger Service
│   ├── monitor/          # Flow Monitor Service
│   ├── librarian/        # Librarian Service
│   ├── archivist/        # Archivist Service
│   └── pkg/eventbus/     # Shared event bus client library
├── sdk/                  # Node Development Kits
│   └── go/               # Go SDK Core (Provider abstraction, FoundryAgent)
├── nodes/                # Standard Node Implementations
│   ├── forge/            # Content Generation Node (concrete ForgeAgent)
│   ├── sort/             # Governance Triage Node
│   ├── appraise/         # Multi-review orchestrator node
│   ├── reviewer/         # Review child node
│   ├── refine/           # Revision node
│   ├── arbiter/          # Deadlock deliberation orchestrator
│   ├── tribunal/         # Hearing deliberation orchestrator
│   ├── juror/            # Judiciary deliberation child node
│   ├── rule-router/      # CEL-based Routing Node (judiciary routing)
│   ├── facilitator/      # Deadlock Resolution Lifecycle Node (judiciary)
│   ├── hitl/             # Generic Config-Driven HITL Node (one image, many CRD instances)
│   ├── law-applicator/   # Petition application action node
│   ├── codification/     # Petition codification fan-out orchestrator
│   ├── codify-smt/       # Reference formal codifier node
│   ├── friction-watcher/ # Judiciary Entry Node (friction threshold events)
│   ├── ttl-watcher/      # Judiciary Entry Node (law TTL expiry)
│   └── null-node/        # Verification Node (Phase 1)
├── gen/                  # Generated Protocol Buffer Code (The Contract)
├── proto/                # Protocol Buffer Definitions
├── charts/               # Helm Charts for deployment
└── tools/                # Dev/debug utilities (e.g., haiku-watch)

## Reading Order

1. **Concepts** (`specs/01-concepts`) — What Foundry Flow is and why it exists.
2. **Architecture** (`specs/01-concepts/01-architecture.md`) — The Six-Plane Model.
3. **The Contract** (`proto/`) — The wire protocol that binds the components.
4. **Implementation** — The code in `platform/operator`, `platform/sidecar`, and `sdk`.

## Quality Gates

All changes to this repository **must** pass the following before being committed:

1. **Tests** — Run `go test ./...` (or the relevant subset) and ensure all tests pass. New functionality requires new or updated tests.
2. **Lint** — Run `make check-fix` and resolve every issue it reports. Do not commit with lint failures.

These two steps are non-negotiable. A change without tests or with lint violations is incomplete.

## Foundational Axioms

1. **Assume Unreliability** — All agents are fallible. Trust intent, verify execution.
2. **Make Work Auditable** — Every action becomes an immutable, traceable record.
3. **Make the Cost Visible** — Friction is a first-class, quantifiable signal.
4. **Quality is Fixed, Cost is Variable** — The standard is non-negotiable; the system measures the cost of achieving it.

## Compatibility Policy

This is a greenfield project. There are no backward compatibility obligations. Breaking API changes are acceptable and preferred over accumulating backward-compat debt. Do not deprecate — remove.

---

## Workflow: Spec → Plan → Execute → Review

New features and changes follow a structured pipeline designed for iterative, reviewed, and auditable development.

### Directory Convention

All feature-level planning lives under `plans/<project-name>/`:

```
plans/<project-name>/
├── SPEC.md          # What to build (requirements, design, acceptance criteria)
├── PLAN.md          # How to build it (phased breakdown, execution order)
├── PHASE_01.md      # Individual phase files (one per phase)
├── PHASE_02.md
└── REVIEW.md        # Spec-compliance audit checklist (produced by implementation-review)
```

The `specs/` directory remains the authoritative system-level source of truth. The `plans/` directory contains per-feature execution plans and reviews. Both live on `main` regardless of where implementation happens.

### Pipeline Steps

#### 1. Spec (`make-project-spec`)

Turn an idea or feature request into `plans/<project-name>/SPEC.md`. The skill explores context, proposes approaches, writes a concrete spec with acceptance criteria, and self-reviews. Output: a reviewed `SPEC.md` ready for planning.

Run: `make-project-spec` skill.

#### 2. Plan (`make-phased-plan`)

Read `SPEC.md` and produce `PLAN.md` + `PHASE_XX.md` files. Proposes multiple phase breakdown strategies (by layer, by service, by risk, etc.) for the user to choose from. Drafts phases in parallel, then reviews each phase individually and holistically. Output: approved `PLAN.md` and `PHASE_XX.md` files.

Run: `make-phased-plan` skill.

#### 3. Execute (`execute-phased-plan`)

Execute the phased plan in a fresh git worktree and `dev/<project-slug>` branch. Each phase is implemented, verified, reviewed, and committed before the next phase begins. After all phases, runs the full quality gate (`go test ./... && make check-fix`) and a final spec-fulfilment review. Output: completed implementation in a worktree ready for merge.

Run: `execute-phased-plan` skill.

#### 4. Review (`implementation-review`)

Audit the current repository state against `SPEC.md`. Dispatches parallel reviewer subagents (one per spec section) to find deviations. Consolidates findings into `REVIEW.md` as a checklist, cross-referencing against previously resolved and wont-fix items. Output: `REVIEW.md` checklist.

Run: `implementation-review` skill.

#### 5. Fix (`systematic-fix-and-review` / `fix-review-item`)

Fix items from `REVIEW.md`.

- **`fix-review-item`**: Fixes exactly one `- [ ]` item, commits the fix (with quality gate), and stops. Run repeatedly for incremental progress.
- **`systematic-fix-and-review`**: Fixes every `- [ ]` item through strict fix → reviewer → commit cycles. Adds a reviewer gate between fix and commit. Runs until every item is `- [x]` or `- [~]` (wont-fix).

### Subagents

This repository defines three subagents under `.opencode/agents/`:

| Agent | Model | Purpose |
|-------|-------|---------|
| `reviewer` | deepseek-v4-flash (high variant) | General-purpose review: correctness, clarity, consistency |
| `implementer` | deepseek-v4-flash (low variant) | Implementation: smallest correct change, verify, report |
| `analyst` | claude-haiku-4.5 | Read-only analysis: exploration, categorisation, structured reports |

### Existing Skills

Additional project-specific skills:

| Skill | Purpose |
|-------|---------|
| `spec-lint-fix` | Run markdown linting from `tools/spec-lint/`, fix issues, rerun until clean |
| `spec-review` | Deep-review all spec documents against AGENTS.md, produce or continue REVIEW.md |
| `publish-release` | Quality gate, build, changelog, README review, tag, push, and `gh release create` |
| `commit-push` | Commit and push all changes (update gitignore where needed) |
