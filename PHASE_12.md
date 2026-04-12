# Phase 12 - Federation Foundations (Proto, Service Schema, Publication Lifecycle)

This phase lays down the Federation service contract, configuration model, and
the operator/SDK integration needed before the Federation service can be
implemented.

## Execution Checklist

Each slice follows this cadence:
1. **Validate green** -- run `go test ./...` from the relevant module(s) to confirm baseline.
2. **Add tests** -- write failing tests for the new behaviour.
3. **Validate red** -- confirm the new tests fail.
4. **Implement** -- write the production code.
5. **Validate green** -- run `go test ./...` and `make check-fix` to confirm everything passes.

Status key: `[ ]` pending, `[-]` in progress, `[x]` done.

---

### 12.1 Federation Wire Protocol

**Goal:** Create `proto/flow/v1/federation.proto` with the full Federation
service RPC surface, regenerate `gen/flow/v1/`, and confirm compilation.

#### Slice 12.1.1 -- Federation proto: membership RPCs

- [x] Validate green: `go test ./gen/...`
- [x] Create `proto/flow/v1/federation.proto` with:
  - `FederationService` service definition
  - `JoinFederation` RPC (request: bootstrap token / Flow identity; response: intermediate CA cert, federation config, state assignments)
  - `LeaveFederation` RPC (request: Flow identity; response: ack)
  - `GetMembership` RPC (request: Flow identity; response: membership snapshot -- states, roles, endpoints)
  - Supporting messages: `JoinFederationRequest/Response`, `LeaveFederationRequest/Response`, `GetMembershipRequest/Response`, `FederationMember`, `State`, `PublisherRole`
- [x] Run `buf generate` (`make proto`) to regenerate `gen/flow/v1/`
- [x] Validate green: `go test ./gen/...` (generated code compiles)

#### Slice 12.1.2 -- Federation proto: discovery RPCs

- [x] Validate green: `go test ./gen/...`
- [x] Add to `federation.proto`:
  - `DiscoverEndpoints` RPC (request: optional state filter; response: list of Flow endpoints with Embassy addresses)
  - `GetPetitionTarget` RPC (request: scope / domain; response: authority Flow identity + Embassy endpoint)
  - Supporting messages: `DiscoverEndpointsRequest/Response`, `FlowEndpoint`, `GetPetitionTargetRequest/Response`
- [x] Run `make proto`
- [x] Validate green: `go test ./gen/...`

#### Slice 12.1.3 -- Federation proto: publication RPCs

- [x] Validate green: `go test ./gen/...`
- [x] Add to `federation.proto`:
  - `SubmitPublication` RPC (request: Law + source Flow identity; response: accept or structured rejection report)
  - `SubscribeLawUpdates` RPC (server-streaming; request: subscriber Flow identity; response stream: published laws with tier + provenance)
  - Supporting messages: `SubmitPublicationRequest/Response`, `PublicationRejection` (reason enum, conflicting law refs, remediation text), `SubscribeLawUpdatesRequest`, `PublishedLawEvent` (law, materialisation tier, petition_id provenance)
- [x] Run `make proto`
- [x] Validate green: `go test ./gen/...`

#### Slice 12.1.4 -- Federation proto: petition-outcome events

- [x] Validate green: `go test ./gen/...`
- [x] Add to `federation.proto`:
  - `SubscribePetitionOutcomes` RPC (server-streaming; request: subscriber Flow identity; response stream: petition outcome events)
  - Supporting messages: `SubscribePetitionOutcomesRequest`, `PetitionOutcomeEvent` (petition_id, outcome enum [accepted/rejected], rejection report if rejected, published law ref if accepted)
- [x] Run `make proto`
- [x] Validate green: `go test ./gen/...`

#### Slice 12.1.5 -- Federation proto compilation test

- [x] Validate green: `go test ./gen/...`
- [x] Add `gen/flow/v1/federation_proto_test.go` (pattern from `embassy_proto_test.go`):
  - Instantiate key generated types (`JoinFederationRequest`, `SubmitPublicationRequest`, `PublishedLawEvent`, `PetitionOutcomeEvent`, etc.) and verify getter methods
  - Verify `FederationServiceClient` interface exists
- [x] Validate red: new test fails (missing assertions or type checks if proto was malformed)
- [x] Fix any issues
- [x] Validate green: `go test ./gen/...`

---

### 12.2 Federation CRD and Configuration

**Goal:** Extend the FoundryFlow CRD with federation identity, state
membership, and publisher role fields. Add validation.

