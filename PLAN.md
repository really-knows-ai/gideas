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
- Phase 12 Federation foundations are complete (proto, service schema, publication lifecycle, dispute records, SDK client, petition-outcome helpers, architectural guard tests).
- Phase 13 Embassy node (13.1-13.5) is complete. Federation service slices 13.6-13.7 are being rewritten (SQLite -> Kubernetes CRDs).
- Next active work: `PHASE_13.md` slice 13.8.1 (Federation service: SubmitPublication - authority validation).

### Phase 13 Federation Redesign

The Federation service has been redesigned from a standalone SQLite-backed gRPC
service to a **Kubebuilder controller + gRPC server** with CRD-based storage:

- `FederationMember` and `FederationState` CRDs replace the SQLite membership
  and states tables. All persistent state lives in K8s CRDs backed by etcd.
- Publication admission uses **distributed semantic search** across publisher
  Librarians (via new `SearchSimilarLaws` RPC using sqlite-vec) plus **LLM
  conflict analysis** (via SDK provider abstraction / Ollama). There is no
  federation-level publication registry.
- Accepted publications are distributed automatically to subscriber Flows via
  `SubscribeLawUpdates` streaming; materialisation into subscriber Libraries
  as T4/T5 is automatic (no HITL ratification).
- The Federation service is architecturally independent of the Flow operator --
  it runs in its own namespace and manages its own CRDs.

Slices 13.6 and 13.7 are marked as superseded in PHASE_13.md and replaced
with CRD-backed equivalents. Slices 13.8+ are revised for the new distributed
conflict detection model. New slice 13.7B adds `SearchSimilarLaws` to the
Librarian.

### Phase 11 Landed

- `FoundryFlow.spec.crossFlow.importTypes` and `Treaty.spec.allowedImportTypes` are implemented as the Embassy configuration foundation.
- Built-in system import types are modelled explicitly: `law-petition` is always present/configured per Flow, rejected from user YAML, and allowed in Treaty policy.
- `proto/flow/v1/embassy.proto` exists with signed manifest/header fields, artefact inventory, foreign stamp records, preflight, and streamed package transfer.
- The old Operator-centric `ExportWorkitem` / `ImportWorkitem` path has been removed from active proto/operator/sidecar usage.
- The Operator now validates the effective import-type model and auto-provisions Embassy infrastructure with projected trust/system config.
- The SDK now includes Embassy client, server, staging/materialisation, import-type resolution, and trust-policy foundations.
- Specs and planning docs are aligned to the platform-owned Embassy / judiciary node and built-in import type model.

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
3. `PHASE_12.md` - complete: Federation proto / service schema / publication lifecycle / dispute record foundations landed.
4. `PHASE_13.md` - next active phase: `nodes/embassy/` (13.1-13.5 complete), Federation service CRD rewrite (13.6-13.7 superseded and rewritten), Librarian semantic search (13.7B new), distributed publication admission (13.8 revised), law distribution (13.9), petition-outcome-watcher (13.10), law-applicator T4-5 wiring (13.11), operator/sidecar integration (13.12-13.13).
5. `PHASE_14.md` - write judiciary FoundryNode CRDs, Deployments, and ConfigMaps.
6. `PHASE_XX.md` - delete Advocate, remove stale `importNode` / Governance Flow paths, and run final quality gates.

## Architecture Summary

- There is no special Governance Flow runtime. Ordinary Flows join a Federation that defines trust, membership, and role / relationship policy.
- The Federation service is a Kubebuilder controller + gRPC server with its own CRDs (`FederationMember`, `FederationState`). No SQLite. Architecturally independent of the Flow operator.
- States are federation-defined groups of Flows; sibling relationships derive from shared state membership. Declared as `FederationState` CRs by the federation admin.
- Embassy replaces Advocate as the symmetric cross-flow Workitem boundary.
- Embassy intake no longer uses a singular `importNode`. Effective import types are the merged set of platform-owned system import types and flow-authored `crossFlow.importTypes`. `law-petition` is a built-in system import type: always present/configured per Flow and not user-defined in YAML.
- Higher-authority escalation is an Embassy-mediated `law-petition`; downward law publication / replication is a separate Federation service path.
- Law-authority Flows publish approved local Tier 3 laws outward when marked `published`. The Federation service validates authority, runs distributed semantic search across publisher Librarians + LLM conflict analysis, and either accepts (automatic T4/T5 materialisation in subscribers) or hard-rejects with a structured report.
- On the T4-5 petition path, law-applicator creates a **dispute record** in the Library (not a law) linking the `petition_id` to cited law IDs. Sort routes workitems citing disputed laws to `pending-hold` instead of re-deadlocking. A **petition-outcome-watcher** retires dispute records and resumes held workitems when the authority accepts or rejects the petition.
- Embassy uses mTLS plus a signed manifest / header and emits local `imported-*` naturalisation stamps for verified foreign stamps.
- If this file conflicts with older historical planning notes, `ARCHITECTURE.md` and the active phase files win.
