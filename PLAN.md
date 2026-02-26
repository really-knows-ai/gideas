# Judiciary Architecture Redesign

## Status: In Progress (Phase 4 next)

This document captures the full plan for replacing the monolithic Jury service
and Clerk platform service with a node-based judiciary that mirrors the main
cycle's Forge/Appraise pattern.

This is a living document. We will iterate across multiple sessions.

---

## Motivation

The current judiciary uses two standalone gRPC services (Jury, Clerk) that hide
deliberation and codification behind opaque RPC calls. This violates the
project's own axioms:

- **Make Work Auditable** -- multi-round deliberation inside the Jury service
  produces no Workitem transitions, no friction records, and no per-round
  artefact trail.
- **Make the Cost Visible** -- the cost of each juror invocation and each
  codification dispatch is invisible to the platform.
- **Assume Unreliability** -- monolithic services are harder to observe, retry,
  and govern than individual node assignments.

The redesign externalises deliberation and codification into the flow topology,
making every step a Workitem transition with full auditability and friction
tracking.

---

## Design Principles

1. **The Judiciary mirrors the main cycle.** Clerk drafts petitions (like
   Forge/Refine produces artefacts). Tribunal reviews petitions (like Appraise
   reviews artefacts). Judiciary Gate checks feedback resolution (like Sort).
2. **The Workitem is the state.** No service-level session state. Round counts,
   prior reasoning, and verdict history all live on the Workitem as artefacts.
3. **Fan-out is the parallelism primitive.** Juror deliberation and codification
   both use the existing `FanOut`/`AwaitChildren`/`CollectArtefacts` SDK
   helpers -- the same pattern Appraise uses for Reviewer fan-out.
4. **The Deliberation Gate is generic.** It tallies votes and routes. It does
   not know about tiers, petitions, or law semantics. Caller-specific routing
   lives in dedicated router nodes.
5. **Petitions are human-readable.** The petition artefact is YAML/Markdown, not
   binary proto. A HITL reviewer can read it directly.
6. **Jurors maximise diversity.** A single Juror node image loads different agent
   configurations at fan-out time. The goal is diversity of judicial philosophy
   for the jury size required.

---

## Key Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Jury service | **Remove entirely** | Replaced by Juror nodes + Deliberation Gate |
| Clerk | **Move from platform service to node** | Drafts petitions, fans out to Codification nodes. Receives Workitems, not RPC calls. |
| ConsensusStrategy type | **New `judiciary.proto`** | Keeps judiciary-specific types grouped. Separate from general wire protocol in `common.proto`. |
| Petition format | **YAML/Markdown GovernedArtefact** | Human-readable for HITL. Consistent with how all other artefacts work. |
| Juror personalities | **Single image, config-driven** | One Juror node binary loads agent configurations for diversity. Not N separate deployments. |
| Multi-round deliberation | **Externalised into flow topology** | Deliberation Gate routes `retry` back to the fan-out parent. Round count is a Workitem artefact. No hidden loops. |
| Codification dispatch | **Clerk fans out to Codification nodes** | Replaces the specced-but-unbuilt gRPC Encode dispatch. Uses existing Workitem fan-out. |
| Tribunal role | **Two modes: hearing + petition review** | Hearing: deliberates on existing laws (Librarian trigger). Review: reviews Clerk's drafted petition (like Appraise). |

---

## Topology

### Shared Components

- **Juror nodes** -- single image, configurable judicial philosophy. Receive
  child Workitems with question + evidence + prior-round reasoning. Produce
  verdict + reasoning artefact. Used by both Arbiter and Tribunal.
- **Deliberation Gate** -- generic consensus tally node. Three well-known
  outputs: `consensus`, `retry`, `hung`. Configured with consensus strategy and
  max rounds.
- **Clerk node** -- drafts/revises petition artefacts (YAML/Markdown). Fans out
  to Codification nodes for formal representations. Routes to Tribunal for
  review.
- **Codification nodes** -- each produces a formal representation in its declared
  output format (Rego, SMT-LIB, etc.). Receive child Workitems from Clerk.
- **Judiciary Gate** -- mirrors Sort for the judiciary inner cycle. Checks
  feedback resolution, applies approved petitions via Librarian, or escalates
  by tier.

