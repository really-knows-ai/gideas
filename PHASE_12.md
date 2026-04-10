# Phase 12 - Federation Foundations (Proto, Service Schema, Publication Lifecycle)

This phase lays down the Federation service contract, configuration model, and
the operator/SDK integration needed before the Federation service can be
implemented.

#### 12.1 Federation Wire Protocol

- Create `proto/flow/v1/federation.proto` for federation join / bootstrap,
  membership snapshots, publication submission, rejection reports, and law
  distribution.
- Define:
  - federation join and trust bootstrap,
  - state / group membership and relationship discovery,
  - petition target discovery for `law-petition`,
  - published-law submission + accept/reject response,
  - structured publication conflict / rejection report,
  - accepted law distribution to subscribers.
- Decide how accepted publications materialise as Tier 4 or Tier 5 in
  subscriber Flows.
- Regenerate `gen/flow/v1/`.

#### 12.2 Federation Service Schema and API Types

- Define federation membership, trust bootstrap, state / organisational-unit
  groupings, authority publisher roles, petition-routing relationships, and
  publication audiences.
- Validation rules:
  - only authorised publisher Flows may publish local Tier 3 laws outward,
  - federation publication roles must be unambiguous for a given Flow.

#### 12.3 Operator and SDK Support (Federation)

- Operator:
  - project federation trust material and topology/config needed by Embassy,
  - stop owning the higher-tier law publication lifecycle.
- Federation plumbing:
  - define how source Flows submit `published` Tier 3 laws and receive
    rejection reports,
  - define how subscriber Flows ingest accepted Tier 4 / Tier 5 publications,
  - keep Federation interactions outside node-local routing except where
    Embassy needs target discovery.

#### 12.4 Dispute Record Support

- Add `CreateDisputeRecord`, `RetireDisputeRecord`, and `GetActiveDisputes`
  RPCs to the Librarian proto.
- Dispute records link a `petition_id` to cited `law_ids` and carry `active` /
  `retired` status.
- Extend Sort's evaluation path to query active dispute records and route to
  `pending-hold` when cited laws are in dispute.

#### 12.5 Petition-Outcome-Watcher Foundations

- Define the Federation event contract that the petition-outcome-watcher
  subscribes to (acceptance and rejection events carrying `petition_id`).
- Define the watcher's Librarian interactions: `RetireDisputeRecord` and
  workitem resume on outcome.
