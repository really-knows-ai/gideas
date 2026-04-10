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

## Remaining

#### 14.1 Judiciary FoundryNode CRDs

Write FoundryNode CRD definitions for all judiciary nodes now that images and
contracts are finalised.

**Node Inventory (25 runtime node instances, 17 distinct images; Embassy is operator-provisioned)**:

| CRD Name | Image | Type | Purpose |
|---|---|---|---|
| **Main Cycle** | | | |
| `forge` | forge:latest | Computation | Content generation (existing) |
| `sort` | sort:latest | Triage | Feedback triage + routing (existing) |
| `appraise` | appraise:latest | Review | Multi-agent review (existing) |
| `refine` | refine:latest | Revision | Content revision (existing) |
| `quench` | quench:latest | Finalization | Content finalization (existing) |
| `facilitator` | facilitator:latest | Lifecycle | Deadlock resolution lifecycle owner |
| **Deliberation** | | | |
| `arbiter` | arbiter:latest | Orchestrator | Deadlock resolution, fans out to jurors |
| `juror` | juror:latest | Computation | Deliberation primitive, votes and completes |
| **Tribunal Path** | | | |
| `tribunal` | tribunal:latest | Orchestrator | Hearing conductor, fans out to jurors |
| `friction-watcher` | friction-watcher:latest | Entry | Friction threshold --> hearing workitems |
| `ttl-watcher` | ttl-watcher:latest | Entry | Law TTL expiry --> hearing workitems |
| **Clerk Cycle** | | | |
| `clerk-forge` | forge:latest | Computation | Petition drafting (prompt-configurable) |
| `codification` | codification:latest | Fan-out | Formal representation fan-out orchestrator |
| `clerk-sort` | sort:latest | Triage | Petition feedback triage (same image as Sort) |
| `clerk-appraise` | appraise:latest | Review | Automated petition review (same image as Appraise) |
| `clerk-refine` | refine:latest | Revision | Petition revision (prompt-configurable) |
| `clerk-facilitator` | facilitator:latest | Lifecycle | Petition deadlock lifecycle (same image as Facilitator) |
| **Clerk Exit Routing** | | | |
| `clerk-done-router` | rule-router:latest | Rule Router | Post-approval tier routing: T1-2 vs T3-5 |
| `hitl-gate` | rule-router:latest | Rule Router | Post-HITL routing: T3 approved vs T4-5 approved |
| **HITL** | | | |
| `hitl-appraise` | hitl:latest | HITL | T3-5 petition HITL review. Exit node. |
| `arbiter-hitl-resolve` | hitl:latest | HITL | Arbiter hung jury HITL resolution. Exit node. |
| `tribunal-hitl-resolve` | hitl:latest | HITL | Tribunal hung jury HITL resolution. Exit node. |
| **Boundary / Terminal** | | | |
| `law-applicator` | law-applicator:latest | Action | Applies petitions via Librarian (T1-3: WriteLaw/RetireLaw; T4-5: CreateDisputeRecord then route to Embassy) |
| `embassy` | embassy:latest | Boundary | Cross-flow import/export boundary and naturalisation |
| **Codification** | | | |
| `codify-smt` | codify-smt:latest | Computation | Formal law representations (SMT-LIB) |

#### 14.2 Judiciary NodeGroup

Add a `judiciary` NodeGroup to the FoundryFlow CRD with the judiciary nodes
and appropriate entry/exit contracts.

#### 14.3 Judiciary Deployments

Add Deployment manifests for all judiciary nodes to
`nodes/haiku-manifests/deployments.yaml`. Each follows the existing pattern:
node container (`:50053`) + sidecar container (`:50051`) with service
connection env vars and ConfigMap volume mounts. Embassy is operator-provisioned
and is not added manually to this manifest set. The Federation service is
platform-level and is outside the node deployment manifests.

#### 14.4 Judiciary ConfigMaps

Add ConfigMap manifests for judiciary nodes to
`nodes/haiku-manifests/configmaps.yaml`. Key configurations:

| Node | Config Fields |
|---|---|
| `friction-watcher` | (Event Bus subscription is implicit, minimal config) |
| `ttl-watcher` | `scanPeriod`, per-tier TTL durations (`tier1`, `tier2`) — only Tier 1-2 laws have TTLs |
| `arbiter` | `jurySize`, `jurorNode`, `consensusStrategy`, `maxRounds` |
| `tribunal` | `jurySize`, `jurorNode`, `consensusStrategy`, `maxRounds` |
| `facilitator` | `arbiterNode` (target for child workitem) |
| `clerk-forge` | `inputArtefacts`, `outputArtefact`, `governedArtefact`, `outputField`, `systemPrompt`, `queryTemplate` (petition-drafting prompt overrides — replace baked-in haiku defaults) |
| `clerk-refine` | `inputArtefacts`, `outputArtefact`, `governedArtefact`, `outputField`, `triageSystemPrompt`, `triageQueryTemplate`, `revisionSystemPrompt`, `revisionQueryTemplate` (petition-revision prompt overrides) |
| `codification` | `petitionArtefact`, `codificationNodes`, `defaultOutput` |
| `clerk-done-router` | CEL rules (tier-based routing: T1-2 vs T3-5) |
| `hitl-gate` | CEL rules (T3 approved → law-applicator / T4-5 → law-applicator → embassy) |
| `codify-smt` | `outputFormat` |
| `juror` | (personality loaded from config) |
| `hitl-appraise` | (CRD-driven: outputs, capabilities, exit-node config) |
| `arbiter-hitl-resolve` | (CRD-driven: outputs, capabilities, exit-node config) |
| `tribunal-hitl-resolve` | (CRD-driven: outputs, capabilities, exit-node config) |
| `law-applicator` | (minimal -- reads petition artefact, tier-aware: T1-3 applies laws, T4-5 creates dispute record) |

Embassy config is not hand-authored in this ConfigMap file. The Operator derives
Embassy behaviour from `FoundryFlow.spec.crossFlow`, projected Federation /
Treaty trust material, and any Embassy-specific runtime settings introduced in
Phase 13.

#### 14.5 Update PLAN.md

Mark Phase 14 complete and update status.