### Arbiter Path (Deadlock Resolution)

```
Sort (deadlock detected)
  |
  v
Arbiter --[fan-out]--> Juror(s) --[Complete]--+
           <--[collect]---------------------------+
  |
  v
Deliberation Gate
  |-- retry ---------> Arbiter (another round)
  |-- hung ----------> HITL --> Clerk
  |-- consensus -----> Clerk
                         |
                    (inner cycle below)
```

When Sort detects a deadlocked feedback item, it routes to the Arbiter. The
Arbiter assembles evidence (artefact content, feedback history, relevant laws,
friction data), fans out to Juror nodes, and collects their verdicts. The
Deliberation Gate tallies votes. On consensus (or after HITL resolution of a
hung jury), the verdict flows to the Clerk.

### Tribunal Path (Librarian-Triggered Hearing)

```
Librarian trigger (friction threshold / TTL expiry)
  |
  v
Tribunal (hearing mode) --[fan-out]--> Juror(s) --[Complete]--+
                           <--[collect]---------------------------+
  |
  v
Deliberation Gate
  |-- retry ---------> Tribunal (another round)
  |-- hung ----------> Advocate (HITL)
  |-- consensus -----> Tribunal Router
                         |-- Tier 1-2 verdict --> Clerk
                         |-- Tier 3+ -----------> Advocate
```

The Librarian triggers a hearing when a law's friction crosses its tier's
threshold or its review TTL expires. The Tribunal assembles evidence (the law
under review, friction data, related laws), fans out to Jurors, and the
Deliberation Gate tallies. The Tribunal Router reads the tier from the
law-reference artefact and routes accordingly.

### Inner Cycle (Clerk <-> Tribunal Review)

```
Clerk
  |-- drafts/revises petition artefact
  |-- [fan-out] --> Codification nodes --[Complete]--+
  |                  <--[collect]------------------------+
  |-- assembles petition (prose + formal representations)
  |
  v
Tribunal (review mode) --[fan-out]--> Juror(s) --[Complete]--+
                          <--[collect]---------------------------+
  |
  v
Deliberation Gate
  |-- retry ---------> Tribunal (another review round)
  |-- hung ----------> Advocate (HITL)
  |-- consensus -----> Judiciary Gate
                         |-- approved, T1-2, feedback resolved --> apply via Librarian --> done
                         |-- rejected, unresolved feedback ------> Clerk (revise petition)
                         |-- approved, T3 -----------------------> HITL ratification --> done
                         |-- T4-5 -------------------------------> Advocate --> Governance Flow
```

This mirrors the main cycle: Clerk (Forge/Refine) produces a petition, Tribunal
(Appraise) reviews it and may add feedback, Judiciary Gate (Sort) checks
feedback resolution and either applies the petition or sends it back to Clerk
for revision.

### Petition Artefact

A single structured YAML/Markdown GovernedArtefact containing the complete
proposed change set:

```yaml
petition:
  context:
    trigger: "deadlock-resolution" | "friction-hearing" | "ttl-hearing"
    source_workitem: "..."
    verdict: "..."
    justification: "..."
  changes:
    - action: "create"
      tier: 2
      goal: "..."
      applies_to: ["..."]
      representations:
        - type: "text/markdown"
          content: "..."
        - type: "application/rego"
          content: "..."
    - action: "retire"
      law_id: "..."
      justification: "..."
    - action: "demote"
      law_id: "..."
      from_tier: 2
      to_tier: 1
      justification: "..."
```

---

## What Gets Removed

