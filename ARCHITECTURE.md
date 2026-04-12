# Judiciary, Embassy, and Federation Target Architecture

This file is the source of truth for the target-state architecture. If it
conflicts with `PHASES_01_09.md`, this file wins. Active implementation
sequencing lives in `PLAN.md` and `PHASE_10.md` onward.

## Status

- Phase 9 implementation is complete.
- The Embassy / Federation / cross-flow redesign is in progress.
- Phase 11 Embassy foundations are complete under the corrected platform-owned node/import-type model.
- Phase 12 Federation foundations are complete (proto, service schema, publication lifecycle, dispute records).
- Active work moves next to `PHASE_13.md` Embassy node, Federation service, and Clerk/authority wiring.

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
   (law-applicator, Codification) do not make routing decisions. This keeps each
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
11. **Cross-flow boundaries are explicit and symmetric.** Every Flow has an
    Embassy. Federation-member and Treaty crossings use the same manifest +
    package protocol. The trust source changes; the runtime protocol does not.
12. **Workitem transfer and law publication are separate.** Embassy moves
    `law-petition`s and any other imported Workitems. The Federation service
    distributes published laws. Authority publication is not modelled as an
    Embassy import type.

---

## Key Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Jury service | **Remove entirely** | Replaced by Juror nodes + orchestrator-internal tally |
| Clerk | **Move from platform service to node** | `clerk-forge` is a CRD instance of `forge:latest` with petition-drafting prompts. Codification is a standalone downstream node. Receives Workitems, not RPC calls. |
| ConsensusStrategy type | **New `judiciary.proto`** | Keeps judiciary-specific types grouped. Separate from general wire protocol in `common.proto`. |
| Petition format | **YAML/Markdown GovernedArtefact** | Human-readable for HITL. Consistent with how all other artefacts work. |
| Juror personalities | **Single image, config-driven** | One Juror node binary loads agent configurations for diversity. Not N separate deployments. |
| Multi-round deliberation | **Internal to orchestrators** | Arbiter and Tribunal handle fan-out, tally, and retry internally. No external Tally or routing nodes needed for deliberation. Round count is a Workitem artefact. |
| Codification dispatch | **Standalone Codification node fans out** | Replaces the specced-but-unbuilt gRPC Encode dispatch. Codification sits between Forge/Refine and Sort in the Clerk cycle. Uses existing Workitem fan-out. |
| Hearing triggers | **Two watcher nodes, not Librarian** | Friction Watcher subscribes to Event Bus friction channel. TTL Watcher polls Librarian for expired laws. Both are entry-bound nodes that create hearing Workitems via `CreateWorkitem`. No judiciary-specific logic in the Operator or Librarian. |
| `CreateHearingWorkitem` RPC | **Remove entirely** | Replaced by generic `CreateWorkitem` called by watcher nodes. The Operator has no judiciary-specific knowledge. |
| Routing logic | **Rule Router with CEL** | Generic node evaluates CEL expressions against workitem state. One image, many CRD instances with different rule configs. Uses `github.com/google/cel-go` (Kubernetes-native expression language). |
| Petition review (T1-2) | **Reuse Appraise image** | Same Appraise node image as the main cycle, configured for petition review. Automated multi-agent review produces feedback. |
| Petition review (T3-5) | **Generic HITL node** | Single `hitl:latest` image configured for petition review. Human can approve, provide feedback, or cancel. |
| Federation | **Dedicated federation service + ordinary Flows** | Joining a federation establishes trust, membership, discovery, and role / relationship policy. There is no special Governance Flow runtime. |
| State model | **States are federation-defined groups of Flows** | Sibling relationships and state-level publication derive from shared group membership, not a dedicated state Flow. |
| Embassy | **Standard cross-flow boundary node** | Every Flow has an operator-provisioned Embassy that handles outbound export, inbound intake, package verification, naturalisation, and onward routing. Replaces Advocate. |
| Higher-authority escalation | **Built-in system `law-petition` import type via Embassy** | Upward petitions are imported Workitems addressed to the relevant authority Flow through a platform-owned import type, not ad hoc governance RPCs or flow-authored YAML config. |
| Cross-flow intake config | **Platform-owned + flow-authored import type registry** | Effective Embassy intake resolves against built-in system import types plus the Flow's `crossFlow.importTypes` extension map. Flow-authored entries still publish `importType -> {node, requireForeignStamps}` and `node` must be entry-bound. |
| Import type ownership | **Two ownership classes in one namespace** | Platform-owned import types (for example `law-petition`) are always present/configured per Flow and are not user-editable. Flow architects may add additional custom import types in `crossFlow.importTypes`. |
| Treaty scope | **Directed trust policy + allowed import types for non-federation exchange** | Treaties remain receiver-enforced one-way trust boundaries outside a federation and may constrain which `importType`s a remote Flow may use. |
| Cross-flow auth | **mTLS + signed Embassy manifest** | mTLS authenticates the Embassy-to-Embassy channel. Trust roots come from federation membership or Treaty policy. No JWT/bearer-token layer is required. |
| Naturalisation | **Per-stamp local attestation (`imported-*`)** | Embassy verifies each required foreign stamp and emits a local `imported-<stamp>` attestation. Local contracts rely on the attested local stamps; foreign stamps remain provenance only. |
| Cross-flow transfer protocol | **Header-first Embassy manifest + streamed package** | Sender Embassy sends a signed manifest containing `importType`, artefact inventory, and foreign stamps. Receiver Embassy preflights before requesting the full package. Same protocol for federation-member and Treaty exchange. |
| Published law distribution | **Federation service distributes published local Tier 3 laws** | Authority Flows publish ordinary local laws outward; subscribers materialise them as Tier 4 or Tier 5 based on publisher role. |
| Publication conflicts | **Reject at federation publication admission** | Conflicting or unauthorised publications are rejected before distribution with structured feedback returned to the source Flow. |
| Law application | **Dedicated law-applicator node** | Applies approved petitions via Librarian (`WriteLaw`/`RetireLaw`). Separated from routing decisions per Principle 5. |
| Deadlock lifecycle | **Facilitator + Arbiter child** | Sort routes deadlocks to the Facilitator. The Facilitator assembles an evidence bundle, creates a child workitem for the Arbiter, and Suspends. The Arbiter deliberates and Completes. The Facilitator resumes and routes back to Sort. |
| Dependent processes | **Suspend/Resume platform primitive** | When a node needs to wait for a long-running dependent process (e.g. Arbiter waiting for Clerk cycle to complete a law change), it creates a child workitem, calls `Suspend(condition)`, and gets re-dispatched when the condition is met. CEL-based conditions. Timeouts. |
| Completion reasons | **`CompletionReason` enum on `Complete()`** | Distinguishes success from cancellation. Stored on workitem status, filterable in CEL. Audit log records the reason. No separate `Cancelled` phase. |
| Clerk cycle structure | **Mirrors main cycle** | The Clerk cycle uses the same node images (Forge, Sort, Appraise, Refine, Facilitator) as CRD instances with different configs (prompts, artefact names). Prompts use code defaults with optional ConfigMap overrides. A standalone Codification node handles formal representation fan-out between Forge/Refine and Sort. |
| Node prompt configuration | **Code defaults + ConfigMap override** | Concrete agents keep their Go `const` default prompts as baked-in defaults. ConfigMap can override `systemPrompt` and/or `queryTemplate`. Empty/missing config field = use the baked-in default (not an error). Different image = different contract, schema, or model (not just different prompts). This enables `forge:latest` to serve both haiku (defaults) and petition drafting (ConfigMap overrides) without separate images. |
| Agent contracts | **Go interfaces in the SDK** | Each node type defines a typed Go interface (`ForgeContract`, `TriageContract`, `RevisionContract`, `ReviewContract`, `EvalContract`, `FindingContract`) in `sdk/go/` that formalises the boundary between a node handler and its agent implementation. Nodes depend on the interface; concrete agents implement it. Agent swap happens at image level (different image = different contract implementation), not at config level. |
| Shared handler library | **`nodes/internal/handlers/`** | Handler logic (read artefacts, query laws, call agent, store output, route) is extracted into shared handlers parameterised by the contract interface. Each node's `main.go` becomes thin: load config, construct concrete agent (with optional prompt overrides), call shared handler passing the agent as the contract interface. |
| HITL node design | **Generic, config-driven** | One `hitl:latest` image. Outputs become user choices. `WRITE:feedback` capability enables feedback. Exit-node config enables cancellation. Multiple CRD instances for different use cases. |
| Hung jury resolution | **hitl-resolve (generic HITL instance)** | Hung juries route to a HITL node configured for resolution. Human can provide a resolution (routes back to orchestrator) or cancel (exit). Separate CRD instances per orchestrator. |
| Tribunal review mode | **Removed** | Tribunal retains hearing mode only. Petition review is handled by the Clerk cycle's Sort/Appraise/Refine loop. |
| Tally node | **Eliminated** | Vote tallying is absorbed into the Arbiter and Tribunal orchestrators. No external Tally node. |
| Deliberation Gate | **Eliminated** | Replaced by orchestrator-internal tally + direct routing. |
| Tribunal Router | **Eliminated** | Replaced by orchestrator-internal routing and Rule Router instances. |
| Judiciary Gate | **Eliminated** | Split into Rule Router instances + law-applicator. |
| Adjudicator | **Eliminated** | Replaced by generic HITL node instances (`hitl-resolve`). |
| Verdict-context contract | **Prose decision, not structured fields** | The verdict-context artefact carries the court's reasoned argument as natural language (`trigger` + `decision` fields only). Old structured fields (`goal`, `action`, `tier`, `applies_to`, `law_id`, `feedback_ids`, `source_workitem`) removed. clerk-forge interprets the prose into structured petition changes. clerk-appraise reviews the petition against the verdict to ensure alignment. |

