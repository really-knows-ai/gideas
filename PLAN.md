# Judiciary Architecture Redesign

## Status: Phases 1–8 complete. Phase 9 in progress: 9.1 (Rule Router), 9.2 (Facilitator), 9.3 (Law-Applicator), and 9.4 (HITL) complete. Cross-cutting InputArtefacts refactor complete. Next: Phase 9.5 (Clerk-Forge).

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

1. **The Clerk cycle mirrors the main cycle.** The Clerk cycle uses the same
   node images as the main cycle (Sort, Appraise, Refine, Facilitator) with
   different CRD instances and configs. A petition is a governed artefact that
   goes through the same quality process as any other work product.
2. **The Workitem is the state.** No service-level session state. Round counts,
   prior reasoning, and verdict history all live on the Workitem as artefacts.
3. **Fan-out is the parallelism primitive.** Juror deliberation and codification
   both use the existing `FanOut`/`AwaitChildren`/`CollectArtefacts` SDK
   helpers -- the same pattern Appraise uses for Reviewer fan-out.
4. **Routing is configuration, not code.** The Rule Router node evaluates CEL
   expressions against workitem state and routes to the matching output. All
   conditional routing decisions are declared in YAML config, not baked into
   node source code. One image, many CRD instances.
5. **Actions are separate from routing decisions.** Nodes that make routing
   decisions (Rule Routers, Sort) do not mutate state. Nodes that mutate state
   (law-applicator, Clerk-Forge) do not make routing decisions. This keeps each
   node's responsibility singular and auditable.
6. **Petitions are human-readable.** The petition artefact is YAML/Markdown, not
   binary proto. A HITL reviewer can read it directly.
7. **Jurors maximise diversity.** A single Juror node image loads different agent
   configurations at fan-out time. The goal is diversity of judicial philosophy
   for the jury size required.
8. **Small nodes are cheap.** Each FoundryNode CRD is a lightweight pod. The
   judiciary is a low-volume path (deadlocks and hearings, not every workitem).
   The cost of an extra Operator reconciliation hop (~100-500ms) is dwarfed by
   LLM inference time in jurors and human decision time in HITL nodes. Prefer
   many small, focused nodes over fewer monolithic ones.
9. **Orchestrators own their lifecycle.** The Arbiter and Tribunal are self-
   contained orchestrators that fan out to jurors, tally verdicts, and handle
   multi-round retry internally. They do not delegate tally or routing to
   external nodes. When they need a dependent process (e.g. a Clerk cycle),
   they use the Suspend/Resume platform primitive to park the workitem.
10. **HITL nodes are generic and config-driven.** A single HITL node image
    (`hitl:latest`) derives its behaviour from CRD configuration: outputs
    become user choices, `WRITE:feedback` enables feedback, exit-node config
    enables cancellation. One image, many CRD instances.

---

## Key Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Jury service | **Remove entirely** | Replaced by Juror nodes + orchestrator-internal tally |
| Clerk | **Move from platform service to node** | `clerk-forge` drafts petitions, fans out to Codification nodes. Receives Workitems, not RPC calls. |
| ConsensusStrategy type | **New `judiciary.proto`** | Keeps judiciary-specific types grouped. Separate from general wire protocol in `common.proto`. |
| Petition format | **YAML/Markdown GovernedArtefact** | Human-readable for HITL. Consistent with how all other artefacts work. |
| Juror personalities | **Single image, config-driven** | One Juror node binary loads agent configurations for diversity. Not N separate deployments. |
| Multi-round deliberation | **Internal to orchestrators** | Arbiter and Tribunal handle fan-out, tally, and retry internally. No external Tally or routing nodes needed for deliberation. Round count is a Workitem artefact. |
| Codification dispatch | **Clerk-Forge fans out to Codification nodes** | Replaces the specced-but-unbuilt gRPC Encode dispatch. Uses existing Workitem fan-out. |
| Hearing triggers | **Two watcher nodes, not Librarian** | Friction Watcher subscribes to Event Bus friction channel. TTL Watcher polls Librarian for expired laws. Both are entry-bound nodes that create hearing Workitems via `CreateWorkitem`. No judiciary-specific logic in the Operator or Librarian. |
| `CreateHearingWorkitem` RPC | **Remove entirely** | Replaced by generic `CreateWorkitem` called by watcher nodes. The Operator has no judiciary-specific knowledge. |
| Routing logic | **Rule Router with CEL** | Generic node evaluates CEL expressions against workitem state. One image, many CRD instances with different rule configs. Uses `github.com/google/cel-go` (Kubernetes-native expression language). |
| Petition review (T1-2) | **Reuse Appraise image** | Same Appraise node image as the main cycle, configured for petition review. Automated multi-agent review produces feedback. |
| Petition review (T3-5) | **Generic HITL node** | Single `hitl:latest` image configured for petition review. Human can approve, provide feedback, or cancel. |
| Advocate role | **Gateway, fire-and-forget** | Advocate exclusively submits approved T4-5 petitions to the Governance Flow and Completes. Not a HITL node. No suspension. |
| Law application | **Dedicated law-applicator node** | Applies approved petitions via Librarian (`WriteLaw`/`RetireLaw`). Separated from routing decisions per Principle 5. |
| Deadlock lifecycle | **Facilitator + Arbiter child** | Sort routes deadlocks to the Facilitator. The Facilitator assembles an evidence bundle, creates a child workitem for the Arbiter, and Suspends. The Arbiter deliberates and Completes. The Facilitator resumes and routes back to Sort. |
| Dependent processes | **Suspend/Resume platform primitive** | When a node needs to wait for a long-running dependent process (e.g. Arbiter waiting for Clerk cycle to complete a law change), it creates a child workitem, calls `Suspend(condition)`, and gets re-dispatched when the condition is met. CEL-based conditions. Timeouts. |
| Completion reasons | **`CompletionReason` enum on `Complete()`** | Distinguishes success from cancellation. Stored on workitem status, filterable in CEL. Audit log records the reason. No separate `Cancelled` phase. |
| Clerk cycle structure | **Mirrors main cycle** | The Clerk cycle uses the same node images (Sort, Appraise, Refine, Facilitator) as CRD instances with different configs. `clerk-forge` and `clerk-refine` extend Forge/Refine with codification fan-out. |
| HITL node design | **Generic, config-driven** | One `hitl:latest` image. Outputs become user choices. `WRITE:feedback` capability enables feedback. Exit-node config enables cancellation. Multiple CRD instances for different use cases. |
| Hung jury resolution | **hitl-resolve (generic HITL instance)** | Hung juries route to a HITL node configured for resolution. Human can provide a resolution (routes back to orchestrator) or cancel (exit). Separate CRD instances per orchestrator. |
| Tribunal review mode | **Removed** | Tribunal retains hearing mode only. Petition review is handled by the Clerk cycle's Sort/Appraise/Refine loop. |
| Tally node | **Eliminated** | Vote tallying is absorbed into the Arbiter and Tribunal orchestrators. No external Tally node. |
| Deliberation Gate | **Eliminated** | Replaced by orchestrator-internal tally + direct routing. |
| Tribunal Router | **Eliminated** | Replaced by orchestrator-internal routing and Rule Router instances. |
| Judiciary Gate | **Eliminated** | Split into Rule Router instances + law-applicator. |
| Adjudicator | **Eliminated** | Replaced by generic HITL node instances (`hitl-resolve`). |

