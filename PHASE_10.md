# Phase 10 - Specification Updates (Embassy, Federation, and Cross-Flow Revision)

Status: complete and reviewed.

This phase is done. The next active implementation phase is `PHASE_11.md`.

This phase rewrites the specs around Embassy, Federation membership / roles,
`law-petition`, published law distribution, Treaties for non-federation
exchange, and Embassy-issued `imported-*` naturalisation stamps.

Update the specs to reflect the Embassy redesign, the new Federation service
model, and the removal of Governance Flow as a runtime primitive. This is a
second major spec pass after Phase 1.

Core goals:

- Replace Advocate with Embassy everywhere.
- Remove Governance Flow as a runtime concept; define Federation membership,
  state grouping, and authority publisher relationships.
- Reserve `law-petition` as the built-in higher-authority Embassy import type.
- Separate Embassy Workitem transfer from Federation law publication /
  distribution.
- Restore Tier 4 / Tier 5 as state-published vs federation-published external
  laws in subscriber Flows.
- Define publication submission, conflict rejection, and non-blocking
  higher-authority escalation (including dispute records for in-flight petitions).

#### 10.1 Core Concept Specs

| File | Changes |
|---|---|
| `specs/01-concepts/00-overview.md` | Replace Governance Flow language with Federation membership, states as grouped Flows, authority publishers, and the Embassy / Federation split. Add Embassy and Federation service to the built-in runtime picture. |
| `specs/01-concepts/01-architecture.md` | Update the six-plane model so Embassy handles Workitem crossings while Federation service handles identity, topology, and published-law distribution. Clarify federation-member vs Treaty trust sources. |
| `specs/01-concepts/02-foundry-cycle.md` | Remove the Advocate section. Add Embassy as the higher-authority petition boundary. Rewrite the Clerk-cycle exit path so Tier 4-5 petitions route to Embassy as `law-petition`s, not to a Governance Flow. Clarify the local cycle completes after handoff, not after remote ratification. |
| `specs/01-concepts/03-data-model.md` | Restore Tier 4 / Tier 5 as state-published vs federation-published external laws. Define `published` local laws, petition correlation IDs, dispute records (for in-flight petitions), and imported foreign provenance vs local `imported-*` attestation stamps. |
| `specs/01-concepts/04-governance.md` | Rewrite governance around Federation membership, states / organisational units, authority publisher roles, upward `law-petition`s, and downward publication. Remove Governance Flow as a special runtime. |

#### 10.2 Platform Specs

| File | Changes |
|---|---|
| `specs/02-flow/00-overview.md` | Update runtime inventory and lifecycle summary for Embassy Workitem transfer, Federation membership / publishing, and Tier 4 / Tier 5 distribution. |
| `specs/02-flow/01-operator.md` | Clarify Operator responsibilities: Embassy auto-provisioning, federation trust / config projection, and `crossFlow.importTypes` validation, but no ownership of higher-tier law publication / distribution. |
| `specs/02-flow/02-workitem.md` | Rewrite imported Workitem lifecycle so Embassy materialises imported petitions locally, and clarify higher-authority submission is fire-and-forget beyond local handoff. |
| `specs/02-flow/03-nodes-external.md` | Replace Advocate with Embassy in the node catalogue. Clarify Federation service is a platform service, not a node. |
| `specs/02-flow/04-system-services.md` | Add Federation service for join, trust, state / group relationships, publication admission, conflict detection, and Tier 4 / Tier 5 distribution. Remove stale Governance Flow / Advocate wording. |
| `specs/02-flow/05-configuration.md` | Replace `importNode` with `spec.crossFlow.importTypes`. Define `law-petition`, flow-specific import types, and entry-bound node requirements. Clarify federation topology and publication roles live in Federation service policy, not in Embassy config. |
| `specs/02-flow/06-cross-flow.md` | Full rewrite around Embassy manifests, package streaming, `law-petition`, Treaty `allowedImportTypes` for non-federation exchange, and Embassy-applied `imported-*` stamps. Separate this from Federation law publication / distribution. |
| `specs/02-flow/07-operations.md` | Update operational guidance for Embassy endpoints plus Federation membership, publication flows, and validation / rejection handling. |
| `specs/02-flow/08-federation.md` | **New file.** Define federation membership, trust bootstrap, state groupings, authority publisher roles, petition-routing relationships, publication admission, conflict rejection, and Tier 4 / Tier 5 distribution. |

#### 10.3 Node and SDK Specs

| File | Changes |
|---|---|
| `specs/03-node/00-overview.md` | Add Embassy as a first-class node pattern and clarify Federation service interactions are external platform-service relationships, not node-local routing. |
| `specs/03-node/01-sidecar.md` | Document the boundary between Sidecar-mediated local APIs, Embassy's node-owned transfer protocol, and Federation service control-plane interactions. |
| `specs/03-node/03-patterns.md` | Add an Embassy `law-petition` import/export pattern: signed manifest preflight, streamed package transfer, local Workitem materialisation, naturalisation, routing. Remove Advocate-specific HITL cross-flow guidance. |
| `specs/04-sdk/00-overview.md` | Add Embassy / cross-flow SDK surface to the overview and remove Advocate / Governance Flow-specific language. |
| `specs/04-sdk/05-sdk-workitems.md` | Clarify how imported Workitems are materialised, what local code sees after Embassy naturalisation, and how imported petitions enter the receiving Flow. |
| `specs/04-sdk/08-sdk-hitl.md` | Delete the Advocate section. HITL becomes fully generic with no Judiciary-specific boundary node. |
| `specs/04-sdk/09-sdk-cross-flow.md` | **New file.** Define Embassy manifest/package transfer, `law-petition` handoff, staging / materialisation responsibilities, and naturalisation semantics for node implementers. |

#### 10.4 Reference Specs

| File | Changes |
|---|---|
| `specs/05-reference/crds.md` | Replace `importNode` with `crossFlow.importTypes`; reserve `law-petition`; add Federation service / publication references; replace Advocate with Embassy in the node inventory and glossary links. |
| `specs/05-reference/grpc-api.md` | Replace the operator-centric `ExportWorkitem` / `ImportWorkitem` description with the Embassy transfer API and add Federation service APIs for membership, publication submission, conflict rejection, and distribution. |
| `specs/05-reference/error-catalogue.md` | Add Embassy errors (`unknown import type`, `header rejected`, `foreign stamp invalid`, `package digest mismatch`, Treaty import type denied, naturalisation failure) plus Federation publication errors (`unauthorised publish`, `conflicting published law`, `unknown state / scope`, `publication rejected`). |
| `specs/05-reference/glossary.md` | Remove Advocate and Governance Flow runtime terms. Add Federation, State, authority publisher, `law-petition`, published law, dispute record, imported attestation stamps, and the revised naturalisation model. |

Estimated scope: ~24-32 spec files including one new platform spec and one new SDK spec.
