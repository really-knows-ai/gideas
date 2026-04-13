# Phase 13 - Embassy Node, Federation Service, and Clerk/Authority Wiring

Implement the Embassy boundary node, the Federation control-plane service, the
petition-outcome-watcher, and the Clerk/authority T4-5 wiring.

## Prerequisites (from Phase 11-12)

The following foundations exist and are assumed stable:

- **Embassy proto** (`embassy.proto`) — `PreflightManifest`, `StreamPackage`, `ExportPackage` RPCs.
- **Embassy SDK** — `EmbassyClient` (client), `EmbassyServiceHandler` (server interface), `EmbassyPackageStager`, `EmbassyMaterializer`, import-type resolution, trust-policy validation, `MaterializeStreamedPackage` pipeline.
- **Federation proto** (`federation.proto`) — 8 RPCs: membership, discovery, publication, petition-outcome streaming.
- **Federation SDK** — `FederationClient` with `GetPetitionTarget`, `DiscoverEndpoints`, `SubmitPublication`, `SubscribeLawUpdates`, `SubscribePetitionOutcomes`.
- **Petition outcome helpers** — `IsPetitionAccepted`, `IsPetitionRejected`.
- **Operator CRD** — `CrossFlowConfig`, `FederationConfig`, `ImportTypeSpec`, `NaturalisationConfig`, with validation.
- **Operator infra** — Embassy Deployment/Service reconciliation with federation env var projection.
- **Librarian** — dispute record CRUD (`CreateDisputeRecord`, `RetireDisputeRecord`, `GetActiveDisputes`), fully implemented. `ReplicateLaws` stubbed.
- **Sidecar** — Librarian proxy with `GetActiveDisputes` forwarding.
- **Sort** — `pending-hold` suspension when cited laws have active disputes.
- **Law-applicator** — reads petition, applies changes via Librarian, stores approval-stamp, calls `Complete()`. No dispute record wiring yet.

## Execution Checklist

Each slice follows this cadence:
1. **Validate green** -- run `go test ./...` from the relevant module(s) to confirm baseline.
2. **Add tests** -- write failing tests for the new behaviour.
3. **Validate red** -- confirm the new tests fail.
4. **Implement** -- write the production code.
5. **Validate green** -- run `go test ./...` and `make check-fix` to confirm everything passes.

Status key: `[ ]` pending, `[-]` in progress, `[x]` done.

---

### 13.1 Embassy Node Scaffold

**Goal:** Create the Embassy node binary with `StartEntry` lifecycle and the
inbound gRPC server that receives manifest/package transfers from remote
Embassies. This slice wires the skeleton — no business logic yet.

#### Slice 13.1.1 -- Embassy entry-node scaffold and gRPC server

- [x] Validate green: `go test ./nodes/...`
- [x] Create `nodes/embassy/main.go`:
  - `main()` calls `flow.StartEntry(watchInbound, handleExport)` (Embassy is both an inbound listener and an export handler)
  - Import conventions: `flow "github.com/gideas/flow/sdk/go"`, `flowv1 "github.com/gideas/flow/gen/flow/v1"`
  - Entry function `watchInbound(ctx, entry)`: starts an Embassy gRPC server on env `EMBASSY_INBOUND_PORT` (default 50059) that serves `EmbassyService` RPCs for remote callers
  - Handler function `handleExport(ctx, wctx)`: handles locally-created outbound export Workitems
  - Implement `EmbassyServiceHandler` interface as a struct `embassyHandler` with stub methods returning `Unimplemented`
  - Wire `flow.NewEmbassyServer(handler)` registration on the gRPC listener
- [x] Create `nodes/embassy/testutil_test.go`:
  - Spy servers (operator, sidecar, archivist, librarian, event bus) following friction-watcher/ttl-watcher pattern
  - Test helpers for creating Embassy handler under test
- [x] Add tests in `nodes/embassy/main_test.go`:
  - Embassy starts without error (entry function + handler registered)
  - Stub `PreflightManifest` returns `Unimplemented`
  - Stub `StreamPackage` returns `Unimplemented`
  - Stub `ExportPackage` returns `Unimplemented`
- [x] Validate red: tests fail (no implementation)
- [x] Implement the scaffold
- [x] Validate green: `go test ./nodes/...`

#### Slice 13.1.2 -- Embassy config loading

- [x] Validate green: `go test ./nodes/...`
- [x] Add tests:
  - Embassy loads system import types from `EMBASSY_SYSTEM_IMPORT_TYPES` env var (JSON)
  - Embassy loads flow-authored import types from `EMBASSY_FLOW_IMPORT_TYPES` env var (JSON)
  - Embassy loads federation identity from `EMBASSY_FEDERATION_IDENTITY` env var
  - Embassy loads federation endpoint from `EMBASSY_FEDERATION_ENDPOINT` env var
  - Embassy loads federation states from `EMBASSY_FEDERATION_STATES` env var (JSON array)
  - Embassy loads federation CA from `EMBASSY_FEDERATION_CA_PEM` env var
  - Embassy loads naturalisation config from `EMBASSY_NATURALISATION_CONFIG` env var (JSON)
  - Missing optional vars produce sensible defaults (non-federated mode)
