# Phase 14 - Judiciary Manifests (CRDs, Deployments, ConfigMaps)

Write the FoundryNode CRDs, Deployment manifests, and ConfigMaps for all
judiciary nodes. Embassy is operator-provisioned and excluded from hand-authored
manifests. The Federation service is a platform service, also outside this
manifest set.

Historical Phase 7 context is preserved in `PHASES_01_09.md`.

## Completed

- `7.1` (GovernedArtefact stamps) is complete.
- `7.2a` (Judiciary GovernedArtefact CRDs) is complete.
- `7.5` (label cleanup) is complete.

## Architecture Source of Truth

`ARCHITECTURE.md` — Node Inventory (lines 863-924), Topology (lines 133-164),
Rule Router (lines 163-203), Generic HITL (lines 205-222), Clerk Cycle
(lines 556-658), Arbiter Path (lines 480-521), Tribunal Path (lines 523-554).

## Quality Gates (AGENTS.md, non-negotiable)

1. `go test ./...` — all tests pass. New functionality requires new/updated tests.
2. `make check-fix` — resolves all lint issues. No commit with lint failures.

## Files Modified

| File | Action | Content |
|---|---|---|
| `nodes/haiku-manifests/flow.yaml` | Modify | Add judiciary FoundryNode CRDs, update Sort/Facilitator outputs, add FoundryFlow nodeGroups + judiciary entry/exit contracts |
| `nodes/haiku-manifests/configmaps.yaml` | Modify | Add ConfigMaps for all judiciary nodes that need config |
| `nodes/haiku-manifests/deployments.yaml` | Modify | Add Deployment manifests for all judiciary nodes |

## Conventions (from existing manifests)

- **FoundryNode label**: `flow.gideas.io/flow-name: haiku-flow`
- **Deployment labels**: `app.kubernetes.io/name: <node>` + `flow.gideas.io/node-name: <node>`
- **Sidecar env (always)**: `FLOW_NODE_ID`, `FLOW_SIDECAR_PORT`, `OPERATOR_ADDRESS`, `ARCHIVIST_ADDRESS`
- **Sidecar env (conditional)**: `LIBRARIAN_ADDRESS`, `EVENT_BUS_ADDRESS`, `FRICTION_LEDGER_ADDRESS`
- **Node env (conditional)**: `OLLAMA_BASE_URL` (only LLM-using nodes)
- **Ports**: node `:50053`, sidecar `:50051`
- **ConfigMap**: `<node>-config` with key `node-config.yaml` mounted at `/etc/foundry`

---

## Execution Checklist

Each slice is atomic: CRD + Deployment + ConfigMap (if needed) for one node or
one logical group. Tests validate YAML structure via `go test`. Each slice
follows strict TDD: write failing test, implement, pass, lint, commit.

### Slice 14.1.1 — Update existing Sort CRD (add judiciary outputs)

Sort currently has outputs `[quench, appraise, refine]`. It needs two additional
outputs for judiciary routing:

- `"arbiter"` → target `"facilitator"` (Sort routes deadlocks to Facilitator,
  not directly to Arbiter — see ARCHITECTURE.md Main Cycle topology)
- `"pending-hold"` — Sort uses `Suspend()` for this path, no CRD output needed.
  The `"arbiter"` output is the only new CRD wiring.

Sort also needs `LIBRARIAN_ADDRESS` in its sidecar for the `GetActiveDisputes`
call (pending-hold path).

**flow.yaml changes (Sort FoundryNode):**

```yaml
spec:
  image: sort:latest
  exit: "standard-exit"
  outputs:
    - name: "quench"
      target: "quench"
    - name: "appraise"
      target: "appraise"
    - name: "refine"
      target: "refine"
    - name: "arbiter"
      target: "facilitator"
  capabilities:
    - "READ:artefact/haiku"
    - "READ:feedback"
    - "STAMP:artefact/haiku/approval"
    - "SUSPEND:workitem"
```

**deployments.yaml changes (Sort sidecar):** Add `LIBRARIAN_ADDRESS`.

**sort-config change:** Add `deadlockOutput: "arbiter"` (or confirm existing
`deadlockThreshold: 3` is sufficient — code uses the `outputArbiter` constant).
No config change needed; the output name is hardcoded as `"arbiter"`.

Steps:

- [ ] Write test: validate Sort FoundryNode has `arbiter` output targeting `facilitator`
- [ ] Write test: validate Sort sidecar has `LIBRARIAN_ADDRESS` env var
- [ ] Write test: validate Sort capabilities include `SUSPEND:workitem`
- [ ] Update Sort FoundryNode in `flow.yaml`
- [ ] Update Sort Deployment in `deployments.yaml` (add `LIBRARIAN_ADDRESS` to sidecar)
- [ ] Run tests, verify pass
- [ ] Run `make check-fix`, resolve issues
- [ ] Commit: `feat(manifests): add judiciary outputs and suspend capability to Sort`

### Slice 14.1.2 — Facilitator CRD + Deployment + ConfigMap

New FoundryNode, Deployment, and ConfigMap for the main-cycle Facilitator.

