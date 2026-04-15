# Phase XX - Cleanup, Deletion, and Validation (Always Last)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove all superseded Advocate/cross-flow artefacts, fix stale terminology in proto and specs, update AGENTS.md, and pass all quality gates.

**Architecture:** `ARCHITECTURE.md` is the source of truth. Embassy replaces Advocate. Federation service replaces Governance Flow runtime. `importNode` is replaced by `crossFlow.importTypes`. All `legacy/` files are historical and not modified (they are excluded from spec-lint and quality gates).

**Tech Stack:** Go, Protocol Buffers (buf generate), Kubernetes CRDs, Makefile, Node.js spec-lint

**Quality Gates (non-negotiable per `AGENTS.md`):**

1. `make test-all` -- all tests pass
2. `make check-fix-all` -- all lint/tidy clean
3. `node lint.mjs` in `tools/spec-lint/` -- all specs clean
4. Grep verification -- no orphaned references outside `legacy/` and planning docs

---

## Parallelism Guide

- **Batch A (Tasks 1-4):** Independent deletions -- can be dispatched in parallel
- **Batch B (Tasks 5-8):** Independent doc/proto fixes -- can be dispatched in parallel
- **Sequential (Tasks 9-11):** Must run after Batches A and B complete, in order

---

## Task 1: Delete `nodes/advocate/` directory

**Context:** The Advocate node is superseded by the Embassy boundary node + generic HITL nodes (per ARCHITECTURE.md "What Gets Removed"). The directory contains 3 files: `main.go`, `main_test.go`, `testutil_test.go`. It is part of the `nodes` Go module (`nodes/go.mod`, module `github.com/gideas/flow/nodes`). No separate `go.mod` in the advocate directory. The `./nodes/...` test glob auto-excludes deleted subdirectories. `bin/` is gitignored so the built binary is not tracked.

**Files:**

- Delete: `nodes/advocate/main.go`
- Delete: `nodes/advocate/main_test.go`
- Delete: `nodes/advocate/testutil_test.go`

- [x] **Step 1: Delete the advocate directory**

```bash
rm -rf nodes/advocate/
```

- [x] **Step 2: Verify deletion**

```bash
ls nodes/advocate/ 2>&1
```

Expected: `No such file or directory`

- [x] **Step 3: Commit**

```bash
git add -A nodes/advocate/ && git commit -m "remove: delete nodes/advocate/ (superseded by Embassy + HITL nodes)"
```

---

## Task 2: Remove Advocate from Makefile

**Context:** The Makefile has two advocate references: (1) `build-advocate` in the `build:` dependency list on line 68, and (2) the standalone `build-advocate` target on lines 98-100. No other Makefile references exist.

**Files:**

- Modify: `Makefile`

- [x] **Step 1: Remove `build-advocate` from the `build:` dependency list on line 68**

The current line 68 reads:

```makefile
build: build-sidecar build-null-node build-forge build-sort build-appraise build-reviewer build-refine build-advocate build-arbiter build-juror build-codify-smt build-codification build-rule-router build-facilitator build-hitl build-law-applicator build-tribunal build-friction-watcher build-ttl-watcher build-archivist build-monitor build-eventbus build-frictionledger build-librarian ## Build all binaries.
```

Change to (remove `build-advocate` from the list):

```makefile
build: build-sidecar build-null-node build-forge build-sort build-appraise build-reviewer build-refine build-arbiter build-juror build-codify-smt build-codification build-rule-router build-facilitator build-hitl build-law-applicator build-tribunal build-friction-watcher build-ttl-watcher build-archivist build-monitor build-eventbus build-frictionledger build-librarian ## Build all binaries.
```

- [x] **Step 2: Delete the `build-advocate` target (lines 98-100)**

Delete these exact three lines:

```makefile
.PHONY: build-advocate
build-advocate: ## Build the Advocate node binary.
	CGO_ENABLED=1 go build -o bin/advocate ./nodes/advocate
```

Note: line 100 has a tab indent before `CGO_ENABLED=1`. The blank line after line 100 (before the `build-arbiter` target) should remain.

