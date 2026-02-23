# Implementation Plan: Event Bus, Flow Monitor, and Friction Ledger

## Status

| Phase | Description | Status |
|-------|-------------|--------|
| 1 | Flow Event Bus — proto + service | complete |
| 2 | Friction Ledger — extract from Monitor + new proto | complete |
| 3 | Sidecar & SDK rewiring + Monitor refactor (atomic cut-over) | **complete** |
| 4 | Audit event publishing + Librarian hearing triggers | **complete** |
| 5 | Operator provisioning & configuration | **complete** |

After completing and committing each phase, update the table above by changing `pending` to `complete`.

---

## Instructions for Each Session

1. Read this file first to understand which phases are complete and which to tackle next.
2. Implement the next pending phase in full (code + tests).
3. Ensure quality gates pass: `go test ./...` and `make check-fix`.
4. Commit and push the completed phase using the `/commit-push` skill.
5. Update the Status table in this file (change `pending` -> `complete` for the delivered phase).
6. Commit and push the updated PLAN.md in the same commit, or a follow-up commit.

---

## Problem Statement

The specs for Foundry Flow now describe a three-service architecture for observability and governance signalling:

1. **Flow Event Bus** — a durable pub/sub bus with three channels (telemetry, audit, friction).
2. **Friction Ledger** — the sole friction aggregation and threshold-evaluation service, fed by the Event Bus.
3. **Flow Monitor** — a stateless pipeline adapter that subscribes to the Event Bus and exports to Prometheus and stdout.

The current implementation has a **single monolithic `monitor/` service** that conflates all three concerns: it ingests friction directly via gRPC (`AddFriction`), stores telemetry events, and serves `QueryFriction`. There is no Event Bus, no pub/sub, and no channel-based fan-out. The sidecar proxies friction and telemetry calls directly to this service.

This plan closes the gap between the spec and the implementation.

---

## Architectural Goal

```
Nodes / Services
      |
      v (gRPC via Sidecar)
Flow Event Bus  <---------------------------------------------+
  telemetry channel --> Friction Ledger (friction aggregation) |
  telemetry channel --> Flow Monitor (metrics export)          |
  audit channel     --> Flow Monitor (JSON Lines to stdout)    |
  friction channel  --> Librarian (hearing triggers)           |
                                                               |
Friction Ledger --(threshold-crossing publish to friction ch)--+
Friction Ledger --(QueryFriction gRPC)--> Tribunal / Librarian
Flow Monitor    --(/metrics HTTP)-------> Prometheus
Flow Monitor    --(JSON Lines stdout)---> Log pipeline
```

**Key invariants (from spec):**
- Service Invariant #13: The Flow Event Bus is durable. Events are persisted to SQLite before fan-out. Retention is per-channel and operator-configurable.
- Service Invariant #14: Audit events are published by the authoritative service, not by nodes.
- Service Invariant #15: The Friction Ledger is the sole aggregation and query surface for friction data.
- Service Invariant #16: The Flow Monitor is a stateless pipeline adapter. It does not persist events or serve query APIs.

---

## Data Type Conventions

These conventions apply across all phases and were confirmed during plan review.

### Friction magnitude — `double`

The spec defines magnitude as `float64` (`specs/04-sdk/06-sdk-telemetry.md`). The current proto uses `int32`. New protos use `double` to align with the spec and allow fractional friction values. The existing `AddFrictionRequest.magnitude` field in `monitor.proto` remains `int32` until that proto is deleted in Phase 3; the Friction Ledger's internal store and `sidecar.proto` replacement both use `double`. When serialised into `FlowEvent.attributes`, magnitude is a decimal string (e.g. `"3.5"`).

### `law_ids` serialisation — comma-separated string

`FlowEvent.attributes` is `map<string,string>`. Multi-valued fields are serialised as comma-separated strings, matching the spec's own convention (`specs/05-reference/grpc-api.md` line 207: "law_ids (comma-separated)"). For friction events: `law_ids` = `"law-1,law-2"`, `magnitude` = `"3.5"`. Consumers split on commas to recover the list. The `SubscribeFilter.law_id` (singular) matches if the filter value appears as an element in the comma-separated `law_ids` attribute (exact element match after splitting, not substring).

### Flow Monitor replay — checkpoint file