---

## Topology

### Node Types

The judiciary uses six categories of node, each with a clear responsibility
boundary:

- **Lifecycle nodes** -- own a dependent process. Assemble context, create
  child workitems, suspend, resume, route the result. (Facilitator.)
- **Orchestrator nodes** -- conduct deliberation. Fan out to Jurors, tally
  verdicts, handle multi-round retry internally. May suspend for dependent
  Clerk cycles. (Arbiter, Tribunal.)
- **Computation nodes** -- read artefacts, perform domain logic, write result
  artefacts, route to a single output. No routing decisions. (Clerk-Forge,
  Clerk-Refine, Juror, Codification nodes.)
- **Rule Router nodes** -- read workitem state, evaluate CEL rules, route to
  the matching output. No state mutation. One image (`rule-router:latest`),
  many CRD instances with different configs. (clerk-done-router, hitl-gate.)
- **Action nodes** -- perform a single side-effecting operation and complete.
  (law-applicator, Advocate.)
- **HITL nodes** -- present work to a human, collect a decision. Behaviour
  is derived from CRD config: outputs become choices, `WRITE:feedback`
  enables feedback, exit-node enables cancellation. One image
  (`hitl:latest`), many CRD instances. (hitl-appraise, arbiter-hitl-resolve,
  tribunal-hitl-resolve.)

### Rule Router