- [x] **Step 3: Verify no advocate references remain in Makefile**

```bash
grep -i "advocate" Makefile
```

Expected: No output.

- [x] **Step 4: Commit**

```bash
git add Makefile && git commit -m "remove: drop build-advocate target from Makefile"
```

---

## Task 3: Remove `advocate-context` GovernedArtefact from haiku manifests

**Context:** `nodes/haiku-manifests/flow.yaml` contains a YAML document defining the `advocate-context` GovernedArtefact (lines 149-156). This is the only advocate reference in the haiku manifests.

**Files:**

- Modify: `nodes/haiku-manifests/flow.yaml`

- [x] **Step 1: Delete the advocate-context GovernedArtefact document**

Remove these exact lines (including the leading `---` separator):

```yaml
---
apiVersion: flow.gideas.io/v1
kind: GovernedArtefact
metadata:
  name: advocate-context
  namespace: default
spec:
  stamps: []
```

The line immediately before this block is `  stamps: []` (end of the `codification-result` document). The line immediately after is `---` (start of the `human-decision` document). After deletion, the `codification-result` document's final `  stamps: []` should be followed by `---` for the `human-decision` document.

- [x] **Step 2: Verify no advocate references remain in haiku-manifests**

```bash
grep -ri "advocate" nodes/haiku-manifests/
```

Expected: No output.

- [x] **Step 3: Commit**

```bash
git add nodes/haiku-manifests/flow.yaml && git commit -m "remove: delete advocate-context GovernedArtefact from haiku manifests"
```

---

## Task 4: Update `AGENTS.md` -- remove Advocate from repo structure

**Context:** `AGENTS.md` line 51 lists `advocate/` in the repository structure tree. This is the only advocate reference in the file.

**Files:**

- Modify: `AGENTS.md`

- [x] **Step 1: Delete the advocate line from the repo structure tree**

Remove this exact line (line 51):

```
│   ├── advocate/         # Governance escalation gateway (deferred redesign)
```

The line before is `│   ├── refine/           # Revision node` and the line after is `│   ├── arbiter/          # Deadlock deliberation orchestrator`.

- [x] **Step 2: Verify no advocate references remain in AGENTS.md**

```bash
grep -i "advocate" AGENTS.md
```

Expected: No output.

- [x] **Step 3: Commit**

```bash
git add AGENTS.md && git commit -m "docs: remove advocate from AGENTS.md repo structure"
```

---

## Task 5: Fix stale proto comments in `librarian.proto` + regenerate

**Context:** `proto/flow/v1/librarian.proto` has two stale comments: (1) `WriteLaw` comment (line 36-37) references "Judiciary Gate" (eliminated) and "Governance Flow" (eliminated as runtime concept), (2) `ReplicateLaws` comment (lines 44-45) says "from a remote Librarian" (should be Federation service) and "two-stage conflict protocol" (conflict detection is in Federation admission, not Librarian). The generated code in `gen/flow/v1/librarian_grpc.pb.go` propagates these comments and will auto-update when `buf generate` runs.

**Files:**

- Modify: `proto/flow/v1/librarian.proto`
- Regenerate: `gen/flow/v1/librarian_grpc.pb.go` (via `buf generate`)

- [x] **Step 1: Fix WriteLaw comment (lines 36-37)**

Change these exact lines:

```protobuf
  // Persists a law (Tier 2 Ruling applied by the Judiciary Gate after petition
  // approval, Tier 3+ applied by administrator or Governance Flow).
```

To:

```protobuf
  // Persists a law (Tier 1-2 applied by law-applicator after petition
  // approval, Tier 3 applied by law-applicator or administrator).
```

- [x] **Step 2: Fix ReplicateLaws comment (lines 44-45)**

Change these exact lines:

```protobuf
  // Receives higher-tier laws from a remote Librarian for integration.
  // Triggers the two-stage conflict protocol.
```

To:

```protobuf
  // Stores laws received from the Federation service for local materialisation.
  // Subscriber Flows receive published laws as Tier 4 or Tier 5.
```

- [x] **Step 3: Regenerate proto code**

```bash
buf generate
```