| Component | Current | Disposition |
|---|---|---|
| Jury service (`jury/`) | Standalone gRPC service with deliberation engine, juror personalities | **Removed entirely** |
| Jury proto (`proto/flow/v1/jury.proto`) | `Deliberate` RPC, `ConsensusStrategy`, `JurorJustification` | **Removed** (types relocated to `judiciary.proto`) |
| Clerk service (`platform/clerk/`) | Platform gRPC service, prose drafting | **Removed** (replaced by Clerk node) |
| Clerk proto (`proto/flow/v1/clerk.proto`) | `DraftLaw` RPC | **Removed** (Clerk is a node now) |
| Sidecar Jury proxy (`platform/sidecar/internal/proxy/jury.go`) | Forwards `Deliberate` to Jury service | **Removed** |
| Sidecar Clerk proxy (`platform/sidecar/internal/proxy/clerk.go`) | Forwards `DraftLaw` to Clerk service | **Removed** |
| SDK `client.Deliberate()` (`sdk/go/client.go`) | Convenience wrapper for Jury RPC | **Removed** |
| SDK `client.DraftLaw()` (`sdk/go/client.go`) | Convenience wrapper for Clerk RPC | **Removed** |

## What Gets Added

| Component | Role |
|---|---|
| Juror node (`nodes/juror/`) | Single image, configurable judicial philosophy. Receives evidence, produces verdict. |
| Deliberation Gate (`nodes/deliberation-gate/`) | Generic consensus tally. Outputs: `consensus`, `retry`, `hung`. |
| Clerk node (`nodes/clerk/`) | Drafts/revises petition artefacts. Fans out to Codification nodes. |
| Codification nodes (`nodes/codify-*/`) | Produce formal representations (Rego, SMT-LIB, etc.). |
| Tribunal Router (`nodes/tribunal-router/`) | Tier-aware routing after Tribunal hearing deliberation. |
| Judiciary Gate (`nodes/judiciary-gate/`) | Feedback resolution check, petition application, tier-based escalation. |
| `judiciary.proto` | Shared judiciary types: `ConsensusStrategy`, `JurorJustification`. |

## What Gets Rewritten

| Component | Changes |
|---|---|
| Arbiter (`nodes/arbiter/`) | Replace `Deliberate()`/`DraftLaw()` with Juror fan-out. Route to Deliberation Gate. |
| Tribunal (`nodes/tribunal/`) | Two modes (hearing + review). Replace `Deliberate()`/`DraftLaw()` with Juror fan-out. |
| Advocate (`nodes/advocate/`) | Remove `DraftLaw()` calls. New entry paths from Deliberation Gate and Judiciary Gate. Routes to Clerk. |

---

## Implementation Phases

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

### Phase 4: Delete Jury Service and Clerk Service

#### 4.1 Delete Jury Service

Delete entire `jury/` directory (15 files):

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

Note: juror personality logic and deliberation engine logic should be ported to
the new Juror and Deliberation Gate nodes (Phase 5) before deletion.

#### 4.2 Delete Clerk Platform Service

Delete entire `platform/clerk/` directory (7 files):

- `platform/clerk/cmd/main.go`
- `platform/clerk/internal/service/clerk_server.go`
- `platform/clerk/internal/service/clerk_server_test.go`
- `platform/clerk/go.mod`, `platform/clerk/go.sum`
- `platform/clerk/Dockerfile`
- `platform/clerk/deployment.yaml`

Note: prose drafting logic should be ported to the new Clerk node (Phase 5)
before deletion.

#### 4.3 Build System Cleanup

| File | Changes |
|---|---|
| `Makefile` | Remove `test-clerk`, `build-clerk` targets. Remove `./platform/clerk/...` from lint invocations. Remove `platform/clerk` from tidy target. Add new targets for new nodes. |
| `go.work` | Remove `./platform/clerk`. |
| `platform/go.work` | Remove `./clerk`. |
| `AGENTS.md` | Update repository structure tree. |

---

### Phase 5: New Node Implementations

#### 5.1 Juror Node (`nodes/juror/`)

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

#### 5.2 Deliberation Gate (`nodes/deliberation-gate/`)

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

#### 5.3 Clerk Node (`nodes/clerk/`)

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

#### 5.4 Codification Nodes

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

#### 5.5 Tribunal Router (`nodes/tribunal-router/`)

- Reads verdict artefacts and law-reference artefact (for tier context)
- Routes based on tier and outcome:
  - Tier 1-2 verdict -> Clerk (to draft petition)
  - Tier 2 promote to Tier 3 -> Advocate (HITL ratification)
  - Tier 3+ -> Advocate (petition/appeal)

Files to create:

- `nodes/tribunal-router/main.go`
- `nodes/tribunal-router/main_test.go`
- `nodes/tribunal-router/testutil_test.go`

