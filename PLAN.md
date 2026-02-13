# Node and SDK Authoring Plan

This plan defines how to complete the developer-facing specification after the `01-concepts/` and `02-flow/` drafts.

## Locked Structure Decision

The spec structure is split so runtime internals and developer APIs are separate top-level sections:

- `03-node/` -> internal node architecture and sidecar boundary.
- `04-sdk/` -> external developer programming interface.
- `05-reference/` -> renumbered quick-reference section.

This split is normative for all new writing and link updates.

## Scope and Goals

### Primary goals for `03-node/`

`03-node/` defines how node runtime components behave inside the Flow platform:

- Node process model and assignment execution contract at implementation level.
- Sidecar authentication and mediation boundary (identity brokering, service brokering, transport policy).
- Node-local configuration semantics and implementation constraints.
- Reusable implementation patterns that preserve runtime invariants.

### Primary goals for `04-sdk/`

`04-sdk/` defines the developer contract exposed to node authors:

- SDK mental model and assignment-scoped execution surface.
- Artefact, legal, feedback, Workitem, and telemetry APIs.
- Error and capability semantics at API boundaries.
- Practical behavioural guarantees needed to write correct nodes.

## Non-Negotiable Invariants to Preserve

All `03-node/` and `04-sdk/` documents must remain consistent with these decisions:

- Nodes do not mutate Workitem control-plane state directly.
- Sidecar mediates and authenticates node actions; runtime services enforce capability boundaries.
- Operator validates routing and contract transitions.
- `complete()` is exit-node-only and Operator-validated.
- Workitems carry artefact references only (`id`, `kind`); provenance is Archivist-owned.
- No `WorkitemType`, no `spec.type`, no context bag.
- Sort routing order and stamp semantics remain unchanged.
- Forge reads laws only and does not write laws.
- Assay authority ceiling remains resolve/propose/appeal.

## File Inventory and Deliverables

### `03-node/` (internal architecture)

1. `03-node/00-overview.md`
   - Purpose: define node runtime shape and boundaries.
   - Must cover: execution lifecycle, control/data boundaries, relationship to Sidecar and SDK section.

2. `03-node/01-sidecar.md`
   - Purpose: define Sidecar as runtime authentication and identity mediation boundary.
   - Must cover: assigned Workitem mediation, authenticated brokering, service-authorisation boundary, failure behaviour.

3. `03-node/02-configuration.md`
   - Purpose: define node-local configuration that realises Flow semantics.
   - Must cover: bindings, outputs, capabilities, timeout implications, validation expectations.

4. `03-node/03-patterns.md`
   - Purpose: provide implementation patterns and anti-patterns.
   - Must cover: idempotency, retriable side effects, human-in-loop patterns, export-boundary nodes, governance-safe loops.

### `04-sdk/` (developer interface)

1. `04-sdk/00-overview.md`
   - Purpose: define SDK scope, lifecycle, and behavioural guarantees.

2. `04-sdk/01-sdk-core.md`
   - Purpose: core types, handler contract, assignment scoping, routing return model.

3. `04-sdk/02-sdk-artefacts.md`
   - Purpose: artefact read/write/version/passport/stamp interactions.

4. `04-sdk/03-sdk-legal.md`
   - Purpose: law query, citation, and finding-record surfaces.

5. `04-sdk/04-sdk-feedback.md`
   - Purpose: feedback creation, resolution, deadlock-related interaction boundaries.

6. `04-sdk/05-sdk-workitems.md`
   - Purpose: Workitem-facing SDK actions, local creation admission, and routing constraints.

7. `04-sdk/06-sdk-telemetry.md`
   - Purpose: telemetry and friction emission semantics, attribution shape, operational expectations.

## Proposed Writing Order (Order of Attack)

### Phase 0 - Structural migration

1. Update `AGENTS.md` structure, reading order, and path references for `04-sdk/` and `05-reference/`.
2. Update cross-links in completed docs:
   - `../03-node/02..07-sdk-*.md` -> `../04-sdk/01..06-sdk-*.md`
   - `../03-node/08-configuration.md` -> `../03-node/02-configuration.md`
   - `../03-node/09-patterns.md` -> `../03-node/03-patterns.md`
   - `../04-reference/*` -> `../05-reference/*`

### Phase 1 - Node architecture backbone

1. Write `03-node/00-overview.md`.
2. Write `03-node/01-sidecar.md`.
3. Write `03-node/02-configuration.md`.
4. Write `03-node/03-patterns.md`.

### Phase 2 - SDK contract set

1. Write `04-sdk/00-overview.md`.
2. Write `04-sdk/01-sdk-core.md`.
3. Write `04-sdk/02-sdk-artefacts.md`.
4. Write `04-sdk/03-sdk-legal.md`.
5. Write `04-sdk/04-sdk-feedback.md`.
6. Write `04-sdk/05-sdk-workitems.md`.
7. Write `04-sdk/06-sdk-telemetry.md`.

### Phase 3 - Consistency and quality gates

1. Run cross-document consistency pass across `01-concepts/`, `02-flow/`, `03-node/`, `04-sdk/`.
2. Ensure first-mention cross-links exist for concepts with detail pages.
3. Validate terminology and invariant consistency against `AGENTS.md`.
4. Run lint in `lint/` and fix until clean.

## Dependency Notes

- `03-node/01-sidecar.md` depends on `03-node/00-overview.md` boundary definitions.
- `04-sdk/01-sdk-core.md` depends on Sidecar boundary semantics from `03-node/01-sidecar.md`.
- SDK domain docs (`02..06`) depend on stable terminology and contracts in `04-sdk/01-sdk-core.md`.
- `03-node/03-patterns.md` should cross-link to SDK docs only after `04-sdk/` skeleton exists.

## Definition of Done

The node + SDK plan is complete when:

- `03-node/` and `04-sdk/` files above are drafted with consistent terminology.
- All moved/renumbered links resolve to new section paths.
- No document reintroduces superseded constructs (`WorkitemType`, `spec.type`, context bag).
- Runtime authority boundaries remain implementable and non-contradictory.
- Markdown lint passes cleanly.
