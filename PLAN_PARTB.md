# Part B: Perspective-Aware Fan-Out Appraise

## Overview

Add an optional `perspective` field to the Law CRD (full stack: CRD, proto, Librarian store, query filter). Refactor Appraise into a fan-out orchestrator that groups laws by perspective and delegates per-perspective review to a new lightweight Reviewer node. The parent Appraise retains Phase 1 (feedback evaluation) and Phase 3 (finding capture).

**Depends on:** Part A (Agent/Model/Provider refactor) must be completed first. The new Reviewer node will use the clean model abstractions from day one.

## Design Decisions

1. **No dedicated fan-out/fan-in nodes** -- the pattern uses composable SDK helper functions (`FanOut`, `AwaitChildren`, `CollectArtefacts`) built in the previous sprint.

2. **Appraise is an orchestrator, not a reviewer** -- the parent Appraise node does no reviewing itself. All review work is delegated to child Reviewer nodes. The parent consolidates results, evaluates actioned/wont-fix feedback (Phase 1), and captures findings (Phase 3).

3. **New lightweight Reviewer node** -- a dedicated node type that only does Phase 2 (fresh review against a set of laws). It receives all data as artefacts from the parent, not by querying services.

4. **Parent passes laws as artefacts** -- the parent fetches all laws, groups by perspective, and serializes each group into an artefact for the child. The child does not query the Librarian. This keeps the Reviewer node simple and avoids needing `READ:law` capability on children.

5. **Single perspective per law** -- `perspective` is an optional string on the Law CRD. Empty means unset; consumers group unset laws under `"general"`. The semantic default is applied by consumers, not the CRD.

6. **Full stack perspective support** -- the `perspective` field is added to the CRD, proto message, Librarian store, and `LawFilter` so nodes can query by perspective server-side if needed.

7. **Per-perspective system prompt suffix** -- the Appraise config allows extra prompt text per perspective, configured as a map in the ConfigMap. Unknown perspectives use no suffix.

8. **Phase 1 and Phase 3 stay in the parent** -- feedback evaluation (Phase 1) and finding capture (Phase 3) happen in the parent Appraise node after fan-in, not in the children.

## Current Appraise Architecture

The Appraise node currently operates in three phases, all within a single node:

1. **Phase 1 -- Feedback Evaluation** (`EvalAgent`) -- evaluates existing ACTIONED and WONT_FIX feedback items in parallel
2. **Phase 2 -- Fresh Review** (`ReviewAgent`) -- reviews content against governance laws, producing new feedback
3. **Phase 3 -- Finding Capture** (`FindingAgent`) -- distils novel arguments from Phase 1 into Tier 1 Findings

All laws are fetched in a single `QueryLaws` call and fed into one ReviewAgent LLM call.

### Key files (current)

| File | Purpose |
|---|---|
| `nodes/appraise/main.go` | Entrypoint, config, handler, 3-phase orchestration |
| `nodes/appraise/agent_eval.go` | EvalAgent (Phase 1) |
| `nodes/appraise/agent_review.go` | ReviewAgent (Phase 2) |
| `nodes/appraise/agent_finding.go` | FindingAgent (Phase 3) |

## Target Architecture

```
                    Appraise (Orchestrator)
                   /          |           \
          FanOut  /           |            \  FanOut
                 /            |             \
        Reviewer          Reviewer          Reviewer
       (security)       (architecture)     (general)
                 \            |             /
       AwaitChildren + CollectArtefacts    /
                   \          |           /
                    Appraise (Merge + Phase 1 + Phase 3)
                         |
                    RouteToOutput
```

### Parent Appraise flow (new)

1. Fetch input artefact, review artefact, laws, existing feedback (same as today)
2. **Phase 1** -- evaluate actioned/wont-fix feedback (unchanged, stays in parent)
3. **Group laws by perspective** -- `map[string][]*flowv1.Law`, using `"general"` for laws with empty perspective
4. **Build FanOutTasks** -- for each perspective group, create a `flow.FanOutTask` targeting the `reviewer` node with serialized artefacts
5. **FanOut** -> **AwaitChildren** -> **CollectArtefacts** (collect `review-output` from each child)
6. **Merge reviews** -- parse each child's `review-output` JSON, aggregate all feedback items
7. **Stamp + AddFeedback + Cite** -- same as today, but from merged results
8. **Phase 3** -- finding capture from Phase 1 (unchanged)

### Child Reviewer flow

1. Read artefacts: `input`, `review`, `laws`, `history`, `perspective`
2. Build ReviewAgent with perspective-specific system prompt suffix
3. Run ReviewAgent against the laws for this perspective
4. Store `review-output` artefact (JSON feedback array)
5. Call `Complete()`

## Changes by Area