---

## Topology

### Node Types

The redesigned judiciary and cross-flow boundary use seven categories of node,
each with a clear responsibility boundary:

- **Lifecycle nodes** -- own a dependent process. Assemble context, create
  child workitems, suspend, resume, route the result. (Facilitator.)
- **Orchestrator nodes** -- conduct deliberation. Fan out to Jurors, tally
  verdicts, handle multi-round retry internally. May suspend for dependent
  Clerk cycles. (Arbiter, Tribunal.)
- **Computation nodes** -- read artefacts, perform domain logic, write result
  artefacts, route to a single output. No routing decisions. (Forge, Refine,
  Juror, Codification nodes.)
- **Rule Router nodes** -- read workitem state, evaluate CEL rules, route to
  the matching output. No state mutation. One image (`rule-router:latest`),
  many CRD instances with different configs. (clerk-done-router, hitl-gate.)
- **Action nodes** -- perform a single side-effecting operation and complete.
  (law-applicator.)
- **Boundary nodes** -- manage inter-flow exchange. Perform manifest
  preflight, request/stream cross-flow packages, materialise imported
  workitems, apply naturalisation stamps, and route to configured intake
  nodes. (Embassy.)
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

### Embassy

The Embassy is the standard cross-flow boundary node. Every Flow has one
operator-provisioned `embassy` instance; `Advocate` is removed. Embassy runs as
a persistent entry node (watcher-style `StartEntry` process) with two jobs:

- **Outbound export** -- package local work for another Flow, send a signed
  manifest first, and stream the full package only after the remote Embassy
  accepts it.
- **Inbound import** -- receive a signed manifest, preflight the declared
  `importType`, request the full package, verify it, create a new local
  Workitem, unpack artefacts, apply naturalisation stamps, and route onward.

**Flow config** (`FoundryFlow.spec.crossFlow.importTypes`) defines only the
flow-authored extension set:

```yaml
crossFlow:
  importTypes:
    external-submission:
      node: intake-triage
      requireForeignStamps:
        submission:
          - approval
```

`law-petition` is a built-in system `importType`. It exists in the same
namespace as flow-authored import types, but it is not declared in YAML and may
not be overridden by the flow architect. The platform provisions and configures
it per Flow in the same way it provisions/configures the built-in judiciary
subsystem and Embassy itself.

Additional `importType`s are flow-authored and declared in
`crossFlow.importTypes`. Their `node` values are receiver-defined and must
reference entry-bound FoundryNodes.

Embassy resolves imports against the merged effective import-type registry:
built-in system import types plus the receiving Flow's published
`crossFlow.importTypes`. Senders target public `importType`s, never private node
names.

**Manifest/header** -- signed by the sending Flow and sent before the full
package. Includes at minimum `importType`, source/target flow identity,
transfer ID and expiry, artefact inventory (governed name, digest, size,
representation metadata), and the foreign stamps for each artefact. The
receiving Embassy uses the manifest for preflight and only requests the full
package if the import is admissible.

**Naturalisation** -- if all required foreign stamps validate, Embassy applies a
local `imported-<stamp>` attestation for each required foreign stamp on the
imported artefact. Foreign stamps remain attached for provenance/audit, but
downstream local contracts rely on the local `imported-*` stamps.

**Trust topologies** -- federation-member exchange and treaty-based exchange use
the same Embassy protocol. The difference is only the trust source: federation
root vs Treaty policy. Treaties may constrain allowed `importType`s for
non-federation exchange.

**Transport authentication** -- Embassy-to-Embassy connections use mTLS. The
signed manifest carries per-transfer claims and artefact inventory. Federation
membership or Treaty policy authorises the remote Flow and import types; this
is not embedded in the certificate and does not require a JWT-style bearer
token.

**Scope boundary** -- Embassy handles Workitem transfer (built-in
`law-petition` plus any flow-authored import types). Published law distribution is a Federation
service responsibility, not an Embassy `importType`.

### Federation Service

The Federation service is the control-plane authority that sits above
individual Flows. It is not a node in the Flow topology. Joining a federation
establishes Flow identity, trust-root discovery, endpoint discovery, and
membership. Federation policy then defines:

- **States / organisational units** -- groups of Flows that share state-level
  relationships; sibling relationships derive from shared membership.
- **Authority publisher roles** -- which Flows may publish local Tier 3 laws
  outward, whether publication lands as Tier 4 (state) or Tier 5 (federation),
  and which domains / scopes they cover.
- **Petition routing** -- which authority Flow receives built-in
  `law-petition` imports for a given relationship / scope.
- **Publication admission** -- when a Flow marks a local Tier 3 law
  `published`, it submits the law to the Federation service. The service
  validates role / scope / relationship constraints, runs conflict detection,
  and either accepts the publication for distribution or rejects it with a
  structured report returned to the source Flow.
