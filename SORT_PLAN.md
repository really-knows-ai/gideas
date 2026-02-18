# Sort Dynamic Topology Discovery Plan

## Problem

`haiku-sort` hardcodes all stamp names, output names, and routing targets as constants. The spec at `specs/03-node/03-patterns.md` explicitly calls this an anti-pattern:

> "Hardcoded stamp-provider routing. Gate logic that routes to a specific node name for a specific stamp instead of discovering the provider from Flow configuration."

The specs reference `READ:flow` capability 10+ times as the mechanism for stamp-to-node mapping discovery, but the RPC to do this doesn't exist anywhere — not in the protos, not in the SDK, not in the Operator.

## Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| RPC service | OperatorService | Operator owns all CRD state (FoundryFlow, FoundryNode, GovernedArtefact). Sidecar proxies it like every other Operator call. |
| Response shape | General topology (all nodes + exit contract) | Reusable by any node with `READ:flow`, not just gate nodes. |
| Stamp ordering | `NODE_ORDER` env var (comma-separated node names) | Explicit, Flow Architect controlled. Passed via FoundryNode CRD container env. |
| Feedback check | Per-stamp-phase via `FeedbackItem.source` | Ties feedback to the stamp phase that generated it, rather than a blanket unresolved check. |
| Approval stamp | Discovered dynamically from own capabilities + exit contract | No hardcoded stamp names at all. Sort finds which stamps it can apply from its own `STAMP:artefact/<kind>/<stamp>` capabilities. |
| Artefact kinds | From the exit contract | Sort processes all artefact kinds listed in its bound exit contract. |
| Routing to refine | Always routes to refine output | Feedback `source` identifies who *left* the feedback, not who can *fix* it. Refine handles all feedback regardless of source. |

## Key Discoveries

### FeedbackItem.source Already Exists

`FeedbackItem.source` (field 2 in `common.proto:130`) is already populated server-side by the Archivist. In `archivist_server.go:402`:

```go
source := extractNodeID(ctx)  // reads x-flow-node-id from gRPC metadata
```

The Sidecar injects `x-flow-node-id` into every request, and the Archivist stores it as `source` on the feedback item. Sort can read `feedback.Source` to know which node left the feedback. The proto field just needs better documentation.

### No READ:flow RPC Exists

The specs promise topology discovery via `READ:flow` but nobody built the contract. The legacy docs reference a `GetFlowConfig` RPC that was never carried forward into the v1 protos.

## Proto Design

### New RPC on OperatorService

Add to `proto/flow/v1/operator.proto`:

```protobuf
// Returns the Flow topology visible to the calling node. Requires READ:flow
// capability. The Sidecar injects node identity; the Operator resolves the
// calling node's outputs, all peer nodes with capabilities, and the bound
// exit contract (if exit-bound).
rpc GetFlowTopology(GetFlowTopologyRequest) returns (GetFlowTopologyResponse);
```

### New Messages

```protobuf
// Empty — identity comes from Sidecar-injected metadata.
message GetFlowTopologyRequest {}

message GetFlowTopologyResponse {
  // The calling node's definition (name, capabilities, outputs).
  FlowNode self = 1;
  // All nodes in the flow, keyed by node name.
  map<string, FlowNode> nodes = 2;
  // The exit contract bound to the calling node (if exit-bound).
  // Map of artefact kind to required stamp names.
  // Empty if the calling node is not exit-bound.
  map<string, StampRequirements> exit_contract = 3;
}

// A node in the flow topology.
message FlowNode {
  string name = 1;
  repeated string capabilities = 2;
  repeated FlowOutput outputs = 3;
}

// A named routing output on a node.
message FlowOutput {
  string name = 1;
  string target = 2;
}

// List of required stamp names for an artefact kind in a contract.
message StampRequirements {
  repeated string stamps = 1;
}
```

## Sort Algorithm (New)