- [x] Validate red
- [x] Implement config struct and loading in `nodes/embassy/config.go`
- [x] Validate green: `go test ./nodes/...`

---

### 13.2 Embassy Inbound: Manifest Preflight

**Goal:** Implement the inbound preflight path — accept a signed manifest,
resolve the import type, validate trust, and accept or reject before
requesting the full package.

#### Slice 13.2.1 -- Import type resolution in preflight

- [x] Validate green: `go test ./nodes/...`
- [x] Add tests:
  - `PreflightManifest` with `importType: "law-petition"` resolves against system import types → accepted
  - `PreflightManifest` with a flow-authored import type resolves against flow config → accepted
  - `PreflightManifest` with an unknown import type → rejected with reason
  - `PreflightManifest` generates a `transfer_id` in the response
- [x] Validate red
- [x] Implement import-type resolution in `PreflightManifest` handler using `flow.ResolveEmbassyImportType`
- [x] Validate green: `go test ./nodes/...`

#### Slice 13.2.2 -- Trust source validation in preflight

- [x] Validate green: `go test ./nodes/...`
- [x] Add tests:
  - Federation trust source: manifest from a federation member with valid identity → accepted
  - Treaty trust source: manifest with treaty name resolves against treaty policy → accepted
  - Treaty trust source: import type not in `AllowedImportTypes` → rejected
  - Treaty trust source: subject not in `AllowedSubjects` → rejected
  - Manifest with expired `expires_at` → rejected
- [x] Validate red
- [x] Implement trust validation using `flow.ValidateEmbassyTrustPolicy` in preflight
- [x] Validate green: `go test ./nodes/...`

#### Slice 13.2.3 -- Foreign stamp verification in preflight

- [x] Validate green: `go test ./nodes/...`
- [x] Add tests:
  - Manifest with all required foreign stamps for each artefact → accepted
  - Manifest missing a required foreign stamp → rejected with reason listing missing stamps
  - Manifest with extra unrequired stamps → accepted (extra stamps are provenance only)
- [x] Validate red
- [x] Implement foreign stamp requirement checking against the resolved import type's `requireForeignStamps`
- [x] Validate green: `go test ./nodes/...`

---

### 13.3 Embassy Inbound: Package Streaming and Verification

**Goal:** After preflight acceptance, receive the streamed package, verify
digests, and stage it for materialisation.

#### Slice 13.3.1 -- Package stager implementation

- [x] Validate green: `go test ./nodes/...`
- [x] Add tests:
  - Stager accepts manifest chunk and stores it
  - Stager accepts content chunks and accumulates them
  - Stager accepts trailer chunk with package digest
  - `Complete()` returns `EmbassyStagedPackage` with manifest and chunks
  - Empty chunk stream → error on `Complete()`
- [x] Validate red
- [x] Implement `embassyStager` struct satisfying `flow.EmbassyPackageStager` interface
- [x] Validate green: `go test ./nodes/...`

#### Slice 13.3.2 -- Package digest verification

- [x] Validate green: `go test ./nodes/...`
- [x] Add tests:
  - Staged package with matching trailer digest → verification passes
  - Staged package with mismatched trailer digest → error
  - Per-artefact digest from manifest matches staged content → passes
  - Per-artefact digest mismatch → error
- [x] Validate red
- [x] Implement digest verification on `Complete()` or a separate `Verify()` step
- [x] Validate green: `go test ./nodes/...`

#### Slice 13.3.3 -- StreamPackage handler wiring

- [x] Validate green: `go test ./nodes/...`
- [x] Add tests:
  - `StreamPackage` with valid manifest + content + trailer → returns success with `workitem_id`
  - `StreamPackage` with failed digest verification → returns error
  - `StreamPackage` with unknown import type (no prior preflight) → returns error
- [x] Validate red
- [x] Wire `StreamPackage` handler: stage chunks → verify → materialise (using `flow.MaterializeStreamedPackage`)
- [x] Validate green: `go test ./nodes/...`

---

### 13.4 Embassy Inbound: Materialisation, Naturalisation, and Routing

**Goal:** After package verification, create a local Workitem, unpack
artefacts, apply naturalisation stamps, and route to the intake node.

#### Slice 13.4.1 -- Workitem creation and artefact unpacking

- [x] Validate green: `go test ./nodes/...`
- [x] Add tests:
  - Materializer creates a new Workitem via `operator.CreateWorkitem` with imported metadata
  - Materializer stores each manifest artefact via `archivist.StoreArtefact`
  - Created Workitem metadata includes `import_type`, `source_flow`, `transfer_id`