#### 5.6 Judiciary Gate (`nodes/judiciary-gate/`)

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

### Phase 6: Rewrite Existing Nodes

#### 6.1 Arbiter (`nodes/arbiter/`)

- Remove `client.Deliberate()` and `client.DraftLaw()` calls
- Replace with fan-out to Juror nodes using
  `FanOut()`/`AwaitChildren()`/`CollectArtefacts()`
- Evidence assembly survives (adapted for artefact format passed to Jurors)
- Output goes to Deliberation Gate (not inline verdict processing)
- Rewrite all tests for fan-out pattern

Files affected:

- `nodes/arbiter/main.go` -- major rewrite
- `nodes/arbiter/main_test.go` -- major rewrite
- `nodes/arbiter/testutil_test.go` -- major rewrite (remove Jury/Clerk embeds)

#### 6.2 Tribunal (`nodes/tribunal/`)

- Two modes: **hearing** (initial deliberation on existing law, triggered by
  Librarian) and **review** (reviewing a petition from Clerk, in the inner
  cycle)
- Both modes fan out to Juror nodes
- Hearing mode: assembles law evidence, frames tier-appropriate question, fans
  out to Jurors, routes to Deliberation Gate
- Review mode: reads petition artefact, reviews against governance context,
  adds feedback or approves, routes to Deliberation Gate
- Remove `client.Deliberate()` and `client.DraftLaw()` calls
- Rewrite all tests

Files affected:

- `nodes/tribunal/main.go` -- major rewrite
- `nodes/tribunal/main_test.go` -- major rewrite
- `nodes/tribunal/testutil_test.go` -- major rewrite

#### 6.3 Advocate (`nodes/advocate/`)

- Remove `client.DraftLaw()` calls and `DeliberateResponse` synthetic verdicts
- New entry paths: from Deliberation Gate (`hung`), from Tribunal Router
  (Tier 3+), from Judiciary Gate (Tier 3+ ratification)
- HITL decision routes to Clerk (not directly to Librarian) so the decision
  gets codified as a petition and goes through the normal review cycle
- Rewrite tests

Files affected:

- `nodes/advocate/main.go` -- moderate rewrite
- `nodes/advocate/main_test.go` -- moderate rewrite
- `nodes/advocate/testutil_test.go` -- moderate rewrite

---

### Phase 7: Operator and Platform Updates

#### 7.1 Operator

- **Tribunal detection**: Replace `USE:jury` capability check
  (`operator_server.go:419`) with a new mechanism (e.g. `entry: hearing` label,
  or a dedicated `JUDICIARY:tribunal` capability)
- **CodificationService controller**: Review whether semantics change now that
  Clerk fans out to Codification nodes via Workitems rather than gRPC Encode
  calls. The controller currently manages pod lifecycle but does not wire gRPC
  endpoints -- this may align well with the new model where codification is
  node-based.
- **Hearing workitem creation**: Update if the Tribunal's entry binding changes

Files affected:

- `platform/operator/internal/rpc/operator_server.go` -- Tribunal detection
- `platform/operator/internal/rpc/operator_server_test.go` -- update test
  fixtures
- `platform/operator/internal/controller/codificationservice_controller.go` --
  review
- `platform/operator/cmd/main.go` -- review registrations

#### 7.2 Manifests and Deployment

- New Dockerfiles, `deployment.yaml`, ConfigMaps for each new node
- Update `nodes/haiku-manifests/flow.yaml` with new Judiciary topology
- Update `nodes/haiku-manifests/deployments.yaml` with new node deployments
- Update `nodes/haiku-manifests/configmaps.yaml` with new node configs
- Review Helm chart values for Judiciary configuration changes

---

### Phase 8: Validation and Cleanup

- `go test ./...` -- all tests pass
- `make check-fix` -- all lint clean
- Spec lint (`tools/spec-lint/`) -- all specs clean
- Verify no orphaned references to Jury/Clerk services remain (grep for
  `JuryService`, `ClerkService`, `Deliberate`, `DraftLaw`, `USE:jury`,
  `USE:clerk`, port `50059`, port `50060`)
