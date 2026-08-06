# AGENTS.md

## Project

This repository contains the technical specification and reference implementation for **Foundry Flow** — a governed workflow runtime on Kubernetes.

## Repository Structure

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

1. **The Contract** (`proto/`) — The wire protocol that binds the components.
2. **Implementation** — The code in `platform/operator`, `platform/sidecar`, and `sdk`.

## Hard Rule: The Repo Is Always Green

`make verify` (tests → lint → build) **must pass with zero failures** before any commit. This is the only acceptable state of the repository. There is no such thing as a "pre-existing" or "unrelated" failure — if `make verify` fails, the work is incomplete, period.

If you encounter a failure that you did not introduce, you still fix it. The failure is real. The repo was not green when you started, and it must be green when you finish. This is non-negotiable.

All changes to this repository **must** pass the following before being committed:

1. **Tests** — `go test ./...` (or the relevant subset) passes. New functionality requires new or updated tests.
2. **Lint** — `make check-fix` resolves every issue. Do not commit with lint failures.
3. **Build** — `make build` compiles every binary without errors.

For convenience, `make verify` runs all three in sequence. If it fails, you stop everything and fix the root cause — you do not proceed, you do not rationalise, you do not defer.

## Foundational Axioms

1. **Assume Unreliability** — All agents are fallible. Trust intent, verify execution.
2. **Make Work Auditable** — Every action becomes an immutable, traceable record.
3. **Make the Cost Visible** — Friction is a first-class, quantifiable signal.
4. **Quality is Fixed, Cost is Variable** — The standard is non-negotiable; the system measures the cost of achieving it.

## Coding Ethos

Before writing any code, stop at the first rung that holds:

1. Does this need to be built at all? (YAGNI)
2. Does it already exist in this codebase? Reuse the helper, util, or pattern already here — don't re-write it.
3. Does the standard library already do this? Use it.
4. Does a native platform feature cover it? Use it.
5. Does an already-installed dependency solve it? Use it.
6. Can this be one line? Make it one line.
7. Only then: write the minimum code that works.

The ladder runs *after* you understand the problem, not instead of it: read the task and the code it touches, trace the real flow end to end, then climb.

### Bug Fix Rule

Fix root cause, not symptom. A report names a symptom. Grep every caller of the function you touch and fix the shared function once — one guard there is a smaller diff than one per caller, and patching only the path the ticket names leaves a sibling caller still broken.

### Rules

- No abstractions that weren't explicitly requested.
- No new dependency if it can be avoided.
- No boilerplate nobody asked for.
- Deletion over addition. Boring over clever. Fewest files possible.
- Shortest working diff wins, but only once you understand the problem. The smallest change in the wrong place isn't lazy — it's a second bug.
- Mark intentional simplifications with a `ponytail:` comment. If the short-cut has a known ceiling (global lock, O(n²) scan, naive heuristic), the comment names the ceiling and the upgrade path.
- **`ponytail:` is not a deferral mechanism.** A ponytail documents a trade-off that is genuinely justified — the simplification is correct and the ceiling is acceptable for the foreseeable future. If the "fix" is straightforward (add a constant, copy a map, use a proper error type) and the only reason not to do it is laziness, it is not a ponytail — it is a deferred fix. Deferred fixes get `[FIX]` in reviews, not `[PONYTAIL]`. The question to ask: "Would I accept this code if the ponytail comment were deleted?" If the answer is no, it is not a ponytail.
- Non-trivial logic leaves ONE runnable check behind — the smallest thing that fails if the logic breaks (an assert-based self-check or one small test file; no frameworks, no fixtures). Trivial one-liners need no test.

### Never Negotiable

Trust-boundary validation, data-loss handling, security, and accessibility are never on the chopping block. Lazy code without its safety net is just reckless.

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
├── REVIEW.md        # Review criteria + pass log (one line per review pass)
├── REVIEW_ITEMS.md  # Spec-compliance audit checklist
└── LEARNINGS.md     # Patterns, known deviations, and candidate patterns
```

**Important:** `plans/` is gitignored by design — its contents are never committed. Because the Glob tool relies on the git index, it will not find files under `plans/`. You **must** use `ls` (via Bash) to list directory contents under `plans/`.

### Worktree Convention

Phased plan execution uses isolated git worktrees under `.worktrees/`:

```
.worktrees/<project-slug>/   # git worktree for dev/<project-slug> branch
```

Worktrees are created with:

```bash
git worktree add -b dev/<project-slug> .worktrees/<project-slug> HEAD
```

All implementation, verification, review, and commits happen inside the worktree. The `.worktrees/` directory is gitignored — never commit worktree contents to `main`.

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

#### 4. Review (`special-review`)

Review files against provided criteria. The criteria is read from `REVIEW.md` if it exists; if not, it is a fresh review and the user confirms the criteria. Verifies and prunes prior resolved/wont-fix items, captures learnings, then runs a full fresh review with parallel subagents. Every finding is classified with a remediation tag (`[FIX]` / `[PONYTAIL]` / `[IMPL-NOTE]`). Output: `REVIEW_ITEMS.md` checklist; `REVIEW.md` criteria + pass log.

Run: `special-review` skill.

#### 5. Fix (`special-fixer`)

Fix items from a `REVIEW_ITEMS.md` checklist. Groups items by target file, dispatches one implementer per group. Each implementer verifies the claim before acting, applies the fix matching the classification tag, or marks wont-fix with justification. Supports REPL-mode re-evaluation cycles.

Run: `special-fixer` skill.

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
| `publish-release` | Quality gate, build, changelog, README review, tag, push, and `gh release create` |
| `commit-push` | Commit and push all changes (update gitignore where needed) |
| `ponytail-review` | Review diffs for over-engineering: what to delete, simplify, or replace with stdlib/native equivalents |
| `ponytail-audit` | Whole-repo audit for over-engineering: writes ranked checklist to plans/AUDIT.md |
| `ponytail-fix` | Fix ponytail audit items one at a time: implement → ponytail-review → normal review → quality gate → commit |
