# Nodes and External Integrations

Nodes execute work inside the Flow runtime but do not own control-plane transitions. Node participation semantics, capability boundaries, and external integration behaviour are runtime constraints.

## Node Runtime Boundary

Nodes are execution actors in the data plane. Control-plane authority remains with the Operator.

- Nodes receive assignments through Sidecar-mediated invocation.
- Nodes read and write through SDK APIs mediated by [Sidecar](../03-node/01-sidecar.md).
- Nodes return one routing instruction at the end of each assignment.
- Nodes do not mutate Workitem lifecycle fields directly.
- Nodes admitting new Workitems through local creation must be bound to an entry contract.
- Cross-flow import admission targets configured `importNode`, which must be entry-bound.
- Runtime-triggered review-hearing admission targets the Tribunal's mandatory hearing entry binding.

Every node, including externally integrated nodes, runs inside the same control and governance contract.

## Execution Contract

Assignment execution follows a fixed contract:

1. Operator assigns a `Pending` Workitem to one node.
2. Sidecar invokes the node handler for the assigned Workitem.
3. Node executes business logic using SDK APIs.
4. Runtime services authorise capability-bounded operations on Sidecar-mediated requests.
5. Node returns one routing instruction.
6. Operator evaluates routing and exit guards, then applies state transition.

```mermaid
sequenceDiagram
    participant OP as Operator
    participant SC as Sidecar
    participant ND as Node
    participant SV as Services

    OP->>SC: assign Workitem to node
    SC->>ND: invoke assigned handler
    ND->>SC: SDK call
    SC->>SV: authenticated proxied operation
    SV-->>SC: response
    ND-->>SC: route_to_output / route_to / complete
    SC-->>OP: instruction + Workitem mutation requests
    OP->>OP: evaluate guards and transition
```

Routing instructions are `route_to_output`, `route_to`, or `complete`. Their schema is defined in [CRD Reference](../05-reference/crds.md), wire-level call contracts are defined in [gRPC API](../05-reference/grpc-api.md), and runtime rejection outcomes are defined in [Error Catalogue](../05-reference/error-catalogue.md).

## Capability and Authorisation Model

Node authority is capability-driven and authorised at runtime service boundaries.

- `READ:*` grants read access to scoped resources.
- `WRITE:*` grants write access to scoped resources.
- `STAMP:*` grants stamp application rights.
- `READ:flow` grants topology and configuration discovery access used by gate logic.

Stamp capabilities are explicit and granular:

- Grant format: `STAMP:artefact/<governed-artefact-name>/<stamp-name>`.
- Scope is exact for governed artefact name and stamp name.
- Stamp application is write-once per artefact version hash.

Enforcement split:

- Sidecar mediates authenticated SDK traffic between nodes and runtime services.
- Operator, Archivist, and Librarian authorise operations for their owned state surfaces.
- Operator enforces routing validity, lifecycle transitions, and exit contract checks.

## Reference Arrangement

The [Foundry Cycle](../01-concepts/02-foundry-cycle.md) defines the reference arrangement — node roles, cycle topology, and law authority. Flow Architects adapt it to their context while preserving the runtime invariants below.

From the platform's perspective, reference-arrangement node names (Forge, Quench, Appraise, Sort, Refine) carry no special runtime semantics. All node behaviour is determined by capability grants and configuration. A node named "Sort" with no `READ:flow` capability cannot perform gate logic; a node named "MyGate" with the right capabilities can.

Gate nodes in the reference arrangement discover stamp-provider routing targets from Flow configuration and capability grants — they do not hardcode provider node names. Deadlocked feedback is unresolved by state, so gate implementations must treat deadlock as a special-case branch when evaluating unresolved-feedback predicates. The `approval` stamp is a reference-arrangement convention, not a privileged platform keyword.

## The Judiciary — Standard Subsystem