The Rule Router is a generic, config-driven routing node. It evaluates an
ordered list of [CEL](https://github.com/google/cel-spec) expressions against
the current workitem state. The first rule whose expression evaluates to `true`
determines the output. A default output catches anything that falls through.

**Config shape** (`node-config.yaml`):

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

**CEL environment** -- the node lazily loads workitem data as rules reference
it. Available variables:

| Variable | Type | Source |
|---|---|---|
| `artefacts` | `list<string>` | `ListArtefacts` -- governed artefact names on the workitem |
| `metadata` | `map<string,string>` | Workitem metadata |
| `feedback.unresolved_count` | `int` | Count of unresolved feedback items |
| `feedback.has_deadlocked` | `bool` | Any feedback in `deadlocked` state |
| `feedback.total_count` | `int` | Total feedback items |
| `stamps` | `map<string,list<string>>` | Per-artefact stamp names |
| `artefact_content(name)` | `string` | Lazy function -- reads artefact content |
| `children` | `list<object>` | Child workitems (phase, node, completion_reason) |

The node parses rules at startup, determines which variables each expression
references, and only fetches the data needed for the rules that are configured.
Capabilities on the FoundryNode CRD must cover the superset of what the rules
might access.

**Image**: `rule-router:latest` (single image, all instances).

### Generic HITL Node

The HITL node is a generic, config-driven human-in-the-loop node. A single
image (`hitl:latest`) derives its behaviour entirely from CRD configuration:

- **Outputs** declared on the FoundryNode CRD are presented to the human as
  action choices (e.g., "approve", "resolution").
- **`WRITE:feedback` capability** enables a "provide feedback" action that
  writes feedback to the artefact and routes to the configured feedback output.
- **Exit-node configuration** enables a "cancel" action that calls
  `Complete(WithReason(cancelled))`, terminating the process.

**CRD instance examples:**

| Instance | Outputs | Capabilities | Exit? | Use Case |
|---|---|---|---|---|
| `hitl-appraise` | `approved` | `WRITE:feedback`, `READ:artefact/petition` | Yes | T3-5 petition review |
| `arbiter-hitl-resolve` | `resolution` | `READ:artefact/evidence-bundle` | Yes | Arbiter hung jury |
| `tribunal-hitl-resolve` | `resolution` | `READ:artefact/evidence-bundle` | Yes | Tribunal hung jury |

### Suspend/Resume Platform Primitive

Suspend/Resume is a core Operator capability that allows nodes to park a
workitem and be re-dispatched when a condition is met. This is essential for
the Facilitator (waiting for Arbiter child) and Arbiter (waiting for Clerk
cycle child).

**SDK surface:**

```go
// Suspend parks the workitem. The handler should return nil after this.
// With no options: suspended until explicit Resume(), subject to default timeout.
// With a condition: Operator evaluates the CEL condition and resumes automatically.
func (c *Client) Suspend(ctx context.Context, opts ...SuspendOption) error

// Resume explicitly resumes a suspended workitem by ID.
func (c *Client) Resume(ctx context.Context, workitemID string) error

func WithCondition(cel string) SuspendOption  // CEL expression for auto-resume
func WithTimeout(d time.Duration) SuspendOption  // fails if exceeded
```

**Timeout policy** (Flow CRD `spec.suspension`):

| Field | Default | Description |
|---|---|---|
| `maxSuspendTimeout` | `336h` (2 weeks) | Hard cap. Operator rejects suspensions exceeding this. |
| `defaultSuspendTimeout` | (defaults to `maxSuspendTimeout`) | Applied when `WithTimeout` is not specified. |

Every suspension has a timeout. If the condition is not met before the
deadline, the workitem **fails** (not resumes). There are no truly indefinite
suspensions.

**Re-dispatch:** On resume, the workitem is re-dispatched to the same node
type that suspended it.

**Proto shape:**

```protobuf
message SubmitResultRequest {
    oneof action {
        CompleteAction complete = 1;
        RouteAction route = 2;
        SuspendAction suspend = 3;  // NEW
    }
}

message SuspendAction {
    string condition = 1;            // CEL expression, empty = manual Resume() required
    google.protobuf.Duration timeout = 2;  // optional, capped by maxSuspendTimeout
}

// Separate RPC for explicit resume.
rpc ResumeWorkitem(ResumeWorkitemRequest) returns (ResumeWorkitemResponse);
```

### CompletionReason

`Complete()` accepts an optional `CompletionReason` to distinguish success
from cancellation. The reason is stored on the workitem status and is
filterable in CEL conditions. The audit log records the reason.

```protobuf
enum CompletionReason {
    COMPLETION_REASON_UNSPECIFIED = 0;  // success (default, backward compatible)
    COMPLETION_REASON_CANCELLED = 1;    // HITL cancelled
}

message CompleteAction {
    CompletionReason reason = 1;
}
```

**SDK:** `client.Complete(ctx, flow.WithReason(flow.CompletionReasonCancelled))`

**Cancellation propagation:** When a parent detects a cancelled child on
resume, the flow implementation decides the policy. In the haiku flow,
cancellation propagates up the chain via `Complete(WithReason(cancelled))`.

### Main Cycle (with Judiciary Entry)

```
[forge] --> [sort] --> needs quench -----> [quench] --> [sort]
                   |-> needs refinement -> [refine] --> [sort]
                   |-> needs appraise ---> [appraise] --> [sort]
                   |-> all done ---------> Complete()
                   |-> deadlocked -------> [facilitator]
                                              |
                                              |-- assembles evidence bundle
                                              |-- CreateChild --> [arbiter]
                                              |-- Suspend(children)
                                              |-- ... arbiter completes ...
                                              |-- Resume
                                              |-> resolved --> [sort]
```

When Sort detects deadlocked feedback, it routes to the Facilitator. The
Facilitator gathers evidence (feedback history, artefact content, relevant
laws, friction data), packages it into a bundle artefact, creates a child
workitem for the Arbiter, and Suspends. When the Arbiter child completes, the
Facilitator resumes and routes `resolved` back to Sort.

### Arbiter Path (Deadlock Resolution)

```
[facilitator] -- CreateChild --> [arbiter] --[fan-out]--> [juror](s)
                                    |           (internal tally, internal retry)
                                    |
                                    |-- resolved ----------> Complete()
                                    |                        (no law change needed)
                                    |
                                    |-- consensus ----------> CreateChild --> [clerk]
                                    |                         Suspend(children)
                                    |                         ... clerk cycle completes ...
                                    |                         Resume --> Complete()
                                    |
                                    |-- hung ---------------> [hitl-resolve]
                                                                |-> resolution --> [arbiter]
                                                                |-> hitl cancel -> Complete(cancelled)
```

The Arbiter receives a child workitem with a pre-assembled evidence bundle
from the Facilitator. It fans out to Juror nodes, tallies votes, and handles
multi-round retry internally. Three outcomes:

1. **Resolved** -- the jury settles the dispute within existing law. The
   Arbiter Completes. The Facilitator resumes and routes back to Sort.
2. **Consensus** -- the jury decides a law change is needed. The Arbiter
   creates a child workitem for the Clerk cycle (with `verdict-context`
   artefact) and Suspends. When the Clerk cycle completes (law applied),
   the Arbiter resumes and Completes. The Facilitator then resumes and
   routes back to Sort.
3. **Hung** -- the jury cannot reach consensus after max rounds. The Arbiter
   routes to hitl-resolve. The human can provide a resolution (back to
   Arbiter for re-deliberation) or cancel.

**Suspension chain** (deepest path): Facilitator suspends for Arbiter.
Arbiter suspends for Clerk. Clerk cycle completes. Arbiter resumes and
completes. Facilitator resumes and routes to Sort.

### Tribunal Path (Watcher-Triggered Hearing)

```
[friction-watcher] --+
                     +--> [tribunal] --[fan-out]--> [juror](s)
[ttl-watcher] -------+        |          (internal tally, internal retry)
                               |
                               |-- consensus --> CreateChild --> [clerk]
                               |                 Complete()
                               |                 (fire-and-forget)
                               |
                               |-- hung -------> [hitl-resolve]
                                                    |-> resolution --> [tribunal]
                                                    |-> hitl cancel -> Complete(cancelled)
```

The Friction Watcher subscribes to the Event Bus friction channel and creates
a hearing Workitem when a law's friction crosses its tier's threshold. The TTL
Watcher periodically polls the Librarian and creates a hearing Workitem when a
law's age exceeds its tier's review TTL. Both are entry-bound nodes that call
`CreateWorkitem`, store a `law-reference` artefact, and route to the Tribunal.

The Tribunal fans out to Jurors, tallies, and handles retry internally. On
consensus, it creates a child workitem for the Clerk cycle and **Completes**
(fire-and-forget). The hearing workitem has no parent to report back to, so
no suspension is needed. On hung jury, it routes to hitl-resolve.

### Clerk Cycle (Petition Drafting and Approval)

The Clerk cycle **mirrors the main cycle**. It uses the same node images
(Sort, Appraise, Refine, Facilitator) as different CRD instances with
different output configurations. A petition is a governed artefact that goes
through the same quality process as any other work product.

```
[clerk-forge] --[fan-out]--> [codifiers]
      |
      v
[clerk-sort]
      |-> needs refinement -> [clerk-refine] --[fan-out]--> [codifiers]
      |                                  |
      |                                  v
      |                            [clerk-sort]
      |-> needs appraise ---> [clerk-appraise] --[fan-out]--> [reviewers]
      |                                  |
      |                                  v
      |                            [clerk-sort]
      |-> has feedback ---------> [clerk-sort] (loop)
      |-> deadlocked -----------> [clerk-facilitator]
      |                              |-- CreateChild --> [arbiter]
      |                              |-- Suspend(children)
      |                              |-- Resume --> resolved --> [clerk-sort]
      |-> done ------------------> [clerk-done-router] (Rule Router)
                                      |
                                      |-- tier 1-2 --> [law-applicator] --> Complete()
                                      |
                                      |-- tier 3-5 --> [hitl-appraise]
                                                          |-> cancel --> Complete(cancelled)
                                                          |-> approved -->
                                                              [hitl-gate] (Rule Router)
                                                                 |-- tier 3 --> [law-applicator] --> Complete()
                                                                 |-- tier 4-5 --> [advocate] --> Complete()
```

**Key design points:**

1. **Same images, different configs.** `clerk-sort` is `sort:latest` with
   different output wiring. `clerk-appraise` is `appraise:latest` configured
   for petition review. `clerk-refine` is `clerk-refine:latest` for petition
   revision. `clerk-facilitator` is `facilitator:latest`.

2. **clerk-forge** extends the Forge pattern with codification fan-out. It
   drafts the petition prose and fans out to Codification nodes for formal
   representations (Rego, SMT-LIB, etc.). It is its own image
   (`clerk-forge:latest`), not `forge:latest`, because of the codification
   dependency.

3. **clerk-refine** extends the Refine pattern with codification fan-out.
   When revising a petition based on feedback, it also updates the formal
   representations. Its own image (`clerk-refine:latest`).

4. **clerk-sort** handles petition triage the same way Sort handles haiku
   triage. When all feedback is resolved and the petition is ready, Sort
   routes to "done" which goes to the `clerk-done-router`.

5. **Tier-based routing** -- the `clerk-done-router` (Rule Router) reads
   petition metadata to determine the tier(s) of the laws being changed.
   T1-2 go directly to law-applicator. T3-5 go to hitl-appraise.

6. **Deadlocked petition feedback** goes through the same Facilitator →
   Arbiter path as the main cycle. The Facilitator and Arbiter are generic --
   they handle any governed artefact's deadlocked feedback.

7. **Advocate is fire-and-forget.** Receives approved T4-5 petitions, submits
   to the Governance Flow, Completes.

8. **HITL cancel propagation.** When hitl-appraise cancels, it calls
   `Complete(WithReason(cancelled))`. The child workitem terminates. If the
   parent was an Arbiter that suspended for this Clerk cycle, the Arbiter
   detects the cancellation on resume and propagates it up via
   `Complete(WithReason(cancelled))`.

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
| `CreateHearingWorkitem` RPC (`operator.proto`) | Judiciary-specific Operator RPC for hearing creation | **Removed** (watcher nodes use generic `CreateWorkitem`) |
| Librarian hearing triggers (`hearing_trigger.go`) | Friction subscription + TTL scanner in Librarian | **Removed** (replaced by Friction Watcher and TTL Watcher nodes) |

## What Gets Added

### New Node Images

| Component | Role |
|---|---|
| Facilitator (`nodes/facilitator/`) | Lifecycle owner for deadlock resolution. Assembles evidence bundle, creates child for Arbiter, suspends, resumes, routes back to Sort. Generic -- handles any governed artefact's deadlocked feedback. |
| Clerk-Forge (`nodes/clerk-forge/`) | Petition drafting + codification fan-out. Extends Forge pattern. Drafts petition prose, fans out to Codification nodes for formal representations. |
| Clerk-Refine (`nodes/clerk-refine/`) | Petition revision + codification fan-out. Extends Refine pattern. Revises petition based on feedback, updates formal representations. |
| Juror node (`nodes/juror/`) | Single image, configurable judicial philosophy. Receives evidence, produces verdict. |
| Rule Router (`nodes/rule-router/`) | Generic CEL-based routing node. One image, many CRD instances. |
| Generic HITL (`nodes/hitl/`) | Generic config-driven HITL node. One image, many CRD instances. Behaviour derived from outputs, capabilities, and exit-node config. |
| Law-applicator (`nodes/law-applicator/`) | Applies approved petitions via Librarian (`WriteLaw`/`RetireLaw`). |
| Codification nodes (`nodes/codify-*/`) | Produce formal representations (Rego, SMT-LIB, etc.). |
| Friction Watcher (`nodes/friction-watcher/`) | Subscribes to Event Bus friction channel. Creates hearing Workitems when thresholds cross. |
| TTL Watcher (`nodes/ttl-watcher/`) | Polls Librarian for laws exceeding review TTL. Creates hearing Workitems on expiry. |
| `judiciary.proto` | Shared judiciary types: `ConsensusStrategy`, `JurorJustification`. |

### New Platform Capabilities

| Component | Role |
|---|---|
| Suspend/Resume | New `SuspendAction` on `SubmitResult`, `ResumeWorkitem` RPC. Operator `Suspended` phase, CEL condition evaluation, timeout enforcement. SDK `Suspend()`/`Resume()` methods. |
| CompletionReason | `CompletionReason` enum on `CompleteAction`. Distinguishes success from cancellation. Stored on workitem status. |
| Flow CRD suspension config | `spec.suspension.maxSuspendTimeout`, `spec.suspension.defaultSuspendTimeout`. |

### CRD-Only Instances (no new image, just FoundryNode CRDs with config)

| CRD Name | Image | Purpose |
|---|---|---|
| `clerk-sort` | `sort:latest` | Petition feedback triage (mirrors main cycle Sort) |
| `clerk-appraise` | `appraise:latest` | Automated petition review (mirrors main cycle Appraise) |
| `clerk-facilitator` | `facilitator:latest` | Petition feedback deadlock lifecycle (mirrors main cycle Facilitator) |
| `clerk-done-router` | `rule-router:latest` | Post-approval tier routing: T1-2 vs T3-5 |
| `hitl-gate` | `rule-router:latest` | Post-HITL routing: T3 approved/T4-5 approved |
| `hitl-appraise` | `hitl:latest` | T3-5 petition HITL review. Exit node. |
| `arbiter-hitl-resolve` | `hitl:latest` | Arbiter hung jury HITL resolution. Exit node. |
| `tribunal-hitl-resolve` | `hitl:latest` | Tribunal hung jury HITL resolution. Exit node. |

## What Gets Rewritten

| Component | Changes |
|---|---|
| Arbiter (`nodes/arbiter/`) | **Major rewrite.** No longer assembles evidence (Facilitator does). Receives pre-assembled bundle as child workitem. Internal tally (no external Tally/delib-router). Suspends for Clerk child on consensus path. Three outcomes: resolved (Complete), consensus (Suspend), hung (route to hitl-resolve). |
| Tribunal (`nodes/tribunal/`) | **Major rewrite.** Hearing mode only (review mode removed). Internal tally and retry. Fire-and-forget child to Clerk on consensus. Routes to hitl-resolve on hung. |
| Advocate (`nodes/advocate/`) | **Major narrowing.** Fire-and-forget T4-5 gateway. Read petition, submit to Governance Flow, Complete(). Remove all HITL, escalation types, queue logic. |
| Clerk node (`nodes/clerk/`) | **Renamed to clerk-forge.** Existing petition drafting logic preserved. Codification fan-out pattern extended from Forge. |
| Deliberation Gate (`nodes/deliberation-gate/`) | **Deleted.** Tally logic absorbed into Arbiter/Tribunal. |
| Tribunal Router (`nodes/tribunal-router/`) | **Deleted.** Routing replaced by orchestrator-internal logic and Rule Router instances. |
| Judiciary Gate (`nodes/judiciary-gate/`) | **Deleted.** Routing moved to Rule Router instances. Law application moved to law-applicator. |

---

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
> deleted in Phase 9.10. Code retained for reference only.

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

> **Superseded by architecture revision.** The Clerk node will be renamed/
> replaced by `nodes/clerk-forge/` in Phase 9.6. The existing petition
> drafting + codification fan-out logic is preserved and extended in the new
> Clerk-Forge node.

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
> be deleted in Phase 9.10.

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
> node will be deleted in Phase 9.10. The `applyPetition` logic is extracted
> into `nodes/law-applicator/` in Phase 9.8.

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
> again in Phase 9.2: no evidence assembly (Facilitator does it), receives
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
> again in Phase 9.3: hearing mode only (review mode removed), internal
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

> **Superseded by architecture revision.** The Advocate will be rewritten
> again in Phase 9.9: fire-and-forget T4-5 gateway only. Read petition
> artefact, submit to Governance Flow, Complete(). Remove all HITL logic,
> escalation types, queue handling.

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

> **NOTE**: Phase 7.2b-d depend on Phase 8 (Suspend/Resume) and Phase 9 (new
> node implementations). GovernedArtefact CRDs (7.2a) are complete; FoundryNode
> CRDs (7.2b) are deferred until the node images exist. The topology has been
> finalized — see the Node Inventory section for the complete 24-CRD list.

##### 7.2a Judiciary GovernedArtefact CRDs ✅

Added 14 judiciary GovernedArtefact CRDs to `nodes/haiku-manifests/flow.yaml`.
All have `stamps: []`, `namespace: default`. The `petition` GovernedArtefact
already existed, so 14 new (not 15).

Also removed the redundant `app.kubernetes.io/part-of: haiku-flow` label from
all resources across all four manifest files (`flow.yaml`, `configmaps.yaml`,
`deployments.yaml`, `workitem.yaml`). Namespace scoping already binds
resources to their flow — the label was redundant information.

##### 7.2b Judiciary FoundryNode CRDs

Depends on Phase 8 (Suspend/Resume) and Phase 9 (new node implementations).
The FoundryNode CRD definitions will be written once the node images exist.

**Node Inventory (24 CRD instances, 18 distinct images)**:

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
| `clerk-forge` | clerk-forge:latest | Computation | Petition drafting + codification fan-out |
| `clerk-sort` | sort:latest | Triage | Petition feedback triage (same image as Sort) |
| `clerk-appraise` | appraise:latest | Review | Automated petition review (same image as Appraise) |
| `clerk-refine` | clerk-refine:latest | Revision | Petition revision + codification fan-out |
| `clerk-facilitator` | facilitator:latest | Lifecycle | Petition deadlock lifecycle (same image as Facilitator) |
| **Clerk Exit Routing** | | | |
| `clerk-done-router` | rule-router:latest | Rule Router | Post-approval tier routing: T1-2 vs T3-5 |
| `hitl-gate` | rule-router:latest | Rule Router | Post-HITL routing: T3 approved vs T4-5 approved |
| **HITL** | | | |
| `hitl-appraise` | hitl:latest | HITL | T3-5 petition HITL review. Exit node. |
| `arbiter-hitl-resolve` | hitl:latest | HITL | Arbiter hung jury HITL resolution. Exit node. |
| `tribunal-hitl-resolve` | hitl:latest | HITL | Tribunal hung jury HITL resolution. Exit node. |
| **Terminal** | | | |
| `law-applicator` | law-applicator:latest | Action | Applies petitions via Librarian |
| `advocate` | advocate:latest | Gateway | Submits approved T4-5 petitions to Governance Flow |
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
finalised node inventory from 7.2b.

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
| `clerk-forge` | `codificationNodes` (list) |
| `clerk-refine` | `codificationNodes` (list) |
| `clerk-done-router` | CEL rules (tier-based routing: T1-2 vs T3-5) |
| `hitl-gate` | CEL rules (T3 approved → law-applicator / T4-5 → advocate) |
| `codify-smt` | `outputFormat` |
| `juror` | (personality loaded from config) |
| `hitl-appraise` | (CRD-driven: outputs, capabilities, exit-node config) |
| `arbiter-hitl-resolve` | (CRD-driven: outputs, capabilities, exit-node config) |
| `tribunal-hitl-resolve` | (CRD-driven: outputs, capabilities, exit-node config) |
| `law-applicator` | (minimal -- reads petition artefact) |
| `advocate` | (Governance Flow connection config) |

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

#### 9.5 Clerk-Forge Node (`nodes/clerk-forge/`)

Extends the Forge pattern with codification fan-out. Drafts petition prose,
fans out to Codification nodes for formal representations. Its own image
because of the codification dependency.

Files to create:
- `nodes/clerk-forge/main.go`
- `nodes/clerk-forge/main_test.go`
- `nodes/clerk-forge/testutil_test.go`

#### 9.6 Clerk-Refine Node (`nodes/clerk-refine/`)

Extends the Refine pattern with codification fan-out. Revises petition based
on feedback, updates formal representations. Its own image.

Files to create:
- `nodes/clerk-refine/main.go`
- `nodes/clerk-refine/main_test.go`
- `nodes/clerk-refine/testutil_test.go`

#### 9.7 Arbiter Rewrite (`nodes/arbiter/`)

**Major rewrite.** The Arbiter no longer assembles evidence (the Facilitator
does). It receives a pre-assembled evidence bundle as a child workitem.
Internal tally and retry (no external Tally or delib-router). May suspend
for a Clerk child.

Handler logic:
1. **First invocation** (no completed Clerk children):
   - Read evidence bundle artefact
   - Fan out to Jurors (internal, with `AwaitChildren`)
   - Tally verdicts (internal consensus logic)
   - If retry: re-fan-out (internal loop)
   - If resolved: `Complete()` (dispute settled within existing law)
   - If consensus (law needs changing): `CreateChildWorkitem()` → Clerk
     with `verdict-context` artefact,
     `Suspend(WithCondition("children.all(c, c.phase == \"Completed\")"))`
   - If hung: `RouteToOutput("hung")` → hitl-resolve
2. **Post-resume** (Clerk child completed):
   - Check child's `CompletionReason`
   - If cancelled: `Complete(WithReason(cancelled))`
   - If success: `Complete()` (law was applied, Facilitator handles the rest)

Config:
```yaml
jurySize: 5
jurorNode: juror
consensusStrategy: SIMPLE_MAJORITY
maxRounds: 3
```

Files affected:
- `nodes/arbiter/main.go` -- major rewrite
- `nodes/arbiter/main_test.go` -- major rewrite
- `nodes/arbiter/testutil_test.go` -- major rewrite

#### 9.8 Tribunal Rewrite (`nodes/tribunal/`)

**Major rewrite.** Hearing mode only (review mode removed). Internal tally
and retry. Fire-and-forget child to Clerk on consensus.

Handler logic:
1. Read `law-reference` artefact, fetch law, friction, related laws
2. Assemble evidence, frame question (promote/retire/demote)
3. Fan out to Jurors, tally (internal, with `AwaitChildren`)
4. If retry: re-fan-out (internal loop)
5. If consensus: `CreateChildWorkitem()` → Clerk with `verdict-context`,
   then `Complete()` (fire-and-forget)
6. If hung: `RouteToOutput("hung")` → hitl-resolve

Config:
```yaml
jurySize: 5
jurorNode: juror
consensusStrategy: SIMPLE_MAJORITY
maxRounds: 3
```

Files affected:
- `nodes/tribunal/main.go` -- major rewrite
- `nodes/tribunal/main_test.go` -- major rewrite
- `nodes/tribunal/testutil_test.go` -- major rewrite

#### 9.9 Rewrite Advocate (`nodes/advocate/`)

Major narrowing. Fire-and-forget T4-5 gateway. Read petition artefact, submit
to Governance Flow, Complete(). Remove all HITL logic, escalation types, queue
handling.

Files affected:
- `nodes/advocate/main.go` -- major rewrite
- `nodes/advocate/main_test.go` -- major rewrite
- `nodes/advocate/testutil_test.go` -- major rewrite

#### 9.10 Delete Superseded Nodes

| Node | Disposition |
|---|---|
| `nodes/deliberation-gate/` | Delete (tally absorbed into Arbiter/Tribunal) |
| `nodes/tribunal-router/` | Delete (replaced by orchestrator-internal routing + Rule Router) |
| `nodes/judiciary-gate/` | Delete (split into Rule Router + law-applicator) |
| `nodes/clerk/` | Rename/replace with `nodes/clerk-forge/` |

#### 9.11 Build System Updates

Add new nodes to Makefile (build/test/lint targets), `AGENTS.md`, `go.work`.
Remove deleted nodes.

#### 9.12 Regression Gate

- `make test-all` -- all tests pass
- `make check-fix-all` -- all lint/tidy clean

---

### Phase 10: Specification Updates (Architecture Revision)

Update all spec documents to reflect the revised architecture. This is a
second pass over the specs (Phase 1 was the first pass for the original
Jury/Clerk removal).

**Key spec changes:**

| Area | Changes |
|---|---|
| Facilitator | New node. Lifecycle owner for deadlock resolution. Evidence assembly, child creation, Suspend/Resume. |
| Arbiter | No longer assembles evidence. Receives bundle as child. Internal tally. Suspend for Clerk child. |
| Tribunal | Hearing mode only (review mode removed). Internal tally. Fire-and-forget to Clerk. |
| Advocate | Fire-and-forget T4-5 gateway. Remove all HITL references. |
| Clerk cycle | Mirrors main cycle. clerk-forge, clerk-sort, clerk-appraise, clerk-refine, clerk-facilitator. |
| Suspend/Resume | New platform primitive. Workitem lifecycle, SDK methods, Operator behaviour. |
| CompletionReason | New. Cancellation propagation, audit log. |
| Generic HITL | Config-driven. One image, outputs as choices, capabilities, exit-node. |
| Deliberation Gate / Tally / delib-router | Remove all references. Tally absorbed into orchestrators. |
| Tribunal Router | Remove all references. |
| Judiciary Gate | Remove all references. Split into Rule Router + law-applicator. |
| Adjudicator | Remove all references. Replaced by generic HITL instances. |

Estimated ~20-25 spec files affected. Detailed file list to be produced during
implementation.

---

### Phase 11: Validation and Cleanup

- `make test-all` -- all tests pass
- `make check-fix-all` -- all lint/tidy clean
- Spec lint (`tools/spec-lint/`) -- all specs clean
- Verify no orphaned references to Jury/Clerk services remain (grep for
  `JuryService`, `ClerkService`, `Deliberate`, `DraftLaw`, `USE:jury`,
  `USE:clerk`, port `50059`, port `50060`)
- Verify no orphaned references to `CreateHearingWorkitem` remain
- Verify no orphaned "Librarian triggers hearing" references remain in specs
- Verify no orphaned references to superseded concepts remain:
  - Tally (as a separate node), `tally:latest`
  - delib-router
  - Deliberation Gate (as a single node)
  - Tribunal Router (as a single node)
  - Judiciary Gate (as a single node)
  - Adjudicator
  - petition-tier-router, petition-gate, petition-appraise
  - `hitl-appraise:latest` and `hitl-resolve:latest` as separate images
- Verify Facilitator is documented as the deadlock lifecycle owner
- Verify Arbiter receives pre-assembled bundles (no evidence assembly)
- Verify Tribunal is hearing mode only (no review mode references)
- Verify Advocate references are narrowed to fire-and-forget T4-5 gateway
- Verify Clerk cycle mirrors main cycle (clerk-forge, clerk-sort,
  clerk-appraise, clerk-refine, clerk-facilitator)
- Verify Suspend/Resume is documented in workitem lifecycle specs
- Verify CompletionReason is documented in SDK and workitem specs
- Verify generic HITL node is documented (one image, config-driven)
- Review `specs/05-reference/glossary.md` for completeness
- Update `AGENTS.md` repository structure

---

## Execution Order and Dependencies

```
Phase 1 (specs)         ✅ -- no code dependencies
Phase 2 (protos)        ✅ -- depends on Phase 1
Phase 3 (SDK/sidecar)   ✅ -- depends on Phase 2
Phase 4 (delete old)    ✅ -- depends on Phase 3
Phase 5 (new nodes)     ✅ -- depends on Phase 2+3 (4 of 6 superseded)
Phase 6 (rewrite nodes) ✅ -- depends on Phase 4+5 (all 3 superseded)
Phase 7.1a-d (cleanup)  ✅ -- depends on Phase 6
Phase 7.1e-i (entry SDK)✅ -- depended on FLOW_NAMESPACE_PLAN.md (satisfied)
Phase 7.1j-m (watchers) ✅ -- depends on Phase 7.1e-i
Phase 7.2a (GA CRDs)    ✅ -- GovernedArtefact CRDs written, part-of labels removed
Phase 7.2b-d (FN CRDs)  -- depends on Phase 8+9 (node images must exist)
Phase 7.3 (deployments)  -- depends on Phase 7.2b (CRDs define the nodes)
Phase 7.4 (configmaps)   -- depends on Phase 9 (node configs must be known)
Phase 7.5 (sort output)  ✅ -- unblocked (facilitator exists from 9.2)
Phase 8 (platform)       ✅ -- Suspend/Resume + CompletionReason
                         -- proto (8.1) ✅ → Operator (8.2a-h) ✅ → Sidecar (8.3) ✅ → SDK (8.4) ✅ → Gate (8.5) ✅
InputArtefacts refactor  ✅ -- cross-cutting config change (forge, refine, reviewer, appraise, facilitator)
Phase 9 (new nodes)      -- depends on Phase 8. In progress.
                         -- Rule Router (9.1) ✅
                         -- Facilitator (9.2) ✅
                         -- Law-Applicator (9.3) ✅
                         -- HITL (9.4) ✅
                         -- Clerk-Forge (9.5) + Clerk-Refine (9.6) (child targets for orchestrators)
                         -- Arbiter (9.7) + Tribunal (9.8) (create Clerk children)
                         -- Advocate (9.9) standalone rewrite
                         -- Delete superseded nodes (9.10) after replacements exist
Phase 10 (spec updates)  -- depends on Phase 9
Phase 11 (validation)    -- depends on all above
```

Phases 1–8 are complete. Phase 9 is in progress (9.1 Rule Router, 9.2
Facilitator, 9.3 Law-Applicator, and 9.4 HITL done). The InputArtefacts
cross-cutting refactor is complete. Next is Phase 9.5 (Clerk-Forge). Phases
7.2b–7.4 are deferred until Phase 9 produces the remaining node images and
configurations. Phase 7.5 (Sort output update) is unblocked now that the
Facilitator exists.

## Estimated Scope

| Phase | Files Affected | Effort | Status |
|---|---|---|---|
| 1. Specs | ~22 spec files | High | ✅ Complete |
| 2. Protos | ~6 proto/gen files | Low | ✅ Complete |
| 3. SDK/Sidecar | ~12 files | Medium | ✅ Complete |
| 4. Delete old | ~22 files deleted, ~5 build files | Low | ✅ Complete |
| 5. New nodes | ~18 new files (6 nodes x 3) | High | ✅ Complete (4 of 6 superseded) |
| 6. Rewrite nodes | ~9 files (3 nodes x 3) | High | ✅ Complete (all 3 superseded) |
| 7. Operator/platform/SDK/watchers | ~45 files | High | ✅ 7.1+7.2a complete |
| 8. Platform primitives | ~15 files (proto, operator, sidecar, SDK) | High | ✅ Complete (8.1–8.5) |
| 9. New nodes (arch revision) | ~30 files (6 new + 3 rewrites + 4 deletions + build) | High | In progress (9.1–9.4 complete) |
| 10. Specs (arch revision) | ~20-25 spec files | High | Pending |
| 11. Validation | 0 new, full test suite | Medium | Pending |
| **Total** | **~200 files** | | |

---

## Node Inventory

### Distinct Images (18)

| Image | New? | Notes |
|---|---|---|
| `sort:latest` | Existing | Main cycle + `clerk-sort` CRD instance |
| `forge:latest` | Existing | Main cycle only |
| `appraise:latest` | Existing | Main cycle + `clerk-appraise` CRD instance |
| `refine:latest` | Existing | Main cycle + (`clerk-refine` is own image) |
| `quench:latest` | Existing | Main cycle only |
| `facilitator:latest` | Existing | Main cycle + `clerk-facilitator` CRD instance |
| `arbiter:latest` | Existing (major rewrite) | Single built-in instance |
| `tribunal:latest` | Existing (major rewrite) | Hearing mode only |
| `juror:latest` | Existing | Deliberation primitive |
| `clerk-forge:latest` | **New** | Extends Forge + codification fan-out |
| `clerk-refine:latest` | **New** | Extends Refine + codification fan-out |
| `codify-smt:latest` | Existing | Formal representations |
| `rule-router:latest` | Existing | CEL-based routing |
| `hitl:latest` | **New** | Generic config-driven HITL |
| `law-applicator:latest` | **New** | Applies petitions via Librarian |
| `advocate:latest` | Existing (major narrowing) | T4-5 Governance Flow gateway |
| `friction-watcher:latest` | Existing | Entry node (friction events) |
| `ttl-watcher:latest` | Existing | Entry node (TTL polling) |

### CRD Instances (24)

| CRD Name | Image | Category |
|---|---|---|
| **Main Cycle** | | |
| `forge` | `forge:latest` | Computation |
| `sort` | `sort:latest` | Triage |
| `appraise` | `appraise:latest` | Review |
| `refine` | `refine:latest` | Revision |
| `quench` | `quench:latest` | Finalization |
| `facilitator` | `facilitator:latest` | Lifecycle |
| **Deliberation** | | |
| `arbiter` | `arbiter:latest` | Orchestrator |
| `juror` | `juror:latest` | Computation |
| **Tribunal Path** | | |
| `tribunal` | `tribunal:latest` | Orchestrator |
| `friction-watcher` | `friction-watcher:latest` | Entry |
| `ttl-watcher` | `ttl-watcher:latest` | Entry |
| **Clerk Cycle** | | |
| `clerk-forge` | `clerk-forge:latest` | Computation |
| `clerk-sort` | `sort:latest` | Triage |
| `clerk-appraise` | `appraise:latest` | Review |
| `clerk-refine` | `clerk-refine:latest` | Revision |
| `clerk-facilitator` | `facilitator:latest` | Lifecycle |
| **Clerk Exit Routing** | | |
| `clerk-done-router` | `rule-router:latest` | Rule Router |
| `hitl-gate` | `rule-router:latest` | Rule Router |
| **HITL** | | |
| `hitl-appraise` | `hitl:latest` | HITL |
| `arbiter-hitl-resolve` | `hitl:latest` | HITL |
| `tribunal-hitl-resolve` | `hitl:latest` | HITL |
| **Terminal** | | |
| `law-applicator` | `law-applicator:latest` | Action |
| `advocate` | `advocate:latest` | Gateway |
| **Codification** | | |
| `codify-smt` | `codify-smt:latest` | Computation |

---

## File Inventory

### Files to Delete (34 files -- completed in Phases 4, 7.1d)

**Jury service (`jury/`):** 15 files -- deleted in Phase 4.1.

**Clerk platform service (`platform/clerk/`):** 7 files -- deleted in Phase 4.2.

**Proto definitions:** `jury.proto`, `clerk.proto` -- deleted in Phase 2.2.

**Generated proto code:** `jury.pb.go`, `jury_grpc.pb.go`, `clerk.pb.go`,
`clerk_grpc.pb.go` -- deleted in Phase 2.4.

**Sidecar proxies:** `jury.go`, `jury_test.go`, `clerk.go`, `clerk_test.go`
-- deleted in Phase 3.2.

**Librarian hearing triggers:** `hearing_trigger.go`,
`hearing_trigger_test.go` -- deleted in Phase 7.1d.

### Files to Delete (Phase 9 -- superseded nodes)

- `nodes/deliberation-gate/main.go`
- `nodes/deliberation-gate/main_test.go`
- `nodes/deliberation-gate/testutil_test.go`
- `nodes/tribunal-router/main.go`
- `nodes/tribunal-router/main_test.go`
- `nodes/tribunal-router/testutil_test.go`
- `nodes/judiciary-gate/main.go`
- `nodes/judiciary-gate/main_test.go`
- `nodes/judiciary-gate/testutil_test.go`
- `nodes/clerk/main.go` (replaced by `nodes/clerk-forge/`)
- `nodes/clerk/main_test.go`
- `nodes/clerk/testutil_test.go`

### Files to Create (Phase 8 -- platform primitives)

- Proto changes in `proto/flow/v1/operator.proto`, `proto/flow/v1/common.proto`
- Regenerated `gen/flow/v1/*.go`
- SDK: `sdk/go/client.go` (modifications for Suspend/Resume/WithReason)

### Files Created (Phase 9 -- new nodes)

- `nodes/rule-router/main.go`, `main_test.go`, `testutil_test.go` ✅
- `nodes/facilitator/main.go`, `main_test.go`, `testutil_test.go` ✅
- `nodes/internal/artefacts/fetch.go` ✅ (shared helper, InputArtefacts refactor)
- `nodes/hitl/main.go`, `main_test.go`, `testutil_test.go`
- `nodes/clerk-forge/main.go`, `main_test.go`, `testutil_test.go`
- `nodes/clerk-refine/main.go`, `main_test.go`, `testutil_test.go`
- `nodes/law-applicator/main.go`, `main_test.go`, `testutil_test.go`

### Files to Modify (Phase 9 -- node rewrites)

- `nodes/arbiter/main.go`, `main_test.go`, `testutil_test.go`
- `nodes/tribunal/main.go`, `main_test.go`, `testutil_test.go`
- `nodes/advocate/main.go`, `main_test.go`, `testutil_test.go`

---

## Open Items

Design questions to resolve during implementation. Resolved items are kept
for historical context.

### Resolved

1. **Tribunal mode detection** -- ✅ Resolved in Phase 6.2. Mode is detected by
   artefact presence: `law-reference` = hearing mode, `petition` = review mode.
   *Note: review mode removed in the architecture revision. Tribunal is hearing
   mode only.*

2. **Tribunal Router vs Judiciary Gate** -- ✅ Resolved in Phases 5.5/5.6.
   Both implemented and tested. *Note: both eliminated in the architecture
   revision. Replaced by orchestrator-internal routing and Rule Router
   instances.*

3. **Advocate entry paths** -- ✅ Resolved in Phase 6.3. Four escalation types
   implemented. *Note: Advocate narrowed to fire-and-forget T4-5 gateway in
   the architecture revision. All HITL functionality moves to generic HITL
   node instances.*

4. **Petition schema stability** -- ✅ Resolved in Phase 1.

5. **Deliberation Gate caller-dependent routing** -- ✅ Resolved in architecture
   revision. The Tally/delib-router approach was subsequently eliminated.
   Orchestrators (Arbiter, Tribunal) now handle tally and routing internally.

6. **Tribunal path post-consensus routing** -- ✅ Resolved. Tribunal creates a
   child workitem for Clerk and Completes (fire-and-forget). On hung jury,
   routes to hitl-resolve.

7. **Adjudicator node** -- ✅ Resolved. Eliminated. Replaced by generic HITL
   node instances (`arbiter-hitl-resolve`, `tribunal-hitl-resolve`).

8. **HITL cancel as exit-node pattern** -- ✅ Resolved. HITL nodes configured
   as exit nodes call `Complete(WithReason(cancelled))`. Cancellation
   propagates up the child chain. Each parent decides policy (in the haiku
   flow: propagate via `Complete(WithReason(cancelled))`).

9. **Hung jury resolution** -- ✅ Resolved. Both Arbiter and Tribunal route
   `hung` to a hitl-resolve instance (generic HITL node). Human can provide
   a resolution (back to orchestrator) or cancel (exit).

10. **petition-appraise feedback deadlock** -- ✅ Resolved. The Clerk cycle
    uses the same Facilitator → Arbiter path as the main cycle. The Facilitator
    and Arbiter are generic -- they handle any governed artefact's deadlocked
    feedback, including petition feedback.

### Open

1. **Round count tracking** -- ✅ Resolved. Internal to orchestrators. The
   Arbiter/Tribunal maintain an in-memory counter within the handler's
   fan-out-tally-retry loop. No external artefact needed -- the handler
   runs the loop synchronously within a single invocation.

2. **Juror diversity selection** -- how does the Arbiter/Tribunal select which
   agent configurations to use when fanning out? Config-driven (list of
   personalities) or dynamic (select for diversity based on jury size)?

3. **CodificationService CRD evolution** -- does the existing CRD stay as-is
   (managing pod lifecycle for codification nodes), or does it need new fields
   to describe the node's output format and entry contract?

4. **Watcher node deduplication** -- ✅ Resolved. Accept duplicates (per-replica
   in-memory dedup is best-effort). Implemented in friction-watcher (Phase
   7.1j). Revisit if it becomes a problem.

5. **Watcher node lifecycle** -- watcher nodes are long-lived processes that
   create Workitems but don't themselves process Workitems in the normal
   handler sense. How does the Operator track their health?

6. **Friction Watcher threshold configuration** -- per-tier threshold values
   for `friction.threshold_crossed` events. Deferred to Phase 9 or later.

7. **Event Bus delivery semantics for watcher dedup** -- ✅ Resolved. Accept
   duplicates. Same answer as item 4.

8. **TTL Watcher Librarian access** -- ✅ Resolved in Phase 7.1k. The entry
   loop calls `QueryLaws` through a standard flow.Client (Sidecar proxies
   LibrarianService with the identity fallback).

9. **Rule Router CEL variable scope** -- lazy evaluation design. `cel-go` AST
   inspection for referenced identifiers. Node walks AST at startup. To be
   implemented in Phase 9.4.

10. **Suspend CEL condition scope** -- what variables are available to the
    Operator when evaluating suspend conditions? At minimum: `children`
    (list with phase, node, completion_reason), `elapsed` (duration since
    suspension), `metadata`. The Operator evaluates conditions on child
    phase change events, not on a polling interval. To be implemented in
    Phase 8.2.

11. **Advocate Governance Flow interface** -- the Advocate submits T4-5
    petitions to the Governance Flow. The exact interface (API, protocol,
    fire-and-forget) is deferred.

12. **Facilitator evidence bundle format** -- what exactly goes into the
    evidence bundle artefact? Feedback history, artefact content (truncated),
    relevant laws, friction summary. Needs a defined schema so the Arbiter
    can parse it. To be defined in Phase 9.1.

13. **Arbiter re-entry detection** -- ✅ Resolved. The Arbiter distinguishes
    "first invocation" from "post-Clerk resume" by checking `GetChildren()`
    for completed Clerk children. If found, this is a resume. The Operator
    re-dispatches to the same node type, and the handler inspects child state.

14. **Clerk-Forge vs existing Clerk node** -- ✅ Resolved. The existing
    `nodes/clerk/` will be renamed/replaced with `nodes/clerk-forge/` in
    Phase 9.6/9.10. The codification fan-out logic is preserved and extended.

15. **Clerk-Refine codification** -- does Clerk-Refine always re-run
    codification on revision, or only when the formal representations are
    affected? Config-driven or always?

16. **HITL node presentation layer** -- how does the generic HITL node present
    artefacts and choices to the human? Queue-based (external UI polls for
    pending decisions)? Webhook? This is the `USE:queue/server` capability
    from the SDK HITL pattern. Needs to be made generic.
