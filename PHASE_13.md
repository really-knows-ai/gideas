# Phase 13 - Embassy Node and Federation Service Implementation

Implement the new boundary node and the new federation control-plane service,
then wire them into the Clerk / authority path.

## 13.1 Embassy Scaffold (`nodes/embassy/`)

- Create `nodes/embassy/main.go`, `main_test.go`, `testutil_test.go`.
- Use the persistent entry-node pattern (`StartEntry`) so Embassy can run an
  inbound transfer service continuously while still handling locally created
  import Workitems.

## 13.2 Inbound Manifest Preflight

- Accept signed manifests.
- Resolve `importType` against the effective local import-type registry
  (built-in system import types plus flow-authored `crossFlow.importTypes`).
- Verify:
  - trust source,
  - expiry/nonce,
  - target node existence and entry binding,
  - required foreign stamps for each governed artefact.
- Reject before requesting the full package if the import is inadmissible.

## 13.3 Package Streaming and Verification

- Request/receive the full package only after preflight acceptance.
- Verify package digest/signature.
- Stage the package (scratch storage if needed) before materialisation.
- Ensure streamed bytes match the signed manifest inventory.

## 13.4 Materialisation, Naturalisation, and Routing

- Create a new local Workitem.
- Unpack imported artefacts into the local Workitem.
- For every verified required foreign stamp, apply the local
  `imported-<stamp>` attestation stamp.
- Route the imported Workitem according to the resolved effective import-type
  policy (platform-owned or flow-authored).

## 13.5 Outbound Export

- Build the signed manifest from the local Workitem's export-eligible artefacts.
- Resolve the target authority Flow / import type (`law-petition`) from
  federation policy and the built-in system import-type registry.
- Connect to the remote Embassy over mTLS.
- Send the manifest, wait for acceptance, then stream the full package.
- Complete/fail the local Workitem according to the configured handoff policy.

## 13.6 Federation Service Scaffold

- Create the new federation service implementation, tests, and deployment
  wiring.
- Support join / bootstrap, membership, trust bundle distribution, and endpoint
  discovery.

## 13.7 Federation Roles, Relationships, and Discovery

- Implement states / organisational-unit memberships.
- Implement authority publisher roles (state-level vs federation-level, with
  room for scoped authorities such as security / risk / finance).
- Expose petition target discovery for the built-in `law-petition` path.

## 13.8 Published Law Submission and Conflict Rejection

- When a local Tier 3 law is marked `published`, submit it to the Federation
  service.
- Validate publisher authority and detect conflicts before distribution.
- Return structured rejection / error reports to the source Flow when
  publication is inadmissible.

## 13.9 Published Law Distribution

- Accept state and federation publications and distribute them to subscriber
  Flows.
- Materialise accepted laws locally as Tier 4 or Tier 5 with source / petition
  provenance.

## 13.10 Petition-Outcome-Watcher Implementation

- Implement `nodes/petition-watcher/`.
- Subscribe to Federation events for petition outcomes (acceptance, rejection).
- On acceptance: retire the dispute record via `Librarian.RetireDisputeRecord`,
  resume `pending-hold` workitems.
- On rejection: retire the dispute record, create a new Clerk cycle Workitem
  with the rejection report as context, resume held workitems.

## 13.11 Clerk / Authority Wiring

- Replace the T4-5 Clerk-cycle terminal route from `advocate` to
  `law-applicator` → `embassy`.
- law-applicator on the T4-5 path creates a dispute record via
  `Librarian.CreateDisputeRecord` (linking `petition_id` to cited law IDs)
  before routing to Embassy.
- Use the built-in `law-petition` export path.
- Ensure the local work does not wait for remote authority deliberation after
  Embassy handoff.
- Add tests that exercise the `law-petition` export path, dispute record
  creation, `pending-hold` routing, and accepted / rejected publication flows.
- Update build / deployment wiring for Embassy and the Federation service.