```
topology = client.GetFlowTopology()
nodeOrder = parseNodeOrder(os.Getenv("NODE_ORDER"))  // e.g. ["quench", "appraise"]
exitContract = topology.exit_contract                  // e.g. {"haiku": ["linter", "review", "approval"]}
threshold = readDeadlockThreshold()                    // from DEADLOCK_THRESHOLD env var

// ── Build maps from topology ──────────────────────────────────────────

// stamp_name -> provider_node_name (for each artefact kind)
stampProviders = {}
for each node in topology.nodes:
  for each capability in node.capabilities:
    if capability matches STAMP:artefact/<kind>/<stamp>:
      stampProviders[kind][stamp] = node.name

// target_node_name -> output_name (from self's outputs)
outputRoutes = {}
for each output in topology.self.outputs:
  outputRoutes[output.target] = output.name

// ── For each artefact kind in exit contract ───────────────────────────

for kind, requiredStamps in exitContract:

  // Step 1: Check deadlock FIRST (must precede all other feedback checks)
  // Unchanged from current — scans all feedback items for this artefact.
  if checkDeadlock(ctx, client, kind, threshold):
    route to assay output
    return

  // Step 2: Check stamps in NODE_ORDER
  for each nodeName in nodeOrder:
    // Find what stamps this node provides for this artefact kind
    stamps = stampsProvidedBy(nodeName, kind, stampProviders)
    for each stamp in stamps:
      hasStamp = client.HasStamp(kind, stamp)
      if hasStamp:
        // Stamp present — check for unresolved feedback from this provider
        feedbackItems = client.GetFeedback(kind)
        unresolvedFromProvider = any item where:
          item.source == nodeName AND
          item.state is not RESOLVED and not DEADLOCKED
        if unresolvedFromProvider:
          route to refine
          return
      else:
        // Stamp missing — route to provider node
        outputName = outputRoutes[nodeName]
        route to outputName
        return

  // Step 3: All stamps from NODE_ORDER present, no unresolved feedback
  // Apply any stamps Sort itself provides (e.g. approval)
  myStamps = stampsProvidedBy(self.name, kind, stampProviders)
  for each stamp in myStamps:
    if stamp in requiredStamps:
      client.StampArtefact(kind, stamp)

// Step 4: All governance satisfied — complete
client.Complete()
```

### Algorithm Notes

- **Deadlock check comes first** — per `specs/03-node/03-patterns.md:52`, deadlocked feedback must be checked before the general unresolved-feedback predicate.
- **Per-stamp feedback check**: When a stamp is present, Sort checks if the node that *could* stamp it has left unresolved feedback. This captures the case where e.g. Appraise stamped "review" but also left feedback — the artefact needs refinement before proceeding.
- **Multiple artefact kinds**: The algorithm loops over all artefact kinds in the exit contract. For the haiku flow there's only one (`haiku`), but the design supports multi-artefact flows.
- **Approval is not special**: Sort discovers its own `STAMP` capabilities and applies whatever stamps the exit contract requires that it can provide. In the reference arrangement this happens to be `approval`.

## Work Items

### Phase 1: Proto Changes

1. Add `GetFlowTopology` RPC to `proto/flow/v1/operator.proto`
2. Add request/response messages: `GetFlowTopologyRequest`, `GetFlowTopologyResponse`, `FlowNode`, `FlowOutput`, `StampRequirements`
3. Document `FeedbackItem.source` field in `proto/flow/v1/common.proto` (add comment: "Node identity of the feedback creator. Populated by the Archivist from Sidecar-injected x-flow-node-id metadata.")
4. Regenerate Go code

### Phase 2: Spec Updates

5. Update `specs/05-reference/grpc-api.md` — add `GetFlowTopology` to Operator API Node-Facing Methods table
6. Update `specs/02-flow/05-configuration.md` — reference `GetFlowTopology` as the concrete mechanism for `READ:flow` topology discovery
7. Update `specs/03-node/03-patterns.md` — update gate routing pattern to reference `GetFlowTopology` and the `NODE_ORDER` env var pattern
8. Update `specs/01-concepts/02-foundry-cycle.md` — reference `GetFlowTopology` in Sort description

### Phase 3: Operator Implementation

