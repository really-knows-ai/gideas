# Atomic Foundry Flow: System Protocols (gRPC)

## 5.1 Port Allocation

| Port | Purpose | Access |
|------|---------|--------|
| 35697 | Node ↔ Sidecar gRPC (Session API) | Node container only (localhost) |
| 35698 | System Services gRPC (Archivist, Telemetry, Librarian) | Sidecars only (mTLS) |
| 35699 | Health endpoints (HTTP) | Kubelet only |

## 5.2 The Archivist Contract (Internal)

Standardized on **Port 35698 (SYSTEM)**. Only accessible by Sidecars.

```protobuf
service ArchivistService {
  rpc Store(stream Chunk) returns (StoreReceipt);
  rpc Fetch(FetchRequest) returns (stream Chunk);
  rpc Prune(PruneRequest) returns (Empty);

  // Replay law mutations since a specific sequence ID
  rpc StreamLogs(LogRequest) returns (stream LawUpdate);
}

message LogRequest {
  int64 since_sequence_id = 1;
}
```

## 5.3 The Telemetry Contract (Producer & Consumer)

### The Producer API (Sidecar → Router)

```protobuf
service FlowMonitor {
  rpc RecordTelemetry(TelemetryEvent) returns (Empty);
  rpc Subscribe(ProcessorRegistration) returns (stream TelemetryEvent);
}
```

### Event Type Conventions

| Type | Purpose | Tags |
| :--- | :--- | :--- |
| `foundry.cost.llm` | LLM token usage and cost attribution | `model`, `phase`, `round`, `feedback_id`, `juror` |
| `foundry.friction.report` | Application friction signal | `law_id`, `check_type`, `model`, + arbitrary |
| `foundry.legal.citation` | Law usage (Compliance vs. Resistance) | `law_id` |
| `foundry.governance.escalation` | Economic signal for HITL (Planned vs. Unplanned) | `escalation_type` |
| `foundry.system.log` | Application logs (dual-written to stdout) | `level`, `source` |
| `foundry.system.legal_update` | Real-time propagation of Law state changes | `sequence_id`, `merkle_root` |