- [x] Validate red
- [x] Implement `embassyMaterializer` struct satisfying `flow.EmbassyMaterializer` interface
- [x] Validate green: `go test ./nodes/...`

#### Slice 13.4.2 -- Naturalisation stamps

- [x] Validate green: `go test ./nodes/...`
- [x] Add tests:
  - For each verified required foreign stamp, materializer applies `imported-<stamp>` local attestation
  - Foreign stamps remain attached as provenance (not removed)
  - If naturalisation config has `requireLocalStamps`, those are applied
  - If `autoNaturalise` is false, no `imported-*` stamps are applied (explicit mode)
- [x] Validate red
- [x] Implement naturalisation stamp logic in materializer
- [x] Validate green: `go test ./nodes/...`

#### Slice 13.4.3 -- Intake routing

- [x] Validate green: `go test ./nodes/...`
- [x] Add tests:
  - Built-in `law-petition` import type: Workitem is routed to the platform-owned petition intake path
  - Flow-authored import type: Workitem is routed to the configured `node` value from import type spec
  - Unknown import type (should not happen post-preflight): error
- [x] Validate red
- [x] Implement routing after materialisation (use topology or direct route based on import type)
- [x] Validate green: `go test ./nodes/...`

---

### 13.5 Embassy Outbound: Export

**Goal:** Implement the export handler — when a local Workitem is dispatched
to Embassy for outbound transfer, build a manifest, connect to the remote
Embassy, send the manifest, wait for acceptance, and stream the package.

#### Slice 13.5.1 -- Manifest builder

- [x] Validate green: `go test ./nodes/...`
- [x] Add tests:
  - Manifest builder reads Workitem artefacts via Archivist and builds `TransferManifest`
  - Manifest includes `import_type`, `source_flow`, `target_flow`, `transfer_id` (generated UUID), `expires_at`
  - Manifest includes `ArtefactManifest` entries with digest, size, representation metadata
  - Manifest includes local stamps as `ForeignStamp` entries
- [x] Validate red
- [x] Implement manifest builder in `nodes/embassy/manifest.go`
- [x] Validate green: `go test ./nodes/...`

#### Slice 13.5.2 -- Target resolution via Federation

- [x] Validate green: `go test ./nodes/...`
- [x] Add tests:
  - For `law-petition` export: calls `FederationClient.GetPetitionTarget(scope)` to resolve authority Flow
  - Returns target authority's Embassy endpoint and Flow identity
  - Error from Federation (no authority found) → export fails with descriptive error
- [x] Validate red
- [x] Implement target resolution in export handler using `flow.NewFederationClient()`
- [x] Validate green: `go test ./nodes/...`

#### Slice 13.5.3 -- Remote Embassy connection and transfer

- [x] Validate green: `go test ./nodes/...`
- [x] Add tests:
  - Export handler connects to remote Embassy via `flow.NewEmbassyClient()`
  - Sends manifest via `PreflightManifest` → if rejected, export fails with rejection reason
  - On preflight acceptance, streams package via `StreamPackage`
  - On successful transfer, calls `client.Complete(ctx)` on the local Workitem
  - On transfer failure, returns error (workitem fails)
- [x] Validate red
- [x] Implement export handler (the `handleExport` function in `main.go`)
- [x] Validate green: `go test ./nodes/...`

---

### 13.6 Federation Service Scaffold

**Goal:** Create the Federation service binary as a new platform service with
membership store, gRPC server, and basic join/leave/membership RPCs.

#### Slice 13.6.1 -- Federation service module and directory scaffold

- [x] Validate green: `go test ./platform/...` (existing modules)
- [x] Create `platform/federation/` directory structure:
  - `platform/federation/cmd/main.go` — entry point
  - `platform/federation/internal/service/federation_server.go` — gRPC service implementation
  - `platform/federation/internal/store/sqlite/store.go` — SQLite persistence
  - `platform/federation/go.mod` — new module `github.com/gideas/flow/platform/federation`
- [x] Add `./platform/federation` to root `go.work`
- [x] Validate green: module compiles and is recognised by the workspace

#### Slice 13.6.2 -- Federation store: membership CRUD

- [x] Validate green: `go test ./platform/federation/...`
- [x] Add tests in `platform/federation/internal/store/sqlite/store_test.go`:
  - `AddMember` persists a federation member (flow identity, embassy endpoint, states, publisher roles)
  - `RemoveMember` removes by flow identity
  - `GetMember` returns member by flow identity
  - `ListMembers` returns all members
  - `ListMembers` with state filter returns members in that state
  - Duplicate flow identity → error
  - Remove non-existent member → error
- [x] Validate red
- [x] Implement store schema and CRUD in `platform/federation/internal/store/sqlite/store.go`:
  - Tables: `members`, `member_states`, `member_publisher_roles`
  - `FederationStore` struct with SQLite db
  - `New(dbPath)` constructor with schema init
  - CRUD methods
- [x] Validate green: `go test ./platform/federation/...`