The [Judiciary](../01-concepts/02-foundry-cycle.md#the-judiciary--standard-subsystem) is a standard subsystem in every Flow. All Judiciary nodes are Operator-provisioned. The Judiciary is routable and cannot be omitted from the runtime.

The Judiciary comprises orchestration nodes (Arbiter, Tribunal, Advocate), deliberation nodes (Juror, Deliberation Gate), and a legislative inner cycle (Clerk, Codification nodes, Tribunal Router, Judiciary Gate). All deliberation and legislative processes are externalised into the flow topology as node-based Workitem transitions — every step produces auditable artefacts with full friction tracking.

### Orchestration Nodes

**[Arbiter](../01-concepts/02-foundry-cycle.md#arbiter-deadlock-resolver)** — resolves deadlocked feedback disputes. Governed-work Workitems are *routed* to the Arbiter (via `route_to`) by the gate node. The governed-work Workitem is already in flight — the Arbiter assembles evidence (artefact content, feedback history, relevant laws, friction data), fans out to [Juror](../01-concepts/02-foundry-cycle.md#juror-judicial-agent) nodes using child Workitems, and collects their verdicts. The Workitem then routes to the [Deliberation Gate](../01-concepts/02-foundry-cycle.md#deliberation-gate-consensus-tally) for consensus tally. On consensus (or after HITL resolution), the verdict flows to the [Clerk](../01-concepts/02-foundry-cycle.md#clerk-petition-drafter) to draft a petition. The Workitem returns to Sort for re-evaluation in the reference arrangement.

**[Tribunal](../01-concepts/02-foundry-cycle.md#tribunal-hearing-conductor)** — conducts review hearings and petition reviews. Operates in two modes:

- **Hearing mode.** The [Librarian](./04-system-services.md#librarian) triggers creation of a *new* hearing Workitem, admitted through the Tribunal's entry binding. The hearing Workitem carries a single `law-reference` artefact — a built-in [GovernedArtefact](../05-reference/crds.md#governedartefact) provisioned by the Operator alongside the Tribunal, whose content is a plain-text string containing the law ID under review. The Tribunal assembles evidence, fans out to [Juror](../01-concepts/02-foundry-cycle.md#juror-judicial-agent) nodes, and routes to the [Deliberation Gate](../01-concepts/02-foundry-cycle.md#deliberation-gate-consensus-tally). On consensus, the [Tribunal Router](../01-concepts/02-foundry-cycle.md#tribunal-router) routes by tier. The governed-work Workitem that prompted the hearing is unaffected.
- **Review mode.** When the [Clerk](../01-concepts/02-foundry-cycle.md#clerk-petition-drafter) drafts a petition and routes it for review, the Tribunal reads the petition artefact, reviews it against governance context, fans out to Juror nodes, and routes to the Deliberation Gate. On consensus, the Workitem flows to the [Judiciary Gate](../01-concepts/02-foundry-cycle.md#judiciary-gate).

The Tribunal is both entry-bound (hearing admission) and exit-bound (hearing completion) for hearing Workitems.

**[Advocate](../01-concepts/02-foundry-cycle.md#advocate-human-escalation)** — the Judiciary's human-in-the-loop escalation point. Receives hung jury escalations (from [Deliberation Gate](../01-concepts/02-foundry-cycle.md#deliberation-gate-consensus-tally)), Tier 3+ hearing outcomes (from [Tribunal Router](../01-concepts/02-foundry-cycle.md#tribunal-router)), Tier 3 petition ratifications (from [Judiciary Gate](../01-concepts/02-foundry-cycle.md#judiciary-gate)), and Tier 4-5 appeals. The Advocate uses the SDK's [HITL pattern](../04-sdk/08-sdk-hitl.md) to expose a queue interface for human reviewers. HITL decisions route to the [Clerk](../01-concepts/02-foundry-cycle.md#clerk-petition-drafter) so they are codified as petitions and go through the normal review cycle.

The two primary paths are distinguished by admission mechanism: deadlock-escalated Workitems arrive at the Arbiter through routing; hearing Workitems arrive at the Tribunal through entry-contract admission as new Workitems.

### Deliberation Nodes

**[Juror](../01-concepts/02-foundry-cycle.md#juror-judicial-agent)** — the deliberation primitive. A single Juror node image loads different agent configurations at fan-out time to maximise diversity of judicial philosophy for the jury size required. Each Juror receives a child Workitem containing the question, evidence, prior-round reasoning (if a retry), and allowed outcomes. It runs a [FoundryAgent](../04-sdk/07-sdk-agent.md) with the loaded judicial personality and produces a structured verdict artefact (outcome + reasoning). Per-juror cost accounting is automatic — each juror's inference steps emit `foundry.cost.llm` telemetry events with attribution tags (`juror`, `round`, `severity`, `feedback_id`).

**[Deliberation Gate](../01-concepts/02-foundry-cycle.md#deliberation-gate-consensus-tally)** — generic consensus tally node. Reads juror verdict artefacts from the Workitem, applies the configured consensus strategy (simple majority, super-majority, or unanimity), and tracks the round count. Three well-known outputs: `consensus`, `retry`, `hung`. The Deliberation Gate does not know about tiers, petitions, or law semantics — it tallies votes and routes.

### Legislative Inner Cycle Nodes

**[Clerk](../01-concepts/02-foundry-cycle.md#clerk-petition-drafter)** — drafts and revises [petition](../01-concepts/02-foundry-cycle.md#petition-artefact) artefacts (structured YAML/Markdown documents describing proposed law changes). Receives verdict and context artefacts, drafts the petition with prose description, fans out to [Codification](../01-concepts/02-foundry-cycle.md#codification-nodes) nodes for formal representations, collects results, and assembles the complete petition. The petition then routes to the Tribunal for review. On revision (feedback via the [Judiciary Gate](../01-concepts/02-foundry-cycle.md#judiciary-gate)), the Clerk reads the feedback, revises the petition, re-fans-out for codification, and re-routes to the Tribunal.

**[Codification nodes](../01-concepts/02-foundry-cycle.md#codification-nodes)** — produce formal representations of laws. Each receives a child Workitem containing the law goal and context as artefacts, produces a formal representation in its declared output format (Rego, SMT-LIB, etc.), and calls `Complete()`. The Clerk fans out to the appropriate Codification nodes based on which representations are needed.

**[Tribunal Router](../01-concepts/02-foundry-cycle.md#tribunal-router)** — handles post-hearing routing. After the Deliberation Gate reaches consensus on a hearing, reads the verdict artefacts and law-reference artefact (for tier context) and routes: Tier 1–2 verdict to Clerk, Tier 3+ to Advocate.

**[Judiciary Gate](../01-concepts/02-foundry-cycle.md#judiciary-gate)** — mirrors Sort for the judiciary's inner cycle. Checks feedback resolution on the petition artefact and routes: approved Tier 1–2 applies via the Librarian, rejected or unresolved routes back to Clerk, approved Tier 3 routes to Advocate for HITL ratification, Tier 4–5 routes to Advocate then Governance Flow.

### Judiciary Node Capabilities

Arbiter and Tribunal capabilities are fixed by the runtime (not configurable by the Flow Architect):

- `WRITE:law/tier2` — resolve Tier 1-2 conflicts and mint Tier 2 Rulings via Clerk petition.
- `READ:law` — query the Library for law context.
- Friction queries, feedback resolution, and stamp application for hearing artefacts.
- Propose Tier 3 changes for human ratification (routed to Advocate).
- Appeal Tier 4-5 conflicts to Governance Flow authorities (routed to Advocate).

The Arbiter and Tribunal do not write Tier 1 Findings by convention; their role is judicial, not observational.

Judiciary Gate capabilities are fixed by the runtime:

- `WRITE:law/tier2` — apply approved Tier 1-2 petitions to the Library via the Librarian (`WriteLaw`/`RetireLaw`).
- `READ:law` — query the Library for context during feedback resolution checks.

Clerk capabilities are fixed by the runtime:

- `READ:law` — query existing laws for context during drafting.
- Fan-out to Codification nodes via child Workitems.

Advocate capabilities are fixed by the runtime:

- `READ:law` — query the Library for law context.
- `USE:queue/server` — enables the HITL queue interface and Federated Queue Mesh.
- Petition HITL for Tier 3 ratification.
- File appeals to the Governance Flow via the Librarian for Tier 4-5 conflicts.

## External Integration Nodes

External integration nodes connect Flow execution to external systems (webhooks, event buses, service APIs, human workflow systems) while preserving Flow invariants.

External nodes follow the same runtime contract as any other node:

- Sidecar-mediated API access only.
- Capability-bounded reads and writes.
- One routing instruction per assignment.
- Full auditability of side effects and outcomes.

External integration requirements:

- Idempotent side-effect handling for retries.
- Correlation identifiers for traceability.
- Explicit timeout handling and failure signalling.
- Deterministic mapping from external response classes to Flow outcomes.

Cross-boundary handoff between Flows uses export/import, which starts a separate Workitem lifecycle. It is not intra-Flow routing.

## Failure and Retry at Node Boundary

Node boundary failures are classified and handled distinctly:

- **Execution failure**: node returns explicit failure or exits abnormally.
- **Timeout failure**: assignment exceeds configured node timeout budget.
- **Routing failure**: returned instruction is invalid or unresolvable.
- **Governance deadlock**: feedback dispute exceeds deadlock threshold and is escalated to the Arbiter.

Retries and backoff may be configured operationally, but retries do not bypass capability checks, routing guards, or exit-contract validation.

## Telemetry and Friction Signals

Nodes are expected to emit operational and governance signals through [SDK Telemetry](../04-sdk/06-sdk-telemetry.md):

- Execution timing and error counters.
- Route-decision context tags.
- Friction events attributed to the current Workitem and optionally to specific laws.

Friction signalling is first-class runtime behaviour and feeds governance-cost analysis in [System Services](./04-system-services.md).

## Node Invariants

All node deployments preserve these invariants:

1. Nodes execute through Operator and Sidecar contracts.
2. Nodes do not mutate Workitem lifecycle fields directly.
3. Routing outcomes are limited to `route_to_output`, `route_to`, or `complete`.
4. Law writing is capability-gated; nodes without a `WRITE:law/tierN` capability grant cannot write laws.
5. Stamp-provider routing is configuration-discovered, not hardcoded by node name.
6. The Judiciary is always present: all Judiciary nodes (Arbiter, Tribunal, Advocate, Juror, Deliberation Gate, Clerk, Codification, Tribunal Router, Judiciary Gate) are Operator-provisioned and constrained to resolve/propose/appeal at their authority ceiling.
7. Hearing Workitems are standard Workitems (no `WorkitemType` or `spec.type`) with Tribunal entry/exit bindings.
8. Stamp authority is capability-scoped by governed artefact name and stamp name.
9. Stamps are write-once per artefact version hash.
10. Nodes admitting locally created Workitems are bound to and validated against entry contracts.
11. External integrations preserve auditability, idempotency, and governance checks.
12. Cross-flow handoff is export/import lifecycle, not local route transition.

Node configuration and implementation patterns are defined in [Node Configuration](../03-node/02-configuration.md) and [Node Patterns](../03-node/03-patterns.md). SDK behaviour is defined in [SDK Core](../04-sdk/01-sdk-core.md), [SDK Artefacts](../04-sdk/02-sdk-artefacts.md), [SDK Legal](../04-sdk/03-sdk-legal.md), [SDK Feedback](../04-sdk/04-sdk-feedback.md), and [SDK Workitems](../04-sdk/05-sdk-workitems.md).