### 1. Law CRD + Proto -- Add `perspective` field

**Files:**

| File | Action | Details |
|---|---|---|
| `proto/flow/v1/common.proto` | Update | Add `string perspective = 9` to `Law` message |
| `proto/flow/v1/librarian.proto` | Update | Add `string perspective = 3` to `LawFilter` message |
| `gen/` | Regenerate | Regenerate protobuf Go code |
| `platform/operator/api/v1/law_types.go` | Update | Add `Perspective string` to `LawSpec` with kubebuilder validation (optional) |
| `platform/operator/config/crd/bases/flow.gideas.io_laws.yaml` | Regenerate | Regenerate CRD with `perspective` field, add printcolumn |

**Details:**

- Optional string field. Empty means unset; the grouping logic interprets empty as `"general"`.
- Add a kubebuilder printcolumn for perspective for `kubectl` visibility.

### 2. Librarian -- Persist and filter by perspective

**Files:**

| File | Action | Details |
|---|---|---|
| `platform/librarian/internal/store/sqlite/store.go` | Update | Add `perspective TEXT NOT NULL DEFAULT ''` column to `laws` table. Include perspective in CreateLaw, CreateLawInactive, UpdateLaw, QueryLaws, and version snapshot. Add schema migration. |
| `platform/librarian/internal/service/librarian_server.go` | Update | Support `perspective` in `LawFilter` for `QueryLaws`. Pass perspective through in `RecordFinding`, `WriteLaw`. |

**Details:**

- `QueryLaws` with `perspective` set returns only laws with that exact perspective value. Empty string in filter means "all perspectives" (no filtering).
- Schema migration: `ALTER TABLE laws ADD COLUMN perspective TEXT NOT NULL DEFAULT ''`. Add `perspective` to `law_versions` table as well for audit.
- Content hash computation (`ComputeContentHash`) must include perspective -- changing a law's perspective creates a new version.

### 3. Operator -- Law controller update

**Files:**

| File | Action | Details |
|---|---|---|
| `platform/operator/internal/controller/law_controller.go` | Update | Map `Perspective` from CRD spec to proto when syncing to Librarian |

### 4. New Reviewer Node

**Files:**

| File | Action | Details |
|---|---|---|
| `nodes/reviewer/main.go` | New | Entrypoint, config, handler |
| `nodes/reviewer/agent_review.go` | New | Extracted/adapted from `nodes/appraise/agent_review.go` |

**Details:**

The Reviewer is a lightweight node that only does Phase 2 (fresh review). It:

1. Receives all data as artefacts from the parent (no Librarian access needed)
2. Deserializes laws and history from JSON artefacts
3. Reads the perspective name and optional system prompt suffix from the `perspective` artefact
4. Builds a ReviewAgent with the perspective's prompt suffix appended to the system prompt
5. Runs the ReviewAgent
6. Stores the review output as the `review-output` artefact
7. Calls `Complete()`

**Config** (ConfigMap):

```yaml
inputArtefact: input
reviewArtefact: review
```

The model is chosen in code (per Part A -- no model in config). Artefact names for the serialized data (`laws`, `history`, `perspective`, `review-output`) are conventions between the parent Appraise and the Reviewer, not configurable.

### 5. Refactored Appraise Node (Orchestrator)

**Files:**

| File | Action | Details |
|---|---|---|
| `nodes/appraise/main.go` | Major refactor | `handleAppraise()` becomes fan-out orchestrator. Phase 2 (review) moves to children. |
| `nodes/appraise/agent_review.go` | Remove | Review logic moves to Reviewer node |

**Config changes** (`appraiseConfig`):

```go
type appraiseConfig struct {
    InputArtefact      string            `yaml:"inputArtefact"`
    ReviewArtefact     string            `yaml:"reviewArtefact"`
    GovernedArtefact   string            `yaml:"governedArtefact"`
    StampName          string            `yaml:"stampName"`
    ReviewerNode       string            `yaml:"reviewerNode"`
    PerspectivePrompts map[string]string `yaml:"perspectivePrompts"`
}
```

- `Model` field removed (per Part A)
- `ReviewerNode` added -- the target node name for fan-out (e.g. `"reviewer"`)
- `PerspectivePrompts` added -- maps perspective name to system prompt suffix

**New `handleAppraise()` flow:**

1. Fetch input artefact, review artefact, laws, existing feedback
2. Phase 1 -- evaluate actioned/wont-fix feedback (unchanged)
3. Group laws by perspective (`groupLawsByPerspective()` helper)
4. For each perspective group, build a `flow.FanOutTask`:
   - Target: `cfg.ReviewerNode`
   - Artefacts: `input`, `review`, `laws` (JSON), `history` (JSON), `perspective` (JSON with name + prompt suffix)