Expected: `gen/flow/v1/librarian_grpc.pb.go` (and potentially other gen files) are regenerated. The Makefile target is `make proto` which runs `buf generate`.

- [x] **Step 4: Verify generated comments are updated**

```bash
grep -n "Governance Flow" gen/flow/v1/librarian_grpc.pb.go
grep -n "remote Librarian" gen/flow/v1/librarian_grpc.pb.go
grep -n "Judiciary Gate" gen/flow/v1/librarian_grpc.pb.go
```

Expected: No output for any grep.

- [x] **Step 5: Commit**

```bash
git add proto/flow/v1/librarian.proto gen/ && git commit -m "fix: update stale WriteLaw/ReplicateLaws proto comments (remove Governance Flow, Judiciary Gate references)"
```

---

## Task 6: Update glossary -- clean Advocate, importNode, and Governance Flow comparison language

**Context:** The glossary uses "replaces the old X" language for things that are now fully removed. With the cleanup complete, these historical comparisons should be simplified. Four entries need attention.

**Files:**

- Modify: `specs/05-reference/glossary.md`

- [x] **Step 1: Simplify HITL node entry (line 187)**

The full current line 187 reads:

```
A generic config-driven Human-in-the-Loop node. Single image, multiple CRD instances. It is used for hung-jury resolution and for human approval in the Clerk cycle's Tier 3-5 petition path. Uses the SDK [HITL pattern](../04-sdk/08-sdk-hitl.md) with `USE:queue/server` capability. Replaces the old Advocate-specific human boundary with a reusable node pattern. Detail: [SDK HITL](../04-sdk/08-sdk-hitl.md).
```

Change `Replaces the old Advocate-specific human boundary with a reusable node pattern.` to `Provides a reusable node pattern for human-in-the-loop decisions.`

So the full line becomes:

```
A generic config-driven Human-in-the-Loop node. Single image, multiple CRD instances. It is used for hung-jury resolution and for human approval in the Clerk cycle's Tier 3-5 petition path. Uses the SDK [HITL pattern](../04-sdk/08-sdk-hitl.md) with `USE:queue/server` capability. Provides a reusable node pattern for human-in-the-loop decisions. Detail: [SDK HITL](../04-sdk/08-sdk-hitl.md).
```

- [x] **Step 2: Simplify crossFlow.importTypes entry (line 27)**

The full current line 27 reads:

```
A map on the FoundryFlow CRD's `spec.crossFlow` that defines the flow-authored/custom import type extension set for cross-flow Workitem reception. Each key is an import type name; each value specifies a target node (must be entry-bound) and optional per-artefact foreign-stamp requirements. Replaces the former `importNode` field. Built-in system import types such as `law-petition` live in the same effective namespace but are not authored in this map. Detail: [CRDs](./crds.md#cross-flow-configuration).
```

Remove the sentence `Replaces the former `importNode` field.` so the line becomes:

```
A map on the FoundryFlow CRD's `spec.crossFlow` that defines the flow-authored/custom import type extension set for cross-flow Workitem reception. Each key is an import type name; each value specifies a target node (must be entry-bound) and optional per-artefact foreign-stamp requirements. Built-in system import types such as `law-petition` live in the same effective namespace but are not authored in this map. Detail: [CRDs](./crds.md#cross-flow-configuration).
```

- [x] **Step 3: Simplify Federation entry (line 39)**

The full current line 39 reads:

```
The control-plane authority that manages inter-Flow membership, trust-root discovery, state groupings, authority publisher roles, petition-routing policy, and published-law distribution. A Federation replaces the former Governance Flow runtime concept: member Flows remain ordinary Flows, while the Federation service governs how T4-T5 authority relationships and publication work across them. Detail: [Governance](../01-concepts/04-governance.md#federation-membership), [Federation](../02-flow/08-federation.md), [gRPC API](./grpc-api.md#federation-api).
```

Change `A Federation replaces the former Governance Flow runtime concept: member Flows remain ordinary Flows` to `Member Flows remain ordinary Flows` so the line becomes:

