# Plan: Integrate FoundryAgent into Current Specs

## Problem

The legacy specs define **FoundryAgent** as a rich, first-class concept: an abstract base class that wraps LLM inference with automatic heartbeat management, schema-first output validation, and atomic cost accounting. It is the recommended pattern for all LLM-backed nodes and the runtime powering Assay's multi-agent jury mechanism.

The current specs reduce this to a **9-line paragraph** in `specs/03-node/03-patterns.md` under "Long-Running and Agent Patterns." There is no SDK-level definition, no telemetry contract, no glossary entry, and no connection to the Assay jury mechanism. Developers reading the current specs have no clear contract to program against for inference workloads.

## Goal

Establish FoundryAgent as a formal SDK-level behavioural contract — language-agnostic, consistent with current spec style — and wire it into the Assay jury mechanism. After this work, a developer should be able to:

1. Understand what FoundryAgent guarantees (managed liveness, schema validation, atomic accounting).
2. Know when to use it vs. raw handler + manual heartbeat.
3. Understand that Assay jurors are FoundryAgent instances with automatic per-juror cost attribution.

## Approach

Behavioural contract only — no language-specific types, class hierarchies, or implementation constants (e.g., no "15-second heartbeat interval"). The new document follows the same structure and tone as existing SDK surface docs (`02-sdk-artefacts.md`, `03-sdk-legal.md`, etc.).

---

## Changes

### 1. NEW: `specs/04-sdk/07-sdk-agent.md` — FoundryAgent Contract

**Priority:** Primary. Everything else links here.

A new dedicated SDK surface document. Sections:

- **FoundryAgent Runtime Role** — what it is: a managed wrapper for inference workloads that automates heartbeat, validates structured output, and emits cost telemetry atomically.
- **Behavioural Guarantees** — the three invariants:
  1. **Managed Liveness** — automatic `Heartbeat()` calls at regular intervals during inference, freeing the developer from manual timer management.
  2. **Schema-First Output Validation** — structured output is validated against a declared schema before it can be written to artefacts or returned as a routing decision. Malformed inference output fails fast.
  3. **Atomic Cost Accounting** — each inference step emits a `foundry.cost.llm` telemetry event immediately via `RecordTelemetry`. If the handler is interrupted, accounting reflects actual work performed, not batched totals.
- **Handler Contract** — how a FoundryAgent handler differs from a raw handler: developer implements an `Infer` method (receives validated input, returns output); the wrapper handles heartbeat lifecycle, output validation, and telemetry emission around it.
- **When to Use / When Not to Use** — recommended for all LLM-backed nodes. Not needed for deterministic validators, simple routing, or nodes that don't perform inference. Nodes performing inference without FoundryAgent must manage `Heartbeat()` manually.
- **Relationship to Assay Jury** — Assay's multi-agent deliberation uses FoundryAgent instances as jurors; cost accounting per juror is automatic with attribution tags (`juror`, `round`, `severity`, `feedback_id`).
- **FoundryAgent Invariants** — formal list.

### 2. EDIT: `specs/04-sdk/00-overview.md` — SDK Surface Map

- Add a row to the SDK Surface Map table for the `Agent` surface, pointing to `07-sdk-agent.md`.
- Brief mention near the `FlowSupportService` paragraph noting FoundryAgent as another base-class-level SDK construct.

### 3. EDIT: `specs/04-sdk/01-sdk-core.md` — Heartbeat Cross-Reference

- Add a paragraph at the end of "Heartbeat and Activity Tracking" (after line 81) cross-referencing FoundryAgent as the managed wrapper that automates heartbeat for inference workloads, with a link to `07-sdk-agent.md`.

### 4. EDIT: `specs/04-sdk/06-sdk-telemetry.md` — Cost Accounting Convention

- Add a subsection or paragraph (after "Operational Signal Quality", ~line 91) documenting the `foundry.cost.llm` event type convention that FoundryAgent emits, and the expected payload shape (model, tokens, duration — behavioural, not schema-prescriptive). Cross-reference `07-sdk-agent.md`.

### 5. EDIT: `specs/03-node/03-patterns.md` — Expand Agent Pattern

- Expand the current 9-line paragraph at lines 72–80 into a fuller pattern description that:
  - Describes the three guarantees (heartbeat, validation, accounting).
  - Links to `04-sdk/07-sdk-agent.md` as the authoritative contract.
  - Keeps it pattern-level guidance, not duplicating the SDK doc.

### 6. EDIT: `specs/02-flow/03-nodes-external.md` — Assay Jury Connection

- In the "Assay as Standard Component" section (~line 78), add a paragraph noting that Assay's deadlock adjudication uses the FoundryAgent pattern for jury deliberation — parallel FoundryAgent instances evaluate disputes, with automatic per-juror cost accounting through atomic telemetry emission.

### 7. EDIT: `specs/01-concepts/02-foundry-cycle.md` — Assay Role Mention

- In the Assay section (line 42–47), expand the "(potentially via a multi-agent jury)" parenthetical to reference the FoundryAgent pattern as the recommended implementation for jury members.

### 8. EDIT: `specs/05-reference/glossary.md` — New Glossary Entry

- Add **FoundryAgent** to the "Canonical Runtime Terms" section (alphabetically, between "Flow Support Service" and "Librarian"). Definition: managed inference wrapper providing automatic heartbeat, schema-first output validation, and atomic cost accounting. Cross-reference to `04-sdk/07-sdk-agent.md`.

---

## Files NOT Changed

| File | Reason |
|------|--------|
| `specs/03-node/01-sidecar.md` | Already describes heartbeat mechanics. The new SDK doc references it; no reciprocal update needed. |
| `specs/03-node/02-configuration.md` | Already references FoundryAgent at line 93. The existing link target (`03-patterns.md#long-running-and-agent-patterns`) still works. |
| `specs/05-reference/error-catalogue.md` | Already references FoundryAgent at line 37. No update needed. |
| `specs/05-reference/grpc-api.md` | FoundryAgent introduces no new gRPC surface; it wraps existing `Heartbeat()` and `RecordTelemetry()` calls. |
| `specs/05-reference/crds.md` | No CRD-level changes needed for a behavioural contract. |

## Execution Order

1. `specs/04-sdk/07-sdk-agent.md` — create the authoritative document first.
2. `specs/04-sdk/00-overview.md` — add SDK Surface Map row.
3. `specs/04-sdk/01-sdk-core.md` — add heartbeat cross-reference.
4. `specs/04-sdk/06-sdk-telemetry.md` — add cost accounting convention.
5. `specs/03-node/03-patterns.md` — expand pattern section.
6. `specs/02-flow/03-nodes-external.md` — add Assay jury connection.
7. `specs/01-concepts/02-foundry-cycle.md` — expand Assay role mention.
8. `specs/05-reference/glossary.md` — add glossary entry.

## Validation

After all changes:

- Every cross-reference link resolves to an existing heading.
- The glossary entry matches the authoritative definition in `07-sdk-agent.md`.
- No language-specific types, class signatures, or implementation constants appear in any spec file.
- The existing link targets in `03-node/02-configuration.md` and `05-reference/error-catalogue.md` remain valid.
- Run `tools/spec-lint/` to verify markdown hygiene.