**FoundryNode (`facilitator`):**

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: facilitator
  namespace: default
  labels:
    flow.gideas.io/flow-name: haiku-flow
spec:
  image: facilitator:latest
  outputs:
    - name: "resolved"
      target: "sort"
  capabilities:
    - "READ:artefact/petition"
    - "READ:artefact/haiku"
    - "READ:feedback"
    - "READ:law"
    - "CREATE:workitem/child"
    - "SUSPEND:workitem"
    - "WRITE:artefact/dispute-workitem"
    - "WRITE:artefact/dispute-details"
    - "WRITE:artefact/dispute-artefact"
    - "WRITE:artefact/dispute-inputs"
    - "WRITE:artefact/appendix"
    - "WRITE:artefact/disputed-artefact"
```

**Deployment:** Two containers (node `:50053` + sidecar `:50051`). Sidecar needs
`LIBRARIAN_ADDRESS` (GetLaw, QueryLaws) and `FRICTION_LEDGER_ADDRESS`
(QueryFriction). ConfigMap mount.

**ConfigMap (`facilitator-config`):**

```yaml
arbiterNode: "arbiter"
inputArtefacts: ["petition"]
```

Steps:

- [ ] Write test: validate Facilitator FoundryNode exists with correct outputs and capabilities
- [ ] Write test: validate Facilitator Deployment has LIBRARIAN_ADDRESS and FRICTION_LEDGER_ADDRESS
- [ ] Write test: validate facilitator-config ConfigMap has arbiterNode field
- [ ] Add Facilitator FoundryNode to `flow.yaml`
- [ ] Add facilitator-config ConfigMap to `configmaps.yaml`
- [ ] Add Facilitator Deployment to `deployments.yaml`
- [ ] Run tests, verify pass
- [ ] Run `make check-fix`, resolve issues
- [ ] Commit: `feat(manifests): add Facilitator node CRD, deployment, and config`

### Slice 14.1.3 — Arbiter CRD + Deployment + ConfigMap

**FoundryNode (`arbiter`):**

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: arbiter
  namespace: default
  labels:
    flow.gideas.io/flow-name: haiku-flow
spec:
  image: arbiter:latest
  outputs:
    - name: "hung"
      target: "arbiter-hitl-resolve"
  capabilities:
    - "READ:artefact/evidence-bundle"
    - "WRITE:artefact/verdict-context"
    - "WRITE:artefact/question"
    - "WRITE:artefact/evidence"
    - "WRITE:artefact/allowed-outcomes"
    - "WRITE:artefact/prior-round-reasoning"
    - "CREATE:workitem/child"
    - "SUSPEND:workitem"
```

Note: The Arbiter has no `"resolved"` or `"consensus"` output — on resolved it
calls `Complete()`, on consensus it creates a Clerk child + Suspends then
Completes on resume. Only `"hung"` routes to an external node.

**Deployment:** Sidecar needs only `OPERATOR_ADDRESS` + `ARCHIVIST_ADDRESS`
(no Librarian, EventBus, or FrictionLedger). ConfigMap mount.

**ConfigMap (`arbiter-config`):**

```yaml
jurySize: 5
jurorNode: "juror"
consensusStrategy: "SIMPLE_MAJORITY"
maxRounds: 3
clerkNode: "clerk-forge"
hungOutput: "hung"
```

Steps:

- [ ] Write test: validate Arbiter FoundryNode exists with `hung` output targeting `arbiter-hitl-resolve`
- [ ] Write test: validate Arbiter capabilities include SUSPEND and child creation
- [ ] Write test: validate arbiter-config ConfigMap has all required fields
- [ ] Write test: validate Arbiter Deployment has ConfigMap mount
- [ ] Add Arbiter FoundryNode to `flow.yaml`
- [ ] Add arbiter-config ConfigMap to `configmaps.yaml`
- [ ] Add Arbiter Deployment to `deployments.yaml`
- [ ] Run tests, verify pass
- [ ] Run `make check-fix`, resolve issues
- [ ] Commit: `feat(manifests): add Arbiter node CRD, deployment, and config`

### Slice 14.1.4 — Juror CRD + Deployment + ConfigMap

**FoundryNode (`juror`):**

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: juror
  namespace: default
  labels:
    flow.gideas.io/flow-name: haiku-flow
spec:
  image: juror:latest
  outputs: []
  capabilities:
    - "READ:artefact/question"
    - "READ:artefact/evidence"
    - "READ:artefact/allowed-outcomes"
    - "READ:artefact/prior-round-reasoning"
    - "WRITE:artefact/verdict"
