# 05-reference Plan

## Section Purpose

`05-reference/` is the quick-lookup layer for exact shapes, wire contracts, errors, and vocabulary. It is normative for schemas and API definitions, and concise by design.

## Goals

- Provide a single lookup surface for implementation-critical details.
- Keep reference material aligned with the behavioural semantics in `02-flow/`, `03-node/`, and `04-sdk/`.
- Minimise interpretation by using explicit tables and canonical field names.
- Prevent drift between prose docs and schemas.

## Planned Files

1. `05-reference/crds.md`
2. `05-reference/grpc-api.md`
3. `05-reference/error-catalog.md`
4. `05-reference/glossary.md`

## File-by-File Scope

## `crds.md`

- Canonical CRD surfaces for FoundryFlow, FoundryNode, Workitem, GovernedArtefact, and law objects.
- Required and optional fields, constraints, and behavioural notes.
- Contract shape definitions (`entryContracts`, `exitContracts`) and binding fields.

## `grpc-api.md`

- Service and method inventory by component boundary.
- Request/response shape summaries and key status mappings.
- Call-path notes where behaviour depends on authority boundaries.

## `error-catalog.md`

- Stable error IDs/codes and meanings.
- Typical causes, actor boundary, and caller response guidance.
- Mapping to key runtime guard failures.

## `glossary.md`

- Canonical term definitions.
- Superseded term markers to prevent legacy drift.
- Cross-links to first normative detail locations.

## Writing Order

1. `glossary.md` (terminology baseline)
2. `crds.md`
3. `grpc-api.md`
4. `error-catalog.md`

## Consistency Checklist

- Field names match current decisions in `AGENTS.md`.
- No reintroduction of superseded terms (`entryNode`, `terminalContracts`, `WorkitemType`, context bag).
- Contract and capability syntax matches Flow and SDK docs.
- Error catalogue aligns with runtime guard and validation semantics.