9. Implement `GetFlowTopology` handler in `operator/internal/rpc/operator_server.go`:
   - Extract `flow_id` and `node_id` from Sidecar-injected gRPC metadata
   - Fetch the FoundryFlow CRD to get exit contracts
   - Fetch all FoundryNode CRDs in the namespace
   - Find the calling node's CRD to get its exit binding → resolve to the exit contract
   - Build and return the `GetFlowTopologyResponse`
10. Add tests in `operator/internal/rpc/operator_server_test.go`

### Phase 4: SDK

11. Add `GetFlowTopology(ctx) (*flowv1.GetFlowTopologyResponse, error)` convenience method to `sdk/go/client.go`
12. Add spy method + test in `sdk/go/client_test.go`

### Phase 5: Sort Refactor

13. Refactor `haiku-sort/main.go`:
    - Remove all hardcoded stamp/output/artefact constants
    - Add `NODE_ORDER` env var parsing (comma-separated node names)
    - Call `GetFlowTopology()` at the start of each handler invocation
    - Build stamp-provider map from node capabilities
    - Build output-routing map from self's outputs
    - New per-stamp-phase routing with `feedback.source` checking
    - Dynamic approval stamp discovery from own capabilities + exit contract
    - `DEADLOCK_THRESHOLD` env var unchanged
14. Refactor `haiku-sort/testutil_test.go` — update `sortSpy` with `GetFlowTopology` support
15. Refactor `haiku-sort/main_test.go` — update all 25 tests for new dynamic logic

### Phase 6: Quality Gates

16. `go test ./...` across all modules
17. `make check-fix` — lint clean

## Environment Variables

| Variable | Source | Default | Description |
|---|---|---|---|
| `NODE_ORDER` | FoundryNode CRD container env | (required) | Comma-separated node names defining stamp-checking order. e.g. `quench,appraise` |
| `DEADLOCK_THRESHOLD` | FoundryNode CRD container env | `3` | Feedback depth at which items are escalated to Assay |

## Relevant Files

### Will Create/Modify

| File | Action | Description |
|---|---|---|
| `proto/flow/v1/operator.proto` | Modify | Add `GetFlowTopology` RPC + messages |
| `proto/flow/v1/common.proto` | Modify | Document `FeedbackItem.source` field |
| `gen/flow/v1/*.go` | Regenerate | Proto codegen output |
| `operator/internal/rpc/operator_server.go` | Modify | Implement `GetFlowTopology` handler |
| `operator/internal/rpc/operator_server_test.go` | Modify | Add handler tests |
| `sdk/go/client.go` | Modify | Add `GetFlowTopology` convenience method |
| `sdk/go/client_test.go` | Modify | Add spy method + test |
| `nodes/haiku-sort/main.go` | Rewrite | Dynamic topology-driven routing |
| `nodes/haiku-sort/main_test.go` | Rewrite | All tests updated for new logic |
| `nodes/haiku-sort/testutil_test.go` | Rewrite | Updated spy with topology support |
| `specs/05-reference/grpc-api.md` | Modify | Add `GetFlowTopology` to API reference |
| `specs/02-flow/05-configuration.md` | Modify | Reference concrete `READ:flow` mechanism |
| `specs/03-node/03-patterns.md` | Modify | Update gate pattern with `GetFlowTopology` |
| `specs/01-concepts/02-foundry-cycle.md` | Modify | Reference `GetFlowTopology` in Sort description |

### Reference (Read-Only)

| File | Why |
|---|---|
| `operator/api/v1/foundryflow_types.go` | CRD types for exit contracts |
| `operator/api/v1/foundrynode_types.go` | CRD types for outputs, capabilities |
| `operator/api/v1/governedartefact_types.go` | CRD types for stamp vocabulary |
| `archivist/internal/service/archivist_server.go` | Confirms `source` = `extractNodeID(ctx)` |
| `specs/04-sdk/04-sdk-feedback.md` | Feedback lifecycle reference |
| `specs/05-reference/crds.md` | CRD field reference |