```

Juror is a child node (like Reviewer) — `outputs: []`, completes via child
workitem protocol.

**Deployment:** Node container needs `OLLAMA_BASE_URL` (LLM calls). Sidecar
needs only `OPERATOR_ADDRESS` + `ARCHIVIST_ADDRESS`. ConfigMap mount.

**ConfigMap (`juror-config`):**

```yaml
personality: "textualist"
```

Steps:

- [ ] Write test: validate Juror FoundryNode has empty outputs and correct capabilities
- [ ] Write test: validate Juror Deployment has OLLAMA_BASE_URL in node container
- [ ] Write test: validate juror-config ConfigMap has personality field
- [ ] Add Juror FoundryNode to `flow.yaml`
- [ ] Add juror-config ConfigMap to `configmaps.yaml`
- [ ] Add Juror Deployment to `deployments.yaml`
- [ ] Run tests, verify pass
- [ ] Run `make check-fix`, resolve issues
- [ ] Commit: `feat(manifests): add Juror node CRD, deployment, and config`

### Slice 14.1.5 — Tribunal CRD + Deployment + ConfigMap

**FoundryNode (`tribunal`):**

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: tribunal
  namespace: default
  labels:
    flow.gideas.io/flow-name: haiku-flow
spec:
  image: tribunal:latest
  outputs:
    - name: "hung"
      target: "tribunal-hitl-resolve"
  capabilities:
    - "READ:artefact/law-reference"
    - "WRITE:artefact/verdict-context"
    - "WRITE:artefact/question"
    - "WRITE:artefact/evidence"
    - "WRITE:artefact/allowed-outcomes"
    - "WRITE:artefact/prior-round-reasoning"
    - "READ:law"
    - "CREATE:workitem/child"
```

Note: Tribunal does NOT Suspend — it fire-and-forgets the Clerk child then
Completes. Only `"hung"` routes externally.

**Deployment:** Sidecar needs `LIBRARIAN_ADDRESS` (GetLaw, QueryLaws) and
`FRICTION_LEDGER_ADDRESS` (QueryFriction). ConfigMap mount.

**ConfigMap (`tribunal-config`):**

```yaml
jurySize: 5
jurorNode: "juror"
consensusStrategy: "SIMPLE_MAJORITY"
maxRounds: 3
clerkNode: "clerk-forge"
hungOutput: "hung"
```

Steps:

- [ ] Write test: validate Tribunal FoundryNode has `hung` output targeting `tribunal-hitl-resolve`
- [ ] Write test: validate Tribunal capabilities include READ:law and CREATE:workitem/child but NOT SUSPEND
- [ ] Write test: validate Tribunal Deployment sidecar has LIBRARIAN_ADDRESS and FRICTION_LEDGER_ADDRESS
- [ ] Write test: validate tribunal-config ConfigMap
- [ ] Add Tribunal FoundryNode to `flow.yaml`
- [ ] Add tribunal-config ConfigMap to `configmaps.yaml`
- [ ] Add Tribunal Deployment to `deployments.yaml`
- [ ] Run tests, verify pass
- [ ] Run `make check-fix`, resolve issues
- [ ] Commit: `feat(manifests): add Tribunal node CRD, deployment, and config`

### Slice 14.1.6 — Watcher Nodes (friction-watcher + ttl-watcher) CRDs + Deployments + ConfigMaps

Both are entry nodes that create Workitems and route to Tribunal.

**FoundryNode (`friction-watcher`):**

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: friction-watcher
  namespace: default
  labels:
    flow.gideas.io/flow-name: haiku-flow
spec:
  image: friction-watcher:latest
  entry: "hearing-entry"
  outputs:
    - name: "default"
      target: "tribunal"
  capabilities:
    - "WRITE:artefact/law-reference"
    - "CREATE:workitem"
```

**FoundryNode (`ttl-watcher`):**

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: ttl-watcher
  namespace: default
  labels:
    flow.gideas.io/flow-name: haiku-flow
spec:
  image: ttl-watcher:latest
  entry: "hearing-entry"
  outputs:
    - name: "default"
      target: "tribunal"
  capabilities:
    - "WRITE:artefact/law-reference"
    - "CREATE:workitem"
    - "READ:law"
```

Both use `entry: "hearing-entry"` which requires a new entry contract on the
FoundryFlow (see Slice 14.2.1).

**Deployment (friction-watcher):** Sidecar needs `EVENT_BUS_ADDRESS`
(subscription). No ConfigMap needed (implicit channel/event type).

**Deployment (ttl-watcher):** Sidecar needs `LIBRARIAN_ADDRESS` (QueryLaws for
TTL scanning). ConfigMap mount.

**ConfigMap (`ttl-watcher-config`):**

```yaml
scanPeriod: "5m"
tier1: "168h"
tier2: "720h"
```

Steps:

- [ ] Write test: validate friction-watcher FoundryNode has entry binding and default output to tribunal
- [ ] Write test: validate ttl-watcher FoundryNode has entry binding and default output to tribunal
- [ ] Write test: validate friction-watcher Deployment sidecar has EVENT_BUS_ADDRESS
- [ ] Write test: validate ttl-watcher Deployment sidecar has LIBRARIAN_ADDRESS
- [ ] Write test: validate ttl-watcher-config ConfigMap has scanPeriod and tier fields
- [ ] Add friction-watcher and ttl-watcher FoundryNodes to `flow.yaml`
- [ ] Add ttl-watcher-config ConfigMap to `configmaps.yaml`
- [ ] Add friction-watcher and ttl-watcher Deployments to `deployments.yaml`
- [ ] Run tests, verify pass
- [ ] Run `make check-fix`, resolve issues
- [ ] Commit: `feat(manifests): add friction-watcher and ttl-watcher entry nodes`