#### Slice 13.6.3 -- Federation store: state management

- [x] Validate green: `go test ./platform/federation/...`
- [x] Add tests:
  - `CreateState` persists a state (id, name)
  - `ListStates` returns all states
  - `GetState` returns by id
  - Duplicate state id → error
- [x] Validate red
- [x] Implement state CRUD in store
- [x] Validate green: `go test ./platform/federation/...`

#### Slice 13.6.4 -- Federation service: JoinFederation RPC

- [x] Validate green: `go test ./platform/federation/...`
- [x] Add tests in `platform/federation/internal/service/federation_server_test.go`:
  - `JoinFederation` with valid bootstrap token and flow identity → returns federation config, intermediate CA, states, publisher roles
  - `JoinFederation` with empty flow identity → `InvalidArgument`
  - `JoinFederation` with empty bootstrap token → `InvalidArgument`
  - `JoinFederation` with already-joined flow identity → `AlreadyExists`
  - Response includes assigned states and publisher roles
- [x] Validate red
- [x] Implement `JoinFederation` RPC handler:
  - Validate inputs
  - Add member to store
  - Return `JoinFederationResponse` with federation config, CA pem, states, roles
- [x] Validate green: `go test ./platform/federation/...`

#### Slice 13.6.5 -- Federation service: LeaveFederation and GetMembership RPCs

- [x] Validate green: `go test ./platform/federation/...`
- [x] Add tests:
  - `LeaveFederation` removes a joined member → ack
  - `LeaveFederation` for non-member → `NotFound`
  - `GetMembership` for joined member → returns snapshot (states, roles, endpoint)
  - `GetMembership` for non-member → `NotFound`
- [x] Validate red
- [x] Implement `LeaveFederation` and `GetMembership` RPC handlers
- [x] Validate green: `go test ./platform/federation/...`

#### Slice 13.6.6 -- Federation service: cmd/main.go wiring

- [x] Validate green: `go test ./platform/federation/...`
- [x] Add tests:
  - Server starts on configured port
  - Server registers `FederationServiceServer`
  - Graceful shutdown on SIGTERM
- [x] Validate red
- [x] Implement `platform/federation/cmd/main.go`:
  - Read config from env (`FEDERATION_PORT`, `FEDERATION_DB_PATH`)
  - Create store
  - Create service server
  - Start gRPC server with reflection
  - Signal handler for graceful shutdown
- [x] Validate green: `go test ./platform/federation/...`

---

### 13.7 Federation Service: Discovery and Roles

**Goal:** Implement endpoint discovery, petition target resolution, and
authority publisher role enforcement.

#### Slice 13.7.1 -- Federation store: publisher roles and authority lookup

- [x] Validate green: `go test ./platform/federation/...`
- [x] Add tests:
  - Store records publisher roles per member (scope + level)
  - `GetAuthorityForScope(scope)` returns the member with matching publisher role
  - State-level authority: member with `level: "state"` and matching scope for a given state
  - Federation-level authority: member with `level: "federation"` and matching scope
  - No authority for scope → returns not found
- [x] Validate red
- [x] Implement authority lookup in store
- [x] Validate green: `go test ./platform/federation/...`

#### Slice 13.7.2 -- Federation service: DiscoverEndpoints RPC

- [x] Validate green: `go test ./platform/federation/...`
- [x] Add tests:
  - `DiscoverEndpoints` with no filter → returns all member endpoints
  - `DiscoverEndpoints` with state filter → returns only members in that state
  - Each `FlowEndpoint` includes `flow_identity`, `embassy_address`, `state_ids`
  - Empty federation → returns empty list
- [x] Validate red
- [x] Implement `DiscoverEndpoints` RPC handler
- [x] Validate green: `go test ./platform/federation/...`

#### Slice 13.7.3 -- Federation service: GetPetitionTarget RPC

- [x] Validate green: `go test ./platform/federation/...`
- [x] Add tests:
  - `GetPetitionTarget` with valid scope → returns authority flow identity + embassy endpoint
  - `GetPetitionTarget` with unknown scope → `NotFound`
  - `GetPetitionTarget` when authority has left federation → `NotFound`
  - State-level scope resolves to state-level authority
  - Federation-level scope resolves to federation-level authority
- [x] Validate red
- [x] Implement `GetPetitionTarget` RPC handler using store authority lookup
- [x] Validate green: `go test ./platform/federation/...`

---

### 13.8 Federation Service: Publication Admission

**Goal:** Implement the publication submission and conflict rejection path.
When a Tier 3 law is marked `published`, the source Flow submits it to the
Federation service which validates authority and runs conflict detection.

#### Slice 13.8.1 -- Federation store: publication records

- [ ] Validate green: `go test ./platform/federation/...`
- [ ] Add tests:
  - `RecordPublication` stores a published law with source flow identity, scope, tier
  - `GetPublication` returns by law id
  - `ListPublications` returns all, optionally filtered by scope or state
  - `FindConflicts(law)` returns existing publications that conflict (same scope, overlapping `applies_to`)
