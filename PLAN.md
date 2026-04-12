# Judiciary, Embassy, and Federation Architecture Redesign Plan

This file is the canonical entrypoint for future sessions. If a prompt says
"continue the work in `@PLAN.md`", follow the `Next active work` item and use
the linked phase files plus `ARCHITECTURE.md` as the detailed source of truth.

## Status

- Phases 1-8 are complete.
- Phase 9 implementation is complete.
- Phase 9.10 (`Rewrite Advocate`) is cancelled / superseded by the Embassy + Federation redesign.
- Phase 10 specification rewrite is complete and reviewed.
- Phase 11 implementation is complete and reviewed against the corrected built-in import type model.
- Next active work: `PHASE_12.md`.

## Planning Files

- `ARCHITECTURE.md` - target-state judiciary, Embassy, and Federation architecture; topology; node inventory; publication model; and current open items.
- `PHASE_10.md` - spec rewrite for Embassy, Federation service, law publication, and cross-flow redesign.
- `PHASE_11.md` - CRD / proto / operator / SDK foundations for Embassy.
- `PHASE_12.md` - Federation foundations (proto, service schema, publication lifecycle, dispute records).
- `PHASE_13.md` - Embassy node, Federation service, petition-outcome-watcher, and Clerk / authority wiring.
- `PHASE_14.md` - Judiciary manifests (FoundryNode CRDs, Deployments, ConfigMaps).
- `PHASE_XX.md` - cleanup, deletion, validation, and final regression gates (always last).
- `PHASES_01_09.md` - completed and superseded implementation history.

## Active Execution Order

1. `PHASE_10.md` - complete and reviewed: specs rewritten around Embassy, Federation membership / roles, `law-petition`, published law distribution, `crossFlow.importTypes`, Treaties for non-federation exchange, and `imported-*` naturalisation.
2. `PHASE_11.md` - complete: Embassy CRD / proto / operator / SDK foundations aligned around platform-owned Embassy / judiciary nodes and built-in import types.
3. `PHASE_12.md` - next active phase: land Federation proto / service schema / publication lifecycle / dispute record foundations.
4. `PHASE_13.md` - implement `nodes/embassy/`, Federation service, petition-outcome-watcher, and Clerk / authority wiring.
5. `PHASE_14.md` - write judiciary FoundryNode CRDs, Deployments, and ConfigMaps.
6. `PHASE_XX.md` - delete Advocate, remove stale `importNode` / Governance Flow paths, and run final quality gates.

## Architecture Summary

- There is no special Governance Flow runtime. Ordinary Flows join a Federation that defines trust, membership, and role / relationship policy.
- States are federation-defined groups of Flows; sibling relationships derive from shared state membership.
- Embassy replaces Advocate as the symmetric cross-flow Workitem boundary.
- Embassy intake no longer uses a singular `importNode`. Effective import types are the merged set of platform-owned system import types and flow-authored `crossFlow.importTypes`. `law-petition` is a built-in system import type: always present/configured per Flow and not user-defined in YAML.
- Higher-authority escalation is an Embassy-mediated `law-petition`; downward law publication / replication is a separate Federation service path.
- Law-authority Flows publish approved local Tier 3 laws outward when marked `published`; subscriber Flows materialise them as Tier 4 or Tier 5 based on publisher authority.
- On the T4-5 petition path, law-applicator creates a **dispute record** in the Library (not a law) linking the `petition_id` to cited law IDs. Sort routes workitems citing disputed laws to `pending-hold` instead of re-deadlocking. A **petition-outcome-watcher** retires dispute records and resumes held workitems when the authority accepts or rejects the petition.
- Embassy uses mTLS plus a signed manifest / header and emits local `imported-*` naturalisation stamps for verified foreign stamps.
- If this file conflicts with older historical planning notes, `ARCHITECTURE.md` and the active phase files win.