### Slice 14.1.7 — HITL Node CRDs (hitl-appraise, arbiter-hitl-resolve, tribunal-hitl-resolve) + Deployments

All three use `hitl:latest`. HITL behaviour is CRD-driven (outputs become
choices, capabilities signal features, exit contract enables cancel).

**FoundryNode (`hitl-appraise`):**

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: hitl-appraise
  namespace: default
  labels:
    flow.gideas.io/flow-name: haiku-flow
spec:
  image: hitl:latest
  exit: "clerk-exit"
  outputs:
    - name: "approved"
      target: "hitl-gate"
  capabilities:
    - "READ:artefact/petition"
    - "WRITE:feedback"
    - "STAMP:artefact/petition/approval"
```

The `exit` binding enables the cancel choice. `"approved"` output becomes the
human's approve action. `WRITE:feedback` enables the feedback action.

**FoundryNode (`arbiter-hitl-resolve`):**

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: arbiter-hitl-resolve
  namespace: default
  labels:
    flow.gideas.io/flow-name: haiku-flow
spec:
  image: hitl:latest
  exit: "clerk-exit"
  outputs:
    - name: "resolution"
      target: "arbiter"
  capabilities:
    - "READ:artefact/evidence-bundle"
    - "READ:artefact/deliberation-result"
```

**FoundryNode (`tribunal-hitl-resolve`):**

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: tribunal-hitl-resolve
  namespace: default
  labels:
    flow.gideas.io/flow-name: haiku-flow
spec:
  image: hitl:latest
  exit: "clerk-exit"
  outputs:
    - name: "resolution"
      target: "tribunal"
  capabilities:
    - "READ:artefact/evidence-bundle"
    - "READ:artefact/deliberation-result"
```

**Deployments:** All three use `hitl:latest`. Sidecar needs only
`OPERATOR_ADDRESS` + `ARCHIVIST_ADDRESS`. No ConfigMap needed (CRD-driven).
Optional `choiceLabels` ConfigMap for human-friendly display labels.

**ConfigMaps (optional, for display labels):**

```yaml
# hitl-appraise-config
choiceLabels:
  approved: "Approve Petition"

# arbiter-hitl-resolve-config
choiceLabels:
  resolution: "Provide Resolution"

# tribunal-hitl-resolve-config
choiceLabels:
  resolution: "Provide Resolution"
```

Steps:

- [ ] Write test: validate hitl-appraise FoundryNode has `approved` output, exit binding, WRITE:feedback
- [ ] Write test: validate arbiter-hitl-resolve has `resolution` output targeting `arbiter`
- [ ] Write test: validate tribunal-hitl-resolve has `resolution` output targeting `tribunal`
- [ ] Write test: validate all three HITL Deployments use hitl:latest image
- [ ] Add three HITL FoundryNodes to `flow.yaml`
- [ ] Add HITL ConfigMaps to `configmaps.yaml` (choice labels)
- [ ] Add three HITL Deployments to `deployments.yaml`
- [ ] Run tests, verify pass
- [ ] Run `make check-fix`, resolve issues
- [ ] Commit: `feat(manifests): add HITL node CRDs and deployments`

### Slice 14.1.8 — Clerk Cycle FoundryNode CRDs (clerk-forge, clerk-sort, clerk-appraise, clerk-refine, clerk-facilitator)

All five reuse existing images with different CRD configs.

**FoundryNode (`clerk-forge`):**

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: clerk-forge
  namespace: default
  labels:
    flow.gideas.io/flow-name: haiku-flow
spec:
  image: forge:latest
  outputs:
    - name: "default"
      target: "codification"
  capabilities:
    - "READ:artefact/verdict-context"
    - "WRITE:artefact/petition"
    - "READ:law"
```

**FoundryNode (`clerk-sort`):**

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: clerk-sort
  namespace: default
  labels:
    flow.gideas.io/flow-name: haiku-flow
spec:
  image: sort:latest
  outputs:
    - name: "appraise"
      target: "clerk-appraise"
    - name: "refine"
      target: "clerk-refine"
    - name: "arbiter"
      target: "clerk-facilitator"
    - name: "done"
      target: "clerk-done-router"
  capabilities:
    - "READ:artefact/petition"
    - "READ:feedback"
    - "STAMP:artefact/petition/approval"
    - "SUSPEND:workitem"
```

Note: clerk-sort has no `quench` output (petitions don't need linting). It uses
`"done"` instead of `exit` because the exit goes through routing. The `"arbiter"`
output routes to `clerk-facilitator` (same pattern as main-cycle Sort → Facilitator).

**FoundryNode (`clerk-appraise`):**

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: clerk-appraise
  namespace: default
  labels:
    flow.gideas.io/flow-name: haiku-flow
spec:
  image: appraise:latest
  outputs:
    - name: "default"
      target: "clerk-sort"
  capabilities:
    - "READ:artefact/verdict-context"
    - "READ:artefact/petition"
    - "READ:law"
    - "WRITE:feedback/new"
    - "STAMP:artefact/petition/review"
    - "CREATE:workitem/child"
```

**FoundryNode (`clerk-refine`):**

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: clerk-refine
  namespace: default
  labels:
    flow.gideas.io/flow-name: haiku-flow