```
The control-plane authority that manages inter-Flow membership, trust-root discovery, state groupings, authority publisher roles, petition-routing policy, and published-law distribution. Member Flows remain ordinary Flows, while the Federation service governs how T4-T5 authority relationships and publication work across them. Detail: [Governance](../01-concepts/04-governance.md#federation-membership), [Federation](../02-flow/08-federation.md), [gRPC API](./grpc-api.md#federation-api).
```

- [x] **Step 4: Simplify Sibling Flow entry (line 371)**

The full current line 371 reads:

```
A Flow that shares membership in at least one federation-defined state with another Flow. Sibling relationships derive from shared state membership and federation policy, not from a dedicated Governance Flow runtime. Federation-member exchange between sibling Flows uses the federation trust root; Treaties are only needed for non-federation exchange.
```

Change `not from a dedicated Governance Flow runtime` to `not from a dedicated runtime` so the line becomes:

```
A Flow that shares membership in at least one federation-defined state with another Flow. Sibling relationships derive from shared state membership and federation policy, not from a dedicated runtime. Federation-member exchange between sibling Flows uses the federation trust root; Treaties are only needed for non-federation exchange.
```

- [x] **Step 5: Verify no stale references remain in glossary**

```bash
grep -in "advocate\|importNode\|Governance Flow" specs/05-reference/glossary.md
```

Expected: No output.

- [x] **Step 6: Commit**

```bash
git add specs/05-reference/glossary.md && git commit -m "docs: remove Advocate/importNode/Governance Flow comparison language from glossary"
```

---

## Task 7: Update `crds.md` -- simplify importNode comparison

**Context:** `specs/05-reference/crds.md` line 502 has a design note that says "`crossFlow.importTypes` replaces the former `importNode` field". With the cleanup complete, the historical comparison is unnecessary.

**Files:**

- Modify: `specs/05-reference/crds.md`

- [x] **Step 1: Simplify the crossFlow.importTypes design note (line 502)**

Change this exact line:

```
12. `crossFlow.importTypes` replaces the former `importNode` field for flow-authored import types. Built-in system import types (currently `law-petition`) share the same effective namespace but are platform-owned rather than YAML-authored.
```

To:

```
12. `crossFlow.importTypes` defines the flow-authored import type extension set. Built-in system import types (currently `law-petition`) share the same effective namespace but are platform-owned rather than YAML-authored.
```

- [x] **Step 2: Verify no importNode references remain in crds.md**

```bash
grep -in "importNode" specs/05-reference/crds.md
```

Expected: No output.

- [x] **Step 3: Commit**

```bash
git add specs/05-reference/crds.md && git commit -m "docs: remove importNode comparison from crds.md design notes"
```

---

## Task 8: Simplify `grpc-api.md` Embassy section

**Context:** `specs/05-reference/grpc-api.md` line 362 says "It replaces the former Operator-centric `ExportWorkitem`/`ImportWorkitem` methods with a manifest-preflight and package-streaming protocol." The old RPCs are fully removed and the comparison is no longer needed.

**Files:**

- Modify: `specs/05-reference/grpc-api.md`

- [x] **Step 1: Simplify the Embassy API description (line 362)**

Change this exact line:

```
The Embassy is the cross-flow gateway node responsible for Workitem import/export. It replaces the former Operator-centric `ExportWorkitem`/`ImportWorkitem` methods with a manifest-preflight and package-streaming protocol. The Embassy is Operator-provisioned and holds cross-flow transfer capabilities.
```

To:

```
The Embassy is the cross-flow gateway node responsible for Workitem import/export, using a manifest-preflight and package-streaming protocol. The Embassy is Operator-provisioned and holds cross-flow transfer capabilities.
```

- [x] **Step 2: Verify no ExportWorkitem/ImportWorkitem references remain in grpc-api.md**

```bash
grep -in "ExportWorkitem\|ImportWorkitem" specs/05-reference/grpc-api.md
```

Expected: No output.

- [x] **Step 3: Commit**

```bash
git add specs/05-reference/grpc-api.md && git commit -m "docs: simplify Embassy API description (remove ExportWorkitem/ImportWorkitem comparison)"
```

---