- **Publication distribution** -- accepted state publications materialise as
  Tier 4 laws in subscriber Flows. Accepted federation-wide publications
  materialise as Tier 5 laws. The law remains Tier 3 in its source Flow;
  publication changes how other Flows receive it, not what it is locally.

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

```text
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

```text
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
   creates a child workitem for the Clerk cycle with a `verdict-context`
   artefact containing the jury's reasoned decision (prose, not structured
   fields) and Suspends. clerk-forge interprets the decision into a
   structured petition. When the Clerk cycle completes (law applied locally
   or submitted upward through Embassy), the Arbiter resumes and Completes.
   The Facilitator then resumes and routes back to Sort. On the T4-5 path,
   law-applicator creates the dispute record before routing to Embassy, so
   the record exists before Sort re-evaluates — Sort routes to
   `pending-hold` instead of re-deadlocking (see design point 8).
3. **Hung** -- the jury cannot reach consensus after max rounds. The Arbiter
   routes to hitl-resolve. The human can provide a resolution (back to
   Arbiter for re-deliberation) or cancel.

**Suspension chain** (deepest path): Facilitator suspends for Arbiter.
Arbiter suspends for Clerk. Clerk cycle completes. Arbiter resumes and
completes. Facilitator resumes and routes to Sort.

### Tribunal Path (Watcher-Triggered Hearing)

```text
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
a hearing Workitem when a law's friction crosses its tier's threshold. Friction
hearings can target any tier, including imported Tier 4-5 laws — a hearing
verdict on a T4-5 law feeds a Clerk cycle that exits via Embassy as a
`law-petition` to the authority. The TTL Watcher periodically polls the
Librarian and creates a hearing Workitem when a law's age exceeds its tier's
review TTL. Only Tier 1-2 laws have TTLs; Tier 3-5 laws are not subject to
TTL-based review. Both watchers are entry-bound nodes that call
`CreateWorkitem`, store a `law-reference` artefact, and route to the Tribunal.

The Tribunal fans out to Jurors, tallies, and handles retry internally. On
consensus, it creates a child workitem for the Clerk cycle with a
`verdict-context` artefact containing the jury's reasoned decision (prose)
and **Completes** (fire-and-forget). clerk-forge interprets the decision
into a structured petition. The hearing workitem has no parent to report
back to, so no suspension is needed. On hung jury, it routes to hitl-resolve.

### Clerk Cycle (Petition Drafting and Approval)

The Clerk cycle **mirrors the main cycle**. It uses the same node images
(Forge, Sort, Appraise, Refine, Facilitator) as different CRD instances with
different configs -- primarily different agent prompts loaded from ConfigMaps.
A petition is a governed artefact that goes through the same quality process
as any other work product. A standalone Codification node sits between
Forge/Refine and Sort to handle formal representation fan-out.

```text
[clerk-forge] --> [codification] --[fan-out]--> [codifiers]
                       |
                       v
                 [clerk-sort]
                       |-> needs refinement -> [clerk-refine] --> [codification] --> [clerk-sort]
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
                                                                                    |-- tier 4-5 --> [law-applicator] --> [embassy] --> Complete()
