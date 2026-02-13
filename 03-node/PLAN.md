# 03-node Plan

`03-node/` defines the internal runtime architecture for building and operating nodes inside Foundry Flow. Detailed writing stubs now live in each target file.

## Stub Documents

1. [Node Runtime Overview](./00-overview.md)
2. [Sidecar Boundary](./01-sidecar.md)
3. [Node Configuration Semantics](./02-configuration.md)
4. [Node Implementation Patterns](./03-patterns.md)

## Suggested Writing Order

1. [Node Runtime Overview](./00-overview.md)
2. [Sidecar Boundary](./01-sidecar.md)
3. [Node Configuration Semantics](./02-configuration.md)
4. [Node Implementation Patterns](./03-patterns.md)

## Done Criteria

- Each document preserves terminology and invariants from `AGENTS.md` and `02-flow/`.
- No document reintroduces superseded constructs (`WorkitemType`, `spec.type`, context bag, terminal-by-shape semantics).
- `03-node/` runtime boundaries remain consistent with `01-concepts/01-architecture.md` and `01-concepts/02-data-model.md`.
- Cross-links to `04-sdk/` and `05-reference/` exist where API and schema details are delegated.
- Markdown lint passes from `lint/`.

## 03-node Invariants

1. Operator is the sole authority for Workitem control-plane transition persistence.
2. Sidecar is the mandatory mediation boundary for node-originated runtime operations.
3. One assignment has one active assignee and one routing outcome.
4. Nodes do not mutate Workitem lifecycle fields directly.
5. `complete()` is exit-node-only and validated by Operator against the node's bound exit contract.
6. Entry and exit semantics are binding-driven (`entry`, `exit`), not inferred from topology shape.
7. No `WorkitemType`, no `spec.type`, and no freeform Workitem context bag semantics.
8. Forge reads laws for context seeding and does not write laws.
9. Sort decision order is fixed in the reference arrangement and missing-stamp providers are configuration-discovered.
10. Stamp authority is capability-scoped (`STAMP:artefact/<kind>/<stamp-name>`) and write-once per artefact version.
11. Assay is a mandatory Flow component with authority ceiling: resolve Tier 1-2, propose Tier 3, appeal Tier 4-5.
12. Imported Workitems start at configured `importNode`, which must be entry-bound.
13. Node code may call external business services, but authenticated Flow runtime operations remain Sidecar-mediated.