- [ ] Validate red
- [ ] Implement publication record tables and CRUD in store
- [ ] Validate green: `go test ./platform/federation/...`

#### Slice 13.8.2 -- Federation service: SubmitPublication RPC - authority validation

- [ ] Validate green: `go test ./platform/federation/...`
- [ ] Add tests:
  - `SubmitPublication` from a member with matching publisher role → accepted
  - `SubmitPublication` from a member without publisher role → rejected with `UNAUTHORISED`
  - `SubmitPublication` from a member with wrong scope → rejected with `OUT_OF_SCOPE`
  - `SubmitPublication` from a non-member → `PermissionDenied`
- [ ] Validate red
- [ ] Implement authority validation in `SubmitPublication` handler
- [ ] Validate green: `go test ./platform/federation/...`

#### Slice 13.8.3 -- Federation service: SubmitPublication RPC - conflict detection

- [ ] Validate green: `go test ./platform/federation/...`
- [ ] Add tests:
  - `SubmitPublication` with no conflicting laws → accepted
  - `SubmitPublication` conflicting with existing publication → rejected with `CONFLICT`, `conflicting_law_ids`, and `remediation_text`
  - Conflict detection checks scope overlap and `applies_to` overlap
- [ ] Validate red
- [ ] Implement conflict detection in `SubmitPublication` handler
- [ ] Validate green: `go test ./platform/federation/...`

#### Slice 13.8.4 -- Federation service: publication record on acceptance

- [ ] Validate green: `go test ./platform/federation/...`
- [ ] Add tests:
  - Accepted publication is recorded in the store
  - Publication record includes source flow identity, law reference, petition_id provenance, materialisation tier
  - Materialisation tier is Tier 4 for state-level publisher, Tier 5 for federation-level publisher
- [ ] Validate red
- [ ] Implement publication recording on acceptance path
- [ ] Validate green: `go test ./platform/federation/...`

---

### 13.9 Federation Service: Law Distribution

**Goal:** Distribute accepted publications to subscriber Flows via
`SubscribeLawUpdates` streaming and implement `ReplicateLaws` in the
Librarian for materialisation.

#### Slice 13.9.1 -- Federation service: subscriber registry and event dispatch

- [ ] Validate green: `go test ./platform/federation/...`
- [ ] Add tests:
  - `SubscribeLawUpdates` registers a subscriber and receives events on the stream
  - When a publication is accepted, all subscribers in the target states receive a `PublishedLawEvent`
  - `PublishedLawEvent` includes `law`, `materialisation_tier`, `petition_id`, `publisher_flow_identity`, `published_at`
  - Subscriber that leaves federation is removed from registry
  - Multiple subscribers receive the same event
- [ ] Validate red
- [ ] Implement subscriber registry (in-memory subscriber map) and event dispatch in `SubscribeLawUpdates` handler
- [ ] Validate green: `go test ./platform/federation/...`

#### Slice 13.9.2 -- Federation service: SubscribePetitionOutcomes RPC

- [ ] Validate green: `go test ./platform/federation/...`
- [ ] Add tests:
  - `SubscribePetitionOutcomes` registers a subscriber and receives petition outcome events
  - When a publication is accepted, petition outcome event with `ACCEPTED` is dispatched to the petitioning Flow
  - When a publication is rejected, petition outcome event with `REJECTED` is dispatched with rejection report
  - `PetitionOutcomeEvent` includes `petition_id`, `outcome`, optional `rejection`, optional `published_law_id`
- [ ] Validate red
- [ ] Implement petition-outcome event dispatch (events are generated as side effects of `SubmitPublication`)
- [ ] Validate green: `go test ./platform/federation/...`

#### Slice 13.9.3 -- Librarian: ReplicateLaws implementation

- [ ] Validate green: `go test ./platform/librarian/...`
- [ ] Add tests in `platform/librarian/internal/service/librarian_server_test.go`:
  - `ReplicateLaws` with valid laws and source flow namespace → stores laws as Tier 4 or Tier 5
  - `ReplicateLaws` returns `IntegrationResult` per law with success/failure status
  - `ReplicateLaws` with empty laws list → returns empty results (no error)
  - Laws stored via `ReplicateLaws` retain provenance metadata (source flow, petition_id)
  - Duplicate law id → conflict/update result
- [ ] Validate red
- [ ] Implement `ReplicateLaws` in `platform/librarian/internal/service/librarian_server.go` (replace the existing stub)
- [ ] Validate green: `go test ./platform/librarian/...`

#### Slice 13.9.4 -- Librarian store: replicated law storage

- [ ] Validate green: `go test ./platform/librarian/...`
- [ ] Add tests in `platform/librarian/internal/store/sqlite/store_test.go`:
  - `ReplicateLaw` stores a law with external provenance (source flow, materialisation tier)
  - Replicated law is queryable via existing `QueryLaws`
  - Replicated law retains `petition_id` in provenance
  - Updating an existing replicated law (same id) updates content and provenance
