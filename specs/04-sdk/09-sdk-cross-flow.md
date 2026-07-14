# SDK Cross-Flow

The Cross-Flow SDK surface describes how node implementers interact with cross-flow transfer from the SDK perspective. The full protocol — Embassy architecture, trust topologies, Treaty CRDs, and wire-level transfer semantics — is defined in [Cross-Flow Collaboration](../02-flow/06-cross-flow.md). This document covers the node-facing surface only.

All Cross-Flow clients (`EntryClient`, `FederationClient`, `EmbassyClient`) follow
the same no-`ctx` pattern as the core SDK: no `context.Context` appears in any
user-facing method signature. Streaming operations return watcher objects with
`Recv()/Stop()` lifecycle, not channels.

## Embassy Overview

The [Embassy](../02-flow/06-cross-flow.md#embassy) is the standard cross-flow boundary node, present in every Flow. It handles outbound export and inbound import. Node handlers do not call Embassy methods directly — outbound transfer happens by routing a Workitem to Embassy, and inbound transfer is materialised before any node sees the resulting Workitem.

## Export: `law-petition` Handoff

When a node (for example [law-applicator](../01-concepts/02-foundry-cycle.md#law-applicator)) determines that work must be exported to another Flow, it does not invoke an export API. Instead, it routes the Workitem to Embassy and Embassy handles the rest:

1. The node stores the petition artefact and any required stamps via standard [Artefact SDK](./02-sdk-artefacts.md) operations.
2. The node routes the Workitem to the Embassy boundary node.
3. Embassy validates the artefacts and stamps required for export, creates a signed manifest, and sends it to the receiving Flow's Embassy.
4. The local Workitem completes after Embassy has successfully handed off the package according to the configured cross-flow policy.

The node does not specify a destination Flow, target Embassy, or import type. Export routing is Flow-level configuration plus federation or Treaty policy — the node only hands the Workitem to Embassy.

## Import: Staging and Materialisation

When the receiving Embassy accepts an inbound manifest and streams the full package, it performs materialisation before any node handler sees the Workitem:

1. **Workitem creation** — the Embassy creates a new local Workitem in `Pending` state. This is a separate lifecycle object — it is not a continuation of the source Workitem.
2. **Artefact unpacking** — the Embassy stores each artefact from the package into the local [Archivist](../02-flow/04-system-services.md#archivist). Content digests are verified against the manifest inventory.
3. **Naturalisation** — for each required foreign stamp declared in `requireForeignStamps`, the Embassy verifies the stamp's cryptographic chain against the trust source (federation root or Treaty-pinned CA). If verification succeeds, the Embassy applies a local `imported-<stamp>` attestation stamp on the artefact.
4. **Routing** — the Embassy routes the new Workitem according to the resolved effective import-type policy.

By the time a node handler receives the assignment, all artefacts are persisted locally and `imported-*` stamps are applied. The handler uses standard SDK operations with no awareness of the Workitem's foreign origin.

## Naturalisation Semantics

Naturalisation converts foreign provenance into locally attested governance state. What local node code sees after Embassy naturalisation:

- **`imported-*` attestation stamps** appear on artefact passports and behave identically to any locally-applied stamp in SDK queries (`ListStamps`, `HasStamp`, contract evaluation).
- **Foreign stamps** remain attached to the artefact for provenance and audit but are not used by local contracts.
- **Contract evaluation** — whether `imported-*` stamps satisfy local entry or routing contracts is determined by Operator-level contract checks, not by node code. A node that requires a `review` stamp will accept an `imported-review` stamp only if the Flow's contracts are configured to recognise it.

Naturalisation is a per-stamp attestation. The Embassy does not re-sign foreign content — it attests that specific foreign stamps were valid at import time.

## Import Type Registration and Resolution

The receiving Flow publishes `crossFlow.importTypes` on the [FoundryFlow CRD](../05-reference/crds.md#foundryflow) as the flow-authored extension set of import types:

```yaml
crossFlow:
  importTypes:
    external-submission:
      node: intake-triage
      requireForeignStamps: {}
```

Each flow-authored import type maps to an entry-bound node in the receiving Flow. The Operator validates at admission time that each referenced node exists and has an entry binding. Senders target the public import type name, never the receiving Flow's internal node names.

`law-petition` is the only currently defined built-in system import type. It lives in the same effective namespace as flow-authored import types, but it is always present/configured per Flow by the platform and is not declared in YAML. Embassy resolves import types against the merged registry: built-in system import types plus flow-authored `crossFlow.importTypes`.

### How Imported Petitions Enter the Receiving Flow

For `law-petition` imports specifically:

1. The sending Flow's law-applicator routes a petition Workitem to its Embassy boundary.
2. The sending Embassy packages the petition artefact (with its stamps) and sends the manifest to the receiving Embassy.
3. The receiving Embassy validates the manifest, streams the package, materialises the Workitem, and applies `imported-*` stamps.
4. The Workitem routes according to the platform-owned `law-petition` intake policy for the receiving Flow.
5. The receiving node sees a normal Workitem with `imported-approval` and `imported-judiciary-consensus` stamps on the petition artefact. It processes based on artefact content and stamp state.

## EntryClient

`EntryClient` provides operations for entry-bound node logic, available through
`StartEntry` which provides an `EntryFunc(ctx context.Context, client *EntryClient) error`.
The entry function receives a `context.Context` for shutdown signalling — this is
separate from the per-call context that was removed from SDK method signatures.

All `EntryClient` methods accept no `context.Context`:

| Method | Returns | Description |
|--------|---------|-------------|
| `EntryClient.CreateWorkitem(metadata)` | `(string, error)` | Creates a new Workitem with optional metadata. |
| `EntryClient.Subscribe(channel, eventType)` | `(*EventStream, error)` | Opens a streaming subscription to the Event Bus. |
| `EntryClient.QueryLaws(governedArtefact, representationType)` | `([]*flowv1.Law, error)` | Returns laws matching the filter. |
| `EntryClient.RetireDisputeRecord(petitionID)` | `error` | Retires a dispute record by petition ID. |
| `EntryClient.ResumeWorkitem(workitemID)` | `error` | Resumes a suspended workitem by ID. |
| `EntryClient.ListSuspendedWorkitems(conditionContains)` | `([]string, error)` | Returns suspended workitem IDs matching the condition filter. |

`EventStream` supports `Recv()/Stop()` lifecycle:

```go
func (s *EventStream) Recv() (*flowv1.FlowEvent, error)
func (s *EventStream) Stop()
```

## FederationClient

`FederationClient` provides SDK helpers for the Federation service RPCs. All methods
accept no `context.Context`:

| Method | Returns | Description |
|--------|---------|-------------|
| `FederationClient.GetPetitionTarget(scope)` | `(*PetitionTarget, error)` | Returns the authority Flow identity and Embassy endpoint for a petition scope. |
| `FederationClient.DiscoverEndpoints(stateFilter)` | `([]*FlowEndpoint, error)` | Returns Flow endpoints within the federation. |
| `FederationClient.SubmitPublication(law, sourceFlowIdentity)` | `error` | Submits a local Tier 3 law for publication admission. |
| `FederationClient.SubscribeLawUpdates(subscriberFlowIdentity)` | `(*LawUpdateWatcher, error)` | Opens a streaming subscription for published law distribution events. |
| `FederationClient.SubscribePetitionOutcomes(subscriberFlowIdentity)` | `(*PetitionOutcomeWatcher, error)` | Opens a streaming subscription for petition outcome events. |

Watcher types support `Recv()/Stop()`:

```go
type LawUpdateWatcher struct { /* unexported */ }
func (w *LawUpdateWatcher) Recv() (*flowv1.PublishedLawEvent, error)
func (w *LawUpdateWatcher) Stop()

type PetitionOutcomeWatcher struct { /* unexported */ }
func (w *PetitionOutcomeWatcher) Recv() (*flowv1.PetitionOutcomeEvent, error)
func (w *PetitionOutcomeWatcher) Stop()
```

## EmbassyClient

`EmbassyClient` provides SDK helpers for the Embassy transfer protocol. All methods
accept no `context.Context`:

| Method | Returns | Description |
|--------|---------|-------------|
| `EmbassyClient.PreflightManifest(manifest, remoteFlowIdentity)` | `(*PreflightManifestResponse, error)` | Validates a transfer manifest before package streaming. |
| `EmbassyClient.StreamPackage(packageChunks)` | `(*StreamPackageResponse, error)` | Sends a package stream to the receiving Embassy. |
| `EmbassyClient.ExportPackage(workitemID, importType)` | `(*EmbassyExportStream, error)` | Starts a package export stream. |

`EmbassyExportStream` supports `Recv()/Stop()` for reading exported package chunks:

```go
func (s *EmbassyExportStream) Recv() (*flowv1.PackageChunk, error)
func (s *EmbassyExportStream) Stop()
```

## SDK Surface Boundaries

The Cross-Flow SDK surface is deliberately thin:

- **No export API** — export is triggered by routing to Embassy, not by an SDK method.
- **No import API** — import materialisation is Embassy-internal. Node handlers receive normal Workitem assignments.
- **No destination targeting** — nodes do not specify remote Flows, Embassies, or import types. Export routing is Flow-level configuration.
- **No foreign stamp inspection** — local code uses `imported-*` attestation stamps. Foreign stamps are available for audit through artefact passport inspection but are not part of the standard SDK query surface.

The node implementer's responsibility is limited to: handing export work to Embassy through routing, and processing imported Workitems (which appear as normal assignments) for import.

## Cross-Flow SDK Invariants

1. Export is triggered by routing a Workitem to Embassy, not by an SDK method. The Embassy handles packaging and transfer.
2. Import materialisation is Embassy-internal. Node handlers see normal Workitem assignments with locally-attested `imported-*` stamps.
3. Embassy resolves imports against one effective namespace composed of built-in system import types plus flow-authored `crossFlow.importTypes`. Senders target public import type names, never internal node names.
4. `law-petition` is the only currently defined built-in system import type. The Flow Engineering Team does not declare it in YAML.
5. Naturalisation applies `imported-<stamp>` attestation stamps for each verified foreign stamp. Foreign stamps remain for provenance.
6. Local contracts evaluate `imported-*` stamps at the Operator level, not in node code.
7. Node handlers do not specify destination Flows, target Embassies, or import types.
8. All `EntryClient`, `FederationClient`, and `EmbassyClient` methods accept no `context.Context`. Streaming returns watcher objects with `Recv()/Stop()` — no channel-based patterns.
9. The `EntryFunc` signature `func(ctx context.Context, client *EntryClient) error` receives a `context.Context` for shutdown signalling, not per-call gRPC context. This is distinct from the removed per-method `context.Context` parameters.