- Review `specs/05-reference/glossary.md` for completeness
- Update `AGENTS.md` repository structure

---

## Execution Order and Dependencies

```
Phase 1 (specs)         -- no code dependencies, start immediately
Phase 2 (protos)        -- can overlap with Phase 1; finalise after spec decisions
Phase 3 (SDK/sidecar)   -- depends on Phase 2 (new generated types)
Phase 4 (delete old)    -- depends on Phase 3 (consumers updated first)
                        -- port logic to new nodes (Phase 5) BEFORE deleting
Phase 5 (new nodes)     -- depends on Phase 2 + 3 (needs new types + clean SDK)
                        -- can partially overlap with Phase 4 (port then delete)
Phase 6 (rewrite nodes) -- depends on Phase 4 + 5 (old code gone, new nodes exist)
Phase 7 (operator)      -- depends on Phase 6 (new node shapes defined)
Phase 8 (validation)    -- depends on all above
```

Phases 1 and 2 can overlap. Phases 5 and 6 can partially overlap (new nodes do
not depend on old node rewrites). Phases 3 and 4 are sequential.

## Estimated Scope

| Phase | Files Affected | Effort |
|---|---|---|
| 1. Specs | ~22 spec files | High (design authority, must be precise) |
| 2. Protos | ~6 proto/gen files | Low |
| 3. SDK/Sidecar | ~12 files | Medium |
| 4. Delete old | ~22 files deleted, ~5 build files | Low |
| 5. New nodes | ~18 new files (6 nodes x 3) | High |
| 6. Rewrite nodes | ~9 files (3 nodes x 3) | High |
| 7. Operator/manifests | ~8 files | Medium |
| 8. Validation | 0 new, full test suite | Medium |
| **Total** | **~110 files** | |

---

## File Inventory

### Files to Delete (32 files)

**Jury service (`jury/`):**

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
- `jury/go.mod`
- `jury/go.sum`
- `jury/Dockerfile`
- `jury/deployment.yaml`

**Clerk platform service (`platform/clerk/`):**

- `platform/clerk/cmd/main.go`
- `platform/clerk/internal/service/clerk_server.go`
- `platform/clerk/internal/service/clerk_server_test.go`
- `platform/clerk/go.mod`
- `platform/clerk/go.sum`
- `platform/clerk/Dockerfile`
- `platform/clerk/deployment.yaml`

**Proto definitions:**

- `proto/flow/v1/jury.proto`
- `proto/flow/v1/clerk.proto`

**Generated proto code:**

- `gen/flow/v1/jury.pb.go`
- `gen/flow/v1/jury_grpc.pb.go`
- `gen/flow/v1/clerk.pb.go`
- `gen/flow/v1/clerk_grpc.pb.go`

**Sidecar proxies:**

- `platform/sidecar/internal/proxy/jury.go`
- `platform/sidecar/internal/proxy/jury_test.go`
- `platform/sidecar/internal/proxy/clerk.go`
- `platform/sidecar/internal/proxy/clerk_test.go`

### Files to Modify (~57 files)

**Build system:**

- `go.work` -- remove `./platform/clerk`
- `platform/go.work` -- remove `./clerk`
- `Makefile` -- remove clerk targets, add new node targets
- `AGENTS.md` -- update repository structure

**Sidecar:**

- `platform/sidecar/cmd/main.go` -- remove Jury/Clerk env vars, proxy blocks

**SDK:**

- `sdk/go/client.go` -- remove Jury/Clerk fields, Deliberate(), DraftLaw()
- `sdk/go/client_test.go` -- remove Jury/Clerk tests
- `sdk/go/testutil_test.go` -- remove Jury/Clerk client fields
- `sdk/go/child_test.go` -- remove Jury/Clerk registrations
- `sdk/go/fanout_test.go` -- remove Jury/Clerk embeds

**Shared node config:**

- `nodes/internal/nodeconfig/load.go` -- update ParseConsensusStrategy
- `nodes/internal/nodeconfig/load_test.go` -- update tests

**Existing nodes (rewrite):**