- [ ] Validate red
- [ ] Implement `ReplicateLaw(ctx, law, sourceFlowNamespace)` in Librarian store
- [ ] Validate green: `go test ./platform/librarian/...`

---

### 13.10 Petition-Outcome-Watcher Node

**Goal:** Implement `nodes/petition-watcher/` as an entry node that subscribes
to Federation petition-outcome events and handles acceptance/rejection.

#### Slice 13.10.1 -- Petition-outcome-watcher scaffold

- [ ] Validate green: `go test ./nodes/...`
- [ ] Create `nodes/petition-watcher/main.go`:
  - `main()` calls `flow.StartEntry(watchOutcomes, handleOutcome)`
  - Entry function `watchOutcomes(ctx, entry)`: connects to Federation via `flow.NewFederationClient()`, calls `SubscribePetitionOutcomes`, processes events in a reconnect loop (following friction-watcher pattern)
  - Handler function `handleOutcome(ctx, wctx)`: handles Workitems created by the entry function (e.g. new Clerk cycle on rejection)
- [ ] Create `nodes/petition-watcher/testutil_test.go`:
  - Spy servers for operator, sidecar, librarian, federation
- [ ] Add tests in `nodes/petition-watcher/main_test.go`:
  - Watcher starts and connects to Federation
  - Watcher reconnects on stream error (reconnect loop)
- [ ] Validate red
- [ ] Implement scaffold
- [ ] Validate green: `go test ./nodes/...`

#### Slice 13.10.2 -- Petition-outcome-watcher: acceptance path

- [ ] Validate green: `go test ./nodes/...`
- [ ] Add tests:
  - On `ACCEPTED` outcome event: watcher calls `Librarian.RetireDisputeRecord(petition_id)`
  - On `ACCEPTED` outcome event: watcher calls `Operator.ResumeWorkitem` for each workitem held on that `petition_id`
  - If `RetireDisputeRecord` returns `NotFound` (already retired): log warning, continue
  - If `ResumeWorkitem` fails: log warning, continue (best-effort)
- [ ] Validate red
- [ ] Implement acceptance handling in entry function
- [ ] Validate green: `go test ./nodes/...`

#### Slice 13.10.3 -- Petition-outcome-watcher: rejection path

- [ ] Validate green: `go test ./nodes/...`
- [ ] Add tests:
  - On `REJECTED` outcome event: watcher calls `Librarian.RetireDisputeRecord(petition_id)`
  - On `REJECTED` outcome event: watcher creates a new Clerk cycle Workitem via `entry.CreateWorkitem` with rejection context metadata (`petition_id`, rejection reason, conflicting law ids)
  - On `REJECTED` outcome event: watcher calls `Operator.ResumeWorkitem` for held workitems
  - Created Workitem metadata includes `rejection_report` as serialised JSON for clerk-forge to interpret
- [ ] Validate red
- [ ] Implement rejection handling in entry function
- [ ] Validate green: `go test ./nodes/...`

#### Slice 13.10.4 -- Petition-outcome-watcher: held workitem discovery

- [ ] Validate green: `go test ./nodes/...`
- [ ] Add tests:
  - Watcher discovers suspended workitems keyed on `petition_id` (the pending-hold condition from Sort)
  - Discovery uses an operator query or convention-based lookup
  - Multiple held workitems for the same `petition_id` are all resumed
  - Zero held workitems → no error, just log
- [ ] Validate red
- [ ] Implement held workitem discovery and resume
- [ ] Validate green: `go test ./nodes/...`

---

### 13.11 Law-Applicator T4-5 Wiring

**Goal:** Extend law-applicator to create dispute records and route to Embassy
on the T4-5 petition path, instead of just calling `Complete()`.

#### Slice 13.11.1 -- Law-applicator: detect T4-5 petition

- [ ] Validate green: `go test ./nodes/...`
- [ ] Add tests in `nodes/law-applicator/main_test.go`:
  - Petition with all changes at Tier 1-2 → `Complete()` as before (regression guard)
  - Petition with any change at Tier 3 → `Complete()` as before
  - Petition with any change at Tier 4 or Tier 5 → does NOT call `Complete()` (new behaviour)
  - Tier detection reads from `petition.changes[].tier` or metadata
- [ ] Validate red
- [ ] Implement tier detection logic in law-applicator
- [ ] Validate green: `go test ./nodes/...`

#### Slice 13.11.2 -- Law-applicator: create dispute record on T4-5 path

- [ ] Validate green: `go test ./nodes/...`
- [ ] Add tests:
  - T4-5 petition: law-applicator calls `Librarian.CreateDisputeRecord` with `petition_id` and `cited_law_ids`
  - `cited_law_ids` are extracted from the petition changes (law IDs being created/retired/demoted)
  - `petition_id` is read from `petition.petition_id`
  - If `CreateDisputeRecord` fails with `AlreadyExists` → log warning, continue (idempotent)
  - If `CreateDisputeRecord` fails with other error → return error
