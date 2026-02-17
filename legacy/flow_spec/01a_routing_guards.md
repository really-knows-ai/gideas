# Atomic Foundry Flow: Routing Guards

## 1. The Entry Guard (Contract-Based Validation)

The Entry Guard validates **all** new Workitems regardless of creation source:

**Workitem Creation Sources:**

1. **Ingress Node Pattern (Primary):** A gateway node exposes HTTP/REST/GraphQL, translates external requests to `CreateWorkitem()` SDK calls. The Sidecar creates the Workitem CRD.

2. **Direct CRD Creation:** `kubectl apply -f workitem.yaml` or direct Kubernetes API calls. Used for debugging, admin tasks, or CI/CD smoke tests.

3. **Federation:** Sibling flows inject Workitem CRDs directly via the Governance Operator, subject to RBAC permissions from the Annexation handshake.

**Validation Logic:**

```
1. Verify Workitem.spec.type is in FoundryFlow.spec.entryContract.workitemTypes[]
2. For each requirement in FoundryFlow.spec.entryContract.requiredArtefacts[]:
   a. Find artefact in Workitem.status.artefacts[] where kind == requirement.kind
   b. REJECT if artefact not found
   c. Validate artefact meets requirement.state:
      - If state == "present": Check exists (already found in step 2a)
      - If state == "valid": Fetch GovernedArtefact, check all requiredRoles are stamped
3. Select entry node from Flow topology
4. Set Workitem.status.currentAssignee = entryNode
5. Set Workitem.status.state = "Pending"
```

---

## 2. The Router Guard

When a routing instruction is written to a Workitem, the Operator resolves and validates:

```
1. Read Workitem.status.routingInstruction
2. If type == "route_to_output":
   a. Fetch FoundryNode CRD for previous assignee
   b. Find output in FoundryNode.spec.outputs[] where name == targetOutput
   c. REJECT if output not found (INVALID_OUTPUT)
   d. Resolve targetRole to candidate nodes
   e. If no nodes exist with that role: FAIL with NO_AVAILABLE_TARGET
  • See Node Selection in assignment_flow for the distribution policy across ready replicas.
3. If type == "route_to":
   a. Verify target node exists
   b. REJECT if not found (NODE_NOT_FOUND)
4. If type == "complete":
   a. Delegate to Terminal Guard (see below)
5. Query target node's /readyz endpoint
6. If not ready: leave Workitem in queue (currentAssignee empty, state Pending)
7. If ready:
   a. Set Workitem.status.currentAssignee = targetNode
   b. Clear Workitem.status.routingInstruction
   c. Workitem stays state = "Pending" (Sidecar will set to Running)
```

### 2.1 Two-Phase Validation Model

Routing validation is split between Sidecar (sync) and Operator (async):

| Phase | Component | Validates | Error Behavior |
|-------|-----------|-----------|----------------|
| **Phase 1 (Sync)** | Sidecar | Output name exists in local config | Immediate error to Node |
| **Phase 2 (Async)** | Operator | Target role has available nodes | Workitem marked `Failed` |

**Implication:** A Node receives success from `CompleteWorkitem` as long as the output name is valid. If the target role has no ready nodes, the Node is **oblivious** - the failure happens asynchronously after the handler has completed.

**Failure Reason:** `NO_AVAILABLE_TARGET` indicates the Operator could not resolve the routing instruction to a ready node.

**Design Rationale:** The Sidecar validates the node's local contract. The Operator validates cluster-wide topology. Requiring the Sidecar to know real-time readiness of every node would introduce distributed state complexity and latency into the local execution loop.

### 2.2 Result Types

Nodes return one of three result types:

| Result Type | Description | Validation |
|-------------|-------------|------------|
| `RouteToOutput(name)` | Route via named output channel | Output must exist in Node's config |
| `RouteTo(nodeName)` | Direct routing to specific node | Node must exist |
| `Complete()` | Signal terminal completion | Terminal Guard validation |

---

## 3. The Terminal Guard (Cryptographic Gatekeeper)

When a Node returns `Complete()`, the Terminal Guard validates the terminal contract:

```
1. Fetch FoundryNode CRD for current node
2. REJECT if FoundryNode.spec.isTerminal != true (NOT_TERMINAL_NODE)
3. Fetch FoundryFlow CRD
4. Find contract in FoundryFlow.spec.terminalContracts[] 
   where name == FoundryNode.spec.terminalContract
5. REJECT if contract not found (INVALID_TERMINAL_CONTRACT)

6. For each requirement in contract.requiredArtefacts[]:
   a. Find artefact in Workitem.status.artefacts[] where kind == requirement.kind
   b. REJECT if artefact not found (ARTEFACT_MISSING)
   
   c. Fetch GovernedArtefact for artefact.kind
   
   d. If requirement.state == "present":
      - Artefact exists (step 6a passed)
      - No stamp validation needed
      - PASS
   
   e. If requirement.state == "valid":
      - For each role in GovernedArtefact.spec.requiredRoles[]:
         i.   Find stamp in artefact.passport[] where stamp.role == role
         ii.  REJECT if stamp not found (VALIDITY_NOT_MET)
         iii. Verify stamp.signature using stamp.certificateChain[]
         iv.  REJECT if signature invalid (SIGNATURE_INVALID)
         v.   Verify certificate chain terminates at trusted State Root
         vi.  REJECT if chain invalid (CERTIFICATE_CHAIN_INVALID)
      - All roles stamped: PASS
   
7. Set Workitem.status.state = "Completed"
8. Set Workitem.status.terminalContract = contract.name
9. Emit foundry.workitem.completed telemetry event
```

### 3.1 Deterministic Validation (Binary Model)

The Terminal Guard logic is **deterministic and fail-fast**:
- If `state: "present"`, only check existence
- If `state: "valid"`, iterate through `requiredRoles` and fail immediately on first missing role
- No map iteration, no "highest level" calculation, no ambiguity

This replaces the previous multi-tier validity model which required iterating through an unordered `validityLevels` map.

### 3.2 Signature Verification (Federated Trust)

In Atomic mode, stamps are verified against the Flow Operator's own certificate.

In Federated mode, stamps may originate from **any Sibling Flow** within the State. The Terminal Guard validates by traversing the certificate chain:

```
Stamp Signature
  └─ Signing Node Certificate (Leaf)
      └─ Issuing Operator Certificate (Intermediate - ANY Sibling)
          └─ State Root Certificate (Trusted by ALL Siblings)
```

See [03_identity_and_federation.md](./03_identity_and_federation.md) for the full certificate hierarchy.

### 3.4 Relationship to Thrash Guard

Routing Guards validate semantic routing decisions and terminal contracts. The Thrash Guard (see timeout_and_thrash) is an infrastructure safety net that fails workitems when `guestbook` visit counts exceed `maxVisits`. They are complementary:
- Routing Guards: sidecar/operator validation of outputs and contracts (semantic).
- Thrash Guard: sidecar enforcement against infinite infrastructure loops.
Precedence: Thrash Guard may preempt routing when max visits are exceeded; otherwise routing guards apply.

### 3.3 Atomic Mode Scope (Sovereign Silo)

- Only stamps from nodes issued by the current Flow Operator are accepted.
- Cross-flow stamps are invalid in v1; validation fails with `SIGNATURE_INVALID`.
- The Operator is the sole trust root in Atomic Mode; there is no State Root.
- Implementation note: Atomic validation MUST reject stamps whose `issuing-operator` does not match the local Operator CA. Certificate-chain traversal is disabled in v1 but issuer mismatch checks still apply.
