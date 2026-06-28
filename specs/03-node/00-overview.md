# Node Runtime Overview

[Nodes](../01-concepts/00-overview.md) execute assignment-scoped work inside a Flow while preserving [Flow Operator](../02-flow/01-operator.md) control-plane authority. Node code performs business execution; lifecycle transitions, routing guard enforcement, and completion validation remain Operator responsibilities.

## Node Runtime Boundary

Nodes are [data-plane executors](../01-concepts/01-architecture.md#data-plane). See [Runtime Composition](../02-flow/00-overview.md#runtime-composition) for the fixed boundary responsibilities across Operator, Sidecar, Archivist, and Librarian.

## Assignment Lifecycle from the Node Perspective

Assignment handling is single-owner and single-outcome. See [Runtime Loop](../02-flow/00-overview.md#runtime-loop) for the end-to-end sequence diagram and [Service Interaction](../02-flow/00-overview.md#runtime-composition) for the component interaction model.

## Node Categories

Nodes fall into five categories based on their runtime role:

- **Business nodes** — execute assignment-scoped work (content generation, review, refinement). Examples: [Forge](../01-concepts/02-foundry-cycle.md#forge-creator), [Appraise](../01-concepts/02-foundry-cycle.md#appraise-reviewer), [Refine](../01-concepts/02-foundry-cycle.md#refine-refiner).
- **Gate nodes** — evaluate governance state and route accordingly. Example: [Sort](../01-concepts/02-foundry-cycle.md#sort-gate).
- **Judiciary nodes** — resolve disputes, conduct hearings, codify rulings, and apply law changes. Examples: [Facilitator](../01-concepts/02-foundry-cycle.md#facilitator), [Arbiter](../01-concepts/02-foundry-cycle.md#arbiter-deadlock-resolver), [Tribunal](../01-concepts/02-foundry-cycle.md#tribunal-hearing-conductor), [Juror](../01-concepts/02-foundry-cycle.md#juror-judicial-agent), [Clerk cycle](../01-concepts/02-foundry-cycle.md#clerk-cycle) nodes, [Rule Router](../01-concepts/02-foundry-cycle.md#rule-router), [law-applicator](../01-concepts/02-foundry-cycle.md#law-applicator).
- **HITL nodes** — human-in-the-loop decision points using the [generic config-driven HITL pattern](../04-sdk/08-sdk-hitl.md). Examples: hitl-appraise, arbiter-hitl-resolve, tribunal-hitl-resolve.
- **Embassy** — the standard [cross-flow boundary node](../02-flow/06-cross-flow.md), present in every Flow. Handles outbound export and inbound import of Workitems using a signed manifest and streamed package protocol. The Embassy is a first-class node pattern because it owns a node-to-node transfer protocol (Embassy-to-Embassy) that operates outside Sidecar mediation for the cross-flow transfer itself, while still using Sidecar-mediated paths for local service access (Archivist, Operator).

[Federation service](../02-flow/08-federation.md) interactions (membership, published-law distribution, authority endpoint discovery) are external platform-service relationships, not node-local routing. Nodes do not interact with the Federation service directly — the Embassy queries it for authority endpoints, and the Librarian receives distributed laws from it.

## Ownership and Mutation Boundaries

Workitems carry no `WorkitemType` or `spec.type` discriminator and no freeform context bag. All work context is represented by explicit governed artefacts.

See [Governance Runtime Mechanics](../02-flow/00-overview.md#governance-runtime-mechanics) and [Exit Completion Model](../02-flow/00-overview.md#exit-completion-model) for the capability-gating model, completion semantics, and judiciary authority bounds.

## Relationship to SDK Documents

This document defines runtime authority and boundary semantics. API-level behaviour lives in:

- [SDK Core](../04-sdk/01-sdk-core.md)
- [SDK Artefacts](../04-sdk/02-sdk-artefacts.md)
- [SDK Legal](../04-sdk/03-sdk-legal.md)
- [SDK Feedback](../04-sdk/04-sdk-feedback.md)
- [SDK Workitems](../04-sdk/05-sdk-workitems.md)
- [SDK Telemetry](../04-sdk/06-sdk-telemetry.md)

Wire and schema references remain in [gRPC API](../05-reference/grpc-api.md) and [CRD Reference](../05-reference/crds.md).

