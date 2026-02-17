# Sidecar Lifecycle

> Status: Draft Implementation Contract

## Startup Sequence
1. Read injected configuration and capabilities.
2. Establish local gRPC binding on 35697.
3. Wait for node runtime to connect via `NodeRuntime.ProcessWorkitem`.

## Session Lifecycle
- Session created per workitem delivery.
- Context includes `workitem_id`, capabilities, target outputs.
- Heartbeats required to maintain inactivity timeout.

## Workitem Delivery
- Operator assigns → Sidecar receives from System Services → delivers to node.
- Delivery expiry requeues on failure to acknowledge.

## Completion and Export
- `CompleteWorkitem`: validates `terminalContract` synchronously.
- `Export`: marks workitem completed on success and terminates session.

## Error Propagation
- Node errors surfaced via structured details where applicable.
- Infrastructure errors mapped to gRPC codes with Foundry reasons.