The spec calls the Flow Monitor a "stateless pipeline adapter" (Service Invariant #16), meaning it does not persist events or serve query APIs. However, to avoid losing audit log events across restarts, the Monitor persists its per-channel `last_sequence` to a small checkpoint file on disk (not SQLite). On startup it reads the checkpoint and replays from that sequence if still within the Event Bus retention window. This is minimal state — a single file with two integers — not a data store. If the checkpoint is missing or the sequence is beyond the retention window, the Monitor starts from live-only.

### Event Bus retention format — duration and size

Retention windows support both duration-based and size-based limits from day one. The spec says "the exact field names and value formats are implementation-defined" (`specs/02-flow/05-configuration.md`). Configuration accepts:
- Duration format: Go `time.Duration` strings (e.g. `"24h"`, `"168h"`)
- Size format: byte-count strings with unit suffix (e.g. `"100MB"`, `"1GB"`)
- Both can be specified per channel; the Event Bus evicts when **either** limit is exceeded (whichever triggers first)

Eviction runs two strategies: age-based (delete events older than the duration window) and size-based (delete oldest events until the channel's total payload is below the size limit). Configuration env vars per channel: `EVENT_BUS_RETENTION_TELEMETRY_DURATION`, `EVENT_BUS_RETENTION_TELEMETRY_SIZE`, etc.

---

## Phase 1: Flow Event Bus

**Goal:** Implement the Event Bus as a new standalone service with durable SQLite-backed pub/sub.

### New files

**Proto** (`proto/flow/v1/eventbus.proto`):
- Service: `FlowEventBusService`
  - `rpc Publish(PublishRequest) returns (PublishResponse)` — write-ahead publish
  - `rpc Subscribe(SubscribeRequest) returns (stream FlowEvent)` — server-side streaming with optional replay
- Messages:
  - `FlowEvent`: `event_id` (string), `sequence` (uint64), `channel` (EventChannel), `event_type` (string), `flow_id` (string), `node_id` (string), `workitem_id` (string), `timestamp` (Timestamp), `trace_id` (string), `attributes` (map<string,string>), `payload` (bytes, max 64 KB)
  - `EventChannel` enum: `EVENT_CHANNEL_UNSPECIFIED=0`, `EVENT_CHANNEL_TELEMETRY=1`, `EVENT_CHANNEL_AUDIT=2`, `EVENT_CHANNEL_FRICTION=3`
  - `PublishRequest`: `channel` (EventChannel), `event` (FlowEvent)
  - `PublishResponse`: `acknowledged` (bool), `sequence` (uint64)
  - `SubscribeRequest`: `channel` (EventChannel), `filter` (SubscribeFilter), `last_sequence` (uint64, optional — 0 means live-only)
  - `SubscribeFilter`: `event_type` (string, optional), `law_id` (string, optional)
- After adding proto, run `buf generate` to regenerate Go code in `gen/`.

**Service** (`eventbus/`):
- `go.mod` — new module `github.com/gideas/flow/eventbus`
- `cmd/main.go` — gRPC server on port 50056; reads `EVENT_BUS_DB_PATH`, `EVENT_BUS_PORT`, and per-channel retention env vars (`EVENT_BUS_RETENTION_TELEMETRY_DURATION`, `EVENT_BUS_RETENTION_TELEMETRY_SIZE`, `EVENT_BUS_RETENTION_AUDIT_DURATION`, `EVENT_BUS_RETENTION_AUDIT_SIZE`, `EVENT_BUS_RETENTION_FRICTION_DURATION`, `EVENT_BUS_RETENTION_FRICTION_SIZE`)
- `internal/store/sqlite/store.go`:
  - Table `events`: `id TEXT PK`, `sequence INTEGER`, `channel INTEGER`, `event_type TEXT`, `flow_id TEXT`, `node_id TEXT`, `workitem_id TEXT`, `timestamp DATETIME`, `attributes BLOB` (JSON-encoded map), `payload BLOB`
  - Per-channel monotonic sequence counter (use SQLite `MAX(sequence)` per channel on startup, then atomic in-memory increment)
  - `Insert(event)` — write-ahead, returns assigned sequence
  - `GetSince(channel, lastSequence, limit)` — for replay
  - `Evict(channel, retentionDuration, retentionSize)` — deletes events older than duration window OR oldest events until channel total payload is under size limit (whichever triggers first)
- `internal/service/eventbus_server.go`:
  - `Publish`: validate, call `store.Insert`, fan-out to active subscribers for that channel, return `PublishResponse` with sequence
  - `Subscribe`: register subscriber goroutine; if `last_sequence > 0`, replay from store first, then switch to live fan-out; honour `SubscribeFilter`; apply per-subscriber backpressure via dedicated per-subscriber buffered channel (slow subscribers do not block fast ones — spec requirement)
  - Background eviction goroutine: runs periodically (default 60s) per channel against configured retention
- `internal/service/subscriber.go` — subscriber registry (channel-keyed map of active subscriber channels, with mutex); each subscriber gets an independent buffered channel so slow subscribers never block publishers or other subscribers
- Tests: `eventbus_server_test.go` covering publish/subscribe round-trip, replay from sequence, multi-subscriber fan-out, filter matching (event_type and law_id against comma-separated attributes), retention eviction, `SEQUENCE_EXPIRED` error when replay beyond retention window, slow-subscriber isolation

**Infra**:
- `Dockerfile`
- `deployment.yaml`
- Add `eventbus` to `go.work`

**Error codes** to handle:
- `SERVICE_UNAVAILABLE` (gRPC `UNAVAILABLE`) — Bus unreachable
- `SEQUENCE_EXPIRED` (gRPC `OUT_OF_RANGE`) — replay requested beyond retention window

---

## Phase 2: Friction Ledger

**Goal:** Create the Friction Ledger as a new standalone service. The existing `monitor/` service continues to run unchanged during this phase — no existing code is deleted or broken.

### New files

**Proto** (`proto/flow/v1/frictionledger.proto`):
- Service: `FrictionLedgerService`
  - `rpc QueryFriction(QueryFrictionRequest) returns (QueryFrictionResponse)` — direct gRPC query surface
- Messages (new in this file, duplicating the query contract):
  - `QueryFrictionRequest`: contains `FrictionFilter`
  - `QueryFrictionResponse`: `repeated FrictionAggregate friction_aggregates`
  - `FrictionFilter`: `law_id`, `node_id`, `workitem_id`, `tier` (LawTier), `time_range` (TimeRange)
- Note: `FrictionAggregate`, `LawTier`, and `TimeRange` remain in `common.proto` where they already live. `FrictionFilter` is currently in `monitor.proto` (lines 69-75) — define a copy in `frictionledger.proto` so the Friction Ledger proto is self-contained. The `monitor.proto` copy is left untouched (removed later in Phase 3).
- Run `buf generate`

**Service** (`frictionledger/`):
- `go.mod` — new module `github.com/gideas/flow/frictionledger`
- Add `frictionledger` to `go.work`
- `cmd/main.go` — gRPC server on port 50057; reads `FRICTION_LEDGER_DB_PATH`, `FRICTION_LEDGER_PORT`, `EVENT_BUS_ADDRESS`, friction threshold env vars per tier (`FRICTION_THRESHOLD_TIER1` through `FRICTION_THRESHOLD_TIER5`, float64 values parsed from string)
- `internal/store/sqlite/store.go`:
  - Tables: `friction_events`, `friction_event_laws` (same schema as current `monitor/internal/store/sqlite/store.go`)
  - `subscriber_checkpoint` table: `channel TEXT PK`, `last_sequence INTEGER` — persists Event Bus replay position
  - `AddFriction(event, lawIDs)` — transactional insert (same as current monitor store)
  - `QueryFriction(filter)` — dynamic SQL aggregation grouped by (law_id, node_id, workitem_id) with per-topology-path grouping axis added alongside per-law, per-node, per-tier
  - `GetCheckpoint(channel)` / `SetCheckpoint(channel, sequence)` — replay position management
- `internal/service/frictionledger_server.go`:
  - `QueryFriction` RPC — same logic as current `monitor/internal/service/monitor_server.go:QueryFriction`
  - Startup: read `last_sequence` from checkpoint; subscribe to Event Bus telemetry channel with that sequence
  - Event loop goroutine: receive `FlowEvent` from Event Bus subscription stream; if `event_type == "friction"`, parse `law_ids` (comma-separated) and `magnitude` (string to double) from attributes, persist to friction store, update checkpoint
  - Threshold evaluator: after each friction write, query per-law accumulated friction; compare against configured tier thresholds; if crossed, call `eventbus.Publish` to friction channel with threshold-crossing event
    - Threshold-crossing event attributes: `law_id`, `tier`, `accumulated_friction`, `threshold`
    - `event_type = "friction.threshold_crossed"`
  - Reconnection: on Event Bus disconnect, retry with exponential backoff; resume from checkpoint on reconnect
- Tests: aggregation correctness, threshold-crossing detection and publication, replay recovery from checkpoint, `QueryFriction` filtering, reconnection behaviour

**Infra**:
- `Dockerfile`
- `deployment.yaml`

**No changes to existing files in this phase.** The old `monitor/` service and all sidecar/SDK code remain functional. The Friction Ledger runs alongside the existing monitor as a parallel service.

---

## Phase 3: Sidecar & SDK Rewiring + Monitor Refactor (Atomic Cut-Over)

**Goal:** In a single phase, rewire the Sidecar and SDK to use the Event Bus (for telemetry/friction publishing) and Friction Ledger (for queries), refactor the Flow Monitor into a stateless pipeline adapter, and remove the old monitor ingestion paths. This is done atomically to avoid broken intermediate states.

**Critical ordering note:** The old Phases 2-3-4 were split across three phases, which would leave the system in a broken compile state between phases (Monitor RPCs removed but Sidecar still calling them). This phase merges all cut-over work into one deliverable.

### Proto changes

**`proto/flow/v1/monitor.proto`** — delete the entire file. The Flow Monitor has no gRPC API per spec (Service Invariant #16). All messages that lived only in `monitor.proto` are handled as follows:
- `AddFrictionRequest`, `AddFrictionResponse` — move to `proto/flow/v1/sidecar.proto` (the Sidecar still needs these types as its internal translation surface for the `AddFriction` and `RecordTelemetry` RPCs it exposes to nodes)
- `RecordTelemetryRequest`, `RecordTelemetryResponse` — move to `proto/flow/v1/sidecar.proto`
- `QueryFrictionRequest`, `QueryFrictionResponse`, `FrictionFilter` — already duplicated in `frictionledger.proto` from Phase 2; delete the `monitor.proto` copies
- `FlowMonitorService` — deleted

**`proto/flow/v1/sidecar.proto`** — add two new RPCs to `SidecarService`:
- `rpc AddFriction(AddFrictionRequest) returns (AddFrictionResponse)` — Sidecar translates to Event Bus publish
- `rpc RecordTelemetry(RecordTelemetryRequest) returns (RecordTelemetryResponse)` — Sidecar translates to Event Bus publish

This preserves the existing SDK call surface (`client.AddFriction`, `client.RecordTelemetry`) while internally routing to the Event Bus. The Sidecar is the translation boundary — the SDK never builds `FlowEvent` messages.

Run `buf generate`.

### New sidecar files

**`sidecar/internal/proxy/eventbus.go`** — `EventBusProxy`:
- Wraps `FlowEventBusServiceClient`
- `PublishFriction(ctx, flowID, workitemID, nodeID string, lawIDs []string, magnitude double)` — builds `FlowEvent` with `channel=TELEMETRY`, `event_type="friction"`, serialises `law_ids` and `magnitude` into `attributes` map, calls `eventbus.Publish`
- `PublishTelemetry(ctx, flowID, nodeID, workitemID, eventType string, payload []byte)` — builds `FlowEvent` with `channel=TELEMETRY`, uses caller's `event_type`, calls `eventbus.Publish`
- Tests in `eventbus_test.go`

**`sidecar/internal/proxy/frictionledger.go`** — `FrictionLedgerProxy`:
- Wraps `FrictionLedgerServiceClient`
- `QueryFriction` passthrough to upstream Friction Ledger
- Tests in `frictionledger_test.go`

**`sidecar/internal/buffer/telemetry_buffer.go`** — async priority buffer:
- Bounded dual-channel buffer (configurable size, default 1000 events per channel)
- Separate HIGH priority channel (friction events) and NORMAL priority channel (custom telemetry)
- `Submit(event, priority)` — non-blocking; callers never block on telemetry delivery
- Background goroutine drains both channels, HIGH first, and publishes to Event Bus via `EventBusProxy`
- **Retry with exponential backoff** when Event Bus is unavailable (spec: "Sidecars buffer events locally and retry", `specs/04-sdk/06-sdk-telemetry.md` lines 120-128)
- When NORMAL buffer is full, drops oldest NORMAL events. Friction events (HIGH) are never dropped until HIGH buffer also full.
- Emits `dropped_telemetry_total` counter metric when dropping
- Tests in `telemetry_buffer_test.go`: priority ordering, drop semantics, retry on unavailable, non-blocking submission

### Sidecar changes

**`sidecar/internal/proxy/monitor.go`** — **deleted** (MonitorProxy replaced by EventBusProxy + FrictionLedgerProxy)
**`sidecar/internal/proxy/monitor_test.go`** — **deleted**

**`sidecar/internal/proxy/librarian.go`**:
- Change constructor: `NewLibrarianProxy(librarianAddr string, eventBusProxy *EventBusProxy)` — accepts EventBusProxy instead of monitor address string
- `Cite()` friction emission: replace `monitorClient.AddFriction(...)` with `eventBusProxy.PublishFriction(...)`
- Remove `monitorClient` and `monitorConn` fields
- Update `Close()` to not close monitor connection
- Update `librarian_test.go` accordingly

**`sidecar/internal/service/sidecar_server.go`**:
- Implement `AddFriction` and `RecordTelemetry` handlers on the SidecarServer (since they are now on `SidecarService` in the proto)
- `AddFriction`: enforce `WRITE:friction` capability (moved from MonitorProxy), inject identity from session metadata, submit to telemetry buffer with HIGH priority
- `RecordTelemetry`: inject identity from session metadata, submit to telemetry buffer with NORMAL priority

**`sidecar/cmd/main.go`**:
- New env vars: `EVENT_BUS_ADDRESS`, `FRICTION_LEDGER_ADDRESS`
- Remove: `MONITOR_ADDRESS`, `envMonitorAddress`
- Wire `EventBusProxy` (connects to Event Bus)
- Wire `FrictionLedgerProxy` (connects to Friction Ledger, registered as `FrictionLedgerServiceServer`)
- Create telemetry buffer, pass to SidecarServer and LibrarianProxy
- Remove `FlowMonitorService` registration
- Remove `monitorCloser`
- Pass `EventBusProxy` to `NewLibrarianProxy` instead of `monitorAddr`

### SDK changes (`sdk/go/client.go`)

- Replace `Monitor flowv1.FlowMonitorServiceClient` with `FrictionLedger flowv1.FrictionLedgerServiceClient`
- `RecordTelemetry` convenience method: calls `c.Sidecar.RecordTelemetry(...)` — the SDK calls the Sidecar, which wraps in a FlowEvent and publishes to Event Bus. The SDK does NOT build FlowEvent messages directly (spec: "the Sidecar wraps every telemetry event in a standard envelope", `specs/04-sdk/06-sdk-telemetry.md` lines 21-29).
- `AddFriction` (if exposed): calls `c.Sidecar.AddFriction(...)` — same pattern
- `QueryFriction` convenience method: calls `c.FrictionLedger.QueryFriction(...)`
- Remove `NewFlowMonitorServiceClient` initialisation
- Add `NewFrictionLedgerServiceClient` initialisation
- `Sidecar` client already exists; `AddFriction`/`RecordTelemetry` RPCs are now on it
- Update all node code that references `c.Monitor` (search for `Monitor` field usage across `nodes/`)
- Update `sdk/go/client_test.go`

### Monitor refactor (`monitor/`)

**Complete rewrite** — the monitor becomes a stateless pipeline adapter:

- **Delete**: `internal/store/` (entire directory — no persistence)
- **Delete**: `internal/service/monitor_server.go` and `monitor_server_test.go`
- **Delete**: `internal/store/sqlite/store.go` and `store_test.go`
- **New**: `internal/subscriber/telemetry.go` — subscribes to Event Bus telemetry channel; transforms events into Prometheus metrics (counters for friction magnitude by law/node/tier, histograms for `foundry.cost.llm` events, gauges for custom metrics)
- **New**: `internal/subscriber/audit.go` — subscribes to Event Bus audit channel; serialises each `FlowEvent` as a JSON Line and writes to stdout
- **New**: `internal/metrics/metrics.go` — Prometheus metric definitions using `github.com/prometheus/client_golang/prometheus` and `promauto`
- **Rewrite**: `cmd/main.go`:
  - No gRPC server (the Flow Monitor has no gRPC API)
  - Starts HTTP server on port 2112 (Prometheus convention) serving `/metrics`
  - Starts Event Bus subscriber goroutines for telemetry and audit channels
  - Each subscriber persists its `last_sequence` to a checkpoint file (see Data Type Conventions — Flow Monitor replay). On startup, reads checkpoint and replays from that sequence if within retention window; otherwise starts live-only.
  - Reads `EVENT_BUS_ADDRESS`, `FLOW_MONITOR_PORT`, `FLOW_MONITOR_CHECKPOINT_PATH`
- Tests: metric registration, JSON Lines formatting, subscriber filter logic, checkpoint persistence and recovery

### Sidecar mediation metrics note

The Sidecar currently emits its own operational metrics (mediation outcomes, latencies). These metrics are Sidecar-internal and do not route through the Monitor. After this phase, the Sidecar continues to emit its own metrics independently. If the Sidecar needs to publish its metrics to the Event Bus telemetry channel for Flow Monitor export, that can be added as a follow-up — the spec does not mandate a specific path for Sidecar-internal metrics.

---

## Phase 4: Audit Event Publishing + Librarian Hearing Triggers

**Goal:** Each authoritative service publishes audit events to the Event Bus audit channel. The Librarian subscribes to the friction channel for hearing triggers and implements review-TTL-expiry triggers.

### Archivist (`archivist/`)

Add Event Bus client (connect to `EVENT_BUS_ADDRESS` env var). Publish to audit channel at:
- Artefact version creation: `event_type="audit.artefact.version_created"`, attributes: `action="version_created"`, `resource_id=<artefact_id>`
- Stamp application: `event_type="audit.artefact.stamped"`, attributes: `action="stamped"`, `resource_id=<artefact_id>`, `stamp_name=<name>`
- Feedback state transitions (add, resolve, refuse, accept, reject, deadlock, link-ruling): `event_type="audit.artefact.feedback.<action>"`, attributes: `action=<action>`, `resource_id=<feedback_id>`

### Operator (`operator/`)

Add Event Bus client. Publish to audit channel at:
- Workitem lifecycle transitions (Pending->Running, Running->Routing, Routing->Completed, ->Failed): `event_type="audit.workitem.<transition>"`
- Routing decisions: `event_type="audit.workitem.routed"`, attributes: `routing_type`, `target`
- Contract evaluations (pass/fail): `event_type="audit.workitem.contract.<pass|fail>"`

### Librarian (`librarian/`)

Add Event Bus client. Publish to audit channel at:
- Law creation: `event_type="audit.law.created"`, attributes: `resource_id=<law_id>`, `tier`
- Law retirement: `event_type="audit.law.retired"`, attributes: `resource_id=<law_id>`
- Law integration: `event_type="audit.law.integrated"`, attributes: `resource_id=<law_id>`, `outcome`

**Friction channel subscription** (hearing triggers):
- On startup, subscribe to Event Bus friction channel with last-seen sequence from persisted checkpoint
- On receiving `event_type="friction.threshold_crossed"`: call `operator.CreateHearingWorkitem` with the `law_id` from event attributes
- On startup/reconnection: call `frictionledger.QueryFriction` to catch up on any thresholds crossed during the disconnection window
- Persist checkpoint after each processed event

**Review-TTL-expiry triggers** (spec: `specs/02-flow/04-system-services.md` lines 180-181):
- Background goroutine periodically scans all laws in the Library
- For each law, compare its age against its tier's configured review TTL (from FoundryFlow governance policy, loaded from env vars or Operator API)
- When a law's age exceeds its tier's review TTL: call `operator.CreateHearingWorkitem` with the `law_id`
- The law remains active during the hearing — expiry is the trigger, not a demotion event
- Track which laws have already had hearings triggered (de-duplicate) to avoid re-triggering for the same expiry event

---

## Phase 5: Operator Provisioning & Configuration

**Goal:** The Operator provisions Event Bus, Friction Ledger, and Flow Monitor as Control Plane infrastructure alongside every Flow.

### Operator changes

`operator/api/v1/foundryflow_types.go`:
- Add `EventBusConfig` struct and field on `FoundryFlowSpec` (additive change alongside existing `GovernancePolicy` which already has `FrictionThresholds` and `ReviewTTLs`):
  ```go
  type EventBusConfig struct {
      Retention EventBusRetention `json:"retention,omitempty"`
  }
  type EventBusRetention struct {
      TelemetryDuration string `json:"telemetryDuration,omitempty"` // Go duration, e.g. "24h"
      TelemetrySize     string `json:"telemetrySize,omitempty"`     // Byte count, e.g. "100MB"
      AuditDuration     string `json:"auditDuration,omitempty"`     // e.g. "168h" (7 days)
      AuditSize         string `json:"auditSize,omitempty"`         // e.g. "1GB"
      FrictionDuration  string `json:"frictionDuration,omitempty"`  // e.g. "72h"
      FrictionSize      string `json:"frictionSize,omitempty"`      // e.g. "100MB"
  }
  ```

`operator/internal/controller/foundryflow_controller.go`:
- In the reconcile loop, ensure Event Bus, Friction Ledger, and Flow Monitor Deployments and Services exist in the Flow namespace
- Pass retention config to Event Bus as env vars on the Deployment
- Pass friction thresholds (from existing `GovernancePolicy.FrictionThresholds` CRD fields, which are `float` values) to Friction Ledger as env vars (float64 string format, e.g. `100.0`)
- Pass review TTLs to Librarian for TTL-expiry triggers

### Backup scope (spec: `specs/02-flow/04-system-services.md` lines 429-449)

Document and implement backup boundaries for the new services:
- Event Bus SQLite log: service-owned backup process. Event data within retention window.
- Friction Ledger SQLite aggregation store + checkpoint: service-owned backup process.
- Recovery ordering: Event Bus restored before Friction Ledger (Friction Ledger replays from checkpoint).

### Helm chart updates (`charts/`)

- Add Event Bus, Friction Ledger, and Flow Monitor as sub-charts or template additions
- Wire `EVENT_BUS_ADDRESS`, `FRICTION_LEDGER_ADDRESS` into Sidecar Deployment templates
- Remove `MONITOR_ADDRESS` from Sidecar Deployment templates

---

## Key Spec References

- `specs/02-flow/04-system-services.md` — primary definition of all three services and their invariants
- `specs/05-reference/grpc-api.md` — Event Bus and Friction Ledger API contracts
- `specs/04-sdk/06-sdk-telemetry.md` — SDK telemetry contract, non-blocking emission, buffer backpressure, and retry requirements
- `specs/02-flow/05-configuration.md` — Event Bus per-channel retention configuration
- `specs/03-node/01-sidecar.md` — Sidecar telemetry buffering and identity injection

## Proto File Map (target state)

| File | Service | Purpose |
|------|---------|---------|
| `common.proto` | (shared) | Enums, shared messages incl. `FrictionAggregate`, `TimeRange`, `LawTier` |
| `node.proto` | NodeService | Node assignment (unchanged) |
| `sidecar.proto` | SidecarService | Heartbeat, timer, **AddFriction, RecordTelemetry** (Sidecar-mediated ingestion) |
| `operator.proto` | OperatorService | Workitem lifecycle (unchanged) |
| `archivist.proto` | ArchivistService | Artefact lifecycle (unchanged) |
| `librarian.proto` | LibrarianService | Law lifecycle (unchanged) |
| `eventbus.proto` | FlowEventBusService | **NEW** — Publish, Subscribe |
| `frictionledger.proto` | FrictionLedgerService | **NEW** — QueryFriction |
| `monitor.proto` | **DELETED** | Flow Monitor has no gRPC API |
| `jury.proto` | JuryService | Deliberation (unchanged) |
| `clerk.proto` | ClerkService | Law drafting (unchanged) |
| `queue.proto` | QueuePeerService | HITL queue (unchanged) |

## Service Port Map (target state)

| Service | Port | Protocol |
|---------|------|----------|
| Sidecar | 50051 | gRPC |
| Operator (gRPC) | 50052 | gRPC |
| NodeService (SDK server) | 50053 | gRPC |
| Archivist | 50054 | gRPC |
| **Flow Event Bus (NEW)** | 50056 | gRPC |
| **Friction Ledger (NEW)** | 50057 | gRPC |
| Librarian | 50058 | gRPC |
| Jury | 50059 | gRPC |
| Clerk | 50060 | gRPC |
| Flow Monitor (was gRPC; now HTTP) | 2112 | HTTP (`/metrics`) |
