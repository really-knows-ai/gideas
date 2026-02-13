# 03-node Plan

## Section Purpose

`03-node/` defines the internal runtime architecture for building and operating nodes inside Foundry Flow. It covers runtime boundaries, sidecar enforcement, node configuration, and implementation patterns. It does not define SDK API surfaces in detail.

## Goals

- Define node execution semantics that are implementable and consistent with `02-flow/`.
- Specify Sidecar as the mandatory policy, identity, and mediation boundary.
- Capture node-local configuration behaviour that realises Flow-level semantics.
- Provide reusable implementation patterns that preserve governance and control-plane invariants.

## Planned Files

1. `03-node/00-overview.md`
2. `03-node/01-sidecar.md`
3. `03-node/02-configuration.md`
4. `03-node/03-patterns.md`

## File-by-File Scope

## `00-overview.md`

- Node runtime role and boundaries.
- Assignment lifecycle from node perspective.
- Interaction model across Operator, Sidecar, Archivist, and Librarian.
- Cross-links to `04-sdk/` for developer APIs.

## `01-sidecar.md`

- Identity and trust responsibilities.
- Capability enforcement and API brokering.
- Assignment lease and Workitem scoping guarantees.
- Failure behaviour, fail-closed paths, and audit signals.

## `02-configuration.md`

- Node-local settings: outputs, capabilities, entry/exit bindings, timeout budgets.
- How FoundryNode config interacts with FoundryFlow config.
- Validation expectations and rejection scenarios.
- Runtime implications of config drift and rollout.

## `03-patterns.md`

- Idempotent handlers and retry-safe side effects.
- Human-in-loop and external integration patterns.
- Governance-safe feedback/refine loop patterns.
- Anti-patterns that violate invariants.

## Writing Order

1. `00-overview.md`
2. `01-sidecar.md`
3. `02-configuration.md`
4. `03-patterns.md`

## Consistency Checklist

- Preserve Operator ownership of Workitem control-plane transitions.
- Preserve Sidecar mediation for node-originated actions.
- Preserve `complete()` as exit-node-only and Operator-validated.
- Preserve no `WorkitemType`, no `spec.type`, no context bag.
- Keep Sort routing order and stamp semantics unchanged.