spec:
  image: refine:latest
  outputs:
    - name: "default"
      target: "codification"
  capabilities:
    - "READ:artefact/verdict-context"
    - "READ:artefact/petition"
    - "WRITE:artefact/petition"
    - "READ:feedback"
    - "WRITE:feedback/actioned"
    - "READ:law"
```

Note: clerk-refine routes to `codification` (not clerk-sort) because revisions
need re-codification before triage.

**FoundryNode (`clerk-facilitator`):**

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: clerk-facilitator
  namespace: default
  labels:
    flow.gideas.io/flow-name: haiku-flow
spec:
  image: facilitator:latest
  outputs:
    - name: "resolved"
      target: "clerk-sort"
  capabilities:
    - "READ:artefact/verdict-context"
    - "READ:artefact/petition"
    - "READ:feedback"
    - "READ:law"
    - "CREATE:workitem/child"
    - "SUSPEND:workitem"
    - "WRITE:artefact/dispute-workitem"
    - "WRITE:artefact/dispute-details"
    - "WRITE:artefact/dispute-artefact"
    - "WRITE:artefact/dispute-inputs"
    - "WRITE:artefact/appendix"
    - "WRITE:artefact/disputed-artefact"
```

Steps:

- [ ] Write test: validate clerk-forge routes to codification and reads verdict-context
- [ ] Write test: validate clerk-sort has appraise/refine/arbiter/done outputs with correct targets
- [ ] Write test: validate clerk-appraise routes to clerk-sort and has CREATE:workitem/child
- [ ] Write test: validate clerk-refine routes to codification (not clerk-sort)
- [ ] Write test: validate clerk-facilitator routes resolved to clerk-sort
- [ ] Add all five Clerk cycle FoundryNodes to `flow.yaml`
- [ ] Run tests, verify pass
- [ ] Run `make check-fix`, resolve issues
- [ ] Commit: `feat(manifests): add Clerk cycle FoundryNode CRDs`

### Slice 14.1.9 — Clerk Cycle Deployments + ConfigMaps

**Deployment (`clerk-forge`):** Image `forge:latest`. Node needs
`OLLAMA_BASE_URL`. Sidecar needs `LIBRARIAN_ADDRESS`, `EVENT_BUS_ADDRESS`,
`FRICTION_LEDGER_ADDRESS` (same as main-cycle Forge). ConfigMap mount.

**ConfigMap (`clerk-forge-config`):**

```yaml
inputArtefacts: ["verdict-context"]
outputArtefact: "petition"
governedArtefact: "petition"
outputField: "petition"
systemPrompt: |
  You are a legal clerk drafting governance petitions. Given a court's verdict
  decision, produce a structured YAML petition with concrete law changes
  (create, retire, demote) that faithfully implement the court's reasoning.
  Each change must include a justification grounded in the verdict.
queryTemplate: |
  The court has issued the following verdict decision:

  {{.Input}}

  Draft a governance petition that implements this decision. The petition must
  include concrete law changes with justifications. Output valid YAML matching
  the petition schema.
```

**Deployment (`clerk-sort`):** Image `sort:latest`. Same as main Sort but with
`clerk-sort-config`. Sidecar needs `LIBRARIAN_ADDRESS` (GetActiveDisputes).

**ConfigMap (`clerk-sort-config`):**

```yaml
nodeOrder: "appraise"
deadlockThreshold: 3
```

Note: No `quench` in nodeOrder — petitions have no linter stamp.

**Deployment (`clerk-appraise`):** Image `appraise:latest`. Node needs
`OLLAMA_BASE_URL`. Sidecar needs `LIBRARIAN_ADDRESS`, `EVENT_BUS_ADDRESS`,
`FRICTION_LEDGER_ADDRESS`. ConfigMap mount.

**ConfigMap (`clerk-appraise-config`):**

```yaml
inputArtefacts: ["verdict-context"]
reviewArtefact: "petition"
governedArtefact: "petition"
stampName: "review"
reviewerNode: "reviewer"
divisionPrompts:
  alignment: "Verify the petition faithfully implements the court's verdict decision. Check that each change is justified by the verdict reasoning."
  completeness: "Check that all aspects of the verdict are addressed. Verify no law changes are missing or extraneous."
  feasibility: "Assess whether the proposed law changes are well-formed and internally consistent."
```

**Deployment (`clerk-refine`):** Image `refine:latest`. Node needs
`OLLAMA_BASE_URL`. Sidecar needs `LIBRARIAN_ADDRESS`, `EVENT_BUS_ADDRESS`,
`FRICTION_LEDGER_ADDRESS`. ConfigMap mount.

**ConfigMap (`clerk-refine-config`):**

```yaml
inputArtefacts: ["verdict-context"]
outputArtefact: "petition"
governedArtefact: "petition"
outputField: "petition"
systemPrompt: |
  You are a legal clerk revising a governance petition based on review feedback.
  The petition must faithfully implement the court's verdict decision. Address
  each piece of feedback while maintaining alignment with the verdict.
queryTemplate: |
  Original verdict decision:
  {{.VerdictContext}}

  Current petition:
  {{.Input}}

  Review feedback to address:
  {{.Feedback}}

  Revise the petition to address the feedback while maintaining alignment with
  the verdict. Output valid YAML matching the petition schema.
```