5. `client.FanOut(ctx, tasks)`
6. `client.AwaitChildren(ctx)`
7. `client.CollectArtefacts(ctx, statuses, "review-output")`
8. Parse and merge all review outputs
9. Stamp + AddFeedback + Cite (from merged results)
10. Phase 3 -- finding capture (unchanged)

### 6. Artefact Serialization Contract

The parent-to-child data contract:

| Artefact ID | Direction | Format | Content |
|---|---|---|---|
| `input` | Parent -> Child | Plain text (UTF-8) | Creative brief / petition text |
| `review` | Parent -> Child | Plain text (UTF-8) | The artefact under review |
| `laws` | Parent -> Child | JSON | `[{"id":"...","tier":N,"goal":"..."}]` |
| `history` | Parent -> Child | JSON | `[{"state":"...","message":"..."}]` |
| `perspective` | Parent -> Child | JSON | `{"name":"security","promptSuffix":"..."}` |
| `review-output` | Child -> Parent | JSON | `{"feedback":[{"message":"...","severity":"...","cited_laws":["..."]}]}` |

**GovernedArtefact for child artefacts:** The child's artefacts are internal data transfer, not governed work products. They should use a dedicated governed artefact name (e.g. `"review-data"`) or be ungoverned if the system supports that.

### 7. ConfigMap / Manifests

**Files:**

| File | Action | Details |
|---|---|---|
| `nodes/haiku-manifests/configmaps.yaml` | Update | Update appraise config with `reviewerNode` and `perspectivePrompts` map. Add reviewer config. |

**Example appraise config:**

```yaml
inputArtefact: petition
reviewArtefact: haiku
governedArtefact: haiku
stampName: review
reviewerNode: reviewer
perspectivePrompts:
  security: "Pay special attention to information disclosure and injection risks."
  architecture: "Focus on structural patterns, separation of concerns, and maintainability."
  style: "Evaluate adherence to style guides, naming conventions, and consistency."
```

**Example reviewer config:**

```yaml
inputArtefact: input
reviewArtefact: review
```

### 8. Tests

| Area | Tests |
|---|---|
| Law CRD/proto | Validate perspective field serialization round-trip |
| Librarian store | Perspective column persistence, migration, QueryLaws with perspective filter, QueryLaws without perspective filter (all perspectives), content hash includes perspective |
| Librarian server | `QueryLaws` with perspective in `LawFilter`, `RecordFinding` with perspective, `WriteLaw` with perspective |
| Operator law controller | Perspective field passed through to Librarian on sync |
| Reviewer node | Unit tests: happy path review, empty laws, perspective prompt suffix injection, review-output artefact format |
| Appraise orchestration | Law grouping by perspective (`groupLawsByPerspective`), FanOut task construction, review output merge logic, per-perspective prompt suffix from config, empty perspective defaults to "general" |
| Integration | Update existing appraise E2E tests if they exist |

### 9. Spec Updates

| File | Change |
|---|---|
| `specs/05-reference/crds.md` | Add `perspective` field to Law CRD reference table |
| `specs/01-concepts/03-data-model.md` | Mention perspective in Laws section |
| `specs/02-flow/04-system-services.md` | Update Librarian section with perspective filtering |

## Execution Order

1. Proto + generated code (perspective on `Law`, `LawFilter`)
2. CRD types + regenerate CRD YAML
3. Librarian store (schema migration, persist perspective, filter by perspective)
4. Librarian server (support perspective in QueryLaws, RecordFinding, WriteLaw)
5. Operator law controller (pass perspective through)
6. Reviewer node (new)
7. Appraise refactor (orchestrator mode)
8. ConfigMap updates
9. Tests throughout (each step includes its tests)
10. Spec updates
11. Quality gates: `go test ./...`, `make check-fix`, `make lint-operator`

## Open Questions (to resolve during implementation)

1. **GovernedArtefact for child artefacts** -- what governed artefact name should the child's internal data artefacts use? The parent's `cfg.GovernedArtefact` (e.g. `"haiku"`), a dedicated one like `"review-data"`, or ungoverned?

2. **Reviewer node deployment** -- does the Reviewer need its own FoundryNode CRD manifest, Deployment, and Service in the haiku manifests? Or is it provisioned differently?

3. **Law representations in child artefact** -- the current ReviewAgent only uses `Id`, `Tier`, and `Goal` from each law (not representations). The serialized laws artefact should only include these three fields to keep the contract minimal. Confirm this is sufficient.

## Quality Gates

All changes must pass before commit:

1. `go test ./...` -- all tests pass
2. `make check-fix` -- lint clean (2 pre-existing issues in `sdk/go/child_test.go` and `sdk/go/testutil_test.go` are known and accepted)
3. `make lint-operator` -- 0 issues