## Task 9: Run quality gates

**Context:** All code/doc changes from Tasks 1-8 must be complete before running quality gates. These are the non-negotiable gates from `AGENTS.md`.

**Dependencies:** Tasks 1-8 must all be complete.

- [x] **Step 1: Run all tests**

```bash
make test-all
```

Expected: All tests pass. Zero failures. The `test-nodes` target (`./nodes/...`) will auto-exclude the deleted `nodes/advocate/` directory.

- [x] **Step 2: Run lint and tidy**

```bash
make check-fix-all
```

Expected: Clean exit. No lint failures. If `check-fix-all` auto-fixes anything (goimports, tidy), those changes need to be committed.

- [x] **Step 3: Run spec lint**

```bash
cd tools/spec-lint && node lint.mjs
```

Expected: All specs clean. Zero violations. The spec-lint tool excludes `legacy/`, `node_modules/`, `tools/spec-lint/`, and plan files by default.

- [x] **Step 4: Commit any auto-fix changes**

If `check-fix-all` or spec-lint identified/fixed issues:

```bash
git add -A && git commit -m "chore: auto-fix lint/tidy from quality gates"
```

If no changes, skip this step.

---

## Task 10: Orphaned reference sweep

**Context:** Verify no orphaned references remain outside `legacy/` and planning docs. The following patterns are checked: Advocate (the node, not `devils-advocate` juror personality), `importNode`, Governance Flow as a runtime primitive, `ExportWorkitem`/`ImportWorkitem`, and `pending-decision` Tier 1 laws.

**Important:** `devils-advocate` / `devil's advocate` in `nodes/juror/main.go` is a juror personality name, NOT an Advocate node reference. These must be excluded from the sweep. Retirement guard tests (files named `*_retirement_test.go`) that assert old symbols do NOT exist are intentional and acceptable.

**Dependencies:** Tasks 1-9 must all be complete.

- [x] **Step 1: Sweep for Advocate**

```bash
grep -ri "advocate" --include='*.go' --include='*.proto' --include='*.yaml' --include='*.md' . \
  | grep -v "legacy/" | grep -v "PLAN.md" | grep -v "PHASE_" | grep -v "PHASES_" | grep -v "ARCHITECTURE.md" \
  | grep -v "devils-advocate" | grep -v "devil's advocate"
```

Expected: No output.

- [x] **Step 2: Sweep for importNode**

```bash
grep -ri "importNode\|import_node\|ImportNode" --include='*.go' --include='*.proto' --include='*.yaml' --include='*.md' . \
  | grep -v "legacy/" | grep -v "PLAN.md" | grep -v "PHASE_" | grep -v "PHASES_" | grep -v "ARCHITECTURE.md"
```

Expected: Only retirement guard tests in these files (these are intentional -- they assert the old field does NOT exist):
- `platform/operator/api/v1/foundryflow_types_test.go` (assertNotContains "importNode")
- `platform/operator/api/v1/config_artifacts_test.go` (assertNotContains "importNode:")

Zero stale usage.

- [x] **Step 3: Sweep for Governance Flow as runtime**

```bash
grep -ri "Governance Flow\|GovernanceFlow\|governance-flow\|governance_flow" --include='*.go' --include='*.proto' --include='*.yaml' --include='*.md' . \
  | grep -v "legacy/" | grep -v "PLAN.md" | grep -v "PHASE_" | grep -v "PHASES_" | grep -v "ARCHITECTURE.md"
```

Expected: Only the retirement guard test `gen/flow/v1/federation_proto_retirement_test.go` (asserts `GovernanceFlow` does NOT exist in federation.proto) and `specs/01-concepts/04-governance.md` line 129 which says "There is no special Governance Flow runtime" (correct architectural statement). Zero stale usage.

- [x] **Step 4: Sweep for ExportWorkitem/ImportWorkitem**

```bash
grep -ri "ExportWorkitem\|ImportWorkitem" --include='*.go' --include='*.proto' --include='*.yaml' --include='*.md' . \
  | grep -v "legacy/" | grep -v "PLAN.md" | grep -v "PHASE_" | grep -v "PHASES_" | grep -v "ARCHITECTURE.md"
```