#### Slice 12.2.1 -- FoundryFlow CRD: federation config fields

- [x] Validate green: `make -C platform/operator test`
- [x] Add tests in `platform/operator/api/v1/foundryflow_types_test.go` verifying:
  - `FederationConfig` struct serialises/deserialises correctly
  - Federation fields are optional (nil when absent)
- [x] Validate red
- [x] Add to `CrossFlowConfig` in `foundryflow_types.go`:
  - `Federation *FederationConfig` field (optional)
  - `FederationConfig` struct: `Identity string` (Flow's federation identity), `States []string` (assigned state names), `PublisherRoles []FederationPublisherRole` (optional), `FederationEndpoint string` (Federation service address)
  - `FederationPublisherRole` struct: `Scope string` (domain, e.g. "security"), `Level string` (enum: "state" / "federation")
- [x] Run `make -C platform/operator manifests generate`
- [x] Validate green: `make -C platform/operator test`

#### Slice 12.2.2 -- Operator validation: federation config

- [x] Validate green: `make -C platform/operator test`
- [x] Add unit tests in `platform/operator/internal/controller/validation_unit_test.go`:
  - Federation identity required when federation config is set
  - Publisher role level must be "state" or "federation"
  - Publisher role scope must be non-empty
  - States list must be non-empty when federation config is set
  - FederationEndpoint must be non-empty when federation config is set
- [x] Validate red
- [x] Implement validation logic in the operator (extend existing validation path)
- [x] Validate green: `make -C platform/operator test`

#### Slice 12.2.3 -- Operator: project federation config to Embassy

- [x] Validate green: `make -C platform/operator test`
- [x] Add tests verifying the Embassy deployment receives federation config env vars:
  - `EMBASSY_FEDERATION_IDENTITY` projected from `CrossFlow.Federation.Identity`
  - `EMBASSY_FEDERATION_ENDPOINT` projected from `CrossFlow.Federation.FederationEndpoint`
  - `EMBASSY_FEDERATION_STATES` projected (JSON-encoded list)
  - Existing `EMBASSY_FEDERATION_CA_PEM` unchanged
- [x] Validate red
- [x] Update `foundryflow_infra.go` to project the new federation env vars to the Embassy Deployment
- [x] Validate green: `make -C platform/operator test`

---

### 12.3 SDK Federation Client

**Goal:** Add a thin SDK client for the Federation service RPCs that Embassy
and petition-outcome-watcher will use.

#### Slice 12.3.1 -- SDK Federation client: membership + discovery

- [x] Validate green: `go test ./sdk/go/...`
- [x] Add `sdk/go/federation.go` tests in `sdk/go/federation_test.go`:
  - `FederationClient` connects to a configurable address
  - `GetPetitionTarget(scope)` returns authority endpoint
  - `DiscoverEndpoints(stateFilter)` returns endpoint list
- [x] Validate red
- [x] Implement `sdk/go/federation.go`:
  - `FederationClient` struct wrapping generated `FederationServiceClient`
  - `NewFederationClient(addr string)` constructor
  - `GetPetitionTarget(ctx, scope) (*PetitionTarget, error)` method
  - `DiscoverEndpoints(ctx, stateFilter) ([]FlowEndpoint, error)` method
  - Use `DefaultFederationAddress = "localhost:50061"` or env-override
- [x] Validate green: `go test ./sdk/go/...`

#### Slice 12.3.2 -- SDK Federation client: publication + events

- [x] Validate green: `go test ./sdk/go/...`
- [x] Add tests:
  - `SubmitPublication(law)` returns accept/reject
  - `SubscribeLawUpdates()` returns streaming reader
  - `SubscribePetitionOutcomes()` returns streaming reader
- [x] Validate red
- [x] Implement publication and event subscription methods on `FederationClient`
- [x] Validate green: `go test ./sdk/go/...`

---

### 12.4 Dispute Record Support (Librarian)

**Goal:** Add dispute record CRUD to the Librarian proto, store, and service.

#### Slice 12.4.1 -- Librarian proto: dispute record RPCs

- [x] Validate green: `go test ./gen/...`
- [x] Add to `proto/flow/v1/librarian.proto`:
  - `DisputeRecord` message: `petition_id`, `repeated cited_law_ids`, `created_at` (Timestamp), `status` (enum: `ACTIVE`, `RETIRED`)
  - `CreateDisputeRecord` RPC (request: petition_id, cited_law_ids; response: dispute record)
  - `RetireDisputeRecord` RPC (request: petition_id; response: ack)
  - `GetActiveDisputes` RPC (request: optional law_id filter; response: repeated DisputeRecord)
  - Supporting request/response messages
- [x] Run `make proto`
- [x] Validate green: `go test ./gen/...`

#### Slice 12.4.2 -- Librarian store: dispute record table + CRUD

- [x] Validate green: `go test ./platform/librarian/...`
- [x] Add tests in `platform/librarian/internal/store/sqlite/store_test.go`:
  - `CreateDisputeRecord` persists a record with status `active`
  - `RetireDisputeRecord` sets status to `retired`
  - `GetActiveDisputes` returns only active records
  - `GetActiveDisputes` with law_id filter returns records citing that law
  - Duplicate petition_id creation returns error
  - Retiring a non-existent petition_id returns error
- [x] Validate red
- [x] Implement in `platform/librarian/internal/store/sqlite/store.go`:
  - Add `dispute_records` and `dispute_record_laws` tables to schema init
  - `CreateDisputeRecord(ctx, petitionID, citedLawIDs) (*DisputeRecord, error)`
  - `RetireDisputeRecord(ctx, petitionID) error`
  - `GetActiveDisputes(ctx, lawIDFilter string) ([]*DisputeRecord, error)`
- [x] Validate green: `go test ./platform/librarian/...`

#### Slice 12.4.3 -- Librarian service: dispute record RPC handlers

- [x] Validate green: `go test ./platform/librarian/...`
- [x] Add tests in `platform/librarian/internal/service/librarian_server_test.go`:
  - `CreateDisputeRecord` returns created record
  - `CreateDisputeRecord` with empty petition_id returns InvalidArgument
  - `CreateDisputeRecord` with empty cited_law_ids returns InvalidArgument
  - `RetireDisputeRecord` retires an active record
  - `RetireDisputeRecord` for non-existent petition returns NotFound
  - `GetActiveDisputes` returns only active records
  - `GetActiveDisputes` with law_id filter
- [x] Validate red
- [x] Implement RPC handlers in `platform/librarian/internal/service/librarian_server.go`:
  - `CreateDisputeRecord` handler (validate inputs, delegate to store, return response)
  - `RetireDisputeRecord` handler (validate, delegate to store)
  - `GetActiveDisputes` handler (delegate to store, map to proto)
- [x] Validate green: `go test ./platform/librarian/...`

#### Slice 12.4.4 -- Librarian service: dispute record audit events

- [x] Validate green: `go test ./platform/librarian/...`
- [x] Add tests in `platform/librarian/internal/service/audit_test.go`:
  - `CreateDisputeRecord` emits audit event with petition_id and cited laws
  - `RetireDisputeRecord` emits audit event with petition_id
- [x] Validate red
- [x] Implement audit event emission in dispute record handlers
- [x] Validate green: `go test ./platform/librarian/...`

---

### 12.5 Dispute Record Support (Sort Node)

**Goal:** Extend Sort to query active dispute records and route to
`pending-hold` (Suspend with petition_id key) instead of deadlocking when
cited laws are in active dispute.

#### Slice 12.5.1 -- Sidecar Librarian proxy: dispute record forwarding

- [x] Validate green: `go test ./platform/sidecar/...`
- [x] Add tests verifying the Sidecar Librarian proxy forwards:
  - `GetActiveDisputes` to the Librarian backend
- [x] Validate red
- [x] Implement proxy forwarding for `GetActiveDisputes` in the Sidecar
- [x] Validate green: `go test ./platform/sidecar/...`

#### Slice 12.5.2 -- Sort: query dispute records before deadlock routing

- [x] Validate green: `go test ./nodes/...`
- [x] Add tests in `nodes/sort/main_test.go`:
  - When feedback is deadlocked AND cited law has an active dispute record:
    Sort calls Suspend (pending-hold) instead of routing to arbiter
  - When feedback is deadlocked AND no active dispute records:
    Sort routes to arbiter as before (regression guard)
  - Pending-hold suspension includes petition_id in metadata
- [x] Validate red
- [x] Implement in `nodes/sort/main.go`:
  - After deadlock detection (Step 1), before routing to arbiter:
    call `GetActiveDisputes` with the cited law IDs from deadlocked feedback
  - If any active dispute found: call `Suspend()` with a CEL condition
    keyed on the petition_id (e.g. `dispute_retired("<petition_id>")`) and
    a timeout, rather than routing to the arbiter output
  - If no dispute: route to arbiter as before
- [x] Validate green: `go test ./nodes/...`

---

### 12.6 Petition-Outcome-Watcher Foundations

**Goal:** Define the event contract and Librarian interactions that the
petition-outcome-watcher will use. No node implementation yet (that is
Phase 13), but the interfaces and proto contracts must be ready.

#### Slice 12.6.1 -- Federation proto: petition-outcome event contract (already covered in 12.1.4)

This slice is satisfied by slice 12.1.4. The `PetitionOutcomeEvent` message
and `SubscribePetitionOutcomes` RPC define the contract the watcher will
consume. Mark complete when 12.1.4 is done.

- [x] Confirm `PetitionOutcomeEvent` has: `petition_id`, outcome enum (`ACCEPTED`/`REJECTED`), optional `PublicationRejection` (from 12.1.3), optional published law reference
- [x] Confirm `SubscribePetitionOutcomes` is a server-streaming RPC

#### Slice 12.6.2 -- SDK: petition-outcome event helpers

- [x] Validate green: `go test ./sdk/go/...`
- [x] Add tests for watcher-specific SDK helpers:
  - `IsPetitionAccepted(event)` / `IsPetitionRejected(event)` convenience methods
  - Event deserialization from the Federation stream
- [x] Validate red
- [x] Implement helpers in `sdk/go/federation.go` or a new `sdk/go/petition_outcome.go`
- [x] Validate green: `go test ./sdk/go/...`

---

### 12.7 Cross-Cutting Validation

**Goal:** Ensure all Phase 12 changes pass the project quality gates as a
whole.

#### Slice 12.7.1 -- Full test suite

- [x] Run `go test ./...` from repo root (all modules via go.work)
- [x] All tests pass

#### Slice 12.7.2 -- Lint and tidy

- [x] Run `make check-fix`
- [x] All lint issues resolved
- [x] Run `make -C platform/operator lint-fix`
- [x] All operator lint issues resolved

#### Slice 12.7.3 -- Proto generation idempotency

- [x] Run `make proto` a second time
- [x] `git diff` shows no changes (generation is idempotent)

#### Slice 12.7.4 -- Architectural guard test

- [x] Add `gen/flow/v1/federation_proto_retirement_test.go` (pattern from `operator_proto_retirement_test.go`):
  - Assert `federation.proto` does NOT contain any retired concepts (e.g. `ExportWorkitem`, `ImportWorkitem`, `CreateHearingWorkitem`, `GovernanceFlow`)
- [x] Validate green

---

## Dependency Order

```text
12.1.1 ─► 12.1.2 ─► 12.1.3 ─► 12.1.4 ─► 12.1.5
                                   │
                                   ▼
                               12.6.1 ─► 12.6.2
                                             │
12.2.1 ─► 12.2.2 ─► 12.2.3                  │
              │                               │
              ▼                               │
          12.3.1 ─► 12.3.2 ◄─────────────────┘
                                              
12.4.1 ─► 12.4.2 ─► 12.4.3 ─► 12.4.4
                        │
                        ▼
                    12.5.1 ─► 12.5.2

All above ──────► 12.7.1 ─► 12.7.2 ─► 12.7.3 ─► 12.7.4
```

**Parallelism opportunities:**
- 12.1.x (federation proto) and 12.4.x (dispute record proto/store) can run in parallel until 12.5.x (Sort needs both).
- 12.2.x (CRD) can run in parallel with 12.1.x and 12.4.x.
- 12.3.x (SDK client) depends on 12.1.x (proto must exist).
- 12.5.x (Sort pending-hold) depends on 12.4.x (dispute RPCs) and the Sidecar proxy.
- 12.6.x depends on 12.1.4 (petition-outcome proto).
- 12.7.x is the final gate after everything else.

---

## Notes

- **`pending-hold` is modelled as a Suspend, not a new Workitem phase.** Sort
  calls `Suspend()` with a CEL condition that resolves when the
  petition-outcome-watcher retires the dispute record. This avoids adding a
  new phase to the Workitem CRD enum and reuses the existing Suspend/Resume
  infrastructure.
- **`ReplicateLaws` remains stubbed.** The Federation service (Phase 13) will
  call `ReplicateLaws` as the materialisation path for accepted publications.
  The stub will be implemented in Phase 13 alongside the Federation service.
- **No `platform/federation/` directory yet.** This phase defines the proto
  contract and configuration surfaces. The Federation service implementation
  is Phase 13.