**Deployment (`clerk-facilitator`):** Image `facilitator:latest`. Same sidecar
as main Facilitator (LIBRARIAN_ADDRESS + FRICTION_LEDGER_ADDRESS). ConfigMap mount.

**ConfigMap (`clerk-facilitator-config`):**

```yaml
arbiterNode: "arbiter"
inputArtefacts: ["verdict-context"]
```

Steps:

- [ ] Write test: validate clerk-forge Deployment uses forge:latest and has OLLAMA_BASE_URL
- [ ] Write test: validate clerk-forge-config has systemPrompt and queryTemplate overrides
- [ ] Write test: validate clerk-sort Deployment has LIBRARIAN_ADDRESS
- [ ] Write test: validate clerk-sort-config nodeOrder has no quench
- [ ] Write test: validate clerk-appraise Deployment has all required sidecar env vars
- [ ] Write test: validate clerk-refine routes to codification via Deployment config
- [ ] Write test: validate clerk-facilitator has LIBRARIAN_ADDRESS and FRICTION_LEDGER_ADDRESS
- [ ] Add all five Clerk Deployment manifests to `deployments.yaml`
- [ ] Add all five Clerk ConfigMaps to `configmaps.yaml`
- [ ] Run tests, verify pass
- [ ] Run `make check-fix`, resolve issues
- [ ] Commit: `feat(manifests): add Clerk cycle deployments and ConfigMaps`

### Slice 14.1.10 — Codification + codify-smt CRDs + Deployments + ConfigMaps

**FoundryNode (`codification`):**

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: codification
  namespace: default
  labels:
    flow.gideas.io/flow-name: haiku-flow
spec:
  image: codification:latest
  outputs:
    - name: "default"
      target: "clerk-sort"
  capabilities:
    - "READ:artefact/petition"
    - "WRITE:artefact/petition"
    - "WRITE:artefact/codification-goal"
    - "CREATE:workitem/child"
```

**FoundryNode (`codify-smt`):**

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: codify-smt
  namespace: default
  labels:
    flow.gideas.io/flow-name: haiku-flow
spec:
  image: codify-smt:latest
  outputs: []
  capabilities:
    - "READ:artefact/codification-goal"
    - "WRITE:artefact/codification-result"
```

codify-smt is a child node (like Reviewer/Juror) — `outputs: []`.

**Deployment (`codification`):** Sidecar needs only `OPERATOR_ADDRESS` +
`ARCHIVIST_ADDRESS`. ConfigMap mount.

**ConfigMap (`codification-config`):**

```yaml
petitionArtefact: "petition"
codificationNodes: ["codify-smt"]
defaultOutput: "default"
```

**Deployment (`codify-smt`):** Node needs `OLLAMA_BASE_URL` (LLM). Sidecar
needs only `OPERATOR_ADDRESS` + `ARCHIVIST_ADDRESS`. ConfigMap mount.

**ConfigMap (`codify-smt-config`):**

```yaml
outputFormat: "application/smt-lib"
```

Steps:

- [ ] Write test: validate codification FoundryNode routes to clerk-sort and has CREATE:workitem/child
- [ ] Write test: validate codify-smt is a child node (empty outputs)
- [ ] Write test: validate codification Deployment has ConfigMap mount
- [ ] Write test: validate codify-smt Deployment has OLLAMA_BASE_URL
- [ ] Write test: validate codification-config has codificationNodes field
- [ ] Add codification and codify-smt FoundryNodes to `flow.yaml`
- [ ] Add codification-config and codify-smt-config to `configmaps.yaml`
- [ ] Add codification and codify-smt Deployments to `deployments.yaml`
- [ ] Run tests, verify pass
- [ ] Run `make check-fix`, resolve issues
- [ ] Commit: `feat(manifests): add Codification and codify-smt nodes`

### Slice 14.1.11 — Rule Router CRDs (clerk-done-router + hitl-gate) + Deployments + ConfigMaps

Both use `rule-router:latest` with different CEL rule configs.

**FoundryNode (`clerk-done-router`):**

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: clerk-done-router
  namespace: default
  labels:
    flow.gideas.io/flow-name: haiku-flow
spec:
  image: rule-router:latest
  outputs:
    - name: "law-applicator"
      target: "law-applicator"
    - name: "hitl-appraise"
      target: "hitl-appraise"
  capabilities:
    - "READ:artefact/petition"
```

**FoundryNode (`hitl-gate`):**

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: hitl-gate
  namespace: default
  labels:
    flow.gideas.io/flow-name: haiku-flow
spec:
  image: rule-router:latest
  outputs:
    - name: "law-applicator"
      target: "law-applicator"
    - name: "law-applicator-embassy"
      target: "law-applicator"
  capabilities:
    - "READ:artefact/petition"
```