```

**Key design points:**

1. **Same images, different configs.** `clerk-forge` is `forge:latest` with
   petition-drafting prompts provided as ConfigMap overrides of the baked-in
   haiku defaults. `clerk-sort` is `sort:latest` with different output
   wiring. `clerk-appraise` is `appraise:latest` configured for petition
   review. `clerk-refine` is `refine:latest` with petition-revision prompt
   overrides. `clerk-facilitator` is `facilitator:latest`. **No separate
   `clerk-forge:latest` or `clerk-refine:latest` images.** The variation is
   entirely in the ConfigMap prompt overrides and artefact config.

2. **Codification is a standalone node.** The Codification node
   (`codification:latest`) sits between Forge/Refine and Sort. It reads the
   petition artefact only (not verdict-context — all needed info is in each
   change entry), fans out to codify-\* nodes **per-change** for non-retire
   changes, collects formal representations, attaches them to the originating
   changes, and routes to Sort. This keeps Forge and Refine generic.

3. **Prompts are code defaults with ConfigMap override.** Concrete agents
   keep their Go `const` default prompt templates as baked-in defaults.
   ConfigMap can override `systemPrompt` and/or `queryTemplate` — if the
   ConfigMap provides them, they replace the defaults; if the ConfigMap
   omits them, the baked-in defaults are used (not an error). This is
   what makes `forge:latest` serve both haiku generation (using defaults)
   and petition drafting (using ConfigMap overrides) via different CRD
   configs. A different image is only needed when the **contract**
   (input/output types, schema) or **model** changes.

4. **clerk-sort** handles petition triage the same way Sort handles haiku
   triage. When all feedback is resolved and the petition is ready, Sort
   routes to "done" which goes to the `clerk-done-router`.

5. **Tier-based routing** -- the `clerk-done-router` (Rule Router) reads
   petition metadata to determine the tier(s) of the laws being changed.
   T1-2 go directly to law-applicator. T3-5 go to hitl-appraise.

6. **Deadlocked petition feedback** goes through the same Facilitator →
   Arbiter path as the main cycle. The Facilitator and Arbiter are generic --
   they handle any governed artefact's deadlocked feedback.

7. **Embassy is the cross-flow boundary.** Receives approved T4-5 petitions,
   exports them as built-in `law-petition`s to the authority Flow selected by
   federation policy, and Completes the local workitem after successful
   handoff. The local Flow does not wait for remote deliberation.

8. **Dispute records and pending-hold.** When law-applicator processes a
   T4-5 petition, it creates a **dispute record** in the Library via the
   Librarian (`CreateDisputeRecord`) before routing to Embassy. A dispute
   record is not a law — it is a separate Library entity that links the
   `petition_id` to the specific law IDs cited in the petition's changes.
   Sort checks for active dispute records when evaluating feedback: if any
   cited law has an active dispute, Sort routes the workitem to
   `pending-hold` (suspended, keyed on the `petition_id`) instead of
   deadlocking. This damps repeat thrash while the remote authority
   deliberates. A **petition-outcome-watcher** node monitors Federation
   events for petition resolution. On acceptance, it retires the dispute
   record and resumes held workitems; the published law materialises as
   T4/T5 via normal distribution. On rejection, it retires the dispute
   record, creates a new Clerk cycle Workitem with the rejection report
   as context, and resumes held workitems.

9. **Standalone Flows.** A standalone Flow (no federation membership) only
   has Tier 1-3 laws. The T4-5 path through law-applicator → Embassy, dispute
   records, `pending-hold`, and petition-outcome-watcher are all inert — the
   `clerk-done-router` will never route to the T4-5 branch because no
   petition will reference external-tier laws.

10. **HITL cancel propagation.** When hitl-appraise cancels, it calls
   `Complete(WithReason(cancelled))`. The child workitem terminates. If the
   parent was an Arbiter that suspended for this Clerk cycle, the Arbiter
   detects the cancellation on resume and propagates it up via
   `Complete(WithReason(cancelled))`.

### Petition Artefact

A single structured YAML/Markdown GovernedArtefact containing the complete
proposed change set. Produced by clerk-forge from the court's prose verdict
decision. A petition is a multi-law patch — it may contain changes to
multiple laws (e.g., retire two laws and create a new one).

```yaml
petition:
  petition_id: "f47ac10b-58cc-4372-a567-0e02b2c3d479"  # UUID, generated by clerk-forge at drafting time
  context:
    trigger: "deadlock-resolution" | "hearing"
    verdict_decision: "The court has reviewed the evidence and decided that..."
    justification: "..."   # clerk-forge's prose summary of the petition rationale
  changes:
    - action: "create"
      tier: 2
      goal: "..."
      applies_to: ["..."]
      justification: "..."
      representations:
        - type: "text/markdown"
          content: "..."
        - type: "application/smt-lib"
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

The `context.verdict_decision` preserves the original court decision for
downstream alignment checking — clerk-appraise reviews the petition's
changes against the verdict to ensure they faithfully implement the court's
reasoning. Only `create`, `update`, and `demote` changes receive formal
`representations` from the Codification node. Retire changes are purely
administrative — no codification needed.

### Published Law Lifecycle

Authority Flows are ordinary Flows. When an approved local Tier 3 law is marked
`published`, the source Flow submits it to Federation service publication
admission. If accepted, the Federation service distributes the law to
subscriber Flows, where it materialises as Tier 4 or Tier 5 according to the
publisher's authority role.

If the law originated from a cross-flow `law-petition`, the approved published
law carries the `petition_id` in provenance. The threading path is:

1. The petition artefact carries `petition_id` as a top-level field (set at
   drafting time by clerk-forge in the originating Flow).
2. Embassy transfers the petition artefact with `petition_id` intact as part
   of the `law-petition` import.
3. The authority Flow's governance cycle operates on the petition artefact.
   The `petition_id` survives naturally — it is an artefact field, not
   metadata that nodes need to explicitly propagate.
4. When the authority Flow's law-applicator writes the resulting law via
   `Librarian.WriteLaw`, it copies the `petition_id` from the petition
   artefact into the law's provenance metadata.
5. When the law is marked `published`, the Federation service distributes the
   full law object including provenance. The `petition_id` is part of that
   provenance.

Subscriber Flows use the `petition_id` in the distributed law's provenance to
match the outcome to active dispute records (see design point 8). The
petition-outcome-watcher retires the dispute record and resumes any workitems
held in `pending-hold`.

If the Federation service rejects a publication, it returns a structured
conflict / authorisation report. The petition-outcome-watcher receives this
rejection event, retires the associated dispute record, creates a new Clerk
cycle Workitem with the rejection report as context (so the petition can be
revised and resubmitted), and resumes any workitems held in `pending-hold`.

