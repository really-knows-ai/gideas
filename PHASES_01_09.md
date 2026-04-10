# Phases 01-09 History

This file preserves completed and superseded implementation planning from
Phases 1-9. It is historical context only. For the current target architecture,
see `ARCHITECTURE.md`. For active work, see `PLAN.md` and `PHASE_10.md`
onward.

## Implementation Phases

> **Regression gate:** Run `make test-all && make check-fix-all` at the end of
> any phase where the codebase should be in a compilable, passing state.
> Phases that make known breaking changes (e.g. deleting services before their
> replacements exist) skip the gate — the next stabilising phase picks it up.

### Phase 1: Specification Updates ✅

Update all spec documents to reflect the new architecture. Specs are the source
of truth -- everything else flows from them.

**Completed.** All 21 spec files updated across 4 sub-phases (1.1–1.4). Commit
`135d470`.

#### 1.1 Core Concept Specs ✅

| File | Changes |
|---|---|
| `specs/01-concepts/00-overview.md` | Replace "two core services (Jury, Clerk)" with new node inventory. Update tier table and sequence diagram. |
| `specs/01-concepts/01-architecture.md` | Rewrite Judiciary paragraph. Replace "three nodes...two core services" with new architecture. |
| `specs/01-concepts/02-foundry-cycle.md` | Rewrite Arbiter, Tribunal, and Advocate sections. Replace Jury/Clerk service calls with Juror fan-out. Add Deliberation Gate, Clerk node, Judiciary Gate to cycle topology. New topology diagrams. |
| `specs/01-concepts/03-data-model.md` | Update friction formula for Juror node model. Add petition as a GovernedArtefact type. Update law lifecycle descriptions. |
| `specs/01-concepts/04-governance.md` | Rewrite conflict resolution flow. Replace Jury invocations with fan-out. Update authority ceiling descriptions. Define petition structure (YAML/Markdown schema). |

#### 1.2 Platform Specs ✅

| File | Changes |
|---|---|
| `specs/02-flow/00-overview.md` | Replace Jury/Clerk service references with new node descriptions. |
| `specs/02-flow/03-nodes-external.md` | Major rewrite of Judiciary subsystem section. Add new node definitions: Juror, Deliberation Gate, Clerk (as node), Tribunal Router, Judiciary Gate. Update capabilities for Arbiter and Tribunal. Remove Jury/Clerk as core services. |
| `specs/02-flow/04-system-services.md` | Delete Jury section. Delete Clerk section. Rewrite hearing lifecycle with new topology and sequence diagrams. Update codification section for Clerk node fan-out. |
| `specs/02-flow/05-configuration.md` | Update codification discovery. Update Tribunal hearing bindings. |
| `specs/02-flow/06-cross-flow.md` | Update Clerk references in cross-flow resolution. |

#### 1.3 Node and SDK Specs ✅

| File | Changes |
|---|---|
| `specs/03-node/00-overview.md` | Update Clerk reference. |
| `specs/03-node/01-sidecar.md` | Remove Jury/Clerk proxy references. |
| `specs/03-node/03-patterns.md` | Replace Jury deliberation reference with Juror fan-out pattern description. |
| `specs/04-sdk/00-overview.md` | Update codification note -- fan-out is now the actual model, not "future". |
| `specs/04-sdk/03-sdk-legal.md` | Update Clerk references for law drafting and codification. |
| `specs/04-sdk/04-sdk-feedback.md` | Rewrite deadlock resolution flow for Juror fan-out. |
| `specs/04-sdk/07-sdk-agent.md` | Rewrite "Relationship to the Jury Service" as "Relationship to Juror Nodes". |
| `specs/04-sdk/08-sdk-hitl.md` | Update Advocate entry path description. |

#### 1.4 Reference Specs ✅

| File | Changes |
|---|---|
| `specs/05-reference/crds.md` | Rewrite Judiciary node table. Add new node CRD entries. Remove Jury/Clerk service entries. Add petition GovernedArtefact definition. |
| `specs/05-reference/grpc-api.md` | Delete Jury API and Clerk API sections. Remove port table entries (50059, 50060). Add judiciary.proto types reference. |
| `specs/05-reference/error-catalogue.md` | Replace `JURY_HUNG`, `JURY_INFERENCE_FAILED` with new node-level error codes. Update `LAW_WRITE_FAILED` reference. |
| `specs/05-reference/glossary.md` | Rewrite entries: Arbiter, Advocate, Clerk, Tribunal, Judiciary, Ruling, FoundryAgent. Remove Jury entry. Add entries: Juror, Deliberation Gate, Judiciary Gate, Petition, Tribunal Router. |

---

### Phase 2: Proto and Generated Code ✅

**Completed.** New `judiciary.proto` created, old `jury.proto` and `clerk.proto`
deleted, generated code regenerated and orphaned files removed.

#### 2.1 Create `judiciary.proto` ✅

New file: `proto/flow/v1/judiciary.proto`

Relocate from `jury.proto`:

- `ConsensusStrategy` enum (SIMPLE_MAJORITY, SUPER_MAJORITY, UNANIMITY)
- `JurorJustification` message (juror_id, outcome, reasoning)

Potentially add:

- A shared `Verdict` message if a common structure emerges

#### 2.2 Delete Old Protos ✅

- Deleted `proto/flow/v1/jury.proto`
- Deleted `proto/flow/v1/clerk.proto`

#### 2.3 Update Remaining Protos ✅

- `proto/flow/v1/librarian.proto` -- updated `WriteLaw` comment (Clerk service
  reference replaced with Judiciary Gate)
- `proto/flow/v1/operator.proto` -- reviewed; hearing workitem comments still
  accurate for new architecture, no changes needed

#### 2.4 Regenerate ✅

- Ran `buf generate` to regenerate `gen/flow/v1/`
- Deleted orphaned generated files: `jury.pb.go`, `jury_grpc.pb.go`,
  `clerk.pb.go`, `clerk_grpc.pb.go`
- Verified new `judiciary.pb.go` generated with `ConsensusStrategy` enum and
  `JurorJustification` message (no `_grpc.pb.go` since no service definition)

**Expected downstream breakage (fixed in later phases):**

- SDK (`sdk/go/client.go`) -- references `JuryServiceClient`, `ClerkServiceClient`,
  `Deliberate()`, `DraftLaw()` -> Phase 3
- Sidecar proxies (`platform/sidecar/internal/proxy/jury.go`, `clerk.go`) ->
  Phase 3 (delete)
- Clerk service (`platform/clerk/`) -> Phase 4 (delete)
- `nodeconfig/load.go` -- uses `ConsensusStrategy` which moved from `jury.pb.go`
  to `judiciary.pb.go` within the same `flowv1` package; no code changes needed,
  will compile once SDK transitive blocker is removed

---

### Phase 3: SDK and Sidecar Cleanup ✅

**Completed.** Removed Jury/Clerk fields, convenience methods, proxy files, and
service registrations from the SDK and Sidecar. NodeConfig required no changes
(ConsensusStrategy moved within the same `flowv1` Go package).

#### 3.1 SDK Changes (`sdk/go/`) ✅

| File | Changes |
|---|---|
| `client.go` | Removed `Jury` and `Clerk` fields from Client struct. Removed from constructor. Deleted `Deliberate()` and `DraftLaw()` methods. |
| `client_test.go` | Removed Jury/Clerk server embeds, service registrations, handler methods, and test cases (`TestDeliberate_*`, `TestDraftLaw_*`). |
| `testutil_test.go` | Removed Jury/Clerk client fields from test setup. |
| `child_test.go` | Removed Jury/Clerk server registrations. |
| `fanout_test.go` | Removed Jury/Clerk server embeds and registrations. |

#### 3.2 Sidecar Changes (`platform/sidecar/`) ✅

| File | Changes |
|---|---|
| `cmd/main.go` | Removed `JURY_ADDRESS`/`CLERK_ADDRESS` env vars, proxy creation, server registration, closers. Updated doc comment. |
| `internal/proxy/jury.go` | **Deleted.** |
| `internal/proxy/jury_test.go` | **Deleted.** |
| `internal/proxy/clerk.go` | **Deleted.** |
| `internal/proxy/clerk_test.go` | **Deleted.** |

#### 3.3 Shared Node Config (`nodes/internal/nodeconfig/`) ✅

No code changes needed. `ParseConsensusStrategy` uses `flowv1.ConsensusStrategy`
which moved from `jury.pb.go` to `judiciary.pb.go` within the same `flowv1` Go
package -- import path and type names are identical.

**Expected downstream breakage (fixed in later phases):**

- `nodes/arbiter/` -- references `client.Deliberate()`, `flowv1.DeliberateResponse` -> Phase 6
- `nodes/tribunal/` -- references `client.Deliberate()`, `flowv1.DeliberateResponse` -> Phase 6
- `nodes/advocate/` -- references `client.DraftLaw()`, `flowv1.DeliberateResponse` -> Phase 6
- `platform/clerk/` -- references `flowv1.ClerkServiceServer`, `flowv1.DraftLawRequest` -> Phase 4 (delete)

---

### Phase 4: Delete Jury Service and Clerk Service ✅

**Completed.** Deleted `jury/` (15 files) and `platform/clerk/` (7 files).
Removed clerk from `go.work`, `platform/go.work`, and all Makefile targets
(test, build, lint, tidy). Updated `AGENTS.md` repository structure.

#### 4.1 Delete Jury Service ✅

Deleted entire `jury/` directory (15 files):

- `jury/cmd/main.go`
- `jury/internal/service/jury_server.go`
- `jury/internal/service/jury_server_test.go`
- `jury/internal/deliberation/engine.go`
- `jury/internal/deliberation/engine_test.go`
- `jury/internal/jurors/juror.go`
- `jury/internal/jurors/textualist.go`
- `jury/internal/jurors/devils_advocate.go`
- `jury/internal/jurors/reformer.go`
- `jury/internal/jurors/conservator.go`
- `jury/internal/jurors/pragmatist.go`
- `jury/go.mod`, `jury/go.sum`
- `jury/Dockerfile`
- `jury/deployment.yaml`

#### 4.2 Delete Clerk Platform Service ✅

Deleted entire `platform/clerk/` directory (7 files):

- `platform/clerk/cmd/main.go`
- `platform/clerk/internal/service/clerk_server.go`
- `platform/clerk/internal/service/clerk_server_test.go`
- `platform/clerk/go.mod`, `platform/clerk/go.sum`
- `platform/clerk/Dockerfile`
- `platform/clerk/deployment.yaml`

#### 4.3 Build System Cleanup ✅

| File | Changes |
|---|---|
| `Makefile` | Removed `test-clerk`, `build-clerk` targets. Removed `./platform/clerk/...` from lint invocations. Removed `platform/clerk` from tidy target. |
| `go.work` | Removed `./platform/clerk`. |
| `platform/go.work` | Removed `./clerk`. |
| `AGENTS.md` | Removed `clerk/` from repository structure tree. |

---

### Phase 5: New Node Implementations

#### 5.1 Juror Node (`nodes/juror/`) ✅

- Single image, loads agent configurations for personality diversity
- Receives child Workitem with: question, evidence, prior-round reasoning (if
  retry), allowed outcomes
