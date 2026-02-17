# Operator Reconciliation Logic

> Status: Draft Implementation Contract

This document specifies the Foundry Flow Operator reconciliation loop, state machines, and failure handling.

## Workitem State Machine
States: `Pending` → `Assigned` → `Running` → (`Completed` | `Failed`)

Transitions:
- `Pending`→`Assigned`: Assignment algorithm selects target node.
- `Assigned`→`Running`: Sidecar delivers workitem to node runtime.
- `Running`→`Completed`: Node calls `CompleteWorkitem` with route/complete.
- `Running`→`Failed`: Timeout, thrash guard, or explicit failure.

## Assignment Algorithm (Overview)
- Filter nodes by required `targetRole` or direct `target`.
- Respect `concurrency` limits and `defaultConcurrency` from Helm values.
- Prefer nodes with lowest current load; tie-break by recent success rate.

## Reconciliation Loop Pseudocode
```
for each Workitem where status in {Pending, Assigned, Running}:
  ensure finalizer present
  if status == Pending:
    target = selectTarget(workitem)
    if target == none:
      setCondition(NO_AVAILABLE_TARGET)
      continue
    assign(workitem, target)
  if status == Assigned:
    if deliveryExpired(workitem):
      requeue(workitem)
  if status == Running:
    if timeoutExceeded(workitem):
      fail(workitem, TIMEOUT)
    if visitsExceeded(workitem):
      fail(workitem, THRASH_GUARD)
```

## Failure Handling
- Delivery expiry requeues to avoid stuck `Assigned` state.
- Node crash detection via pod health; session drained and workitem requeued.
- Concurrent updates protected via optimistic locking on `resourceVersion`.

## Leader Election
- Operator uses Kubernetes leader election (Lease resource) for HA.
- Only the leader performs reconciliation; others standby.

## Contract Enforcement Timing
- `terminalContract` is checked synchronously during `CompleteWorkitem`.
- Violations return `TERMINAL_CONTRACT_VIOLATED` with `ContractDetails`.