- `nodes/arbiter/main.go`
- `nodes/arbiter/main_test.go`
- `nodes/arbiter/testutil_test.go`
- `nodes/tribunal/main.go`
- `nodes/tribunal/main_test.go`
- `nodes/tribunal/testutil_test.go`
- `nodes/advocate/main.go`
- `nodes/advocate/main_test.go`
- `nodes/advocate/testutil_test.go`

**Operator:**

- `platform/operator/internal/rpc/operator_server.go` -- Tribunal detection
- `platform/operator/internal/rpc/operator_server_test.go` -- update fixtures
- `platform/operator/internal/rpc/audit_test.go` -- review "clerk" node refs

**Remaining protos (comment updates):**

- `proto/flow/v1/librarian.proto`
- `proto/flow/v1/operator.proto`

**Spec documents (~22 files):**

- `specs/01-concepts/00-overview.md`
- `specs/01-concepts/01-architecture.md`
- `specs/01-concepts/02-foundry-cycle.md`
- `specs/01-concepts/03-data-model.md`
- `specs/01-concepts/04-governance.md`
- `specs/02-flow/00-overview.md`
- `specs/02-flow/03-nodes-external.md`
- `specs/02-flow/04-system-services.md`
- `specs/02-flow/05-configuration.md`
- `specs/02-flow/06-cross-flow.md`
- `specs/03-node/00-overview.md`
- `specs/03-node/01-sidecar.md`
- `specs/03-node/03-patterns.md`
- `specs/04-sdk/00-overview.md`
- `specs/04-sdk/03-sdk-legal.md`
- `specs/04-sdk/04-sdk-feedback.md`
- `specs/04-sdk/07-sdk-agent.md`
- `specs/04-sdk/08-sdk-hitl.md`
- `specs/05-reference/crds.md`
- `specs/05-reference/grpc-api.md`
- `specs/05-reference/error-catalogue.md`
- `specs/05-reference/glossary.md`

### Files to Create (~21 files)

**New nodes:**

- `nodes/juror/main.go`
- `nodes/juror/main_test.go`
- `nodes/juror/testutil_test.go`
- `nodes/deliberation-gate/main.go`
- `nodes/deliberation-gate/main_test.go`
- `nodes/deliberation-gate/testutil_test.go`
- `nodes/clerk/main.go`
- `nodes/clerk/main_test.go`
- `nodes/clerk/testutil_test.go`
- `nodes/codify-smt/main.go` (reference codification impl)
- `nodes/codify-smt/main_test.go`
- `nodes/codify-smt/testutil_test.go`
- `nodes/tribunal-router/main.go`
- `nodes/tribunal-router/main_test.go`
- `nodes/tribunal-router/testutil_test.go`
- `nodes/judiciary-gate/main.go`
- `nodes/judiciary-gate/main_test.go`
- `nodes/judiciary-gate/testutil_test.go`

**New proto:**

- `proto/flow/v1/judiciary.proto`

---

## Open Items

These are design questions to resolve during implementation:

1. **Tribunal mode detection** -- how does the Tribunal know whether it is in
   hearing mode vs petition review mode? Likely by inspecting which artefacts
   are present on the Workitem (law-reference = hearing, petition = review).

2. **Round count artefact** -- what is the artefact format for tracking
   deliberation round count? Needs to be readable by the Deliberation Gate and
   incrementable on retry.

3. **Juror diversity selection** -- how does the Arbiter/Tribunal select which
   agent configurations to use when fanning out? Config-driven (list of
   personalities) or dynamic (select for diversity based on jury size)?

4. **CodificationService CRD evolution** -- does the existing CRD stay as-is
   (managing pod lifecycle for codification nodes), or does it need new fields
   to describe the node's output format and entry contract?

5. **Tribunal Router vs Judiciary Gate** -- verify these are clearly distinct.
   Tribunal Router handles post-hearing routing (before the inner cycle).
   Judiciary Gate handles post-review routing (after the inner cycle). They
   should not be merged.

6. **Advocate entry paths** -- map all entry paths to the Advocate in the new
   architecture and verify the Advocate's handler can distinguish them.

7. **Petition schema stability** -- the petition YAML/Markdown format needs a
   spec-level schema definition (Phase 1) before Clerk and Tribunal can be
   implemented (Phase 5/6).