### Dispute Records

A **dispute record** is a Library entity type distinct from laws. It represents
an active cross-flow petition whose outcome is pending. Dispute records are not
laws — they carry no governance weight and do not appear in the law hierarchy.

A dispute record contains:

- `petition_id` — the stable identifier linking to the petition artefact.
- `cited_law_ids` — the law IDs whose conflicts prompted the petition.
- `created_at` — timestamp for expiry/TTL purposes.
- `status` — `active` or `retired`.

**Lifecycle:**

1. **Created** by law-applicator on the T4-5 path via
   `Librarian.CreateDisputeRecord`, before routing to Embassy.
2. **Queried** by Sort when evaluating feedback. If any law cited in feedback
   has an active dispute record, Sort routes to `pending-hold` instead of
   deadlocking. The workitem is suspended, keyed on the `petition_id`.
3. **Retired** by the petition-outcome-watcher when the Federation reports
   acceptance or rejection. On retirement, all workitems held against that
   `petition_id` are resumed.

Sort's `pending-hold` routing prevents thrash: multiple workitems encountering
the same disputed laws all park until the authority resolves the petition,
rather than each independently re-escalating through the Facilitator → Arbiter
→ Clerk cycle.

---

## What Gets Removed

| Component | Current | Disposition |
|---|---|---|
| Jury service (`jury/`) | Standalone gRPC service with deliberation engine, juror personalities | **Removed entirely** |
| Jury proto (`proto/flow/v1/jury.proto`) | `Deliberate` RPC, `ConsensusStrategy`, `JurorJustification` | **Removed** (types relocated to `judiciary.proto`) |
| Clerk service (`platform/clerk/`) | Platform gRPC service, prose drafting | **Removed** (replaced by Clerk node) |
| Clerk proto (`proto/flow/v1/clerk.proto`) | `DraftLaw` RPC | **Removed** (Clerk is a node now) |
| Advocate node (`nodes/advocate/`) | Judiciary-specific HITL / escalation boundary | **Removed** (replaced by Embassy) |
| Sidecar Jury proxy (`platform/sidecar/internal/proxy/jury.go`) | Forwards `Deliberate` to Jury service | **Removed** |
| Sidecar Clerk proxy (`platform/sidecar/internal/proxy/clerk.go`) | Forwards `DraftLaw` to Clerk service | **Removed** |
| SDK `client.Deliberate()` (`sdk/go/client.go`) | Convenience wrapper for Jury RPC | **Removed** |
| SDK `client.DraftLaw()` (`sdk/go/client.go`) | Convenience wrapper for Clerk RPC | **Removed** |
| `CreateHearingWorkitem` RPC (`operator.proto`) | Judiciary-specific Operator RPC for hearing creation | **Removed** (watcher nodes use generic `CreateWorkitem`) |
| Librarian hearing triggers (`hearing_trigger.go`) | Friction subscription + TTL scanner in Librarian | **Removed** (replaced by Friction Watcher and TTL Watcher nodes) |

## What Gets Added

### New Platform Services

| Component | Role |
|---|---|
| Federation service | Federation control plane for membership, trust-root distribution, state / organisational-unit grouping, authority publisher roles, petition target discovery, publication admission, conflict rejection, and Tier 4 / Tier 5 distribution. |

### New Node Images