Note: Both hitl-gate outputs target `law-applicator`. The distinction is
cosmetic for auditability — the T3 path goes directly to law-applicator (which
Completes after applying), while the T4-5 path also goes to law-applicator
(which creates a dispute record and routes to Embassy). law-applicator handles
tier logic internally; the router just selects the conceptual path. In practice,
both outputs target the same node — the CRD output names exist for audit
trail clarity.

Actually, per the ARCHITECTURE.md Clerk Cycle diagram:
- T3 → law-applicator → Complete()
- T4-5 → law-applicator → embassy → Complete()

law-applicator routes to Embassy for T4-5 internally. So hitl-gate only needs
one output targeting law-applicator. Revising:

**FoundryNode (`hitl-gate`) (revised):**

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: hitl-gate
  namespace: default
  labels:
    flow.gideas.io/flow-name: haiku-flow
spec:
  image: rule-router:latest
  outputs:
    - name: "law-applicator"
      target: "law-applicator"
  capabilities:
    - "READ:artefact/petition"
```

Both Rule Router CEL rules evaluate to the same target node. The hitl-gate
exists as a routing checkpoint for audit clarity, even if both branches
converge.

**Deployments:** Both use `rule-router:latest`. Sidecar needs only
`OPERATOR_ADDRESS` + `ARCHIVIST_ADDRESS`. ConfigMap mount.

**ConfigMap (`clerk-done-router-config`):**

```yaml
rules:
  - name: "tier-1-2"
    when: 'metadata["petition_max_tier"] in ["FINDING", "RULING"]'
    output: "law-applicator"
  - name: "tier-3-5"
    when: 'true'
    output: "hitl-appraise"
default: "hitl-appraise"
```

**ConfigMap (`hitl-gate-config`):**

```yaml
rules:
  - name: "all-approved"
    when: 'true'
    output: "law-applicator"
default: "law-applicator"
```

Steps:

- [ ] Write test: validate clerk-done-router has two outputs (law-applicator, hitl-appraise)
- [ ] Write test: validate hitl-gate has law-applicator output
- [ ] Write test: validate clerk-done-router-config CEL rules reference correct tier metadata
- [ ] Write test: validate hitl-gate-config has rules
- [ ] Write test: validate both Deployments use rule-router:latest
- [ ] Add clerk-done-router and hitl-gate FoundryNodes to `flow.yaml`
- [ ] Add clerk-done-router-config and hitl-gate-config to `configmaps.yaml`
- [ ] Add clerk-done-router and hitl-gate Deployments to `deployments.yaml`
- [ ] Run tests, verify pass
- [ ] Run `make check-fix`, resolve issues
- [ ] Commit: `feat(manifests): add Rule Router CRDs for Clerk exit routing`

### Slice 14.1.12 — Law-applicator CRD + Deployment

**FoundryNode (`law-applicator`):**

```yaml
apiVersion: flow.gideas.io/v1
kind: FoundryNode
metadata:
  name: law-applicator
  namespace: default
  labels:
    flow.gideas.io/flow-name: haiku-flow
spec:
  image: law-applicator:latest
  outputs:
    - name: "embassy"
      target: "embassy"
  capabilities:
    - "READ:artefact/petition"
    - "WRITE:artefact/approval-stamp"
    - "READ:law"
    - "WRITE:law"
```

Note: law-applicator routes to Embassy only for T4-5 petitions. For T1-3 it
Completes. The `"embassy"` output exists for the T4-5 path. The `WRITE:law`
capability covers WriteLaw/RetireLaw/CreateDisputeRecord via Librarian.

**Deployment:** Sidecar needs `LIBRARIAN_ADDRESS` (WriteLaw, RetireLaw, GetLaw,
CreateDisputeRecord). No ConfigMap needed.

Steps:

- [ ] Write test: validate law-applicator FoundryNode has embassy output and WRITE:law capability
- [ ] Write test: validate law-applicator Deployment sidecar has LIBRARIAN_ADDRESS
- [ ] Write test: validate law-applicator Deployment has no ConfigMap mount
- [ ] Add law-applicator FoundryNode to `flow.yaml`
- [ ] Add law-applicator Deployment to `deployments.yaml`
- [ ] Run tests, verify pass
- [ ] Run `make check-fix`, resolve issues
- [ ] Commit: `feat(manifests): add law-applicator node CRD and deployment`

### Slice 14.2.1 — FoundryFlow update: judiciary NodeGroup + entry/exit contracts

The FoundryFlow CRD needs:

1. A `hearing-entry` entry contract for watcher nodes (law-reference artefact).
2. A `clerk-exit` exit contract for the Clerk cycle (petition with approval stamp).
3. A `judiciary` nodeGroup listing all judiciary nodes.

**FoundryFlow additions:**

```yaml
spec:
  entryContracts:
    standard-entry:
      "petition": []
    hearing-entry:
      "law-reference": []
  exitContracts:
    standard-exit:
      "haiku":
        - linter
        - review
        - approval
    clerk-exit:
      "petition":
        - review
        - approval
  nodeGroups:
    main-cycle:
      - forge
      - sort
      - quench
      - appraise
      - reviewer
      - refine
    judiciary:
      - facilitator
      - arbiter
      - juror
      - tribunal
      - friction-watcher
      - ttl-watcher
      - hitl-appraise
      - arbiter-hitl-resolve
      - tribunal-hitl-resolve
      - law-applicator
    clerk-cycle:
      - clerk-forge
      - clerk-sort
      - clerk-appraise
      - clerk-refine
      - clerk-facilitator
      - codification
      - codify-smt
      - clerk-done-router
      - hitl-gate