- Runs a FoundryAgent with the loaded judicial personality
- Produces a structured verdict artefact (outcome + reasoning)
- Calls `Complete()`
- Port relevant agent logic from `jury/internal/jurors/` (textualist, reformer,
  conservator, pragmatist, devil's advocate)

Files to create:

- `nodes/juror/main.go`
- `nodes/juror/main_test.go`
- `nodes/juror/testutil_test.go`

#### 5.2 Deliberation Gate (`nodes/deliberation-gate/`) ✅ ⛔ SUPERSEDED

> **Superseded by architecture revision.** Tally logic absorbed into Arbiter
> and Tribunal orchestrators (internal tally + retry). This node will be
> deleted in Phase 9.11. Code retained for reference only.

- Generic consensus tally node
- Reads juror verdict artefacts from the Workitem (parent collected them)
- Applies consensus strategy (from config: SIMPLE_MAJORITY, SUPER_MAJORITY,
  UNANIMITY)
- Tracks round count (from Workitem artefact/metadata, incremented each pass)
- Three well-known outputs: `consensus`, `retry`, `hung`
- Port consensus logic from `jury/internal/deliberation/engine.go`

Configuration:

```yaml
consensusStrategy: SIMPLE_MAJORITY
maxRounds: 3
```

Files to create:

- `nodes/deliberation-gate/main.go`
- `nodes/deliberation-gate/main_test.go`
- `nodes/deliberation-gate/testutil_test.go`

#### 5.3 Clerk Node (`nodes/clerk/`) ✅ ⛔ SUPERSEDED

> **Superseded by architecture revision.** The Clerk node is deleted in
> Phase 9.11. Forge with configurable prompts replaces the petition-drafting
> role. Codification fan-out logic moves to the standalone Codification node
> (`nodes/codification/`) in Phase 9.7.

- Receives verdict + context artefacts (from Arbiter consensus, HITL decision,
  or Tribunal hearing verdict)
- Drafts petition artefact (YAML/Markdown) with prose description of proposed
  law changes
- Fans out to Codification nodes for formal representations
- Collects codification results, assembles into petition
- On revision (feedback from Tribunal via Judiciary Gate), reads feedback and
  revises petition
- Routes to Tribunal for review
- Port prose drafting logic from `platform/clerk/internal/service/clerk_server.go`

Files to create:

- `nodes/clerk/main.go`
- `nodes/clerk/main_test.go`
- `nodes/clerk/testutil_test.go`

#### 5.4 Codification Nodes ✅

**Completed.** Reference implementation `codify-smt` created (3 files, 17 tests
passing, lint clean). Uses a FoundryAgent (KimiK2) to translate law goals into
SMT-LIB formal representations. Config-driven output format (default
`application/smt-lib`). Follows the Clerk fan-out contract: reads
`codification-goal` artefact, produces `codification-result` artefact, calls
`Complete()`.

Each codification node:

- Receives a child Workitem with law goal + context as artefacts
- Produces a formal representation in its declared output format
- Calls `Complete()`

These are new implementations. The CodificationService CRD exists
(`platform/operator/api/v1/codificationservice_types.go`) but had no
node-level code. The CRD may need to evolve to describe codification nodes
rather than codification gRPC services.

Files to create (per codification type, starting with one reference impl):

- `nodes/codify-smt/main.go` (or a generic `nodes/codification/main.go` with
  output format config)
- Tests

#### 5.5 Tribunal Router (`nodes/tribunal-router/`) ✅ ⛔ SUPERSEDED

> **Superseded by architecture revision.** Routing replaced by Tribunal
> orchestrator-internal logic and Rule Router CRD instances. This node will
> be deleted in Phase 9.11.

**Completed.** Tier-aware post-hearing routing node (3 files, 19 tests with
sub-tests passing, lint clean). Reads `deliberation-result` artefact and `law-reference` artefact,
fetches the law's tier from the Librarian, and routes based on tier and outcome:
Tier 1-2 non-promote to Clerk, Tier 1-2 promote and Tier 3+ to Advocate. Pure
routing node with no artefact modification.

- Reads verdict artefacts and law-reference artefact (for tier context)
- Routes based on tier and outcome:
  - Tier 1-2 verdict -> Clerk (to draft petition)
  - Tier 2 promote to Tier 3 -> Advocate (HITL ratification)
  - Tier 3+ -> Advocate (petition/appeal)

Files to create:

- `nodes/tribunal-router/main.go`
- `nodes/tribunal-router/main_test.go`
- `nodes/tribunal-router/testutil_test.go`

#### 5.6 Judiciary Gate (`nodes/judiciary-gate/`) ✅ ⛔ SUPERSEDED

> **Superseded by architecture revision.** Split into Rule Router CRD
> instances (routing) + `law-applicator` node (petition application). This
> node will be deleted in Phase 9.11. The `applyPetition` logic is extracted
> into `nodes/law-applicator/` in Phase 9.3.

**Completed.** Feedback resolution gate for the judiciary inner cycle (3 files,
29 tests passing, lint clean). Reads `deliberation-result`, `petition`, and
`law-reference` artefacts. Checks feedback resolution on the petition artefact.
Routing rules: Tier 4-5 always to Advocate (Governance Flow); Tier 3 approved
to Advocate (HITL ratification); rejected or unresolved feedback to Clerk
(revision); Tier 1-2 approved with all feedback resolved applies the petition
via Librarian (WriteLaw/RetireLaw/demote) and stores an approval stamp before
completing.

- Mirrors Sort for the judiciary inner cycle
- Checks feedback resolution on the petition artefact
- Routing:
  - Approved, all feedback resolved, Tier 1-2: apply petition via Librarian
    (`WriteLaw`/`RetireLaw`), add approval stamp, done
  - Rejected, unresolved feedback: route to Clerk for revision
  - Approved, Tier 3: route to HITL ratification, then apply
  - Tier 4-5: route to Advocate -> Governance Flow

Files to create:

- `nodes/judiciary-gate/main.go`
- `nodes/judiciary-gate/main_test.go`
- `nodes/judiciary-gate/testutil_test.go`

---

### Phase 6: Rewrite Existing Nodes ✅

#### 6.1 Arbiter (`nodes/arbiter/`) ✅ ⛔ SUPERSEDED

> **Superseded by architecture revision.** The Arbiter will be rewritten
> again in Phase 9.8: no evidence assembly (Facilitator does it), receives
> pre-assembled bundle as child workitem, internal tally (no Deliberation
> Gate), Suspend for Clerk child on consensus, three outcomes (resolved,
> consensus, hung → hitl-resolve).

**Completed.** Replaced `client.Deliberate()` and `client.DraftLaw()` with
Juror fan-out using `FanOut()`/`AwaitChildren()`. Evidence assembly preserved.
Output routes to Deliberation Gate instead of inline verdict processing. Stores
`verdict-context` artefact for downstream Clerk consumption. Config simplified:
`jurySize`, `jurorNode`, `gateOutput` (removed `consensusStrategy`/`maxRounds`
which are now Deliberation Gate config). All 16 tests passing, lint clean.

| File | Changes |
|---|---|
| `nodes/arbiter/main.go` | Major rewrite: removed Deliberate/DraftLaw/LinkRuling calls. Added fan-out to Juror nodes. Added verdict-context artefact. Routes to Deliberation Gate. |
| `nodes/arbiter/main_test.go` | Major rewrite: 16 tests covering fan-out count, child artefacts, verdict-context, routing, timer pause/resume, evidence assembly, config, no-deadlock fallback, errors. |
| `nodes/arbiter/testutil_test.go` | Major rewrite: removed Jury/Clerk service embeds. Added fan-out spy support (CreateChildWorkitem, RouteChild, GetChildren, PauseTimer/ResumeTimer, child artefact storage). |

#### 6.2 Tribunal (`nodes/tribunal/`) ✅ ⛔ SUPERSEDED

> **Superseded by architecture revision.** The Tribunal will be rewritten
> again in Phase 9.9: hearing mode only (review mode removed), internal
> tally and retry, fire-and-forget child to Clerk on consensus, route to
> hitl-resolve on hung.

**Completed.** Replaced `client.Deliberate()` and `client.DraftLaw()` with
Juror fan-out using `FanOut()`/`AwaitChildren()`. Implemented two-mode
operation: **hearing mode** (law lifecycle review, triggered by `law-reference`
artefact) and **review mode** (petition review, triggered by `petition`
artefact). Mode detection by artefact presence. Evidence assembly preserved.
Output routes to Deliberation Gate in both modes. Hearing mode stores
`verdict-context` artefact for downstream Clerk consumption. Config simplified:
`jurySize`, `jurorNode`, `gateOutput` (removed `consensusStrategy`/`maxRounds`
which are now Deliberation Gate config). All 33 tests passing, lint clean.

| File | Changes |
|---|---|
| `nodes/tribunal/main.go` | Major rewrite: removed Deliberate/DraftLaw calls. Added two-mode operation (hearing + review). Added fan-out to Juror nodes. Added verdict-context artefact (hearing mode). Routes to Deliberation Gate. |
| `nodes/tribunal/main_test.go` | Major rewrite: 33 tests covering hearing fan-out, review fan-out, mode detection, child artefacts, verdict-context, routing, timer pause/resume, evidence assembly, config, error propagation. |
| `nodes/tribunal/testutil_test.go` | Major rewrite: removed Jury/Clerk service embeds. Added fan-out spy support (CreateChildWorkitem, RouteChild, GetChildren, PauseTimer/ResumeTimer, child artefact storage). Added artefact-based mode control. |

#### 6.3 Advocate (`nodes/advocate/`) ✅ ⛔ SUPERSEDED

> **Superseded by architecture revision.** The Advocate is no longer part of
> the target architecture. It is replaced entirely by the Embassy boundary
> node planned in Phases 10-13.

- Remove `client.DraftLaw()` calls and `DeliberateResponse` synthetic verdicts
- New entry paths: from Deliberation Gate (`hung`), from Tribunal Router
  (Tier 3+), from Judiciary Gate (Tier 3+ ratification)
- HITL decision routes to Clerk (not directly to Librarian) so the decision
  gets codified as a petition and goes through the normal review cycle
- Rewrite tests
- **Status**: 3 files rewritten, 18 tests (22 including subtests) passing, lint clean

Files affected:

| File | Changes |
|---|---|
| `nodes/advocate/main.go` | Major rewrite: removed DraftLaw/DeliberateResponse/LinkRuling calls. Added `judiciary-ratify` escalation type. All actionable decisions store `human-decision` artefact and route to Clerk. Reject decisions Complete(). Removed `tierForTribunalChoice` helper and `outputSort` constant. |
| `nodes/advocate/main_test.go` | Major rewrite: 18 tests covering all 4 escalation types (arbiter-hung, tribunal-hung, tribunal-promote, judiciary-ratify), accept/reject paths, human-decision artefact validation, error propagation (store, route, artefact, choices, type), context cancellation. Table-driven tests for accept-route-to-clerk and reject-complete patterns. |
| `nodes/advocate/testutil_test.go` | Major rewrite: removed Jury/Clerk service embeds (UnimplementedJuryServiceServer, UnimplementedClerkServiceServer). Removed DraftLaw/LinkRuling spy methods. Added StoreArtefact spy with `getStoredArtefact` helper. Registered only 5 services (Sidecar, Operator, Archivist, Librarian, FrictionLedger). |

#### 6.4 Regression Check ✅

- `make test-all` -- all tests pass (first green build since Phase 4) ✅
- `make check-fix-all` -- all lint/tidy clean ✅
- Minor fix: 2 line-length (lll) lint violations in `nodes/juror/main_test.go` — wrapped long `seedArtefacts` calls

---

### Phase 7: Operator, Platform, and Watcher Nodes

#### 7.1 Decouple Hearing Triggers from Librarian and Operator

> **Prerequisite**: Phases 7.1e–7.1m depend on `FLOW_NAMESPACE_PLAN.md`
> (Workstream B: Retire `flow_id`) completing first. The Sidecar identity
> fallback for sessionless calls (B.2) is required for entry-bound nodes.
>
> **SATISFIED.** Workstream B is fully complete. Ready to proceed with 7.1e.

**Workstream B summary** (`FLOW_NAMESPACE_PLAN.md`): The codebase originally
threaded a `flow_id` string through every layer (proto, Sidecar, Operator,
SDK, nodes) to identify which FoundryFlow a Workitem belonged to. Foundry
Flow enforces a one-FoundryFlow-per-namespace model, making `flow_id`
redundant with the Kubernetes namespace. Workstream B retired the concept in
three stages:

1. **B.1–B.4 (Operator/Sidecar)**: Enforced singleton FoundryFlow per
   namespace. Replaced the `x-flow-flow-id` gRPC metadata header with
   `x-flow-namespace` (derived from the pod's namespace). The Sidecar now
   injects namespace and node identity on every outbound call -- including
   calls from entry-bound nodes that have no active Workitem session (the
   "identity fallback" that 7.1e–7.1m depend on). The Operator derives the
   namespace from this header instead of requiring callers to supply a flow
   ID.

2. **B.5–B.7 (Documentation/verification)**: Proto field comments updated,
   platform services audited, Operator pod construction verified to use
   `FLOW_NAMESPACE`.

3. **B.8a–B.8g + B.R (Proto field rename)**: Renamed the wire-protocol
   fields themselves: `flow_id` → `flow_namespace` in `WorkitemContext`,
   `FlowEvent`, `AddFrictionRequest`, `RecordTelemetryRequest`;
   `source_flow_id` → `source_flow_namespace` in `ReplicateLawsRequest`.
   All Go accessors (`GetFlowId()` → `GetFlowNamespace()`, `FlowId:` →
   `FlowNamespace:`) updated across every platform service, SDK, node, and
   spec file (~35 files). Regression gate passed (`make test-all &&
   make check-fix-all`). SQLite column names were intentionally left as
   `flow_id` to avoid schema migrations.

The Librarian currently has judiciary-specific logic: it subscribes to friction
events, scans for TTL expiry, and calls `CreateHearingWorkitem` on the
Operator. The Operator has a judiciary-specific RPC (`CreateHearingWorkitem`).
In the nodes-all-the-way-down model, hearing triggers belong to dedicated
watcher nodes that use the generic `CreateWorkitem` entry-bound pattern.

**New nodes:**

- **Friction Watcher** (`nodes/friction-watcher/`) -- subscribes to Event Bus
  friction channel, creates hearing Workitems when thresholds cross.
- **TTL Watcher** (`nodes/ttl-watcher/`) -- periodically polls Librarian for
  laws exceeding review TTL, creates hearing Workitems on expiry.

Both are entry-bound with the hearing entry contract. The node process is
long-lived; it creates Workitems via `CreateWorkitem` (assigned to itself),
stores a `law-reference` artefact, then routes to the Tribunal via its
`default` output.

##### 7.1a Spec Updates (~15 files) ✅

**Completed.** Replaced "Librarian triggers hearings" with "Friction Watcher / TTL Watcher
trigger hearings" across 13 spec files. Added Friction Watcher and TTL Watcher node
definitions to foundry cycle, nodes-external, CRDs, and glossary. Removed
`CreateHearingWorkitem` from Operator API. Rewrote Librarian as pure law store (removed
hearing trigger subsection from system services). Updated hearing lifecycle sequence
diagram, service invariants, inter-service contracts, and failure/degradation semantics.
Added `CreateHearingWorkitem` to superseded terms in glossary.

Files updated:

| File | Changes |
|---|---|
| `specs/01-concepts/00-overview.md` | Replaced "Librarian triggers" with "Friction Watcher triggers". Added watcher nodes to Judiciary composition. |
| `specs/01-concepts/01-architecture.md` | Rewrote Governance Plane Librarian paragraph as pure law store. Updated Hybrid Persistence to reference Friction Watcher instead of Librarian for friction channel subscription. |
| `specs/01-concepts/02-foundry-cycle.md` | Updated Tribunal hearing mode to reference watcher nodes. Added Friction Watcher and TTL Watcher node definitions. Updated hearing path diagram. Updated Judiciary composition to include watcher nodes. |
| `specs/01-concepts/03-data-model.md` | Updated Tier 1 friction threshold trigger to reference Friction Watcher; TTL decay to reference TTL Watcher. |
| `specs/01-concepts/04-governance.md` | Updated organic discovery, review hearing, decay/retirement, and friction-as-governance-signal sections to reference watcher nodes. |
| `specs/02-flow/03-nodes-external.md` | Updated Tribunal hearing mode to reference watcher nodes. Added Watcher Nodes subsection. Updated Judiciary composition and node invariants. Updated two-primary-paths paragraph. |
| `specs/02-flow/04-system-services.md` | Removed hearing trigger responsibility from Librarian description. Rewrote "Law Lifecycle Hearing Triggers" as "Law Lifecycle" (pure store). Updated friction channel subscribers, trigger ownership, execution path, sequence diagram, inter-service contracts, failure semantics, and service invariants. |
| `specs/02-flow/05-configuration.md` | Updated hearing trigger policy knob description to reference watcher nodes. |
| `specs/04-sdk/03-sdk-legal.md` | Updated friction-to-hearing trigger chain to reference Friction Watcher instead of Librarian. |
| `specs/04-sdk/06-sdk-telemetry.md` | Updated friction threshold signal destination to reference Friction Watcher. |
| `specs/05-reference/crds.md` | Updated Tribunal description. Added Friction Watcher and TTL Watcher to judiciary node table. Updated FrictionThresholds and ReviewTTLs descriptions. |
| `specs/05-reference/grpc-api.md` | Removed `CreateHearingWorkitem` from Operator service-facing methods. Updated Librarian service inventory description. |
| `specs/05-reference/glossary.md` | Rewrote Librarian as pure law store. Updated Tribunal, Judiciary, Assay, review hearing, TTL entries. Added Friction Watcher and TTL Watcher entries. Added `CreateHearingWorkitem` to superseded terms. |

##### 7.1b Proto and Generated Code ✅

**Completed.** Removed `CreateHearingWorkitem` RPC, `CreateHearingWorkitemRequest`,
and `CreateHearingWorkitemResponse` from `proto/flow/v1/operator.proto`. Ran
`buf generate` to regenerate `gen/flow/v1/`. Verified no `Hearing` references
remain in generated code. `OperatorServiceServer` interface now has 9 methods
(was 10).

- Removed `CreateHearingWorkitem` RPC and its comment (lines 58-60)
- Removed `CreateHearingWorkitemRequest` message (lines 90-92)
- Removed `CreateHearingWorkitemResponse` message (lines 94-96)
- Regenerated `gen/flow/v1/operator.pb.go` and `gen/flow/v1/operator_grpc.pb.go`

##### 7.1c Operator Cleanup ✅

**Completed.** Removed `CreateHearingWorkitem` method (75 lines) from
`operator_server.go` and 3 associated tests (`TestCreateHearingWorkitem_HappyPath`,
`_MissingLawID`, `_NoTribunalNode`) from `operator_server_test.go` (95 lines).
Build clean, all operator tests pass.

| File | Changes |
|---|---|
| `operator_server.go` | Removed `CreateHearingWorkitem` method (lines 391-465). |
| `operator_server_test.go` | Removed 3 `TestCreateHearingWorkitem_*` tests (lines 681-775). |

##### 7.1d Librarian Cleanup ✅

- Deleted `platform/librarian/internal/service/hearing_trigger.go` (342 lines)
- Deleted `platform/librarian/internal/service/hearing_trigger_test.go` — relocated
  audit tests (lines 19-226) to new `audit_test.go`; hearing trigger tests
  (lines 228-441) removed
- Stripped hearing trigger setup from `platform/librarian/cmd/main.go`: removed
  Operator connection, `ReviewTTLConfig` parsing, `HearingTrigger` creation and
  goroutine, `OPERATOR_ADDRESS`/`REVIEW_TTL_TIER*` env vars, `parseDuration` helper.
  Kept Event Bus connection for audit publishing.
- Librarian is now a pure law store + lifecycle service
- Build clean, all librarian tests pass

##### 7.1e Proto: Add Metadata to `CreateWorkitemRequest` and `WorkitemContext` ✅

**Completed.** Added `map<string, string> metadata` to `CreateWorkitemRequest`
(field 1) in `operator.proto` and to `WorkitemContext` (field 4) in
`common.proto`. Regenerated `gen/flow/v1/`. Both `GetMetadata()` accessors
confirmed in generated code. Existing nodes unaffected (metadata defaults to
empty map).

**Depends on**: `FLOW_NAMESPACE_PLAN.md` Workstream B complete (specifically
B.2 — the Sidecar identity fallback for sessionless calls).

Entry-bound nodes need to attach context when creating workitems (e.g. the
law_id that triggered a hearing). The metadata must travel through the
Operator and arrive at the handler that processes the workitem. This is
necessary because multiple replicas of the same node may exist — the entry
loop creates the workitem on one replica, but the Operator may assign it to a
different replica, so in-memory state cannot be used.

**Changes**:

1. `proto/flow/v1/operator.proto`:
   ```protobuf
   message CreateWorkitemRequest {
     map<string, string> metadata = 1;
   }
   ```

2. `proto/flow/v1/common.proto`:
   ```protobuf
   message WorkitemContext {
     string flow_namespace = 1;
     string workitem_id = 2;
     string node_id = 3;
     map<string, string> metadata = 4;
   }
   ```

3. Run `buf generate`.

**Files**: `proto/flow/v1/operator.proto`, `proto/flow/v1/common.proto`,
`gen/flow/v1/*.go` (regenerated)

**Acceptance**: Generated code compiles. Existing nodes continue to work
(metadata field is optional, defaults to empty map).

##### 7.1f Operator: Store and Propagate Metadata ✅

**Completed.** Added `Metadata map[string]string` field to `WorkitemStatus` in
`workitem_types.go`. `CreateWorkitem` in `operator_server.go` reads
`req.GetMetadata()` and stores it on the CRD via status subresource update.
`Dispatcher.Assign` accepts a `metadata` parameter and includes it in
`WorkitemContext.Metadata` when building the `AssignWorkRequest`.
`reconcilePending` passes `workitem.Status.Metadata` to the dispatcher.
Regenerated CRD schemas and deepcopy via `make manifests generate`.

Also fixed pre-existing breakage: removed orphaned `CreateHearingWorkitem`
methods from sidecar proxy and mock (missed in 7.1b/7.1c), removed unused
`hasCapability` helper from operator server, added `nolint:unparam` for
sidecar `extractIdentityFromMD` namespace return.

New tests: `TestCreateWorkitem_WithMetadata`, `TestCreateWorkitem_NoMetadata`,
`TestAssign_MetadataPropagated`. All existing tests updated for new
`Assign` signature. `make test-all && make check-fix-all` clean.

The Operator stores metadata from `CreateWorkitemRequest` on the Workitem CRD
and includes it when dispatching via `AssignWork`.

**Changes**:

1. **`workitem_types.go`**: Add `Metadata map[string]string` field to
   `WorkitemStatus`.
   ```go
   type WorkitemStatus struct {
       Phase           string            `json:"phase,omitempty"`
       CurrentAssignee string            `json:"currentAssignee,omitempty"`
       AssignedAt      *metav1.Time      `json:"assignedAt,omitempty"`
       ThrashCounters  map[string]int32  `json:"thrashCounters,omitempty"`
       Metadata        map[string]string `json:"metadata,omitempty"`
   }
   ```

2. **`operator_server.go` (`CreateWorkitem`)**: Read `req.GetMetadata()` and
   store it on `workitem.Status.Metadata` via the status subresource update.

3. **Dispatcher** (`dispatcher.go`): Accept metadata parameter (or read from
   the Workitem CRD status). Include it in `WorkitemContext.Metadata` when
   building the `AssignWorkRequest`.

4. **`workitem_controller.go` (`reconcilePending`)**: Read
   `workitem.Status.Metadata` and pass it to the Dispatcher.

5. Run `make manifests generate` to regenerate CRD schemas and deepcopy.

**Files**:
- `platform/operator/api/v1/workitem_types.go`
- `platform/operator/internal/rpc/operator_server.go`
- `platform/operator/internal/controller/workitem_controller.go`
- `platform/operator/internal/controller/dispatcher/dispatcher.go`
- All corresponding `_test.go` files
- `platform/operator/config/crd/bases/` (regenerated, do not edit)

**Acceptance**: `CreateWorkitem` with metadata stores it on the CRD. The
metadata arrives in `WorkitemContext` when the handler is invoked. Round-trip
test: create with metadata, verify handler receives it.

##### 7.1g SDK: `StartEntry`, `EntryClient`, `EventStream` ✅

**Completed.** Created `sdk/go/entry.go` (~150 lines) and
`sdk/go/entry_test.go` (~250 lines). Implements the entry-bound node SDK
pattern:

- `EntryFunc` — long-lived goroutine signature for entry logic.
- `EntryClient` — connects to Sidecar for `CreateWorkitem` (with metadata)
  and directly to Event Bus for `Subscribe`. No workitem-id interceptor
  (uses Sidecar's identity fallback).
- `EventStream` — wraps server-streaming Event Bus subscription with
  `Recv()` and `Close()`.
- `StartEntry(entry, handler, opts...)` — runs entry function and handler
  server concurrently. Shutdown on SIGTERM/SIGINT or entry error: cancels
  entry context, then GracefulStop on the gRPC server.
- `newEntryClient(sidecarAddr, eventBusAddr)` — internal constructor.
- 9 tests (8 pass, 1 skip for signal-based test), all lint clean.

**New file**: `sdk/go/entry.go`

**Types**:

```go
// EntryFunc is the function signature for entry-bound node logic.
// It runs as a long-lived goroutine alongside the handler server.
// Returning an error initiates graceful shutdown.
type EntryFunc func(ctx context.Context, client *EntryClient) error

// EntryClient provides operations available to entry-bound node logic.
// It connects to the Sidecar for CreateWorkitem (identity enriched via
// the Sidecar's namespace/node fallback) and directly to the Event Bus
// for Subscribe (same pattern as existing WatchChildren).
type EntryClient struct { /* ... */ }

// CreateWorkitem creates a new Workitem with optional metadata.
// The metadata map is stored on the Workitem CRD and passed through
// to the handler via WorkitemContext.Metadata.
func (e *EntryClient) CreateWorkitem(ctx context.Context, metadata map[string]string) (string, error)

// Subscribe opens a streaming subscription to the Event Bus.
// Returns an EventStream that yields events matching the channel
// and event type filter.
func (e *EntryClient) Subscribe(ctx context.Context, channel, eventType string) (*EventStream, error)

// Close releases underlying gRPC connections.
func (e *EntryClient) Close() error

// EventStream wraps a server-streaming Event Bus subscription.
type EventStream struct { /* ... */ }

func (s *EventStream) Recv() (*flowv1.FlowEvent, error)
func (s *EventStream) Close() error
```

**`StartEntry`**:

```go
// StartEntry launches a node with both an entry loop and a handler server.
//
// The handler server listens for Process calls from the Sidecar (same as
// flow.Start). The entry function runs concurrently in a background
// goroutine with a cancellable context and an EntryClient.
//
// Shutdown sequence:
//   1. SIGTERM/SIGINT received.
//   2. Entry context is cancelled. Entry function should return.
//   3. gRPC server performs GracefulStop.
//   4. StartEntry returns.
//
// If the entry function returns an error, shutdown is initiated.
func StartEntry(entry EntryFunc, handler Handler, opts ...StartOption) error
```

**`EntryClient` connection model**:
- Connects to the Sidecar (`localhost:50051` or `SIDECAR_ADDRESS`) for
  `CreateWorkitem`. Does NOT attach `x-flow-workitem-id` interceptor — the
  Sidecar's identity fallback (from B.2) provides namespace + node_id.
- Connects directly to the Event Bus (`EVENT_BUS_ADDRESS`) for `Subscribe`.
  Same direct-connection pattern as existing `WatchChildren` in `client.go`.

**Files**:
- `sdk/go/entry.go` (~150 lines)
- `sdk/go/entry_test.go` (~200 lines)

**Acceptance**: `StartEntry` runs both the entry function and the handler
server concurrently. `EntryClient.CreateWorkitem` succeeds when the Sidecar
has the identity fallback enabled. `EntryClient.Subscribe` returns a working
event stream. Graceful shutdown works on SIGTERM.

##### 7.1h Specs: Document Entry Node Pattern ✅

Add documentation for the entry-bound node SDK pattern.

**File**: `specs/03-node/03-patterns.md`

**Content**:
- Entry Node Pattern section
- `StartEntry(entry, handler)` lifecycle description
- `EntryClient` capabilities
- Metadata passing: entry loop attaches metadata via `CreateWorkitem`,
  handler reads it from `WorkitemContext.Metadata`
- Concurrency model: entry loop and handler run concurrently, may be on
  different replicas
- Deduplication guidance (per-replica in-memory tracking is acceptable;
  duplicate workitems are handled gracefully by downstream nodes)
- Graceful shutdown semantics
- Example: Friction Watcher pattern

**Done**: Added Entry Node Pattern section to `specs/03-node/03-patterns.md`
(~85 lines). Covers: StartEntry lifecycle with mermaid diagram, EntryClient
capabilities (CreateWorkitem + Subscribe), metadata passing semantics,
concurrency model (multi-replica awareness), deduplication guidance (best-effort
in-memory tracking), graceful shutdown sequence, Friction Watcher example. Added
entry-node-specific anti-pattern (shared mutable state between entry loop and
handler) and two new pattern invariants (9, 10). Spec lint clean.

##### 7.1i Entry Node SDK Regression Gate ✅

Run `make test-all && make check-fix-all`.

**Done.** All tests pass, all lint clean (0 issues). Stabilization checkpoint
confirmed — ready for watcher node implementations (7.1j–7.1k).

##### 7.1j Friction Watcher Node (`nodes/friction-watcher/`)

**Depends on**: 7.1e–7.1i complete (Entry Node SDK).

Entry-bound watcher node that subscribes to the Event Bus friction channel
for `friction.threshold_crossed` events. Creates hearing workitems.

**Files**: `nodes/friction-watcher/main.go`, `main_test.go`,
`testutil_test.go`

**Architecture**:
```go
func main() {
    if err := flow.StartEntry(watchFriction, handleHearing); err != nil {
        slog.Error("friction-watcher: failed", "error", err)
        os.Exit(1)
    }
}

func watchFriction(ctx context.Context, entry *flow.EntryClient) error {
    // Reconnect loop with backoff.
    for {
        events, err := entry.Subscribe(ctx, "friction", "friction.threshold_crossed")
        // ... error handling, backoff ...
        for {
            evt, err := events.Recv()
            // ... error handling, break to reconnect ...
            lawID := extractLawID(evt)
            // Per-replica dedup (best-effort).
            if alreadyPending(lawID) { continue }
            markPending(lawID)
            if _, err := entry.CreateWorkitem(ctx, map[string]string{
                "law_id": lawID,
            }); err != nil {
                clearPending(lawID)
                slog.Warn("friction-watcher: create workitem failed", "law_id", lawID, "error", err)
            }
        }
    }
}

func handleHearing(ctx context.Context, wctx *flowv1.WorkitemContext) error {
    lawID := wctx.GetMetadata()["law_id"]
    // ... create client, heartbeat, store artefact, route ...
    client.StoreArtefact(ctx, "law-reference", "txt", []byte(lawID))
    client.RouteToOutput(ctx, "default")
    return nil
}
```

**Deduplication note**: The entry loop tracks pending law IDs in a
per-replica in-memory set. With multiple replicas, the same threshold event
may reach multiple replicas (depending on Event Bus delivery semantics). This
can produce duplicate hearing workitems. This is acceptable — the Tribunal
handles duplicate hearings gracefully, and the Event Bus can be configured to
shard by law_id for stricter dedup in future.

**Acceptance**: Node compiles, tests pass. Entry loop subscribes to friction
channel, creates workitems with law_id metadata, handler stores law-reference
artefact and routes to default output.

**Done.** ✅ Friction Watcher node implemented with 3 files:
- `nodes/friction-watcher/main.go` (~260 lines): entry function with reconnect
  loop and exponential backoff, per-replica in-memory dedup tracker,
  `consumeEvents` stream processor, `handleHearing` handler that stores
  law-reference artefact and routes to default output.
- `nodes/friction-watcher/main_test.go` (~420 lines): 15 tests covering
  `extractLawID`, `pendingTracker`, `nextBackoff`, `sleepCtx`,
  `consumeEvents` (workitem creation, dedup, missing law_id, error recovery,
  context cancellation), and `processHearing` (success, missing law_id, nil
  metadata).
- `nodes/friction-watcher/testutil_test.go` (~180 lines): spy servers for
  Operator (captures CreateWorkitem), Event Bus (sends pre-configured events),
  and full handler spy (captures Heartbeat, StoreArtefact, SubmitResult).
- Also added `NewEntryClientForTest` to `sdk/go/entry.go` (exported test
  constructor for EntryClient, needed by external node packages).
- All 15 tests pass, `make test-all` clean, `make check-fix-all` 0 issues.

##### 7.1k TTL Watcher Node (`nodes/ttl-watcher/`) ✅

Entry-bound watcher node that periodically polls the Librarian via
`QueryLaws` for laws whose age exceeds their tier's configured review TTL.
Creates hearing workitems.

**Files**: `nodes/ttl-watcher/main.go`, `main_test.go`, `testutil_test.go`

**Architecture**: Same `StartEntry` pattern. The entry function polls on a
timer rather than subscribing to a stream. Uses `EntryClient.CreateWorkitem`
with `map[string]string{"law_id": lawID}`. The handler is identical to the
Friction Watcher's handler.

**Note**: The TTL Watcher's entry loop needs to call `QueryLaws` on the
Librarian. The `EntryClient` may need a Librarian client (via Sidecar) or a
direct connection. Design decision to be made during implementation — the
Sidecar already proxies LibrarianService, so the entry loop could use a
regular `flow.NewClient()` for Librarian calls (the identity fallback
provides namespace + node_id for the proxied call).

**Acceptance**: Node compiles, tests pass. Entry loop polls Librarian, creates
workitems for expired laws, handler stores law-reference artefact and routes
to default output.

##### 7.1l Build System Updates ✅

Add watcher nodes to the build system.

**Changes**:
- `Makefile`: Add build/test targets for friction-watcher and ttl-watcher.
- `AGENTS.md`: Add `nodes/friction-watcher/` and `nodes/ttl-watcher/` to
  the repository structure documentation.

**Files**: `Makefile`, `AGENTS.md`

**Note**: No `go.mod` changes needed — watcher nodes use the shared
`nodes/go.mod`.

##### 7.1m Watcher Nodes Regression Gate ✅

Run `make test-all && make check-fix-all`.

#### 7.2 Manifests -- Judiciary CRDs

> **NOTE**: GovernedArtefact CRDs (7.2a) are complete. Remaining manifest work
> (FoundryNode CRDs, Deployments, ConfigMaps) moved to `PHASE_14.md`.

##### 7.2a Judiciary GovernedArtefact CRDs ✅

Added 14 judiciary GovernedArtefact CRDs to `nodes/haiku-manifests/flow.yaml`.
All have `stamps: []`, `namespace: default`. The `petition` GovernedArtefact
already existed, so 14 new (not 15).

Also removed the redundant `app.kubernetes.io/part-of: haiku-flow` label from
all resources across all four manifest files (`flow.yaml`, `configmaps.yaml`,
`deployments.yaml`, `workitem.yaml`). Namespace scoping already binds
resources to their flow — the label was redundant information.

##### 7.2b Judiciary FoundryNode CRDs

Depends on Phase 8 (Suspend/Resume), Phase 9 (new node implementations), and
Phase 13 for the final Embassy runtime shape. The FoundryNode CRD definitions
will be written once the node images exist.
Embassy is the exception: it is operator-provisioned rather than hand-authored
in `flow.yaml`.

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
| `law-applicator` | law-applicator:latest | Action | Applies petitions via Librarian |
| `embassy` | embassy:latest | Boundary | Cross-flow import/export boundary and naturalisation |
| **Codification** | | | |
| `codify-smt` | codify-smt:latest | Computation | Formal law representations (SMT-LIB) |

##### 7.2c Judiciary NodeGroup

Add a `judiciary` NodeGroup to the FoundryFlow CRD with the judiciary nodes
and appropriate entry/exit contracts. Details depend on finalised node list.

##### 7.2d Update PLAN.md

Mark 7.2 phases complete and update status.

#### 7.3 Manifests -- Judiciary Deployments

Add Deployment manifests for all judiciary nodes to
`nodes/haiku-manifests/deployments.yaml`. Each follows the existing pattern:
node container (`:50053`) + sidecar container (`:50051`) with service
connection env vars and ConfigMap volume mounts. Deployment list depends on
finalised node inventory from 7.2b. Embassy is operator-provisioned and is not
added manually to this manifest set.

#### 7.4 Manifests -- Judiciary ConfigMaps

Add ConfigMap manifests for judiciary nodes to
`nodes/haiku-manifests/configmaps.yaml`. Key configurations:

| Node | Config Fields |
|---|---|
| `friction-watcher` | (Event Bus subscription is implicit, minimal config) |
| `ttl-watcher` | `scanPeriod`, per-tier TTL durations (`tier1` through `tier5`) |
| `arbiter` | `jurySize`, `jurorNode`, `consensusStrategy`, `maxRounds` |
| `tribunal` | `jurySize`, `jurorNode`, `consensusStrategy`, `maxRounds` |
| `facilitator` | `arbiterNode` (target for child workitem) |
| `clerk-forge` | `inputArtefacts`, `outputArtefact`, `governedArtefact`, `outputField`, `systemPrompt`, `queryTemplate` (petition-drafting prompt overrides — replace baked-in haiku defaults) |
| `clerk-refine` | `inputArtefacts`, `outputArtefact`, `governedArtefact`, `outputField`, `triageSystemPrompt`, `triageQueryTemplate`, `revisionSystemPrompt`, `revisionQueryTemplate` (petition-revision prompt overrides) |
| `codification` | `petitionArtefact`, `codificationNodes`, `defaultOutput` |
| `clerk-done-router` | CEL rules (tier-based routing: T1-2 vs T3-5) |
| `hitl-gate` | CEL rules (T3 approved → law-applicator / T4-5 → embassy) |
| `codify-smt` | `outputFormat` |
| `juror` | (personality loaded from config) |
| `hitl-appraise` | (CRD-driven: outputs, capabilities, exit-node config) |
| `arbiter-hitl-resolve` | (CRD-driven: outputs, capabilities, exit-node config) |
| `tribunal-hitl-resolve` | (CRD-driven: outputs, capabilities, exit-node config) |
| `law-applicator` | (minimal -- reads petition artefact) |

Embassy config is not hand-authored in this ConfigMap file. The Operator derives
Embassy behaviour from `FoundryFlow.spec.crossFlow`, projected trust material,
and any Embassy-specific runtime settings introduced in the later Embassy
implementation phases.

#### 7.5 Manifests -- Sort Output Update

Update the existing Sort FoundryNode CRD in `flow.yaml` to add the
`facilitator` output. Sort detects deadlocked feedback and routes to the
Facilitator, which assembles evidence and creates a child workitem for the
Arbiter.

| File | Changes |
|---|---|
| `flow.yaml` (Sort node) | Add output `{ name: "facilitator", target: "facilitator" }`. |

#### 7.6 Helm Chart Review

Review `charts/foundry-flow/values.yaml` for any Judiciary-related
configuration that should be exposed. Currently the chart only covers
control-plane infrastructure (Event Bus, Friction Ledger, Monitor, Librarian,
Operator, Sidecar). Node-level deployment is handled by haiku-manifests, not
Helm. No changes expected unless we want to add Judiciary infrastructure
(unlikely at this stage).

#### 7.7 Regression Check

- `make test-all` -- all tests pass
- `make check-fix-all` -- all lint/tidy clean

---

### Phase 8: Platform Primitives (Suspend/Resume, CompletionReason)

New core platform capabilities required by the revised architecture. These
must be implemented before the node rewrites in Phase 9 since the Facilitator
and Arbiter depend on Suspend/Resume.

#### 8.1 Proto: Suspend/Resume and CompletionReason ✅

**Completed.** Major proto redesign: replaced flat `RoutingInstruction` on
`SubmitResultRequest` with `oneof action { CompleteAction, RouteAction,
SuspendAction }`. Added `CompletionReason` enum, `ResumeWorkitem` RPC,
`completion_reason` on `ChildWorkitemStatus`. Kept `RoutingInstruction` for
`RouteChild` (children only route, never complete/suspend). Fixed all
downstream breakage across SDK, Operator, Sidecar, and ~18 node test files.
All tests pass, all lint clean.

**Proto changes** (`proto/flow/v1/operator.proto`):

- `SubmitResultRequest`: `oneof action { CompleteAction, RouteAction, SuspendAction }`
- `CompletionReason` enum: `UNSPECIFIED` (success), `CANCELLED`
- `CompleteAction`: `CompletionReason reason`
- `RouteAction`: `string target`, `bool output` (output=true for route_to_output)
- `SuspendAction`: `string condition` (CEL), `Duration timeout`
- `ResumeWorkitem` RPC + request/response messages
- `ChildWorkitemStatus`: added `CompletionReason completion_reason` field

**CRD changes** (`platform/operator/api/v1/workitem_types.go`):

- `WorkitemStatus.Phase` enum: added `Suspended`
- `WorkitemStatus`: added `CompletionReason`, `ResumeCondition`, `SuspendedAt`,
  `ResumeTimeout` fields
- `RoutingInstruction`: added `suspend` to type enum, added `CompletionReason`,
  `SuspendCondition`, `SuspendTimeout` fields

**Operator server changes**:

- `SubmitResult`: uses `convertSubmitAction()` to map oneof → CRD type
- Added `ResumeWorkitem` RPC handler (validates Suspended phase, transitions to
  Pending, clears suspend fields)
- `GetChildren`: includes `CompletionReason` in response
- Added `submitActionString()`, `completionReasonFromString()` helpers

**SDK changes** (`sdk/go/client.go`):

- `Complete()`: signature changed from `(ctx, target)` to `(ctx, ...CompleteOption)`
- Added `CompleteOption`, `WithReason(CompletionReason)` option type
- `RouteToOutput()`: uses new `RouteAction{Target, Output: true}`

**Breakage scope**: ~50 files touched (proto, gen, operator, sidecar, SDK,
18 node testutil files, 7 node main files, 2 operator test files, 1 sidecar
mock + test).

#### 8.2 Operator: Suspended Phase and Condition Evaluation

Add `Suspended` workitem phase. Implement suspension handling in the workitem
controller: CEL condition evaluation against child workitem states, timeout
enforcement (transition to `Failed` on expiry), re-dispatch to same node type
on resume.

**WorkitemStatus changes**:

```go
type WorkitemStatus struct {
    // ... existing fields ...
    ResumeCondition string        `json:"resumeCondition,omitempty"`
    SuspendedAt     *metav1.Time  `json:"suspendedAt,omitempty"`
    ResumeTimeout   *string       `json:"resumeTimeout,omitempty"`
    CompletionReason string       `json:"completionReason,omitempty"`
}
```

**Flow CRD changes** (`FoundryFlowSpec`):

```go
type SuspensionConfig struct {
    MaxSuspendTimeout     *metav1.Duration `json:"maxSuspendTimeout,omitempty"`
    DefaultSuspendTimeout *metav1.Duration `json:"defaultSuspendTimeout,omitempty"`
}
```

**Files**:
- `platform/operator/api/v1/workitem_types.go`
- `platform/operator/api/v1/foundryflow_types.go`
- `platform/operator/internal/rpc/operator_server.go`
- `platform/operator/internal/controller/workitem_controller.go`
- All corresponding test files
- `platform/operator/config/crd/bases/` (regenerated)

**8.2 Completion Notes (sub-steps a–g):**

✅ **8.2a**: Added `SuspensionConfig` struct to `foundryflow_types.go` with
`MaxSuspendTimeout` and `DefaultSuspendTimeout` fields (`*metav1.Duration`).

✅ **8.2b**: Implemented `reconcileSuspended` (timeout enforcement + CEL condition
evaluation), `resumeWorkitem` (Suspended→Pending), `evaluateResumeCondition`
(CEL env with `children: list(dyn)`). Added `cel-go v0.26.0` as direct dependency.

✅ **8.2c**: Extended scheduler `Result` with `SuspendCondition`/`SuspendTimeout`,
added `handleSuspend` method with timeout resolution from instruction/flow
defaults/max cap.

✅ **8.2d**: Added `validateSuspendTimeout` in `operator_server.go` (step 3c of
`SubmitResult`). Validates explicit timeouts against max, applies defaults.

✅ **8.2e**: Added suspend branch in `reconcileRouting`. Preserves `CurrentAssignee`,
sets `SuspendedAt`/`ResumeCondition`/`ResumeTimeout`, publishes audit/lifecycle
events.

✅ **8.2f**: Ran `make manifests generate`. Verified `SuspensionConfig` deepcopy,
workitem suspend fields, flow suspension config in CRD manifests.

✅ **8.2g**: 19 new tests across 3 test files:
- **Scheduler** (`scheduler_test.go`): 8 tests — explicit timeout, flow default
  timeout, fallback to max, timeout exceeds max (SUSPEND_TIMEOUT_EXCEEDED),
  invalid timeout string (INVALID_SUSPEND), condition passthrough, no
  SuspensionConfig, nil flow/workitem.
- **Controller** (`workitem_guard_test.go`): 7 tests — timeout exceeded→Failed,
  invalid timeout→Failed, CEL condition met→Pending (resume), CEL condition not
  met→requeue, no condition/no timeout→requeue, resume preserves assignee,
  routing suspend happy path (Routing→Suspended with all fields set).
- **RPC** (`operator_server_test.go`): 4 tests — suspend timeout exceeds max
  (InvalidArgument), no explicit timeout uses default, valid timeout accepted,
  no SuspensionConfig accepted.

✅ **8.2h**: Regression gate passed. `make test-all` all tests pass. `make
check-fix-all` found 5 `goconst` lint issues in test files (string literals
that should use existing constants). Fixed: added `phaseSuspended` constant in
`scheduler_test.go`, `testAssignee` constant in `workitem_guard_test.go` (for
`"worker"`), replaced `"Suspended"` with `wiPhaseSuspended` in guard tests,
replaced `"Routing"`/`"suspend"` with `phaseRouting`/`suspendType` in RPC tests.
Re-ran lint — 0 issues. All tests pass.

#### 8.3 Sidecar: Proxy Suspend and Resume ✅

Add proxy support for `SuspendAction` in `SubmitResult` and for the
`ResumeWorkitem` RPC.

**Files**: `platform/sidecar/cmd/main.go`, proxy files

**Completed:**
- Added `ResumeWorkitem` proxy method to `OperatorProxy` (thin pass-through with metadata propagation)
- Added `ResumeWorkitem` to mock `OperatorHandler` for testing
- `SubmitResult` already handles `SuspendAction` transparently (forwards entire proto without inspecting oneof)
- 2 new tests: forwarding + metadata propagation for `ResumeWorkitem`
- Regression gate passed: `make test-all` + `make check-fix-all` clean

#### 8.4 SDK: Suspend(), Resume(), WithReason() ✅

Add `Suspend()`, `Resume()`, `SuspendOption`s, and `WithReason()` to the SDK.

```go
func (c *Client) Suspend(ctx context.Context, opts ...SuspendOption) error
func (c *Client) Resume(ctx context.Context, workitemID string) error
func WithCondition(cel string) SuspendOption
func WithTimeout(d time.Duration) SuspendOption
func WithReason(r CompletionReason) CompleteOption
```

**Files**: `sdk/go/client.go`, `sdk/go/client_test.go`

**Completed:**
- Added `SuspendOption` type with `WithCondition()` and `WithTimeout()` option constructors
- `Suspend()` returns `error` only (no accepted bool — caller should return nil after suspending)
- `Resume()` takes explicit `workitemID string` parameter for the target workitem
- Added `lastSubmitReq` and `lastResumeReq` capture fields to test spy
- 8 new tests: 4 Suspend (no options, with condition, with timeout, both), 2 Resume (correct ID, caller metadata), 2 Complete WithReason (CANCELLED, UNSPECIFIED default)
- Regression gate passed: `make test-all` + `make check-fix-all` clean

#### 8.5 Regression Gate ✅

- `make test-all` -- all tests pass ✅
- `make check-fix-all` -- all lint/tidy clean ✅ (one `lll` fix in `client_test.go`)

---

### Cross-Cutting: InputArtefact → InputArtefacts Refactor ✅

**Completed.** All main-cycle nodes that consumed a single `inputArtefact`
config field (string) were refactored to use plural `inputArtefacts` (string
slice), enabling nodes to consume multiple input artefacts.

**Shared helper** (`nodes/internal/artefacts/fetch.go`, 49 lines):
- `FetchInputs(ctx, client, ids)` — fetches each artefact by ID and
  concatenates with `## <id>` Markdown headers
- `InputLabel(ids)` — returns human-readable comma-joined label for prompt
  templates

**Nodes updated** (config `InputArtefact string` → `InputArtefacts []string`):
- Forge (`nodes/forge/main.go`) — uses `artefacts.FetchInputs`
- Refine (`nodes/refine/main.go`) — uses `artefacts.FetchInputs`
- Reviewer (`nodes/reviewer/main.go`, `agent_review.go`) — uses
  `artefacts.FetchInputs` and `artefacts.InputLabel` for prompt template data
- Appraise (`nodes/appraise/main.go`, `agent_eval.go`) — uses
  `artefacts.FetchInputs` and `artefacts.InputLabel`, fan-out passes combined
  content as `"inputs"` artefact to child Reviewer nodes

**Facilitator** (`nodes/facilitator/main.go`):
- Removed `maxArtefactContentLen` constant (no artificial truncation)
- `buildDisputeInputs` delegates to shared `FetchInputs` helper
- `buildDisputeArtefact` returns raw content directly

**Manifests** (`nodes/haiku-manifests/configmaps.yaml`):
- Updated 4 ConfigMaps: `inputArtefact: "..."` → `inputArtefacts: ["..."]`
  (forge-config, appraise-config, reviewer-config, refine-config)

All tests pass, lint clean, build clean.

---

New and rewritten nodes for the revised architecture. Ordered by dependency:
standalone/leaf nodes first, then orchestrators that create children targeting
them, then cleanup.

#### 9.1 Rule Router Node (`nodes/rule-router/`) ✅

**Completed.** Generic CEL-based routing node (3 files, 1,788 lines, 35 tests
passing, lint clean). Highest-priority new implementation because multiple CRD
instances depend on it.

- Parses CEL rules from config at startup
- On workitem arrival: lazily loads referenced data, evaluates rules in order,
  routes to first match
- Five CEL environment variables (lazily loaded): `metadata`, `artefacts`,
  `feedback` (aggregated unresolved/deadlocked/total counts), `stamps`
  (per-artefact), `children` (phase, completion_reason, workitem_id)
- Heuristic variable detection via `needsVar()` — only referenced variables
  trigger RPCs
- Structured telemetry: `foundry.rule_router.started`, `.matched`, `.no_match`
- Dependencies: `github.com/google/cel-go`
- Image: `rule-router:latest`

Files created:
- `nodes/rule-router/main.go`
- `nodes/rule-router/main_test.go`
- `nodes/rule-router/testutil_test.go`

#### 9.2 Facilitator Node (`nodes/facilitator/`) ✅

**Completed.** Lifecycle owner for deadlock resolution (3 files, 3,007 lines,
49 tests passing, lint clean). Generic — handles any governed artefact's
deadlocked feedback.

Handler logic:
1. **First invocation** (no completed children):
   - Discovers flow topology and exit contract
   - Scans all artefact kinds for DEADLOCKED feedback, selects highest-severity
   - Assembles evidence: 6 child artefacts (`dispute-workitem`,
     `dispute-details`, `dispute-artefact`, `dispute-inputs`, `appendix`,
     `disputed-artefact`)
   - `dispute-inputs` uses shared `artefacts.FetchInputs()` helper
   - `CreateChildWorkitem()` targeting the Arbiter, routes child, then suspends
   - `Suspend(WithCondition("children.all(c, c.phase == \"Completed\")"))`
2. **Post-resume** (child completed):
   - Check child's `CompletionReason`
   - If cancelled: `Complete(WithReason(cancelled))`
   - If success: `RouteToOutput("resolved")`

Notable design decisions:
- GetLaw failure is non-fatal (best-effort enrichment, fallback text)
- No-deadlock graceful exit (routes to `resolved` with warning)
- Config: `arbiterNode` (default `"arbiter"`) and `inputArtefacts` (list)
- Structured telemetry: `started`, `evidence_assembled`, `suspended`,
  `resolved`, `cancelled`, `no_deadlock`

Files created:
- `nodes/facilitator/main.go`
- `nodes/facilitator/main_test.go`
- `nodes/facilitator/testutil_test.go`

#### 9.3 Law-Applicator Node (`nodes/law-applicator/`)

Action node that applies approved petitions via the Librarian. Reads the
`petition` artefact, calls `WriteLaw`/`RetireLaw` for each change, and calls
`Complete()`.

The implementation extracts the apply logic from `nodes/judiciary-gate/main.go`.

Files created: ✅
- `nodes/law-applicator/main.go` — ~265 lines (petition types, apply logic, Complete)
- `nodes/law-applicator/main_test.go` — 18 tests (8 happy path, 10 error path)
- `nodes/law-applicator/testutil_test.go` — spy server, setup helper, assertion helpers

#### 9.4 Generic HITL Node (`nodes/hitl/`) ✅

Config-driven HITL node. One image, many CRD instances.

- Reads configured artefacts and presents them to the human
- Outputs declared on the CRD become user action choices
- `WRITE:feedback` capability enables a "provide feedback" action
- Exit-node config enables a "cancel" action (`Complete(WithReason(cancelled))`)
- Image: `hitl:latest`

Files to create:
- `nodes/hitl/main.go`
- `nodes/hitl/main_test.go`
- `nodes/hitl/testutil_test.go`

#### 9.5 SDK Agent Contracts (`sdk/go/`) ✅

> **Architecture revision.** The original Phase 9.5 ("Configurable Forge
> Prompts") and 9.6 ("Configurable Refine Prompts") are replaced by a
> layered Agent Contracts architecture. Instead of removing Go constant
> defaults and making ConfigMap the sole source, we define typed Go
> interfaces (contracts) in the SDK, extract shared handler logic, and
> have concrete agents keep their baked-in defaults with optional
> ConfigMap overrides.

**Completed.** Six typed Go interfaces and their SDK-level result types
defined in 4 new files. All interfaces compile, no existing code changes,
all tests pass, lint clean.

**Current agent inventory (6 agents across 4 node types):**

| Agent | Node | File | Model | Prompt Pattern | Schema |
|---|---|---|---|---|---|
| ForgeAgent | forge | `nodes/forge/agent.go` | GptOss120bOllama | Go const, rendered with `OutputField` | Dynamic from `OutputField` |
| TriageAgent | refine | `nodes/refine/agent_triage.go` | GptOss120bOllama | Go const, rendered with `OutputArtefact` | Hardcoded `var []byte` |
| RevisionAgent | refine | `nodes/refine/agent_revision.go` | GptOss120bOllama | Go const, rendered with `OutputArtefact`+`OutputField` | Dynamic from `OutputField` |
| EvalAgent | appraise | `nodes/appraise/agent_eval.go` | KimiK2Ollama | Go const, rendered with `ReviewArtefact`+`InputArtefact` | Hardcoded `var []byte` |
| FindingAgent | appraise | `nodes/appraise/agent_finding.go` | KimiK2Ollama | Go const, rendered with `ReviewArtefact`+`GovernedArtefact` | Hardcoded `var []byte` |
| ReviewAgent | reviewer | `nodes/reviewer/agent_review.go` | KimiK2Ollama | Go const, rendered with `ReviewArtefact`+`InputArtefact`+`DivisionSuffix` | Hardcoded `var []byte` |

**Note:** Sort has no agent — it is pure topology-driven routing, already a
perfect generic LEGO block. No contract needed.

**Six contracts:**

| Contract | Node | Agent(s) | Input Types | Result Type |
|---|---|---|---|---|
| `ForgeContract` | Forge | ForgeAgent | `ctx`, `input string`, `laws []*flowv1.Law` | `string` (generated content) |
| `TriageContract` | Refine | TriageAgent | `ctx`, `fb *flowv1.FeedbackItem`, `inputContent, reviewContent string`, `laws []*flowv1.Law` | `*TriageResult` (decision + justification) |
| `RevisionContract` | Refine | RevisionAgent | `ctx`, `inputContent, reviewContent string`, `laws []*flowv1.Law`, `fixes []ActionedFeedback` | `string` (revised content) |
| `ReviewContract` | Reviewer | ReviewAgent | `ctx`, `inputContent, reviewContent string`, `laws []ReviewLaw`, `history []ReviewHistory` | `*ReviewResult` (feedback items) |
| `EvalContract` | Appraise | EvalAgent | `ctx`, `fb *flowv1.FeedbackItem`, `inputContent, reviewContent, kind string` | `*EvalResult` (verdict + reason) |
| `FindingContract` | Appraise | FindingAgent | `ctx`, `items []*flowv1.FeedbackItem` | `*FindingsResult` (findings list) |

**Result types** are SDK-level domain types (not raw `[]byte`). They are
defined alongside the interfaces in `sdk/go/`.

**SDK-level domain types introduced:**
- `TriageResult` — Decision, Message, JustificationType, CitationIDs, Argument
- `ActionedFeedback` — FeedbackID, Message, FixDescription
- `ReviewResult` — Feedback ([]ReviewFeedback)
- `ReviewFeedback` — Message, Severity, CitedLaws
- `ReviewLaw` — ID, Tier, Goal
- `ReviewHistory` — State, Message
- `EvalResult` — Verdict, Reason
- `FindingsResult` — Findings ([]Finding)
- `Finding` — Goal, AppliesTo, Rationale

**Interface example:**

```go
// ForgeContract defines the boundary between the Forge handler and its
// agent implementation. The handler provides structured inputs; the agent
// returns typed output.
type ForgeContract interface {
    Run(ctx context.Context, input string, laws []*flowv1.Law) (string, error)
}
```

**Key design rules:**

1. **Prompts are encapsulated in the agent, not the node.** System prompts,
   query templates, schemas, and model choice all belong to the concrete
   agent implementation. The node provides a "contract" specifying what
   inputs it will provide and what outputs it expects.
2. **Each image = shared handler + one specific agent wired together.** The
   image's `main.go` is thin: load config, construct the concrete agent
   (with optional prompt overrides from config), call the shared handler
   passing the agent as the contract interface.
3. **Agent swap at image level, not config level.** Each node image contains
   exactly one agent implementation. To use a different agent (different
   model, schema, or contract implementation), deploy a different image via
   the CRD. No in-image agent registry.

**Files created:**
- `sdk/go/contract_forge.go` — `ForgeContract` interface
- `sdk/go/contract_refine.go` — `TriageContract`, `RevisionContract`, `TriageResult`, `ActionedFeedback`
- `sdk/go/contract_review.go` — `ReviewContract`, `ReviewResult`, `ReviewFeedback`, `ReviewLaw`, `ReviewHistory`
- `sdk/go/contract_appraise.go` — `EvalContract`, `FindingContract`, `EvalResult`, `FindingsResult`, `Finding`

#### 9.6 Shared Handler Library (`nodes/internal/handlers/`) ✅

Extract the handler logic that is identical regardless of which agent
implements the contract into a shared handler library. Each handler takes the
contract interface as a parameter, not a concrete agent type.

**Handler inventory:**

| Handler | Contract | Logic |
|---|---|---|
| `HandleForge` | `ForgeContract` | Fetch inputs, query laws, call agent, store output artefact, route |
| `HandleRefine` | `TriageContract` + `RevisionContract` | Fetch inputs + output + feedback, triage (parallel), revise if actioned, store, route |
| `HandleReview` | `ReviewContract` | Fetch inputs + review + laws/history/division from artefacts, convert to SDK types, call agent, store review-output, Complete() |
| `HandleAppraise` | `EvalContract` + `FindingContract` | Evaluate feedback (parallel), fan-out review, stamp, raise feedback, mint findings, route |

**Handler signature example:**

```go
// HandleForge executes the Forge node handler logic using the provided
// contract implementation. The handler is generic — it works with any
// ForgeContract (haiku agent, petition agent, etc.).
func HandleForge(ctx context.Context, client *flow.Client, agent flow.ForgeContract, cfg ForgeConfig) error
```

**Key design points:**

1. Handlers live in `nodes/internal/handlers/` — internal to the nodes
   module, not exported in the SDK.
2. Config types (`ForgeConfig`, `RefineConfig`, `ReviewConfig`,
   `AppraiseConfig`) are defined in the handlers package and contain only
   the handler-level config (artefact names, output names, governed
   artefact). Agent-level config (prompts, model, schema) stays in the
   concrete agent.
3. The existing `nodes/internal/artefacts/fetch.go` shared helper is
   already used by handlers — this extends the pattern.
4. Shared serialization types (`DivisionData`, `LawData`, `HistoryData`)
   and convention artefact ID constants (`ArtefactLaws`, `ArtefactHistory`,
   `ArtefactDivision`, `ArtefactReviewOutput`) are defined in the handlers
   package and exported for use by both the Reviewer and Appraise nodes.
   This eliminates the previous duplication between the two nodes.
5. `HandleReview` converts wire-format `LawData`/`HistoryData` to SDK-level
   `flow.ReviewLaw`/`flow.ReviewHistory` before calling the agent — keeping
   the ReviewContract decoupled from serialization concerns.
6. `HandleAppraise` is a unified orchestration function (not split into
   `HandleEval`/`HandleFinding`) because the three phases share read state
   and must execute sequentially.

**Files created:**
- `nodes/internal/handlers/forge.go` — `HandleForge` + `ForgeConfig`
- `nodes/internal/handlers/refine.go` — `HandleRefine` + `RefineConfig` + `triageFeedback` + `buildJustification`
- `nodes/internal/handlers/review.go` — `HandleReview` + `ReviewConfig` + shared types (`DivisionData`, `LawData`, `HistoryData`) + artefact ID constants
- `nodes/internal/handlers/appraise.go` — `HandleAppraise` + `AppraiseConfig` + `evaluateFeedback` + `fanOutReview` + `mintFindings` + `groupLawsByDivision` + severity helpers

**Acceptance:** All handlers compile. `make test-all` passes (all existing
tests green — handlers are not yet called by node `main.go` files; that
happens in 9.6a–9.6d). `make check-fix-all` clean (0 issues).

#### 9.6a Forge: Agent Contract + ConfigMap Override (`nodes/forge/`) ✅

Apply the Agent Contracts pattern to the Forge node. The ForgeAgent
implements `ForgeContract`. Prompts use code defaults with optional
ConfigMap override. The `main.go` becomes thin.

**Changes:**

1. `nodes/forge/agent.go`:
   - ForgeAgent implements `flow.ForgeContract`
   - **Keep** `forgeSystemPromptTemplate` and `forgeQueryPromptTemplate`
     Go constants as baked-in defaults
   - Constructor accepts optional `systemPrompt`/`queryTemplate` overrides
     from config. If provided, they replace the defaults. If empty/missing,
     the defaults are used.
   - `Run(ctx, input, laws)` returns `(string, error)` per the contract

2. `nodes/forge/main.go`:
   - Add optional `SystemPrompt string` and `QueryTemplate string` fields
     to `forgeConfig` (empty = use default)
   - Replace inline handler logic with call to
     `handlers.HandleForge(ctx, client, agent, handlerCfg)`
   - `main.go` becomes: load config → construct ForgeAgent (with optional
     overrides) → call shared handler

3. `nodes/forge/main_test.go`:
   - Update tests for contract-based handler
   - Add test: ConfigMap overrides replace defaults
   - Add test: empty ConfigMap fields use defaults (no error)

4. `nodes/haiku-manifests/configmaps.yaml`:
   - No changes needed for haiku (uses defaults). Clerk-forge ConfigMap will
     provide petition-specific prompts as overrides.

**Files affected:**
- `nodes/forge/agent.go` — implement ForgeContract, add override support
- `nodes/forge/main.go` — thin handler, delegate to shared handler
- `nodes/forge/main_test.go` — update tests

#### 9.6b Refine: Agent Contracts + ConfigMap Override (`nodes/refine/`) ✅

Same treatment as Forge, but for Refine's two agents (TriageAgent and
RevisionAgent). Both implement their respective contracts. Both keep
baked-in default prompts with optional ConfigMap overrides.

**Changes:**

1. `nodes/refine/agent_triage.go`:
   - TriageAgent implements `flow.TriageContract`
   - **Keep** Go constant default prompts
   - Constructor accepts optional overrides from config
   - `Run(ctx, feedbackContext)` returns `(TriageResult, error)`

2. `nodes/refine/agent_revision.go`:
   - RevisionAgent implements `flow.RevisionContract`
   - **Keep** Go constant default prompts
   - Constructor accepts optional overrides from config
   - `Run(ctx, content, laws, fixes)` returns `(string, error)`

3. `nodes/refine/main.go`:
   - Add optional override fields to `refineConfig`:
     `TriageSystemPrompt`, `TriageQueryTemplate`,
     `RevisionSystemPrompt`, `RevisionQueryTemplate` (empty = use default)
   - Replace inline handler logic with call to
     `handlers.HandleRefine(ctx, client, triageAgent, revisionAgent, handlerCfg)`

4. Tests and ConfigMap updated accordingly.

**Files affected:**
- `nodes/refine/agent_triage.go` — implement TriageContract, add override support
- `nodes/refine/agent_revision.go` — implement RevisionContract, add override support
- `nodes/refine/main.go` — thin handler, delegate to shared handler
- `nodes/refine/main_test.go` — update tests
- `nodes/refine/testutil_test.go` — update test helpers

#### 9.6c Reviewer: Agent Contract (`nodes/reviewer/`) ✅

Apply the Agent Contracts pattern to the Reviewer node. The ReviewAgent
implements `ReviewContract`. Prompts keep baked-in defaults with optional
ConfigMap overrides.

**Changes:**

1. `nodes/reviewer/agent_review.go`:
   - ReviewAgent implements `flow.ReviewContract`
   - **Keep** Go constant default prompts
   - Constructor accepts optional overrides from config
   - `Run(ctx, content, laws, history)` returns `(ReviewResult, error)`

2. `nodes/reviewer/main.go`:
   - Add optional override fields to `reviewerConfig`
   - Replace inline handler logic with call to
     `handlers.HandleReview(ctx, client, agent, handlerCfg)`

3. Tests updated accordingly.

**Files affected:**
- `nodes/reviewer/agent_review.go` — implement ReviewContract, add override support
- `nodes/reviewer/main.go` — thin handler, delegate to shared handler
- `nodes/reviewer/main_test.go` — update tests

#### 9.6d Appraise: Agent Contracts (`nodes/appraise/`) ✅

Apply the Agent Contracts pattern to the Appraise node. Two agents:
EvalAgent implements `EvalContract`, FindingAgent implements
`FindingContract`. Both keep baked-in defaults with optional ConfigMap
overrides.

**Changes:**

1. `nodes/appraise/agent_eval.go`:
   - EvalAgent implements `flow.EvalContract`
   - **Keep** Go constant default prompts
   - Constructor accepts optional overrides from config
   - `Run(ctx, feedback, history, kind)` returns `(EvalResult, error)`

2. `nodes/appraise/agent_finding.go`:
   - FindingAgent implements `flow.FindingContract`
   - **Keep** Go constant default prompts
   - Constructor accepts optional overrides from config
   - `Run(ctx, discussions)` returns `(FindingsResult, error)`

3. `nodes/appraise/main.go`:
   - Add optional override fields to `appraiseConfig`
   - Replace inline handler logic with call to shared handlers
     (`HandleEval`, `HandleFinding`)

4. Tests updated accordingly.

**Files affected:**
- `nodes/appraise/agent_eval.go` — implement EvalContract, add override support
- `nodes/appraise/agent_finding.go` — implement FindingContract, add override support
- `nodes/appraise/main.go` — thin handler, delegate to shared handlers
- `nodes/appraise/main_test.go` — update tests

#### 9.6e Agent Contracts Regression Gate ✅

- `make test-all` -- all tests pass
- `make check-fix-all` -- all lint/tidy clean (one lll fix in `nodes/reviewer/agent_review.go`)

#### 9.7 Codification Node (`nodes/codification/`)

Standalone fan-out orchestrator for formal law representations. Sits between
Forge/Refine and Sort in the Clerk cycle. One image (`codification:latest`),
one CRD instance. **No LLM** — pure orchestration.

A petition is a multi-law patch. Each `petitionChange` represents a change
to one law (create, retire, demote, update). The Codification node iterates
the petition's changes and fans out to codify-\* nodes **per-change** for
every change that requires formal representations. Retire changes are
skipped — retirement is purely administrative, no SMT-LIB or Rego needed.

The Codification node does **not** read `verdict-context`. All the
information it needs (`goal`, `applies_to`, `tier`, `action`) is already
present on each `petitionChange` entry in the petition artefact. The
verdict-context is consumed upstream by clerk-forge when it produces the
petition.

Available codifiers are listed in the node's static YAML config
(`codificationNodes`). No runtime discovery — when a new codifier is
deployed, the config is updated.

**Config:**

```yaml
petitionArtefact:   "petition"         # artefact ID to read/update
codificationNodes:                     # list of codify-* node names (static config)
  - codify-smt
defaultOutput:      "default"
```

**Key design points:**

- The codification fan-out contract is unchanged: `codification-goal` in,
  `codification-result` out, child calls `Complete()`. Codify-SMT already
  implements this contract — no changes to `nodes/codify-smt/` needed.
- Fan-out is **per-change × per-codifier**: a petition with 2 qualifying
  changes and 2 codification nodes produces 4 child Workitems. Results are
  mapped back to originating changes by index arithmetic.
- If **all** changes are retire → no fan-out, route directly to output.
- Petition structure survives the read → modify → store round-trip. Only
  the `Representations` field on qualifying changes is mutated.

##### 9.7a Core Implementation (`nodes/codification/main.go`)

**Types:**

- `codificationConfig` — `PetitionArtefact string`, `CodificationNodes
  []string`, `DefaultOutput string` (with default helpers)
- `petition` / `petitionBody` / `petitionContext` / `petitionChange` /
  `petitionRep` — petition structure (same shape as Clerk's types)
- `codificationGoal` — `Goal`, `AppliesTo`, `Tier`, `Action` (built
  per-change, matches what codify-smt expects)
- `codificationResult` — `Type`, `Content` (what codify-\* children
  produce)

**Entry point:**

- `main()` → `flow.Start(handler)`
- `handler(ctx, wctx)` → load config, create client, call
  `handleCodification`

**Handler logic (`handleCodification`):**

1. Heartbeat.
2. Read petition artefact → unmarshal into `petition`.
3. Partition changes into two lists:
   - `needsCodification`: action is `create`, `update`, or `demote`.
   - `skipCodification`: action is `retire`.
4. If no changes need codification → store petition as-is, route to
   default output, return.
5. For each qualifying change, build a `codificationGoal` from that
   change's `Goal`, `AppliesTo`, `Tier`, `Action`.
6. Build `[]FanOutTask`: for each qualifying change × each configured
   codify-\* node → one task with `codification-goal` artefact.
   Total children = `len(needsCodification) × len(codificationNodes)`.
7. `client.FanOut(ctx, tasks)`.
8. `client.AwaitChildren(ctx)`.
9. `client.CollectArtefacts(ctx, children, "codification-result")`.
10. Map results back to originating changes by index:
    `changeIdx = taskIdx / len(codificationNodes)`. Parse each child's
    result as `codificationResult{Type, Content}` → append as
    `petitionRep` to the originating change's `Representations`.
11. Store updated petition artefact.
12. Route to default output.

**Files:**
- `nodes/codification/main.go`

##### 9.7b Tests (`nodes/codification/main_test.go` + `testutil_test.go`)

**Test utility (`testutil_test.go`):**

- Spy struct implementing Sidecar + Operator + Archivist gRPC services.
- `setupCodificationTest` helper: starts local gRPC server, returns
  `*flow.Client`.
- Seed helpers: `seedPetition(changes...)`, `seedCodificationResult(type,
  content)`.

**Test cases (`main_test.go`):**

1. **Happy path (mixed changes):** Petition with 2 changes (create +
   retire), 1 codify node. Only the create change gets representations.
   Retire change passes through untouched.
2. **Multi-change, multi-codifier:** 2 qualifying changes × 2 codify
   nodes = 4 children. Representations mapped correctly to originating
   changes.
3. **All retire:** No fan-out occurs. Petition stored unchanged. Routes
   to default output.
4. **Single change:** Backward-compatible with single-change petitions.
5. **Missing petition artefact:** Returns error.
6. **Child failure:** Error propagation from failed codify-\* child.
7. **Empty codification nodes list:** No fan-out. Routes directly.
8. **Round-trip preservation:** Non-representation petition fields survive
   the read → modify → store cycle.

**Files:**
- `nodes/codification/main_test.go`
- `nodes/codification/testutil_test.go`

##### 9.7c Regression Gate

- `make test-all` — all tests pass.
- `make check-fix-all` — all lint/tidy clean.
- Update PLAN.md: mark 9.7 complete, advance status line.

#### 9.8 Arbiter Rewrite (`nodes/arbiter/`)

**Major rewrite.** The Arbiter no longer assembles evidence (the Facilitator
does). It receives a pre-assembled evidence bundle as a child workitem.
Internal tally and retry (no external Tally or delib-router). May suspend
for a Clerk child.

**Verdict-Context Contract (revised):**

The verdict-context artefact is a **prose decision**, not a structured list
of actions. The court's reasoned argument is self-contained — clerk-forge
interprets it into structured petition changes.

```json
{
  "trigger": "deadlock-resolution",
  "decision": "The court has reviewed the evidence and decided that..."
}
```

Two fields only:

- `trigger` — provenance: what kind of proceeding produced this decision
  (`"deadlock-resolution"`). Informs clerk-forge's framing of the petition.
- `decision` — the court's full reasoned argument in natural language.
  Describes what laws should be created, retired, updated, and why.

The old structured fields (`goal`, `action`, `tier`, `applies_to`,
`feedback_ids`, `source_workitem`) are **removed**. The Arbiter's own
workitem context (source workitem, feedback IDs) lives on the parent
workitem — the Clerk child has no access to it and doesn't need it.
Everything the Clerk needs is in the prose decision. clerk-appraise
reviews the petition against the verdict decision to ensure alignment.

**What gets removed from current code:**

- Evidence assembly (`assembleEvidence`, `findDeadlockedFeedback`,
  topology discovery, all feedback/artefact/law/friction reads) — the
  Facilitator now owns evidence assembly. The Arbiter receives a
  pre-assembled `evidence-bundle` artefact.
- Single-output routing to deliberation-gate — absorbed internally.
- Structured verdict-context fields — replaced with prose decision.
- `gateOutput` config — deliberation-gate no longer exists.

**What gets added:**

- Internal tally: read juror vote artefacts, count outcomes, apply
  consensus strategy.
- Multi-round retry loop: maintain round counter, pass prior-round
  reasoning to jurors, enforce `maxRounds`.
- Three-outcome branching: resolved → `Complete()`, consensus →
  `CreateChildWorkitem()` + `Suspend()`, hung → `RouteToOutput("hung")`.
- Two-invocation handler: first invocation (deliberation), post-resume
  (check Clerk child completion reason).
- Prose verdict-context: synthesize jury reasoning into natural language
  decision.

##### 9.8a Shared Tally Library (`nodes/internal/tally/`)

Extract consensus tally logic into a shared package used by both Arbiter
(9.8) and Tribunal (9.9). This avoids duplicating the tally/retry logic.

**Types:**

- `TallyConfig` — `ConsensusStrategy`, `MaxRounds int`,
  `JurySize int`, `JurorNode string`.
- `JurorVote` — `Outcome string`, `Reasoning string` (parsed from juror
  artefact).
- `TallyResult` — `Outcome string` (winning outcome or empty),
  `IsConsensus bool`, `IsHung bool`, `Votes []JurorVote`,
  `Round int`.
- `RoundInput` — `Question string`, `Evidence string`,
  `AllowedOutcomes []string`, `PriorRoundReasoning string` (empty on
  round 1).

**Functions:**

- `Tally(votes []JurorVote, strategy ConsensusStrategy) TallyResult` —
  counts votes and applies the strategy (simple majority, super majority,
  unanimity). Returns which outcome won or whether it's hung.
- `BuildFanOutTasks(cfg TallyConfig, input RoundInput) []flow.FanOutTask`
  — builds N fan-out tasks with juror artefacts (question, evidence,
  allowed-outcomes, prior-round-reasoning).
- `CollectVotes(ctx, client, children) ([]JurorVote, error)` — reads
  juror vote artefacts from completed children, parses into `JurorVote`.
- `SummariseRound(votes []JurorVote) string` — builds prior-round
  reasoning text for retry.

**Files:**
- `nodes/internal/tally/tally.go`
- `nodes/internal/tally/tally_test.go`

##### 9.8b Arbiter Core (`nodes/arbiter/main.go`)

Full rewrite of `main.go`. The handler uses a two-invocation pattern:

**Types:**

- `arbiterConfig` — `JurySize`, `JurorNode`, `ConsensusStrategy`,
  `MaxRounds`, `ClerkNode`, `HungOutput`.
- `verdictContext` — `Trigger string`, `Decision string`.

**First invocation (`handleArbiter`):**

1. Heartbeat.
2. Check for completed Clerk children (`client.GetChildren()`) — if
   found, jump to post-resume logic.
3. Read `evidence-bundle` artefact (pre-assembled by Facilitator).
4. Frame question (hardcoded for deadlock resolution).
5. Deliberation loop (up to `maxRounds`):
   a. Build fan-out tasks via `tally.BuildFanOutTasks()`.
   b. `client.FanOut()` → `client.AwaitChildren()`.
   c. `tally.CollectVotes()` from children.
   d. `tally.Tally()` with configured strategy.
   e. If consensus → break loop.
   f. If hung and more rounds → `tally.SummariseRound()` for next
      round's prior reasoning.
6. Post-loop outcomes:
   - **Resolved** (consensus outcome = "resolved"): `Complete()`.
   - **Consensus** (law change needed): synthesize prose decision from
     jury reasoning, store verdict-context, `CreateChildWorkitem()` →
     Clerk, `Suspend(WithCondition(...))`.
   - **Hung** (max rounds exhausted): `RouteToOutput("hung")`.

**Post-resume:**

1. `client.GetChildren()` — find completed Clerk child.
2. Check `CompletionReason`:
   - Cancelled → `Complete(WithReason(cancelled))`.
   - Success → `Complete()`.

**Files:**
- `nodes/arbiter/main.go`

##### 9.8c Arbiter Tests (`nodes/arbiter/main_test.go` + `testutil_test.go`)

Full rewrite of test files.

**Test cases:**

1. **Happy path (consensus round 1):** evidence-bundle → fan-out → tally
   → consensus → Clerk child + Suspend.
2. **Resolved (no law change needed):** jury votes "resolved" →
   `Complete()` directly.
3. **Hung after max rounds:** no consensus after N rounds →
   `RouteToOutput("hung")`.
4. **Multi-round retry:** no consensus round 1 → prior-round reasoning
   passed to round 2 → consensus round 2.
5. **Post-resume success:** Clerk child completed → `Complete()`.
6. **Post-resume cancelled:** Clerk child cancelled →
   `Complete(WithReason(cancelled))`.
7. **Verdict-context is prose:** verify `trigger` + `decision` fields
   only, no structured fields.
8. **Consensus strategies:** simple majority, super majority, unanimity.
9. **Missing evidence-bundle artefact:** error.
10. **Fan-out failure:** error propagation.
11. **Config defaults and custom config.**

**Files:**
- `nodes/arbiter/main_test.go`
- `nodes/arbiter/testutil_test.go`

##### 9.8d Arbiter Regression Gate

- `make test-all` — all tests pass.
- `make check-fix-all` — all lint/tidy clean.
- Update PLAN.md: mark 9.8 complete.

#### 9.9 Tribunal Rewrite (`nodes/tribunal/`)

**Major rewrite.** Hearing mode only (review mode removed). Internal tally
and retry. Fire-and-forget child to Clerk on consensus.

**Verdict-Context Contract (revised):**

Same prose-decision principle as the Arbiter. The Tribunal's verdict-context
carries the court's reasoned argument, not structured fields.

```json
{
  "trigger": "hearing",
  "decision": "The court has reviewed law-42 and decided that..."
}
```

Two fields only:

- `trigger` — provenance: `"hearing"` (friction or TTL hearing).
- `decision` — the court's full reasoned argument. References the law
  under review by ID/name naturally within the prose. Describes the
  recommended action (promote, retire, demote, renew) and reasoning.

The old structured fields (`goal`, `action`, `tier`, `applies_to`,
`law_id`, `source_workitem`) are **removed**. The `law_id` is redundant —
it must be mentioned in the decision prose for clerk-forge to act on it.
The Tribunal has no `source_workitem` (hearings are triggered by system
watchers, not by a parent workitem's feedback cycle).

**What gets removed from current code:**

- Review mode entirely (`handleReviewMode`, `assembleReviewEvidence`,
  mode detection logic, review-mode constants, `artefactPetition`).
- Single-output routing to deliberation-gate — absorbed internally.
- Structured verdict-context fields — replaced with prose decision.
- `gateOutput` config — deliberation-gate no longer exists.

**What gets retained:**

- Evidence assembly (hearing mode) — the Tribunal still assembles its
  own evidence. Reads `law-reference` artefact, fetches law from
  Librarian, queries friction and related laws.
- Tier-based question framing (`frameHearingQuestion`).

**What gets added:**

- Internal tally: reuses shared `nodes/internal/tally/` from 9.8a.
- Multi-round retry loop: same pattern as Arbiter.
- Two-outcome branching: consensus → `CreateChildWorkitem()` +
  `Complete()` (fire-and-forget), hung → `RouteToOutput("hung")`.
- Prose verdict-context: synthesize jury reasoning into natural language
  decision.

##### 9.9a Tribunal Core (`nodes/tribunal/main.go`) ✅

**Completed.** Rewrote `nodes/tribunal/main.go` to hearing-only operation.
Removed review-mode branching and deliberation-gate routing. The Tribunal now
assembles hearing evidence, runs internal multi-round juror fan-out using
shared `nodes/internal/tally/`, routes hung outcomes to `hung`, and on
consensus creates a fire-and-forget Clerk child with a prose-only
`verdict-context` (`trigger` + `decision`) before calling `Complete()`.

Full rewrite of `main.go`. Simpler than Arbiter — no Suspend/Resume,
no "resolved" outcome, no two-invocation pattern.

**Types:**

- `tribunalConfig` — `JurySize`, `JurorNode`, `ConsensusStrategy`,
  `MaxRounds`, `ClerkNode`, `HungOutput`.
- `verdictContext` — `Trigger string`, `Decision string`.

**Handler (`handleTribunal`):**

1. Heartbeat.
2. Read `law-reference` artefact → get law ID.
3. Fetch full law from Librarian (`client.GetLaw()`).
4. Query friction data (filtered by law ID).
5. Query related laws.
6. Assemble hearing evidence (Markdown bundle — retained from current
   code with cleanup).
7. Frame question by tier (`frameHearingQuestion` — retained).
8. Deliberation loop (up to `maxRounds`):
   a. `tally.BuildFanOutTasks()`.
   b. `client.FanOut()` → `client.AwaitChildren()`.
   c. `tally.CollectVotes()`.
   d. `tally.Tally()`.
   e. If consensus → break.
   f. If hung and more rounds → `tally.SummariseRound()`.
9. Post-loop outcomes:
   - **Consensus**: synthesize prose decision from jury reasoning,
     store verdict-context on new child workitem,
     `CreateChildWorkitem()` → Clerk, `Complete()` (fire-and-forget).
   - **Hung**: `RouteToOutput("hung")` → hitl-resolve.

**Files:**
- `nodes/tribunal/main.go`

##### 9.9b Tribunal Tests (`nodes/tribunal/main_test.go` + `testutil_test.go`) ✅

**Completed.** Rewrote the Tribunal test suite and spies for the hearing-only
orchestrator. Deleted review-mode expectations, added consensus/hung/retry
coverage, verified prose-only `verdict-context` on the Clerk child, checked
fire-and-forget completion semantics, question framing, evidence assembly,
consensus strategies, config defaults/custom values, and ensured the handler
does not read `petition` artefacts.

Full rewrite of test files. Review-mode tests deleted.

**Test cases:**

1. **Happy path (consensus round 1):** law-reference → evidence →
   fan-out → tally → consensus → Clerk child + Complete.
2. **Hung after max rounds:** → `RouteToOutput("hung")`.
3. **Multi-round retry:** no consensus round 1 → retry → consensus
   round 2.
4. **Verdict-context is prose:** verify `trigger` + `decision` fields
   only.
5. **Fire-and-forget:** Clerk child created, verdict-context stored on
   child, Tribunal Completes immediately (does not Suspend).
6. **Tier-based question framing:** Finding vs Ruling questions and
   allowed outcomes.
7. **Evidence assembly:** law content, friction summary, related laws
   all present in evidence.
8. **Missing law-reference artefact:** error.
9. **GetLaw failure:** error propagation.
10. **Fan-out failure:** error propagation.
11. **Consensus strategies:** reuses shared tally (lighter coverage
    since tally is tested in 9.8a).
12. **Config defaults and custom config.**
13. **No review mode:** verify petition artefact is not read / no mode
    detection.

**Files:**
- `nodes/tribunal/main_test.go`
- `nodes/tribunal/testutil_test.go`

##### 9.9c Tribunal Regression Gate ✅

**Completed.** Ran the Tribunal regression gate successfully. `make check-fix-all`
passed after tightening test helpers to satisfy `unparam`, and `make test-all`
passed after the updated Tribunal suite landed. Phase 9.9 is now complete.

- `make test-all` — all tests pass.
- `make check-fix-all` — all lint/tidy clean.
- Update PLAN.md: mark 9.9 complete, advance status line.

#### 9.10 Rewrite Advocate (`nodes/advocate/`) ⛔ CANCELLED / SUPERSEDED

**Superseded.** The interim "narrow Advocate" plan is cancelled. The node is
not being rewritten into a fire-and-forget Governance Flow gateway. Instead,
the entire cross-flow boundary is redesigned around an operator-provisioned
`embassy` node that exists in every Flow.

What changed:

- Governance Flow is treated as a normal Flow operating in governance mode.
- Cross-flow handoff is no longer modeled as a special Advocate-only path.
- Embassy replaces Advocate entirely for both outbound export and inbound
  intake.
- The receiving Flow publishes `crossFlow.importTypes`; senders target an
  `importType`, not a private node name.
- Naturalisation becomes Embassy's responsibility via local `imported-*`
  attestation stamps derived from verified foreign stamps.

Follow-on work is now planned under Phases 10+:

- Phase 10 -- spec rewrite for Embassy, Governance Flow, Treaties, and
  cross-flow import types.
- Phase 11 -- CRD/proto/operator/SDK foundations for Embassy.
- Phase 12 -- Federation foundations (proto, service schema, publication lifecycle, dispute records).
- Phase 13 -- Embassy node, Federation service, petition-outcome-watcher, and Clerk / authority wiring.
- Phase XX -- cleanup, deletion of `nodes/advocate/`, and regression gates (always last).

#### 9.11 Delete Superseded Nodes ✅

**Completed.** Deleted the superseded node implementations and their tests:
`nodes/deliberation-gate/`, `nodes/tribunal-router/`,
`nodes/judiciary-gate/`, and `nodes/clerk/`. Updated Arbiter's default
Clerk-cycle target from `clerk` to `clerk-forge` so the live orchestration path
no longer points at the deleted legacy node. Verified the replacement
orchestrators still pass targeted tests (`go test ./nodes/arbiter` and
`go test ./nodes/tribunal`).

Post-delete notes:

- No remaining Go references to the deleted package paths were found.
- Advocate still contains legacy `clerk` routing, but that rewrite remains
  intentionally deferred with Phase 9.10.
- Broad documentation/spec cleanup is still deferred to Phase 10.

| Node | Disposition |
|---|---|
| `nodes/deliberation-gate/` | Delete (tally absorbed into Arbiter/Tribunal) |
| `nodes/tribunal-router/` | Delete (replaced by orchestrator-internal routing + Rule Router) |
| `nodes/judiciary-gate/` | Delete (split into Rule Router + law-applicator) |
| `nodes/clerk/` | Delete (replaced by `forge:latest` with configurable prompts + Codification node) |

Detailed execution plan:

1. **Delete the superseded directories outright.** Remove all code and tests in:
   `nodes/deliberation-gate/`, `nodes/tribunal-router/`,
   `nodes/judiciary-gate/`, and `nodes/clerk/`.
2. **Apply direct follow-on runtime cleanup caused by the deletion.** Update
   any still-live node defaults that point at the deleted Clerk node. In
   particular, Arbiter's default `clerkNode` should point to `clerk-forge`,
   not `clerk`.
3. **Update affected tests to match the new defaults.** Arbiter tests should
   expect `clerk-forge` as the default Clerk-cycle entry after the legacy
   `nodes/clerk/` implementation is removed.
4. **Search for stale references after deletion.** Grep for deleted node names
   (`deliberation-gate`, `tribunal-router`, `judiciary-gate`, `nodes/clerk`,
   and legacy `clerk` default usage where it referred to the deleted node) and
   classify the survivors:
   - code/runtime references to fix now,
   - spec/documentation references to leave for Phase 10.
5. **Validate the local replacement paths still hold.** Run at least the
   targeted node tests for the replacement flows (`nodes/arbiter/`,
   `nodes/tribunal/`) after deletion before moving on to build-system cleanup.

Expected non-goals for 9.11:

- **Do not rewrite Advocate.** Phase 9.10 is now cancelled/superseded by the
  Embassy redesign, so stale Advocate behaviour is noted but not redesigned in
  this cleanup phase.
- **Do not do broad spec cleanup here.** Phase 10 handles the large doc/spec
  pass to remove obsolete Deliberation Gate / Tribunal Router /
  Judiciary Gate / Clerk references.
- **Do not do build-system cleanup here.** That belongs to Phase 9.12.

Known risk to handle during execution:

- Arbiter still uses a legacy default of `clerk`; deleting `nodes/clerk/`
  without updating that default would leave a stale runtime path.
- Tribunal is already on the new default (`clerk-forge`), so the main drift is
  on the Arbiter side.
- Advocate still routes to `clerk` today, but because the node is now planned
  for deletion/replacement by Embassy this was recorded as known drift rather
  than folded into 9.11.

#### 9.12 Build System Updates ✅

**Completed.** Updated the build-system metadata to reflect the active node
inventory after Phase 9.11 deletion. At that point the root `Makefile` built
the live judiciary and review nodes (`appraise`, `reviewer`, `refine`,
`advocate`, `arbiter`, `juror`, `codify-smt`, `codification`,
`law-applicator`, `tribunal`) in addition to the previously wired binaries.
`AGENTS.md` now lists the current node inventory. No `go.work` changes were
needed because the nodes already build from the shared `./nodes` module in the
workspace. A later Embassy follow-on phase will replace Advocate in the build
inventory.

Validation:

- `make build` — passed with the updated target set.

Add new nodes to Makefile (build/test/lint targets), `AGENTS.md`, `go.work`.
Remove deleted nodes.

#### 9.13 Regression Gate ✅

**Completed.** Ran the full repository regression gate successfully. The tree is
clean under the required quality gates after the Phase 9 implementation work.

- `make check-fix-all` — passed.
- `make test-all` — passed.

- `make test-all` -- all tests pass
- `make check-fix-all` -- all lint/tidy clean

---