| Component | Role |
|---|---|
| Facilitator (`nodes/facilitator/`) | Lifecycle owner for deadlock resolution. Assembles evidence bundle, creates child for Arbiter, suspends, resumes, routes back to Sort. Generic -- handles any governed artefact's deadlocked feedback. |
| Codification (`nodes/codification/`) | Standalone fan-out orchestrator. Reads the petition artefact, iterates its changes, fans out to codify-\* nodes **per-change** for non-retire changes, collects formal representations, attaches them to the originating changes, stores the updated petition, and routes to Sort. Sits between Forge/Refine and Sort in the Clerk cycle. Does not read verdict-context (all needed info is in the petition's changes). |
| Juror node (`nodes/juror/`) | Single image, configurable judicial philosophy. Receives evidence, produces verdict. |
| Rule Router (`nodes/rule-router/`) | Generic CEL-based routing node. One image, many CRD instances. |
| Generic HITL (`nodes/hitl/`) | Generic config-driven HITL node. One image, many CRD instances. Behaviour derived from outputs, capabilities, and exit-node config. |
| Law-applicator (`nodes/law-applicator/`) | Applies approved petitions via Librarian. T1-3: `WriteLaw`/`RetireLaw`, copying the petition's `petition_id` into the law's provenance metadata. T4-5: `CreateDisputeRecord` (links `petition_id` to cited law IDs), then routes to Embassy. |
| Petition-outcome-watcher (`nodes/petition-watcher/`) | Monitors Federation events for petition outcomes. On acceptance: retires dispute record, resumes `pending-hold` workitems. On rejection: retires dispute record, creates new Clerk cycle Workitem with rejection context, resumes held workitems. |
| Embassy (`nodes/embassy/`) | Standard cross-flow ingress/egress boundary. Sends signed manifests, streams export packages, receives imports, validates foreign stamps, applies `imported-*` naturalisation stamps, and routes to configured import types. |
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
| Cross-flow import type config | `spec.crossFlow.importTypes` publishes the flow-authored extension set `importType -> {node, requireForeignStamps}` and replaces the old `importNode` field. Built-in system import types (for example `law-petition`) live in the same effective namespace but are platform-owned rather than YAML-authored. |
| Embassy transfer protocol | Signed manifest/header + streamed package transfer, shared by federation-member and Treaty-based exchange. Treaties may constrain `allowedImportTypes`. |
| Published law admission / distribution | Source Flows submit local Tier 3 laws marked `published`; accepted laws materialise as Tier 4 or Tier 5 in subscribers, rejected publications return structured reports. |

### CRD-Only Instances (no new image, just FoundryNode CRDs with config)

| CRD Name | Image | Purpose |
|---|---|---|
| `clerk-sort` | `sort:latest` | Petition feedback triage (mirrors main cycle Sort) |
| `clerk-forge` | `forge:latest` | Petition drafting (same image as Forge, petition-specific prompts via ConfigMap override of baked-in defaults) |
| `clerk-refine` | `refine:latest` | Petition revision (same image as Refine, petition-specific prompts via ConfigMap override of baked-in defaults) |
| `clerk-appraise` | `appraise:latest` | Automated petition review (same image as Appraise, petition-specific prompts via ConfigMap override) |
| `clerk-facilitator` | `facilitator:latest` | Petition feedback deadlock lifecycle (mirrors main cycle Facilitator) |
| `clerk-done-router` | `rule-router:latest` | Post-approval tier routing: T1-2 vs T3-5 |
| `hitl-gate` | `rule-router:latest` | Post-HITL routing: T3 approved/T4-5 approved |
| `hitl-appraise` | `hitl:latest` | T3-5 petition HITL review. Exit node. |
| `arbiter-hitl-resolve` | `hitl:latest` | Arbiter hung jury HITL resolution. Exit node. |
| `tribunal-hitl-resolve` | `hitl:latest` | Tribunal hung jury HITL resolution. Exit node. |

## What Gets Rewritten

| Component | Changes |
|---|---|
| Arbiter (`nodes/arbiter/`) | **Major rewrite.** No longer assembles evidence (Facilitator does). Receives pre-assembled bundle as child workitem. Internal tally (no external Tally/delib-router). Suspends for Clerk child on consensus path. Three outcomes: resolved (Complete), consensus (Suspend), hung (route to hitl-resolve). Verdict-context simplified to prose decision (trigger + decision fields only). |
| Tribunal (`nodes/tribunal/`) | **Major rewrite.** Hearing mode only (review mode removed). Internal tally and retry. Fire-and-forget child to Clerk on consensus. Routes to hitl-resolve on hung. Verdict-context simplified to prose decision (trigger + decision fields only). |
| Advocate (`nodes/advocate/`) | **Deleted.** Replaced entirely by the operator-provisioned Embassy boundary node. All HITL and escalation semantics are removed from the cross-flow boundary. |
| Governance Flow / higher-tier law propagation | **Deleted as a runtime primitive.** Replaced by Federation service membership, state grouping, authority publisher roles, and published-law distribution. |
| Cross-flow import/export (`importNode`, current `ExportWorkitem` / `ImportWorkitem` shape) | **Major redesign.** Replace the single `importNode` with `crossFlow.importTypes`, move preflight admission to Embassy manifests, and model naturalisation as Embassy-applied `imported-*` stamps. |
| Clerk node (`nodes/clerk/`) | **Deleted.** Forge with configurable prompts replaces the petition-drafting role. Codification fan-out logic moves to the standalone Codification node. |
| Deliberation Gate (`nodes/deliberation-gate/`) | **Deleted.** Tally logic absorbed into Arbiter/Tribunal. |
| Tribunal Router (`nodes/tribunal-router/`) | **Deleted.** Routing replaced by orchestrator-internal logic and Rule Router instances. |
| Judiciary Gate (`nodes/judiciary-gate/`) | **Deleted.** Routing moved to Rule Router instances. Law application moved to law-applicator. |

---

---

## Node Inventory

### Distinct Images (17)

| Image | New? | Notes |
|---|---|---|
| `sort:latest` | Existing | Main cycle + `clerk-sort` CRD instance |
| `forge:latest` | Existing | Main cycle + `clerk-forge` CRD instance (prompt-configurable) |
| `appraise:latest` | Existing | Main cycle + `clerk-appraise` CRD instance |
| `refine:latest` | Existing | Main cycle + `clerk-refine` CRD instance (prompt-configurable) |
| `quench:latest` | Existing | Main cycle only |
| `facilitator:latest` | Existing | Main cycle + `clerk-facilitator` CRD instance |
| `arbiter:latest` | Existing (major rewrite) | Single built-in instance |
| `tribunal:latest` | Existing (major rewrite) | Hearing mode only |
| `juror:latest` | Existing | Deliberation primitive |
| `codification:latest` | **New** | Fan-out orchestrator for formal representations |
| `codify-smt:latest` | Existing | Formal representations |
| `rule-router:latest` | Existing | CEL-based routing |
| `hitl:latest` | **New** | Generic config-driven HITL |
| `law-applicator:latest` | **New** | Applies petitions via Librarian |
| `embassy:latest` | **New** | Standard cross-flow boundary node (import/export + naturalisation) |
| `friction-watcher:latest` | Existing | Entry node (friction events) |
| `ttl-watcher:latest` | Existing | Entry node (TTL polling) |

### Runtime Node Instances (25)

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
| `clerk-forge` | `forge:latest` | Computation |
| `codification` | `codification:latest` | Fan-out Orchestrator |
| `clerk-sort` | `sort:latest` | Triage |
| `clerk-appraise` | `appraise:latest` | Review |
| `clerk-refine` | `refine:latest` | Revision |
| `clerk-facilitator` | `facilitator:latest` | Lifecycle |
| **Clerk Exit Routing** | | |
| `clerk-done-router` | `rule-router:latest` | Rule Router |
| `hitl-gate` | `rule-router:latest` | Rule Router |
| **HITL** | | |
| `hitl-appraise` | `hitl:latest` | HITL |
| `arbiter-hitl-resolve` | `hitl:latest` | HITL |
| `tribunal-hitl-resolve` | `hitl:latest` | HITL |
| **Boundary / Terminal** | | |
| `law-applicator` | `law-applicator:latest` | Action |
| `embassy` | `embassy:latest` | Boundary |
| **Codification** | | |
| `codify-smt` | `codify-smt:latest` | Computation |

---

## Open Items

1. **Juror diversity selection** -- how does the Arbiter/Tribunal select which
   agent configurations to use when fanning out? Config-driven (list of
   personalities) or dynamic (select for diversity based on jury size)?

2. **CodificationService CRD evolution** -- does the existing CRD stay as-is
   (managing pod lifecycle for codification nodes), or does it need new fields
   to describe the node's output format and entry contract?

3. **Persistent entry-node lifecycle** -- watcher nodes and Embassy are
   long-lived processes that create Workitems or accept inbound transfers, not
   just normal assignment handlers. How does the Operator track their health,
   readiness, and externally exposed endpoints?

4. **Friction Watcher threshold configuration** -- per-tier threshold values
   for `friction.threshold_crossed` events. Still open; defer to later
   flow-configuration work.

5. **Embassy manifest/package protocol** -- exact wire schema, signature and
   digest coverage, retry/idempotency semantics, and whether the package is a
   signed tarball or another archive/container format.

6. **Facilitator evidence bundle format** -- what exactly goes into the
   evidence bundle artefact? Feedback history, artefact content (truncated),
   relevant laws, friction summary. The code path exists, but the schema still
   needs to be written down explicitly in the specs/reference docs.

7. **HITL node presentation layer** -- how does the generic HITL node present
   artefacts and choices to the human? Queue-based (external UI polls for
   pending decisions)? Webhook? This is the `USE:queue/server` capability from
   the SDK HITL pattern. Needs to be made generic.

8. **Federation service object model** -- what are the exact resources / APIs
   for federation membership, state / organisational-unit grouping, authority
   publisher roles, petition target discovery, and endpoint discovery?

9. **Authority scope model** -- how are domains such as security, risk, and
   finance declared, attached to publishers, attached to laws / petitions, and
   used for routing?

10. **Overlap/conflict escalation policy** -- if two equal-tier external
    authorities overlap or publish contradictory laws, how is that conflict
    detected, who receives the resulting `law-petition`, and what prevents
    ambiguous local precedence?

11. **Publication lifecycle details** -- what is the exact `published` marker
    semantics, rejection report shape, petition-ID correlation, dispute
    record retirement behaviour, and subscriber materialisation path for
    accepted Tier 4 / Tier 5 laws?

12. **Embassy staging/storage model** -- does Embassy require scratch storage
    for streamed packages in all cases, or only for large bundles / retryable
    transfers? How is partial-transfer recovery modeled?

13. **Federation service deployment model** -- where does the Federation
    service run? Per-cluster or shared across clusters? Its own namespace or
    alongside an operator? Single instance or HA? How do Flows discover it?
    This affects trust bootstrap, network topology, and operational model.

14. **Trust bootstrap** -- how does a Flow authenticate to the Federation
    service to join in the first place? The federation provides the trust
    root, but the Flow needs to authenticate before it has that root.
    Options include out-of-band shared secret, manual certificate exchange,
    or a bootstrap token model.