- [ ] Validate red
- [ ] Implement dispute record creation in law-applicator's T4-5 path
- [ ] Validate green: `go test ./nodes/...`

#### Slice 13.11.3 -- Law-applicator: route to Embassy on T4-5 path

- [ ] Validate green: `go test ./nodes/...`
- [ ] Add tests:
  - T4-5 petition: after dispute record creation, law-applicator calls `client.RouteToOutput(ctx, "embassy")`
  - Workitem metadata includes `import_type: "law-petition"`, `petition_id`, target scope
  - T1-2 petition: still calls `Complete()` (regression guard)
  - T3 petition: calls `Complete()` (T3 laws are local, no cross-flow export)
- [ ] Validate red
- [ ] Implement Embassy routing in law-applicator (add "embassy" output, route on T4-5)
- [ ] Validate green: `go test ./nodes/...`

---

### 13.12 Operator: Federation Service Provisioning

**Goal:** Extend the operator to reconcile a Federation service Deployment
and Service when federation config is present on the FoundryFlow CRD.

#### Slice 13.12.1 -- Operator: reconcileFederation Deployment and Service

- [ ] Validate green: `make -C platform/operator test`
- [ ] Add tests in `platform/operator/internal/controller/foundryflow_infra_test.go`:
  - When `spec.crossFlow.federation` is set: Federation Deployment is created with correct image, port (50061), labels
  - When `spec.crossFlow.federation` is set: Federation Service is created (`flow-federation`, port 50061)
  - When `spec.crossFlow.federation` is nil: no Federation Deployment or Service created
  - Federation env vars include: `FEDERATION_PORT`, `FEDERATION_DB_PATH`, `EVENT_BUS_ADDRESS`
  - Federation Deployment has `/data` volume (EmptyDir) for SQLite
- [ ] Validate red
- [ ] Implement `reconcileFederation`, `reconcileFederationDeployment`, `federationEnvVars` in `foundryflow_infra.go`:
  - Constants: `federationImage = "ghcr.io/gideas/flow/federation:latest"`, `federationPort = 50061`, `federationSvcName = "flow-federation"`
  - Add `r.reconcileFederation(ctx, flow)` to `reconcileInfrastructure` (conditional on `spec.crossFlow.federation != nil`)
- [ ] Validate green: `make -C platform/operator test`

#### Slice 13.12.2 -- Operator: project Federation address to Embassy and nodes

- [ ] Validate green: `make -C platform/operator test`
- [ ] Add tests:
  - Embassy Deployment receives `FEDERATION_ADDRESS` env var pointing to `flow-federation:50061` when federation is configured
  - petition-outcome-watcher node Deployment receives `FEDERATION_ADDRESS` env var
  - When federation is not configured, `FEDERATION_ADDRESS` is not projected
- [ ] Validate red
- [ ] Update Embassy env var projection to include `FEDERATION_ADDRESS`
- [ ] Validate green: `make -C platform/operator test`

---

### 13.13 Sidecar: Federation Proxy (Optional)

**Goal:** Add a Federation service proxy to the sidecar so nodes can reach the
Federation service via the sidecar's unified gRPC endpoint. This is needed
for nodes like petition-outcome-watcher that use `FederationClient`.

#### Slice 13.13.1 -- Sidecar: Federation proxy

- [ ] Validate green: `go test ./platform/sidecar/...`
- [ ] Add tests in `platform/sidecar/internal/proxy/federation_test.go`:
  - `NewFederationProxy` connects to the configured address
  - All 8 RPCs are forwarded to the Federation backend
  - Metadata is propagated via `propagateMetadata`
- [ ] Validate red
- [ ] Create `platform/sidecar/internal/proxy/federation.go`:
  - `FederationProxy` struct embedding `flowv1.UnimplementedFederationServiceServer`
  - `NewFederationProxy(addr string)` constructor
  - Forward all RPCs (including streaming RPCs for `SubscribeLawUpdates` and `SubscribePetitionOutcomes`)
  - `Close()` method
- [ ] Validate red (streaming proxy may need additional wiring)
- [ ] Wire into `platform/sidecar/cmd/main.go`:
  - Add `envFederationAddress = "FEDERATION_ADDRESS"`
  - Conditional registration: if env var set, create proxy and register; else skip
- [ ] Validate green: `go test ./platform/sidecar/...`

---

### 13.14 Cross-Cutting Validation

**Goal:** Ensure all Phase 13 changes pass the project quality gates as a
whole.

#### Slice 13.14.1 -- Full test suite

- [ ] Run `go test ./...` from repo root (all modules via go.work, including new `platform/federation`)
- [ ] Run `make -C platform/operator test`
- [ ] All tests pass

#### Slice 13.14.2 -- Lint and tidy

