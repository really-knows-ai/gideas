# Atomic Foundry Flow: Workitem Assignment Flow

## 1. Overview

The assignment flow uses K8s-native CRD watches for coordination.

### Components

| Component | Role |
|-----------|------|
| **Operator** | Watches Workitems, validates routing, selects nodes, manages state |
| **Sidecar** | Watches Workitems assigned to its node, bridges to Node process |
| **Node** | Processes work async, returns Result via CompleteWorkitem() |

---

## 2. State Machine

```
┌─────────────────────────────────────────────────────────────────────────┐
│ 1. CREATION                                                             │
│    External request → Entry Guard validates                             │
│    Creates Workitem CRD: state=Pending, currentAssignee=entryNode       │
└─────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ 2. SIDECAR PICKS UP                                                     │
│    Watches: Workitem where currentAssignee==myNode AND state==Pending   │
│    Patches state: Pending → Running                                     │
│    Sets /readyz → 503 (busy)                                            │
│    Calls ProcessWorkitem(workitem) → returns Empty (async handoff)      │
└─────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ 3. NODE PROCESSES (async)                                               │
│    Executes handler, makes SDK calls (stamp, cite, heartbeat, etc.)     │
│    Calls CompleteWorkitem(Result) when done                             │
└─────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ 4. SIDECAR VALIDATES & WRITES                                           │
│    Validates: output name exists in Node's outputs config               │
│    Patches Workitem CRD:                                                │
│      routingInstruction: {type, target}                                 │
│      currentAssignee: ""                                                │
│      state: Pending                                                     │
│    Sets /readyz → 200 (ready)                                           │
└─────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ 5. OPERATOR PROCESSES ROUTING                                           │
│    Watches: Workitem where routingInstruction != null                   │
│    Router Guard validates (see 01a_routing_guards.md)                   │
│    Selects next node (ready replicas via /readyz, distribution policy)  │
│    Patches: currentAssignee=nextNode, clears routingInstruction         │
│    → Loop to step 2                                                     │
└─────────────────────────────────────────────────────────────────────────┘
                                    │
                                    ▼
┌─────────────────────────────────────────────────────────────────────────┐
│ 6. TERMINAL                                                             │
│    Node returns Complete()                                              │
│    Terminal Guard validates contract                                    │
│    On pass: state → Completed                                           │
│    On fail: state → Failed                                              │
└─────────────────────────────────────────────────────────────────────────┘
```

---

## 3. State Cycle

Each routing hop follows: `Pending → Running → Pending`

```
Pending → Running → Pending → Running → ... → Completed
                                          └→ Failed
```

---

## 4. Node Selection

When the Operator needs to assign work to a node with multiple replicas:

1. Query all pods for the target node Deployment
2. Check `/readyz` endpoint for each pod
3. Filter to only ready pods (returning 200)
4. Apply distribution policy (Round Robin) across ready pods
5. If no ready pods: Workitem stays queued (`currentAssignee: ""`, `state: Pending`)

### Distribution Policy

The Operator uses **in-memory round-robin** for load distribution. No persistent state is required - if the Operator restarts, it simply resets the counter. Strict ordering is unnecessary for load balancing, and the Thrash Guard provides safety against pathological routing.

**Clarifications:**
- No sticky affinity: repeat assignments to the same role may hit different replicas.
- Not random: selection advances deterministically via the round-robin counter.
- Not first-ready only: all currently ready pods participate in distribution.

---

## 5. Validation Split

| Validator | Checks |
|-----------|--------|
| **Sidecar** | Output name exists in Node's `outputs` config |
| **Operator** | Node exists, entry contract, terminal contract, routing resolution |

### 5.1 Atomic Patching

To reduce etcd churn, all state transitions MUST be performed as a single atomic patch. For example, when routing from one node to the next, the Operator MUST update `state`, `routingInstruction`, and `currentAssignee` in a single API call. 