Expected: Only retirement guard tests in these files (intentional):
- `gen/flow/v1/federation_proto_retirement_test.go`
- `gen/flow/v1/operator_proto_retirement_test.go`
- `platform/sidecar/internal/mock/legacy_crossflow_retirement_test.go`

Zero stale usage.

- [x] **Step 5: Sweep for pending-decision Tier 1**

```bash
grep -ri "pending-decision\|pending_decision" --include='*.go' --include='*.proto' --include='*.yaml' --include='*.md' . \
  | grep -v "legacy/" | grep -v "PLAN.md" | grep -v "PHASE_" | grep -v "PHASES_" | grep -v "ARCHITECTURE.md"
```

Expected: No output.

- [x] **Step 6: If any sweep finds unexpected results, investigate and fix before proceeding**

If any stale reference is found that is NOT a retirement guard test or an explicit "there is no X" architectural statement, fix it and re-run the quality gates (Task 9).

---

## Task 11: Update PLAN.md status

**Context:** Mark Phase XX complete and update the "Next active work" line. This is the final bookkeeping step.

**Dependencies:** Tasks 9-10 must pass.

**Files:**

- Modify: `PLAN.md`

- [x] **Step 1: Update status line**

Change line 17 from:

```
- Next active work: `PHASE_XX.md` (cleanup, deletion, validation, and final regression gates).
```

To:

```
- Phase XX is complete. All phases of the Embassy + Federation redesign are done.
```

- [x] **Step 2: Update active execution order entry**

Change line 69 from:

```
6. `PHASE_XX.md` - next active phase: delete Advocate, remove stale `importNode` / Governance Flow paths, and run final quality gates.
```

To:

```
6. `PHASE_XX.md` - complete: Advocate deleted, stale importNode / Governance Flow references removed, all quality gates passed.
```

- [x] **Step 3: Commit**

```bash
git add PLAN.md && git commit -m "docs: mark Phase XX complete in PLAN.md"
```

---

## Summary: Execution Order and Parallelism

```
Batch A (parallel):  Task 1, Task 2, Task 3, Task 4
Batch B (parallel):  Task 5, Task 6, Task 7, Task 8
Sequential:          Task 9 → Task 10 → Task 11
```

| Task | Description | Dependencies | Estimated Effort |
|------|-------------|--------------|------------------|
| 1 | Delete `nodes/advocate/` | none | 1 min |
| 2 | Remove Advocate from Makefile | none | 2 min |
| 3 | Remove `advocate-context` from haiku manifests | none | 1 min |
| 4 | Update `AGENTS.md` | none | 1 min |
| 5 | Fix stale proto comments + regenerate | none | 3 min |
| 6 | Update glossary | none | 3 min |
| 7 | Update `crds.md` | none | 1 min |
| 8 | Simplify `grpc-api.md` | none | 1 min |
| 9 | Run quality gates | Tasks 1-8 | 5 min |
| 10 | Orphaned reference sweep | Task 9 | 3 min |
| 11 | Update PLAN.md | Task 10 | 1 min |

## Key Context for Cold-Start Sessions

- **`devils-advocate`** in `nodes/juror/main.go` is a juror personality, NOT an Advocate node reference. Do not delete.
- **Retirement guard tests** (`*_retirement_test.go`) assert that old symbols do NOT exist. These are intentional guards and should appear in sweep results. They are not stale.
- **`specs/01-concepts/04-governance.md` line 129** says "There is no special Governance Flow runtime" -- this is a correct architectural statement, not stale terminology.
- **`bin/` is gitignored.** The `bin/advocate` binary is a local build artifact and not tracked. No git cleanup needed.
- **`legacy/` directory** contains historical pre-Embassy specs. These are excluded from spec-lint and quality gates. Do not modify.
- **Proto regeneration** uses `buf generate` (Makefile target: `make proto`). This regenerates all files in `gen/`.
- **`nodes/advocate/` is part of the shared `nodes` Go module** (`nodes/go.mod`). Deleting the directory is sufficient; no `go.mod` changes needed.