- [ ] Run `make check-fix`
- [ ] All lint issues resolved
- [ ] Run `make -C platform/operator lint-fix`
- [ ] All operator lint issues resolved

#### Slice 13.14.3 -- Proto generation idempotency

- [ ] Run `make proto`
- [ ] `git diff` shows no changes (generation is idempotent)

#### Slice 13.14.4 -- Architectural guard tests

- [ ] Add `gen/flow/v1/embassy_implementation_test.go` (or extend existing test):
  - Assert `embassy.proto` RPC surface has not regressed (PreflightManifest, StreamPackage, ExportPackage still present)
- [ ] Add integration-style test verifying:
  - Embassy node implements all 3 `EmbassyServiceHandler` methods (not stubs)
  - Federation service implements all 8 `FederationServiceServer` methods (not stubs)
- [ ] Validate green

---

## Dependency Order

```text
13.1.1 ─► 13.1.2 ─► 13.2.1 ─► 13.2.2 ─► 13.2.3 ─► 13.3.1 ─► 13.3.2 ─► 13.3.3
                                                                              │
                                                                              ▼
                                                                          13.4.1 ─► 13.4.2 ─► 13.4.3
                                                                                                  │
                     13.5.1 ─► 13.5.2 ─► 13.5.3 ◄────────────────────────────────────────────────┘
                                  ▲
                                  │
13.6.1 ─► 13.6.2 ─► 13.6.3 ─► 13.6.4 ─► 13.6.5 ─► 13.6.6
              │
              ▼
          13.7.1 ─► 13.7.2 ─► 13.7.3
                                  │
                                  ▼
                              13.8.1 ─► 13.8.2 ─► 13.8.3 ─► 13.8.4
                                                                 │
                                                                 ▼
                                                             13.9.1 ─► 13.9.2
                                                                          │
13.9.3 ─► 13.9.4  (Librarian, independent)                               │
                                                                          │
13.10.1 ──────────────────────────────────────────────────────────────────►│
    │                                                                     │
    ▼                                                                     │
13.10.2 ─► 13.10.3 ─► 13.10.4 ◄──────────────────────────────────────────┘

13.11.1 ─► 13.11.2 ─► 13.11.3  (Law-applicator, semi-independent)

13.12.1 ─► 13.12.2  (Operator, depends on 13.6.1 for image/port constants)

13.13.1  (Sidecar, depends on 13.6.1 for proto)

All above ──────► 13.14.1 ─► 13.14.2 ─► 13.14.3 ─► 13.14.4
```

**Parallelism opportunities:**
- 13.1.x-13.5.x (Embassy node) and 13.6.x-13.9.x (Federation service) can progress in parallel — Embassy scaffold does not depend on Federation implementation (it uses the SDK client which already exists).
- 13.9.3-13.9.4 (Librarian ReplicateLaws) is independent of the Federation service and can run in parallel with 13.6.x.
- 13.11.x (law-applicator wiring) is semi-independent — it only needs the Librarian dispute RPCs (already exist) and an Embassy output (needs 13.5.x to exist).
- 13.12.x (operator provisioning) can run in parallel with 13.6.x once the image/port constants are known.
- 13.13.x (sidecar proxy) can run in parallel with everything above.
- 13.10.x (petition-outcome-watcher) depends on Federation streaming RPCs being implemented (13.9.2) for integration, but the scaffold can start earlier.
- 13.14.x is the final gate after everything else.

---

## Notes

- **Embassy is both a server and a node.** The entry function runs an Embassy gRPC server for inbound transfers (remote Embassies connect to it). The handler function processes outbound export Workitems that are routed to Embassy by upstream nodes (e.g. law-applicator). This dual-mode is the reason for `StartEntry`.
- **Federation service is a platform service, not a node.** It has its own module in `platform/federation/`, its own `go.mod`, and is provisioned by the operator (like eventbus, librarian, etc.). It does not use `flow.Start()` or `flow.StartEntry()`.
- **`ReplicateLaws` is the materialisation bridge.** When the Federation service distributes a law, subscriber Flows materialise it via `ReplicateLaws` called on their Librarian. This is the Federation → Librarian path for accepted publications. The Federation service (or a subscriber-side component) calls this RPC.
- **Petition-outcome-watcher needs a mechanism to discover held workitems.** The `pending-hold` Suspend condition is `dispute_retired("<petition_id>")`. The watcher needs to either query the operator for suspended workitems with this condition, or the Operator needs to auto-resume based on the condition. This is an open design question that may need a new operator RPC or a convention-based query.
- **mTLS is deferred.** Phase 13 wires the functional paths (manifest, stream, verify, materialise). Actual mTLS certificate management and manifest signing/verification can be hardened in a later phase without changing the protocol.
- **No new proto changes expected.** Phase 12 defined all the proto contracts. Phase 13 implements them. If proto changes are needed, they should be done as early slices before implementation depends on them.