```

Steps:

- [ ] Write test: validate FoundryFlow has hearing-entry contract with law-reference artefact
- [ ] Write test: validate FoundryFlow has clerk-exit contract with petition stamps
- [ ] Write test: validate FoundryFlow has judiciary and clerk-cycle nodeGroups
- [ ] Write test: validate all 24 hand-authored nodes appear in exactly one nodeGroup
- [ ] Update FoundryFlow in `flow.yaml` with new contracts and nodeGroups
- [ ] Run tests, verify pass
- [ ] Run `make check-fix`, resolve issues
- [ ] Commit: `feat(manifests): add judiciary entry/exit contracts and nodeGroups`

### Slice 14.3.1 — Cross-cutting manifest validation test

A comprehensive test that validates the complete manifest set for internal
consistency:

- Every FoundryNode referenced as an output target exists as a FoundryNode CRD
- Every FoundryNode in a nodeGroup exists as a FoundryNode CRD
- Every FoundryNode with a ConfigMap mount has a corresponding ConfigMap
- Every Deployment `FLOW_NODE_ID` matches a FoundryNode metadata.name
- No duplicate FoundryNode names
- Embassy is NOT in the hand-authored manifests (operator-provisioned)
- Node count: exactly 24 hand-authored FoundryNodes (25 minus Embassy)

Steps:

- [ ] Write comprehensive manifest consistency test
- [ ] Run tests, verify pass
- [ ] Run `make check-fix`, resolve issues
- [ ] Commit: `test(manifests): add cross-cutting manifest consistency validation`

### Slice 14.4.1 — Update PLAN.md

- [ ] Update PLAN.md to mark Phase 14 complete
- [ ] Commit: `docs: mark Phase 14 complete`

---

## Execution Order

Slices are designed for sequential execution. Dependencies:

1. **14.2.1** (FoundryFlow contracts) — creates the `hearing-entry` and
   `clerk-exit` contracts that watcher and HITL nodes reference. Run first.
2. **14.1.1** (Sort update) — update existing node, no new dependencies.
3. **14.1.2** (Facilitator) — depends on Sort having `arbiter` output.
4. **14.1.3** (Arbiter) — depends on Facilitator existing as target.
5. **14.1.4** (Juror) — child node, no output dependencies.
6. **14.1.5** (Tribunal) — depends on 14.1.7 for hitl-resolve target.
7. **14.1.6** (Watchers) — depends on Tribunal existing, plus 14.2.1 contracts.
8. **14.1.7** (HITL nodes) — depends on 14.2.1 for clerk-exit contract.
9. **14.1.8** (Clerk CRDs) — depends on codification, clerk-done-router existing.
10. **14.1.9** (Clerk Deployments/ConfigMaps) — depends on 14.1.8.
11. **14.1.10** (Codification) — depends on clerk-sort existing as target.
12. **14.1.11** (Rule Routers) — depends on law-applicator, hitl-appraise existing.
13. **14.1.12** (Law-applicator) — depends on Embassy existing (operator-provisioned).
14. **14.3.1** (Cross-cutting test) — must be last, validates everything.
15. **14.4.1** (PLAN.md update) — after all tests pass.

**Recommended execution order** (resolves circular output-target references by
adding all FoundryNodes before all Deployments, since K8s applies CRDs
declaratively):

1. 14.2.1 — FoundryFlow contracts and nodeGroups
2. 14.1.1 — Sort update
3. 14.1.2 — Facilitator
4. 14.1.3 — Arbiter
5. 14.1.4 — Juror
6. 14.1.5 — Tribunal
7. 14.1.6 — Watchers
8. 14.1.7 — HITL nodes
9. 14.1.8 — Clerk cycle CRDs
10. 14.1.9 — Clerk cycle Deployments + ConfigMaps
11. 14.1.10 — Codification + codify-smt
12. 14.1.11 — Rule Routers
13. 14.1.12 — Law-applicator
14. 14.3.1 — Cross-cutting validation
15. 14.4.1 — PLAN.md update

## Test Strategy

Tests live in `nodes/haiku-manifests/manifests_test.go` (or a new test file in
the same package). Tests parse the YAML manifests and validate:

1. **Structural correctness** — each FoundryNode has required fields (image,
   outputs, capabilities, labels).
2. **Topology correctness** — output targets reference existing nodes.
3. **Deployment correctness** — sidecar env vars match node capabilities
   (e.g., LIBRARIAN_ADDRESS present iff node needs law access).
4. **ConfigMap correctness** — ConfigMap exists for nodes that mount one;
   config contains expected fields.
5. **Completeness** — all 24 hand-authored nodes have CRDs + Deployments.

Tests use Go's `testing` package + `gopkg.in/yaml.v3` (or `sigs.k8s.io/yaml`)
to parse multi-document YAML. No Kubernetes client required — pure structural
validation.
